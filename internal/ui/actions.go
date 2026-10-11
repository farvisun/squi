package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/farvisun/squi/internal/dataio"
	"github.com/farvisun/squi/internal/db"
	"github.com/farvisun/squi/internal/editsql"
	"github.com/farvisun/squi/internal/history"
	"github.com/farvisun/squi/internal/sqltext"
	"github.com/farvisun/squi/internal/ui/components"
	"github.com/farvisun/squi/internal/ui/theme"
)

// ---- toasts ----

type toastLevel int

const (
	toastInfo toastLevel = iota
	toastWarn
	toastError
)

type toastMsg struct {
	text  string
	level toastLevel
}

func (m *Model) toast(text string, level toastLevel) tea.Cmd {
	m.toastText, m.toastLevel, m.toastUntil = text, level, time.Now().Add(4*time.Second)
	if level == toastError {
		m.toastUntil = time.Now().Add(8 * time.Second)
	}
	return nil
}

// ---- metadata views ----

type detailMsg struct {
	ac   *activeConn
	d    *db.TableDetail
	err  error
	then func(*db.TableDetail) tea.Cmd
}

// loadDetail fetches table detail and passes it to then.
func (m *Model) loadDetail(ac *activeConn, ref db.ObjectRef, then func(*db.TableDetail) tea.Cmd) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		d, err := ac.conn.TableDetail(ctx, ref)
		return detailMsg{ac: ac, d: d, err: err, then: then}
	}
}

func (m *Model) showDetail(ref db.ObjectRef) tea.Cmd {
	t := m.currentTab()
	if t == nil {
		return nil
	}
	return m.loadDetail(t.ac, ref, func(d *db.TableDetail) tea.Cmd {
		p := newDetailPanel(t.ac, d, m.settings.MaxColumnWidth)
		for _, x := range t.out.panels {
			if old, ok := x.(*detailPanel); ok && old.ref == ref {
				p.ix = old.ix
				t.out.replace(old, p)
				m.focus = focusOutput
				if p.isDDL() {
					return p.loadDDL()
				}
				return nil
			}
		}
		t.out.add(p)
		m.focus = focusOutput
		return nil
	})
}

type ddlTextMsg struct {
	tab  *tab
	name string
	ddl  string
	err  error
}

func (m *Model) showDDL(ref db.ObjectRef) tea.Cmd {
	t := m.currentTab()
	if t == nil {
		return nil
	}
	ac := t.ac
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		ddl, err := ac.conn.DDL(ctx, ref)
		return ddlTextMsg{tab: t, name: ref.Name, ddl: ddl, err: err}
	}
}

func (m *Model) insertSelect(ref db.ObjectRef) tea.Cmd {
	t := m.currentTab()
	if t == nil {
		return nil
	}
	return m.appendToEditor(t.ac.dialect().SelectAll(ref, 100) + ";")
}

// appendToEditor adds text on a new line at the end of the editor.
func (m *Model) appendToEditor(text string) tea.Cmd {
	t := m.currentTab()
	if t == nil {
		return nil
	}
	cur := t.editor.Value()
	t.editor.SetCursorOffset(len(cur))
	prefix := ""
	if strings.TrimSpace(cur) != "" {
		prefix = "\n\n"
		if strings.HasSuffix(cur, "\n") {
			prefix = "\n"
		}
	}
	t.editor.InsertText(prefix + text)
	m.focus = focusEditor
	return nil
}

func (m *Model) useDatabase(name, schemaName string) tea.Cmd {
	t := m.currentTab()
	if t == nil || name == "" {
		return nil
	}
	if t.inTx {
		return m.toast("Commit or roll back the open transaction first", toastWarn)
	}
	return m.op(t, "switching database", func(ctx context.Context, s db.Session) tea.Msg {
		err := s.UseDatabase(ctx, name)
		if err == nil && schemaName != "" && t.ac.dialect().HasSchemas() {
			t.schema = schemaName
			// SQL Server has no session search path (the default schema is the
			// user's): the schema only steers completion there
			if t.ac.driver() == db.Postgres {
				if rs, e := s.Run(ctx, "SET search_path TO "+t.ac.dialect().QuoteIdent(schemaName)+", public"); e == nil {
					_, _ = rs.Next(1)
					_ = rs.Close()
				}
			}
		}
		if err != nil {
			return toastMsg{text: "USE failed: " + err.Error(), level: toastError}
		}
		return toastMsg{text: "Using " + qualify(name, schemaName), level: toastInfo}
	})
}

