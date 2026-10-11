package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/farvisun/squi/internal/config"
	"github.com/farvisun/squi/internal/db"
	"github.com/farvisun/squi/internal/db/clickhouse"
	"github.com/farvisun/squi/internal/db/mysql"
	"github.com/farvisun/squi/internal/db/postgres"
	"github.com/farvisun/squi/internal/history"
	"github.com/farvisun/squi/internal/sqltext"
)

type runOpts struct {
	name    string        // result tab name (default "Result N")
	source  *db.ObjectRef // table the rows come from (editable)
	replace *resultPanel  // re-run: replace this panel
	useDB   string        // switch database first (pg cross-db)
	src     string        // editor text the statements came from (error positions)
}

type stmtResult struct {
	stmt     sqltext.Statement
	cols     []db.ResultColumn
	rows     [][]db.Cell
	stream   *openStream
	done     bool
	partial  bool
	tag      string
	affected int64
	elapsed  time.Duration
	err      error
}

type runMsg struct {
	tab     *tab
	opts    runOpts
	results []stmtResult
	total   int
}

// runCurrent runs the selection, or the statement under the cursor.
func (m *Model) runCurrent(t *tab) tea.Cmd {
	src := t.editor.Value()
	d := t.ac.driver()
	if a, b, ok := t.editor.Selection(); ok {
		stmts := sqltext.Split(src[a:b], d)
		for i := range stmts {
			stmts[i].Start += a
			stmts[i].End += a
			stmts[i].Line, stmts[i].Col = sqltext.LineCol(src, stmts[i].Start)
		}
		return m.execute(t, stmts, runOpts{src: src})
	}
	st, ok := sqltext.StatementAt(src, t.editor.CursorOffset(), d)
	if !ok {
		return m.toast("Nothing to run", toastWarn)
	}
	return m.execute(t, []sqltext.Statement{st}, runOpts{src: src})
}

// runAll runs every statement in the editor.
func (m *Model) runAll(t *tab) tea.Cmd {
	src := t.editor.Value()
	return m.execute(t, sqltext.Split(src, t.ac.driver()), runOpts{src: src})
}

// execute applies the safety rules, then runs stmts.
func (m *Model) execute(t *tab, stmts []sqltext.Statement, opts runOpts) tea.Cmd {
	if len(stmts) == 0 {
		return m.toast("Nothing to run", toastWarn)
	}
	if t.busy {
		return m.toast("Tab is busy — ctrl+c cancels", toastWarn)
	}
	cfg := t.ac.cfg
	d := t.ac.driver()
	var destructive []string
	writes := 0
	for _, st := range stmts {
		c := sqltext.Classify(st.SQL, d)
		if !c.IsWrite() {
			continue
		}
		writes++
		if cfg.ReadOnly {
			reason := c.Reason
			if reason == "" {
				reason = strings.ToUpper(c.Keyword) + " modifies data"
			}
			t.out.msgs.log(logError, "Blocked on read-only connection: "+reason+"\n"+abbrev(st.SQL, 200))
			t.out.ix = 0
			return m.toast("Read-only connection: write statement blocked", toastError)
		}
		if c.Destructive {
			destructive = append(destructive, fmt.Sprintf("• %s\n  %s", c.Reason, abbrev(oneLine(st.SQL), 90)))
		}
	}
	proceed := func() tea.Cmd { return m.startRun(t, stmts, opts) }
	if len(destructive) > 0 {
		expect := t.db
		if expect == "" {
			expect = cfg.Title()
		}
		body := fmt.Sprintf("You are about to run on %s:\n\n%s", envLabel(cfg), strings.Join(destructive, "\n"))
		m.push(newTypedConfirm("⚠ Destructive statement", body, expect, proceed))
		return nil
	}
	if writes > 0 && cfg.Env == config.EnvProd && m.settings.ConfirmProdAll {
		m.push(&confirmModal{title: "Write on production", danger: true,
			body:  fmt.Sprintf("Run %d write statement(s) on %s?", writes, envLabel(cfg)),
			onYes: proceed})
		return nil
	}
	return proceed()
}

func envLabel(cfg config.Connection) string {
	if cfg.Env != config.EnvNone {
		return fmt.Sprintf("%s [%s]", cfg.Title(), strings.ToUpper(string(cfg.Env)))
	}
	return cfg.Title()
}

