package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/farvisun/squi/internal/db"
	"github.com/farvisun/squi/internal/ui/components"
)

// copyWhere copies a WHERE predicate built from the cursor: the cell, the
// row's key, or every loaded value of the column as an IN list.
func (m *Model) copyWhere(t *tab, r *resultPanel) tea.Cmd {
	row, col := r.grid.Cursor()
	if row < 0 || col >= len(r.cols) {
		return nil
	}
	d := t.ac.dialect()
	name := r.cols[col].Name
	v, _ := r.cellValue(row, col)
	var items []components.PickerItem
	add := func(title, pred string) {
		items = append(items, components.PickerItem{Title: title, Detail: abbrev(pred, 80), Value: pred})
	}
	add("Cell", d.Equals([]string{name}, []db.Cell{v}))
	if len(r.keyIdx) > 0 {
		cols := make([]string, len(r.keyIdx))
		vals := make([]db.Cell, len(r.keyIdx))
		for i, ki := range r.keyIdx {
			cols[i] = r.cols[ki].Name
			vals[i], _ = r.cellValue(row, ki)
		}
		add("Row key", d.Equals(cols, vals))
	}
	if pred, n := columnIn(d, name, r, col); n > 1 {
		add(fmt.Sprintf("Column values (%d distinct loaded)", n), pred)
	}
	if len(items) == 1 {
		return m.copy(items[0].Value.(string), "predicate")
	}
	m.push(newPickerModal("Copy WHERE predicate", items, func(it components.PickerItem) tea.Cmd {
		return m.copy(it.Value.(string), "predicate")
	}))
	return nil
}

// columnIn renders "col IN (…)" over the distinct loaded values of a column
// (plus "OR col IS NULL" when there are NULLs) and the number of values.
func columnIn(d db.Dialect, name string, r *resultPanel, col int) (string, int) {
	seen := map[string]bool{}
	var vals []string
	null := false
	for i := range r.rows() {
		if r.deleted[i] {
			continue
		}
		v, _ := r.cellValue(i, col)
		if v.Null {
			null = true
			continue
		}
		if !seen[v.S] {
			seen[v.S] = true
			vals = append(vals, d.Value(v))
		}
	}
	q := d.QuoteIdent(name)
	var pred string
	switch {
	case len(vals) > 0 && null:
		pred = fmt.Sprintf("(%s IN (%s) OR %s IS NULL)", q, strings.Join(vals, ", "), q)
	case len(vals) > 0:
		pred = fmt.Sprintf("%s IN (%s)", q, strings.Join(vals, ", "))
	case null:
		pred = q + " IS NULL"
	}
	n := len(vals)
	if null {
		n++
	}
	return pred, n
}

type countMsg struct {
	tab   *tab
	panel *resultPanel
	what  string // table (and filter) counted
	n     int64
	exact bool
	ok    bool
	job   bool // an exact COUNT(*) background job
	took  time.Duration
	err   error
}

// countRows shows the source table's row count: the server's cheap estimate
// first; pressing the key again within a few seconds runs an exact
// COUNT(*) (with the browse filter) on a separate session.
func (m *Model) countRows(t *tab, r *resultPanel) tea.Cmd {
	if r.source == nil {
		return m.toast("Count needs a result from a single table", toastWarn)
	}
	ref := *r.source
	where := ""
	if r.browse != nil {
		where = r.browse.Where
	}
	what := ref.Name
	if where != "" {
		what += " (filtered)"
	}
	if time.Now().Before(r.countArmed) || where != "" {
		r.countArmed = time.Time{}
		return m.exactCount(t, r, ref, where, what)
	}
	est, ok := t.ac.conn.(db.RowEstimator)
	if !ok {
		r.countArmed = time.Now().Add(5 * time.Second)
		return m.toast("Press "+m.keys.Display("results.count")+" again to run SELECT COUNT(*) on "+ref.Name, toastInfo)
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		n, exact, ok, err := est.EstimateRows(ctx, ref)
		return countMsg{tab: t, panel: r, what: what, n: n, exact: exact, ok: ok, err: err}
	}
}

// exactCount runs SELECT COUNT(*) as a background job under the tab's
// statement timeout.
func (m *Model) exactCount(t *tab, r *resultPanel, ref db.ObjectRef, where, what string) tea.Cmd {
	d := t.ac.dialect()
	sql := "SELECT COUNT(*) FROM " + d.Qualified(ref)
	if where != "" {
		sql += " WHERE (" + where + ")"
	}
	ac, dbName, timeout := t.ac, t.db, t.queryTimeout()
	if d.HasSchemas() && ref.Database != "" {
		dbName = ref.Database
	}
	m.jobs++
	m.toast("Counting "+what+"…", toastInfo)
	return tea.Batch(m.spinner.Tick, func() tea.Msg {
		ctx := context.Background()
		start := time.Now()
		s, err := ac.conn.NewSession(ctx, dbName)
		if err != nil {
			return countMsg{tab: t, panel: r, what: what, exact: true, job: true, err: err}
		}
		defer s.Close()
		wd := startWatchdog(s, timeout)
		rs, err := s.Run(ctx, sql)
		var rows [][]db.Cell
		if err == nil {
			rows, err = rs.Next(1)
			if err != nil && len(rows) > 0 {
				err = nil // io.EOF with the single row
			}
			_ = rs.Close()
		}
		if wd.stop() && err != nil {
			err = &timeoutError{after: timeout, err: err}
		}
		msg := countMsg{tab: t, panel: r, what: what, exact: true, job: true, took: time.Since(start), err: err, ok: true}
		if err == nil && len(rows) > 0 && len(rows[0]) > 0 {
			msg.n, msg.err = strconv.ParseInt(rows[0][0].S, 10, 64)
		}
		return msg
	})
}

func (m *Model) handleCount(msg countMsg) tea.Cmd {
	if msg.job {
		m.jobs--
	}
	if msg.err != nil {
		if m.hasTab(msg.tab) {
			msg.tab.out.msgs.log(logError, "Count "+msg.what+": "+msg.err.Error())
		}
		return m.toast("Count failed — see Messages", toastError)
	}
	again := m.keys.Display("results.count")
	switch {
	case !msg.ok:
		msg.panel.countArmed = time.Now().Add(5 * time.Second)
		return m.toast("No row estimate for "+msg.what+" — press "+again+" again for an exact COUNT(*)", toastInfo)
	case msg.job:
		text := fmt.Sprintf("%s: %s rows (exact, %s)", msg.what, groupDigits(msg.n), fmtDuration(msg.took))
		if m.hasTab(msg.tab) {
			msg.tab.out.msgs.log(logOK, text)
		}
		return m.toast(text, toastInfo)
	case msg.exact:
		return m.toast(fmt.Sprintf("%s: %s rows (from table statistics)", msg.what, groupDigits(msg.n)), toastInfo)
	}
	msg.panel.countArmed = time.Now().Add(5 * time.Second)
	return m.toast(fmt.Sprintf("%s: ≈ %s rows (estimate) — press %s again for an exact COUNT(*)", msg.what, groupDigits(msg.n), again), toastInfo)
}

// groupDigits formats 1234567 as "1,234,567".
func groupDigits(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}
