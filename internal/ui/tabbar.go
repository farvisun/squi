package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/farvisun/squi/internal/ui/theme"
)

// tabBarHits is the clickable geometry of the tab bar.
type tabBarHits struct {
	tabs       []hitSpan // ix = index in m.tabs
	closes     []hitSpan // the × of each tab
	prev, next hitSpan   // overflow chevrons (empty when not shown)
	newTab     hitSpan
}

// tabBar renders the tabs from m.tabScroll on, keeping the current one
// visible, and records their positions.
func (m *Model) tabBar() string {
	labels := make([]string, len(m.tabs))
	total := 0
	for i, t := range m.tabs {
		labels[i] = m.tabLabel(i, t)
		total += lipgloss.Width(labels[i]) + 1
	}
	const plus = " + "
	avail := m.width - len(plus)
	overflow := total > avail
	if overflow {
		avail -= 4 // "‹ " and " ›"
		m.tabScroll = min(m.tabScroll, m.cur)
		for m.tabScroll < m.cur && tabsWidth(labels[m.tabScroll:m.cur+1]) > avail {
			m.tabScroll++
		}
	} else {
		m.tabScroll = 0
	}

	var hits tabBarHits
	var b strings.Builder
	pos := 0
	put := func(s string) (x0, x1 int) {
		b.WriteString(s)
		x0 = pos
		pos += lipgloss.Width(s)
		return x0, pos
	}
	if overflow {
		st := theme.Key
		if m.tabScroll == 0 {
			st = theme.Dim
		}
		x0, x1 := put(st.Render("‹") + " ")
		hits.prev = hitSpan{x0: x0, x1: x1}
	}
	end := 0
	used := 0
	for i := m.tabScroll; i < len(labels); i++ {
		w := lipgloss.Width(labels[i]) + 1
		if overflow && used+w > avail && i > m.tabScroll {
			break
		}
		x0, _ := put(labels[i])
		put(" ")
		used += w
		end = i + 1
		x1 := x0 + w - 1
		hits.tabs = append(hits.tabs, hitSpan{x0, x1, i})
		// the label ends in "× " (the right padding)
		hits.closes = append(hits.closes, hitSpan{x1 - 2, x1, i})
	}
	if overflow {
		st := theme.Key
		if end >= len(labels) {
			st = theme.Dim
		}
		put(strings.Repeat(" ", max(0, avail-used)))
		x0, x1 := put(st.Render("›") + " ")
		hits.next = hitSpan{x0: x0, x1: x1}
	}
	x0, x1 := put(theme.Key.Render(plus))
	hits.newTab = hitSpan{x0: x0, x1: x1}
	m.frame.tabBar = hits
	return b.String()
}

func tabsWidth(labels []string) int {
	w := 0
	for _, l := range labels {
		w += lipgloss.Width(l) + 1
	}
	return w
}

func (m *Model) mouseTabBar(ev mouseEvent) tea.Cmd {
	hits := m.frame.tabBar
	if ev.wheel {
		switch ev.btn {
		case tea.MouseButtonWheelUp, tea.MouseButtonWheelLeft:
			m.switchTab(-1)
		case tea.MouseButtonWheelDown, tea.MouseButtonWheelRight:
			m.switchTab(1)
		}
		return nil
	}
	i := spanAt(hits.tabs, ev.x)
	switch ev.btn {
	case tea.MouseButtonMiddle:
		if i >= 0 {
			return m.confirmCloseTab(m.tabs[i])
		}
	case tea.MouseButtonRight:
		if i >= 0 {
			return m.tabMenu(i, ev.x, ev.y)
		}
	case tea.MouseButtonLeft:
		switch {
		case i >= 0 && spanAt(hits.closes, ev.x) == i:
			return m.confirmCloseTab(m.tabs[i])
		case i >= 0:
			m.cur = i
			m.startDrag(&dragState{move: m.dragTab})
		case ev.x >= hits.prev.x0 && ev.x < hits.prev.x1:
			m.switchTab(-1)
		case ev.x >= hits.next.x0 && ev.x < hits.next.x1:
			m.switchTab(1)
		case ev.x >= hits.newTab.x0 && ev.x < hits.newTab.x1:
			return m.newTabPicker()
		}
	}
	return nil
}

func (m *Model) switchTab(d int) {
	if n := len(m.tabs); n > 0 {
		m.cur = (m.cur + d + n) % n
	}
}

// dragTab moves the current tab to the slot under the pointer. It only swaps
// once the pointer would still be on the dragged tab afterwards, so tabs of
// different widths do not flip back and forth.
func (m *Model) dragTab(x, _ int) tea.Cmd {
	spans := m.frame.tabBar.tabs
	if len(spans) == 0 {
		return nil // swapped since the last render: positions are stale
	}
	var from, to *hitSpan
	for i := range spans {
		s := &spans[i]
		if s.ix == m.cur {
			from = s
		}
		if x >= s.x0 && x < s.x1 {
			to = s
		}
	}
	if from == nil || to == nil || from == to {
		return nil
	}
	w := from.x1 - from.x0
	if (to.ix < m.cur && x >= to.x0+w) || (to.ix > m.cur && x < to.x1-w) {
		return nil
	}
	tb := m.tabs[m.cur]
	m.tabs = append(m.tabs[:m.cur], m.tabs[m.cur+1:]...)
	m.tabs = append(m.tabs[:to.ix], append([]*tab{tb}, m.tabs[to.ix:]...)...)
	m.cur = to.ix
	m.frame.tabBar.tabs = nil
	return nil
}

// closeOtherTabs closes every tab but keep, asking about those with open work.
func (m *Model) closeOtherTabs(keep *tab) tea.Cmd {
	var cmds []tea.Cmd
	for _, t := range append([]*tab(nil), m.tabs...) {
		if t != keep {
			cmds = append(cmds, m.confirmCloseTab(t))
		}
	}
	return tea.Batch(cmds...)
}

func (m *Model) tabMenu(i, x, y int) tea.Cmd {
	t := m.tabs[i]
	items := []menuItem{
		{"c", "Close tab", func() tea.Cmd { return m.confirmCloseTab(t) }},
		{"o", "Close other tabs", func() tea.Cmd { return m.closeOtherTabs(t) }},
		{"n", "New tab", m.newTabPicker},
	}
	m.pushMenu(t.title(), items, x, y)
	return nil
}
