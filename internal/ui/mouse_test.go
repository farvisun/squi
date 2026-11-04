package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"

	"github.com/farvisun/squi/internal/config"
)

// locate finds text on screen, so tests fail if hit-testing drifts from what
// is rendered.
func locate(t *testing.T, m *Model, text string) (int, int) {
	t.Helper()
	for y, line := range strings.Split(ansi.Strip(m.View()), "\n") {
		if i := strings.Index(line, text); i >= 0 {
			return runewidth.StringWidth(line[:i]), y
		}
	}
	t.Fatalf("%q not on screen:\n%s", text, ansi.Strip(m.View()))
	return 0, 0
}

func press(m *Model, x, y int, b tea.MouseButton) {
	_ = m.View()
	drain(m, m.handleMouse(tea.MouseMsg{X: x, Y: y, Button: b, Action: tea.MouseActionPress}))
}

func release(m *Model, x, y int) {
	_ = m.View()
	drain(m, m.handleMouse(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease}))
}

func motion(m *Model, x, y int) {
	_ = m.View()
	drain(m, m.handleMouse(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion}))
}

// click is one left click; the tracker is reset so it never counts as a
// double click of an earlier one.
func click(m *Model, x, y int) {
	m.clicks = clickTracker{}
	press(m, x, y, tea.MouseButtonLeft)
	release(m, x, y)
}

func doubleClickAt(m *Model, x, y int) {
	m.clicks = clickTracker{}
	press(m, x, y, tea.MouseButtonLeft)
	release(m, x, y)
	press(m, x, y, tea.MouseButtonLeft)
	release(m, x, y)
}

func dragTo(m *Model, x0, y0, x1, y1 int) {
	m.clicks = clickTracker{}
	press(m, x0, y0, tea.MouseButtonLeft)
	motion(m, x1, y1)
	release(m, x1, y1)
}

func wheel(m *Model, x, y int, b tea.MouseButton) { press(m, x, y, b) }

func devConn() config.Connection {
	return config.Connection{ID: "c", Name: "dev", Driver: "postgres"}
}

func TestClickFocusesPanes(t *testing.T) {
	m, _, _ := newTestModel(t, devConn())
	_ = m.View()
	f := m.frame
	click(m, f.output.x+5, f.output.y+3)
	if m.focus != focusOutput {
		t.Fatalf("focus = %v, want output", m.focus)
	}
	click(m, f.editor.x+5, f.editor.y+1)
	if m.focus != focusEditor {
		t.Fatalf("focus = %v, want editor", m.focus)
	}
	click(m, f.explorer.x+5, f.explorer.y+1)
	if m.focus != focusExplorer {
		t.Fatalf("focus = %v, want explorer", m.focus)
	}
}

func TestClickWhileZoomedKeepsThePane(t *testing.T) {
	m, _, _ := newTestModel(t, devConn())
	m.focus = focusOutput
	m.zoom = true
	_ = m.View()
	click(m, 3, 10) // where the explorer is when not zoomed
	if m.focus != focusOutput {
		t.Fatalf("zoomed click moved focus to %v", m.focus)
	}
}

func TestOutsideClickKeepsConfirm(t *testing.T) {
	m, _, _ := newTestModel(t, devConn())
	ran := false
	m.push(&confirmModal{title: "x", body: "y", onYes: func() tea.Cmd { ran = true; return nil }})
	_ = m.View()
	press(m, m.frame.modal.x-1, m.frame.modal.y-1, tea.MouseButtonLeft) // outside
	if len(m.modals) != 1 || ran {
		t.Fatal("outside click must not close or confirm a confirm dialog")
	}
}

func TestOutsideClickClosesMenuNotPrompt(t *testing.T) {
	m, _, _ := newTestModel(t, devConn())
	m.pushMenu("Menu", []menuItem{{"a", "A", func() tea.Cmd { return nil }}}, 10, 10)
	_ = m.View()
	click(m, 100, 35)
	if len(m.modals) != 0 {
		t.Fatal("outside click should close a menu")
	}
	m.push(newPrompt("Name", "", "typed", false, func(string) tea.Cmd { return nil }))
	_ = m.View()
	click(m, 0, 0)
	if len(m.modals) != 1 {
		t.Fatal("outside click must not discard a prompt")
	}
}

func TestContextMenuClickRunsItem(t *testing.T) {
	m, _, _ := newTestModel(t, devConn())
	got := ""
	m.pushMenu("Menu", []menuItem{
		{"a", "First", func() tea.Cmd { got = "first"; return nil }},
		{"b", "Second", func() tea.Cmd { got = "second"; return nil }},
	}, 110, 37)
	x, y := locate(t, m, "Second")
	if x >= 110 && y >= 37 {
		t.Fatalf("menu not kept on screen: %d,%d", x, y)
	}
	click(m, x, y)
	if got != "second" || len(m.modals) != 0 {
		t.Fatalf("got %q, %d modals", got, len(m.modals))
	}
}

func TestDragExplorerSplit(t *testing.T) {
	m, _, _ := newTestModel(t, devConn())
	_ = m.View()
	x := m.frame.editor.x
	dragTo(m, x, 10, 50, 10)
	_ = m.View()
	if m.frame.editor.x != 51 {
		t.Fatalf("editor starts at %d, want 51", m.frame.editor.x)
	}
	dragTo(m, 51, 10, 2, 10)
	if m.explorerW != minExplorerW {
		t.Fatalf("explorerW = %d, want minimum", m.explorerW)
	}
	dragTo(m, minExplorerW, 10, 119, 10)
	if m.explorerW != m.width/2 {
		t.Fatalf("explorerW = %d, want %d", m.explorerW, m.width/2)
	}
}

