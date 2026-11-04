package components

import "github.com/mattn/go-runewidth"

// colSpan is a column on screen: its cells start at x0 and are w wide, with
// one space each side and the separator after.
type colSpan struct{ col, x0, w int }

func (s colSpan) sep() int { return s.x0 + s.w + 2 }

// columnSpans returns the columns on screen, frozen ones first. The last may
// be cut short when enough of it fits to be useful.
func (g *Grid) columnSpans() []colSpan {
	gw := g.gutter()
	avail := g.width - gw
	var out []colSpan
	used := 0
	add := func(c int) bool {
		w := min(g.widths[c], avail-used-3)
		if w < 1 {
			return false
		}
		out = append(out, colSpan{c, gw + used, w})
		used += w + 3
		return true
	}
	f := g.frozenCols()
	for c := 0; c < f; c++ {
		if !add(c) {
			return out
		}
	}
	for c := max(g.left, f); c < len(g.Columns); c++ {
		if used+g.widths[c]+3 > avail && len(out) > 0 {
			if avail-used > 4 {
				add(c)
			}
			break
		}
		if !add(c) {
			break
		}
	}
	return out
}

// frozenCols returns the frozen count that leaves room to scroll: columns
// that would fill the view do not stay frozen.
func (g *Grid) frozenCols() int {
	f := min(g.frozen, len(g.widths))
	for f > 0 && g.colsWidth(0, f-1) > (g.width-g.gutter())*2/3 {
		f--
	}
	return f
}

// ToggleFreeze freezes the columns up to the cursor column, or unfreezes.
// It reports whether columns are frozen now.
func (g *Grid) ToggleFreeze() bool {
	if g.frozen == g.col+1 {
		g.frozen = 0
	} else {
		g.frozen = g.col + 1
	}
	g.clamp()
	return g.frozen > 0
}

// GridZone is what a cell of the grid belongs to.
type GridZone int

const (
	GridNone GridZone = iota
	GridHeader
	GridColEdge // the separator after a column, in the header row
	GridGutter
	GridCell
)

// GridHit describes the grid element at a position.
type GridHit struct {
	Zone GridZone
	Row  int // in view coordinates
	Col  int
	X0   int // where the column's cells start
}

// Gutter returns the width of the row-number column.
func (g *Grid) Gutter() int { return g.gutter() }

// HitTest finds the element at a position relative to the grid.
func (g *Grid) HitTest(x, y int) GridHit {
	if x < 0 || y < 0 || x >= g.width || y >= g.height || y == 1 {
		return GridHit{}
	}
	spans := g.columnSpans()
	if y == 0 {
		for _, s := range spans {
			switch {
			case x == s.sep():
				return GridHit{Zone: GridColEdge, Col: s.col, X0: s.x0}
			case x >= s.x0 && x < s.sep():
				return GridHit{Zone: GridHeader, Col: s.col, X0: s.x0}
			}
		}
		return GridHit{}
	}
	vi := g.top + y - 2
	if vi >= len(g.view) {
		return GridHit{}
	}
	if x < g.gutter() {
		return GridHit{Zone: GridGutter, Row: vi}
	}
	for _, s := range spans {
		if x >= s.x0 && x <= s.sep() {
			return GridHit{Zone: GridCell, Row: vi, Col: s.col, X0: s.x0}
		}
	}
	return GridHit{}
}

// SetCursorView moves the cursor to a row in view coordinates.
func (g *Grid) SetCursorView(row int) {
	g.row = row
	g.clamp()
}

// SetCursorCol moves the cursor to a column.
func (g *Grid) SetCursorCol(col int) {
	g.col = col
	g.clamp()
}

// PressCell puts the cursor on a cell and starts a possible range there.
func (g *Grid) PressCell(row, col int) {
	g.ranged, g.rangeRows = false, false
	g.anchorRow, g.anchorCol = row, col
	g.row, g.col = row, col
	g.clamp()
}

// PressRow selects a whole row and starts a possible range of rows there.
func (g *Grid) PressRow(row int) {
	g.ranged, g.rangeRows = true, true
	g.anchorRow = row
	g.row = row
	g.clamp()
}

