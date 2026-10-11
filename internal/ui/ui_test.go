package ui

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/farvisun/squi/internal/config"
	"github.com/farvisun/squi/internal/db"
	"github.com/farvisun/squi/internal/schema"
	"github.com/farvisun/squi/internal/secrets"
	"github.com/farvisun/squi/internal/sqltext"
)

// ---- fake driver ----

type fakeConn struct {
	driver db.DriverName
	mu     sync.Mutex
	ran    []string
}

func (c *fakeConn) Dialect() db.Dialect        { return db.Dialect{Driver: c.driver} }
func (c *fakeConn) ServerVersion() string      { return "Fake 1.0" }
func (c *fakeConn) Ping(context.Context) error { return nil }
func (c *fakeConn) Admin() db.Admin            { return nil }
func (c *fakeConn) Close() error               { return nil }
func (c *fakeConn) DDL(context.Context, db.ObjectRef) (string, error) {
	return "CREATE TABLE t ();", nil
}
func (c *fakeConn) Databases(context.Context) ([]db.Database, error) {
	return []db.Database{{Name: "app"}}, nil
}
func (c *fakeConn) Schemas(context.Context, string) ([]string, error) { return []string{"public"}, nil }
func (c *fakeConn) Objects(context.Context, string, string) ([]db.Object, error) {
	return []db.Object{{Schema: "public", Name: "users", Kind: db.KindTable}}, nil
}
func (c *fakeConn) AllColumns(context.Context, string, string) (map[string][]string, error) {
	return map[string][]string{"users": {"id", "email"}}, nil
}
func (c *fakeConn) TableDetail(_ context.Context, ref db.ObjectRef) (*db.TableDetail, error) {
	return &db.TableDetail{Ref: ref, Columns: []db.Column{{Name: "id", Type: "int", IsPK: true}, {Name: "email", Type: "text", Nullable: true}}}, nil
}
func (c *fakeConn) NewSession(context.Context, string) (db.Session, error) {
	return &fakeSession{conn: c}, nil
}

func (c *fakeConn) statements() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.ran...)
}

type fakeSession struct {
	conn *fakeConn
	inTx bool
}

func (s *fakeSession) Run(_ context.Context, sql string) (db.ResultStream, error) {
	s.conn.mu.Lock()
	s.conn.ran = append(s.conn.ran, sql)
	s.conn.mu.Unlock()
	switch c := sqltext.Classify(sql, s.conn.driver); c.TxOp {
	case sqltext.TxBegin:
		s.inTx = true
	case sqltext.TxCommit, sqltext.TxRollback:
		s.inTx = false
	}
	if strings.HasPrefix(strings.ToUpper(sql), "SELECT") {
		rows := make([][]db.Cell, 2500)
		for i := range rows {
			rows[i] = []db.Cell{db.Text("1"), db.Text("a@b.c")}
		}
		return &fakeStream{cols: []db.ResultColumn{{Name: "id", Type: "int"}, {Name: "email", Type: "text"}}, rows: rows}, nil
	}
	return &fakeStream{affected: 1}, nil
}
func (s *fakeSession) Begin(ctx context.Context) error  { _, err := s.Run(ctx, "BEGIN"); return err }
func (s *fakeSession) Commit(ctx context.Context) error { _, err := s.Run(ctx, "COMMIT"); return err }
func (s *fakeSession) Rollback(ctx context.Context) error {
	_, err := s.Run(ctx, "ROLLBACK")
	return err
}
func (s *fakeSession) InTx() bool                                { return s.inTx }
func (s *fakeSession) Cancel(context.Context) error              { return nil }
func (s *fakeSession) UseDatabase(context.Context, string) error { return nil }
func (s *fakeSession) CurrentDatabase() string                   { return "app" }
func (s *fakeSession) Close() error                              { return nil }
func (s *fakeSession) Explain(context.Context, string, bool) (*db.PlanNode, error) {
	return &db.PlanNode{Title: "Seq Scan on users", Cost: 10, Children: []*db.PlanNode{{Title: "child", Cost: 2}}}, nil
}
func (s *fakeSession) BulkInsert(context.Context, db.ObjectRef, []string, [][]db.Cell) (int64, error) {
	return 0, nil
}

