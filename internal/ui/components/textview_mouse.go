package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/farvisun/squi/internal/ui/theme"
)

type cellPos struct{ line, cell int }

func (a cellPos) less(b cellPos) bool {
	return a.line < b.line || (a.line == b.line && a.cell < b.cell)
}

func (t *TextView) cellAt(x, y int) cellPos {
	line := clampInt(t.top+y, 0, max(0, len(t.lines)-1))
	return cellPos{line, max(0, x+t.left)}
}

// PressAt clears any selection and starts a possible one at a position
// relative to the view.
func (t *TextView) PressAt(x, y int) {
	t.selOn = false
	t.selA = t.cellAt(x, y)
	t.selH = t.selA
}

// DragTo extends the selection to the pointer, scrolling when it is past the
// top or bottom edge.
func (t *TextView) DragTo(x, y int) {
	switch {
	case y < 0:
		t.Scroll(-1)
		y = 0
	case y >= t.height:
		t.Scroll(1)
		y = t.height - 1
	}
	t.selH = t.cellAt(x, y)
	t.selOn = t.selOn || t.selH != t.selA
}

// ScrollColumns moves the view sideways by n cells.
func (t *TextView) ScrollColumns(n int) { t.left += n; t.clamp() }

func (t *TextView) selBounds() (cellPos, cellPos) {
	if t.selH.less(t.selA) {
		return t.selH, t.selA
	}
	return t.selA, t.selH
}

// Selected returns the selected text as shown on screen.
func (t *TextView) Selected() (string, bool) {
	if !t.selOn {
		return "", false
	}
	a, b := t.selBounds()
	var out []string
	for i := a.line; i <= b.line && i < len(t.lines); i++ {
		plain := ansi.Strip(t.lines[i])
		from, to := 0, ansi.StringWidth(plain)
		if i == a.line {
			from = a.cell
		}
		if i == b.line {
			to = min(to, b.cell)
		}
		out = append(out, strings.TrimRight(ansi.Cut(plain, from, max(from, to)), " "))
	}
	s := strings.Join(out, "\n")
	return s, s != ""
}

// CopyText returns the selection, or all the text when nothing is selected.
func (t *TextView) CopyText() string {
	if s, ok := t.Selected(); ok {
		return s
	}
	return t.raw
}

// selected renders line i with its selected part highlighted.
func (t *TextView) selected(i int) string {
	l := t.lines[i]
	if !t.selOn {
		return l
	}
	a, b := t.selBounds()
	if i < a.line || i > b.line {
		return l
	}
	w := ansi.StringWidth(l)
	from, to := 0, w
	if i == a.line {
		from = min(a.cell, w)
	}
	if i == b.line {
		to = min(b.cell, w)
	}
	if from >= to {
		return l
	}
	mid := ansi.Strip(ansi.Cut(l, from, to))
	return ansi.Cut(l, 0, from) + lipgloss.NewStyle().Background(theme.SelBg).Render(mid) + ansi.Cut(l, to, w)
}
