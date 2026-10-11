package schema

import (
	"github.com/farvisun/squi/internal/sqltext"
)

// tableRef is a table referenced in a FROM/JOIN/UPDATE/INTO clause.
type tableRef struct {
	qual  string // schema (Postgres) or database (MySQL); "" if unqualified
	name  string // "" for a derived table (subquery)
	alias string
}

// notNames are keywords that end a table reference instead of naming one.
var notNames = map[string]bool{
	"SELECT": true, "SET": true, "WHERE": true, "VALUES": true, "ON": true, "USING": true, "JOIN": true,
	"LEFT": true, "RIGHT": true, "INNER": true, "OUTER": true, "FULL": true, "CROSS": true,
	"NATURAL": true, "GROUP": true, "ORDER": true, "HAVING": true, "LIMIT": true, "UNION": true,
	"DEFAULT": true, "AS": true, "WITH": true, "RETURNING": true, "FROM": true, "INTO": true,
	"TABLE": true, "WINDOW": true, "OFFSET": true, "FOR": true, "EXCEPT": true, "INTERSECT": true,
}

// isName reports whether t can be an identifier in a table reference.
func isName(t sqltext.Token) bool {
	return t.Kind == sqltext.TokIdent || t.Kind == sqltext.TokQuotedIdent ||
		(t.Kind == sqltext.TokKeyword && !notNames[t.Upper()])
}

// identName returns the identifier denoted by t, unquoting quoted names.
func identName(t sqltext.Token) string {
	if t.Kind != sqltext.TokQuotedIdent {
		return t.Text
	}
	return sqltext.UnquoteIdent(t.Text)
}

// parseRefs collects the table references of a statement's significant
// tokens.
func parseRefs(ts []sqltext.Token) []tableRef {
	var refs []tableRef
	for i := 0; i < len(ts); i++ {
		switch w := ts[i].Upper(); w {
		case "FROM", "JOIN", "UPDATE", "INTO", "TABLE", "TRUNCATE", "USING":
			if w == "USING" && i+1 < len(ts) && ts[i+1].Text == "(" {
				continue // JOIN … USING (col)
			}
			list := w == "FROM" || w == "UPDATE" || w == "USING"
			for j := i + 1; ; {
				ref, next, ok := parseRef(ts, j)
				if !ok {
					break
				}
				refs = append(refs, ref)
				i = next - 1
				if !list || next >= len(ts) || ts[next].Text != "," {
					break
				}
				j = next + 1
			}
		}
	}
	return refs
}

// parseRef parses "[ONLY|LATERAL] name[.name[.name]] [AS] [alias]" or
// "(subquery) [AS] alias" at ts[j]. next is the index after the reference.
func parseRef(ts []sqltext.Token, j int) (ref tableRef, next int, ok bool) {
	for j < len(ts) {
		switch ts[j].Upper() {
		case "ONLY", "LATERAL", "IF", "NOT", "EXISTS", "IGNORE", "TABLE":
			j++
			continue
		}
		break
	}
	if j >= len(ts) {
		return ref, j, false
	}
	switch {
	case ts[j].Text == "(":
		j = matchParen(ts, j) + 1
	case isName(ts[j]):
		parts := []string{identName(ts[j])}
		j++
		for j+1 < len(ts) && ts[j].Text == "." && isName(ts[j+1]) && len(parts) < 3 {
			parts = append(parts, identName(ts[j+1]))
			j += 2
		}
		ref.name = parts[len(parts)-1]
		if len(parts) > 1 {
			ref.qual = parts[len(parts)-2]
		}
		if j < len(ts) && ts[j].Text == "(" { // table function call
			j = matchParen(ts, j) + 1
		}
	default:
		return ref, j, false
	}
	if j < len(ts) && ts[j].Upper() == "AS" {
		j++
	}
	if j < len(ts) && (ts[j].Kind == sqltext.TokIdent || ts[j].Kind == sqltext.TokQuotedIdent) {
		ref.alias = identName(ts[j])
		j++
		if j < len(ts) && ts[j].Text == "(" { // column alias list
			j = matchParen(ts, j) + 1
		}
	}
	return ref, j, ref.name != "" || ref.alias != ""
}

func matchParen(ts []sqltext.Token, i int) int {
	depth := 0
	for j := i; j < len(ts); j++ {
		switch ts[j].Text {
		case "(":
			depth++
		case ")":
			if depth--; depth == 0 {
				return j
			}
		}
	}
	return len(ts) - 1
}
