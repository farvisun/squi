// Package sqlfmt pretty-prints a single SQL statement: one major clause
// per line, select lists, SET lists and AND/OR conditions broken over lines,
// and subqueries indented. It is deliberately conservative:
//
//   - only queries and DML (SELECT, WITH, INSERT, UPDATE, DELETE, VALUES,
//     REPLACE) are reflowed; any other statement is returned unchanged;
//   - string literals, quoted identifiers and comments are never altered;
//   - only well-known reserved words are upper-cased, so identifiers that
//     happen to be keywords keep their case (ClickHouse names are
//     case-sensitive);
//   - parentheses that are not subqueries (function calls, IN lists) stay
//     on one line.
//
// The output contains exactly the input's tokens in the same order; only
// the whitespace between them and the case of reserved words change.
package sqlfmt

import (
	"strings"

	"github.com/farvisun/squi/internal/db"
	"github.com/farvisun/squi/internal/sqltext"
)

const indentUnit = "  "

// reserved are upper-cased when lexed as keywords.
var reserved = set(
	"ALL", "AND", "ANY", "AS", "ASC", "BETWEEN", "BY", "CASE", "CAST", "CROSS", "DELETE", "DESC",
	"DISTINCT", "ELSE", "END", "EXCEPT", "EXISTS", "FALSE", "FOR", "FROM", "FULL", "GROUP", "HAVING",
	"ILIKE", "IN", "INNER", "INSERT", "INTERSECT", "INTO", "IS", "JOIN", "LATERAL", "LEFT", "LIKE",
	"LIMIT", "NATURAL", "NOT", "NULL", "OFFSET", "ON", "OR", "ORDER", "OUTER", "PREWHERE", "RETURNING",
	"RIGHT", "SELECT", "SET", "SOME", "THEN", "TRUE", "UNION", "UPDATE", "USING", "VALUES", "WHEN",
	"WHERE", "WITH", "REPLACE", "DO", "CONFLICT", "NOTHING", "FORMAT", "SETTINGS", "FINAL", "SAMPLE",
	"TOP", "OUTPUT", "FETCH", "NEXT", "ROWS", "ONLY", "PERCENT", "TIES", "APPLY",
)

// clauses start a new line at the statement's (or subquery's) indent.
var clauses = set(
	"SELECT", "FROM", "WHERE", "PREWHERE", "GROUP", "ORDER", "HAVING", "LIMIT", "OFFSET", "UNION",
	"INTERSECT", "EXCEPT", "WITH", "INSERT", "REPLACE", "UPDATE", "DELETE", "SET", "VALUES",
	"RETURNING", "WINDOW", "QUALIFY", "SETTINGS", "FORMAT",
)

// joinWords may start a JOIN clause.
var joinWords = set("JOIN", "LEFT", "RIGHT", "INNER", "FULL", "CROSS", "NATURAL", "STRAIGHT_JOIN", "ARRAY", "GLOBAL", "ASOF", "ANY", "SEMI", "ANTI", "PASTE")

// spaceBeforeParen are words followed by " (" rather than a call's "(".
var spaceBeforeParen = set(
	"IN", "AS", "EXISTS", "AND", "OR", "NOT", "ON", "USING", "OVER", "ANY", "ALL", "SOME", "VALUES",
	"INTO", "BETWEEN", "WHEN", "THEN", "ELSE", "CASE", "RETURNING", "SET", "LATERAL", "UNION",
	"INTERSECT", "EXCEPT", "IS", "LIKE", "ILIKE", "SELECT", "FROM", "WHERE", "JOIN", "WITH", "HAVING",
	"BY", "TABLE", "DISTINCT", "CONFLICT", "TOP",
)

var formattable = set("SELECT", "WITH", "INSERT", "UPDATE", "DELETE", "VALUES", "REPLACE")