type dbPickMsg struct {
	tab   *tab
	names []string
	err   error
}

// databasePicker lists the server's databases and switches the tab to the
// chosen one.
func (m *Model) databasePicker(t *tab) tea.Cmd {
	ac := t.ac
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		dbs, err := ac.conn.Databases(ctx)
		names := make([]string, len(dbs))
		for i, d := range dbs {
			names[i] = d.Name
		}
		return dbPickMsg{tab: t, names: names, err: err}
	}
}

func (m *Model) handleDBPick(msg dbPickMsg) tea.Cmd {
	if msg.err != nil {
		return m.toast("Databases: "+msg.err.Error(), toastError)
	}
	if !m.hasTab(msg.tab) || m.currentTab() != msg.tab {
		return nil
	}
	m.push(dbPicker("Use database", msg.names, msg.tab.db, func(name string) tea.Cmd {
		return m.useDatabase(name, "")
	}))
	return nil
}

// dbPicker lists database names with cur marked and highlighted.
func dbPicker(title string, names []string, cur string, pick func(string) tea.Cmd) *pickerModal {
	items := make([]components.PickerItem, len(names))
	at := 0
	for i, n := range names {
		items[i] = components.PickerItem{Title: n, Value: n}
		if n == cur {
			items[i].Badge = theme.OK.Render("current")
			at = i
		}
	}
	pm := newPickerModal(title, items, func(it components.PickerItem) tea.Cmd { return pick(it.Value.(string)) })
	pm.picker.SetIndex(at)
	return pm
}

type grantsMsg struct {
	user  string
	table *db.Table
	err   error
}

func (m *Model) showGrants(ac *activeConn, user string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		t, err := ac.conn.Admin().Grants(ctx, user)
		return grantsMsg{user: user, table: t, err: err}
	}
}

// openAdmin adds (or focuses) the admin panel of the current tab.
func (m *Model) openAdmin(sub int) tea.Cmd {
	t := m.currentTab()
	if t == nil {
		return nil
	}
	for i, p := range t.out.panels {
		if a, ok := p.(*adminPanel); ok {
			t.out.ix = i
			a.ix = sub
			m.focus = focusOutput
			return a.load(sub)
		}
	}
	a := newAdminPanel(t.ac)
	a.ix = sub
	t.out.add(a)
	m.focus = focusOutput
	return a.load(sub)
}

// ---- inspector / record view ----

func (m *Model) inspectCell(col db.ResultColumn, v db.Cell) tea.Cmd {
	title := col.Name
	if col.Type != "" {
		title += " (" + col.Type + ")"
	}
	text := prettyValue(v.S)
	if v.Null {
		text = "NULL"
	}
	tm := newTextModal(title, text, "")
	m.push(tm)
	return nil
}

func (m *Model) showRecord(r *resultPanel, row int) tea.Cmd {
	w := 0
	for _, c := range r.cols {
		w = max(w, len([]rune(c.Name)))
	}
	var lines, raw []string
	vals := r.effectiveRow(row)
	for i, c := range r.cols {
		v := theme.Null.Render("NULL")
		rv := "NULL"
		if !vals[i].Null {
			rv = vals[i].S
			v = components.Sanitize(vals[i].S)
		}
		lines = append(lines, theme.Key.Render(components.Fit(c.Name, w))+"  "+v+"  "+theme.Dim.Render(c.Type))
		raw = append(raw, c.Name+": "+rv)
	}
	tm := newTextModal(fmt.Sprintf("Row %d", row+1), "", "")
	tm.tv.SetStyledLines(strings.Join(raw, "\n"), lines)
	m.push(tm)
	return nil
}

// ---- copy / export ----

func (m *Model) exportOpts(source *db.ObjectRef) dataio.Options {
	o := dataio.DefaultOptions()
	if t := m.currentTab(); t != nil {
		o.Dialect = t.ac.dialect()
	}
	if source != nil {
		o.Table = *source
	}
	return o
}

