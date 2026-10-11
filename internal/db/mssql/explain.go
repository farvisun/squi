package mssql

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/farvisun/squi/internal/db"
)

// Explain returns the estimated plan (SET SHOWPLAN_XML: the statement is
// not run) or, with analyze, the actual plan (SET STATISTICS XML: the
// statement runs and its results are discarded).
func (s *session) Explain(ctx context.Context, sql string, analyze bool) (*db.PlanNode, error) {
	on, off := "SET SHOWPLAN_XML ON", "SET SHOWPLAN_XML OFF"
	if analyze {
		on, off = "SET STATISTICS XML ON", "SET STATISTICS XML OFF"
	}
	if err := s.exec(ctx, on); err != nil {
		return nil, err
	}
	defer s.exec(context.WithoutCancel(ctx), off) //nolint:errcheck // best effort

	plans, err := s.collectPlans(ctx, strings.TrimRight(sql, "; \t\r\n"))
	if err != nil {
		return nil, err
	}
	if len(plans) == 0 {
		return nil, errors.New("sqlserver: no plan returned (statements without a plan, such as DDL, cannot be explained)")
	}
	var root *db.PlanNode
	for _, p := range plans {
		n, err := parsePlan(p, analyze)
		if err != nil {
			return nil, err
		}
		switch {
		case root == nil:
			root = n
		case root.Title != "Batch":
			root = &db.PlanNode{Title: "Batch", Children: []*db.PlanNode{root, n}}
		default:
			root.Children = append(root.Children, n)
		}
	}
	var raw []string
	for _, p := range plans {
		raw = append(raw, indentXML(p))
	}
	root.Raw = strings.Join(raw, "\n\n")
	return root, nil
}

// showplanColumn names the result set carrying an XML plan.
const showplanColumn = "Microsoft SQL Server 2005 XML Showplan"

// collectPlans runs q and returns the XML plans among its result sets.
func (s *session) collectPlans(ctx context.Context, q string) ([]string, error) {
	qctx, release := s.watch(ctx)
	defer release()
	rows, err := s.conn.QueryContext(qctx, q)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer rows.Close()
	var plans []string
	for {
		cols, err := rows.Columns()
		if err != nil {
			return nil, wrapErr(err)
		}
		isPlan := len(cols) == 1 && cols[0] == showplanColumn
		for rows.Next() {
			if !isPlan {
				continue // the statement's own results (analyze)
			}
			var p string
			if err := rows.Scan(&p); err != nil {
				return nil, err
			}
			plans = append(plans, p)
		}
		if !rows.NextResultSet() {
			break
		}
	}
	return plans, wrapErr(rows.Err())
}

// xnode is a generic XML element.
type xnode struct {
	XMLName xml.Name
	Attrs   []xml.Attr `xml:",any,attr"`
	Nodes   []xnode    `xml:",any"`
}

