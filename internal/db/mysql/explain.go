package mysql

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/farvisun/squi/internal/db"
)

// Explain uses EXPLAIN FORMAT=TREE / EXPLAIN ANALYZE on MySQL 8.0.16+ /
// 8.0.18+, and EXPLAIN FORMAT=JSON / ANALYZE FORMAT=JSON elsewhere
// (ANALYZE is MariaDB only).
func (s *session) Explain(ctx context.Context, q string, analyze bool) (*db.PlanNode, error) {
	q = strings.TrimRight(strings.TrimSpace(q), ";")
	c := s.c
	tree := c.mysqlAtLeast(8, 0, 16)
	var stmt string
	switch {
	case analyze && c.mysqlAtLeast(8, 0, 18):
		stmt = "EXPLAIN ANALYZE " + q
	case analyze && c.mariadb:
		stmt = "ANALYZE FORMAT=JSON " + q
	case analyze:
		return nil, db.ErrNotSupported
	case tree:
		stmt = "EXPLAIN FORMAT=TREE " + q
	default:
		stmt = "EXPLAIN FORMAT=JSON " + q
	}

	qctx, release := s.watch(ctx)
	defer release()
	var raw string
	if err := s.conn.QueryRowContext(qctx, stmt).Scan(&raw); err != nil {
		return nil, wrapErr(err)
	}
	var root *db.PlanNode
	if tree {
		root = parseTree(raw)
	} else {
		var err error
		if root, err = parseJSONPlan(raw, analyze); err != nil {
			return nil, err
		}
	}
	root.Raw = raw
	return root, nil
}

const numPat = `\d+(?:\.\d+)?(?:e[+-]?\d+)?`

var (
	costRe   = regexp.MustCompile(`\(cost=(` + numPat + `)(?:\.\.(` + numPat + `))? rows=(` + numPat + `)\)`)
	actualRe = regexp.MustCompile(`\(actual time=(` + numPat + `)\.\.(` + numPat + `) rows=(` + numPat + `) loops=(\d+)\)`)
)

// parseTree parses MySQL's indented "-> node (cost=… rows=…) (actual …)" plan.
func parseTree(raw string) *db.PlanNode {
	root := &db.PlanNode{Title: "Plan"}
	type level struct {
		indent int
		node   *db.PlanNode
	}
	stack := []level{{-1, root}}
	for _, line := range strings.Split(raw, "\n") {
		text := strings.TrimLeft(line, " ")
		if !strings.HasPrefix(text, "->") {
			if text != "" && len(stack) > 1 { // wrapped continuation line
				n := stack[len(stack)-1].node
				n.Title += " " + strings.TrimSpace(text)
			}
			continue
		}
		indent := len(line) - len(text)
		n := treeNode(strings.TrimSpace(text[2:]))
		for len(stack) > 1 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
		parent := stack[len(stack)-1].node
		parent.Children = append(parent.Children, n)
		stack = append(stack, level{indent, n})
	}
	if len(root.Children) == 1 {
		return root.Children[0]
	}
	return root
}

func treeNode(text string) *db.PlanNode {
	n := &db.PlanNode{Title: text}
	cut := len(text)
	if m := costRe.FindStringSubmatchIndex(text); m != nil {
		cut = min(cut, m[0])
		lo, hi := text[m[2]:m[3]], text[m[2]:m[3]]
		if m[4] >= 0 {
			hi = text[m[4]:m[5]]
		}
		n.Cost = num(hi)
		n.Rows = num(text[m[6]:m[7]])
		cost := hi
		if lo != hi {
			cost = lo + ".." + hi
		}
		n.Details = append(n.Details, db.KV{Key: "cost", Value: cost}, db.KV{Key: "estimated rows", Value: text[m[6]:m[7]]})
	}
	if m := actualRe.FindStringSubmatchIndex(text); m != nil {
		cut = min(cut, m[0])
		first, last := text[m[2]:m[3]], text[m[4]:m[5]]
		rows, loops := text[m[6]:m[7]], text[m[8]:m[9]]
		n.Rows = num(rows)
		n.Time = num(last) * num(loops)
		n.Details = append(n.Details,
			db.KV{Key: "actual time", Value: first + ".." + last + " ms"},
			db.KV{Key: "actual rows", Value: rows},
			db.KV{Key: "loops", Value: loops})
	}
	if i := strings.Index(text, "(never executed)"); i >= 0 {
		cut = min(cut, i)
		n.Details = append(n.Details, db.KV{Key: "actual", Value: "never executed"})
	}
	n.Title = strings.TrimSpace(text[:cut])
	return n
}

