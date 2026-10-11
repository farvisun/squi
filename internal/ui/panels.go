package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/farvisun/squi/internal/db"
	"github.com/farvisun/squi/internal/ui/components"
	"github.com/farvisun/squi/internal/ui/theme"
)

// subTabs renders a row of inner tab labels, abbreviating unselected
// labels to their number when they do not fit in w.
func subTabs(names []string, ix, w int) string {
	full := 0
	for i, n := range names {
		full += len(fmt.Sprintf("%d %s", i+1, n)) + 2
	}
	parts := make([]string, len(names))
	for i, n := range names {
		label := fmt.Sprintf("%d %s", i+1, n)
		if full > w && i != ix {
			word := strings.SplitN(n, " ", 2)[0]
			label = fmt.Sprintf("%d %s", i+1, word[:min(3, len(word))])
		}
		if i == ix {
			parts[i] = lipgloss.NewStyle().Foreground(theme.Accent).Bold(true).Underline(true).Render(label)
		} else {
			parts[i] = theme.Dim.Render(label)
		}
	}
	return strings.Join(parts, "  ")
}

// subTabKey handles 1-9 and </> for inner tab switching.
func subTabKey(k string, ix, n int) (int, bool) {
	switch k {
	case ">", "L":
		return (ix + 1) % n, true
	case "<", "H":
		return (ix - 1 + n) % n, true
	}
	if len(k) == 1 && k[0] >= '1' && k[0] <= '9' {
		if i := int(k[0] - '1'); i < n {
			return i, true
		}
	}
	return ix, false
}

func tableGrid(cols []string, rows [][]string, maxW int) *components.Grid {
	g := components.NewGrid()
	g.MaxColWidth = maxW
	cells := make([][]db.Cell, len(rows))
	for i, r := range rows {
		cells[i] = make([]db.Cell, len(r))
		for j, v := range r {
			cells[i][j] = db.Text(v)
		}
	}
	g.SetData(cols, nil, cells)
	return g
}

func dbTableGrid(t *db.Table, maxW int) *components.Grid {
	g := components.NewGrid()
	g.MaxColWidth = maxW
	g.SetData(t.Columns, nil, t.Rows)
	return g
}

// ---- table detail ----

type detailPanel struct {
	ac     *activeConn
	ref    db.ObjectRef
	d      *db.TableDetail
	names  []string
	grids  []*components.Grid
	ddl    *components.TextView
	ddlErr string
	ix     int
}

func newDetailPanel(ac *activeConn, d *db.TableDetail, maxW int) *detailPanel {
	p := &detailPanel{ac: ac, ref: d.Ref, d: d}
	add := func(name string, g *components.Grid) {
		p.names = append(p.names, name)
		p.grids = append(p.grids, g)
	}
	var rows [][]string
	for _, c := range d.Columns {
		def := ""
		if c.Default != nil {
			def = *c.Default
		}
		rows = append(rows, []string{fmt.Sprint(c.Position), c.Name, c.Type, yesNo(c.Nullable, "NULL", "NOT NULL"),
			def, yesNo(c.IsPK, "PK", ""), c.Extra, c.Comment})
	}
	add(fmt.Sprintf("Columns (%d)", len(d.Columns)), tableGrid([]string{"#", "name", "type", "null", "default", "key", "extra", "comment"}, rows, maxW))

	rows = nil
	for _, ix := range d.Indexes {
		rows = append(rows, []string{ix.Name, strings.Join(ix.Columns, ", "), yesNo(ix.Primary, "PRIMARY", yesNo(ix.Unique, "UNIQUE", "")),
			ix.Method, ix.Size, ix.Definition})
	}
	add(fmt.Sprintf("Indexes (%d)", len(d.Indexes)), tableGrid([]string{"name", "columns", "kind", "method", "size", "definition"}, rows, maxW*2))

	rows = nil
	for _, fk := range d.ForeignKeys {
		rows = append(rows, []string{fk.Name, strings.Join(fk.Columns, ", "),
			qualify(fk.RefSchema, fk.RefTable) + "(" + strings.Join(fk.RefColumns, ", ") + ")", fk.OnUpdate, fk.OnDelete})
	}
	add(fmt.Sprintf("Foreign keys (%d)", len(d.ForeignKeys)), tableGrid([]string{"name", "columns", "references", "on update", "on delete"}, rows, maxW))

	rows = nil
	for _, fk := range d.ReferencedBy {
		rows = append(rows, []string{fk.Name, qualify(fk.Schema, fk.Table) + "(" + strings.Join(fk.Columns, ", ") + ")",
			strings.Join(fk.RefColumns, ", "), fk.OnUpdate, fk.OnDelete})
	}
	add(fmt.Sprintf("Referenced by (%d)", len(d.ReferencedBy)), tableGrid([]string{"name", "from", "to columns", "on update", "on delete"}, rows, maxW))

	rows = nil
	for _, c := range d.Constraints {
		rows = append(rows, []string{c.Name, c.Type, c.Definition})
	}
	add(fmt.Sprintf("Constraints (%d)", len(d.Constraints)), tableGrid([]string{"name", "type", "definition"}, rows, maxW*2))

	rows = nil
	for _, tr := range d.Triggers {
		rows = append(rows, []string{tr.Name, tr.Timing, tr.Event, tr.Definition})
	}
	add(fmt.Sprintf("Triggers (%d)", len(d.Triggers)), tableGrid([]string{"name", "timing", "event", "definition"}, rows, maxW*2))

	rows = nil
	if d.Comment != "" {
		rows = append(rows, []string{"Comment", d.Comment})
	}
	for _, kv := range d.Stats {
		rows = append(rows, []string{kv.Key, kv.Value})
	}
	add("Stats", tableGrid([]string{"property", "value"}, rows, maxW*2))
	p.names = append(p.names, "DDL")
	return p
}

