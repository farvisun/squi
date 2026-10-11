package ui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/farvisun/squi/internal/config"
	"github.com/farvisun/squi/internal/ui/components"
	"github.com/farvisun/squi/internal/ui/keymap"
	"github.com/farvisun/squi/internal/ui/theme"
)

// command is an action reachable from the ':' line and the palette.
type command struct {
	name    string
	aliases []string
	args    string
	desc    string
	keys    string
	needTab bool
	run     func(m *Model, t *tab, args string) tea.Cmd
}

var commands []command

func init() {
	commands = []command{
		{name: "run", aliases: []string{"r"}, desc: "Run statement under cursor / selection", keys: "@run · ctrl+enter · space r", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd { return m.runCurrent(t) }},
		{name: "runall", aliases: []string{"ra"}, desc: "Run all statements", keys: "@run_all · space a", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd { return m.runAll(t) }},
		{name: "explain", desc: "Explain statement under cursor", keys: "@explain · space e", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd { return m.explain(t, false) }},
		{name: "analyze", desc: "Explain analyze (executes the statement)", keys: "@analyze", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd { return m.explain(t, true) }},
		{name: "cancel", desc: "Cancel the running query", keys: "@cancel", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd { return m.cancel(t) }},
		{name: "commit", desc: "Commit the transaction", keys: "@tx_menu c", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd { return m.txAction(t, "COMMIT") }},
		{name: "rollback", desc: "Roll back the transaction", keys: "@tx_menu r", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd { return m.txAction(t, "ROLLBACK") }},
		{name: "begin", desc: "Begin a transaction", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd { return m.txAction(t, "BEGIN") }},
		{name: "tx", desc: "Toggle manual transaction mode", keys: "@tx_menu m", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd { return m.txChoice(t, "MANUAL") }},
		{name: "w", aliases: []string{"save"}, desc: "Save pending grid edits", keys: "@results.save", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd {
				if r, ok := t.out.current().(*resultPanel); ok {
					return m.saveEdits(t, r)
				}
				for _, r := range t.out.results() {
					if r.dirty() {
						return m.saveEdits(t, r)
					}
				}
				return m.toast("No pending changes", toastInfo)
			}},
		{name: "timeout", args: "[30s|2m|0|default]", desc: "Statement timeout of this tab (0 = none); no argument shows it", needTab: true,
			run: func(m *Model, t *tab, a string) tea.Cmd { return m.setTimeout(t, strings.TrimSpace(a)) }},
		{name: "use", args: "[database] [schema]", desc: "Switch database (and schema) of this tab; no argument lists them", needTab: true,
			run: func(m *Model, t *tab, a string) tea.Cmd {
				f := strings.Fields(a)
				if len(f) == 0 {
					return m.databasePicker(t)
				}
				s := ""
				if len(f) > 1 {
					s = f[1]
				}
				return m.useDatabase(f[0], s)
			}},
		{name: "export", desc: "Export the current result", keys: "@results.export (in results)", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd {
				if r, ok := t.out.current().(*resultPanel); ok {
					return m.exportResult(r)
				}
				return m.toast("Select a result tab first", toastWarn)
			}},
		{name: "history", desc: "Search query history", keys: "ctrl+r · space h", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd { return m.historyPicker(t) }},
		{name: "consoles", aliases: []string{"dg"}, desc: "Open a DataGrip query console or query file of this connection", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd { return m.consolePicker(t) }},
		{name: "snippets", desc: "Insert a saved snippet", keys: "space s", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd { return m.snippetPicker(t) }},
		{name: "snippet", args: "<name>", desc: "Save statement under cursor as a snippet", needTab: true,
			run: func(m *Model, t *tab, a string) tea.Cmd { return m.saveSnippet(t, strings.TrimSpace(a)) }},
		{name: "admin", aliases: []string{"processes", "ps"}, desc: "Server processes (cancel / kill)", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd { return m.openAdmin(adminProcesses) }},
		{name: "variables", aliases: []string{"vars"}, desc: "Server variables", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd { return m.openAdmin(adminVariables) }},
		{name: "users", desc: "Users / roles and grants", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd { return m.openAdmin(adminUsers) }},
		{name: "sizes", desc: "Database and table sizes", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd { return m.openAdmin(adminSizes) }},
		{name: "edit", aliases: []string{"e"}, desc: "Edit the query in $VISUAL / $EDITOR", keys: "@external_editor · space E", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd { return m.openExternalEditor(t) }},
		{name: "format", aliases: []string{"fmt"}, desc: "Format the statement under the cursor (or the selection)", keys: "space f", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd { return m.formatSQL(t) }},
		{name: "clear", desc: "Clear the editor", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd { t.editor.SetValue(""); return nil }},
		{name: "refresh", desc: "Refresh explorer", keys: "@explorer.refresh (in explorer)", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd {
				t.ac.cacheLoaded = nil
				return tea.Batch(t.ac.explorer.load(nil), m.ensureCache(t))
			}},
		{name: "open", aliases: []string{"connect"}, args: "<name|url>", desc: "Open a saved connection or a connection URL in a new tab",
			run: func(m *Model, t *tab, a string) tea.Cmd {
				a = strings.TrimSpace(a)
				switch {
				case a == "":
					return m.newTabPicker()
				case config.IsURL(a):
					return m.connectURL(a)
				}
				for _, c := range m.conns {
					if strings.EqualFold(c.Name, a) || c.ID == a {
						return m.connect(c)
					}
				}
				return m.toast("No connection named "+a, toastError)
			}},
		{name: "connections", aliases: []string{"conn", "c"}, desc: "Connection manager", keys: "@connections",
			run: func(m *Model, t *tab, _ string) tea.Cmd { m.openConnections(); return nil }},
		{name: "new", aliases: []string{"tabnew"}, desc: "Open a new tab", keys: "@new_tab",
			run: func(m *Model, t *tab, _ string) tea.Cmd { return m.newTabPicker() }},
		{name: "close", aliases: []string{"q"}, desc: "Close the tab", keys: "@window q", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd { return m.confirmCloseTab(t) }},
		{name: "tabn", desc: "Next tab", keys: "@next_tab",
			run: func(m *Model, t *tab, _ string) tea.Cmd {
				if len(m.tabs) > 0 {
					m.cur = (m.cur + 1) % len(m.tabs)
				}
				return nil
			}},
		{name: "tabp", desc: "Previous tab", keys: "@prev_tab",
			run: func(m *Model, t *tab, _ string) tea.Cmd {
				if len(m.tabs) > 0 {
					m.cur = (m.cur - 1 + len(m.tabs)) % len(m.tabs)
				}
				return nil
			}},
		{name: "zoom", desc: "Zoom the focused pane", keys: "@window z",
			run: func(m *Model, t *tab, _ string) tea.Cmd { m.zoom = !m.zoom; return nil }},
		{name: "set", args: "vim|novim|fetch=N|width=N", desc: "Change a setting (saved)",
			run: func(m *Model, t *tab, a string) tea.Cmd { return m.setOption(strings.TrimSpace(a)) }},
		{name: "keys", desc: "List every key binding and its action name (for settings.toml [keys])",
			run: func(m *Model, t *tab, _ string) tea.Cmd { return m.showKeys() }},
		{name: "help", aliases: []string{"h"}, desc: "Key bindings", keys: "@help",
			run: func(m *Model, t *tab, _ string) tea.Cmd { return m.showHelp() }},
		{name: "quit", aliases: []string{"qa", "exit"}, desc: "Quit squi", keys: "@quit",
			run: func(m *Model, t *tab, _ string) tea.Cmd { return m.quit() }},
	}
}

