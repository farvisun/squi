package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/farvisun/squi/internal/dataio"
	"github.com/farvisun/squi/internal/db"
	"github.com/farvisun/squi/internal/editsql"
	"github.com/farvisun/squi/internal/ui/components"
	"github.com/farvisun/squi/internal/ui/theme"
)

// resultPanel shows one result set and supports inline editing when the
// rows come from a single table with a primary/unique key.
type resultPanel struct {
	name    string
	sql     string
	cols    []db.ResultColumn
	grid    *components.Grid
	stream  *openStream
	done    bool // all rows fetched
	partial bool // stream was closed before all rows were fetched
	loading bool
	tag     string
	elapsed time.Duration

	// editing
	source   *db.ObjectRef
	detail   *db.TableDetail
	keyIdx   []int
	edits    map[int]map[int]db.Cell
	added    map[int]bool
	deleted  map[int]bool
	pendingD bool
}

func newResultPanel(name, sql string, cols []db.ResultColumn, rows [][]db.Cell, maxColW int) *resultPanel {
	r := &resultPanel{name: name, sql: sql, cols: cols, grid: components.NewGrid(),
		edits: map[int]map[int]db.Cell{}, added: map[int]bool{}, deleted: map[int]bool{}}
	r.grid.MaxColWidth = maxColW
	names := make([]string, len(cols))
	types := make([]string, len(cols))
	for i, c := range cols {
		names[i], types[i] = c.Name, c.Type
	}
	r.grid.SetData(names, types, rows)
	r.grid.CellValue = r.cellValue
	return r
}

func (r *resultPanel) title() string {
	t := r.name
	if r.dirty() {
		t += theme.Warn.Render(" ●")
	}
	return t
}

func (r *resultPanel) dirty() bool { return len(r.edits) > 0 || len(r.added) > 0 || len(r.deleted) > 0 }

func (r *resultPanel) rows() [][]db.Cell { return r.grid.Rows() }

func (r *resultPanel) cellValue(row, col int) (db.Cell, components.CellState) {
	if col < 0 {
		switch {
		case r.deleted[row]:
			return db.Cell{}, components.CellDeleted
		case r.added[row]:
			return db.Cell{}, components.CellAdded
		}
		return db.Cell{}, components.CellClean
	}
	if e, ok := r.edits[row][col]; ok {
		if r.added[row] {
			return e, components.CellAdded
		}
		return e, components.CellChanged
	}
	rows := r.rows()
	if row < len(rows) && col < len(rows[row]) {
		return rows[row][col], components.CellClean
	}
	return db.NullCell, components.CellClean
}

func (r *resultPanel) closeStream() {
	if r.stream != nil {
		r.stream.close()
		r.stream = nil
		if !r.done {
			r.partial = true
		}
	}
}

func (r *resultPanel) status() string {
	rows := len(r.rows())
	s := fmt.Sprintf("%d rows", rows)
	switch {
	case r.loading:
		s += " (loading…)"
	case !r.done && r.stream != nil:
		s += " (more…)"
	case r.partial:
		s += " (truncated)"
	}
	if r.grid.Filter() != "" {
		_, n := r.grid.CursorView()
		s = fmt.Sprintf("%d/%s · filter %q", n, s, r.grid.Filter())
	}
	if r.elapsed > 0 {
		s += " · " + fmtDuration(r.elapsed)
	}
	if r.source != nil {
		s += " · " + r.source.Name
		if r.detail != nil && len(r.keyIdx) > 0 {
			s += " (editable)"
		}
	}
	return s
}

func (r *resultPanel) scroll(n int) { r.grid.Scroll(n) }

func (r *resultPanel) view(w, h int, focused bool) string {
	if focused {
		r.grid.Focus()
	} else {
		r.grid.Blur()
	}
	r.grid.SetSize(w, h-1)
	row, total := r.grid.CursorView()
	_, col := r.grid.Cursor()
	pos := ""
	if total > 0 && col < len(r.cols) {
		pos = fmt.Sprintf("  row %d · %s %s", row+1, r.cols[col].Name, theme.Dim.Render(r.cols[col].Type))
	}
	footer := theme.Dim.Render(r.status()) + pos
	if r.dirty() {
		footer += "  " + theme.Warn.Render(fmt.Sprintf("%d pending change(s) — ctrl+s save · u revert row", r.changeCount()))
	}
	return r.grid.View() + "\n" + components.FitStyled(footer, w)
}

func (r *resultPanel) changeCount() int {
	return len(r.edits) + len(r.deleted) + len(r.added) - r.addedEdited()
}

func (r *resultPanel) addedEdited() int {
	n := 0
	for row := range r.added {
		if _, ok := r.edits[row]; ok {
			n++
		}
	}
	return n
}

