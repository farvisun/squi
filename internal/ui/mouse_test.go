package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"

	"github.com/farvisun/squi/internal/config"
	"github.com/farvisun/squi/internal/db"
	"github.com/farvisun/squi/internal/ui/components"
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
	if m.frame.editor.x != 50 {
		t.Fatalf("editor starts at %d, want 50", m.frame.editor.x)
	}
	dragTo(m, 50, 10, 2, 10)
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
	h := m.frame.editor.h
	dragTo(m, x, m.frame.output.y, x, m.frame.output.y+6)
	_ = m.View()
	if m.frame.editor.h != h+6 {
		t.Fatalf("editor height %d, want %d", m.frame.editor.h, h+6)
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
	first := m.drag
	press(m, m.frame.editor.x+20, 5, tea.MouseButtonLeft)
	if m.drag == first {
		t.Fatal("a new press should end the earlier drag")
	}
}

func TestLayoutPersists(t *testing.T) {
	m, _, _ := newTestModel(t, devConn())
	_ = m.View()
	dragTo(m, m.frame.editor.x, 10, 46, 10)
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

func editorSetup(t *testing.T, vim bool, sql string) (*Model, *tab) {
	t.Helper()
	m, tb, _ := newTestModel(t, devConn())
	tb.editor.Vim = vim
	tb.editor.SetValue(sql)
	tb.editor.SetCursorOffset(0)
	m.focus = focusEditor
	return m, tb
}

func TestEditorClickMovesCursor(t *testing.T) {
	m, tb := editorSetup(t, false, "SELECT one\nFROM two")
	x, y := locate(t, m, "two")
	click(m, x+1, y)
	if off := tb.editor.CursorOffset(); off != len("SELECT one\nFROM t") {
		t.Fatalf("offset = %d", off)
	}
}

func TestEditorClickHandlesWideRunes(t *testing.T) {
	m, tb := editorSetup(t, false, "SELECT '日本語' x")
	x, y := locate(t, m, " x")
	click(m, x+1, y)
	if off := tb.editor.CursorOffset(); off != len("SELECT '日本語' ") {
		t.Fatalf("offset = %d, want %d", off, len("SELECT '日本語' "))
	}
}

func TestEditorDragSelects(t *testing.T) {
	for _, vim := range []bool{false, true} {
		m, tb := editorSetup(t, vim, "SELECT one FROM two")
		x, y := locate(t, m, "one")
		dragTo(m, x, y, x+2, y)
		start, end, ok := tb.editor.Selection()
		if !ok {
			t.Fatalf("vim=%v: no selection", vim)
		}
		got := tb.editor.Value()[start:end]
		want := "on"
		if vim {
			want = "one" // visual mode includes the cursor
		}
		if got != want {
			t.Fatalf("vim=%v: selected %q, want %q", vim, got, want)
		}
	}
}

func TestEditorDoubleAndTripleClick(t *testing.T) {
	for _, vim := range []bool{false, true} {
		m, tb := editorSetup(t, vim, "SELECT one\nFROM two")
		x, y := locate(t, m, "one")
		doubleClickAt(m, x+1, y)
		start, end, ok := tb.editor.Selection()
		if !ok || tb.editor.Value()[start:end] != "one" {
			t.Fatalf("vim=%v: word selection wrong: %d %d %v", vim, start, end, ok)
		}
		press(m, x+1, y, tea.MouseButtonLeft)
		start, end, ok = tb.editor.Selection()
		if !ok || tb.editor.Value()[start:end] != "SELECT one" {
			t.Fatalf("vim=%v: line selection wrong: %d %d %v", vim, start, end, ok)
		}
		release(m, x+1, y)
	}
}

func TestEditorClickClearsSelection(t *testing.T) {
	m, tb := editorSetup(t, false, "SELECT one")
	x, y := locate(t, m, "one")
	dragTo(m, x, y, x+2, y)
	click(m, x, y)
	if _, _, ok := tb.editor.Selection(); ok {
		t.Fatal("click should clear the selection")
	}
}

func TestEditorWheelKeepsCursor(t *testing.T) {
	m, tb := editorSetup(t, false, strings.Repeat("line\n", 100))
	off := tb.editor.CursorOffset()
	_ = m.View()
	r := m.frame.editor
	wheel(m, r.x+5, r.y+2, tea.MouseButtonWheelDown)
	wheel(m, r.x+5, r.y+2, tea.MouseButtonWheelDown)
	if tb.editor.CursorOffset() != off {
		t.Fatal("wheel moved the cursor")
	}
	firstRow := func() string { return strings.Split(ansi.Strip(m.View()), "\n")[2] }
	if !strings.Contains(firstRow(), "│7 ") {
		t.Fatalf("view did not scroll: %q", firstRow())
	}
	key(m, "x")
	if !strings.Contains(firstRow(), "│1 ") {
		t.Fatalf("a key should bring the cursor back into view: %q", firstRow())
	}
}

func TestEditorDragPastEdgeAutoscrolls(t *testing.T) {
	m, tb := editorSetup(t, false, strings.Repeat("line\n", 100))
	_ = m.View()
	r := m.frame.editor
	m.clicks = clickTracker{}
	press(m, r.x+8, r.y+2, tea.MouseButtonLeft)
	motion(m, r.x+8, r.y+r.h+3)
	id := m.drag.id
	before := tb.editor.CursorOffset()
	for i := 0; i < 5; i++ {
		m.Update(dragTickMsg{id})
		_ = m.View()
	}
	if tb.editor.CursorOffset() <= before {
		t.Fatal("holding past the edge should keep selecting")
	}
	release(m, r.x+8, r.y+r.h+3)
	if m.drag != nil {
		t.Fatal("drag still active")
	}
}

func TestEditorContextMenuCopiesSelection(t *testing.T) {
	m, tb := editorSetup(t, false, "SELECT one FROM two")
	x, y := locate(t, m, "one")
	doubleClickAt(m, x, y)
	press(m, x+1, y, tea.MouseButtonRight)
	if len(m.modals) != 1 {
		t.Fatal("no menu")
	}
	if _, _, ok := tb.editor.Selection(); !ok {
		t.Fatal("right click inside the selection must keep it")
	}
	cx, cy := locate(t, m, "Cut")
	click(m, cx, cy)
	if tb.editor.Value() != "SELECT  FROM two" {
		t.Fatalf("after cut: %q", tb.editor.Value())
	}
}

func TestEditorCompletionClick(t *testing.T) {
	m, tb := editorSetup(t, false, "SEL")
	tb.editor.SetCursorOffset(3)
	_ = m.View()
	tb.editor.ShowCompletion([]components.CompletionItem{{Label: "SELECT", Insert: "SELECT"}, {Label: "SELL", Insert: "SELL"}}, 0, true)
	x, y := locate(t, m, "SELL")
	click(m, x, y)
	if tb.editor.Value() != "SELL" {
		t.Fatalf("value = %q", tb.editor.Value())
	}
}

// resultsSetup runs a query so the output pane shows a result grid.
func resultsSetup(t *testing.T) (*Model, *tab, *resultPanel) {
	t.Helper()
	m, tb, _ := newTestModel(t, devConn())
	setSQL(tb, "SELECT * FROM users")
	key(m, "f5")
	rs := tb.out.results()
	if len(rs) != 1 {
		t.Fatalf("want a result panel, got %d", len(rs))
	}
	m.focus = focusOutput
	_ = m.View()
	return m, tb, rs[0]
}

func TestGridClickSelectsCell(t *testing.T) {
	m, _, r := resultsSetup(t)
	x, y := locate(t, m, "a@b.c")
	click(m, x, y+1)
	row, col := r.grid.Cursor()
	if row != 1 || r.cols[col].Name != "email" {
		t.Fatalf("cursor %d,%d", row, col)
	}
}

func TestGridHeaderClickSorts(t *testing.T) {
	m, _, r := resultsSetup(t)
	x, y := locate(t, m, "email")
	click(m, x, y)
	if v := ansi.Strip(m.View()); !strings.Contains(v, "email▲") {
		t.Fatalf("no ascending mark:\n%s", v)
	}
	_, col := r.grid.Cursor()
	if r.cols[col].Name != "email" {
		t.Fatalf("cursor column %d", col)
	}
}

func TestGridDoubleClickInspectsNotEdits(t *testing.T) {
	m, _, r := resultsSetup(t)
	x, y := locate(t, m, "a@b.c")
	doubleClickAt(m, x, y)
	if len(m.modals) != 1 {
		t.Fatalf("want the inspector, got %d modals", len(m.modals))
	}
	if _, ok := m.modals[0].(*textModal); !ok {
		t.Fatalf("modal is %T", m.modals[0])
	}
	if r.dirty() {
		t.Fatal("double click must not edit")
	}
}

func TestGridDragSelectsRangeAndCopies(t *testing.T) {
	m, _, r := resultsSetup(t)
	x, y := locate(t, m, "a@b.c")
	dragTo(m, x, y, x, y+3)
	rows, cols, ok := r.grid.RangeSelection()
	if !ok || len(cols) != 1 || len(rows) != 4 {
		t.Fatalf("range rows %v cols %v", rows, cols)
	}
	key(m, "esc")
	if r.grid.Ranged() || m.focus != focusOutput {
		t.Fatalf("esc should clear the range first, focus %v", m.focus)
	}
	key(m, "esc")
	if m.focus != focusEditor {
		t.Fatal("second esc should leave the pane")
	}
}

func TestGridRangeFollowsSortAndFilter(t *testing.T) {
	g := components.NewGrid()
	g.SetSize(60, 12)
	g.SetData([]string{"a", "b"}, nil, cellRows("1", "x", "2", "y", "3", "x"))
	g.SetFilter("x")
	g.PressCell(0, 0)
	g.DragTo(g.Gutter()+1, 3)
	rows, _, ok := g.RangeSelection()
	if !ok || len(rows) != 2 || rows[0] != 0 || rows[1] != 2 {
		t.Fatalf("range over a filtered view: %v", rows)
	}
	g.ToggleSort()
	if g.Ranged() {
		t.Fatal("sorting should drop the range")
	}
}

func cellRows(vals ...string) [][]db.Cell {
	var out [][]db.Cell
	for i := 0; i+1 < len(vals); i += 2 {
		out = append(out, []db.Cell{db.Text(vals[i]), db.Text(vals[i+1])})
	}
	return out
}

func TestColumnResizeSurvivesNewRows(t *testing.T) {
	g := components.NewGrid()
	g.SetSize(80, 12)
	g.SetData([]string{"a", "b"}, nil, cellRows("1", "x"))
	g.ResizeColumn(0, 20)
	g.AppendRows(cellRows("2", strings.Repeat("y", 30)))
	if hit := g.HitTest(g.Gutter()+1+20+1, 0); hit.Zone != components.GridColEdge || hit.Col != 0 {
		t.Fatalf("edge not where the user put it: %+v", hit)
	}
	g.SetData([]string{"a", "b"}, nil, cellRows("1", "x"))
	if hit := g.HitTest(g.Gutter()+1+20+1, 0); hit.Zone == components.GridColEdge && hit.Col == 0 {
		t.Fatal("new data should reset widths")
	}
}

// cellOf returns the screen column of the first sep at or after column x.
func cellOf(line string, x int, sep string) int {
	col := 0
	for i, r := range line {
		if col >= x && strings.HasPrefix(line[i:], sep) {
			return col
		}
		col += runewidth.RuneWidth(r)
	}
	return -1
}

func TestDragColumnEdgeAndAutofit(t *testing.T) {
	m, _, _ := resultsSetup(t)
	x, y := locate(t, m, "email")
	lines := strings.Split(ansi.Strip(m.View()), "\n")
	edge := cellOf(lines[y], x, "│")
	dragTo(m, edge, y, edge+15, y)
	if x2, y2 := locate(t, m, "email"); x2 != x || y2 != y {
		t.Fatalf("column moved: %d,%d", x2, y2)
	}
	lines = strings.Split(ansi.Strip(m.View()), "\n")
	if got := cellOf(lines[y], x, "│"); got != edge+15 {
		t.Fatalf("edge at %d, want %d", got, edge+15)
	}
	doubleClickAt(m, edge+15, y)
	lines = strings.Split(ansi.Strip(m.View()), "\n")
	if got := cellOf(lines[y], x, "│"); got >= edge+15 {
		t.Fatalf("autofit should shrink: %d", got)
	}
}

func TestFreezeKeepsColumnsWhileScrolling(t *testing.T) {
	g := components.NewGrid()
	g.SetSize(40, 8)
	cols := []string{"id", "c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8"}
	row := make([]db.Cell, len(cols))
	for i := range row {
		row[i] = db.Text(strings.Repeat("v", 8))
	}
	g.SetData(cols, nil, [][]db.Cell{row})
	g.Focus()
	if !g.ToggleFreeze() {
		t.Fatal("should freeze")
	}
	g.ScrollColumns(4)
	v := ansi.Strip(g.View())
	if !strings.Contains(v, "id") || !strings.Contains(v, "c5") || strings.Contains(v, "c1") {
		t.Fatalf("frozen column gone or view not scrolled:\n%s", v)
	}
	if !strings.Contains(v, "┃") {
		t.Fatalf("no frozen separator:\n%s", v)
	}
	g.SetCursorCol(0)
	if g.ToggleFreeze() {
		t.Fatal("should unfreeze")
	}
}

func TestWheelOnGridKeepsLoadingRows(t *testing.T) {
	g := components.NewGrid()
	g.SetSize(40, 12)
	var rows [][]db.Cell
	for i := 0; i < 50; i++ {
		rows = append(rows, []db.Cell{db.Text("v")})
	}
	g.SetData([]string{"a"}, nil, rows)
	for i := 0; i < 20; i++ {
		g.Scroll(wheelStep)
	}
	row, _ := g.CursorView()
	if row < 40 {
		t.Fatalf("cursor should stay in view: row %d", row)
	}
	if !g.AtBottom() {
		t.Fatal("the end of the rows is on screen, so more should load")
	}
}

func TestOutputSubTabsClickAndClose(t *testing.T) {
	m, tb, _ := resultsSetup(t)
	x, y := locate(t, m, "Messages")
	click(m, x, y)
	if _, ok := tb.out.current().(*messagesPanel); !ok {
		t.Fatalf("current is %T", tb.out.current())
	}
	r := tb.out.results()[0]
	title := stripANSI(r.title())
	x, y = locate(t, m, title)
	press(m, x, y, tea.MouseButtonMiddle)
	if len(tb.out.results()) != 0 {
		t.Fatal("middle click should close the panel")
	}
}

func TestPanelCloseGuardedByUnsavedEdits(t *testing.T) {
	m, tb, r := resultsSetup(t)
	r.deleted = map[int]bool{0: true}
	x, y := locate(t, m, stripANSI(r.title()))
	press(m, x, y, tea.MouseButtonMiddle)
	if len(tb.out.results()) != 1 {
		t.Fatal("unsaved edits must keep the panel open")
	}
}

func TestTextPanelDragSelects(t *testing.T) {
	m, tb, _ := newTestModel(t, devConn())
	p := &textPanel{name: "DDL", tv: components.NewTextView("CREATE TABLE users\n  id int", "")}
	tb.out.add(p)
	x, y := locate(t, m, "TABLE")
	dragTo(m, x, y, x+5, y)
	if s, ok := p.tv.Selected(); !ok || s != "TABLE" {
		t.Fatalf("selected %q %v", s, ok)
	}
	if got := p.tv.CopyText(); got != "TABLE" {
		t.Fatalf("copy %q", got)
	}
	click(m, x, y)
	if _, ok := p.tv.Selected(); ok {
		t.Fatal("click should clear the selection")
	}
}

func TestExplorerRightClickMenu(t *testing.T) {
	m, tb, _ := newTestModel(t, devConn())
	e := tb.ac.explorer
	if len(e.flat) == 0 {
		t.Skip("no nodes in the fake connection")
	}
	_ = m.View()
	r := m.frame.explorer
	press(m, r.x+5, r.y+1, tea.MouseButtonRight)
	if _, ok := m.modals[0].(*menuModal); !ok {
		t.Fatalf("modal %T", m.modals[0])
	}
}

func TestExplorerFilterShowsNoMatches(t *testing.T) {
	m, tb, _ := newTestModel(t, devConn())
	e := tb.ac.explorer
	e.filter = "zzzz-nothing"
	e.rebuild()
	m.focus = focusExplorer
	if v := ansi.Strip(m.View()); !strings.Contains(v, "no matches") {
		t.Fatalf("no empty state:\n%s", v)
	}
	key(m, "esc")
	if e.filter != "" || m.focus != focusExplorer {
		t.Fatal("esc should clear the filter first")
	}
	key(m, "esc")
	if m.focus != focusEditor {
		t.Fatal("esc with no filter should return to the editor")
	}
}

func TestContextMenuKey(t *testing.T) {
	m, _, _ := resultsSetup(t)
	drain(m, m.handleKey(tea.KeyMsg{Type: tea.KeyF10}))
	if len(m.modals) != 1 {
		t.Fatal("f10 should open the menu")
	}
	key(m, "esc")
	drain(m, m.handleKey(tea.KeyMsg{Type: tea.KeyF2}))
	if m.focus == focusOutput {
		t.Fatal("f2 should move focus")
	}
}

func TestConfirmButtons(t *testing.T) {
	m, _, _ := newTestModel(t, devConn())
	yes := false
	m.push(&confirmModal{title: "Sure", body: "Really?", onYes: func() tea.Cmd { yes = true; return nil }})
	x, y := locate(t, m, "n no")
	click(m, x, y)
	if yes || len(m.modals) != 0 {
		t.Fatalf("no button: yes=%v modals=%d", yes, len(m.modals))
	}
	m.push(&confirmModal{title: "Sure", body: "Really?", onYes: func() tea.Cmd { yes = true; return nil }})
	x, y = locate(t, m, "y yes")
	click(m, x, y)
	if !yes {
		t.Fatal("yes button did not confirm")
	}
}

func TestTypedConfirmCannotBeClickedThrough(t *testing.T) {
	m, _, _ := newTestModel(t, devConn())
	ran := false
	tc := newTypedConfirm("Drop", "Drops everything", "DROP", func() tea.Cmd { ran = true; return nil })
	m.push(tc)
	x, y := locate(t, m, "enter confirm")
	click(m, x, y)
	if ran || len(m.modals) != 1 {
		t.Fatal("confirm must need the typed text")
	}
	click(m, 0, 0)
	if len(m.modals) != 1 {
		t.Fatal("outside click must not close it")
	}
	tc.input.SetValue("DROP")
	click(m, x, y)
	if !ran {
		t.Fatal("confirm should run once the text matches")
	}
	tc2 := newTypedConfirm("Drop", "Drops everything", "DROP", func() tea.Cmd { ran = false; return nil })
	m.push(tc2)
	x, y = locate(t, m, "esc cancel")
	click(m, x, y)
	if len(m.modals) != 0 || !ran {
		t.Fatal("cancel should close without running")
	}
}

func TestPromptClickPlacesCursorAndSubmits(t *testing.T) {
	m, _, _ := newTestModel(t, devConn())
	got := ""
	p := newPrompt("Name", "A label", "abcdef", false, func(v string) tea.Cmd { got = v; return nil })
	m.push(p)
	x, y := locate(t, m, "abcdef")
	click(m, x+2, y)
	if p.input.Position() != 2 {
		t.Fatalf("cursor at %d", p.input.Position())
	}
	x, y = locate(t, m, "enter ok")
	click(m, x, y)
	if got != "abcdef" || len(m.modals) != 0 {
		t.Fatalf("got %q, %d modals", got, len(m.modals))
	}
}

func TestPickerClickRuns(t *testing.T) {
	m, _, _ := newTestModel(t, devConn())
	got := ""
	m.push(newPickerModal("Pick", []components.PickerItem{{Title: "alpha", Value: "a"}, {Title: "beta", Value: "b"}},
		func(it components.PickerItem) tea.Cmd { got = it.Value.(string); return nil }))
	x, y := locate(t, m, "beta")
	click(m, x, y)
	if got != "b" {
		t.Fatalf("got %q", got)
	}
}

func TestPickerMultiLineClick(t *testing.T) {
	m, _, _ := newTestModel(t, devConn())
	got := ""
	pm := newPickerModal("Pick", []components.PickerItem{{Title: "alpha", Detail: "first", Value: "a"}, {Title: "beta", Detail: "second", Value: "b"}},
		func(it components.PickerItem) tea.Cmd { got = it.Value.(string); return nil })
	pm.picker.MultiLine = true
	m.push(pm)
	x, y := locate(t, m, "second")
	click(m, x, y)
	if got != "b" {
		t.Fatalf("detail row: got %q", got)
	}
}

func TestPickerModalRanksAboveTextModalOnOutsideClick(t *testing.T) {
	m, _, _ := newTestModel(t, devConn())
	tm := newTextModal("T", "text", "")
	tm.onKey = func(string) (bool, tea.Cmd) { return false, nil }
	m.push(tm)
	_ = m.View()
	click(m, 0, 0)
	if len(m.modals) != 1 {
		t.Fatal("a text view that decides something must not close on an outside click")
	}
}

func managerSetup(t *testing.T) (*Model, *connManager) {
	t.Helper()
	m, _, _ := newTestModel(t, devConn())
	m.conns = []config.Connection{{ID: "a", Name: "alpha", Driver: "sqlite", Database: "a.db"}, {ID: "b", Name: "beta", Driver: "sqlite", Database: "b.db"}}
	m.openConnections()
	_ = m.View()
	return m, m.mgr
}

func TestManagerClickAndMenu(t *testing.T) {
	m, c := managerSetup(t)
	x, y := locate(t, m, "beta")
	click(m, x, y)
	if cfg, ok := c.selected(m); !ok || cfg.ID != "b" {
		t.Fatalf("selected %+v", cfg)
	}
	press(m, x, y, tea.MouseButtonRight)
	if len(m.modals) != 1 {
		t.Fatal("no context menu")
	}
	ex, ey := locate(t, m, "Edit")
	click(m, ex, ey)
	if c.editing == nil || c.editing.ID != "b" {
		t.Fatal("Edit should open the form")
	}
}

func TestManagerFooterClick(t *testing.T) {
	m, c := managerSetup(t)
	x, y := locate(t, m, "ctrl+n new")
	click(m, x, y)
	if c.editing == nil {
		t.Fatal("clicking the hint should open the new connection form")
	}
}

func TestManagerFooterWrapsOnNarrowTerminal(t *testing.T) {
	m, _ := managerSetup(t)
	m.width = 60
	v := ansi.Strip(m.View())
	if !strings.Contains(v, "esc back") {
		t.Fatalf("esc back was cut off:\n%s", v)
	}
	if n := len(strings.Split(v, "\n")); n != m.height {
		t.Fatalf("%d lines, want %d", n, m.height)
	}
}

func TestManagerShowsToastAndConnecting(t *testing.T) {
	m, _ := managerSetup(t)
	drain(m, m.toast("Saved it", toastInfo))
	if v := ansi.Strip(m.View()); !strings.Contains(v, "Saved it") {
		t.Fatalf("no toast on the manager:\n%s", v)
	}
	m.connecting["a"] = true
	m.connectSince = time.Now()
	if v := ansi.Strip(m.View()); !strings.Contains(v, "Connecting to alpha") {
		t.Fatalf("no progress:\n%s", v)
	}
}

func TestFormClickToggleSelectAndText(t *testing.T) {
	m, c := managerSetup(t)
	c.edit(m, config.Connection{Driver: "postgres", Host: "dbhost", Port: 5432})
	x, y := locate(t, m, "mysql")
	click(m, x, y)
	if c.form.Value("driver") != "mysql" {
		t.Fatalf("driver = %q", c.form.Value("driver"))
	}
	if c.form.Int("port") != 3306 {
		t.Fatalf("port should follow the driver: %d", c.form.Int("port"))
	}
	x, y = locate(t, m, "dbhost")
	click(m, x, y)
	if c.form.FocusedKey() != "host" {
		t.Fatalf("focused %q", c.form.FocusedKey())
	}
	x, y = locate(t, m, "Read-only")
	click(m, x, y)
	if !c.form.BoolValue("read_only") {
		t.Fatal("click should flip the toggle")
	}
}

func TestFormEscAsksBeforeDiscarding(t *testing.T) {
	m, c := managerSetup(t)
	c.edit(m, config.Connection{Driver: "postgres", Host: "dbhost", Port: 5432})
	_ = m.View()
	key(m, "esc")
	if c.editing != nil || len(m.modals) != 0 {
		t.Fatal("an unchanged form should just close")
	}
	c.edit(m, config.Connection{Driver: "postgres", Host: "dbhost", Port: 5432})
	_ = m.View()
	key(m, "x")
	key(m, "esc")
	if c.editing == nil || len(m.modals) != 1 {
		t.Fatal("edits should be confirmed before they are thrown away")
	}
	key(m, "n")
	if c.editing == nil {
		t.Fatal("no should keep the form")
	}
	key(m, "esc", "y")
	if c.editing != nil {
		t.Fatal("yes should close the form")
	}
}

func TestSaveFocusesBadField(t *testing.T) {
	m, c := managerSetup(t)
	c.edit(m, config.Connection{Driver: "postgres", Port: 5432})
	c.form.FocusKey("name")
	drain(m, c.save(m))
	if c.form.FocusedKey() != "host" {
		t.Fatalf("focused %q", c.form.FocusedKey())
	}
}

func TestStatusBarClicks(t *testing.T) {
	m, tb, _ := newTestModel(t, config.Connection{ID: "c", Name: "dev", Driver: "postgres", ReadOnly: true})
	tb.db = "shop"
	tb.manualTx = true
	x, y := locate(t, m, "manual tx")
	click(m, x, y)
	if _, ok := m.modals[0].(*menuModal); !ok {
		t.Fatalf("modal %T", m.modals[0])
	}
	key(m, "esc")
	x, y = locate(t, m, "READ-ONLY")
	click(m, x, y)
	if len(m.modals) != 1 {
		t.Fatal("no menu")
	}
	if !tb.ac.cfg.ReadOnly {
		t.Fatal("a click must not enable writes")
	}
	key(m, "esc")
	x, y = locate(t, m, "help")
	click(m, x, y)
	if _, ok := m.modals[0].(*textModal); !ok {
		t.Fatalf("modal %T", m.modals[0])
	}
}

func TestStatusBarErrorOpensMessages(t *testing.T) {
	m, tb, _ := newTestModel(t, devConn())
	tb.out.add(&textPanel{name: "DDL", tv: components.NewTextView("x", "")})
	drain(m, m.toast("something failed", toastError))
	x, y := locate(t, m, "something failed")
	click(m, x, y)
	if _, ok := tb.out.current().(*messagesPanel); !ok || m.focus != focusOutput {
		t.Fatalf("panel %T focus %v", tb.out.current(), m.focus)
	}
}

func TestDeleteRowHintAppears(t *testing.T) {
	m, _, r := resultsSetup(t)
	key(m, "d")
	if !r.pendingD || !strings.Contains(m.toastText, "again") {
		t.Fatalf("toast %q", m.toastText)
	}
}

func TestDetailRetriesFailedDDL(t *testing.T) {
	m, tb, _ := newTestModel(t, devConn())
	p := newDetailPanel(tb.ac, &db.TableDetail{}, 40)
	tb.out.add(p)
	p.ix = len(p.names) - 1
	p.ddlErr = "boom"
	m.focus = focusOutput
	if v := ansi.Strip(m.View()); !strings.Contains(v, "r retries") {
		t.Fatalf("no retry hint:\n%s", v)
	}
	key(m, "r")
	if p.ddlErr != "" {
		t.Fatal("r should clear the error and reload")
	}
}

func TestEditorPlaceholderWhenFocused(t *testing.T) {
	m, tb, _ := newTestModel(t, devConn())
	m.focus = focusEditor
	tb.editor.SetValue("")
	if v := ansi.Strip(m.View()); !strings.Contains(v, "write SQL here") {
		t.Fatalf("no placeholder while focused:\n%s", v)
	}
}

func TestHLReachGridInDetailPanel(t *testing.T) {
	m, tb, _ := newTestModel(t, devConn())
	p := newDetailPanel(tb.ac, &db.TableDetail{}, 40)
	tb.out.add(p)
	m.focus = focusOutput
	_ = m.View()
	ix := p.ix
	key(m, "L")
	if p.ix != ix {
		t.Fatal("L should go to the grid, not switch sub-tabs")
	}
	key(m, ">")
	if p.ix == ix {
		t.Fatal("> should switch sub-tabs")
	}
}

func TestEveryMenuFitsNarrowTerminals(t *testing.T) {
	for _, sz := range [][2]int{{50, 14}, {80, 24}} {
		m, _, _ := resultsSetup(t)
		m.width, m.height = sz[0], sz[1]
		drain(m, m.handleKey(tea.KeyMsg{Type: tea.KeyF10}))
		lines := strings.Split(m.View(), "\n")
		if len(lines) != sz[1] {
			t.Fatalf("%dx%d: %d lines", sz[0], sz[1], len(lines))
		}
		for _, l := range lines {
			if w := ansi.StringWidth(l); w > sz[0] {
				t.Fatalf("%dx%d: line %d cells wide", sz[0], sz[1], w)
			}
		}
	}
}

func TestPanelsRenderAtSmallSizes(t *testing.T) {
	m, tb, _ := resultsSetup(t)
	root := &db.PlanNode{Title: "Seq Scan", Children: []*db.PlanNode{{Title: "Index"}}}
	panels := []panel{
		newDetailPanel(tb.ac, &db.TableDetail{}, 40),
		&textPanel{name: "DDL", tv: components.NewTextView("CREATE TABLE x (\n  id int\n)", "")},
		newExplainPanel("SELECT 1", root, false),
	}
	for _, p := range panels {
		tb.out.add(p)
		for _, sz := range [][2]int{{50, 14}, {80, 24}} {
			m.width, m.height = sz[0], sz[1]
			if n := len(strings.Split(m.View(), "\n")); n != sz[1] {
				t.Errorf("%T at %dx%d: %d lines", p, sz[0], sz[1], n)
			}
		}
	}
}

func TestExplainClickToggles(t *testing.T) {
	m, tb, _ := newTestModel(t, devConn())
	root := &db.PlanNode{Title: "Seq Scan", Children: []*db.PlanNode{{Title: "Index Lookup"}}}
	p := newExplainPanel("SELECT 1", root, false)
	tb.out.add(p)
	x, y := locate(t, m, "Seq Scan")
	click(m, x-2, y) // the arrow
	if _, ok := p.collapsed[root]; !ok || !p.collapsed[root] {
		t.Fatal("clicking the arrow should collapse the node")
	}
	if strings.Contains(ansi.Strip(m.View()), "Index Lookup") {
		t.Fatal("child still shown")
	}
}

func TestCopyMenuOffersSelection(t *testing.T) {
	m, _, _ := resultsSetup(t)
	x, y := locate(t, m, "a@b.c")
	dragTo(m, x, y, x, y+2)
	key(m, "c")
	if v := ansi.Strip(m.View()); !strings.Contains(v, "Selection as CSV") {
		t.Fatalf("no selection in the copy menu:\n%s", v)
	}
}

func TestMouseIgnoredWhenTooSmall(t *testing.T) {
	m, _ := managerSetup(t)
	m.width, m.height = 40, 10
	_ = m.View()
	click(m, 5, 5)
	if m.mgr.editing != nil || len(m.modals) != 0 {
		t.Fatal("nothing is on screen to click")
	}
}