func findCommand(name string) (command, bool) {
	for _, c := range commands {
		if c.name == name {
			return c, true
		}
		for _, a := range c.aliases {
			if a == name {
				return c, true
			}
		}
	}
	return command{}, false
}

func (m *Model) runCommand(line string) tea.Cmd {
	line = strings.TrimSpace(strings.TrimPrefix(line, ":"))
	if line == "" {
		return nil
	}
	name, args, _ := strings.Cut(line, " ")
	c, ok := findCommand(name)
	if !ok {
		return m.toast("Unknown command: "+name, toastError)
	}
	t := m.currentTab()
	if c.needTab && t == nil {
		return m.toast("Open a connection first", toastWarn)
	}
	return c.run(m, t, args)
}

func (m *Model) openCmdline(initial string) {
	ti := textinput.New()
	ti.Prompt = ":"
	ti.SetValue(initial)
	ti.Focus()
	var names []string
	for _, c := range commands {
		names = append(names, c.name)
	}
	ti.SetSuggestions(names)
	ti.ShowSuggestions = true
	m.cmdline = &ti
}

func (m *Model) updateCmdline(km tea.KeyMsg) tea.Cmd {
	switch km.String() {
	case "esc", "ctrl+c":
		m.cmdline = nil
		return nil
	case "enter":
		line := m.cmdline.Value()
		m.cmdline = nil
		return m.runCommand(line)
	}
	var cmd tea.Cmd
	*m.cmdline, cmd = m.cmdline.Update(km)
	return cmd
}

