package components

import "github.com/mattn/go-runewidth"

// Mouse coordinates here are relative to the editor's content area, the
// gutter included.

// posAt returns the buffer position under a cell. Points past the edges map
// to the row just beyond the view, so holding a drag there scrolls.
func (e *Editor) posAt(x, y int) pos {
	r := clampInt(e.top+y, 0, len(e.lines)-1)
	line := e.lines[r]
	tx := max(0, x-e.gutterWidth()+e.left)
	col := 0
	for c, rn := range line {
		w := runewidth.RuneWidth(rn)
		if tx < col+w {
			return pos{r, c}
		}
		col += w
	}
	return pos{r, len(line)}
}

func (e *Editor) moveTo(p pos) {
	e.cur = p
	e.clampCursor()
	e.wantCol = e.cur.c
	e.viewDetached = false
	e.scroll()
}

// ClearSelection ends any selection, leaving the cursor where it is.
func (e *Editor) ClearSelection() {
	e.selecting = false
	if e.mode == ModeVisual || e.mode == ModeVisualLine {
		e.mode = ModeNormal
	}
}

// enterVisual switches a vim editor to a visual mode anchored at p.
func (e *Editor) enterVisual(mode EditorMode, p pos) {
	e.inInsert = false
	e.mode, e.anchor = mode, p
}

// MouseDown handles a left press. Clicks is 1 (place the cursor), 2 (select
// the word) or 3 (select the line). A vim editor keeps its mode on a single
// click and enters visual mode for a selection.
func (e *Editor) MouseDown(x, y, clicks int) {
	e.complOn = false
	e.pending, e.count = "", 0
	p := e.posAt(x, y)
	switch clicks {
	case 2:
		a, b := e.wordBounds(p)
		if e.Vim {
			e.enterVisual(ModeVisual, a)
			if a.less(b) {
				b.c--
			}
		} else {
			e.selecting, e.anchor = true, a
		}
		e.moveTo(b)
	case 3:
		a, b := pos{p.r, 0}, pos{p.r, len(e.lines[p.r])}
		if e.Vim {
			e.enterVisual(ModeVisualLine, a)
		} else {
			e.selecting, e.anchor = true, a
		}
		e.moveTo(b)
	default:
		e.ClearSelection()
		e.anchor = p
		e.moveTo(p)
		e.anchor = e.cur
	}
}

// MouseDrag extends the selection from the press position to the pointer.
func (e *Editor) MouseDrag(x, y int) {
	p := e.posAt(x, y)
	if !e.hasSelection() {
		if p == e.anchor {
			return
		}
		if e.Vim {
			e.enterVisual(ModeVisual, e.anchor)
		} else {
			e.selecting = true
		}
	}
	e.moveTo(p)
}

// ScrollView moves the view by n rows without moving the cursor. The next key
// or edit brings the cursor back into view.
func (e *Editor) ScrollView(n int) {
	e.complOn = false
	e.top = clampInt(e.top+n, 0, max(0, len(e.lines)-e.height))
	e.viewDetached = true
}

// ScrollColumns moves the view sideways by n cells without moving the cursor.
func (e *Editor) ScrollColumns(n int) {
	e.complOn = false
	e.left = max(0, e.left+n)
	e.viewDetached = true
}

// SelectionContains reports whether the cell is on the current selection.
func (e *Editor) SelectionContains(x, y int) bool {
	if !e.hasSelection() {
		return false
	}
	a, b := e.selRange()
	p := e.posAt(x, y)
	return !p.less(a) && p.less(b)
}

// SelectAll selects the whole buffer.
func (e *Editor) SelectAll() {
	a := pos{0, 0}
	b := pos{len(e.lines) - 1, len(e.lines[len(e.lines)-1])}
	if e.Vim {
		e.enterVisual(ModeVisual, a)
	} else {
		e.selecting, e.anchor = true, a
	}
	e.moveTo(b)
}

// CopySelection puts the selection in the editor's register and returns it.
func (e *Editor) CopySelection() (string, bool) {
	if !e.hasSelection() {
		return "", false
	}
	e.register, e.regLinewise = e.selectedText(), false
	if e.Vim {
		e.ClearSelection()
	}
	return e.register, true
}

// CutSelection copies the selection and deletes it.
func (e *Editor) CutSelection() (string, bool) {
	if !e.hasSelection() {
		return "", false
	}
	e.register, e.regLinewise = e.selectedText(), false
	e.pushUndo()
	e.deleteSelection()
	e.changed()
	e.scroll()
	return e.register, true
}

func (e *Editor) deleteSelection() {
	a, b := e.selRange()
	e.deleteRange(a, b)
	e.cur = a
	e.ClearSelection()
	e.clampCursor()
}

// Paste replaces the selection with s, or inserts s at the cursor.
func (e *Editor) Paste(s string) {
	e.pushUndo()
	if e.hasSelection() {
		e.deleteSelection()
	}
	e.cur = e.insertAt(e.cur, s)
	e.changed()
	e.clampCursor()
	e.scroll()
}

const complMaxItems = 8

// complGeometry returns the first and end item shown by the completion popup,
// its position and size in the content area.
func (e *Editor) complGeometry() (start, end, x, y, w, h int) {
	items := e.compl
	if e.complIx >= complMaxItems {
		start = e.complIx - complMaxItems + 1
	}
	end = min(len(items), start+complMaxItems)
	lw, dw := e.complWidths(start, end)
	w = lw + dw + 3
	h = end - start
	if len(items) > complMaxItems {
		h++
	}
	x = e.gutterWidth() + displayCol(e.lines[e.complAt.r], e.complAt.c) - e.left
	y = e.cur.r - e.top + 1
	if y+h > e.height && e.cur.r-e.top-h >= 0 {
		y = e.cur.r - e.top - h
	}
	if x+w > e.width {
		x = max(0, e.width-w)
	}
	return start, end, x, y, w, h
}

func (e *Editor) complWidths(start, end int) (lw, dw int) {
	for _, it := range e.compl[start:end] {
		lw = max(lw, runewidth.StringWidth(it.Label))
		dw = max(dw, runewidth.StringWidth(it.Detail)+runewidth.StringWidth(it.Kind)+1)
	}
	return min(lw, 40), min(dw, 30)
}

// CompletionHit reports whether the cell is on the completion popup, and which
// item it is on (-1 for the counter row).
func (e *Editor) CompletionHit(x, y int) (item int, inside bool) {
	if !e.complOn || !e.focused {
		return -1, false
	}
	start, end, px, py, w, h := e.complGeometry()
	if x < px || x >= px+w || y < py || y >= py+h {
		return -1, false
	}
	if i := start + y - py; i < end {
		return i, true
	}
	return -1, true
}

// AcceptCompletionAt inserts completion item i.
func (e *Editor) AcceptCompletionAt(i int) {
	e.complIx = i
	e.acceptCompletion()
	e.scroll()
}

// ScrollCompletion moves the completion selection by d.
func (e *Editor) ScrollCompletion(d int) {
	e.complIx = clampInt(e.complIx+d, 0, len(e.compl)-1)
	e.complNav = true
}
