package ui

import (
	"testing"

	"github.com/farvisun/squi/internal/config"
)

func TestQueryParametersAskThenRun(t *testing.T) {
	m, tb, fc := newTestModel(t, config.Connection{ID: "c", Name: "dev", Driver: "postgres"})
	setSQL(tb, "SELECT * FROM users WHERE id = :id AND name = :name OR id = :id")
	key(m, "f5")
	pm, ok := m.modals[len(m.modals)-1].(*paramsModal)
	if !ok || len(pm.names) != 2 {
		t.Fatalf("expected a parameters form for :id and :name, got %#v", m.modals)
	}
	pm.form.SetValue(":id", "42")
	key(m, "enter") // next field
	key(m, "enter") // :name is empty: warns instead of running
	if len(m.modals) != 1 || pm.warn == "" {
		t.Fatalf("empty value must not run: modals=%d warn=%q", len(m.modals), pm.warn)
	}
	pm.form.SetValue(":name", "'Ada'")
	key(m, "enter")
	want := "SELECT * FROM users WHERE id = 42 AND name = 'Ada' OR id = 42"
	found := false
	for _, s := range fc.statements() {
		found = found || s == want
	}
	if !found || len(m.modals) != 0 {
		t.Fatalf("bound statement not run: %v", fc.statements())
	}

	// The values are offered again next time.
	key(m, "f5")
	pm = m.modals[len(m.modals)-1].(*paramsModal)
	if pm.form.Value(":id") != "42" || pm.form.Value(":name") != "'Ada'" {
		t.Fatalf("values not remembered: %q %q", pm.form.Value(":id"), pm.form.Value(":name"))
	}
	key(m, "esc")
	if len(m.modals) != 0 {
		t.Fatal("esc must close the form")
	}
}

func TestQueryParametersOff(t *testing.T) {
	m, tb, fc := newTestModel(t, config.Connection{ID: "c", Name: "dev", Driver: "postgres"})
	m.settings.QueryParameters = false
	setSQL(tb, "SELECT :id")
	key(m, "f5")
	if len(m.modals) != 0 {
		t.Fatal("no form when query_parameters is off")
	}
	found := false
	for _, s := range fc.statements() {
		found = found || s == "SELECT :id"
	}
	if !found {
		t.Fatalf("statement not run as written: %v", fc.statements())
	}
}

func TestNoParametersRunsDirectly(t *testing.T) {
	m, tb, _ := newTestModel(t, config.Connection{ID: "c", Name: "dev", Driver: "postgres"})
	setSQL(tb, "SELECT created_at::date FROM users")
	key(m, "f5")
	if len(m.modals) != 0 {
		t.Fatal("a cast is not a parameter")
	}
}