func (m *Model) palette() tea.Cmd {
	var items []components.PickerItem
	for _, c := range commands {
		title := c.desc
		detail := ":" + c.name
		if c.keys != "" {
			detail += "  " + m.keyText(c.keys)
		}
		items = append(items, components.PickerItem{Title: title, Detail: detail, Value: c})
	}
	m.push(newPickerModal("Command palette", items, func(it components.PickerItem) tea.Cmd {
		c := it.Value.(command)
		if c.args != "" {
			m.openCmdline(c.name + " ")
			return nil
		}
		return m.runCommand(c.name)
	}))
	return nil
}

func (m *Model) setOption(a string) tea.Cmd {
	key, val, _ := strings.Cut(a, "=")
	switch key {
	case "vim", "novim":
		m.settings.VimMode = key == "vim"
		for _, t := range m.tabs {
			v := t.editor.Value()
			t.editor = components.NewEditor(m.settings.VimMode, components.LexerFor(t.ac.cfg.Driver))
			t.editor.SetValue(v)
		}
	case "fetch":
		n, err := strconv.Atoi(val)
		if err != nil || n <= 0 {
			return m.toast("Usage: :set fetch=1000", toastWarn)
		}
		m.settings.FetchSize = n
	case "width":
		n, err := strconv.Atoi(val)
		if err != nil || n <= 0 {
			return m.toast("Usage: :set width=40", toastWarn)
		}
		m.settings.MaxColumnWidth = n
	default:
		return m.toast("Usage: :set vim|novim|fetch=N|width=N", toastWarn)
	}
	if err := m.store.SaveSettings(m.settings); err != nil {
		return m.toast(err.Error(), toastError)
	}
	return m.toast("Setting saved", toastInfo)
}

// setTimeout changes the statement timeout of t ("default" restores the
// connection's).
func (m *Model) setTimeout(t *tab, a string) tea.Cmd {
	describe := func(d time.Duration) string {
		if d <= 0 {
			return "no statement timeout"
		}
		return "statement timeout " + fmtTimeout(d)
	}
	switch a {
	case "":
		src := "connection default"
		if t.timeoutSet {
			src = "set with :timeout"
		}
		return m.toast(fmt.Sprintf("This tab has %s (%s)", describe(t.queryTimeout()), src), toastInfo)
	case "default", "reset":
		t.timeout, t.timeoutSet = 0, false
	default:
		d, err := config.ParseTimeout(a)
		if err != nil {
			return m.toast(err.Error(), toastWarn)
		}
		t.timeout, t.timeoutSet = d, true
	}
	return m.toast("This tab now has "+describe(t.queryTimeout()), toastInfo)
}

// ---- help ----

