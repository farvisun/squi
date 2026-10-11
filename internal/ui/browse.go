package ui

import (
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/farvisun/squi/internal/config"
	"github.com/farvisun/squi/internal/db"
	"github.com/farvisun/squi/internal/sqltext"
	"github.com/farvisun/squi/internal/ui/components"
	"github.com/farvisun/squi/internal/ui/theme"
)

// A result panel opened on a table ("browsing") filters and sorts on the
// server: / sets a WHERE condition and s an ORDER BY, so the answer covers
// the whole table rather than the rows loaded so far. Foreign keys can be
// followed from a cell (>) or back to the rows referencing it (<); each step
// replaces the panel and backspace returns to the previous one.

// browseFrame is a panel state that navigation can return to.
type browseFrame struct {
	sql      string
	name     string
	source   *db.ObjectRef
	browse   *db.Browse // nil for an ad-hoc query
	row, col int
}

// navState is applied to the panel produced by a navigation run. Without
// one, a re-run keeps the replaced panel's state.
type navState struct {
	browse  *db.Browse
	back    []browseFrame
	row     int
	col     int
	restore bool // move the cursor to row/col
}

// frameOf captures the current state of r.
func frameOf(r *resultPanel) browseFrame {
	row, col := r.grid.Cursor()
	return browseFrame{sql: r.sql, name: r.name, source: r.source, browse: r.browse, row: row, col: col}
}

// browseFrameFor is the state showing b.
func browseFrameFor(d db.Dialect, b db.Browse) browseFrame {
	ref := b.Ref
	return browseFrame{sql: d.Select(b, 0), name: b.Ref.Name, source: &ref, browse: &b}
}

// showFrame runs f in place of r. back becomes the panel's history.
func (m *Model) showFrame(t *tab, r *resultPanel, f browseFrame, back []browseFrame, restoreCursor bool) tea.Cmd {
	if r.dirty() {
		return m.toast("Save (ctrl+s) or revert (U) pending edits first", toastWarn)
	}
	opts := runOpts{replace: r, name: f.name, source: f.source,
		nav: &navState{browse: f.browse, back: back, row: f.row, col: f.col, restore: restoreCursor}}
	if f.browse != nil && t.ac.dialect().HasSchemas() && f.browse.Ref.Database != "" && f.browse.Ref.Database != t.db {
		opts.useDB = f.browse.Ref.Database
	}
	return m.execute(t, []sqltext.Statement{{SQL: f.sql}}, opts)
}

// applyNav sets the navigation state of a panel produced by a run.
func (r *resultPanel) applyNav(n *navState) {
	r.browse, r.back = n.browse, n.back
	if n.restore {
		r.grid.SetCursor(n.row, n.col)
	}
	r.grid.SetSortMark(-1, false)
	if r.browse != nil && len(r.browse.Order) > 0 {
		if ix := r.colIndex(r.browse.Order[0].Column); ix >= 0 {
			r.grid.SetSortMark(ix, r.browse.Order[0].Desc)
		}
	}
}

// browseFilter asks for the WHERE condition of a browsed table.
func (m *Model) browseFilter(t *tab, r *resultPanel) tea.Cmd {
	b := *r.browse
	pm := newPrompt("Filter "+b.Ref.Name+" (server-side)", "WHERE", b.Where, false, func(v string) tea.Cmd {
		cond, err := sqltext.CleanCondition(v, t.ac.driver())
		if err != nil {
			return m.toast(err.Error(), toastWarn)
		}
		if cond == b.Where {
			return nil
		}
		b.Where = cond
		return m.showFrame(t, r, browseFrameFor(t.ac.dialect(), b), r.back, false)
	})
	pm.extra = "e.g. status = 'paid' AND created_at > now() - interval '1 day' · empty = all rows · f filters loaded rows only"
	m.push(pm)
	return nil
}

// clearBrowseFilter drops the WHERE condition of a browsed table.
func (m *Model) clearBrowseFilter(t *tab, r *resultPanel) tea.Cmd {
	b := *r.browse
	b.Where = ""
	return m.showFrame(t, r, browseFrameFor(t.ac.dialect(), b), r.back, false)
}

