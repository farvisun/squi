package components

import (
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/farvisun/squi/internal/ui/theme"
)

// TextView is a scrollable read-only text area.
type TextView struct {
	raw           string
	lines         []string // rendered lines
	top, left     int
	width, height int
	focused       bool
	pendingG      bool
}

// NewTextView creates a text view. If lexer is non-empty, the text is
// highlighted as SQL.
func NewTextView(text, lexer string) *TextView {
	t := &TextView{}
	t.SetText(text, lexer)
	return t
}

// SetText replaces the content.
func (t *TextView) SetText(text, lexer string) {
	t.raw = text
	if lexer != "" {
		t.lines = HighlightSQL(text, lexer)
	} else {
		t.lines = strings.Split(strings.ReplaceAll(text, "\t", "    "), "\n")
	}
	t.top, t.left = 0, 0
}

// SetStyledLines sets pre-rendered lines.
func (t *TextView) SetStyledLines(raw string, lines []string) { t.raw, t.lines = raw, lines }

// LineCount returns the number of lines.
func (t *TextView) LineCount() int { return len(t.lines) }

// Text returns the raw text.
func (t *TextView) Text() string { return t.raw }

// SetSize sets the size.
func (t *TextView) SetSize(w, h int) { t.width, t.height = w, h }

// Focus / Blur.
func (t *TextView) Focus() { t.focused = true }
func (t *TextView) Blur()  { t.focused = false }

// Update scrolls on navigation keys.
func (t *TextView) Update(msg tea.KeyMsg) bool {
	k := msg.String()
	if t.pendingG {
		t.pendingG = false
		if k == "g" {
			t.top = 0
			return true
		}
	}
	switch k {
	case "j", "down":
		t.top++
	case "k", "up":
		t.top--
	case "h", "left":
		t.left -= 4
	case "l", "right":
		t.left += 4
	case "0", "home":
		t.left = 0
	case "g":
		t.pendingG = true
	case "G", "end":
		t.top = len(t.lines) - t.height
	case "ctrl+d", "pgdown", "ctrl+f", " ":
		t.top += max(1, t.height/2)
	case "ctrl+u", "pgup", "ctrl+b":
		t.top -= max(1, t.height/2)
	default:
		return false
	}
	t.clamp()
	return true
}

// Scroll scrolls by n lines.
func (t *TextView) Scroll(n int) { t.top += n; t.clamp() }

func (t *TextView) clamp() {
	t.top = clampInt(t.top, 0, max(0, len(t.lines)-t.height))
	if t.left < 0 {
		t.left = 0
	}
}

// View renders the visible window.
func (t *TextView) View() string {
	out := make([]string, 0, t.height)
	for i := t.top; i < len(t.lines) && i < t.top+t.height; i++ {
		l := t.lines[i]
		if t.left > 0 {
			l = ansi.TruncateLeft(l, t.left, "")
		}
		out = append(out, l)
	}
	if len(t.lines) > t.height && t.height > 0 && len(out) > 0 {
		pct := 100 * (t.top + t.height) / len(t.lines)
		last := len(out) - 1
		ind := theme.Dim.Render(" " + strconv.Itoa(min(pct, 100)) + "%")
		out[last] = FitStyled(out[last], max(0, t.width-ansi.StringWidth(ind))) + ind
	}
	return PadLines(strings.Join(out, "\n"), t.width, t.height)
}

// Raw returns the unstyled text.
func (v *TextView) Raw() string { return v.raw }