type fakeStream struct {
	cols     []db.ResultColumn
	rows     [][]db.Cell
	affected int64
}

func (f *fakeStream) Columns() []db.ResultColumn { return f.cols }
func (f *fakeStream) Next(n int) ([][]db.Cell, error) {
	if n >= len(f.rows) {
		out := f.rows
		f.rows = nil
		return out, io.EOF
	}
	out := f.rows[:n]
	f.rows = f.rows[n:]
	return out, nil
}
func (f *fakeStream) RowsAffected() int64 { return f.affected }
func (f *fakeStream) CommandTag() string  { return "" }
func (f *fakeStream) Close() error        { return nil }

// ---- harness ----

func newTestModel(t *testing.T, cfg config.Connection) (*Model, *tab, *fakeConn) {
	t.Helper()
	store := config.NewStore(t.TempDir())
	m, err := New(Options{Store: store, Secrets: &secrets.Memory{}, Bridge: &Bridge{}})
	if err != nil {
		t.Fatal(err)
	}
	m.width, m.height = 120, 40
	fc := &fakeConn{driver: db.DriverName(cfg.Driver)}
	ac := &activeConn{cfg: cfg, conn: fc, cache: schema.NewCache(fc.driver)}
	ac.explorer = newExplorer(ac)
	m.active[cfg.ID] = ac
	drain(m, m.newTab(ac))
	drain(m, ac.explorer.load(nil))
	return m, m.currentTab(), fc
}

// drain runs cmd and feeds resulting messages back until quiescent.
// Timer-driven commands (spinner, clock, cursor blink) are dropped.
func drain(m *Model, cmd tea.Cmd) {
	queue := []tea.Cmd{cmd}
	for steps := 0; len(queue) > 0 && steps < 200; steps++ {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch msg := runQuick(c).(type) {
		case nil, tickMsg, spinner.TickMsg, tea.QuitMsg:
		case tea.BatchMsg:
			queue = append(queue, msg...)
		default:
			_, next := m.Update(msg)
			queue = append(queue, next)
		}
	}
}

// runQuick runs a command, giving up on ones that wait on a timer.
func runQuick(c tea.Cmd) tea.Msg {
	ch := make(chan tea.Msg, 1)
	go func() { ch <- c() }()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(100 * time.Millisecond):
		return nil
	}
}

func key(m *Model, keys ...string) {
	for _, k := range keys {
		drain(m, m.handleKey(keyMsgFor(k)))
	}
}