// browseSort cycles the server-side ORDER BY on the cursor column:
// ascending, descending, unsorted.
func (m *Model) browseSort(t *tab, r *resultPanel) tea.Cmd {
	_, c := r.grid.Cursor()
	if c < 0 || c >= len(r.cols) {
		return nil
	}
	col := r.cols[c].Name
	b := *r.browse
	switch {
	case len(b.Order) > 0 && b.Order[0].Column == col && !b.Order[0].Desc:
		b.Order = []db.Order{{Column: col, Desc: true}}
	case len(b.Order) > 0 && b.Order[0].Column == col:
		b.Order = nil
	default:
		b.Order = []db.Order{{Column: col}}
	}
	f := browseFrameFor(t.ac.dialect(), b)
	f.col = c
	return m.showFrame(t, r, f, r.back, true)
}

// browseBack returns to the previous panel state.
func (m *Model) browseBack(t *tab, r *resultPanel) tea.Cmd {
	if len(r.back) == 0 {
		return m.toast("Nothing to go back to", toastInfo)
	}
	f := r.back[len(r.back)-1]
	return m.showFrame(t, r, f, r.back[:len(r.back)-1:len(r.back)-1], true)
}

// withDetail calls fn with the source table's detail, loading it once.
func (r *resultPanel) withDetail(m *Model, t *tab, fn func(*db.TableDetail) tea.Cmd) tea.Cmd {
	if r.source == nil {
		return m.toast("Result does not come from a single table", toastWarn)
	}
	if r.detail != nil {
		return fn(r.detail)
	}
	return m.loadDetail(t.ac, *r.source, func(d *db.TableDetail) tea.Cmd {
		r.setDetail(d)
		return fn(d)
	})
}

// fkTarget resolves the table a foreign key points at, relative to src.
func fkTarget(d db.Dialect, src db.ObjectRef, schema, table string) db.ObjectRef {
	ref := db.ObjectRef{Database: src.Database, Schema: schema, Name: table, Kind: db.KindTable}
	if !d.HasSchemas() {
		if schema != "" {
			ref.Database = schema
		}
		ref.Schema = ref.Database
	} else if ref.Schema == "" {
		ref.Schema = src.Schema
	}
	return ref
}

// rowValues returns the cursor row's values of cols, or the first column
// missing from the result.
func (r *resultPanel) rowValues(row int, cols []string) ([]db.Cell, string) {
	vals := make([]db.Cell, len(cols))
	for i, c := range cols {
		ix := r.colIndex(c)
		if ix < 0 {
			return nil, c
		}
		vals[i], _ = r.cellValue(row, ix)
	}
	return vals, ""
}

func allNull(vals []db.Cell) bool {
	for _, v := range vals {
		if !v.Null {
			return false
		}
	}
	return true
}

// followFK opens the row the cursor cell's foreign key points at.
func (m *Model) followFK(t *tab, r *resultPanel) tea.Cmd {
	row, col := r.grid.Cursor()
	if row < 0 || col >= len(r.cols) {
		return nil
	}
	name := r.cols[col].Name
	return r.withDetail(m, t, func(d *db.TableDetail) tea.Cmd {
		var cands []db.ForeignKey
		for _, fk := range d.ForeignKeys {
			if slices.Contains(fk.Columns, name) {
				cands = append(cands, fk)
			}
		}
		if len(cands) == 0 {
			if len(d.ForeignKeys) == 0 {
				return m.toast(r.source.Name+" has no foreign keys", toastInfo)
			}
			return m.toast("No foreign key on "+name+" — move to a key column", toastInfo)
		}
		follow := func(fk db.ForeignKey) tea.Cmd {
			vals, missing := r.rowValues(row, fk.Columns)
			if missing != "" {
				return m.toast(missing+" is not in the result", toastWarn)
			}
			if allNull(vals) {
				return m.toast(strings.Join(fk.Columns, ", ")+" is NULL — no referenced row", toastInfo)
			}
			dia := t.ac.dialect()
			b := db.Browse{Ref: fkTarget(dia, *r.source, fk.RefSchema, fk.RefTable), Where: dia.Equals(fk.RefColumns, vals)}
			return m.showFrame(t, r, browseFrameFor(dia, b), pushFrame(r), false)
		}
		if len(cands) == 1 {
			return follow(cands[0])
		}
		items := make([]components.PickerItem, len(cands))
		for i, fk := range cands {
			items[i] = components.PickerItem{Title: fkLabel(fk.Columns, fk.RefTable, fk.RefColumns), Detail: fk.Name, Value: fk}
		}
		m.push(newPickerModal("Follow foreign key", items, func(it components.PickerItem) tea.Cmd {
			return follow(it.Value.(db.ForeignKey))
		}))
		return nil
	})
}

