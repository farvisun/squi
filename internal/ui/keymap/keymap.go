// Package keymap maps keys to named UI actions so key bindings can be
// overridden in settings.toml:
//
//	[keys]
//	run = ["ctrl+r", "f5"]
//	"results.follow_fk" = ["gf"]
//	analyze = []            # unbind
//
// Global actions have plain names; actions of the result grid, the explorer
// and the output tabs are prefixed with their scope. Navigation keys (hjkl,
// gg, arrows…) and the editor's own keys are not remappable.
package keymap

import (
	"fmt"
	"sort"
	"strings"
)

// Scope is where a binding applies.
type Scope string

const (
	Global   Scope = "global"
	Results  Scope = "results"
	Explorer Scope = "explorer"
	Output   Scope = "output"
)

// Binding is one action and its keys.
type Binding struct {
	Action string
	Scope  Scope
	Keys   []string
	Desc   string
}

// Defaults are the built-in bindings.
var Defaults = []Binding{
	{"help", Global, []string{"f1", "?"}, "key reference"},
	{"palette", Global, []string{"ctrl+p"}, "command palette"},
	{"command_line", Global, []string{":"}, "command line (outside insert mode)"},
	{"connections", Global, []string{"ctrl+o"}, "connection manager"},
	{"new_tab", Global, []string{"ctrl+t"}, "new tab"},
	{"next_tab", Global, []string{"alt+]", "ctrl+pgdown", "alt+right"}, "next tab"},
	{"prev_tab", Global, []string{"alt+[", "ctrl+pgup", "alt+left"}, "previous tab"},
	{"window", Global, []string{"ctrl+w"}, "window prefix: h/j/k/l focus · z zoom · </> explorer width · -/+ editor height · q close tab"},
	{"focus_next", Global, []string{"tab"}, "focus next pane"},
	{"focus_prev", Global, []string{"shift+tab"}, "focus previous pane"},
	{"toggle_explorer", Global, []string{"ctrl+b"}, "show / hide the explorer"},
	{"tx_menu", Global, []string{"ctrl+g"}, "transaction menu (commit / rollback / manual mode)"},
	{"cancel", Global, []string{"ctrl+c"}, "cancel the running query"},
	{"quit", Global, []string{"ctrl+q"}, "quit"},
	{"run", Global, []string{"f5", "ctrl+j", "alt+enter"}, "run statement under cursor (or selection)"},
	{"run_all", Global, []string{"f6"}, "run all statements"},
	{"explain", Global, []string{"f7"}, "explain"},
	{"analyze", Global, []string{"f8"}, "explain analyze (executes the statement)"},
	{"external_editor", Global, []string{"f4"}, "edit the query in $VISUAL / $EDITOR"},

	{"output.next", Output, []string{"]"}, "next output tab"},
	{"output.prev", Output, []string{"["}, "previous output tab"},
	{"output.close", Output, []string{"X"}, "close the output tab"},

	{"results.inspect", Results, []string{"enter"}, "inspect cell"},
	{"results.record", Results, []string{"v"}, "view row as record"},
	{"results.copy_cell", Results, []string{"y"}, "copy cell"},
	{"results.copy_row", Results, []string{"Y"}, "copy row"},
	{"results.copy_menu", Results, []string{"c"}, "copy menu (CSV, JSON, INSERT, Markdown…)"},
	{"results.filter", Results, []string{"/"}, "filter rows (tables: WHERE on the server)"},
	{"results.local_filter", Results, []string{"f"}, "filter loaded rows only"},
	{"results.clear_filter", Results, []string{"esc"}, "clear the filter"},
	{"results.sort", Results, []string{"s"}, "sort by column (tables: ORDER BY on the server)"},
	{"results.export", Results, []string{"x"}, "export"},
	{"results.rerun", Results, []string{"r", "f5"}, "re-run"},
	{"results.load_all", Results, []string{"F"}, "load all rows"},
	{"results.edit", Results, []string{"e", "i"}, "edit cell"},
	{"results.add_row", Results, []string{"o"}, "add row"},
	{"results.delete_row", Results, []string{"d"}, "delete row (press twice)"},
	{"results.revert_row", Results, []string{"u"}, "revert row"},
	{"results.revert_all", Results, []string{"U"}, "revert all edits"},
	{"results.save", Results, []string{"ctrl+s"}, "review generated SQL and save edits"},
	{"results.follow_fk", Results, []string{">"}, "follow the foreign key of the cell"},
	{"results.referencing", Results, []string{"<"}, "rows referencing this row"},
	{"results.back", Results, []string{"backspace"}, "back to the previous table after > or <"},
	{"results.bookmark", Results, []string{"*"}, "bookmark this table view (filter + sort) in ★ Favorites"},
	{"results.copy_where", Results, []string{"W"}, "copy a WHERE predicate: cell, or the column's loaded values"},
	{"results.count", Results, []string{"#"}, "count the table's rows (estimate, then exact on request)"},

	{"explorer.open", Explorer, []string{"enter", "o"}, "open table data"},
	{"explorer.open_tab", Explorer, []string{"t"}, "open table data in a new tab"},
	{"explorer.details", Explorer, []string{"d"}, "table details"},
	{"explorer.ddl", Explorer, []string{"D"}, "DDL"},
	{"explorer.insert_select", Explorer, []string{"s"}, "insert SELECT into the editor"},
	{"explorer.copy_name", Explorer, []string{"y", "c"}, "copy name"},
	{"explorer.export", Explorer, []string{"x"}, "export table"},
	{"explorer.import", Explorer, []string{"i"}, "import CSV"},
	{"explorer.use", Explorer, []string{"u"}, "use this database / schema in the tab"},
	{"explorer.refresh", Explorer, []string{"r", "f5"}, "refresh"},
	{"explorer.filter", Explorer, []string{"/"}, "filter objects"},
	{"explorer.pin", Explorer, []string{"*"}, "pin / unpin in ★ Favorites"},
}

