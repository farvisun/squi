package ui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/farvisun/squi/internal/config"
)

const (
	minExplorerW = 16
	minEditorPct = 15
	maxEditorPct = 85
	minPaneH     = 5
	defaultPct   = 40
)

func (m *Model) setExplorerW(w int) {
	m.explorerW = max(minExplorerW, min(w, max(minExplorerW, m.width/2)))
}

func (m *Model) setEditorPct(p int) {
	m.editorPct = max(minEditorPct, min(p, maxEditorPct))
}

// mouseSplit starts dragging the pane border under the event, if any. Two
// borders sit side by side at a split, so both count. A double click restores
// the default size.
func (m *Model) mouseSplit(ev mouseEvent) bool {
	f := &m.frame
	if f.explorer.w > 0 && f.editor.w > 0 && ev.y >= f.explorer.y && (ev.x == f.editor.x-1 || ev.x == f.editor.x) {
		if ev.clicks == 2 {
			m.setExplorerW(m.settings.ExplorerWidth)
			m.saveLayout()
			return true
		}
		m.startDrag(&dragState{move: func(x, _ int) tea.Cmd {
			m.setExplorerW(x + 1)
			return nil
		}, end: m.saveLayout})
		return true
	}
	if f.editor.w > 0 && f.output.w > 0 && ev.x >= f.editor.x && (ev.y == f.output.y-1 || ev.y == f.output.y) {
		if ev.clicks == 2 {
			m.setEditorPct(defaultPct)
			m.saveLayout()
			return true
		}
		bodyH := max(1, m.height-2)
		m.startDrag(&dragState{move: func(_, y int) tea.Cmd {
			// y is the row the editor's bottom border should occupy; the body
			// starts at row 1, so the editor is then y rows tall. Round up, as
			// layout() rounds down.
			m.setEditorPct((y*100 + bodyH - 1) / bodyH)
			return nil
		}, end: m.saveLayout})
		return true
	}
	return false
}

func (m *Model) loadLayout() {
	if m.store == nil {
		return
	}
	lo, err := m.store.LoadLayout()
	if err != nil {
		return
	}
	if lo.ExplorerWidth > 0 {
		m.explorerW = max(minExplorerW, lo.ExplorerWidth)
	}
	if lo.EditorPct > 0 {
		m.setEditorPct(lo.EditorPct)
	}
	m.hideExplore = lo.HideExplorer
	m.lastLayout = lo
}

// saveLayout writes the pane sizes when they changed. Failing to save them
// is not worth interrupting anyone for.
func (m *Model) saveLayout() {
	if m.store == nil {
		return
	}
	lo := config.Layout{ExplorerWidth: m.explorerW, EditorPct: m.editorPct, HideExplorer: m.hideExplore}
	if lo == m.lastLayout {
		return
	}
	if m.store.SaveLayout(lo) == nil {
		m.lastLayout = lo
	}
}