func yesNo(b bool, y, n string) string {
	if b {
		return y
	}
	return n
}

func qualify(schema, name string) string {
	if schema == "" {
		return name
	}
	return schema + "." + name
}

func (p *detailPanel) title() string { return "ⓘ " + p.ref.Name }

func (p *detailPanel) isDDL() bool { return p.ix == len(p.names)-1 }

func (p *detailPanel) update(m *Model, t *tab, km tea.KeyMsg) tea.Cmd {
	k := km.String()
	if ix, ok := subTabKey(k, p.ix, len(p.names)); ok {
		p.ix = ix
		if p.isDDL() && p.ddl == nil && p.ddlErr == "" {
			return p.loadDDL()
		}
		return nil
	}
	if p.isDDL() {
		if p.ddl == nil {
			return nil
		}
		if k == "y" {
			return m.copy(p.ddl.Text(), "DDL")
		}
		if k == "e" {
			return m.appendToEditor(p.ddl.Text())
		}
		p.ddl.Update(km)
		return nil
	}
	g := p.grids[p.ix]
	if g.Update(km) {
		return nil
	}
	switch k {
	case "y":
		row, col := g.Cursor()
		if row >= 0 {
			return m.copy(g.Rows()[row][col].S, "value")
		}
	case "enter":
		row, col := g.Cursor()
		if row >= 0 {
			return m.inspectCell(db.ResultColumn{Name: g.Columns[col]}, g.Rows()[row][col])
		}
	case "o":
		return m.openTableData(p.ref)
	case "r":
		return m.showDetail(p.ref)
	}
	return nil
}

type ddlMsg struct {
	panel *detailPanel
	text  *components.TextView
	ddl   string
	err   error
}

func (p *detailPanel) loadDDL() tea.Cmd {
	ac, ref := p.ac, p.ref
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		ddl, err := ac.conn.DDL(ctx, ref)
		return ddlMsg{panel: p, ddl: ddl, err: err}
	}
}

func (p *detailPanel) scroll(n int) {
	if p.isDDL() {
		if p.ddl != nil {
			p.ddl.Scroll(n)
		}
		return
	}
	p.grids[p.ix].Scroll(n)
}

func (p *detailPanel) view(w, h int, focused bool) string {
	head := components.FitStyled(subTabs(p.names, p.ix, w), w)
	if p.isDDL() {
		switch {
		case p.ddlErr != "":
			return head + "\n" + theme.Error.Render(p.ddlErr)
		case p.ddl == nil:
			return head + "\n" + theme.Dim.Render("loading…")
		}
		p.ddl.SetSize(w, h-1)
		return head + "\n" + p.ddl.View()
	}
	g := p.grids[p.ix]
	if focused {
		g.Focus()
	} else {
		g.Blur()
	}
	g.SetSize(w, h-1)
	return head + "\n" + g.View()
}