func (r *resultPanel) update(m *Model, t *tab, km tea.KeyMsg) tea.Cmd {
	k := km.String()
	if r.pendingD {
		r.pendingD = false
		if k == "d" {
			return r.toggleDelete(m, t)
		}
	}
	if r.grid.Update(km) {
		return r.maybeFetch(m, t)
	}
	row, col := r.grid.Cursor()
	switch k {
	case "enter":
		if row >= 0 {
			v, _ := r.cellValue(row, col)
			return m.inspectCell(r.cols[col], v)
		}
	case "v":
		if row >= 0 {
			return m.showRecord(r, row)
		}
	case "y":
		if row >= 0 {
			v, _ := r.cellValue(row, col)
			if v.Null {
				return m.copy("NULL", "NULL")
			}
			return m.copy(v.S, "cell")
		}
	case "Y":
		if row >= 0 {
			return m.copy(dataio.FormatRows(dataio.FormatTSV, r.cols, [][]db.Cell{r.effectiveRow(row)}, m.exportOpts(r.source)), "row")
		}
	case "c":
		return m.copyMenu(r)
	case "/":
		m.push(newPrompt("Filter rows", "Show rows containing (case-insensitive):", r.grid.Filter(), false,
			func(v string) tea.Cmd { r.grid.SetFilter(strings.TrimSpace(v)); return nil }))
	case "esc":
		if r.grid.Filter() != "" {
			r.grid.SetFilter("")
		}
	case "s":
		r.grid.ToggleSort()
	case "x":
		return m.exportResult(r)
	case "r", "f5":
		return m.rerun(t, r)
	case "F":
		return m.fetchMore(t, r, 1<<30)
	case "e", "i":
		if row >= 0 {
			return r.editCell(m, t, row, col)
		}
	case "o":
		return r.addRow(m, t)
	case "d":
		r.pendingD = true
	case "u":
		if row >= 0 {
			r.revertRow(row)
		}
	case "U":
		r.revertAll()
	case "ctrl+s":
		return m.saveEdits(t, r)
	}
	return nil
}

func (r *resultPanel) maybeFetch(m *Model, t *tab) tea.Cmd {
	if r.grid.AtBottom() && !r.done && !r.loading && r.stream != nil {
		return m.fetchMore(t, r, m.settings.FetchSize)
	}
	return nil
}

func (r *resultPanel) effectiveRow(row int) []db.Cell {
	out := make([]db.Cell, len(r.cols))
	for c := range r.cols {
		out[c], _ = r.cellValue(row, c)
	}
	return out
}

// ensureEditable resolves the source table's key, loading its detail if
// needed; then calls next.
func (r *resultPanel) ensureEditable(m *Model, t *tab, next func() tea.Cmd) tea.Cmd {
	if t.ac.cfg.ReadOnly {
		return m.toast("Connection is read-only", toastWarn)
	}
	if r.source == nil {
		return m.toast("Result is not editable: it does not come from a single table", toastWarn)
	}
	if r.detail != nil {
		if len(r.keyIdx) == 0 {
			return m.toast("Not editable: no primary/unique key in the result columns", toastWarn)
		}
		return next()
	}
	ref := *r.source
	return m.loadDetail(t.ac, ref, func(d *db.TableDetail) tea.Cmd {
		r.detail = d
		r.keyIdx = nil
		for _, k := range d.PrimaryKey() {
			ix := r.colIndex(k)
			if ix < 0 {
				r.keyIdx = nil
				break
			}
			r.keyIdx = append(r.keyIdx, ix)
		}
		if len(r.keyIdx) == 0 {
			return m.toast("Not editable: no primary/unique key in the result columns", toastWarn)
		}
		return next()
	})
}

func (r *resultPanel) colIndex(name string) int {
	for i, c := range r.cols {
		if c.Name == name {
			return i
		}
	}
	for i, c := range r.cols {
		if strings.EqualFold(c.Name, name) {
			return i
		}
	}
	return -1
}

func (r *resultPanel) tableHasColumn(name string) bool {
	if r.detail == nil {
		return false
	}
	for _, c := range r.detail.Columns {
		if c.Name == name {
			return true
		}
	}
	return false
}

func (r *resultPanel) editCell(m *Model, t *tab, row, col int) tea.Cmd {
	return r.ensureEditable(m, t, func() tea.Cmd {
		if r.deleted[row] {
			return m.toast("Row is marked for deletion", toastWarn)
		}
		name := r.cols[col].Name
		if !r.tableHasColumn(name) {
			return m.toast(name+" is not a column of "+r.source.Name, toastWarn)
		}
		cur, _ := r.cellValue(row, col)
		val := cur.S
		set := func(c db.Cell) {
			orig := db.NullCell
			if row < len(r.rows()) && col < len(r.rows()[row]) {
				orig = r.rows()[row][col]
			}
			if r.edits[row] == nil {
				r.edits[row] = map[int]db.Cell{}
			}
			if c == orig && !r.added[row] {
				delete(r.edits[row], col)
				if len(r.edits[row]) == 0 {
					delete(r.edits, row)
				}
				return
			}
			r.edits[row][col] = c
		}
		pm := newPrompt("Edit "+name, fmt.Sprintf("%s (%s)", name, r.cols[col].Type), val, false, func(v string) tea.Cmd {
			set(db.Text(v))
			return nil
		})
		pm.extra = theme.Key.Render("ctrl+n") + " NULL"
		if cur.Null {
			pm.label += theme.Null.Render("  currently NULL")
		}
		pm.onKey = func(key string, p *promptModal) (bool, tea.Cmd) {
			if key == "ctrl+n" {
				set(db.NullCell)
				return true, nil
			}
			return false, nil
		}
		m.push(pm)
		return nil
	})
}

