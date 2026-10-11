package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/farvisun/squi/internal/config"
	"github.com/farvisun/squi/internal/db"
	"github.com/farvisun/squi/internal/secrets"
	"github.com/farvisun/squi/internal/ui/keymap"
)

func TestKeyOverrides(t *testing.T) {
	m, tb, fc := newTestModel(t, config.Connection{ID: "c", Name: "dev", Driver: "postgres", Database: "app"})
	m.keys, _ = keymap.New(map[string][]string{"run": {"ctrl+r"}, "results.follow_fk": {"g"}, "results.sort": {"S"}})
	setSQL(tb, "SELECT 1 AS x")
	key(m, "f5") // unbound now
	if len(fc.statements()) != 0 {
		t.Fatalf("F5 still runs: %v", fc.statements())
	}
	drain(m, m.handleKey(keyMsgFor("ctrl+r")))
	if got := fc.statements(); len(got) != 1 {
		t.Fatalf("ctrl+r should run: %v", got)
	}
	openBrowse(t, m, tb, "users")
	key(m, "S")
	if got := lastStatement(fc); !strings.HasSuffix(got, `ORDER BY "id"`) {
		t.Fatalf("S should sort: %s", got)
	}
	m.showHelp()
	help := m.modals[len(m.modals)-1].(*textModal).tv.Raw()
	if !strings.Contains(help, "ctrl+r · ctrl+enter") {
		t.Fatal("help should show the overridden run key")
	}
}

func TestTypingKeysDoNotTriggerGlobals(t *testing.T) {
	m, tb, _ := newTestModel(t, config.Connection{ID: "c", Name: "dev", Driver: "postgres"})
	if !tb.editor.Inserting() {
		t.Fatal("new tabs start in insert mode")
	}
	key(m, "?")
	if len(m.modals) != 0 || !strings.Contains(tb.editor.Value(), "?") {
		t.Fatalf("? must be typed, not open help (modals=%d, text=%q)", len(m.modals), tb.editor.Value())
	}
}

func TestKeyWarningsShownAtStart(t *testing.T) {
	store := config.NewStore(t.TempDir())
	st := config.DefaultSettings()
	st.Keys = map[string][]string{"bogus": {"x"}}
	if err := store.SaveSettings(st); err != nil {
		t.Fatal(err)
	}
	m, err := New(Options{Store: store, Secrets: &secrets.Memory{}, Bridge: &Bridge{}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.startErr, `unknown action "bogus"`) {
		t.Fatalf("startErr = %q", m.startErr)
	}
}

func TestCopyWherePredicates(t *testing.T) {
	m, tb, _ := newTestModel(t, config.Connection{ID: "c", Name: "dev", Driver: "postgres", Database: "app"})
	r := openBrowse(t, m, tb, "orders")
	r.setDetail(&db.TableDetail{Columns: []db.Column{{Name: "id", IsPK: true}}})
	key(m, "l") // user_id
	key(m, "W")
	pm, ok := m.modals[len(m.modals)-1].(*pickerModal)
	if !ok {
		t.Fatalf("expected picker, got %T", m.modals[len(m.modals)-1])
	}
	var got []string
	for _, it := range pm.picker.Items() {
		got = append(got, it.Value.(string))
	}
	want := []string{`"user_id" = '7'`, `"id" = '1'`}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("predicates: %v", got) // one distinct user_id → no IN list
	}
	pred, n := columnIn(db.Dialect{Driver: db.Postgres}, "id", r, 0)
	if n != 30 || !strings.HasPrefix(pred, `"id" IN ('1', '2', `) {
		t.Fatalf("IN list: %d %s", n, abbrev(pred, 40))
	}
}

type estimatingConn struct{ *fakeConn }

func (estimatingConn) EstimateRows(context.Context, db.ObjectRef) (int64, bool, bool, error) {
	return 1234567, false, true, nil
}

func TestCountRows(t *testing.T) {
	m, tb, fc := newTestModel(t, config.Connection{ID: "c", Name: "dev", Driver: "postgres", Database: "app"})
	tb.ac.conn = estimatingConn{fc}
	openBrowse(t, m, tb, "users")
	key(m, "#")
	if !strings.Contains(m.toastText, "≈ 1,234,567 rows (estimate)") {
		t.Fatalf("estimate toast: %q", m.toastText)
	}
	key(m, "#")
	if got := lastStatement(fc); got != `SELECT COUNT(*) FROM "public"."users"` {
		t.Fatalf("exact count: %s", got)
	}
	if m.jobs != 0 {
		t.Fatalf("jobs = %d", m.jobs)
	}
	if groupDigits(-1234) != "-1,234" || groupDigits(999) != "999" {
		t.Fatal("groupDigits")
	}
}