func (m *Model) copyMenu(r *resultPanel) tea.Cmd {
	row, col := r.grid.Cursor()
	type choice struct {
		what string
		rows [][]db.Cell
		f    dataio.Format
		cols []db.ResultColumn
	}
	var items []components.PickerItem
	add := func(title string, c choice) {
		items = append(items, components.PickerItem{Title: title, Value: c})
	}
	all := make([][]db.Cell, 0, len(r.rows()))
	for i := range r.rows() {
		if !r.deleted[i] {
			all = append(all, r.effectiveRow(i))
		}
	}
	if row >= 0 {
		one := [][]db.Cell{r.effectiveRow(row)}
		add("Row as TSV", choice{"row", one, dataio.FormatTSV, r.cols})
		add("Row as JSON", choice{"row", one, dataio.FormatJSON, r.cols})
		add("Row as INSERT", choice{"row", one, dataio.FormatSQL, r.cols})
		colVals := make([][]db.Cell, 0, len(all))
		for _, rr := range all {
			colVals = append(colVals, []db.Cell{rr[col]})
		}
		add("Column values ("+r.cols[col].Name+")", choice{"column", colVals, dataio.FormatTSV, []db.ResultColumn{r.cols[col]}})
	}
	for _, f := range dataio.Formats {
		add(fmt.Sprintf("All loaded rows as %s", strings.ToUpper(string(f))), choice{"rows", all, f, r.cols})
	}
	m.push(newPickerModal("Copy", items, func(it components.PickerItem) tea.Cmd {
		c := it.Value.(choice)
		o := m.exportOpts(r.source)
		if c.what == "column" {
			o.Header = false
		}
		return m.copy(strings.TrimRight(dataio.FormatRows(c.f, c.cols, c.rows, o), "\n"), c.what)
	}))
	return nil
}

type jobDoneMsg struct {
	text string
	err  error
	ac   *activeConn
	ref  *db.ObjectRef // table to refresh in explorer
}

// chooseExport asks for format and path, then calls run(format, path).
func (m *Model) chooseExport(base string, run func(f dataio.Format, path string) tea.Cmd) tea.Cmd {
	var items []components.PickerItem
	for _, f := range dataio.Formats {
		items = append(items, components.PickerItem{Title: strings.ToUpper(string(f)), Detail: "." + f.Ext(), Value: f})
	}
	m.push(newPickerModal("Export format", items, func(it components.PickerItem) tea.Cmd {
		f := it.Value.(dataio.Format)
		wd, _ := os.Getwd()
		def := filepath.Join(wd, fmt.Sprintf("%s-%s.%s", sanitizeFile(base), time.Now().Format("20060102-150405"), f.Ext()))
		m.push(newPrompt("Export to file", "Path:", def, false, func(p string) tea.Cmd {
			p = expandHome(strings.TrimSpace(p))
			if p == "" {
				return nil
			}
			return run(f, p)
		}))
		return nil
	}))
	return nil
}

func (m *Model) exportResult(r *resultPanel) tea.Cmd {
	t := m.currentTab()
	if t == nil {
		return nil
	}
	base := r.name
	if r.source != nil {
		base = r.source.Name
	}
	write := sqltext.Classify(r.sql, t.ac.driver()).IsWrite()
	loaded := r.done || write
	return m.chooseExport(base, func(f dataio.Format, path string) tea.Cmd {
		if loaded {
			rows := make([][]db.Cell, 0, len(r.rows()))
			for i := range r.rows() {
				rows = append(rows, r.effectiveRow(i))
			}
			o := m.exportOpts(r.source)
			cols := r.cols
			return m.job(func() jobDoneMsg {
				n, err := writeRows(path, f, cols, rows, o)
				return jobDoneMsg{text: fmt.Sprintf("Exported %d rows to %s", n, path), err: err}
			})
		}
		return m.exportQuery(t.ac, t.db, r.sql, r.source, f, path)
	})
}

