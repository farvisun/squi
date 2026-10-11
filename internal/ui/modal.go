package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/farvisun/squi/internal/ui/components"
	"github.com/farvisun/squi/internal/ui/theme"
)

// modal is a dialog drawn above everything else. The topmost modal
// receives all input.
type modal interface {
	update(m *Model, msg tea.Msg) (done bool, cmd tea.Cmd)
	view(w, h int) string
}

func (m *Model) push(md modal) { m.modals = append(m.modals, md) }

func modalBox(title, body string, w int, accent lipgloss.TerminalColor) string {
	lines := strings.Split(body, "\n")
	return components.Box(title, body, w, len(lines)+2, true, accent)
}

// ---- confirm ----

type confirmModal struct {
	title, body string
	danger      bool
	onYes       func() tea.Cmd
	onNo        func() tea.Cmd
}

func (c *confirmModal) update(m *Model, msg tea.Msg) (bool, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return false, nil
	}
	switch strings.ToLower(km.String()) {
	case "y", "enter":
		if c.onYes != nil {
			return true, c.onYes()
		}
		return true, nil
	case "n", "esc", "q", "ctrl+c":
		if c.onNo != nil {
			return true, c.onNo()
		}
		return true, nil
	}
	return false, nil
}

func (c *confirmModal) view(w, h int) string {
	bw := min(w-4, max(50, longestLine(c.body)+4))
	accent := lipgloss.TerminalColor(theme.Accent)
	if c.danger {
		accent = theme.Red
	}
	body := wrap(c.body, bw-4) + "\n\n" + theme.Key.Render("y") + " yes   " + theme.Key.Render("n") + " no"
	return modalBox(c.title, body, bw, accent)
}

// ---- typed confirmation (destructive statements) ----

type typedConfirmModal struct {
	title, body string
	expect      string
	input       textinput.Model
	onYes       func() tea.Cmd
}

func newTypedConfirm(title, body, expect string, onYes func() tea.Cmd) *typedConfirmModal {
	ti := textinput.New()
	ti.Prompt = "› "
	ti.Placeholder = expect
	ti.Focus()
	return &typedConfirmModal{title: title, body: body, expect: expect, input: ti, onYes: onYes}
}

func (c *typedConfirmModal) update(m *Model, msg tea.Msg) (bool, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "esc", "ctrl+c":
			return true, nil
		case "enter":
			if c.input.Value() == c.expect {
				return true, c.onYes()
			}
			return false, nil
		}
	}
	var cmd tea.Cmd
	c.input, cmd = c.input.Update(msg)
	return false, cmd
}

func (c *typedConfirmModal) view(w, h int) string {
	bw := min(w-4, max(60, longestLine(c.body)+4))
	c.input.Width = bw - 8
	body := wrap(c.body, bw-4) + "\n\n" + "Type " + theme.Bold.Render(c.expect) + " to confirm:\n" + c.input.View() +
		"\n\n" + theme.Key.Render("enter") + " confirm   " + theme.Key.Render("esc") + " cancel"
	return modalBox(c.title, body, bw, theme.Red)
}

// ---- text prompt ----

type promptModal struct {
	title, label string
	input        textinput.Model
	extra        string // help line
	onSubmit     func(string) tea.Cmd
	onCancel     func() tea.Cmd
	onKey        func(key string, p *promptModal) (bool, tea.Cmd) // custom keys (e.g. ctrl+n = NULL)
	width        int
}

func newPrompt(title, label, value string, masked bool, onSubmit func(string) tea.Cmd) *promptModal {
	ti := textinput.New()
	ti.Prompt = "› "
	ti.SetValue(value)
	ti.CursorEnd()
	if masked {
		ti.EchoMode = textinput.EchoPassword
		ti.EchoCharacter = '•'
	}
	ti.Focus()
	return &promptModal{title: title, label: label, input: ti, onSubmit: onSubmit, width: 70}
}

func (p *promptModal) update(m *Model, msg tea.Msg) (bool, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		if p.onKey != nil {
			if done, cmd := p.onKey(km.String(), p); done {
				return true, cmd
			}
		}
		switch km.String() {
		case "esc", "ctrl+c":
			if p.onCancel != nil {
				return true, p.onCancel()
			}
			return true, nil
		case "enter":
			return true, p.onSubmit(p.input.Value())
		}
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	return false, cmd
}

