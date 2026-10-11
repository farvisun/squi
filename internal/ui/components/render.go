// Package components contains reusable Bubble Tea widgets: the SQL editor,
// the results grid, pickers, forms and text views.
package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// Overlay draws fg on top of bg with its top-left corner at (x, y).
// Both are rendered (possibly ANSI-styled) multi-line strings.
func Overlay(bg, fg string, x, y int) string {
	bgLines := strings.Split(bg, "\n")
	fgLines := strings.Split(fg, "\n")
	for i, fl := range fgLines {
		row := y + i
		if row < 0 || row >= len(bgLines) {
			continue
		}
		bl := bgLines[row]
		w := ansi.StringWidth(fl)
		left := ansi.Truncate(bl, x, "")
		if pad := x - ansi.StringWidth(left); pad > 0 {
			left += strings.Repeat(" ", pad)
		}
		right := ansi.TruncateLeft(bl, x+w, "")
		bgLines[row] = left + fl + right
	}
	return strings.Join(bgLines, "\n")
}

// Center returns the position at which a box of size w×h is centered in W×H.
func Center(W, H, w, h int) (int, int) {
	x, y := (W-w)/2, (H-h)/2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	return x, y
}

// Fit pads or truncates s (plain text) to exactly width display cells.
func Fit(s string, width int) string {
	if width <= 0 {
		return ""
	}
	w := runewidth.StringWidth(s)
	if w == width {
		return s
	}
	if w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return runewidth.Truncate(s, width, "…")
}

// FitStyled pads or truncates a styled string to width display cells.
func FitStyled(s string, width int) string {
	w := ansi.StringWidth(s)
	if w == width {
		return s
	}
	if w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return ansi.Truncate(s, width, "…")
}

// Sanitize makes a value printable on one line.
func Sanitize(s string) string {
	if !strings.ContainsAny(s, "\n\r\t\x1b") {
		return s
	}
	return strings.NewReplacer("\r\n", "↵", "\n", "↵", "\r", "↵", "\t", " ", "\x1b", "␛").Replace(s)
}

// Box renders content inside a rounded border with a title, sized w×h
// (outer dimensions).
func Box(title, content string, w, h int, focused bool, accent lipgloss.TerminalColor) string {
	border := lipgloss.RoundedBorder()
	bc := lipgloss.Color("240")
	var c lipgloss.TerminalColor = bc
	if focused {
		c = accent
	}
	inner := w - 2
	if inner < 1 {
		inner = 1
	}
	ih := h - 2
	if ih < 0 {
		ih = 0
	}
	lines := strings.Split(content, "\n")
	if len(lines) > ih {
		lines = lines[:ih]
	}
	for len(lines) < ih {
		lines = append(lines, "")
	}
	bs := lipgloss.NewStyle().Foreground(c)
	var sb strings.Builder
	// top border with title
	t := ""
	if title != "" {
		t = " " + title + " "
	}
	tw := ansi.StringWidth(t)
	if tw > inner {
		t = ansi.Truncate(t, inner, "…")
		tw = ansi.StringWidth(t)
	}
	sb.WriteString(bs.Render(border.TopLeft + border.Top))
	if tw > 0 {
		ts := lipgloss.NewStyle().Foreground(c).Bold(focused)
		sb.WriteString(ts.Render(ansi.Strip(t)))
	}
	rest := inner - 1 - tw
	if rest < 0 {
		rest = 0
	}
	sb.WriteString(bs.Render(strings.Repeat(border.Top, rest) + border.TopRight))
	for _, l := range lines {
		sb.WriteString("\n")
		sb.WriteString(bs.Render(border.Left))
		sb.WriteString(FitStyled(l, inner))
		sb.WriteString(bs.Render(border.Right))
	}
	if h >= 2 {
		sb.WriteString("\n")
		sb.WriteString(bs.Render(border.BottomLeft + strings.Repeat(border.Bottom, inner) + border.BottomRight))
	}
	return sb.String()
}

// PadLines makes s exactly h lines of exactly w cells.
func PadLines(s string, w, h int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	for i, l := range lines {
		lines[i] = FitStyled(l, w)
	}
	return strings.Join(lines, "\n")
}