// ---- plain text (DDL) ----

type textPanel struct {
	name string
	tv   *components.TextView
}

func (p *textPanel) title() string { return p.name }

func (p *textPanel) update(m *Model, t *tab, km tea.KeyMsg) tea.Cmd {
	switch km.String() {
	case "y":
		return m.copy(p.tv.Text(), "text")
	case "e":
		return m.appendToEditor(p.tv.Text())
	}
	p.tv.Update(km)
	return nil
}

func (p *textPanel) scroll(n int) { p.tv.Scroll(n) }

func (p *textPanel) view(w, h int, focused bool) string {
	p.tv.SetSize(w, h)
	return p.tv.View()
}

// ---- explain ----

type planRow struct {
	n     *db.PlanNode
	depth int
}

type explainPanel struct {
	sql       string
	root      *db.PlanNode
	analyze   bool
	collapsed map[*db.PlanNode]bool
	flat      []planRow
	ix, top   int
	raw       bool
	rawView   *components.TextView
	maxCost   float64
	maxTime   float64
}

func newExplainPanel(sql string, root *db.PlanNode, analyze bool) *explainPanel {
	p := &explainPanel{sql: sql, root: root, analyze: analyze, collapsed: map[*db.PlanNode]bool{}}
	p.rawView = components.NewTextView(root.Raw, "")
	var walk func(n *db.PlanNode)
	walk = func(n *db.PlanNode) {
		p.maxCost = max(p.maxCost, n.Cost)
		p.maxTime = max(p.maxTime, n.Time)
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(root)
	p.rebuild()
	return p
}

func (p *explainPanel) rebuild() {
	p.flat = p.flat[:0]
	var walk func(n *db.PlanNode, d int)
	walk = func(n *db.PlanNode, d int) {
		p.flat = append(p.flat, planRow{n, d})
		if !p.collapsed[n] {
			for _, c := range n.Children {
				walk(c, d+1)
			}
		}
	}
	walk(p.root, 0)
	p.ix = clampIx(p.ix, len(p.flat))
}

func (p *explainPanel) title() string {
	if p.analyze {
		return "Plan (analyze)"
	}
	return "Plan"
}

func (p *explainPanel) update(m *Model, t *tab, km tea.KeyMsg) tea.Cmd {
	k := km.String()
	if p.raw {
		switch k {
		case "r":
			p.raw = false
		case "y":
			return m.copy(p.root.Raw, "plan")
		default:
			p.rawView.Update(km)
		}
		return nil
	}
	switch k {
	case "j", "down":
		p.ix = clampIx(p.ix+1, len(p.flat))
	case "k", "up":
		p.ix = clampIx(p.ix-1, len(p.flat))
	case "G":
		p.ix = len(p.flat) - 1
	case "g":
		p.ix = 0
	case "enter", " ", "l", "h", "left", "right":
		n := p.flat[p.ix].n
		if len(n.Children) > 0 {
			p.collapsed[n] = !p.collapsed[n]
			p.rebuild()
		}
	case "r":
		p.raw = true
	case "y":
		return m.copy(p.root.Raw, "plan")
	}
	return nil
}

func (p *explainPanel) scroll(n int) { p.ix = clampIx(p.ix+n, len(p.flat)) }

func (p *explainPanel) heat(n *db.PlanNode) lipgloss.Style {
	share := 0.0
	switch {
	case p.analyze && p.maxTime > 0:
		share = selfTime(n) / p.maxTime
	case p.maxCost > 0:
		share = selfCost(n) / p.maxCost
	}
	switch {
	case share >= 0.5:
		return theme.Error.Bold(true)
	case share >= 0.2:
		return theme.Warn
	}
	return theme.Base
}

func selfCost(n *db.PlanNode) float64 {
	c := n.Cost
	for _, ch := range n.Children {
		c -= ch.Cost
	}
	return max(c, 0)
}

func selfTime(n *db.PlanNode) float64 {
	c := n.Time
	for _, ch := range n.Children {
		c -= ch.Time
	}
	return max(c, 0)
}

func (p *explainPanel) view(w, h int, focused bool) string {
	if p.raw {
		p.rawView.SetSize(w, h)
		return p.rawView.View()
	}
	sel := p.flat[p.ix].n
	detailH := min(len(sel.Details)+1, h/2)
	treeH := h - detailH
	if p.ix < p.top {
		p.top = p.ix
	}
	if p.ix >= p.top+treeH {
		p.top = p.ix - treeH + 1
	}
	var lines []string
	for i := p.top; i < len(p.flat) && i < p.top+treeH; i++ {
		r := p.flat[i]
		arrow := "  "
		if len(r.n.Children) > 0 {
			arrow = "▾ "
			if p.collapsed[r.n] {
				arrow = "▸ "
			}
		}
		metrics := fmt.Sprintf("cost=%.2f rows=%.0f", r.n.Cost, r.n.Rows)
		if p.analyze && r.n.Time > 0 {
			metrics += fmt.Sprintf(" time=%.3fms", r.n.Time)
		}
		line := strings.Repeat("  ", r.depth) + arrow + p.heat(r.n).Render(r.n.Title) + "  " + theme.Dim.Render(metrics)
		line = components.FitStyled(line, w)
		if i == p.ix && focused {
			line = lipgloss.NewStyle().Background(theme.SelBg).Render(components.Fit(stripANSI(line), w))
		}
		lines = append(lines, line)
	}
	out := components.PadLines(strings.Join(lines, "\n"), w, treeH)
	var det []string
	det = append(det, theme.Dim.Render(strings.Repeat("─", w)))
	for _, kv := range sel.Details {
		det = append(det, components.FitStyled(theme.Key.Render(kv.Key+": ")+components.Sanitize(kv.Value), w))
	}
	return out + "\n" + strings.Join(det[:min(len(det), detailH)], "\n")
}

// ---- admin ----

type adminPanel struct {
	ac       *activeConn
	names    []string
	tables   []*db.Table
	grids    []*components.Grid
	errs     []string
	ix       int
	auto     bool
	lastLoad time.Time
	loading  bool
}

const (
	adminProcesses = iota
	adminVariables
	adminUsers
	adminSizes
)

func newAdminPanel(ac *activeConn) *adminPanel {
	return &adminPanel{ac: ac, names: []string{"Processes", "Variables", "Users", "Sizes"},
		tables: make([]*db.Table, 4), grids: make([]*components.Grid, 4), errs: make([]string, 4), auto: true}
}

func (p *adminPanel) title() string { return "Admin" }

type adminMsg struct {
	panel *adminPanel
	ix    int
	table *db.Table
	err   error
}

func (p *adminPanel) load(ix int) tea.Cmd {
	if p.loading {
		return nil
	}
	p.loading = true
	adm := p.ac.conn.Admin()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		var t *db.Table
		var err error
		switch ix {
		case adminProcesses:
			t, err = adm.Processes(ctx)
		case adminVariables:
			t, err = adm.Variables(ctx)
		case adminUsers:
			t, err = adm.Users(ctx)
		case adminSizes:
			t, err = adm.Sizes(ctx)
		}
		return adminMsg{panel: p, ix: ix, table: t, err: err}
	}
}

