package ui

import (
	"strings"
	"testing"

	"github.com/farvisun/squi/internal/config"
	"github.com/farvisun/squi/internal/db"
)

func lastStatement(fc *fakeConn) string {
	st := fc.statements()
	if len(st) == 0 {
		return ""
	}
	return st[len(st)-1]
}

func submitPrompt(t *testing.T, m *Model, v string) {
	t.Helper()
	pm, ok := m.modals[len(m.modals)-1].(*promptModal)
	if !ok {
		t.Fatalf("expected prompt, got %T", m.modals[len(m.modals)-1])
	}
	pm.input.SetValue(v)
	key(m, "enter")
}

func openBrowse(t *testing.T, m *Model, tb *tab, name string) *resultPanel {
	t.Helper()
	drain(m, m.openTableData(db.ObjectRef{Database: "app", Schema: "public", Name: name, Kind: db.KindTable}))
	r, ok := tb.out.current().(*resultPanel)
	if !ok || r.browse == nil {
		t.Fatalf("expected a browse panel, got %T", tb.out.current())
	}
	return r
}

func TestBrowseServerFilterAndSort(t *testing.T) {
	m, tb, fc := newTestModel(t, config.Connection{ID: "c", Name: "dev", Driver: "postgres", Database: "app"})
	openBrowse(t, m, tb, "users")
	if got := lastStatement(fc); got != `SELECT * FROM "public"."users"` {
		t.Fatalf("open: %s", got)
	}
	key(m, "/")
	submitPrompt(t, m, "email = 'x' -- note")
	if got := lastStatement(fc); got != `SELECT * FROM "public"."users" WHERE (email = 'x')` {
		t.Fatalf("filter: %s", got)
	}
	r := tb.out.current().(*resultPanel)
	if r.browse.Where != "email = 'x'" || !strings.Contains(m.View(), "WHERE email = 'x'") {
		t.Fatalf("filter state: %+v", r.browse)
	}
	key(m, "l", "s") // sort by email
	if got := lastStatement(fc); got != `SELECT * FROM "public"."users" WHERE (email = 'x') ORDER BY "email"` {
		t.Fatalf("sort: %s", got)
	}
	r = tb.out.current().(*resultPanel)
	if _, col := r.grid.Cursor(); col != 1 {
		t.Fatalf("cursor column lost: %d", col)
	}
	key(m, "s")
	if got := lastStatement(fc); !strings.HasSuffix(got, `ORDER BY "email" DESC`) {
		t.Fatalf("sort desc: %s", got)
	}
	key(m, "s")
	if got := lastStatement(fc); strings.Contains(got, "ORDER BY") {
		t.Fatalf("sort off: %s", got)
	}
	key(m, "esc") // clears the server filter
	if got := lastStatement(fc); got != `SELECT * FROM "public"."users"` {
		t.Fatalf("clear: %s", got)
	}
	key(m, "r") // a re-run keeps the browse state
	if r := tb.out.current().(*resultPanel); r.browse == nil {
		t.Fatal("re-run lost the browse state")
	}
}

func TestBrowseRejectsUnsafeCondition(t *testing.T) {
	m, tb, fc := newTestModel(t, config.Connection{ID: "c", Name: "dev", Driver: "postgres", Database: "app"})
	openBrowse(t, m, tb, "users")
	n := len(fc.statements())
	key(m, "/")
	submitPrompt(t, m, "1 = 1; DROP TABLE users")
	if len(fc.statements()) != n || m.toastLevel != toastWarn {
		t.Fatalf("unsafe condition ran: %v (toast %q)", fc.statements(), m.toastText)
	}
}

func TestLocalFilterStillAvailable(t *testing.T) {
	m, tb, fc := newTestModel(t, config.Connection{ID: "c", Name: "dev", Driver: "postgres", Database: "app"})
	r := openBrowse(t, m, tb, "users")
	n := len(fc.statements())
	key(m, "f")
	submitPrompt(t, m, "a@b")
	if len(fc.statements()) != n || r.grid.Filter() != "a@b" {
		t.Fatalf("local filter: ran=%v filter=%q", fc.statements()[n:], r.grid.Filter())
	}
}

func TestFollowForeignKeyAndBack(t *testing.T) {
	m, tb, fc := newTestModel(t, config.Connection{ID: "c", Name: "dev", Driver: "postgres", Database: "app"})
	openBrowse(t, m, tb, "orders")
	key(m, "j", "j", "l") // row 3, user_id
	key(m, ">")
	if got := lastStatement(fc); got != `SELECT * FROM "public"."users" WHERE ("id" = '7')` {
		t.Fatalf("follow: %s", got)
	}
	r := tb.out.current().(*resultPanel)
	if r.name != "users" || r.source.Name != "users" || len(r.back) != 1 || len(tb.out.results()) != 1 {
		t.Fatalf("after follow: name=%s source=%+v back=%d panels=%d", r.name, r.source, len(r.back), len(tb.out.results()))
	}
	key(m, "backspace")
	if got := lastStatement(fc); got != `SELECT * FROM "public"."orders"` {
		t.Fatalf("back: %s", got)
	}
	r = tb.out.current().(*resultPanel)
	row, col := r.grid.Cursor()
	if r.name != "orders" || len(r.back) != 0 || row != 2 || col != 1 {
		t.Fatalf("after back: name=%s back=%d cursor=%d,%d", r.name, len(r.back), row, col)
	}
	key(m, "h", ">") // id is not a foreign key
	if !strings.Contains(m.toastText, "No foreign key on id") {
		t.Fatalf("toast = %q", m.toastText)
	}
}

func TestReferencingRows(t *testing.T) {
	m, tb, fc := newTestModel(t, config.Connection{ID: "c", Name: "dev", Driver: "postgres", Database: "app"})
	openBrowse(t, m, tb, "users")
	key(m, "<")
	if _, ok := m.modals[len(m.modals)-1].(*pickerModal); !ok {
		t.Fatalf("expected picker, got %T", m.modals[len(m.modals)-1])
	}
	key(m, "enter")
	if got := lastStatement(fc); got != `SELECT * FROM "public"."orders" WHERE ("user_id" = '1')` {
		t.Fatalf("referencing: %s", got)
	}
	if r := tb.out.current().(*resultPanel); r.name != "orders" || len(r.back) != 1 {
		t.Fatalf("panel: %s back=%d", r.name, len(r.back))
	}
}

func TestFKTargetMySQL(t *testing.T) {
	d := db.Dialect{Driver: db.MySQL}
	got := fkTarget(d, db.ObjectRef{Database: "shop", Schema: "shop", Name: "orders"}, "crm", "customers")
	if got.Database != "crm" || got.Schema != "crm" || got.Name != "customers" {
		t.Fatalf("%+v", got)
	}
	pg := fkTarget(db.Dialect{Driver: db.Postgres}, db.ObjectRef{Database: "app", Schema: "sales", Name: "orders"}, "", "users")
	if pg.Database != "app" || pg.Schema != "sales" {
		t.Fatalf("%+v", pg)
	}
}