func (r *resultPanel) addRow(m *Model, t *tab) tea.Cmd {
	return r.ensureEditable(m, t, func() tea.Cmd {
		blank := make([]db.Cell, len(r.cols))
		for i := range blank {
			blank[i] = db.NullCell
		}
		r.grid.AppendRows([][]db.Cell{blank})
		ix := len(r.rows()) - 1
		r.added[ix] = true
		r.grid.SetCursorRow(ix)
		return m.toast("New row added — press e to set values, ctrl+s to save", toastInfo)
	})
}

func (r *resultPanel) toggleDelete(m *Model, t *tab) tea.Cmd {
	row, _ := r.grid.Cursor()
	if row < 0 {
		return nil
	}
	return r.ensureEditable(m, t, func() tea.Cmd {
		if r.added[row] {
			r.revertRow(row)
			return nil
		}
		if r.deleted[row] {
			delete(r.deleted, row)
		} else {
			r.deleted[row] = true
		}
		return nil
	})
}

func (r *resultPanel) revertRow(row int) {
	delete(r.edits, row)
	delete(r.deleted, row)
	if r.added[row] {
		r.removeRow(row)
	}
}

// removeRow drops an added (unsaved) row and shifts staged state above it.
func (r *resultPanel) removeRow(row int) {
	rows := r.rows()
	out := make([][]db.Cell, 0, len(rows)-1)
	out = append(append(out, rows[:row]...), rows[row+1:]...)
	edits, added, deleted := map[int]map[int]db.Cell{}, map[int]bool{}, map[int]bool{}
	shift := func(i int) int {
		if i > row {
			return i - 1
		}
		return i
	}
	for i, e := range r.edits {
		if i != row {
			edits[shift(i)] = e
		}
	}
	for i := range r.added {
		if i != row {
			added[shift(i)] = true
		}
	}
	for i := range r.deleted {
		if i != row {
			deleted[shift(i)] = true
		}
	}
	r.edits, r.added, r.deleted = edits, added, deleted
	r.grid.SetRows(out)
}

func (r *resultPanel) revertAll() {
	if len(r.added) > 0 {
		minRow := len(r.rows())
		for row := range r.added {
			minRow = min(minRow, row)
		}
		r.grid.SetRows(r.rows()[:minRow])
	}
	r.edits, r.added, r.deleted = map[int]map[int]db.Cell{}, map[int]bool{}, map[int]bool{}
}

// changes converts staged edits into editsql changes, in row order.
func (r *resultPanel) changes() []editsql.Change {
	var rowsIx []int
	seen := map[int]bool{}
	for row := range r.edits {
		rowsIx, seen[row] = append(rowsIx, row), true
	}
	for row := range r.deleted {
		if !seen[row] {
			rowsIx, seen[row] = append(rowsIx, row), true
		}
	}
	for row := range r.added {
		if !seen[row] {
			rowsIx = append(rowsIx, row)
		}
	}
	sort.Ints(rowsIx)
	var out []editsql.Change
	for _, row := range rowsIx {
		var ch editsql.Change
		if !r.added[row] {
			for _, ki := range r.keyIdx {
				ch.KeyCols = append(ch.KeyCols, r.cols[ki].Name)
				ch.KeyVals = append(ch.KeyVals, r.rows()[row][ki])
			}
		}
		if r.deleted[row] {
			ch.Kind = editsql.Delete
		} else {
			ch.Kind = editsql.Update
			if r.added[row] {
				ch.Kind = editsql.Insert
			}
			cols := make([]int, 0, len(r.edits[row]))
			for c := range r.edits[row] {
				cols = append(cols, c)
			}
			sort.Ints(cols)
			for _, c := range cols {
				ch.Cols = append(ch.Cols, r.cols[c].Name)
				ch.Vals = append(ch.Vals, r.edits[row][c])
			}
		}
		out = append(out, ch)
	}
	return out
}

// prettyValue formats JSON values for the cell inspector.
func prettyValue(v string) string {
	t := strings.TrimSpace(v)
	if (strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[")) && json.Valid([]byte(t)) {
		var buf bytes.Buffer
		if json.Indent(&buf, []byte(t), "", "  ") == nil {
			return buf.String()
		}
	}
	return v
}

func fmtDuration(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%dµs", d.Microseconds())
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.2fs", d.Seconds())
	}
	return d.Round(time.Second).String()
}
