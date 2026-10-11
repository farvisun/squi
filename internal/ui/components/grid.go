package components

import (
	"sort"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/farvisun/squi/internal/db"
	"github.com/farvisun/squi/internal/ui/theme"
)

// CellState marks a cell or row as edited for rendering.
type CellState int

const (
	CellClean CellState = iota
	CellChanged
	CellAdded
	CellDeleted
)

// Grid is a virtualized table view over rows of cells.
type Grid struct {
	Columns []string
	Types   []string
	rows    [][]db.Cell
	view    []int // visible row indexes into rows (after filter/sort)

	widths []int
	row    int // cursor in view coordinates
	col    int
	top    int
	left   int // first visible column

	width, height int
	focused       bool
	MaxColWidth   int

	sortCol  int
	sortDesc bool
	filter   string

	// Overlay hooks used by inline editing.
	CellValue func(row, col int) (db.Cell, CellState)
	pendingG  bool
}

// NewGrid creates an empty grid.
func NewGrid() *Grid { return &Grid{MaxColWidth: 40, sortCol: -1} }

// SetData replaces columns and rows.
func (g *Grid) SetData(cols, types []string, rows [][]db.Cell) {
	g.Columns, g.Types, g.rows = cols, types, rows
	g.row, g.col, g.top, g.left = 0, 0, 0, 0
	g.sortCol, g.sortDesc, g.filter = -1, false, ""
	g.widths = make([]int, len(cols))
	for i, c := range cols {
		g.widths[i] = runewidth.StringWidth(c)
	}
	g.measure(rows)
	g.rebuild()
}

// SetRows replaces rows keeping columns, cursor and sort/filter.
func (g *Grid) SetRows(rows [][]db.Cell) {
	g.rows = rows
	g.measure(rows)
	g.rebuild()
}

// AppendRows adds rows (lazy loading).
func (g *Grid) AppendRows(rows [][]db.Cell) {
	g.rows = append(g.rows, rows...)
	g.measure(rows)
	g.rebuild()
}

// Rows returns all rows (unfiltered).
func (g *Grid) Rows() [][]db.Cell { return g.rows }

// measure widens columns to fit rows, sampling large batches evenly.
func (g *Grid) measure(rows [][]db.Cell) {
	step := 1
	if len(rows) > 20000 {
		step = len(rows) / 20000
	}
	for ri := 0; ri < len(rows); ri += step {
		r := rows[ri]
		for i := range g.widths {
			if i >= len(r) {
				break
			}
			w := 4 // NULL
			if !r[i].Null {
				w = runewidth.StringWidth(Sanitize(r[i].S))
			}
			if w > g.widths[i] {
				g.widths[i] = w
			}
		}
	}
	for i := range g.widths {
		if g.MaxColWidth > 0 && g.widths[i] > g.MaxColWidth {
			g.widths[i] = g.MaxColWidth
		}
		if g.widths[i] < 1 {
			g.widths[i] = 1
		}
	}
}

func (g *Grid) rebuild() {
	g.view = g.view[:0]
	f := strings.ToLower(g.filter)
	for i, r := range g.rows {
		if f == "" || rowMatches(r, f) {
			g.view = append(g.view, i)
		}
	}
	if g.sortCol >= 0 && g.sortCol < len(g.Columns) {
		c, desc := g.sortCol, g.sortDesc
		sort.SliceStable(g.view, func(a, b int) bool {
			less := cellLess(g.cell(g.view[a], c), g.cell(g.view[b], c))
			if desc {
				return cellLess(g.cell(g.view[b], c), g.cell(g.view[a], c))
			}
			return less
		})
	}
	g.clamp()
}

func (g *Grid) cell(r, c int) db.Cell {
	if g.CellValue != nil {
		v, _ := g.CellValue(r, c)
		return v
	}
	if r < len(g.rows) && c < len(g.rows[r]) {
		return g.rows[r][c]
	}
	return db.NullCell
}

func rowMatches(r []db.Cell, f string) bool {
	for _, c := range r {
		if !c.Null && strings.Contains(strings.ToLower(c.S), f) {
			return true
		}
	}
	return false
}

func cellLess(a, b db.Cell) bool {
	if a.Null != b.Null {
		return a.Null // NULLs first
	}
	fa, ea := strconv.ParseFloat(a.S, 64)
	fb, eb := strconv.ParseFloat(b.S, 64)
	if ea == nil && eb == nil {
		return fa < fb
	}
	return a.S < b.S
}