func (n *xnode) attr(name string) string {
	for _, a := range n.Attrs {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func (n *xnode) num(name string) float64 {
	f, _ := strconv.ParseFloat(n.attr(name), 64)
	return f
}

// find returns the descendants named name, not descending into RelOp
// elements below n (they belong to child operators).
func (n *xnode) find(name string) []*xnode {
	var out []*xnode
	var walk func(*xnode)
	walk = func(x *xnode) {
		for i := range x.Nodes {
			c := &x.Nodes[i]
			if c.XMLName.Local == name {
				out = append(out, c)
				if name == "RelOp" {
					continue
				}
			}
			if c.XMLName.Local != "RelOp" {
				walk(c)
			}
		}
	}
	walk(n)
	return out
}

// parsePlan converts one ShowPlanXML document into a plan tree: one node
// per statement, its operators below.
func parsePlan(doc string, analyze bool) (*db.PlanNode, error) {
	var top xnode
	dec := xml.NewDecoder(strings.NewReader(doc))
	// the text is already decoded; a saved plan may still declare utf-16
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	if err := dec.Decode(&top); err != nil {
		return nil, fmt.Errorf("sqlserver: parse plan: %w", err)
	}
	var stmts []*db.PlanNode
	var walk func(*xnode)
	walk = func(x *xnode) {
		for i := range x.Nodes {
			c := &x.Nodes[i]
			if c.XMLName.Local == "StmtSimple" || c.XMLName.Local == "StmtCursor" {
				stmts = append(stmts, statementNode(c, analyze))
				continue
			}
			walk(c)
		}
	}
	walk(&top)
	switch len(stmts) {
	case 0:
		return nil, errors.New("sqlserver: the plan has no statements")
	case 1:
		return stmts[0], nil
	}
	return &db.PlanNode{Title: "Batch", Children: stmts}, nil
}

func statementNode(st *xnode, analyze bool) *db.PlanNode {
	typ := st.attr("StatementType")
	if typ == "" {
		typ = "Statement"
	}
	n := &db.PlanNode{Title: typ, Cost: st.num("StatementSubTreeCost"), Rows: st.num("StatementEstRows")}
	add := func(k, v string) {
		if v != "" {
			n.Details = append(n.Details, db.KV{Key: k, Value: v})
		}
	}
	if text := strings.Join(strings.Fields(st.attr("StatementText")), " "); text != "" {
		if len(text) > 200 {
			text = text[:200] + "…"
		}
		add("Statement", text)
	}
	add("Optimization", st.attr("StatementOptmLevel"))
	add("Early abort", st.attr("StatementOptmEarlyAbortReason"))
	add("CE model", st.attr("CardinalityEstimationModelVersion"))
	for _, qp := range st.find("QueryPlan") {
		add("Degree of parallelism", qp.attr("DegreeOfParallelism"))
		if mg := qp.find("MemoryGrantInfo"); len(mg) > 0 {
			// estimated plans grant nothing; they carry the desired size
			if g := mg[0].num("GrantedMemory"); g > 0 {
				add("Memory grant", mg[0].attr("GrantedMemory")+" KB")
			} else if d := mg[0].num("SerialDesiredMemory"); d > 0 {
				add("Desired memory", mg[0].attr("SerialDesiredMemory")+" KB")
			}
		}
		for _, mi := range qp.find("MissingIndexGroup") {
			for _, ix := range mi.find("MissingIndex") {
				add("Missing index", missingIndex(ix)+" -- impact "+mi.attr("Impact")+"%")
			}
		}
		for _, w := range qp.find("Warnings") {
			add("Warnings", warnings(w))
		}
		for _, r := range qp.find("RelOp") {
			n.Children = append(n.Children, relOpNode(r, analyze))
		}
	}
	if analyze && len(n.Children) == 1 {
		n.Rows, n.Time = n.Children[0].Rows, n.Children[0].Time
	}
	return n
}

func relOpNode(r *xnode, analyze bool) *db.PlanNode {
	title := r.attr("PhysicalOp")
	if l := r.attr("LogicalOp"); l != "" && l != title {
		title += " (" + l + ")"
	}
	if objs := r.find("Object"); len(objs) > 0 {
		o := objs[0]
		on := strings.Trim(o.attr("Table"), "[]")
		if s := strings.Trim(o.attr("Schema"), "[]"); s != "" && on != "" {
			on = s + "." + on
		}
		if ix := strings.Trim(o.attr("Index"), "[]"); ix != "" {
			on += " [" + ix + "]"
		}
		if on != "" {
			title += " on " + on
		}
	}
	n := &db.PlanNode{Title: title, Cost: r.num("EstimatedTotalSubtreeCost"), Rows: r.num("EstimateRows")}
	add := func(k, v string) {
		if v != "" {
			n.Details = append(n.Details, db.KV{Key: k, Value: v})
		}
	}
	for _, k := range []struct{ key, name string }{
		{"SeekPredicates", "Seek"}, {"Predicate", "Predicate"}, {"ProbeResidual", "Residual"},
		{"HashKeysBuild", "Hash keys"}, {"OrderBy", "Order by"}, {"GroupBy", "Group by"},
	} {
		var parts []string
		for _, el := range r.find(k.key) {
			parts = append(parts, scalarText(el))
		}
		add(k.name, strings.Join(parts, " AND "))
	}
	if v := r.attr("EstimateExecutions"); v != "" && v != "1" {
		add("Estimated executions", v)
	}
	add("Estimated rows read", r.attr("EstimatedRowsRead"))
	if r.attr("Parallel") == "1" || r.attr("Parallel") == "true" {
		add("Parallel", "yes")
	}
	for _, w := range r.find("Warnings") {
		add("Warnings", warnings(w))
	}
	for _, c := range r.find("RelOp") {
		n.Children = append(n.Children, relOpNode(c, analyze))
	}
	if analyze {
		counters := r.find("RunTimeCountersPerThread")
		if len(counters) == 0 {
			// operators such as Compute Scalar carry no counters: show
			// what their input produced
			n.Rows, n.Time = 0, 0
			if len(n.Children) > 0 {
				n.Rows, n.Time = n.Children[0].Rows, n.Children[0].Time
			}
			return n
		}
		var rows, execs, reads float64
		elapsed := 0.0
		for _, t := range counters {
			rows += t.num("ActualRows")
			execs += t.num("ActualExecutions")
			reads += t.num("ActualLogicalReads")
			elapsed = max(elapsed, t.num("ActualElapsedms"))
		}
		n.Rows, n.Time = rows, elapsed
		add("Actual executions", strconv.FormatFloat(execs, 'f', -1, 64))
		if reads > 0 {
			add("Logical reads", strconv.FormatFloat(reads, 'f', -1, 64))
		}
	}
	return n
}

// scalarText joins the ScalarString of the outermost scalar operators.
func scalarText(n *xnode) string {
	var parts []string
	var walk func(*xnode)
	walk = func(x *xnode) {
		for i := range x.Nodes {
			c := &x.Nodes[i]
			if s := c.attr("ScalarString"); s != "" {
				parts = append(parts, s)
				continue
			}
			if c.XMLName.Local == "ColumnReference" {
				parts = append(parts, columnRef(c))
				continue
			}
			walk(c)
		}
	}
	walk(n)
	return strings.Join(parts, ", ")
}

func columnRef(c *xnode) string {
	name := c.attr("Column")
	if t := c.attr("Table"); t != "" {
		name = t + "." + name
	}
	return name
}

func missingIndex(ix *xnode) string {
	var keys, include []string
	for _, g := range ix.find("ColumnGroup") {
		for _, c := range g.find("Column") {
			if g.attr("Usage") == "INCLUDE" {
				include = append(include, c.attr("Name"))
			} else {
				keys = append(keys, c.attr("Name"))
			}
		}
	}
	s := "CREATE INDEX ON " + ix.attr("Schema") + "." + ix.attr("Table") + " (" + strings.Join(keys, ", ") + ")"
	if len(include) > 0 {
		s += " INCLUDE (" + strings.Join(include, ", ") + ")"
	}
	return s
}

// warnings names the warnings of an operator, e.g. an implicit conversion
// that prevents a seek.
func warnings(w *xnode) string {
	var out []string
	for i := range w.Nodes {
		c := &w.Nodes[i]
		s := c.XMLName.Local
		if e := c.attr("Expression"); e != "" {
			s += ": " + e
		}
		out = append(out, s)
	}
	for _, a := range w.Attrs {
		if a.Value == "true" || a.Value == "1" {
			out = append(out, a.Name.Local)
		}
	}
	return strings.Join(out, "; ")
}

// indentXML pretty-prints a plan for the raw view.
func indentXML(s string) string {
	dec := xml.NewDecoder(strings.NewReader(s))
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	var b bytes.Buffer
	enc := xml.NewEncoder(&b)
	enc.Indent("", "  ")
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return s
		}
		switch t := tok.(type) {
		case xml.CharData:
			if len(bytes.TrimSpace(t)) == 0 {
				continue
			}
		case xml.StartElement:
			// drop the namespace: the encoder would repeat it on every element
			t.Name.Space = ""
			attrs := t.Attr[:0:0]
			for _, a := range t.Attr {
				if a.Name.Space == "" && a.Name.Local != "xmlns" {
					attrs = append(attrs, a)
				}
			}
			t.Attr = attrs
			tok = t
		case xml.EndElement:
			t.Name.Space = ""
			tok = t
		case xml.ProcInst:
			continue
		}
		if err := enc.EncodeToken(xml.CopyToken(tok)); err != nil {
			return s
		}
	}
	if enc.Flush() != nil {
		return s
	}
	return b.String()
}