func num(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

// member is a key/value of a JSON object; jsonObj keeps key order.
type member struct {
	key string
	val any // string, json.Number, bool, nil, jsonObj or []any
}

type jsonObj []member

func (o jsonObj) get(key string) any {
	for _, m := range o {
		if m.key == key {
			return m.val
		}
	}
	return nil
}

func decodeOrdered(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch tok {
	case json.Delim('{'):
		var o jsonObj
		for dec.More() {
			k, err := dec.Token()
			if err != nil {
				return nil, err
			}
			v, err := decodeOrdered(dec)
			if err != nil {
				return nil, err
			}
			o = append(o, member{k.(string), v})
		}
		_, err := dec.Token()
		return o, err
	case json.Delim('['):
		var a []any
		for dec.More() {
			v, err := decodeOrdered(dec)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
		_, err := dec.Token()
		return a, err
	}
	return tok, nil
}

// parseJSONPlan converts MySQL/MariaDB EXPLAIN FORMAT=JSON output into a
// tree: nested objects and arrays become child nodes, "table" objects
// become "name: access_type" nodes and scalars become details.
func parseJSONPlan(raw string, analyze bool) (*db.PlanNode, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	v, err := decodeOrdered(dec)
	if err != nil {
		return nil, fmt.Errorf("parse plan: %w", err)
	}
	obj, ok := v.(jsonObj)
	if !ok {
		return nil, fmt.Errorf("parse plan: unexpected %T", v)
	}
	root := jsonNode("Plan", obj, analyze)
	if len(root.Children) == 1 && obj.get("query_block") != nil {
		child := root.Children[0]
		child.Details = append(root.Details, child.Details...)
		root = child
	}
	return root, nil
}

func jsonNode(title string, obj jsonObj, analyze bool) *db.PlanNode {
	n := &db.PlanNode{Title: strings.ReplaceAll(title, "_", " ")}
	switch id := obj.get("select_id"); {
	case title == "table":
		n.Title = fmt.Sprint(obj.get("table_name"))
		if at := obj.get("access_type"); at != nil {
			n.Title += fmt.Sprintf(": %v", at)
		}
	case title == "query_block" && id != nil:
		n.Title = fmt.Sprintf("query block #%v", id)
	}
	addDetails(n, "", obj)
	for _, m := range obj {
		switch v := m.val.(type) {
		case jsonObj:
			if !scalarObj(v) {
				n.Children = append(n.Children, jsonNode(m.key, v, analyze))
			}
		case []any:
			if child := jsonArray(m.key, v, analyze); child != nil {
				n.Children = append(n.Children, child)
			}
		}
	}

	n.Cost = firstNum(n.Details, "cost", "cost_info.query_cost", "cost_info.prefix_cost")
	n.Rows = firstNum(n.Details, "rows", "rows_examined_per_scan")
	if analyze {
		if r := firstNum(n.Details, "r_rows"); r > 0 {
			n.Rows = r
		}
		n.Time = firstNum(n.Details, "r_total_time_ms")
		if n.Time == 0 {
			n.Time = firstNum(n.Details, "r_table_time_ms") + firstNum(n.Details, "r_other_time_ms")
		}
	}
	return n
}

// jsonArray turns an array of objects into a node (e.g. "nested_loop")
// whose children are its elements; arrays of scalars are details.
func jsonArray(key string, arr []any, analyze bool) *db.PlanNode {
	n := &db.PlanNode{Title: strings.ReplaceAll(key, "_", " ")}
	for _, e := range arr {
		o, ok := e.(jsonObj)
		if !ok {
			continue
		}
		if len(o) == 1 { // {"table": {...}} / {"query_block": {...}} wrapper
			if inner, ok := o[0].val.(jsonObj); ok {
				n.Children = append(n.Children, jsonNode(o[0].key, inner, analyze))
				continue
			}
		}
		n.Children = append(n.Children, jsonNode(key, o, analyze))
	}
	if len(n.Children) == 0 {
		return nil
	}
	return n
}

// addDetails adds scalar members (and members of scalar-only objects,
// prefixed with their key) as details.
func addDetails(n *db.PlanNode, prefix string, obj jsonObj) {
	for _, m := range obj {
		key := prefix + m.key
		switch v := m.val.(type) {
		case jsonObj:
			if scalarObj(v) {
				addDetails(n, key+".", v)
			}
		case []any:
			var parts []string
			for _, e := range v {
				if _, isObj := e.(jsonObj); !isObj {
					parts = append(parts, fmt.Sprint(e))
				}
			}
			if len(parts) == len(v) && len(parts) > 0 {
				n.Details = append(n.Details, db.KV{Key: key, Value: strings.Join(parts, ", ")})
			}
		case nil:
			n.Details = append(n.Details, db.KV{Key: key, Value: "null"})
		default:
			n.Details = append(n.Details, db.KV{Key: key, Value: fmt.Sprint(v)})
		}
	}
}

func scalarObj(o jsonObj) bool {
	for _, m := range o {
		switch m.val.(type) {
		case jsonObj, []any:
			return false
		}
	}
	return true
}

func firstNum(kvs []db.KV, keys ...string) float64 {
	for _, k := range keys {
		for _, kv := range kvs {
			if kv.Key == k {
				if f, err := strconv.ParseFloat(kv.Value, 64); err == nil {
					return f
				}
			}
		}
	}
	return 0
}