// apply stores a loaded table, then loads the visible sub-tab if the user
// switched to it while this load was running.
func (p *adminPanel) apply(msg adminMsg, maxW int) tea.Cmd {
	p.loading = false
	p.lastLoad = time.Now()
	p.store(msg, maxW)
	if p.tables[p.ix] == nil && p.errs[p.ix] == "" {
		return p.load(p.ix)
	}
	return nil
}

func (p *adminPanel) store(msg adminMsg, maxW int) {
	if msg.err != nil {
		p.errs[msg.ix] = msg.err.Error()
		return
	}
	p.errs[msg.ix] = ""
	p.tables[msg.ix] = msg.table
	if g := p.grids[msg.ix]; g != nil {
		// keep cursor and filter across refreshes
		row, _ := g.CursorView()
		f := g.Filter()
		ng := dbTableGrid(msg.table, maxW)
		ng.SetFilter(f)
		for i := 0; i < row; i++ {
			ng.Scroll(1)
		}
		p.grids[msg.ix] = ng
		return
	}
	p.grids[msg.ix] = dbTableGrid(msg.table, maxW)
}

// tick auto-refreshes the process list.
func (p *adminPanel) tick() tea.Cmd {
	if p.auto && p.ix == adminProcesses && time.Since(p.lastLoad) >= 2*time.Second {
		return p.load(adminProcesses)
	}
	return nil
}