func (m *Model) exportTable(ref db.ObjectRef) tea.Cmd {
	t := m.currentTab()
	if t == nil {
		return nil
	}
	ac := t.ac
	return m.chooseExport(ref.Name, func(f dataio.Format, path string) tea.Cmd {
		dbName := ref.Database
		if !ac.dialect().HasSchemas() {
			dbName = t.db
		}
		return m.exportQuery(ac, dbName, ac.dialect().SelectAll(ref, 0), &ref, f, path)
	})
}

// exportQuery streams a query into a file on a separate session.
func (m *Model) exportQuery(ac *activeConn, dbName, sql string, source *db.ObjectRef, f dataio.Format, path string) tea.Cmd {
	o := m.exportOpts(source)
	o.Dialect = ac.dialect()
	return m.job(func() jobDoneMsg {
		ctx := context.Background()
		s, err := ac.conn.NewSession(ctx, dbName)
		if err != nil {
			return jobDoneMsg{err: err}
		}
		defer s.Close()
		rs, err := s.Run(ctx, sql)
		if err != nil {
			return jobDoneMsg{err: err}
		}
		defer rs.Close()
		file, err := os.Create(path)
		if err != nil {
			return jobDoneMsg{err: err}
		}
		n, err := dataio.Export(ctx, rs, file, f, o, nil)
		if cerr := file.Close(); err == nil {
			err = cerr
		}
		return jobDoneMsg{text: fmt.Sprintf("Exported %d rows to %s", n, path), err: err}
	})
}

func writeRows(path string, f dataio.Format, cols []db.ResultColumn, rows [][]db.Cell, o dataio.Options) (int, error) {
	file, err := os.Create(path)
	if err != nil {
		return 0, err
	}
	w, err := dataio.NewWriter(file, f, cols, o)
	if err != nil {
		file.Close()
		return 0, err
	}
	if err := w.WriteRows(rows); err != nil {
		file.Close()
		return 0, err
	}
	if err := w.Close(); err != nil {
		file.Close()
		return 0, err
	}
	return len(rows), file.Close()
}

// job runs fn in the background and reports its result as a toast.
func (m *Model) job(fn func() jobDoneMsg) tea.Cmd {
	m.jobs++
	return tea.Batch(m.spinner.Tick, func() tea.Msg { return fn() })
}