func keyMsgFor(k string) tea.KeyMsg {
	special := map[string]tea.KeyType{"enter": tea.KeyEnter, "esc": tea.KeyEsc, "f5": tea.KeyF5, "f6": tea.KeyF6,
		"f7": tea.KeyF7, "ctrl+s": tea.KeyCtrlS, "ctrl+g": tea.KeyCtrlG, "ctrl+w": tea.KeyCtrlW, "tab": tea.KeyTab, "down": tea.KeyDown}
	if t, ok := special[k]; ok {
		return tea.KeyMsg{Type: t}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

func setSQL(t *tab, sql string) {
	t.editor.SetValue(sql)
	t.editor.SetCursorOffset(0)
}

// ---- tests ----

func TestRunSelectStreamsFirstPage(t *testing.T) {
	m, tb, _ := newTestModel(t, config.Connection{ID: "c", Name: "dev", Driver: "postgres"})
	setSQL(tb, "SELECT * FROM users")
	key(m, "f5")
	rs := tb.out.results()
	if len(rs) != 1 {
		t.Fatalf("want 1 result panel, got %d", len(rs))
	}
	r := rs[0]
	if len(r.rows()) != 1000 || r.done || r.stream == nil {
		t.Fatalf("first page: rows=%d done=%v stream=%v", len(r.rows()), r.done, r.stream != nil)
	}
	if r.source == nil || r.source.Name != "users" || r.source.Schema != "public" {
		t.Fatalf("single-table source not detected: %+v", r.source)
	}
	drain(m, m.fetchMore(tb, r, 1<<30))
	if len(r.rows()) != 2500 || !r.done {
		t.Fatalf("fetch all: rows=%d done=%v", len(r.rows()), r.done)
	}
	if !strings.Contains(m.View(), "2500 rows") {
		t.Fatal("status should show row count")
	}
}

func TestReadOnlyBlocksWrites(t *testing.T) {
	m, tb, fc := newTestModel(t, config.Connection{ID: "c", Name: "ro", Driver: "mysql", ReadOnly: true})
	setSQL(tb, "UPDATE users SET email = 'x' WHERE id = 1")
	key(m, "f5")
	for _, s := range fc.statements() {
		if strings.HasPrefix(s, "UPDATE") {
			t.Fatal("write reached the server on a read-only connection")
		}
	}
	if !strings.Contains(m.toastText, "Read-only") {
		t.Fatalf("toast = %q", m.toastText)
	}
}

func TestDestructiveNeedsTypedConfirmation(t *testing.T) {
	m, tb, fc := newTestModel(t, config.Connection{ID: "c", Name: "dev", Driver: "postgres", Database: "app"})
	setSQL(tb, "DELETE FROM users")
	key(m, "f5")
	if len(m.modals) != 1 {
		t.Fatalf("expected confirmation modal, got %d", len(m.modals))
	}
	key(m, "w", "r", "o", "n", "g", "enter")
	if len(m.modals) != 1 {
		t.Fatal("wrong confirmation text must not run the statement")
	}
	tc := m.modals[0].(*typedConfirmModal)
	tc.input.SetValue("app")
	key(m, "enter")
	found := false
	for _, s := range fc.statements() {
		found = found || s == "DELETE FROM users"
	}
	if !found || len(m.modals) != 0 {
		t.Fatalf("statement not run after confirmation: %v", fc.statements())
	}
}

func TestProdWritesNeedConfirmation(t *testing.T) {
	m, tb, fc := newTestModel(t, config.Connection{ID: "c", Name: "p", Driver: "postgres", Env: config.EnvProd})
	setSQL(tb, "UPDATE users SET email = 'x' WHERE id = 1")
	key(m, "f5")
	if len(m.modals) != 1 {
		t.Fatal("expected prod confirmation")
	}
	key(m, "n")
	if len(fc.statements()) != 0 {
		t.Fatalf("declined write ran: %v", fc.statements())
	}
	key(m, "f5", "y")
	if got := fc.statements(); len(got) != 1 || !strings.HasPrefix(got[0], "UPDATE") {
		t.Fatalf("confirmed write: %v", got)
	}
}

func TestManualTransactionMode(t *testing.T) {
	m, tb, fc := newTestModel(t, config.Connection{ID: "c", Name: "dev", Driver: "postgres"})
	tb.manualTx = true
	setSQL(tb, "UPDATE users SET email = 'x' WHERE id = 1")
	key(m, "f5")
	got := fc.statements()
	if len(got) != 2 || got[0] != "BEGIN" {
		t.Fatalf("manual mode should BEGIN first: %v", got)
	}
	if !tb.inTx {
		t.Fatal("tab should report an open transaction")
	}
	key(m, "ctrl+g", "c")
	if got := fc.statements(); got[len(got)-1] != "COMMIT" || tb.inTx {
		t.Fatalf("commit: %v inTx=%v", got, tb.inTx)
	}
}

func TestInlineEditGeneratesAndApplies(t *testing.T) {
	m, tb, fc := newTestModel(t, config.Connection{ID: "c", Name: "dev", Driver: "postgres"})
	setSQL(tb, "SELECT id, email FROM users WHERE id < 10")
	key(m, "f5")
	m.focus = focusOutput
	r := tb.out.results()[0]
	key(m, "l", "e") // edit email of row 1
	pm, ok := m.modals[len(m.modals)-1].(*promptModal)
	if !ok {
		t.Fatalf("expected edit prompt, got %T", m.modals[len(m.modals)-1])
	}
	pm.input.SetValue("new@x.io")
	key(m, "enter")
	if !r.dirty() {
		t.Fatal("edit not staged")
	}
	key(m, "ctrl+s")
	if _, ok := m.modals[len(m.modals)-1].(*textModal); !ok {
		t.Fatal("expected SQL preview")
	}
	key(m, "enter")
	want := `UPDATE "public"."users" SET "email" = 'new@x.io' WHERE "id" = '1'`
	got := fc.statements()
	idx := -1
	for i, s := range got {
		if s == want {
			idx = i
		}
	}
	if idx < 1 || got[idx-1] != "BEGIN" || got[idx+1] != "COMMIT" {
		t.Fatalf("edit statements: %v", got)
	}
}

func TestSingleTableSource(t *testing.T) {
	m, tb, _ := newTestModel(t, config.Connection{ID: "c", Name: "dev", Driver: "postgres"})
	_ = m
	cases := map[string]string{
		"SELECT * FROM users":                        "public.users",
		"select id from app.users u where id = 1":    "app.users",
		`SELECT * FROM "Users" AS u ORDER BY 1`:      "public.Users",
		"SELECT * FROM users u JOIN x ON true":       "",
		"SELECT count(*) FROM users GROUP BY 1":      "",
		"SELECT * FROM users, orders":                "",
		"SELECT DISTINCT a FROM users":               "",
		"SELECT * FROM (SELECT 1) s":                 "",
		"SELECT 1":                                   "",
		"SELECT * FROM users UNION SELECT * FROM t":  "",
		"SELECT * FROM users WHERE id IN (SELECT 1)": "public.users",
		"SELECT * FROM users LIMIT 5":                "public.users",
	}
	for sql, want := range cases {
		ref := singleTableSource(sql, tb)
		got := ""
		if ref != nil {
			got = ref.Schema + "." + ref.Name
		}
		if got != want {
			t.Errorf("%q: got %q want %q", sql, got, want)
		}
	}
}

func TestViewAtSizes(t *testing.T) {
	m, tb, _ := newTestModel(t, config.Connection{ID: "c", Name: "dev", Driver: "postgres", Env: config.EnvProd})
	setSQL(tb, "SELECT * FROM users")
	key(m, "f5", "f7")
	for _, sz := range [][2]int{{50, 14}, {80, 24}, {200, 60}, {40, 10}} {
		m.width, m.height = sz[0], sz[1]
		v := m.View()
		if sz[0] >= 50 {
			if n := len(strings.Split(v, "\n")); n != sz[1] {
				t.Errorf("%dx%d: %d lines", sz[0], sz[1], n)
			}
		}
	}
	m.width, m.height = 120, 40
	key(m, "ctrl+w", "z")
	if !m.zoom {
		t.Fatal("ctrl+w z should zoom")
	}
	_ = m.View()
}

func TestSubTabsNarrow(t *testing.T) {
	names := []string{"Columns", "FK in", "FK out", "Indexes", "DDL"}
	if got := subTabs(names, 1, 10); !strings.Contains(got, "FK in") {
		t.Fatalf("selected label should stay full: %q", got)
	}
}

func TestRevertAddedRowShiftsStagedEdits(t *testing.T) {
	cols := []db.ResultColumn{{Name: "id"}}
	rows := [][]db.Cell{{{S: "1"}}, {{S: "new"}}, {{S: "3"}}}
	r := newResultPanel("r", "", cols, rows, 40)
	r.added[1] = true
	r.deleted[2] = true
	r.edits[0] = map[int]db.Cell{0: {S: "x"}}
	r.revertRow(1)
	if len(r.rows()) != 2 || !r.deleted[1] || r.deleted[2] || r.edits[0] == nil || len(r.added) != 0 {
		t.Fatalf("rows=%d added=%v deleted=%v edits=%v", len(r.rows()), r.added, r.deleted, r.edits)
	}
}

func TestSessionSaveAndRestore(t *testing.T) {
	cfg := config.Connection{ID: "c", Name: "dev", Driver: "postgres", Host: "h"}
	m, tb, fc := newTestModel(t, cfg)
	if _, err := m.store.Upsert(cfg); err != nil {
		t.Fatal(err)
	}
	_ = m.reloadConns()
	setSQL(tb, "select 1")
	t2 := m.createTab(m.active["c"])
	t2.label = "users"
	t2.editor.SetValue("select * from users")
	m.cur = 1
	if err := m.saveSession(); err != nil {
		t.Fatal(err)
	}

	m2, err := New(Options{Store: m.store, Secrets: &secrets.Memory{}, Bridge: &Bridge{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(m2.pending) != 2 {
		t.Fatalf("pending = %d", len(m2.pending))
	}
	// tabs waiting for their connection survive another save
	if err := m2.saveSession(); err != nil {
		t.Fatal(err)
	}
	if sess, _ := m.store.LoadSession(); len(sess.Tabs) != 2 {
		t.Fatalf("saved %d tabs before connecting", len(sess.Tabs))
	}
	ac := &activeConn{cfg: cfg, conn: fc, cache: schema.NewCache(fc.driver)}
	drain(m2, func() tea.Msg { return connOpenedMsg{cfg: cfg, ac: ac} })
	if len(m2.tabs) != 2 || len(m2.pending) != 0 {
		t.Fatalf("tabs=%d pending=%d", len(m2.tabs), len(m2.pending))
	}
	if m2.tabs[0].editor.Value() != "select 1" || m2.tabs[1].label != "users" || m2.cur != 1 {
		t.Fatalf("restored %q / %q, cur=%d", m2.tabs[0].editor.Value(), m2.tabs[1].label, m2.cur)
	}
}

func TestDoubleClickOpensTableInNewTab(t *testing.T) {
	m, _, fc := newTestModel(t, config.Connection{ID: "c", Name: "dev", Driver: "postgres"})
	e := m.active["c"].explorer
	for expanded := true; expanded; {
		expanded = false
		for _, n := range e.flat {
			if n.expandable() && !n.expanded && n.kind != nObject {
				drain(m, e.toggle(n))
				expanded = true
				break
			}
		}
	}
	row := -1
	for i, n := range e.flat {
		if n.kind == nObject && n.label == "users" {
			row = i
		}
	}
	if row < 0 {
		t.Fatal("table node not found")
	}
	drain(m, e.click(m, row, 30))
	if len(m.tabs) != 1 {
		t.Fatal("single click opened a tab")
	}
	drain(m, e.click(m, row, 30))
	if len(m.tabs) != 2 || m.tabs[1].label != "users" || m.cur != 1 {
		t.Fatalf("tabs=%d cur=%d", len(m.tabs), m.cur)
	}
	want := `SELECT * FROM "public"."users"`
	found := false
	for _, s := range fc.statements() {
		found = found || s == want
	}
	if !found {
		t.Fatalf("statements: %v", fc.statements())
	}
}

func TestFormDatabasePicker(t *testing.T) {
	m, _, _ := newTestModel(t, config.Connection{ID: "c", Name: "dev", Driver: "postgres"})
	m.openConnections()
	m.mgr.edit(m, config.Connection{Driver: "postgres", Host: "h"})
	m.mgr.applyDBList(m, dbListMsg{names: []string{"app", "billing"}})
	if _, ok := m.modals[len(m.modals)-1].(*pickerModal); !ok {
		t.Fatal("expected database picker")
	}
	key(m, "down", "enter")
	if got := m.mgr.form.Value("database"); got != "billing" {
		t.Fatalf("database = %q", got)
	}
}

// Runes typed quickly arrive together; words that are also key names
// ("end", "up", "esc") must be inserted as text.
func TestBatchedRunesAreText(t *testing.T) {
	m, tb, _ := newTestModel(t, config.Connection{ID: "c", Name: "dev", Driver: "postgres"})
	setSQL(tb, "")
	for _, w := range []string{"case", " ", "end", " ", "up", " ", "esc"} {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(w)}
		if w == " " {
			msg = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")}
		}
		_, cmd := m.Update(msg)
		drain(m, cmd)
	}
	if got := tb.editor.Value(); got != "case end up esc" {
		t.Fatalf("editor = %q", got)
	}
}