func (m *Model) startRun(t *tab, stmts []sqltext.Statement, opts runOpts) tea.Cmd {
	if opts.replace != nil {
		opts.replace.closeStream()
	} else {
		for _, r := range t.out.results() {
			if r.dirty() {
				return m.toast("Save (ctrl+s) or revert (U) pending edits first", toastWarn)
			}
		}
		for _, r := range t.out.results() {
			r.closeStream()
		}
	}
	t.editor.MarkError(-1)
	fetch := m.settings.FetchSize
	if t.ac.cfg.FetchSize > 0 {
		fetch = t.ac.cfg.FetchSize
	}
	manual := t.manualTx
	hist := m.history
	cfg := t.ac.cfg
	d := t.ac.driver()
	what := "running"
	if len(stmts) > 1 {
		what = fmt.Sprintf("running %d statements", len(stmts))
	}
	return m.op(t, what, func(ctx context.Context, s db.Session) tea.Msg {
		msg := runMsg{tab: t, opts: opts, total: len(stmts)}
		if opts.useDB != "" && opts.useDB != s.CurrentDatabase() {
			if err := s.UseDatabase(ctx, opts.useDB); err != nil {
				msg.results = append(msg.results, stmtResult{err: err})
				return msg
			}
		}
		for i, st := range stmts {
			c := sqltext.Classify(st.SQL, d)
			if manual && !s.InTx() && c.Kind != sqltext.KindTx {
				if err := s.Begin(ctx); err != nil {
					msg.results = append(msg.results, stmtResult{stmt: st, err: err})
					break
				}
			}
			res := runOne(s, st, fetch, i == len(stmts)-1, t)
			if hist != nil {
				e := history.Entry{Time: time.Now(), ConnID: cfg.ID, ConnName: cfg.Title(), Database: s.CurrentDatabase(),
					SQL: st.SQL, DurationMS: res.elapsed.Milliseconds(), Rows: int64(len(res.rows))}
				if len(res.cols) == 0 {
					e.Rows = res.affected
				}
				if res.err != nil {
					e.Error = res.err.Error()
				}
				_ = hist.Add(e)
			}
			msg.results = append(msg.results, res)
			if res.err != nil {
				break
			}
		}
		return msg
	})
}

// runOne executes a statement and fetches the first page. The stream stays
// open (for lazy loading) only for the last statement of a run.
func runOne(s db.Session, st sqltext.Statement, fetch int, last bool, t *tab) stmtResult {
	res := stmtResult{stmt: st}
	ctx, cancel := context.WithCancel(context.Background())
	start := time.Now()
	rs, err := s.Run(ctx, st.SQL)
	if err != nil {
		cancel()
		res.err, res.elapsed = err, time.Since(start)
		return res
	}
	res.cols = rs.Columns()
	n := fetch
	if len(res.cols) == 0 {
		n = 1
	}
	rows, err := rs.Next(n)
	res.elapsed = time.Since(start)
	res.rows = rows
	if errors.Is(err, io.EOF) {
		res.done, err = true, nil
	}
	if err != nil {
		res.err = err
	}
	if res.done || !last || err != nil {
		res.partial = !res.done && err == nil
		_ = rs.Close()
		cancel()
		res.tag, res.affected = rs.CommandTag(), rs.RowsAffected()
		return res
	}
	res.stream = &openStream{rs: rs, cancel: cancel, owner: t}
	return res
}

func (m *Model) handleRun(msg runMsg) tea.Cmd {
	t := msg.tab
	if !m.hasTab(t) {
		for _, r := range msg.results {
			if r.stream != nil {
				r.stream.closeNow()
			}
		}
		return nil
	}
	if msg.opts.replace == nil {
		t.out.removeResults()
	}
	var cmds []tea.Cmd
	var lastPanel panel
	var errs int
	cancelled := false
	resultN := 0
	for _, r := range msg.results {
		if r.err != nil {
			errs++
			text := r.err.Error()
			if r.stmt.SQL != "" {
				text = abbrev(oneLine(r.stmt.SQL), 120) + "\n" + text
			}
			if isCancelled(r.err) {
				cancelled = true
				t.out.msgs.log(logWarn, text)
				continue
			}
			t.out.msgs.log(logError, text)
			if off := errorOffset(r.err, r.stmt, msg.opts.src); off >= 0 && msg.opts.src == t.editor.Value() {
				t.editor.MarkError(off)
			}
			continue
		}
		summary := fmt.Sprintf("%s · %s", abbrev(oneLine(r.stmt.SQL), 80), fmtDuration(r.elapsed))
		if len(r.cols) == 0 {
			tag := r.tag
			if tag == "" {
				tag = "OK"
			}
			if r.affected > 0 && !strings.Contains(tag, fmt.Sprint(r.affected)) {
				tag += fmt.Sprintf(" (%d rows)", r.affected)
			}
			t.out.msgs.log(logOK, tag+" — "+summary)
			continue
		}
		resultN++
		name := msg.opts.name
		if name == "" {
			name = fmt.Sprintf("Result %d", resultN)
			if len(msg.results) == 1 {
				name = "Result"
			}
		}
		rp := newResultPanel(name, r.stmt.SQL, r.cols, r.rows, m.settings.MaxColumnWidth)
		rp.stream, rp.done, rp.partial, rp.elapsed, rp.tag = r.stream, r.done, r.partial, r.elapsed, r.tag
		rp.source = msg.opts.source
		if rp.source == nil {
			rp.source = singleTableSource(r.stmt.SQL, t)
		}
		if old := msg.opts.replace; old != nil {
			rp.name, rp.source, rp.detail, rp.keyIdx = old.name, old.source, old.detail, old.keyIdx
			t.out.replace(old, rp)
		} else {
			t.out.add(rp)
		}
		lastPanel = rp
		more := ""
		if !r.done {
			more = "+"
		}
		t.out.msgs.log(logInfo, fmt.Sprintf("%d%s rows — %s", len(r.rows), more, summary))
	}
	switch {
	case cancelled:
		t.out.ix = 0
		t.lastRun = "cancelled"
		cmds = append(cmds, m.toast("Query cancelled", toastWarn))
	case errs > 0:
		t.out.ix = 0
		t.lastRun = "error"
		cmds = append(cmds, m.toast("Query failed — see Messages", toastError))
	case lastPanel != nil:
		if rp, ok := lastPanel.(*resultPanel); ok {
			t.lastRun = rp.status()
		}
	default:
		t.out.ix = 0
		t.lastRun = fmt.Sprintf("%d statement(s) OK", len(msg.results))
	}
	if errs == 0 && len(msg.results) < msg.total {
		t.out.msgs.log(logWarn, fmt.Sprintf("Stopped after %d of %d statements", len(msg.results), msg.total))
	}
	return tea.Batch(cmds...)
}

