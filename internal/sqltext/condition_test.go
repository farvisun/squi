package sqltext

import (
	"testing"

	"github.com/farvisun/squi/internal/db"
)

func TestCleanCondition(t *testing.T) {
	ok := map[string]string{
		"":                                    "",
		"  status = 'paid'  ":                 "status = 'paid'",
		"a = 1 -- trailing comment":           "a = 1",
		"a = 1 /* x */ AND b = 2":             "a = 1 AND b = 2",
		"name = 'it''s; fine'":                "name = 'it''s; fine'",
		"(a = 1 OR b = 2) AND c IN (1, 2, 3)": "(a = 1 OR b = 2) AND c IN (1, 2, 3)",
		"x = $$a;b$$":                         "x = $$a;b$$",
		"note = ''":                           "note = ''",
	}
	for in, want := range ok {
		got, err := CleanCondition(in, db.Postgres)
		if err != nil || got != want {
			t.Errorf("CleanCondition(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if got, err := CleanCondition("a = 1 # mysql comment", db.MySQL); err != nil || got != "a = 1" {
		t.Errorf("mysql comment: %q %v", got, err)
	}
	for _, bad := range []string{
		"1 = 1; DROP TABLE users", "a = 1)", "(a = 1", "a = 1) OR (1 = 1", "name = 'open", "x = $$open",
	} {
		if _, err := CleanCondition(bad, db.Postgres); err == nil {
			t.Errorf("CleanCondition(%q) accepted", bad)
		}
	}
}