func (p *promptModal) view(w, h int) string {
	bw := min(w-4, p.width)
	p.input.Width = bw - 8
	body := ""
	if p.label != "" {
		body = wrap(p.label, bw-4) + "\n"
	}
	body += p.input.View()
	help := theme.Key.Render("enter") + " ok   " + theme.Key.Render("esc") + " cancel"
	if p.extra != "" {
		help += "   " + p.extra
	}
	body += "\n\n" + help
	return modalBox(p.title, body, bw, theme.Accent)
}

// ---- picker ----

type pickerModal struct {
	picker *components.Picker
	onPick func(components.PickerItem) tea.Cmd
	width  int
	height int
}

func newPickerModal(title string, items []components.PickerItem, onPick func(components.PickerItem) tea.Cmd) *pickerModal {
	return &pickerModal{picker: components.NewPicker(title, items), onPick: onPick, width: 80, height: 20}
}

func (p *pickerModal) update(m *Model, msg tea.Msg) (bool, tea.Cmd) {
	done, ok, cmd := p.picker.Update(msg)
	if done && ok {
		it, _ := p.picker.Selected()
		return true, p.onPick(it)
	}
	return done, cmd
}

func (p *pickerModal) view(w, h int) string {
	bw := min(w-4, p.width)
	bh := min(h-2, p.height)
	p.picker.SetSize(bw-2, bh-2)
	return components.Box(p.picker.Title, p.picker.View(), bw, bh, true, theme.Accent)
}

// ---- menu with single-key shortcuts ----

type menuItem struct {
	key   string
	label string
	run   func() tea.Cmd
}

type menuModal struct {
	title string
	items []menuItem
	ix    int
}

func (mm *menuModal) update(m *Model, msg tea.Msg) (bool, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return false, nil
	}
	switch k := km.String(); k {
	case "esc", "q", "ctrl+c":
		return true, nil
	case "up", "k":
		mm.ix = max(0, mm.ix-1)
	case "down", "j":
		mm.ix = min(len(mm.items)-1, mm.ix+1)
	case "enter":
		return true, mm.items[mm.ix].run()
	default:
		for _, it := range mm.items {
			if it.key == k {
				return true, it.run()
			}
		}
	}
	return false, nil
}

func (mm *menuModal) view(w, h int) string {
	var lines []string
	for i, it := range mm.items {
		line := " " + theme.Key.Render(it.key) + "  " + it.label + " "
		if i == mm.ix {
			line = lipgloss.NewStyle().Background(theme.SelBg).Render(components.Fit(stripANSI(line), 48))
		}
		lines = append(lines, line)
	}
	return modalBox(mm.title, strings.Join(lines, "\n"), min(w-4, 52), theme.Accent)
}

// ---- scrollable text (cell inspector, SQL preview, help) ----

type textModal struct {
	title  string
	tv     *components.TextView
	footer string
	onKey  func(key string) (bool, tea.Cmd)
	width  int
	height int
}

func newTextModal(title, text, lexer string) *textModal {
	return &textModal{title: title, tv: components.NewTextView(text, lexer), width: 100, height: 30}
}

func (t *textModal) update(m *Model, msg tea.Msg) (bool, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if t.onKey != nil {
			if done, cmd := t.onKey(msg.String()); done {
				return true, cmd
			}
		}
		switch msg.String() {
		case "esc", "q", "enter", "ctrl+c":
			return true, nil
		case "y":
			return false, m.copy(t.tv.Text(), "text")
		}
		t.tv.Update(msg)
	case tea.MouseMsg:
		if msg.Button == tea.MouseButtonWheelUp {
			t.tv.Scroll(-3)
		} else if msg.Button == tea.MouseButtonWheelDown {
			t.tv.Scroll(3)
		}
	}
	return false, nil
}

func (t *textModal) view(w, h int) string {
	bw := min(w-4, t.width)
	bh := min(h-2, t.height, t.tv.LineCount()+4)
	footer := t.footer
	if footer == "" {
		footer = theme.Key.Render("y") + " copy   " + theme.Key.Render("esc") + " close"
	}
	t.tv.SetSize(bw-2, bh-4)
	body := t.tv.View() + "\n" + theme.Dim.Render(strings.Repeat("─", bw-2)) + "\n" + footer
	return components.Box(t.title, body, bw, bh, true, theme.Accent)
}

// ---- helpers ----

func longestLine(s string) int {
	n := 0
	for _, l := range strings.Split(s, "\n") {
		n = max(n, lipgloss.Width(l))
	}
	return n
}

// wrap soft-wraps plain text to width.
func wrap(s string, width int) string {
	if width <= 10 {
		return s
	}
	return lipgloss.NewStyle().Width(width).Render(s)
}
