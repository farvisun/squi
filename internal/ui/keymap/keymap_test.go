package keymap

import (
	"strings"
	"testing"
)

func TestDefaultsAreUnique(t *testing.T) {
	seen := map[string]bool{}
	byKey := map[Scope]map[string]string{}
	for _, b := range Defaults {
		if seen[b.Action] {
			t.Errorf("duplicate action %s", b.Action)
		}
		seen[b.Action] = true
		if len(b.Keys) == 0 || b.Desc == "" {
			t.Errorf("%s: needs keys and a description", b.Action)
		}
		if b.Scope != Global && !strings.HasPrefix(b.Action, string(b.Scope)+".") {
			t.Errorf("%s: scoped actions are prefixed with their scope", b.Action)
		}
		if byKey[b.Scope] == nil {
			byKey[b.Scope] = map[string]string{}
		}
		for _, k := range b.Keys {
			if o, ok := byKey[b.Scope][k]; ok {
				t.Errorf("key %q of %s already bound to %s", k, b.Action, o)
			}
			byKey[b.Scope][k] = b.Action
		}
	}
	m := Default()
	if m.Action(Global, "f5") != "run" || m.Action(Results, "f5") != "results.rerun" || m.Action(Explorer, "*") != "explorer.pin" {
		t.Fatal("default lookups")
	}
}

func TestOverrides(t *testing.T) {
	m, warns := New(map[string][]string{
		"run":               {"Ctrl+R", "F5"},
		"analyze":           {},
		"results.follow_fk": {"g"},
		"explain":           {"f8"}, // taken from analyze, which is unbound anyway
		"no_such_action":    {"x"},
		"results.sort":      {"S"},
		"results.load_all":  {"S"}, // conflicts with the override above
	})
	if m.Action(Global, "ctrl+r") != "run" || m.Action(Global, "f5") != "run" || m.Action(Global, "ctrl+j") != "" {
		t.Fatalf("run keys: %v", m.Keys("run"))
	}
	if len(m.Keys("analyze")) != 0 || m.Action(Global, "f8") != "explain" || m.Action(Global, "f7") != "" {
		t.Fatalf("explain/analyze: %v %v", m.Keys("explain"), m.Keys("analyze"))
	}
	if m.Action(Results, "g") != "results.follow_fk" || m.Action(Results, ">") != "" {
		t.Fatal("scoped override")
	}
	if m.Action(Explorer, "s") != "explorer.insert_select" {
		t.Fatal("other scopes keep their keys")
	}
	joined := strings.Join(warns, "\n")
	if !strings.Contains(joined, `unknown action "no_such_action"`) || !strings.Contains(joined, `"S" is bound to both`) {
		t.Fatalf("warnings: %v", warns)
	}
	if got := m.Display("run"); got != "ctrl+r · F5" {
		t.Fatalf("display: %q", got)
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"Ctrl+R": "ctrl+r", "F5": "f5", "Y": "Y", "y": "y", "space": " ", "ctrl+space": "ctrl+ ",
		"Alt+Enter": "alt+enter", " shift+tab ": "shift+tab", "alt+X": "alt+X",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q want %q", in, got, want)
		}
	}
}
