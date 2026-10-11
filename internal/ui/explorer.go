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

type nodeKind int

const (
	nDatabase nodeKind = iota
	nSchema
	nGroup
	nObject
	nColumn
	nInfo
)

type node struct {
	kind     nodeKind
	label    string
	detail   string
	database string
	schema   string
	group    db.ObjectKind
	obj      db.Object
	children []*node
	parent   *node
	depth    int
	expanded bool
	loaded   bool
	loading  bool
}

func (n *node) ref() db.ObjectRef { return n.obj.Ref(n.database) }

func (n *node) expandable() bool {
	switch n.kind {
	case nDatabase, nSchema, nGroup:
		return true
	case nObject:
		k := n.obj.Kind
		return k == db.KindTable || k == db.KindView || k == db.KindMatView
	}
	return false
}

// explorer is the database object tree of one connection.
type explorer struct {
	ac      *activeConn
	roots   []*node
	flat    []*node
	ix, top int
	filter  string
	started bool
	pending string

	lastClick    int // row of the previous click (double-click detection)
	lastClickAt  time.Time
	clickToggled bool
}

type explorerMsg struct {
	ac       *activeConn
	parent   *node // nil = roots
	children []*node
	err      error
}

func newExplorer(ac *activeConn) *explorer { return &explorer{ac: ac} }

var groupOrder = []struct {
	kind  db.ObjectKind
	label string
}{
	{db.KindTable, "Tables"}, {db.KindView, "Views"}, {db.KindMatView, "Materialized views"},
	{db.KindFunction, "Functions"}, {db.KindProcedure, "Procedures"}, {db.KindSequence, "Sequences"},
	{db.KindEvent, "Events"},
}

// load fetches the children of parent (nil = databases).
func (e *explorer) load(parent *node) tea.Cmd {
	if parent != nil {
		parent.loading = true
	}
	e.rebuild()
	ac := e.ac
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		kids, err := loadChildren(ctx, ac, parent)
		return explorerMsg{ac: ac, parent: parent, children: kids, err: err}
	}
}

func loadChildren(ctx context.Context, ac *activeConn, parent *node) ([]*node, error) {
	conn := ac.conn
	switch {
	case parent == nil:
		dbs, err := conn.Databases(ctx)
		if err != nil {
			return nil, err
		}
		var out []*node
		names := make([]string, 0, len(dbs))
		for _, d := range dbs {
			names = append(names, d.Name)
			out = append(out, &node{kind: nDatabase, label: d.Name, detail: d.Size, database: d.Name})
		}
		ac.cache.SetDatabases(names)
		return out, nil
	case parent.kind == nDatabase && conn.Dialect().HasSchemas():
		schemas, err := conn.Schemas(ctx, parent.database)
		if err != nil {
			return nil, err
		}
		ac.cache.SetSchemas(parent.database, schemas)
		var out []*node
		for _, s := range schemas {
			out = append(out, &node{kind: nSchema, label: s, database: parent.database, schema: s})
		}
		return out, nil
	case parent.kind == nDatabase || parent.kind == nSchema:
		schema := parent.schema
		if parent.kind == nDatabase {
			schema = parent.database
		}
		objs, err := conn.Objects(ctx, parent.database, schema)
		if err != nil {
			return nil, err
		}
		ac.cache.SetObjects(parent.database, schema, objs)
		var out []*node
		for _, g := range groupOrder {
			grp := &node{kind: nGroup, group: g.kind, database: parent.database, schema: schema, loaded: true}
			for _, o := range objs {
				if o.Kind == g.kind {
					grp.children = append(grp.children, &node{kind: nObject, label: o.Name, detail: o.Comment,
						database: parent.database, schema: schema, obj: o})
				}
			}
			if len(grp.children) > 0 {
				grp.label = fmt.Sprintf("%s (%d)", g.label, len(grp.children))
				out = append(out, grp)
			}
		}
		if len(out) == 0 {
			out = append(out, &node{kind: nInfo, label: "(empty)"})
		}
		return out, nil
	case parent.kind == nObject:
		d, err := conn.TableDetail(ctx, parent.ref())
		if err != nil {
			return nil, err
		}
		var out []*node
		for _, c := range d.Columns {
			label := c.Name
			if c.IsPK {
				label += " 🔑"
			}
			det := c.Type
			if !c.Nullable {
				det += " not null"
			}
			out = append(out, &node{kind: nColumn, label: c.Name, detail: det, database: parent.database,
				schema: parent.schema, obj: db.Object{Name: c.Name}, loaded: true,
				group: map[bool]db.ObjectKind{true: "pk", false: ""}[c.IsPK]})
		}
		return out, nil
	}
	return nil, nil
}