func TestDragEditorSplit(t *testing.T) {
	m, _, _ := newTestModel(t, devConn())
	_ = m.View()
	x := m.frame.output.x + 10
	dragTo(m, x, m.frame.output.y, x, 20)
	_ = m.View()
	if m.frame.editor.h != 20 {
		t.Fatalf("editor height %d, want 20", m.frame.editor.h)
	}
	dragTo(m, x, m.frame.output.y, x, 0)
	if m.editorPct != minEditorPct {
		t.Fatalf("pct = %d", m.editorPct)
	}
	doubleClickAt(m, x, m.frame.output.y)
	if m.editorPct != defaultPct {
		t.Fatalf("double click: pct = %d", m.editorPct)
	}
}

func TestLostReleaseEndsDrag(t *testing.T) {
	m, _, _ := newTestModel(t, devConn())
	_ = m.View()
	press(m, m.frame.editor.x, 10, tea.MouseButtonLeft)
	if m.drag == nil {
		t.Fatal("no drag")
	}
	m.Update(keyMsgFor("down"))
	if m.drag != nil {
		t.Fatal("a key should end the drag")
	}
	press(m, m.frame.editor.x, 10, tea.MouseButtonLeft)
	press(m, m.frame.editor.x+20, 12, tea.MouseButtonLeft)
	if m.drag != nil {
		t.Fatal("a new press away from a border should end the drag")
	}
}

func TestLayoutPersists(t *testing.T) {
	m, _, _ := newTestModel(t, devConn())
	_ = m.View()
	dragTo(m, m.frame.editor.x, 10, 45, 10)
	key(m, "ctrl+w", "-")
	key(m, "ctrl+b")
	m2, err := New(Options{Store: m.store, Bridge: &Bridge{}})
	if err != nil {
		t.Fatal(err)
	}
	if m2.explorerW != 46 || m2.editorPct != m.editorPct || !m2.hideExplore {
		t.Fatalf("restored %d %d %v", m2.explorerW, m2.editorPct, m2.hideExplore)
	}
}

func addTabs(m *Model, tb *tab, n int) {
	for i := 0; i < n; i++ {
		drain(m, m.newTab(tb.ac))
	}
}

func TestTabBarClickCloseAndNew(t *testing.T) {
	m, tb, _ := newTestModel(t, devConn())
	addTabs(m, tb, 2)
	x, y := locate(t, m, "1 ")
	click(m, x+1, y)
	if m.cur != 0 {
		t.Fatalf("cur = %d", m.cur)
	}
	_ = m.View()
	c := m.frame.tabBar.closes[1]
	click(m, c.x0, 0)
	if len(m.tabs) != 2 {
		t.Fatalf("tabs = %d", len(m.tabs))
	}
	press(m, m.frame.tabBar.tabs[1].x0+1, 0, tea.MouseButtonMiddle)
	if len(m.tabs) != 1 {
		t.Fatalf("middle click: tabs = %d", len(m.tabs))
	}
	_ = m.View()
	click(m, m.frame.tabBar.newTab.x0+1, 0)
	if len(m.modals) != 1 {
		t.Fatal("+ should open the tab picker")
	}
}

func TestCloseTabGuardedByMouse(t *testing.T) {
	m, tb, _ := newTestModel(t, devConn())
	addTabs(m, tb, 1)
	m.tabs[1].inTx = true
	_ = m.View()
	click(m, m.frame.tabBar.closes[1].x0, 0)
	if len(m.tabs) != 2 || len(m.modals) != 1 {
		t.Fatalf("open transaction must ask first: tabs %d modals %d", len(m.tabs), len(m.modals))
	}
}

func TestTabDragReorders(t *testing.T) {
	m, tb, _ := newTestModel(t, devConn())
	addTabs(m, tb, 2)
	first := m.tabs[0]
	_ = m.View()
	s := m.frame.tabBar.tabs
	m.clicks = clickTracker{}
	press(m, s[0].x0+1, 0, tea.MouseButtonLeft)
	motion(m, s[2].x1-2, 0)
	release(m, s[2].x1-2, 0)
	if m.tabs[2] != first || m.cur != 2 {
		t.Fatalf("first tab at wrong slot, cur = %d", m.cur)
	}
}

func TestTabBarOverflowKeepsCurrentVisible(t *testing.T) {
	m, tb, _ := newTestModel(t, devConn())
	addTabs(m, tb, 12)
	m.width = 60
	m.cur = 12
	_ = m.View()
	if m.tabScroll == 0 {
		t.Fatal("should scroll to the last tab")
	}
	found := false
	for _, s := range m.frame.tabBar.tabs {
		found = found || s.ix == 12
	}
	if !found {
		t.Fatal("current tab not rendered")
	}
	click(m, m.frame.tabBar.prev.x0, 0)
	if m.cur != 11 {
		t.Fatalf("prev chevron: cur = %d", m.cur)
	}
	wheel(m, 5, 0, tea.MouseButtonWheelDown)
	if m.cur != 12 {
		t.Fatalf("wheel: cur = %d", m.cur)
	}
}

func TestTabContextMenu(t *testing.T) {
	m, tb, _ := newTestModel(t, devConn())
	addTabs(m, tb, 2)
	_ = m.View()
	press(m, m.frame.tabBar.tabs[1].x0+1, 0, tea.MouseButtonRight)
	x, y := locate(t, m, "Close other tabs")
	click(m, x, y)
	if len(m.tabs) != 1 {
		t.Fatalf("tabs = %d", len(m.tabs))
	}
}

func TestMouseLeakIsNotTyped(t *testing.T) {
	m, tb, _ := newTestModel(t, devConn())
	m.focus = focusEditor
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[<0;10;5M")})
	if v := tb.editor.Value(); v != "" {
		t.Fatalf("editor got %q", v)
	}
}
