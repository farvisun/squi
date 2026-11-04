package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/farvisun/squi/internal/ui/theme"
)

// hint is a key shown with what it does. Clicking it presses the key, unless
// it only describes keys (inert).
type hint struct {
	key, label string
	inert      bool
}

// hintHit is where a clickable hint was drawn.
type hintHit struct {
	x0, x1, line int
	key          string
}

// renderHints lays hints out in w cells, wrapping between them so none is
// cut off. Buttons draws them bracketed, for dialogs.
func renderHints(hints []hint, w int, buttons bool) (string, []hintHit) {
	var lines []string
	var hits []hintHit
	var line strings.Builder
	x := 0
	for _, h := range hints {
		s := theme.Key.Render(h.key) + " " + h.label
		if buttons {
			s = theme.Dim.Render("[") + " " + s + " " + theme.Dim.Render("]")
		}
		sw := lipgloss.Width(s)
		if x > 0 && x+2+sw > w {
			lines = append(lines, line.String())
			line.Reset()
			x = 0
		}
		if x > 0 {
			line.WriteString("  ")
			x += 2
		}
		line.WriteString(s)
		if !h.inert {
			hits = append(hits, hintHit{x, x + sw, len(lines), h.key})
		}
		x += sw
	}
	lines = append(lines, line.String())
	return strings.Join(lines, "\n"), hits
}

// offsetHints moves hits down by n lines.
func offsetHints(hits []hintHit, n int) []hintHit {
	for i := range hits {
		hits[i].line += n
	}
	return hits
}

// hintAt returns the key of the hint at (x, line), or "".
func hintAt(hits []hintHit, x, line int) string {
	for _, h := range hits {
		if h.line == line && x >= h.x0 && x < h.x1 {
			return h.key
		}
	}
	return ""
}

// keyPress builds the key message for a key name as shown in hints.
func keyPress(k string) tea.KeyMsg {
	switch k {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	}
	if len(k) == len("ctrl+x") && strings.HasPrefix(k, "ctrl+") {
		return tea.KeyMsg{Type: tea.KeyCtrlA + tea.KeyType(k[5]-'a')}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

// clickInput moves a text input's cursor to column x of the input, its prompt
// included. Inputs scrolled sideways keep their cursor: their first visible
// rune isn't known here.
func clickInput(ti *textinput.Model, x int) {
	x -= lipgloss.Width(ti.Prompt)
	if x < 0 || lipgloss.Width(ti.Value()) > ti.Width {
		return
	}
	col := 0
	for i, r := range []rune(ti.Value()) {
		w := lipgloss.Width(string(r))
		if x < col+w {
			ti.SetCursor(i)
			return
		}
		col += w
	}
	ti.CursorEnd()
}