func (e *explorer) apply(msg explorerMsg) tea.Cmd {
	var cmds []tea.Cmd
	attach := func(parent *node, kids []*node) {
		for _, k := range kids {
			k.parent = parent
			if parent != nil {
				k.depth = parent.depth + 1
			}
			for _, gc := range k.children {
				gc.parent = k
				gc.depth = k.depth + 1
			}
		}
	}
	if msg.parent == nil {
		if msg.err != nil {
			e.roots = []*node{{kind: nInfo, label: "error: " + msg.err.Error()}}
			e.rebuild()
			return nil
		}
		attach(nil, msg.children)
		e.roots = msg.children
		// expand the connection's default database
		want := e.ac.cfg.Database
		for _, r := range e.roots {
			if r.database == want || (want == "" && len(e.roots) == 1) || (want == "" && r.database == e.ac.cfg.User) {
				r.expanded = true
				cmds = append(cmds, e.load(r))
				break
			}
		}
	} else {
		p := msg.parent
		p.loading = false
		if msg.err != nil {
			p.children = []*node{{kind: nInfo, label: "error: " + msg.err.Error(), parent: p, depth: p.depth + 1}}
		} else {
			attach(p, msg.children)
			p.children = msg.children
			p.loaded = true
			// auto-expand a single schema like "public"
			if p.kind == nDatabase {
				for _, c := range p.children {
					if c.kind == nSchema && (c.schema == "public" || len(p.children) == 1) {
						c.expanded = true
						cmds = append(cmds, e.load(c))
					}
				}
			}
			if p.kind == nSchema || (p.kind == nDatabase && !e.ac.dialect().HasSchemas()) {
				for _, c := range p.children {
					if c.group == db.KindTable {
						c.expanded = true
					}
				}
			}
		}
	}
	e.rebuild()
	return tea.Batch(cmds...)
}

func (e *explorer) rebuild() {
	e.flat = e.flat[:0]
	f := strings.ToLower(e.filter)
	var walk func(ns []*node)
	walk = func(ns []*node) {
		for _, n := range ns {
			if f != "" && !n.matches(f) {
				continue
			}
			e.flat = append(e.flat, n)
			if n.expanded || (f != "" && n.kind != nObject) {
				walk(n.children)
			}
		}
	}
	walk(e.roots)
	e.ix = clampIx(e.ix, len(e.flat))
}

func (n *node) matches(f string) bool {
	if n.kind == nObject && strings.Contains(strings.ToLower(n.label), f) {
		return true
	}
	if n.kind == nObject || n.kind == nColumn {
		return false
	}
	for _, c := range n.children {
		if c.matches(f) {
			return true
		}
	}
	return false
}

func (e *explorer) selected() *node {
	if e.ix < 0 || e.ix >= len(e.flat) {
		return nil
	}
	return e.flat[e.ix]
}

func (e *explorer) toggle(n *node) tea.Cmd {
	if !n.expandable() {
		return nil
	}
	n.expanded = !n.expanded
	if n.expanded && !n.loaded && !n.loading {
		return e.load(n)
	}
	e.rebuild()
	return nil
}

// refresh reloads the schema (or database) containing the selection, or
// everything when nothing is selected.
func (e *explorer) refresh() tea.Cmd {
	n := e.selected()
	for n != nil && n.kind != nDatabase && n.kind != nSchema {
		n = n.parent
	}
	if n == nil {
		e.ac.cache.Invalidate("")
		return e.load(nil)
	}
	e.ac.cache.Invalidate(n.database)
	n.loaded, n.expanded = false, true
	return e.load(n)
}

// update handles keys while the explorer is focused.
func (e *explorer) update(m *Model, km tea.KeyMsg) tea.Cmd {
	k := km.String()
	if e.pending == "g" {
		e.pending = ""
		if k == "g" {
			e.ix = 0
			return nil
		}
	}
	n := e.selected()
	switch k {
	case "j", "down":
		e.ix = clampIx(e.ix+1, len(e.flat))
	case "k", "up":
		e.ix = clampIx(e.ix-1, len(e.flat))
	case "g":
		e.pending = "g"
	case "G":
		e.ix = len(e.flat) - 1
	case "ctrl+d", "pgdown":
		e.ix = clampIx(e.ix+10, len(e.flat))
	case "ctrl+u", "pgup":
		e.ix = clampIx(e.ix-10, len(e.flat))
	case "l", "right", " ":
		if n != nil && n.expandable() && !n.expanded {
			return e.toggle(n)
		}
		if n != nil && n.expanded && len(n.children) > 0 {
			e.ix++
		}
	case "h", "left":
		if n == nil {
			return nil
		}
		if n.expanded {
			n.expanded = false
			e.rebuild()
		} else if n.parent != nil {
			for i, f := range e.flat {
				if f == n.parent {
					e.ix = i
				}
			}
		}
	case "t":
		if n != nil && n.kind == nObject && n.expandable() {
			return m.openTableInNewTab(n.ref())
		}
	case "enter", "o":
		if n == nil {
			return nil
		}
		if n.kind == nObject {
			switch n.obj.Kind {
			case db.KindTable, db.KindView, db.KindMatView:
				return m.openTableData(n.ref())
			default:
				return m.showDDL(n.ref())
			}
		}
		return e.toggle(n)
	case "d":
		if n != nil && n.kind == nObject && n.expandable() {
			return m.showDetail(n.ref())
		}
	case "D":
		if n != nil && n.kind == nObject {
			return m.showDDL(n.ref())
		}
	case "s":
		if n != nil && n.kind == nObject && n.expandable() {
			return m.insertSelect(n.ref())
		}
	case "c", "y":
		if n != nil && n.kind != nInfo {
			name := n.label
			if n.kind == nObject {
				name = e.ac.dialect().Qualified(n.ref())
			}
			return m.copy(name, "name")
		}
	case "x":
		if n != nil && n.kind == nObject && n.expandable() {
			return m.exportTable(n.ref())
		}
	case "i":
		if n != nil && n.kind == nObject && n.obj.Kind == db.KindTable {
			return m.importCSV(n.ref())
		}
	case "u":
		if n != nil {
			return m.useDatabase(n.database, n.schema)
		}
	case "r", "f5":
		return e.refresh()
	case "/":
		pm := newPrompt("Filter objects", "", e.filter, false, func(v string) tea.Cmd {
			e.filter = strings.TrimSpace(v)
			e.ix = 0
			e.rebuild()
			return nil
		})
		m.push(pm)
	case "esc":
		if e.filter != "" {
			e.filter = ""
			e.rebuild()
		}
	}
	return nil
}