// isCancelled reports whether err is a server-side statement cancellation.
func isCancelled(err error) bool {
	var pe *postgres.QueryError
	if errors.As(err, &pe) {
		return pe.Code == "57014"
	}
	var me *mysql.QueryError
	if errors.As(err, &me) {
		return me.Number == 1317
	}
	var ce *clickhouse.QueryError
	if errors.As(err, &ce) {
		return ce.Code == clickhouse.CodeQueryCancelled
	}
	return errors.Is(err, context.Canceled)
}

// errorOffset maps an engine error position to a byte offset in src.
func errorOffset(err error, st sqltext.Statement, src string) int {
	var pe *postgres.QueryError
	if errors.As(err, &pe) && pe.Position > 0 {
		runes := []rune(st.SQL)
		p := min(pe.Position-1, len(runes))
		return st.Start + len(string(runes[:p]))
	}
	var ce *clickhouse.QueryError
	if errors.As(err, &ce) && ce.Position > 0 {
		runes := []rune(st.SQL)
		p := min(ce.Position-1, len(runes))
		return st.Start + len(string(runes[:p]))
	}
	var me *mysql.QueryError
	if errors.As(err, &me) && me.Line > 0 && src != "" {
		return sqltext.OffsetOf(src, st.Line+me.Line-1, 1)
	}
	if st.SQL != "" {
		return st.Start
	}
	return -1
}

type fetchMsg struct {
	tab   *tab
	panel *resultPanel
	rows  [][]db.Cell
	done  bool
	err   error
}

// fetchMore loads up to n more rows into r.
func (m *Model) fetchMore(t *tab, r *resultPanel, n int) tea.Cmd {
	if r.stream == nil || r.done || r.loading {
		return nil
	}
	if t.busy {
		return nil
	}
	r.loading = true
	st := r.stream
	return m.opKeep(t, st, "fetching", func(ctx context.Context, s db.Session) tea.Msg {
		var all [][]db.Cell
		for len(all) < n {
			rows, err := st.rs.Next(min(n-len(all), 5000))
			all = append(all, rows...)
			if errors.Is(err, io.EOF) {
				return fetchMsg{tab: t, panel: r, rows: all, done: true}
			}
			if err != nil {
				return fetchMsg{tab: t, panel: r, rows: all, err: err}
			}
			if len(all) >= 2_000_000 {
				break
			}
		}
		return fetchMsg{tab: t, panel: r, rows: all}
	})
}

func (m *Model) handleFetch(msg fetchMsg) tea.Cmd {
	r := msg.panel
	r.loading = false
	r.grid.AppendRows(msg.rows)
	if msg.err != nil {
		msg.tab.out.msgs.log(logError, "Fetch failed: "+msg.err.Error())
		r.closeStream()
		return m.toast("Fetch failed: "+msg.err.Error(), toastError)
	}
	if msg.done {
		r.done = true
		if r.stream != nil {
			r.stream.close()
			r.stream = nil
		}
	}
	msg.tab.lastRun = r.status()
	return nil
}

// rerun re-executes the query behind r and replaces its rows.
func (m *Model) rerun(t *tab, r *resultPanel) tea.Cmd {
	if r.dirty() {
		return m.toast("Save or revert pending edits first", toastWarn)
	}
	st := sqltext.Statement{SQL: r.sql}
	return m.execute(t, []sqltext.Statement{st}, runOpts{replace: r, source: r.source})
}

