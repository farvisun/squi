package ui

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/farvisun/squi/internal/config"
)

func TestEditorCommand(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "code -w")
	if got := editorCommand(); !reflect.DeepEqual(got, []string{"code", "-w"}) {
		t.Fatalf("EDITOR: %v", got)
	}
	t.Setenv("VISUAL", "nvim")
	if got := editorCommand(); !reflect.DeepEqual(got, []string{"nvim"}) {
		t.Fatalf("VISUAL wins: %v", got)
	}
}

func TestExternalEditApplies(t *testing.T) {
	m, tb, _ := newTestModel(t, config.Connection{ID: "c", Name: "dev", Driver: "postgres"})
	setSQL(tb, "select 1")
	path := filepath.Join(t.TempDir(), "q.sql")
	if err := os.WriteFile(path, []byte("SELECT 2\nFROM dual\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	drain(m, m.handleExternalEdit(externalEditMsg{tab: tb, path: path, orig: "select 1"}))
	if got := tb.editor.Value(); got != "SELECT 2\nFROM dual" {
		t.Fatalf("editor = %q", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("temp file should be removed")
	}
}

func TestExternalEditKeepsConcurrentChanges(t *testing.T) {
	m, tb, _ := newTestModel(t, config.Connection{ID: "c", Name: "dev", Driver: "postgres"})
	setSQL(tb, "select 3")
	path := filepath.Join(t.TempDir(), "q.sql")
	_ = os.WriteFile(path, []byte("select 2\n"), 0o600)
	drain(m, m.handleExternalEdit(externalEditMsg{tab: tb, path: path, orig: "select 1"}))
	if tb.editor.Value() != "select 3" || m.toastLevel != toastWarn {
		t.Fatalf("editor = %q toast=%q", tb.editor.Value(), m.toastText)
	}
}

func TestFormatCommand(t *testing.T) {
	m, tb, _ := newTestModel(t, config.Connection{ID: "c", Name: "dev", Driver: "postgres"})
	tb.editor.SetValue("select 1;\nselect a, b from t where x = 1 and y = 2;")
	tb.editor.SetCursorOffset(len("select 1;\nsel"))
	drain(m, m.runCommand("format"))
	want := "select 1;\nSELECT\n  a,\n  b\nFROM t\nWHERE x = 1\n  AND y = 2;"
	if got := tb.editor.Value(); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	drain(m, m.runCommand("format"))
	if !strings.Contains(m.toastText, "Already formatted") {
		t.Fatalf("toast = %q", m.toastText)
	}
}
