package ui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/farvisun/squi/internal/ui/components"
	"github.com/farvisun/squi/internal/ui/keymap"
	"github.com/farvisun/squi/internal/ui/theme"
)

// panel is one sub-tab of the output pane.
type panel interface {
	title() string
	view(w, h int, focused bool) string
	update(m *Model, t *tab, km tea.KeyMsg) tea.Cmd
}

// scroller is implemented by panels that react to the mouse wheel.
type scroller interface{ scroll(n int) }

// output is the lower right pane: result sets, messages, details, plans.
type output struct {
	panels []panel
	ix     int
	msgs   *messagesPanel
}

func newOutput() *output {
	mp := &messagesPanel{}
	return &output{panels: []panel{mp}, msgs: mp}
}

func (o *output) current() panel {
	if o.ix < 0 || o.ix >= len(o.panels) {
		return nil
	}
	return o.panels[o.ix]
}

// add appends a panel and selects it.
func (o *output) add(p panel) {
	o.panels = append(o.panels, p)
	o.ix = len(o.panels) - 1
}

// replace swaps old for p in place (or adds p).
func (o *output) replace(old, p panel) {
	for i, x := range o.panels {
		if x == old {
			o.panels[i] = p
			o.ix = i
			return
		}
	}
	o.add(p)
}

// removeResults drops all result panels (before a new run).
func (o *output) removeResults() {
	kept := o.panels[:0]
	for _, p := range o.panels {
		if _, ok := p.(*resultPanel); !ok {
			kept = append(kept, p)
		}
	}
	o.panels = kept
	o.ix = clampIx(o.ix, len(o.panels))
}

func (o *output) remove(p panel) {
	if p == panel(o.msgs) {
		return
	}
	for i, x := range o.panels {
		if x == p {
			o.panels = append(o.panels[:i], o.panels[i+1:]...)
			if o.ix >= i {
				o.ix--
			}
			break
		}
	}
	o.ix = clampIx(o.ix, len(o.panels))
}

func (o *output) results() []*resultPanel {
	var out []*resultPanel
	for _, p := range o.panels {
		if r, ok := p.(*resultPanel); ok {
			out = append(out, r)
		}
	}
	return out
}

func (o *output) update(m *Model, t *tab, km tea.KeyMsg) tea.Cmd {
	switch m.keys.Action(keymap.Output, km.String()) {
	case "output.next":
		o.ix = (o.ix + 1) % len(o.panels)
		return nil
	case "output.prev":
		o.ix = (o.ix - 1 + len(o.panels)) % len(o.panels)
		return nil
	case "output.close":
		if p := o.current(); p != nil {
			if r, ok := p.(*resultPanel); ok && r.dirty() {
				return m.toast("Panel has unsaved edits (u to revert)", toastWarn)
			}
			o.remove(p)
			if r, ok := p.(*resultPanel); ok {
				r.closeStream()
			}
		}
		return nil
	}
	if p := o.current(); p != nil {
		return p.update(m, t, km)
	}
	return nil
}

func (o *output) view(w, h int, focused bool) string {
	var tabs []string
	used := 0
	start := 0
	// keep the selected tab visible
	for {
		used = 0
		for i := start; i <= o.ix && i < len(o.panels); i++ {
			used += lipgloss.Width(o.panels[i].title()) + 3
		}
		if used <= w || start >= o.ix {
			break
		}
		start++
	}
	used = 0
	for i := start; i < len(o.panels); i++ {
		title := o.panels[i].title()
		var s string
		if i == o.ix {
			st := theme.ActiveTab
			if !focused {
				st = st.Background(theme.Muted)
			}
			s = st.Render(title)
		} else {
			s = theme.InactiveTab.Render(title)
		}
		if used+lipgloss.Width(s)+1 > w {
			tabs = append(tabs, theme.Dim.Render("…"))
			break
		}
		tabs = append(tabs, s)
		used += lipgloss.Width(s) + 1
	}
	header := components.FitStyled(strings.Join(tabs, " "), w)
	body := ""
	if p := o.current(); p != nil {
		body = p.view(w, h-1, focused)
	}
	return header + "\n" + components.PadLines(body, w, h-1)
}

// ---- messages ----

type logLevel int

const (
	logInfo logLevel = iota
	logOK
	logWarn
	logError
)

type logEntry struct {
	at    time.Time
	level logLevel
	text  string
}

type messagesPanel struct {
	entries []logEntry
	tv      components.TextView
	follow  bool
	dirty   bool
	unseen  int // errors logged since the panel was last shown
}

func (p *messagesPanel) log(level logLevel, text string) {
	p.entries = append(p.entries, logEntry{time.Now(), level, text})
	if level == logError {
		p.unseen++
	}
	if len(p.entries) > 1000 {
		p.entries = p.entries[len(p.entries)-1000:]
	}
	p.dirty = true
	p.follow = true
}

func (p *messagesPanel) title() string {
	if p.unseen > 0 {
		return "Messages " + theme.Error.Render("●")
	}
	return "Messages"
}

func (p *messagesPanel) update(m *Model, t *tab, km tea.KeyMsg) tea.Cmd {
	switch km.String() {
	case "y":
		var raw []string
		for _, e := range p.entries {
			raw = append(raw, e.at.Format("15:04:05")+" "+e.text)
		}
		return m.copy(strings.Join(raw, "\n"), "messages")
	case "C":
		p.entries = nil
		p.dirty = true
		return nil
	}
	p.follow = false
	p.tv.Update(km)
	return nil
}

func (p *messagesPanel) scroll(n int) { p.follow = false; p.tv.Scroll(n) }

func (p *messagesPanel) view(w, h int, focused bool) string {
	p.unseen = 0
	if p.dirty {
		var raw []string
		var lines []string
		for _, e := range p.entries {
			ts := theme.Dim.Render(e.at.Format("15:04:05") + " ")
			st := theme.Base
			switch e.level {
			case logOK:
				st = theme.OK
			case logWarn:
				st = theme.Warn
			case logError:
				st = theme.Error
			}
			for i, l := range strings.Split(e.text, "\n") {
				prefix := ts
				if i > 0 {
					prefix = "         "
				}
				lines = append(lines, prefix+st.Render(l))
				raw = append(raw, l)
			}
		}
		p.tv.SetStyledLines(strings.Join(raw, "\n"), lines)
		p.dirty = false
	}
	p.tv.SetSize(w, h)
	if p.follow {
		p.tv.Scroll(1 << 20)
	}
	if len(p.entries) == 0 {
		return theme.Dim.Render(" No messages yet. Run a query with ctrl+enter or F5 (or space r).")
	}
	return p.tv.View()
}