// helpSections is the key reference. In the key column, @action expands to
// the action's current keys (see package keymap), so overrides show up.
var helpSections = []struct {
	title string
	keys  [][2]string
}{
	{"Global", [][2]string{
		{"@help", "this help"}, {"@palette", "command palette"}, {"@command_line", "command line (outside insert mode)"},
		{"@connections", "connection manager"}, {"@new_tab", "new tab"}, {"alt+1…9", "go to tab"},
		{"@next_tab", "next tab"}, {"@prev_tab", "previous tab"},
		{"@focus_next / @focus_prev", "cycle focus explorer → editor → results"},
		{"@window h/j/k/l", "focus pane · z zoom · </> explorer width · -/+ editor height · q close tab"},
		{"@toggle_explorer", "toggle explorer"}, {"@tx_menu", "transaction menu (commit / rollback / manual mode)"},
		{"@cancel", "cancel running query"}, {":timeout 2m · 0", "statement timeout of the tab (prod defaults to 30s)"},
		{"@quit", "quit"},
	}},
	{"Editor", [][2]string{
		{"@run · ctrl+enter", "run statement under cursor (or selection); ctrl+j if ctrl+enter is not passed by the terminal"},
		{"@run_all", "run all statements"}, {"@explain / @analyze", "explain / explain analyze"},
		{"@external_editor · :edit", "edit the query in $VISUAL / $EDITOR"},
		{":format", "format the statement under the cursor (or selection)"},
		{"tab · ctrl+n · ctrl+space", "autocomplete (↑↓ choose, tab/enter accept)"},
		{"ctrl+r (insert mode)", "search history"},
		{"vim normal mode", "hjkl w b e 0 $ gg G · i a o O · v V · d c y + motion · dd yy p u ctrl+r · >> <<"},
		{"space r / a / e / h / s / E / f", "run · run all · explain · history · snippets · $EDITOR · format"},
		{"non-vim mode", "shift+arrows select · ctrl+c/x/v copy/cut/paste · ctrl+z/y undo/redo"},
	}},
	{"Explorer", [][2]string{
		{"j/k · l/h", "move · expand / collapse"}, {"@explorer.open", "open table data"},
		{"@explorer.open_tab · double-click", "open table data in a new tab"},
		{"@explorer.details / @explorer.ddl", "table details / DDL"}, {"@explorer.insert_select", "insert SELECT into editor"},
		{"@explorer.use · :use", "use this database/schema in the tab (:use alone lists databases)"},
		{"@explorer.export / @explorer.import", "export table / import CSV"},
		{"@explorer.filter", "filter"}, {"@explorer.refresh", "refresh"}, {"@explorer.copy_name", "copy name"},
		{"@explorer.pin", "pin / unpin in ★ Favorites"},
	}},
	{"Results", [][2]string{
		{"hjkl · gg G · ctrl+d/u", "navigate (more rows load as you scroll; @results.load_all loads all)"},
		{"@output.prev / @output.next", "previous / next output tab · @output.close closes it"},
		{"@results.inspect / @results.record", "inspect cell / view row as record"},
		{"@results.copy_cell / @results.copy_row / @results.copy_menu", "copy cell / row / copy menu (CSV, JSON, INSERT, Markdown…)"},
		{"@results.copy_where", "copy a WHERE predicate (cell, row key or the column's loaded values)"},
		{"@results.filter · @results.sort", "filter rows · sort by column (tables opened from the explorer: WHERE / ORDER BY on the server)"},
		{"@results.local_filter · @results.clear_filter", "filter loaded rows only · clear the filter"},
		{"@results.follow_fk · @results.referencing", "follow the foreign key of the cell · rows referencing this row"},
		{"@results.back", "back to the previous table after a foreign-key jump"},
		{"@results.bookmark", "bookmark this table view (filter + sort) in ★ Favorites"},
		{"@results.count", "row count: estimate, press again for an exact COUNT(*)"},
		{"@results.export", "export"}, {"@results.rerun", "re-run"},
		{"@results.edit · @results.add_row · @results.delete_row×2 · @results.revert_row / @results.revert_all", "edit cell · add row · delete row · revert row / all"},
		{"@results.save", "review generated SQL and save edits"},
	}},
	{"Detail / Admin / Plan", [][2]string{
		{"1…9 · < >", "switch sub-tab"}, {"o", "open table data (detail)"},
		{"c / K", "cancel query / kill session (processes)"}, {"a", "toggle auto-refresh"},
		{"enter", "expand plan node · user grants"}, {"r", "raw plan / refresh"},
	}},
	{"Custom keys", [][2]string{
		{"settings.toml [keys]", `run = ["ctrl+r", "f5"] · "results.follow_fk" = ["g"] · analyze = [] — names as in :keys`},
	}},
}

var keyRef = regexp.MustCompile(`@([a-z_]+(?:\.[a-z_]+)?)`)

// keyText expands @action references to the action's keys.
func (m *Model) keyText(s string) string {
	return keyRef.ReplaceAllStringFunc(s, func(ref string) string {
		if d := m.keys.Display(ref[1:]); d != "" {
			return d
		}
		return "(unbound)"
	})
}

// showKeys lists the effective bindings with their action names.
func (m *Model) showKeys() tea.Cmd {
	var lines, raw []string
	scope := keymap.Scope("")
	for _, b := range m.keys.Bindings() {
		if b.Scope != scope {
			scope = b.Scope
			if len(lines) > 0 {
				lines, raw = append(lines, ""), append(raw, "")
			}
			lines = append(lines, theme.Title.Render(string(scope)))
			raw = append(raw, string(scope))
		}
		keys := m.keys.Display(b.Action)
		if keys == "" {
			keys = "(unbound)"
		}
		lines = append(lines, "  "+theme.Key.Render(components.Fit(b.Action, 24))+" "+components.Fit(keys, 28)+" "+theme.Dim.Render(b.Desc))
		raw = append(raw, fmt.Sprintf("  %-24s %-28s %s", b.Action, keys, b.Desc))
	}
	tm := newTextModal("Key bindings — override in settings.toml [keys]", "", "")
	tm.tv.SetStyledLines(strings.Join(raw, "\n"), lines)
	tm.height = 40
	m.push(tm)
	return nil
}

func (m *Model) showHelp() tea.Cmd {
	var lines, raw []string
	for _, s := range helpSections {
		lines = append(lines, theme.Title.Render(s.title))
		raw = append(raw, s.title)
		for _, k := range s.keys {
			keys, desc := m.keyText(k[0]), m.keyText(k[1])
			w := max(26, min(40, len([]rune(keys))))
			lines = append(lines, "  "+theme.Key.Render(components.Fit(keys, w))+" "+desc)
			raw = append(raw, fmt.Sprintf("  %-*s %s", w, keys, desc))
		}
		lines = append(lines, "")
		raw = append(raw, "")
	}
	tm := newTextModal("squi — keys", "", "")
	tm.tv.SetStyledLines(strings.Join(raw, "\n"), lines)
	tm.height = 40
	m.push(tm)
	return nil
}