// referencingRows lists the tables whose foreign keys point at the cursor
// row and opens the referencing rows of the chosen one.
func (m *Model) referencingRows(t *tab, r *resultPanel) tea.Cmd {
	row, _ := r.grid.Cursor()
	if row < 0 {
		return nil
	}
	return r.withDetail(m, t, func(d *db.TableDetail) tea.Cmd {
		var items []components.PickerItem
		for _, fk := range d.ReferencedBy {
			if _, missing := r.rowValues(row, fk.RefColumns); missing != "" {
				continue
			}
			items = append(items, components.PickerItem{Title: fkLabel([]string{fk.Table + "." + strings.Join(fk.Columns, ", ")}, d.Ref.Name, fk.RefColumns),
				Detail: fk.Name, Value: fk})
		}
		if len(items) == 0 {
			return m.toast("No foreign keys reference "+r.source.Name, toastInfo)
		}
		open := func(fk db.ForeignKey) tea.Cmd {
			vals, _ := r.rowValues(row, fk.RefColumns)
			dia := t.ac.dialect()
			b := db.Browse{Ref: fkTarget(dia, *r.source, fk.Schema, fk.Table), Where: dia.Equals(fk.Columns, vals)}
			return m.showFrame(t, r, browseFrameFor(dia, b), pushFrame(r), false)
		}
		m.push(newPickerModal("Rows referencing this "+r.source.Name, items, func(it components.PickerItem) tea.Cmd {
			return open(it.Value.(db.ForeignKey))
		}))
		return nil
	})
}

// pushFrame returns r's history plus its current state.
func pushFrame(r *resultPanel) []browseFrame {
	back := make([]browseFrame, 0, len(r.back)+1)
	return append(append(back, r.back...), frameOf(r))
}

func fkLabel(cols []string, refTable string, refCols []string) string {
	return fmt.Sprintf("%s → %s(%s)", strings.Join(cols, ", "), refTable, strings.Join(refCols, ", "))
}

// browseStatus describes the server-side filter, order and history of r.
func (r *resultPanel) browseStatus() string {
	var parts []string
	if r.browse != nil {
		if r.browse.Where != "" {
			parts = append(parts, theme.Warn.Render("WHERE "+abbrev(r.browse.Where, 60)))
		}
		if len(r.browse.Order) > 0 {
			o := r.browse.Order[0]
			parts = append(parts, "ORDER BY "+o.Column+yesNo(o.Desc, " DESC", ""))
		}
	}
	if n := len(r.back); n > 0 {
		parts = append(parts, fmt.Sprintf("⌫ back (%d)", n))
	}
	return strings.Join(parts, " · ")
}

// toggleFavorite pins or unpins an object (or a bookmarked view) of ac.
func (m *Model) toggleFavorite(ac *activeConn, f config.Favorite) tea.Cmd {
	if m.favorites == nil {
		m.favorites = config.Favorites{}
	}
	added := m.favorites.Toggle(ac.cfg.ID, f)
	if ac.explorer != nil {
		ac.explorer.setFavorites(m.favorites[ac.cfg.ID])
	}
	what := f.Name
	if f.IsView() {
		what += " ⟨" + favViewLabel(f) + "⟩"
	}
	if ac.cfg.Transient {
		return m.toast(yesNo(added, "Pinned ", "Unpinned ")+what+" for this session (save the connection to keep favorites)", toastInfo)
	}
	if err := m.store.SaveFavorites(m.favorites); err != nil {
		return m.toast("Save favorites: "+err.Error(), toastError)
	}
	return m.toast(yesNo(added, "Pinned ", "Unpinned ")+what+yesNo(added, " to ★ Favorites", ""), toastInfo)
}

// bookmarkView pins the current filter and sort of a browsed table.
func (m *Model) bookmarkView(t *tab, r *resultPanel) tea.Cmd {
	if r.browse == nil {
		return m.toast("Open a table from the explorer to bookmark its view", toastInfo)
	}
	b := r.browse
	f := config.Favorite{Database: b.Ref.Database, Schema: b.Ref.Schema, Name: b.Ref.Name, Kind: string(b.Ref.Kind), Where: b.Where}
	if f.Kind == "" {
		f.Kind = string(db.KindTable)
	}
	if len(b.Order) > 0 {
		f.OrderBy, f.Desc = b.Order[0].Column, b.Order[0].Desc
	}
	return m.toggleFavorite(t.ac, f)
}