// SetFilter filters rows by substring (case-insensitive).
func (g *Grid) SetFilter(f string) { g.filter = f; g.rebuild() }

// Filter returns the active filter.
func (g *Grid) Filter() string { return g.filter }

// ToggleSort cycles sorting of the cursor column: asc, desc, none.
func (g *Grid) ToggleSort() {
	switch {
	case g.sortCol != g.col:
		g.sortCol, g.sortDesc = g.col, false
	case !g.sortDesc:
		g.sortDesc = true
	default:
		g.sortCol = -1
	}
	g.rebuild()
}

// SetSize sets the outer size.
func (g *Grid) SetSize(w, h int) { g.width, g.height = w, h; g.clamp() }

// Focus / Blur.
func (g *Grid) Focus()        { g.focused = true }
func (g *Grid) Blur()         { g.focused = false }
func (g *Grid) Focused() bool { return g.focused }

// Cursor returns the cursor row (index into Rows()) and column; row is -1
// when empty.
func (g *Grid) Cursor() (int, int) {
	if len(g.view) == 0 {
		return -1, g.col
	}
	return g.view[g.row], g.col
}

// CursorView returns the cursor position in view coordinates and the row count.
func (g *Grid) CursorView() (int, int) { return g.row, len(g.view) }

// SetCursorRow moves the cursor to the row with index r in Rows().
func (g *Grid) SetCursorRow(r int) {
	for i, v := range g.view {
		if v == r {
			g.row = i
			g.clamp()
			return
		}
	}
}

// AtBottom reports whether the cursor is near the last loaded row.
func (g *Grid) AtBottom() bool { return len(g.view) > 0 && g.row >= len(g.view)-3 }

func (g *Grid) bodyHeight() int { return max(0, g.height-2) }

func (g *Grid) clamp() {
	g.row = clampInt(g.row, 0, len(g.view)-1)
	g.col = clampInt(g.col, 0, len(g.Columns)-1)
	bh := g.bodyHeight()
	if g.row < g.top {
		g.top = g.row
	}
	if bh > 0 && g.row >= g.top+bh {
		g.top = g.row - bh + 1
	}
	if g.top < 0 {
		g.top = 0
	}
	if g.col < g.left {
		g.left = g.col
	}
	// ensure cursor column fully visible
	for g.left < g.col && g.colsWidth(g.left, g.col) > g.width-g.gutter() {
		g.left++
	}
}

func (g *Grid) gutter() int { return len(strconv.Itoa(len(g.rows))) + 1 }

func (g *Grid) colsWidth(from, to int) int {
	w := 0
	for i := from; i <= to && i < len(g.widths); i++ {
		w += g.widths[i] + 3
	}
	return w
}

// Update handles navigation keys and reports whether the key was used.
func (g *Grid) Update(msg tea.KeyMsg) bool {
	k := msg.String()
	if g.pendingG {
		g.pendingG = false
		if k == "g" {
			g.row = 0
			g.clamp()
			return true
		}
	}
	bh := max(1, g.bodyHeight())
	switch k {
	case "j", "down":
		g.row++
	case "k", "up":
		g.row--
	case "h", "left":
		g.col--
	case "l", "right":
		g.col++
	case "0", "home":
		g.col = 0
	case "$", "end":
		g.col = len(g.Columns) - 1
	case "w":
		g.col = min(g.col+1, len(g.Columns)-1)
	case "b":
		g.col = max(g.col-1, 0)
	case "g":
		g.pendingG = true
	case "G", "ctrl+end":
		g.row = len(g.view) - 1
	case "ctrl+home":
		g.row = 0
	case "ctrl+d":
		g.row += bh / 2
	case "ctrl+u":
		g.row -= bh / 2
	case "pgdown", "ctrl+f":
		g.row += bh
	case "pgup", "ctrl+b":
		g.row -= bh
	case "H":
		g.row = g.top
	case "L":
		g.row = g.top + bh - 1
	default:
		return false
	}
	g.clamp()
	return true
}

// Scroll moves the cursor by n rows (mouse wheel).
func (g *Grid) Scroll(n int) { g.row += n; g.clamp() }