func sanitizeFile(s string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>| `, r) {
			return '_'
		}
		return r
	}, s)
}

// ---- CSV import ----

func (m *Model) importCSV(ref db.ObjectRef) tea.Cmd {
	t := m.currentTab()
	if t == nil {
		return nil
	}
	if t.ac.cfg.ReadOnly {
		return m.toast("Connection is read-only", toastWarn)
	}
	ac := t.ac
	wd, _ := os.Getwd()
	m.push(newPrompt("Import CSV into "+ref.Name, "CSV file:", wd+string(os.PathSeparator), false, func(p string) tea.Cmd {
		path := expandHome(strings.TrimSpace(p))
		header, rows, err := dataio.Preview(path, dataio.CSVOptions{Header: true}, 5)
		if err != nil {
			return m.toast("Cannot read CSV: "+err.Error(), toastError)
		}
		return m.loadDetail(ac, ref, func(d *db.TableDetail) tea.Cmd {
			m.push(newImportModal(m, ac, ref, d, path, header, rows))
			return nil
		})
	}))
	return nil
}

// runImport loads the CSV into the table inside one transaction.
func (m *Model) runImport(ac *activeConn, ref db.ObjectRef, path string, mapping []int, targets []string, nullText string) tea.Cmd {
	dbName := ref.Database
	return m.job(func() jobDoneMsg {
		ctx := context.Background()
		file, err := os.Open(path)
		if err != nil {
			return jobDoneMsg{err: err}
		}
		defer file.Close()
		rd, err := dataio.NewCSVReader(file, dataio.CSVOptions{Header: true, NullText: nullText})
		if err != nil {
			return jobDoneMsg{err: err}
		}
		s, err := ac.conn.NewSession(ctx, dbName)
		if err != nil {
			return jobDoneMsg{err: err}
		}
		defer s.Close()
		// engines without transactions (ClickHouse) keep the rows loaded
		// before a failure
		tx := ac.dialect().Transactions()
		if tx {
			if err := s.Begin(ctx); err != nil {
				return jobDoneMsg{err: err}
			}
		}
		rollback := func() string {
			if !tx {
				return "rows loaded so far were kept"
			}
			_ = s.Rollback(ctx)
			return "rolled back"
		}
		var total int64
		for {
			batch, rerr := rd.Next(1000)
			if len(batch) > 0 {
				mapped := make([][]db.Cell, len(batch))
				for i, row := range batch {
					out := make([]db.Cell, len(mapping))
					for j, src := range mapping {
						if src < len(row) {
							out[j] = row[src]
						} else {
							out[j] = db.NullCell
						}
					}
					mapped[i] = out
				}
				n, err := s.BulkInsert(ctx, ref, targets, mapped)
				total += n
				if err != nil {
					how := rollback()
					return jobDoneMsg{err: fmt.Errorf("import failed after %d rows (%s): %w", total, how, err)}
				}
			}
			if errors.Is(rerr, io.EOF) {
				break
			}
			if rerr != nil {
				how := rollback()
				return jobDoneMsg{err: fmt.Errorf("read CSV (%s): %w", how, rerr)}
			}
		}
		if tx {
			if err := s.Commit(ctx); err != nil {
				return jobDoneMsg{err: err}
			}
		}
		return jobDoneMsg{text: fmt.Sprintf("Imported %d rows into %s", total, ref.Name), ac: ac, ref: &ref}
	})
}

// ---- saving inline edits ----

type editsAppliedMsg struct {
	tab   *tab
	panel *resultPanel
	n     int
	err   error
}

func (m *Model) saveEdits(t *tab, r *resultPanel) tea.Cmd {
	if !r.dirty() {
		return m.toast("No pending changes", toastInfo)
	}
	stmts, err := editsql.Generate(t.ac.dialect(), *r.source, r.changes())
	if err != nil {
		return m.toast("Cannot save: "+err.Error(), toastError)
	}
	preview := strings.Join(stmts, ";\n") + ";"
	tm := newTextModal(fmt.Sprintf("Apply %d change(s) to %s", len(stmts), envLabel(t.ac.cfg)), preview, components.LexerFor(t.ac.cfg.Driver))
	tm.footer = theme.Key.Render("enter") + " apply   " + theme.Key.Render("y") + " copy   " + theme.Key.Render("esc") + " cancel"
	tm.onKey = func(key string) (bool, tea.Cmd) {
		if key != "enter" {
			return false, nil
		}
		return true, m.applyEdits(t, r, stmts)
	}
	m.push(tm)
	return nil
}

func (m *Model) applyEdits(t *tab, r *resultPanel, stmts []string) tea.Cmd {
	r.closeStream()
	manual := t.manualTx
	return m.op(t, "saving", func(ctx context.Context, s db.Session) tea.Msg {
		began := false
		if !s.InTx() {
			if err := s.Begin(ctx); err != nil {
				return editsAppliedMsg{tab: t, panel: r, err: err}
			}
			began = true
		}
		fail := func(err error) tea.Msg {
			if began {
				_ = s.Rollback(ctx)
			}
			return editsAppliedMsg{tab: t, panel: r, err: err}
		}
		for _, st := range stmts {
			rs, err := s.Run(ctx, st)
			if err != nil {
				return fail(fmt.Errorf("%w\n%s", err, st))
			}
			for {
				if _, err := rs.Next(100); err != nil {
					if !errors.Is(err, io.EOF) {
						_ = rs.Close()
						return fail(err)
					}
					break
				}
			}
			_ = rs.Close()
			if n := rs.RowsAffected(); n != 1 {
				return fail(fmt.Errorf("expected 1 row affected, got %d — the row may have changed; nothing was saved\n%s", n, st))
			}
		}
		if began && !manual {
			if err := s.Commit(ctx); err != nil {
				return editsAppliedMsg{tab: t, panel: r, err: err}
			}
		}
		return editsAppliedMsg{tab: t, panel: r, n: len(stmts)}
	})
}

func (m *Model) handleEditsApplied(msg editsAppliedMsg) tea.Cmd {
	t, r := msg.tab, msg.panel
	if msg.err != nil {
		t.out.msgs.log(logError, "Save failed: "+msg.err.Error())
		return m.toast("Save failed — see Messages", toastError)
	}
	r.revertAll()
	note := ""
	if t.inTx {
		note = " (transaction still open — commit with ctrl+g)"
	}
	t.out.msgs.log(logOK, fmt.Sprintf("Applied %d change(s) to %s%s", msg.n, r.source.Name, note))
	return tea.Batch(m.toast(fmt.Sprintf("Saved %d change(s)%s", msg.n, note), toastInfo), m.rerun(t, r))
}

// ---- explain ----

type explainMsg struct {
	tab     *tab
	sql     string
	plan    *db.PlanNode
	analyze bool
	err     error
}

func (m *Model) explain(t *tab, analyze bool) tea.Cmd {
	src := t.editor.Value()
	var sql string
	if a, b, ok := t.editor.Selection(); ok {
		sql = strings.TrimSpace(src[a:b])
	} else if st, ok := sqltext.StatementAt(src, t.editor.CursorOffset(), t.ac.driver()); ok {
		sql = st.SQL
	}
	if sql == "" {
		return m.toast("Nothing to explain", toastWarn)
	}
	if analyze && !t.ac.dialect().Transactions() {
		return m.toast(t.ac.cfg.Driver+" has no EXPLAIN ANALYZE — F7 shows the plan", toastWarn)
	}
	write := sqltext.Classify(sql, t.ac.driver()).IsWrite()
	timeout := t.queryTimeout()
	run := func() tea.Cmd {
		return m.op(t, "explaining", func(ctx context.Context, s db.Session) tea.Msg {
			rollback := false
			if analyze && write && !s.InTx() {
				if err := s.Begin(ctx); err != nil {
					return explainMsg{tab: t, err: err}
				}
				rollback = true
			}
			wd := startWatchdog(s, timeout)
			plan, err := s.Explain(ctx, sql, analyze)
			if wd.stop() && err != nil {
				err = &timeoutError{after: timeout, err: err}
			}
			if rollback {
				_ = s.Rollback(ctx)
			}
			return explainMsg{tab: t, sql: sql, plan: plan, analyze: analyze, err: err}
		})
	}
	if analyze && write {
		if t.ac.cfg.ReadOnly {
			return m.toast("EXPLAIN ANALYZE of a write is blocked on read-only connections", toastError)
		}
		body := "EXPLAIN ANALYZE executes the statement. It will run inside a transaction that is rolled back afterwards."
		if t.inTx {
			body = "EXPLAIN ANALYZE executes the statement INSIDE YOUR OPEN TRANSACTION. Its changes will stay until you roll back."
		}
		m.push(&confirmModal{title: "Explain analyze a write", body: body, danger: true, onYes: run})
		return nil
	}
	return run()
}

// ---- transactions ----

type txMsg struct {
	tab  *tab
	what string
	err  error
}

func (m *Model) txAction(t *tab, what string) tea.Cmd {
	return m.op(t, strings.ToLower(what), func(ctx context.Context, s db.Session) tea.Msg {
		var err error
		switch what {
		case "BEGIN":
			err = s.Begin(ctx)
		case "COMMIT":
			err = s.Commit(ctx)
		case "ROLLBACK":
			err = s.Rollback(ctx)
		}
		return txMsg{tab: t, what: what, err: err}
	})
}

func (m *Model) txMenu(t *tab) tea.Cmd {
	if !t.ac.dialect().Transactions() {
		return m.toast(t.ac.cfg.Driver+" has no transactions: every statement applies immediately", toastWarn)
	}
	item := func(key, label, v string) menuItem {
		return menuItem{key: key, label: label, run: func() tea.Cmd { return m.txChoice(t, v) }}
	}
	m.push(&menuModal{title: "Transaction", items: []menuItem{
		item("c", "Commit", "COMMIT"),
		item("r", "Rollback", "ROLLBACK"),
		item("b", "Begin transaction", "BEGIN"),
		item("m", "Manual transaction mode: "+yesNo(t.manualTx, "on → off", "off → on"), "MANUAL"),
	}})
	return nil
}

func (m *Model) txChoice(t *tab, v string) tea.Cmd {
	if v == "MANUAL" {
		t.manualTx = !t.manualTx
		return m.toast("Manual transactions "+yesNo(t.manualTx, "on: statements run in a transaction until you commit", "off (autocommit)"), toastInfo)
	}
	return m.txAction(t, v)
}

// ---- history & snippets ----

func (m *Model) historyPicker(t *tab) tea.Cmd {
	if m.history == nil {
		return m.toast("History is unavailable", toastWarn)
	}
	entries := m.history.Search("", t.ac.cfg.ID, 500)
	items := historyItems(entries)
	pm := newPickerModal("History — "+t.ac.cfg.Title(), items, func(it components.PickerItem) tea.Cmd {
		return m.insertSQL(t, it.Value.(history.Entry).SQL)
	})
	pm.picker.MultiLine = true
	pm.height = 30
	// re-search on every keystroke so fuzzy ranking uses the full history
	m.push(&historyModal{pickerModal: pm, store: m.history, connID: t.ac.cfg.ID})
	return nil
}

func historyItems(entries []history.Entry) []components.PickerItem {
	items := make([]components.PickerItem, 0, len(entries))
	for _, e := range entries {
		detail := e.Time.Format("2006-01-02 15:04") + " · " + fmtDuration(time.Duration(e.DurationMS)*time.Millisecond)
		if e.Database != "" {
			detail += " · " + e.Database
		}
		if e.Error != "" {
			detail += " · ✗ " + e.Error
		} else {
			detail += fmt.Sprintf(" · %d rows", e.Rows)
		}
		items = append(items, components.PickerItem{Title: oneLine(e.SQL), Detail: detail, Value: e})
	}
	return items
}

type historyModal struct {
	*pickerModal
	store  *history.Store
	connID string
	query  string
}

func (h *historyModal) update(m *Model, msg tea.Msg) (bool, tea.Cmd) {
	done, cmd := h.pickerModal.update(m, msg)
	if q := h.picker.Query(); !done && q != h.query {
		h.query = q
		h.picker.SetItems(historyItems(h.store.Search(q, h.connID, 500)))
	}
	return done, cmd
}

func (m *Model) insertSQL(t *tab, sql string) tea.Cmd {
	if strings.TrimSpace(t.editor.Value()) == "" {
		t.editor.SetValue(sql)
	} else {
		return m.appendToEditor(sql)
	}
	m.focus = focusEditor
	return nil
}

func (m *Model) snippetPicker(t *tab) tea.Cmd {
	var items []components.PickerItem
	for _, s := range m.snippets {
		if s.Driver != "" && s.Driver != t.ac.cfg.Driver {
			continue
		}
		items = append(items, components.PickerItem{Title: s.Name, Detail: oneLine(s.SQL), Value: s})
	}
	if len(items) == 0 {
		return m.toast("No snippets yet — save one with :snippet <name>", toastInfo)
	}
	m.push(newPickerModal("Snippets", items, func(it components.PickerItem) tea.Cmd {
		return m.insertSQL(t, it.Value.(history.Snippet).SQL)
	}))
	return nil
}

func (m *Model) saveSnippet(t *tab, name string) tea.Cmd {
	src := t.editor.Value()
	sql := ""
	if a, b, ok := t.editor.Selection(); ok {
		sql = src[a:b]
	} else if st, ok := sqltext.StatementAt(src, t.editor.CursorOffset(), t.ac.driver()); ok {
		sql = st.SQL
	}
	if strings.TrimSpace(sql) == "" || name == "" {
		return m.toast("Usage: :snippet <name> (saves the statement under the cursor)", toastWarn)
	}
	out := m.snippets[:0:0]
	for _, s := range m.snippets {
		if s.Name != name {
			out = append(out, s)
		}
	}
	out = append(out, history.Snippet{Name: name, SQL: sql})
	if err := history.SaveSnippets(m.store.SnippetsPath(), out); err != nil {
		return m.toast("Save snippet: "+err.Error(), toastError)
	}
	m.snippets = out
	return m.toast("Saved snippet "+name, toastInfo)
}

// ---- misc ----

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

func stripANSI(s string) string { return ansi.Strip(s) }