func (p *adminPanel) selectedID() (string, bool) {
	g := p.grids[p.ix]
	if g == nil {
		return "", false
	}
	row, _ := g.Cursor()
	if row < 0 {
		return "", false
	}
	return g.Rows()[row][0].S, true
}

func (p *adminPanel) update(m *Model, t *tab, km tea.KeyMsg) tea.Cmd {
	k := km.String()
	if ix, ok := subTabKey(k, p.ix, len(p.names)); ok {
		p.ix = ix
		if p.tables[ix] == nil {
			return p.load(ix)
		}
		return nil
	}
	g := p.grids[p.ix]
	if g != nil && g.Update(km) {
		return nil
	}
	adm := p.ac.conn.Admin()
	switch k {
	case "r", "f5":
		return p.load(p.ix)
	case "a":
		p.auto = !p.auto
		return m.toast(fmt.Sprintf("Auto-refresh %s", yesNo(p.auto, "on", "off")), toastInfo)
	case "/":
		if g != nil {
			m.push(newPrompt("Filter", "", g.Filter(), false, func(v string) tea.Cmd { g.SetFilter(strings.TrimSpace(v)); return nil }))
		}
	case "y":
		if g != nil {
			row, col := g.Cursor()
			if row >= 0 {
				return m.copy(g.Rows()[row][col].S, "value")
			}
		}
	case "enter":
		if g == nil {
			return nil
		}
		row, col := g.Cursor()
		if row < 0 {
			return nil
		}
		if p.ix == adminUsers {
			user := g.Rows()[row][0].S
			if len(g.Rows()[row]) > 1 && p.ac.driver() == db.MySQL {
				user = user + "@" + g.Rows()[row][1].S
			}
			return m.showGrants(p.ac, user)
		}
		return m.inspectCell(db.ResultColumn{Name: g.Columns[col]}, g.Rows()[row][col])
	case "c", "K":
		if p.ix != adminProcesses {
			return nil
		}
		id, ok := p.selectedID()
		if !ok {
			return nil
		}
		kill := k == "K"
		verb := yesNo(kill, "Terminate session", "Cancel query of session")
		m.push(&confirmModal{title: verb, danger: true, body: fmt.Sprintf("%s %s on %s?", verb, id, envLabel(p.ac.cfg)),
			onYes: func() tea.Cmd {
				return func() tea.Msg {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					var err error
					if kill {
						err = adm.Kill(ctx, id)
					} else {
						err = adm.CancelQuery(ctx, id)
					}
					if err != nil {
						return toastMsg{text: err.Error(), level: toastError}
					}
					return toastMsg{text: verb + " " + id + ": done", level: toastInfo}
				}
			}})
	}
	return nil
}

func (p *adminPanel) scroll(n int) {
	if g := p.grids[p.ix]; g != nil {
		g.Scroll(n)
	}
}

func (p *adminPanel) view(w, h int, focused bool) string {
	head := subTabs(p.names, p.ix, w-40)
	hint := ""
	if p.ix == adminProcesses {
		hint = theme.Dim.Render(fmt.Sprintf("  c cancel · K kill · a auto-refresh: %s", yesNo(p.auto, "on", "off")))
	}
	head = components.FitStyled(head+hint, w)
	if p.errs[p.ix] != "" {
		return head + "\n" + theme.Error.Render(p.errs[p.ix])
	}
	g := p.grids[p.ix]
	if g == nil {
		return head + "\n" + theme.Dim.Render("loading…")
	}
	if focused {
		g.Focus()
	} else {
		g.Blur()
	}
	g.SetSize(w, h-1)
	return head + "\n" + g.View()
}