// View renders the grid.
func (g *Grid) View() string {
	if len(g.Columns) == 0 {
		return PadLines(theme.Dim.Render("(no columns)"), g.width, g.height)
	}
	gw := g.gutter()
	avail := g.width - gw
	var visCols []int
	used := 0
	for c := g.left; c < len(g.Columns); c++ {
		w := g.widths[c] + 3
		if used+w > avail && len(visCols) > 0 {
			// include partially visible last column
			if avail-used > 4 {
				visCols = append(visCols, c)
			}
			break
		}
		visCols = append(visCols, c)
		used += w
	}
	sep := theme.Dim.Render("│")
	var lines []string

	// header
	var hb strings.Builder
	hb.WriteString(strings.Repeat(" ", gw))
	usedW := gw
	for _, c := range visCols {
		w := g.widths[c]
		name := g.Columns[c]
		if c == g.sortCol {
			if g.sortDesc {
				name += "▼"
			} else {
				name += "▲"
			}
		}
		cellW := min(w, g.width-usedW-3)
		if cellW < 1 {
			break
		}
		st := theme.Header
		if c == g.col && g.focused {
			st = st.Underline(true)
		}
		hb.WriteString(" " + st.Render(Fit(name, cellW)) + " " + sep)
		usedW += cellW + 3
	}
	lines = append(lines, hb.String())
	var sb strings.Builder
	sb.WriteString(strings.Repeat("─", gw))
	for _, c := range visCols {
		sb.WriteString(strings.Repeat("─", g.widths[c]+2) + "┼")
	}
	lines = append(lines, theme.Dim.Render(Fit(sb.String(), g.width)))

	bh := g.bodyHeight()
	numStyle := theme.Dim
	for i := 0; i < bh; i++ {
		vi := g.top + i
		if vi >= len(g.view) {
			break
		}
		ri := g.view[vi]
		var rb strings.Builder
		rb.WriteString(numStyle.Render(Fit(strconv.Itoa(ri+1), gw-1)) + " ")
		usedW := gw
		rowState := CellClean
		if g.CellValue != nil && len(g.Columns) > 0 {
			_, rowState = g.CellValue(ri, -1)
		}
		for _, c := range visCols {
			cellW := min(g.widths[c], g.width-usedW-3)
			if cellW < 1 {
				break
			}
			var v db.Cell
			state := rowState
			if g.CellValue != nil {
				var cs CellState
				v, cs = g.CellValue(ri, c)
				if cs != CellClean && state == CellClean {
					state = cs
				}
			} else if c < len(g.rows[ri]) {
				v = g.rows[ri][c]
			}
			var txt string
			st := lipgloss.NewStyle()
			if v.Null {
				txt = Fit("NULL", cellW)
				st = theme.Null
			} else {
				s := Sanitize(v.S)
				if isNumericType(g.typeOf(c)) {
					txt = padLeft(s, cellW)
				} else {
					txt = Fit(s, cellW)
				}
			}
			switch state {
			case CellChanged:
				st = st.Background(theme.ChangedBg)
			case CellAdded:
				st = st.Background(theme.AddedBg)
			case CellDeleted:
				st = st.Background(theme.DeletedBg).Strikethrough(true)
			}
			if vi == g.row {
				if c == g.col && g.focused {
					st = theme.Cursor
				} else {
					st = st.Background(theme.SelBg)
				}
			}
			rb.WriteString(st.Render(" "+txt+" ") + sep)
			usedW += cellW + 3
		}
		lines = append(lines, rb.String())
	}
	if len(g.view) == 0 {
		msg := "(no rows)"
		if g.filter != "" {
			msg = "(no rows match filter)"
		}
		lines = append(lines, theme.Dim.Render(strings.Repeat(" ", gw)+msg))
	}
	return PadLines(strings.Join(lines, "\n"), g.width, g.height)
}

func (g *Grid) typeOf(c int) string {
	if c < len(g.Types) {
		return g.Types[c]
	}
	return ""
}

func isNumericType(t string) bool {
	t = strings.ToLower(t)
	for _, p := range []string{"int", "numeric", "decimal", "float", "double", "real", "serial", "money"} {
		if strings.Contains(t, p) {
			return true
		}
	}
	return false
}

func padLeft(s string, w int) string {
	sw := runewidth.StringWidth(s)
	if sw >= w {
		return runewidth.Truncate(s, w, "…")
	}
	return strings.Repeat(" ", w-sw) + s
}