// DragTo extends the range to the cell under the pointer. Positions past an
// edge reach one row or column beyond the view, so holding there scrolls.
func (g *Grid) DragTo(x, y int) {
	row := g.top + y - 2
	if y >= g.height {
		row = g.top + g.bodyHeight()
	}
	row = clampInt(row, 0, len(g.view)-1)
	col := g.col
	if !g.rangeRows {
		col = g.colAt(x)
	}
	if !g.ranged && row == g.anchorRow && col == g.anchorCol {
		return
	}
	g.ranged = true
	g.row, g.col = row, col
	g.clamp()
}

func (g *Grid) colAt(x int) int {
	spans := g.columnSpans()
	if len(spans) == 0 {
		return g.col
	}
	first, last := spans[0], spans[len(spans)-1]
	if x < first.x0 {
		return max(first.col, g.left-1)
	}
	for _, s := range spans {
		if x >= s.x0 && x <= s.sep() {
			return s.col
		}
	}
	return min(last.col+1, len(g.Columns)-1)
}

// Range returns the selected rectangle in view coordinates.
func (g *Grid) Range() (r0, c0, r1, c1 int, ok bool) {
	if !g.ranged {
		return 0, 0, 0, 0, false
	}
	r0, r1 = min(g.anchorRow, g.row), max(g.anchorRow, g.row)
	if g.rangeRows {
		return r0, 0, r1, len(g.Columns) - 1, true
	}
	return r0, min(g.anchorCol, g.col), r1, max(g.anchorCol, g.col), true
}

func (g *Grid) inRange(vi, c int) bool {
	r0, c0, r1, c1, ok := g.Range()
	return ok && vi >= r0 && vi <= r1 && c >= c0 && c <= c1
}

// InRange reports whether the view cell is in the selected range.
func (g *Grid) InRange(row, col int) bool { return g.inRange(row, col) }

// RangeSelection returns the rows (indexes into Rows()) and columns of the
// selected range.
func (g *Grid) RangeSelection() (rows, cols []int, ok bool) {
	r0, c0, r1, c1, ok := g.Range()
	if !ok || r0 >= len(g.view) {
		return nil, nil, false
	}
	for vi := r0; vi <= r1 && vi < len(g.view); vi++ {
		rows = append(rows, g.view[vi])
	}
	for c := c0; c <= c1; c++ {
		cols = append(cols, c)
	}
	return rows, cols, true
}

// ClearRange drops the selected range and reports whether there was one.
func (g *Grid) ClearRange() bool {
	had := g.ranged
	g.ranged, g.rangeRows = false, false
	return had
}

// Ranged reports whether a range is selected.
func (g *Grid) Ranged() bool { return g.ranged }

// ScrollColumns moves the view by n columns, keeping the cursor on screen.
func (g *Grid) ScrollColumns(n int) {
	f := g.frozenCols()
	g.left = clampInt(g.left+n, f, max(f, len(g.Columns)-1))
	avail := g.width - g.gutter() - g.frozenWidth(f)
	if g.col >= f && g.col < g.left {
		g.col = g.left
	}
	for g.col > g.left && g.colsWidth(g.left, g.col) > avail {
		g.col--
	}
}

func (g *Grid) frozenWidth(f int) int {
	if f == 0 {
		return 0
	}
	return g.colsWidth(0, f-1)
}

// ResizeColumn sets a column's width, which then survives new rows.
func (g *Grid) ResizeColumn(c, w int) {
	if c < 0 || c >= len(g.widths) {
		return
	}
	w = clampInt(w, 1, max(1, g.width-g.gutter()-3))
	if g.userW == nil {
		g.userW = map[int]int{}
	}
	g.userW[c] = w
	g.widths[c] = w
}

// AutofitColumn sizes a column to its header and loaded values, past
// MaxColWidth, up to the width of the view.
func (g *Grid) AutofitColumn(c int) {
	if c < 0 || c >= len(g.widths) {
		return
	}
	w := runewidth.StringWidth(g.Columns[c]) + 1 // room for a sort mark
	step := max(1, len(g.rows)/20000)
	for ri := 0; ri < len(g.rows); ri += step {
		if r := g.rows[ri]; c < len(r) {
			cw := 4 // NULL
			if !r[c].Null {
				cw = runewidth.StringWidth(Sanitize(r[c].S))
			}
			w = max(w, cw)
		}
	}
	g.ResizeColumn(c, w)
}

// CopyLayout takes over the user's column sizes and frozen columns from an
// earlier grid of the same columns, across a refresh.
func (g *Grid) CopyLayout(from *Grid) {
	g.frozen = from.frozen
	for c, w := range from.userW {
		g.ResizeColumn(c, w)
	}
}
