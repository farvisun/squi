// Package theme holds squi's colors and lipgloss styles.
package theme

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/farvisun/squi/internal/config"
)

// Palette colors adapt to light and dark terminals.
var (
	Fg        = lipgloss.AdaptiveColor{Light: "#1f2328", Dark: "#d0d7de"}
	Muted     = lipgloss.AdaptiveColor{Light: "#6e7781", Dark: "#7d8590"}
	Subtle    = lipgloss.AdaptiveColor{Light: "#d0d7de", Dark: "#30363d"}
	Accent    = lipgloss.AdaptiveColor{Light: "#0969da", Dark: "#58a6ff"}
	AccentBg  = lipgloss.AdaptiveColor{Light: "#ddf4ff", Dark: "#1f3a5f"}
	SelBg     = lipgloss.AdaptiveColor{Light: "#b6e3ff", Dark: "#264f78"}
	CursorBg  = lipgloss.AdaptiveColor{Light: "#0969da", Dark: "#58a6ff"}
	CursorFg  = lipgloss.AdaptiveColor{Light: "#ffffff", Dark: "#0d1117"}
	Green     = lipgloss.AdaptiveColor{Light: "#1a7f37", Dark: "#3fb950"}
	Yellow    = lipgloss.AdaptiveColor{Light: "#9a6700", Dark: "#d29922"}
	Red       = lipgloss.AdaptiveColor{Light: "#cf222e", Dark: "#f85149"}
	Purple    = lipgloss.AdaptiveColor{Light: "#8250df", Dark: "#bc8cff"}
	Cyan      = lipgloss.AdaptiveColor{Light: "#1b7c83", Dark: "#39c5cf"}
	Orange    = lipgloss.AdaptiveColor{Light: "#bc4c00", Dark: "#f0883e"}
	BarBg     = lipgloss.AdaptiveColor{Light: "#eaeef2", Dark: "#161b22"}
	ChangedBg = lipgloss.AdaptiveColor{Light: "#fff8c5", Dark: "#3d2e00"}
	AddedBg   = lipgloss.AdaptiveColor{Light: "#dafbe1", Dark: "#033a16"}
	DeletedBg = lipgloss.AdaptiveColor{Light: "#ffebe9", Dark: "#4a0d0d"}
)

// Common styles.
var (
	Base        = lipgloss.NewStyle().Foreground(Fg)
	Dim         = lipgloss.NewStyle().Foreground(Muted)
	Bold        = lipgloss.NewStyle().Bold(true)
	Title       = lipgloss.NewStyle().Foreground(Accent).Bold(true)
	Error       = lipgloss.NewStyle().Foreground(Red)
	Warn        = lipgloss.NewStyle().Foreground(Yellow)
	OK          = lipgloss.NewStyle().Foreground(Green)
	Null        = lipgloss.NewStyle().Foreground(Muted).Italic(true)
	Key         = lipgloss.NewStyle().Foreground(Accent).Bold(true)
	Selected    = lipgloss.NewStyle().Background(SelBg)
	Cursor      = lipgloss.NewStyle().Background(CursorBg).Foreground(CursorFg)
	Header      = lipgloss.NewStyle().Foreground(Accent).Bold(true)
	ActiveTab   = lipgloss.NewStyle().Foreground(CursorFg).Background(Accent).Bold(true).Padding(0, 1)
	InactiveTab = lipgloss.NewStyle().Foreground(Muted).Padding(0, 1)
	Changed     = lipgloss.NewStyle().Background(ChangedBg)
	Added       = lipgloss.NewStyle().Background(AddedBg)
	Deleted     = lipgloss.NewStyle().Background(DeletedBg).Strikethrough(true)
)

// Border returns a pane border style; focused panes use the accent color.
func Border(focused bool) lipgloss.Style {
	c := lipgloss.TerminalColor(Subtle)
	if focused {
		c = Accent
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c)
}

// EnvColor is the color associated with an environment tag.
func EnvColor(e config.Env) lipgloss.TerminalColor {
	switch e {
	case config.EnvProd:
		return Red
	case config.EnvStaging:
		return Yellow
	case config.EnvDev:
		return Green
	case config.EnvLocal:
		return Cyan
	}
	return Muted
}

// EnvBadge renders a colored environment label, or "" for untagged.
func EnvBadge(e config.Env) string {
	if e == config.EnvNone {
		return ""
	}
	return lipgloss.NewStyle().Background(EnvColor(e)).Foreground(lipgloss.Color("#000000")).
		Bold(true).Padding(0, 1).Render(upper(string(e)))
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}

// Syntax styles used by the SQL highlighter.
var (
	SynKeyword  = lipgloss.NewStyle().Foreground(Purple).Bold(true)
	SynString   = lipgloss.NewStyle().Foreground(Green)
	SynNumber   = lipgloss.NewStyle().Foreground(Orange)
	SynComment  = lipgloss.NewStyle().Foreground(Muted).Italic(true)
	SynFunction = lipgloss.NewStyle().Foreground(Accent)
	SynOperator = lipgloss.NewStyle().Foreground(Cyan)
	SynIdent    = lipgloss.NewStyle().Foreground(Fg)
	SynType     = lipgloss.NewStyle().Foreground(Yellow)
)
