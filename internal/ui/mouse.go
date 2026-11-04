package ui

import (
	"regexp"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/farvisun/squi/internal/ui/components"
)

// rect is a screen area in terminal cells.
type rect struct{ x, y, w, h int }

func (r rect) has(x, y int) bool {
	return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

// hitSpan is a clickable column range [x0, x1) on one row, standing for item ix.
type hitSpan struct{ x0, x1, ix int }

// spanAt returns the item under column x, or -1.
func spanAt(spans []hitSpan, x int) int {
	for _, s := range spans {
		if x >= s.x0 && x < s.x1 {
			return s.ix
		}
	}
	return -1
}

// frame is the screen geometry of the last render. View records it, so mouse
// hits are tested against what is really on screen, zoomed or not.
type frame struct {
	explorer, editor, output rect // outer boxes; empty when not shown
	tabBar                   tabBarHits
	status                   []statusHit
	modal                    rect // the topmost modal
}

// mouseEvent is a button press or wheel step at surface-local coordinates.
type mouseEvent struct {
	x, y   int
	btn    tea.MouseButton
	clicks int // 1, 2 or 3 for repeated presses at the same cell
	alt    bool
	wheel  bool
}

// wheelStep is how many rows one wheel notch moves.
const wheelStep = 3

// delta returns the wheel direction scaled by wheelStep (0 if not vertical).
func (ev mouseEvent) delta() int {
	switch ev.btn {
	case tea.MouseButtonWheelUp:
		return -wheelStep
	case tea.MouseButtonWheelDown:
		return wheelStep
	}
	return 0
}

// clickTracker counts repeated presses of a button at one cell.
type clickTracker struct {
	x, y  int
	btn   tea.MouseButton
	at    time.Time
	count int
}

func (c *clickTracker) press(x, y int, b tea.MouseButton, now time.Time) int {
	if x == c.x && y == c.y && b == c.btn && now.Sub(c.at) < doubleClick && c.count < 3 {
		c.count++
	} else {
		c.count = 1
	}
	c.x, c.y, c.btn, c.at = x, y, b, now
	return c.count
}

// dragState captures the pointer from a press until its release, wherever
// the pointer goes.
type dragState struct {
	move   func(x, y int) tea.Cmd
	end    func() // optional
	bounds rect   // while the pointer is outside, move repeats on a timer (autoscroll)
	x, y   int
	id     int
	active bool // the autoscroll timer is running
}

type dragTickMsg struct{ id int }

const dragTickEvery = 60 * time.Millisecond

func dragTick(id int) tea.Cmd {
	return tea.Tick(dragTickEvery, func(time.Time) tea.Msg { return dragTickMsg{id} })
}

// startDrag captures the pointer. A zero bounds rect means no autoscroll.
func (m *Model) startDrag(d *dragState) {
	m.dragSeq++
	d.id = m.dragSeq
	m.drag = d
}

// endDrag finishes the current drag; safe to call without one.
func (m *Model) endDrag() {
	if d := m.drag; d != nil {
		m.drag = nil
		if d.end != nil {
			d.end()
		}
	}
}

func (m *Model) dragMoved(x, y int) tea.Cmd {
	d := m.drag
	d.x, d.y = x, y
	cmd := d.move(x, y)
	if d.bounds.w > 0 && !d.bounds.has(x, y) && !d.active {
		d.active = true
		return tea.Batch(cmd, dragTick(d.id))
	}
	return cmd
}

// handleDragTick repeats the move while the pointer rests outside the bounds.
func (m *Model) handleDragTick(msg dragTickMsg) tea.Cmd {
	d := m.drag
	if d == nil || d.id != msg.id {
		return nil
	}
	if d.bounds.has(d.x, d.y) {
		d.active = false
		return nil
	}
	return tea.Batch(d.move(d.x, d.y), dragTick(d.id))
}

// mouseLeak matches a mouse report whose ESC arrived alone, so the terminal
// input parser delivered the rest as typed text.
var mouseLeak = regexp.MustCompile(`^\[<\d+;\d+;\d+[Mm]$`)

func (m *Model) handleMouse(msg tea.MouseMsg) tea.Cmd {
	if m.tooSmall() {
		return nil
	}
	if m.drag != nil {
		switch {
		case msg.Action == tea.MouseActionMotion:
			return m.dragMoved(msg.X, msg.Y)
		case msg.Action == tea.MouseActionRelease:
			m.endDrag()
			return nil
		case !tea.MouseEvent(msg).IsWheel():
			m.endDrag() // the release was lost, e.g. outside the terminal window
		}
	}
	if msg.Action != tea.MouseActionPress {
		return nil
	}
	ev := mouseEvent{x: msg.X, y: msg.Y, btn: msg.Button, alt: msg.Alt, wheel: tea.MouseEvent(msg).IsWheel()}
	if !ev.wheel {
		ev.clicks = m.clicks.press(msg.X, msg.Y, msg.Button, time.Now())
	}
	if len(m.modals) > 0 {
		return m.mouseModal(msg, ev)
	}
	if m.screen == screenConnections || len(m.tabs) == 0 {
		if m.mgr != nil {
			return m.mgr.mouse(m, ev)
		}
		return nil
	}
	if !ev.wheel {
		m.cmdline = nil
		m.winPrefix = false
	}
	return m.mouseMain(ev)
}

// mouseModal gives the event to the topmost modal in box-local coordinates.
// A press outside closes the modal only if it holds nothing to lose.
func (m *Model) mouseModal(msg tea.MouseMsg, ev mouseEvent) tea.Cmd {
	md := m.modals[len(m.modals)-1]
	box := m.frame.modal
	if !ev.wheel && !box.has(ev.x, ev.y) {
		if d, ok := md.(dismissible); ok && d.dismissOnOutsideClick() {
			m.removeModal(md)
		}
		return nil
	}
	msg.X -= box.x
	msg.Y -= box.y
	done, cmd := md.update(m, msg)
	if done {
		m.removeModal(md)
	}
	return cmd
}

// dismissible marks modals that an outside click may close.
type dismissible interface{ dismissOnOutsideClick() bool }

func (*menuModal) dismissOnOutsideClick() bool   { return true }
func (*pickerModal) dismissOnOutsideClick() bool { return true }

// A text view with its own keys decides something on close, like applying SQL.
func (t *textModal) dismissOnOutsideClick() bool { return t.onKey == nil }

// anchored is a modal drawn at a screen position instead of centered.
type anchored interface{ anchor() (x, y int, ok bool) }

// modalPosition returns where to draw a modal box of size bw×bh.
func (m *Model) modalPosition(md modal, bw, bh int) (int, int) {
	if a, ok := md.(anchored); ok {
		if x, y, ok := a.anchor(); ok {
			if x+bw > m.width {
				x = max(0, m.width-bw)
			}
			if y+bh > m.height {
				y = max(0, y-bh+1)
			}
			if y+bh > m.height {
				y = max(0, m.height-bh)
			}
			return x, y
		}
	}
	return components.Center(m.width, m.height, bw, bh)
}

func (m *Model) mouseMain(ev mouseEvent) tea.Cmd {
	t := m.currentTab()
	if t == nil {
		return nil
	}
	switch ev.y {
	case 0:
		return m.mouseTabBar(ev)
	case m.height - 1:
		return m.mouseStatus(t, ev)
	}
	if ev.btn == tea.MouseButtonLeft && m.mouseSplit(ev) {
		return nil
	}
	f := &m.frame
	switch {
	case f.explorer.has(ev.x, ev.y):
		return m.mouseExplorer(t, ev)
	case f.editor.has(ev.x, ev.y):
		return m.mouseEditor(t, ev)
	case f.output.has(ev.x, ev.y):
		return m.mouseOutput(t, ev)
	}
	return nil
}

func (m *Model) mouseExplorer(t *tab, ev mouseEvent) tea.Cmd {
	e := t.ac.explorer
	if !ev.wheel {
		m.focus = focusExplorer
	}
	if e == nil {
		return nil
	}
	r := m.frame.explorer
	switch ev.btn {
	case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
		e.scrollView(ev.delta())
	case tea.MouseButtonLeft:
		return e.click(m, ev.y-r.y-1, ev.x-r.x)
	case tea.MouseButtonRight:
		if i := e.rowAt(ev.y - r.y - 1); i >= 0 {
			e.ix = i
			e.menu(m, ev.x, ev.y)
		}
	}
	return nil
}

// contextMenu opens the focused pane's menu, for keyboards and for terminals
// that keep right-click for themselves.
func (m *Model) contextMenu(t *tab) tea.Cmd {
	switch m.focus {
	case focusExplorer:
		if e := t.ac.explorer; e != nil {
			r := m.frame.explorer
			e.menu(m, r.x+2, r.y+2)
		}
	case focusEditor:
		r := m.frame.editor
		m.editorMenu(t, r.x+2, r.y+2)
	case focusOutput:
		if p, ok := t.out.current().(*resultPanel); ok {
			r := m.frame.output
			p.menu(m, t, r.x+2, r.y+2)
		}
	}
	return nil
}

func (m *Model) mouseEditor(t *tab, ev mouseEvent) tea.Cmd {
	r := m.frame.editor
	x, y := ev.x-r.x-1, ev.y-r.y-1
	item, onPopup := t.editor.CompletionHit(x, y)
	if ev.wheel {
		switch d := ev.delta(); {
		case onPopup:
			t.editor.ScrollCompletion(d / wheelStep)
		case ev.alt && d != 0:
			t.editor.ScrollColumns(d)
		case d != 0:
			t.editor.ScrollView(d)
		case ev.btn == tea.MouseButtonWheelLeft:
			t.editor.ScrollColumns(-wheelStep)
		case ev.btn == tea.MouseButtonWheelRight:
			t.editor.ScrollColumns(wheelStep)
		}
		return nil
	}
	m.focus = focusEditor
	if x < 0 || y < 0 || x >= r.w-2 || y >= r.h-2 {
		return nil // the border only focuses
	}
	switch ev.btn {
	case tea.MouseButtonLeft:
		if onPopup {
			if item >= 0 {
				t.editor.AcceptCompletionAt(item)
			}
			return nil
		}
		t.editor.MouseDown(x, y, ev.clicks)
		m.startDrag(&dragState{bounds: rect{r.x + 1, r.y + 1, r.w - 2, r.h - 2}, move: func(px, py int) tea.Cmd {
			t.editor.MouseDrag(px-r.x-1, py-r.y-1)
			return nil
		}})
	case tea.MouseButtonRight:
		if !t.editor.SelectionContains(x, y) {
			t.editor.MouseDown(x, y, 1)
		}
		m.editorMenu(t, ev.x, ev.y)
	}
	return nil
}

// editorMenu opens the editor's context menu at (x, y).
func (m *Model) editorMenu(t *tab, x, y int) {
	m.pushMenu("Query", []menuItem{
		{"x", "Cut", func() tea.Cmd {
			if s, ok := t.editor.CutSelection(); ok {
				return m.copy(s, "text")
			}
			return nil
		}},
		{"y", "Copy", func() tea.Cmd {
			if s, ok := t.editor.CopySelection(); ok {
				return m.copy(s, "text")
			}
			return nil
		}},
		{"v", "Paste", func() tea.Cmd {
			t.editor.Paste(pasteText(t.editor.Register()))
			return nil
		}},
		{"a", "Select all", func() tea.Cmd { t.editor.SelectAll(); return nil }},
		{"r", "Run", func() tea.Cmd { return m.runCurrent(t) }},
		{"f", "Format", func() tea.Cmd { return m.formatSQL(t) }},
	}, x, y)
}

func (m *Model) mouseOutput(t *tab, ev mouseEvent) tea.Cmd {
	r := m.frame.output
	if !ev.wheel {
		m.focus = focusOutput
	}
	ox, oy := r.x+1, r.y+2 // inside the border, below the sub-tabs
	x := ev.x - ox
	if ev.y == r.y+1 { // the sub-tab strip
		i := spanAt(t.out.spans, x)
		switch ev.btn {
		case tea.MouseButtonWheelUp:
			t.out.ix = (t.out.ix - 1 + len(t.out.panels)) % len(t.out.panels)
		case tea.MouseButtonWheelDown:
			t.out.ix = (t.out.ix + 1) % len(t.out.panels)
		case tea.MouseButtonLeft:
			if i >= 0 {
				t.out.ix = i
			}
		case tea.MouseButtonMiddle:
			if i >= 0 {
				return t.out.closePanel(m, t.out.panels[i])
			}
		case tea.MouseButtonRight:
			if i >= 0 {
				p := t.out.panels[i]
				m.pushMenu(stripANSI(p.title()), []menuItem{
					{"x", "Close", func() tea.Cmd { return t.out.closePanel(m, p) }},
				}, ev.x, ev.y)
			}
		}
		return nil
	}
	if !ev.wheel && (x < 0 || x >= r.w-2 || ev.y == r.y || ev.y >= r.y+r.h-1) {
		return nil // the border only focuses
	}
	p := t.out.current()
	if mp, ok := p.(mousePanel); ok {
		return mp.mouse(m, t, ev, ox, oy)
	}
	if s, ok := p.(scroller); ok && ev.delta() != 0 {
		s.scroll(ev.delta())
	}
	return nil
}

// gridHooks are what a panel does with grid gestures; any may be nil.
type gridHooks struct {
	sort  func() tea.Cmd // header clicked; the cursor is on that column
	open  func() tea.Cmd // cell double-clicked
	fetch func() tea.Cmd // the view moved, more rows may be wanted
	menu  func(x, y int) // right-click at a screen position
}

// mouseGrid handles wheel, click, range and column gestures on a grid whose
// top-left cell is at screen position (ox, oy).
func (m *Model) mouseGrid(g *components.Grid, ev mouseEvent, ox, oy int, h gridHooks) tea.Cmd {
	fetch := func() tea.Cmd {
		if h.fetch != nil {
			return h.fetch()
		}
		return nil
	}
	if ev.wheel {
		d := ev.delta()
		switch {
		case ev.btn == tea.MouseButtonWheelLeft:
			g.ScrollColumns(-1)
		case ev.btn == tea.MouseButtonWheelRight:
			g.ScrollColumns(1)
		case ev.alt && d != 0:
			g.ScrollColumns(d / wheelStep)
		case d != 0:
			g.Scroll(d)
		}
		return fetch()
	}
	hit := g.HitTest(ev.x-ox, ev.y-oy)
	w, ht := g.Size()
	bounds := rect{ox, oy, w, ht}
	switch ev.btn {
	case tea.MouseButtonLeft:
		switch hit.Zone {
		case components.GridColEdge:
			if ev.clicks == 2 {
				g.AutofitColumn(hit.Col)
				return nil
			}
			m.startDrag(&dragState{move: func(x, _ int) tea.Cmd {
				g.ResizeColumn(hit.Col, x-ox-hit.X0-2)
				return nil
			}})
		case components.GridHeader:
			g.SetCursorCol(hit.Col)
			if h.sort != nil {
				return h.sort()
			}
		case components.GridGutter:
			g.PressRow(hit.Row)
			m.startDrag(&dragState{bounds: bounds, move: func(x, y int) tea.Cmd {
				g.DragTo(x-ox, y-oy)
				return fetch()
			}})
		case components.GridCell:
			g.PressCell(hit.Row, hit.Col)
			if ev.clicks >= 2 {
				if h.open != nil {
					return h.open()
				}
				return nil
			}
			m.startDrag(&dragState{bounds: bounds, move: func(x, y int) tea.Cmd {
				g.DragTo(x-ox, y-oy)
				return fetch()
			}})
		}
	case tea.MouseButtonRight:
		if hit.Zone == components.GridCell && h.menu != nil {
			if !g.InRange(hit.Row, hit.Col) {
				g.PressCell(hit.Row, hit.Col)
			}
			h.menu(ev.x, ev.y)
		}
	}
	return nil
}

// mouseText handles wheel, selection and the context menu of a text view
// whose top-left cell is at screen position (ox, oy).
func (m *Model) mouseText(tv *components.TextView, ev mouseEvent, ox, oy int) tea.Cmd {
	if ev.wheel {
		d := ev.delta()
		switch {
		case ev.btn == tea.MouseButtonWheelLeft:
			tv.ScrollColumns(-4)
		case ev.btn == tea.MouseButtonWheelRight:
			tv.ScrollColumns(4)
		case ev.alt && d != 0:
			tv.ScrollColumns(d)
		case d != 0:
			tv.Scroll(d)
		}
		return nil
	}
	switch ev.btn {
	case tea.MouseButtonLeft:
		tv.PressAt(ev.x-ox, ev.y-oy)
		w, h := tv.Size()
		m.startDrag(&dragState{bounds: rect{ox, oy, w, h}, move: func(x, y int) tea.Cmd {
			tv.DragTo(x-ox, y-oy)
			return nil
		}})
	case tea.MouseButtonRight:
		label := "Copy all"
		if _, ok := tv.Selected(); ok {
			label = "Copy selection"
		}
		m.pushMenu("Text", []menuItem{
			{"y", label, func() tea.Cmd { return m.copy(tv.CopyText(), "text") }},
		}, ev.x, ev.y)
	}
	return nil
}

func (m *Model) toggleMouse() tea.Cmd {
	m.mouseOn = !m.mouseOn
	if m.mouseOn {
		return tea.Batch(tea.EnableMouseCellMotion, m.toast("Mouse on", toastInfo))
	}
	return tea.Batch(tea.DisableMouse, m.toast("Mouse off: the terminal selects text. "+m.keys.First("window")+" m turns it back on", toastInfo))
}

// statusHit is a clickable segment of the status bar.
type statusHit struct {
	hitSpan
	run func() tea.Cmd
}

func (m *Model) mouseStatus(t *tab, ev mouseEvent) tea.Cmd {
	if ev.btn != tea.MouseButtonLeft {
		return nil
	}
	for _, h := range m.frame.status {
		if ev.x >= h.x0 && ev.x < h.x1 {
			return h.run()
		}
	}
	return nil
}