const doubleClick = 400 * time.Millisecond

// click handles a left click at row/x inside the explorer pane. Clicking the
// arrow (or a selected database/schema/group) expands it; double-clicking a
// table or view opens its rows in a new tab.
func (e *explorer) click(m *Model, row, x int) tea.Cmd {
	if e.filter != "" {
		row-- // the filter line sits above the tree
	}
	i := e.top + row
	if i < 0 || i >= len(e.flat) {
		return nil
	}
	n := e.flat[i]
	now := time.Now()
	double := i == e.lastClick && now.Sub(e.lastClickAt) < doubleClick
	wasSelected := i == e.ix
	e.lastClick, e.lastClickAt, e.ix = i, now, i
	if double {
		e.lastClick = -1 // a third click starts a new sequence
		if n.kind == nObject {
			switch n.obj.Kind {
			case db.KindTable, db.KindView, db.KindMatView:
				return m.openTableInNewTab(n.ref())
			default:
				return m.showDDL(n.ref())
			}
		}
		if e.clickToggled {
			return nil // the first click already expanded/collapsed it
		}
		return e.toggle(n)
	}
	arrowX := 1 + 2*n.depth // after the pane border and indent
	onArrow := x >= arrowX && x < arrowX+2
	e.clickToggled = n.expandable() && (onArrow || wasSelected && n.kind != nObject)
	if e.clickToggled {
		return e.toggle(n)
	}
	return nil
}

func (e *explorer) view(w, h int, focused bool, current string) string {
	if e.ix < e.top {
		e.top = e.ix
	}
	if e.ix >= e.top+h {
		e.top = e.ix - h + 1
	}
	var lines []string
	if e.filter != "" {
		lines = append(lines, theme.Warn.Render(components.Fit("filter: "+e.filter+"  (esc clears)", w)))
		h--
	}
	if len(e.flat) == 0 {
		lines = append(lines, theme.Dim.Render(" loading…"))
	}
	for i := e.top; i < len(e.flat) && i < e.top+h; i++ {
		n := e.flat[i]
		indent := strings.Repeat("  ", n.depth)
		arrow := "  "
		if n.expandable() {
			arrow = "▸ "
			if n.expanded {
				arrow = "▾ "
			}
		}
		if n.loading {
			arrow = "◌ "
		}
		label := n.label
		st := lipgloss.NewStyle()
		switch n.kind {
		case nDatabase:
			st = st.Foreground(theme.Cyan).Bold(n.database == current)
			if n.database == current {
				label += " ●"
			}
		case nSchema:
			st = st.Foreground(theme.Purple)
		case nGroup:
			st = theme.Dim
		case nObject:
			switch n.obj.Kind {
			case db.KindView, db.KindMatView:
				st = st.Foreground(theme.Yellow)
			case db.KindFunction, db.KindProcedure:
				st = st.Foreground(theme.Accent)
			case db.KindSequence, db.KindEvent:
				st = st.Foreground(theme.Orange)
			}
		case nColumn:
			if n.group == "pk" {
				st = st.Foreground(theme.Yellow)
				label = "🔑" + label
			}
		case nInfo:
			st = theme.Dim
			if strings.HasPrefix(n.label, "error") {
				st = theme.Error
			}
		}
		text := indent + arrow + st.Render(label)
		if n.detail != "" && (n.kind == nColumn || n.kind == nDatabase) {
			text += " " + theme.Dim.Render(n.detail)
		}
		text = components.FitStyled(text, w)
		if i == e.ix {
			if focused {
				text = lipgloss.NewStyle().Background(theme.SelBg).Render(components.Fit(stripANSI(text), w))
			} else {
				text = lipgloss.NewStyle().Background(theme.Subtle).Render(components.Fit(stripANSI(text), w))
			}
		}
		lines = append(lines, text)
	}
	return strings.Join(lines, "\n")
}

func clampIx(i, n int) int {
	if i >= n {
		i = n - 1
	}
	if i < 0 {
		i = 0
	}
	return i
}
