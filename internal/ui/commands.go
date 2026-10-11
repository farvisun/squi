package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/farvisun/squi/internal/ui/components"
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
		{name: "run", aliases: []string{"r"}, desc: "Run statement under cursor / selection", keys: "F5 · ctrl+enter · space r", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd { return m.runCurrent(t) }},
		{name: "runall", aliases: []string{"ra"}, desc: "Run all statements", keys: "F6 · space a", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd { return m.runAll(t) }},
		{name: "explain", desc: "Explain statement under cursor", keys: "F7 · space e", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd { return m.explain(t, false) }},
		{name: "analyze", desc: "Explain analyze (executes the statement)", keys: "F8", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd { return m.explain(t, true) }},
		{name: "cancel", desc: "Cancel the running query", keys: "ctrl+c", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd { return m.cancel(t) }},
		{name: "commit", desc: "Commit the transaction", keys: "ctrl+g c", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd { return m.txAction(t, "COMMIT") }},
		{name: "rollback", desc: "Roll back the transaction", keys: "ctrl+g r", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd { return m.txAction(t, "ROLLBACK") }},
		{name: "begin", desc: "Begin a transaction", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd { return m.txAction(t, "BEGIN") }},
		{name: "tx", desc: "Toggle manual transaction mode", keys: "ctrl+g m", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd { return m.txChoice(t, "MANUAL") }},
		{name: "w", aliases: []string{"save"}, desc: "Save pending grid edits", keys: "ctrl+s", needTab: true,
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
		{name: "export", desc: "Export the current result", keys: "x (in results)", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd {
				if r, ok := t.out.current().(*resultPanel); ok {
					return m.exportResult(r)
				}
				return m.toast("Select a result tab first", toastWarn)
			}},
		{name: "history", desc: "Search query history", keys: "ctrl+r · space h", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd { return m.historyPicker(t) }},
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
		{name: "clear", desc: "Clear the editor", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd { t.editor.SetValue(""); return nil }},
		{name: "refresh", desc: "Refresh explorer", keys: "r (in explorer)", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd {
				t.ac.cacheLoaded = nil
				return tea.Batch(t.ac.explorer.load(nil), m.ensureCache(t))
			}},
		{name: "connections", aliases: []string{"conn", "c"}, desc: "Connection manager", keys: "ctrl+o",
			run: func(m *Model, t *tab, _ string) tea.Cmd { m.openConnections(); return nil }},
		{name: "new", aliases: []string{"tabnew"}, desc: "Open a new tab", keys: "ctrl+t",
			run: func(m *Model, t *tab, _ string) tea.Cmd { return m.newTabPicker() }},
		{name: "close", aliases: []string{"q"}, desc: "Close the tab", keys: "ctrl+w q", needTab: true,
			run: func(m *Model, t *tab, _ string) tea.Cmd { return m.confirmCloseTab(t) }},
		{name: "tabn", desc: "Next tab", keys: "alt+] · ctrl+pgdown",
			run: func(m *Model, t *tab, _ string) tea.Cmd {
				if len(m.tabs) > 0 {
					m.cur = (m.cur + 1) % len(m.tabs)
				}
				return nil
			}},
		{name: "tabp", desc: "Previous tab", keys: "alt+[ · ctrl+pgup",
			run: func(m *Model, t *tab, _ string) tea.Cmd {
				if len(m.tabs) > 0 {
					m.cur = (m.cur - 1 + len(m.tabs)) % len(m.tabs)
				}
				return nil
			}},
		{name: "zoom", desc: "Zoom the focused pane", keys: "ctrl+w z",
			run: func(m *Model, t *tab, _ string) tea.Cmd { m.zoom = !m.zoom; return nil }},
		{name: "set", args: "vim|novim|fetch=N|width=N", desc: "Change a setting (saved)",
			run: func(m *Model, t *tab, a string) tea.Cmd { return m.setOption(strings.TrimSpace(a)) }},
		{name: "help", aliases: []string{"h"}, desc: "Key bindings", keys: "F1 · ?",
			run: func(m *Model, t *tab, _ string) tea.Cmd { return m.showHelp() }},
		{name: "quit", aliases: []string{"qa", "exit"}, desc: "Quit squi", keys: "ctrl+q",
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
			detail += "  " + c.keys
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

// ---- help ----

var helpSections = []struct {
	title string
	keys  [][2]string
}{
	{"Global", [][2]string{
		{"F1 / ?", "this help"}, {"ctrl+p", "command palette"}, {":", "command line (outside insert mode)"},
		{"ctrl+o", "connection manager"}, {"ctrl+t", "new tab"}, {"alt+1…9", "go to tab"},
		{"alt+] / alt+[", "next / previous tab (also ctrl+pgdown/pgup)"},
		{"tab / shift+tab", "cycle focus explorer → editor → results"},
		{"ctrl+w h/j/k/l", "focus pane · z zoom · </> explorer width · -/+ editor height · q close tab"},
		{"ctrl+b", "toggle explorer"}, {"ctrl+g", "transaction menu (commit / rollback / manual mode)"},
		{"ctrl+c", "cancel running query"}, {"ctrl+q", "quit"},
	}},
	{"Editor", [][2]string{
		{"F5 · ctrl+enter · alt+enter", "run statement under cursor (or selection); ctrl+j if ctrl+enter is not passed by the terminal"},
		{"F6", "run all statements"}, {"F7 / F8", "explain / explain analyze"},
		{"tab · ctrl+n · ctrl+space", "autocomplete (↑↓ choose, tab/enter accept)"},
		{"ctrl+r (insert mode)", "search history"},
		{"vim normal mode", "hjkl w b e 0 $ gg G · i a o O · v V · d c y + motion · dd yy p u ctrl+r · >> <<"},
		{"space r / a / e / h / s", "run · run all · explain · history · snippets"},
		{"non-vim mode", "shift+arrows select · ctrl+c/x/v copy/cut/paste · ctrl+z/y undo/redo"},
	}},
	{"Explorer", [][2]string{
		{"j/k · l/h", "move · expand / collapse"}, {"enter", "open table data"},
		{"t · double-click", "open table data in a new tab"},
		{"d / D", "table details / DDL"}, {"s", "insert SELECT into editor"},
		{"u · :use", "use this database/schema in the tab (:use alone lists databases)"}, {"x / i", "export table / import CSV"},
		{"/", "filter"}, {"r", "refresh"}, {"y", "copy name"},
	}},
	{"Results", [][2]string{
		{"hjkl · gg G · ctrl+d/u", "navigate (more rows load as you scroll; F loads all)"},
		{"[ / ]", "previous / next output tab · X closes it"},
		{"enter / v", "inspect cell / view row as record"},
		{"y / Y / c", "copy cell / row / copy menu (CSV, JSON, INSERT, Markdown…)"},
		{"/ · s", "filter rows · sort by column"}, {"x", "export"}, {"r", "re-run"},
		{"e · o · dd · u / U", "edit cell · add row · delete row · revert row / all"},
		{"ctrl+s", "review generated SQL and save edits"},
	}},
	{"Detail / Admin / Plan", [][2]string{
		{"1…9 · < >", "switch sub-tab"}, {"o", "open table data (detail)"},
		{"c / K", "cancel query / kill session (processes)"}, {"a", "toggle auto-refresh"},
		{"enter", "expand plan node · user grants"}, {"r", "raw plan / refresh"},
	}},
}

func (m *Model) showHelp() tea.Cmd {
	var lines, raw []string
	for _, s := range helpSections {
		lines = append(lines, theme.Title.Render(s.title))
		raw = append(raw, s.title)
		for _, k := range s.keys {
			lines = append(lines, "  "+theme.Key.Render(components.Fit(k[0], 26))+" "+k[1])
			raw = append(raw, fmt.Sprintf("  %-26s %s", k[0], k[1]))
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