// openTableData shows the rows of a table in the current tab.
func (m *Model) openTableData(ref db.ObjectRef) tea.Cmd {
	t := m.currentTab()
	if t == nil {
		return nil
	}
	opts := runOpts{name: ref.Name, source: &ref}
	if t.ac.dialect().HasSchemas() && ref.Database != "" && ref.Database != t.db {
		opts.useDB = ref.Database
	}
	sql := t.ac.dialect().SelectAll(ref, 0)
	m.focus = focusOutput
	return m.startRun(t, []sqltext.Statement{{SQL: sql}}, opts)
}

// openTableInNewTab opens a new tab on the current connection with a SELECT
// of ref in the editor and runs it.
func (m *Model) openTableInNewTab(ref db.ObjectRef) tea.Cmd {
	cur := m.currentTab()
	if cur == nil {
		return nil
	}
	t := m.createTab(cur.ac)
	t.db = cur.db
	if ref.Database != "" {
		t.db = ref.Database
	}
	t.label = ref.Name
	sql := t.ac.dialect().SelectAll(ref, 0)
	t.editor.SetValue(sql + ";")
	if m.settings.VimMode && t.editor.Inserting() {
		t.editor.Update(tea.KeyMsg{Type: tea.KeyEsc}) // ready for normal-mode keys
	}
	m.focus = focusOutput
	return m.startRun(t, []sqltext.Statement{{SQL: sql}}, runOpts{name: ref.Name, source: &ref})
}

// singleTableSource guesses the table behind "SELECT … FROM t [WHERE…]".
func singleTableSource(sql string, t *tab) *db.ObjectRef {
	d := t.ac.driver()
	var toks []sqltext.Token
	for _, tk := range sqltext.Tokenize(sql, d) {
		if tk.Kind != sqltext.TokWhitespace && tk.Kind != sqltext.TokComment {
			toks = append(toks, tk)
		}
	}
	if len(toks) < 4 || !strings.EqualFold(toks[0].Text, "select") {
		return nil
	}
	depth := 0
	from := -1
	for i, tk := range toks {
		switch tk.Text {
		case "(":
			depth++
		case ")":
			depth--
		}
		if depth != 0 {
			continue
		}
		up := strings.ToUpper(tk.Text)
		switch up {
		case "JOIN", "UNION", "INTERSECT", "EXCEPT", "GROUP", "HAVING", "DISTINCT", ",", "WINDOW":
			if from >= 0 || up == "DISTINCT" || up == "UNION" {
				return nil
			}
		case "FROM":
			if from >= 0 {
				return nil
			}
			from = i
		}
	}
	if from < 0 || from+1 >= len(toks) {
		return nil
	}
	ident := func(tk sqltext.Token) (string, bool) {
		switch {
		case tk.IsWord():
			return tk.Text, true
		case tk.Kind == sqltext.TokQuotedIdent:
			q := tk.Text[0:1]
			return strings.ReplaceAll(tk.Text[1:len(tk.Text)-1], q+q, q), true
		}
		return "", false
	}
	i := from + 1
	name, ok := ident(toks[i])
	if !ok {
		return nil
	}
	var qual string
	if i+2 < len(toks) && toks[i+1].Text == "." {
		n2, ok := ident(toks[i+2])
		if !ok {
			return nil
		}
		qual, name = name, n2
		i += 2
	}
	// what may follow: alias, WHERE, ORDER, LIMIT, OFFSET, FETCH, FOR, ;
	rest := toks[i+1:]
	isAlias := func(tk sqltext.Token) bool {
		return (tk.IsWord() || tk.Kind == sqltext.TokQuotedIdent) && !isClauseKeyword(tk.Text)
	}
	if len(rest) > 0 && (isAlias(rest[0]) || strings.EqualFold(rest[0].Text, "as")) {
		rest = rest[1:]
		if len(rest) > 0 && isAlias(rest[0]) {
			rest = rest[1:]
		}
	}
	if len(rest) > 0 && !isClauseKeyword(rest[0].Text) && rest[0].Text != ";" {
		return nil
	}
	ref := db.ObjectRef{Name: name, Kind: db.KindTable, Database: t.db}
	if t.ac.dialect().HasSchemas() {
		ref.Schema = qual
		if ref.Schema == "" {
			ref.Schema = t.completionContext().SearchPath[0]
		}
	} else {
		if qual != "" {
			ref.Database = qual
		}
		ref.Schema = ref.Database
	}
	return &ref
}

func isClauseKeyword(s string) bool {
	switch strings.ToUpper(s) {
	case "WHERE", "ORDER", "LIMIT", "OFFSET", "FETCH", "FOR":
		return true
	}
	return false
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func abbrev(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
