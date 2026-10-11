package sqltext

import (
	"strings"
	"unicode/utf8"

	"github.com/farvisun/squi/internal/db"
)

// Statement is one statement of a script.
type Statement struct {
	// SQL is the statement text without surrounding whitespace, leading or
	// trailing comments (server-interpreted hint comments are kept) and the
	// terminating delimiter. Comments inside the statement are kept.
	SQL string
	// Start and End are the byte offsets of SQL in the source.
	Start, End int
	// Line and Col are the 1-based position of Start (Col counts runes).
	Line, Col int

	delimEnd int // end offset of the terminating delimiter (End if none)
}

// Split splits a script into statements on ';' outside strings, comments and
// dollar quotes. For MySQL, client "DELIMITER x" lines switch the delimiter
// (the command itself is not a statement). For Postgres, SQL-standard
// routine bodies (CREATE FUNCTION … BEGIN ATOMIC … END) are kept whole.
// Statements consisting only of comments are dropped.
func Split(src string, d db.DriverName) []Statement {
	toks := Tokenize(src, d)
	var (
		out      []Statement
		begin    = 0 // first token of the current statement
		routine  = false
		depth    = 0 // BEGIN/CASE … END nesting inside a Postgres routine
		lead     []string
		mysqlDel = ";"
	)
	flush := func(endTok, delimEnd int) {
		if st, ok := makeStatement(src, toks[begin:endTok], delimEnd); ok {
			out = append(out, st)
		}
		begin, routine, depth, lead = endTok+1, false, 0, lead[:0]
	}
	for i, t := range toks {
		switch {
		case t.Kind == TokDelimiter:
			if nd, n := parseDelimiterCommand(t.Text); n > 0 {
				mysqlDel = nd
			}
			flush(i, t.End)
		case t.Kind == TokPunct && t.Text == ";" && depth == 0 && (d != db.MySQL || mysqlDel == ";"):
			flush(i, t.End)
		case t.IsWord() && d != db.MySQL:
			w := t.Upper()
			if len(lead) < 4 {
				lead = append(lead, w)
				routine = routine || isRoutineHead(lead)
			}
			if !routine {
				break
			}
			switch w {
			case "BEGIN", "CASE":
				depth++
			case "END":
				if depth > 0 {
					depth--
				}
			}
		}
	}
	if begin < len(toks) {
		flush(len(toks), -1)
	}
	return out
}

// isRoutineHead reports whether the leading words start CREATE [OR REPLACE]
// FUNCTION/PROCEDURE.
func isRoutineHead(w []string) bool {
	if len(w) < 2 || w[0] != "CREATE" {
		return false
	}
	if w[1] == "OR" {
		return len(w) >= 4 && (w[3] == "FUNCTION" || w[3] == "PROCEDURE")
	}
	return w[1] == "FUNCTION" || w[1] == "PROCEDURE"
}

// makeStatement trims trivia from both ends of toks; delimEnd < 0 means the
// statement is not terminated.
func makeStatement(src string, toks []Token, delimEnd int) (Statement, bool) {
	meaningful := func(t Token) bool { return !t.Trivia() || t.IsHint() }
	a, b := 0, len(toks)-1
	for a <= b && !meaningful(toks[a]) {
		a++
	}
	for b >= a && !meaningful(toks[b]) {
		b--
	}
	if a > b {
		return Statement{}, false
	}
	st := Statement{SQL: src[toks[a].Start:toks[b].End], Start: toks[a].Start, End: toks[b].End}
	st.Line, st.Col = LineCol(src, st.Start)
	st.delimEnd = delimEnd
	if delimEnd < 0 {
		st.delimEnd = st.End
	}
	return st, true
}

// StatementAt returns the statement under offset, like "execute statement
// at caret". An offset inside a statement or its delimiter selects it. In the
// gap between statements the previous statement is chosen, unless the next
// one starts on the cursor's line and the previous one ended on an earlier
// line; before the first statement, the first is chosen.
func StatementAt(src string, offset int, d db.DriverName) (Statement, bool) {
	stmts := Split(src, d)
	if len(stmts) == 0 {
		return Statement{}, false
	}
	offset = clamp(offset, 0, len(src))
	var prev *Statement
	for i := range stmts {
		st := &stmts[i]
		if offset >= st.Start && offset <= st.delimEnd {
			return *st, true
		}
		if st.Start > offset {
			line, _ := LineCol(src, offset)
			if prev == nil {
				return *st, true
			}
			prevLine, _ := LineCol(src, prev.delimEnd)
			if st.Line == line && prevLine < line {
				return *st, true
			}
			return *prev, true
		}
		prev = st
	}
	return *prev, true
}

// LineCol converts a byte offset to a 1-based line and rune column.
func LineCol(src string, offset int) (line, col int) {
	offset = clamp(offset, 0, len(src))
	head := src[:offset]
	ls := strings.LastIndexByte(head, '\n') + 1
	return strings.Count(head, "\n") + 1, utf8.RuneCountInString(head[ls:]) + 1
}

// OffsetOf converts a 1-based line and rune column to a byte offset, clamping
// to the line's end and to the bounds of src.
func OffsetOf(src string, line, col int) int {
	off := 0
	for ; line > 1; line-- {
		i := strings.IndexByte(src[off:], '\n')
		if i < 0 {
			return len(src)
		}
		off += i + 1
	}
	for ; col > 1 && off < len(src) && src[off] != '\n'; col-- {
		_, n := utf8.DecodeRuneInString(src[off:])
		off += n
	}
	return off
}

func clamp(v, lo, hi int) int { return max(lo, min(v, hi)) }