// Map resolves keys to actions.
type Map struct {
	bindings []Binding
	byKey    map[Scope]map[string]string
	index    map[string]int
}

// New returns the default bindings with overrides applied. Overrides
// replace an action's keys; a key taken from another action of the same
// scope is moved. Problems are returned as warnings, never errors.
func New(overrides map[string][]string) (*Map, []string) {
	m := &Map{index: map[string]int{}}
	for i, b := range Defaults {
		b.Keys = append([]string(nil), b.Keys...)
		m.bindings = append(m.bindings, b)
		m.index[b.Action] = i
	}
	var warns []string
	names := make([]string, 0, len(overrides))
	for a := range overrides {
		names = append(names, a)
	}
	sort.Strings(names)
	for _, a := range names {
		i, ok := m.index[a]
		if !ok {
			warns = append(warns, fmt.Sprintf("keys: unknown action %q", a))
			continue
		}
		var keys []string
		for _, k := range overrides[a] {
			if k = Normalize(k); k != "" {
				keys = append(keys, k)
			}
		}
		// take the keys away from other actions of the scope
		for j := range m.bindings {
			o := &m.bindings[j]
			if j == i || o.Scope != m.bindings[i].Scope {
				continue
			}
			kept := o.Keys[:0]
			for _, k := range o.Keys {
				if contains(keys, k) {
					if _, overridden := overrides[o.Action]; overridden {
						warns = append(warns, fmt.Sprintf("keys: %q is bound to both %s and %s; %s wins", k, o.Action, a, a))
					}
					continue
				}
				kept = append(kept, k)
			}
			o.Keys = kept
		}
		m.bindings[i].Keys = keys
	}
	m.byKey = map[Scope]map[string]string{}
	for _, b := range m.bindings {
		if m.byKey[b.Scope] == nil {
			m.byKey[b.Scope] = map[string]string{}
		}
		for _, k := range b.Keys {
			m.byKey[b.Scope][k] = b.Action
		}
	}
	return m, warns
}

// Default returns the built-in bindings.
func Default() *Map {
	m, _ := New(nil)
	return m
}

// Action returns the action bound to key in scope, or "".
func (m *Map) Action(s Scope, key string) string { return m.byKey[s][key] }

// Keys returns the keys of an action.
func (m *Map) Keys(action string) []string {
	if i, ok := m.index[action]; ok {
		return m.bindings[i].Keys
	}
	return nil
}

// Has reports whether key is bound to action.
func (m *Map) Has(action, key string) bool { return contains(m.Keys(action), key) }

// Display renders an action's keys for help text, e.g. "F5 · ctrl+j".
func (m *Map) Display(action string) string {
	keys := m.Keys(action)
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = Pretty(k)
	}
	return strings.Join(out, " · ")
}

// First returns the first key of an action for hints, or "".
func (m *Map) First(action string) string {
	if keys := m.Keys(action); len(keys) > 0 {
		return Pretty(keys[0])
	}
	return ""
}

// Bindings returns the effective bindings in definition order.
func (m *Map) Bindings() []Binding { return m.bindings }

// Normalize canonicalises a key name as Bubble Tea reports it: modifiers
// and named keys in lower case ("Ctrl+R" → "ctrl+r", "F5" → "f5"), single
// characters kept as typed ("Y" stays upper case), "space" → " ".
func Normalize(k string) string {
	k = strings.TrimSpace(k)
	if k == "" {
		return ""
	}
	if len([]rune(k)) == 1 {
		return k
	}
	if strings.EqualFold(k, "space") {
		return " "
	}
	// modifiers and named keys are lower case; a final character keeps its
	// case (alt+X) unless ctrl is held, which terminals report case-blind
	parts := strings.Split(k, "+")
	ctrl := false
	for i, p := range parts {
		if i < len(parts)-1 || len([]rune(p)) > 1 {
			parts[i] = strings.ToLower(p)
			ctrl = ctrl || parts[i] == "ctrl"
		}
	}
	if ctrl {
		parts[len(parts)-1] = strings.ToLower(parts[len(parts)-1])
	}
	if last := parts[len(parts)-1]; strings.EqualFold(last, "space") {
		parts[len(parts)-1] = " "
	}
	return strings.Join(parts, "+")
}

// Pretty renders a key for display.
func Pretty(k string) string {
	switch {
	case k == " ":
		return "space"
	case len(k) >= 2 && k[0] == 'f' && k[1] >= '0' && k[1] <= '9':
		return strings.ToUpper(k)
	}
	return k
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