func set(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

// frame is a statement or subquery being laid out.
type frame struct {
	indent  int    // indent of clause keywords
	clause  string // current clause: SELECT, WHERE, SET…
	listNL  bool   // break after commas of the current clause
	condNL  bool   // break before AND / OR of the current clause
	between bool   // a BETWEEN waits for its AND
	sub     bool   // opened by "(" (closes with ")")
	parens  int    // non-subquery parentheses open inside this frame
	cte     bool   // WITH list: break after commas between CTEs
}

// Format pretty-prints one statement (without its terminating ';').
func Format(stmt string, d db.DriverName) string {
	toks := sqltext.Tokenize(stmt, d)
	var sig []sqltext.Token
	for _, t := range toks {
		if t.Kind != sqltext.TokWhitespace {
			sig = append(sig, t)
		}
	}
	first := ""
	for _, t := range sig {
		if t.Kind == sqltext.TokComment {
			continue
		}
		if t.Kind == sqltext.TokPunct && t.Text == "(" {
			continue
		}
		first = t.Upper()
		break
	}
	if !formattable[first] {
		return strings.TrimSpace(stmt)
	}
	f := &formatter{sig: sig, stack: []*frame{{}}}
	for i := range sig {
		f.token(i)
	}
	return strings.TrimRight(f.b.String(), " \n")
}

type formatter struct {
	sig   []sqltext.Token
	stack []*frame
	b     strings.Builder
	// newline is pending: the next token starts a line at this indent
	nl       bool
	nlIndent int
	started  bool
}

func (f *formatter) top() *frame { return f.stack[len(f.stack)-1] }

func (f *formatter) word(i int) string {
	if i < 0 || i >= len(f.sig) {
		return ""
	}
	t := f.sig[i]
	if t.Kind == sqltext.TokComment {
		return ""
	}
	return t.Upper()
}

// prevWord is the previous non-comment token's upper-cased text.
func (f *formatter) prevWord(i int) string {
	for j := i - 1; j >= 0; j-- {
		if f.sig[j].Kind != sqltext.TokComment {
			return f.sig[j].Upper()
		}
	}
	return ""
}

func (f *formatter) prevTok(i int) (sqltext.Token, bool) {
	for j := i - 1; j >= 0; j-- {
		if f.sig[j].Kind != sqltext.TokComment {
			return f.sig[j], true
		}
	}
	return sqltext.Token{}, false
}

func (f *formatter) breakLine(indent int) {
	if !f.started {
		return
	}
	f.nl, f.nlIndent = true, indent
}

func (f *formatter) emit(text string, space bool) {
	switch {
	case !f.started:
	case f.nl:
		f.b.WriteString("\n" + strings.Repeat(indentUnit, f.nlIndent))
	case space:
		f.b.WriteByte(' ')
	}
	f.nl = false
	f.started = true
	f.b.WriteString(text)
}

// clauseAt reports whether token i starts a clause at the current level.
func (f *formatter) clauseAt(i int) bool {
	t := f.sig[i]
	if !t.IsWord() || f.top().parens > 0 {
		return false
	}
	w, prev := t.Upper(), f.prevWord(i)
	switch {
	case joinWords[w]:
		// LEFT [OUTER] JOIN, ARRAY JOIN, GLOBAL ANY LEFT JOIN…: break at the
		// first word of the run that ends in JOIN
		if joinWords[prev] || prev == "OUTER" {
			return false
		}
		for j := i; j < len(f.sig) && j < i+5; j++ {
			switch u := f.word(j); {
			case u == "JOIN" || u == "STRAIGHT_JOIN":
				return true
			case joinWords[u] || u == "OUTER":
				continue
			}
			return false
		}
		return false
	case !clauses[w]:
		return false
	}
	switch w {
	case "FROM":
		return prev != "DELETE" && prev != "DISTINCT" // IS DISTINCT FROM
	case "UPDATE":
		return prev != "FOR" && prev != "KEY" && prev != "DO" && prev != "SHARE"
	case "SET":
		return prev != "UPDATE" || f.top().clause == "INSERT" // ON CONFLICT DO UPDATE SET, ON DUPLICATE KEY UPDATE
	case "REPLACE":
		return prev == "" || prev == "("
	case "WITH":
		return prev == "" || prev == "("
	case "ORDER", "GROUP":
		return f.word(i+1) == "BY"
	case "INSERT":
		return prev == "" || prev == "("
	case "DELETE":
		return prev == "" || prev == "("
	case "FORMAT", "SETTINGS":
		return prev != "" // ClickHouse trailing clauses
	}
	return true
}

// hasTopComma reports whether the clause starting at token i lists several
// items: a comma before the next clause at the same level.
func (f *formatter) hasTopComma(i int) bool {
	depth := 0
	for j := i + 1; j < len(f.sig); j++ {
		t := f.sig[j]
		switch {
		case t.Kind == sqltext.TokPunct && t.Text == "(":
			depth++
		case t.Kind == sqltext.TokPunct && t.Text == ")":
			if depth--; depth < 0 {
				return false
			}
		case depth == 0 && t.Kind == sqltext.TokPunct && t.Text == ",":
			return true
		case depth == 0 && t.IsWord() && clauses[t.Upper()] && t.Upper() != "SET":
			return false
		}
	}
	return false
}

// isSubquery reports whether "(" at i opens a query.
func (f *formatter) isSubquery(i int) bool {
	for j := i + 1; j < len(f.sig); j++ {
		t := f.sig[j]
		if t.Kind == sqltext.TokComment || t.Kind == sqltext.TokPunct && t.Text == "(" {
			continue
		}
		u := t.Upper()
		return u == "SELECT" || u == "WITH" || u == "VALUES" && f.prevWord(i) != "INTO" && f.prevWord(i) != ")"
	}
	return false
}

// unary reports whether an operator at i is a sign rather than binary.
func (f *formatter) unary(i int) bool {
	p, ok := f.prevTok(i)
	if !ok {
		return true
	}
	switch p.Kind {
	case sqltext.TokOperator:
		return true
	case sqltext.TokPunct:
		return p.Text == "(" || p.Text == "," || p.Text == "["
	case sqltext.TokKeyword:
		u := p.Upper()
		return u != "NULL" && u != "TRUE" && u != "FALSE" && u != "END"
	}
	return false
}

func (f *formatter) token(i int) {
	t := f.sig[i]
	fr := f.top()
	text := t.Text
	if t.Kind == sqltext.TokKeyword && reserved[t.Upper()] {
		text = t.Upper()
	}
	prev, hasPrev := f.prevTok(i)
	space := true
	if hasPrev {
		switch {
		case prev.Kind == sqltext.TokPunct && (prev.Text == "(" || prev.Text == "." || prev.Text == "["):
			space = false
		case prev.Kind == sqltext.TokOperator && (prev.Text == "::" || f.unaryOp(prev, i)):
			space = false
		}
	}
	if f.lastWasComment() {
		space = true
	}

	switch {
	case t.Kind == sqltext.TokComment:
		f.emit(text, true)
		if strings.HasPrefix(text, "--") || strings.HasPrefix(text, "#") {
			f.breakLine(fr.indent + f.contIndent())
		}
		return
	case t.Kind == sqltext.TokPunct && t.Text == "(":
		pw := f.prevWord(i)
		space = space && hasPrev && (prev.Kind == sqltext.TokOperator || prev.Kind == sqltext.TokPunct && prev.Text == "," ||
			prev.Kind == sqltext.TokKeyword && spaceBeforeParen[pw] || prev.Kind == sqltext.TokPunct && prev.Text == ")" ||
			fr.parens == 0 && (fr.clause == "INSERT" || fr.clause == "REPLACE")) // INSERT INTO t (a, b)
		if f.isSubquery(i) {
			f.emit("(", space)
			inner := &frame{indent: fr.indent + f.contIndent() + 1, sub: true}
			f.stack = append(f.stack, inner)
			f.breakLine(inner.indent)
			return
		}
		fr.parens++
		f.emit("(", space)
		return
	case t.Kind == sqltext.TokPunct && t.Text == ")":
		if fr.parens > 0 {
			fr.parens--
			f.emit(")", false)
			return
		}
		if fr.sub && len(f.stack) > 1 {
			f.stack = f.stack[:len(f.stack)-1]
			outer := f.top()
			f.breakLine(outer.indent + f.contIndent())
			f.emit(")", false)
			return
		}
		f.emit(")", false)
		return
	case t.Kind == sqltext.TokPunct && (t.Text == "," || t.Text == ";"):
		lineStart := f.nl // a leading comma after a line comment
		f.emit(t.Text, false)
		if t.Text == "," && fr.parens == 0 && !lineStart {
			switch {
			case fr.cte:
				f.breakLine(fr.indent)
			case fr.listNL:
				f.breakLine(fr.indent + 1)
			}
		}
		return
	case t.Kind == sqltext.TokPunct && (t.Text == "." || t.Text == "[" || t.Text == "]"):
		f.emit(text, false)
		return
	case t.Kind == sqltext.TokOperator && t.Text == "::":
		f.emit(text, false)
		return
	}

	if f.clauseAt(i) {
		w := t.Upper()
		f.breakLine(fr.indent)
		f.emit(text, space)
		switch {
		case w == "WITH":
			fr.clause, fr.cte, fr.listNL, fr.condNL = w, true, false, false
		case joinWords[w]:
			fr.clause, fr.listNL, fr.condNL, fr.cte = "JOIN", false, true, false
		default:
			fr.clause, fr.cte = w, false
			fr.condNL = w == "WHERE" || w == "HAVING" || w == "PREWHERE" || w == "QUALIFY"
			fr.listNL = (w == "SELECT" || w == "SET" || w == "VALUES") && f.hasTopComma(i)
			if fr.listNL && w != "VALUES" {
				// first item on its own line too
				f.listStart(i)
			}
		}
		return
	}
	if t.IsWord() && fr.parens == 0 {
		switch w := t.Upper(); {
		case w == "BETWEEN":
			fr.between = true
		case (w == "AND" || w == "OR") && fr.condNL:
			if w == "AND" && fr.between {
				fr.between = false
				break
			}
			f.breakLine(fr.indent + 1)
			f.emit(text, space)
			return
		}
	}
	f.emit(text, space)
}

// listStart moves the first item of a multi-item SELECT/SET list onto its
// own line (after SELECT DISTINCT / SELECT TOP n stays on the keyword line).
func (f *formatter) listStart(i int) {
	if n := f.word(i + 1); n == "DISTINCT" || n == "ALL" || n == "TOP" {
		return
	}
	f.breakLine(f.top().indent + 1)
}

// contIndent is the extra indent of continuation lines inside a clause
// (none in a WITH list, whose CTE bodies sit one level in).
func (f *formatter) contIndent() int {
	if c := f.top().clause; c == "" || c == "WITH" {
		return 0
	}
	return 1
}

func (f *formatter) lastWasComment() bool {
	s := f.b.String()
	return strings.HasSuffix(s, "*/")
}

// unaryOp reports whether prev (the token before i) is a sign.
func (f *formatter) unaryOp(prev sqltext.Token, i int) bool {
	if prev.Text != "-" && prev.Text != "+" {
		return false
	}
	for j := i - 1; j >= 0; j-- {
		if f.sig[j].Start == prev.Start {
			return f.unary(j)
		}
	}
	return false
}

// FormatScript formats every statement of src that overlaps [from, to) and
// returns the new text; everything outside those statements is kept.
func FormatScript(src string, from, to int, d db.DriverName) string {
	stmts := sqltext.Split(src, d)
	var b strings.Builder
	last := 0
	for _, st := range stmts {
		if st.End <= from || st.Start >= to {
			continue
		}
		b.WriteString(src[last:st.Start])
		b.WriteString(Format(st.SQL, d))
		last = st.End
	}
	b.WriteString(src[last:])
	return b.String()
}
