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
	outTabs                  []hitSpan // output sub-tabs, on the row below output's top border
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
func (*textModal) dismissOnOutsideClick() bool   { return true }

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
		e.ix = clampIx(e.ix+ev.delta(), len(e.flat))
	case tea.MouseButtonLeft:
		return e.click(m, ev.y-r.y-1, ev.x-r.x)
	}
	return nil
}

func (m *Model) mouseEditor(t *tab, ev mouseEvent) tea.Cmd {
	if !ev.wheel {
		m.focus = focusEditor
	}
	return nil
}

func (m *Model) mouseOutput(t *tab, ev mouseEvent) tea.Cmd {
	if !ev.wheel {
		m.focus = focusOutput
		return nil
	}
	if s, ok := t.out.current().(scroller); ok {
		s.scroll(ev.delta())
	}
	if r, ok := t.out.current().(*resultPanel); ok {
		return r.maybeFetch(m, t)
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
	return nil
}
