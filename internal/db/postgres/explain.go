package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/farvisun/squi/internal/db"
)

// maxOutputLen truncates the "Output" detail of plan nodes.
const maxOutputLen = 200

// Explain runs EXPLAIN (FORMAT JSON) for sql and converts the plan into a
// db.PlanNode tree. Error positions refer to sql, not the EXPLAIN prefix.
func (s *session) Explain(ctx context.Context, sql string, analyze bool) (*db.PlanNode, error) {
	prefix := "EXPLAIN (FORMAT JSON, VERBOSE, COSTS) "
	if analyze {
		prefix = "EXPLAIN (FORMAT JSON, VERBOSE, COSTS, ANALYZE, BUFFERS) "
	}
	body := strings.TrimRight(sql, "; \t\r\n")
	res, err := s.pc.PgConn().Exec(ctx, prefix+body).ReadAll()
	if err != nil {
		return nil, queryError(err, len([]rune(prefix)))
	}
	if len(res) == 0 || len(res[len(res)-1].Rows) == 0 {
		return nil, errors.New("postgres: EXPLAIN returned no plan")
	}
	raw := res[len(res)-1].Rows[0][0]

	var plans []map[string]any
	if err := json.Unmarshal(raw, &plans); err != nil {
		return nil, fmt.Errorf("postgres: parse EXPLAIN output: %w", err)
	}
	if len(plans) == 0 {
		return nil, errors.New("postgres: EXPLAIN returned no plan")
	}
	top := plans[0]
	plan, _ := top["Plan"].(map[string]any)
	root := planNode(plan, analyze)
	for _, k := range []string{"Planning Time", "Execution Time"} {
		if v, ok := top[k].(float64); ok {
			root.Details = append(root.Details, db.KV{Key: k, Value: strconv.FormatFloat(v, 'f', 3, 64) + " ms"})
		}
	}
	if pretty, err := json.MarshalIndent(plans, "", "  "); err == nil {
		root.Raw = string(pretty)
	} else {
		root.Raw = string(raw)
	}
	return root, nil
}

// planDetails are the plan keys copied into PlanNode.Details, in order.
var planDetails = []string{
	"Index Cond", "Recheck Cond", "Hash Cond", "Merge Cond", "Join Filter", "Filter",
	"Rows Removed by Index Recheck", "Rows Removed by Join Filter", "Rows Removed by Filter",
	"Sort Key", "Sort Method", "Group Key", "Presorted Key", "Heap Fetches",
	"Workers Planned", "Workers Launched", "Actual Loops", "Output",
}

func planNode(p map[string]any, analyze bool) *db.PlanNode {
	n := &db.PlanNode{Title: planTitle(p), Cost: num(p, "Total Cost"), Rows: num(p, "Plan Rows")}
	if analyze {
		n.Rows = num(p, "Actual Rows")
		n.Time = num(p, "Actual Total Time") * num(p, "Actual Loops")
	}
	for _, k := range planDetails {
		v, ok := p[k]
		if !ok || (strings.HasPrefix(k, "Rows Removed") && v == 0.0) {
			continue
		}
		s := detailText(v)
		if k == "Output" && len(s) > maxOutputLen {
			s = s[:maxOutputLen] + "…"
		}
		n.Details = append(n.Details, db.KV{Key: k, Value: s})
	}
	if b := buffers(p); b != "" {
		n.Details = append(n.Details, db.KV{Key: "Buffers", Value: b})
	}
	children, _ := p["Plans"].([]any)
	for _, c := range children {
		if m, ok := c.(map[string]any); ok {
			n.Children = append(n.Children, planNode(m, analyze))
		}
	}
	return n
}

// planTitle mimics the node headline of EXPLAIN's text format.
func planTitle(p map[string]any) string {
	typ := str(p, "Node Type")
	switch {
	case typ == "Aggregate":
		switch str(p, "Strategy") {
		case "Hashed":
			typ = "HashAggregate"
		case "Sorted":
			typ = "GroupAggregate"
		case "Mixed":
			typ = "MixedAggregate"
		}
	case str(p, "Join Type") != "" && str(p, "Join Type") != "Inner":
		jt := str(p, "Join Type")
		if typ == "Nested Loop" {
			typ += " " + jt + " Join"
		} else {
			typ = strings.Replace(typ, " Join", " "+jt+" Join", 1)
		}
	}
	if pm := str(p, "Partial Mode"); pm != "" && pm != "Simple" {
		typ = pm + " " + typ
	}
	if str(p, "Scan Direction") == "Backward" {
		typ += " Backward"
	}
	if ix := str(p, "Index Name"); ix != "" {
		typ += " using " + ix
	}
	alias := str(p, "Alias")
	var target string
	switch {
	case str(p, "Relation Name") != "":
		target = str(p, "Relation Name")
		if sch := str(p, "Schema"); sch != "" {
			target = sch + "." + target
		}
		if alias == str(p, "Relation Name") {
			alias = ""
		}
	case str(p, "CTE Name") != "":
		target = str(p, "CTE Name")
		if alias == target {
			alias = ""
		}
	case str(p, "Function Name") != "":
		target = str(p, "Function Name")
		if alias == target {
			alias = ""
		}
	default:
		target, alias = alias, ""
	}
	if target != "" {
		typ += " on " + target
	}
	if alias != "" {
		typ += " " + alias
	}
	return typ
}

// buffers renders the buffer counters like "shared hit=3 read=1, temp written=8".
func buffers(p map[string]any) string {
	var parts []string
	for _, area := range []string{"Shared", "Local", "Temp"} {
		var ops []string
		for _, op := range []string{"Hit", "Read", "Dirtied", "Written"} {
			if v := num(p, area+" "+op+" Blocks"); v > 0 {
				ops = append(ops, strings.ToLower(op)+"="+strconv.FormatFloat(v, 'f', -1, 64))
			}
		}
		if len(ops) > 0 {
			parts = append(parts, strings.ToLower(area)+" "+strings.Join(ops, " "))
		}
	}
	return strings.Join(parts, ", ")
}

func detailText(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case []any:
		s := make([]string, len(v))
		for i, e := range v {
			s[i] = detailText(e)
		}
		return strings.Join(s, ", ")
	}
	return fmt.Sprint(v)
}

func str(p map[string]any, k string) string {
	s, _ := p[k].(string)
	return s
}

func num(p map[string]any, k string) float64 {
	f, _ := p[k].(float64)
	return f
}
