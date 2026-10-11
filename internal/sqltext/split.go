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
// routine bodies (CREATE FUNCTION … BEGIN ATOMIC … END) are kept whole;
// for SQLite, trigger bodies (CREATE TRIGGER … BEGIN … END) are.
// For SQL Server, GO lines separate batches, BEGIN … END blocks are kept
// whole, and a batch starting with DECLARE or CREATE PROCEDURE / FUNCTION /
// TRIGGER is not split on ';' (variables and routine bodies span the batch).
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
		mssql    = d == db.SQLServer
		batch    = false // SQL Server: up to the next GO
	)
	flush := func(endTok, delimEnd int) {
		if st, ok := makeStatement(src, toks[begin:endTok], delimEnd); ok {
			out = append(out, st)
		}
		begin, routine, batch, depth, lead = endTok+1, false, false, 0, lead[:0]
	}
	for i, t := range toks {
		switch {
		case t.Kind == TokDelimiter:
			if nd, n := parseDelimiterCommand(t.Text); n > 0 {
				mysqlDel = nd
			}
			flush(i, t.End)
		case t.Kind == TokPunct && t.Text == ";" && depth == 0 && !batch && (d != db.MySQL || mysqlDel == ";"):
			flush(i, t.End)
		case t.IsWord() && mssql:
			w := t.Upper()
			if len(lead) < 4 {
				lead = append(lead, w)
				batch = batch || lead[0] == "DECLARE" || isTSQLRoutineHead(lead)
			}
			switch w {
			case "BEGIN":
				switch nextWord(toks, i) {
				case "TRAN", "TRANSACTION", "DISTRIBUTED", "DIALOG", "CONVERSATION":
				default:
					depth++
				}
			case "CASE":
				depth++
			case "END":
				if depth > 0 {
					depth--
				}
			}
		case t.IsWord() && d != db.MySQL:
			w := t.Upper()
			if len(lead) < 4 {
				lead = append(lead, w)
				routine = routine || isRoutineHead(lead, d)
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
// FUNCTION/PROCEDURE, or for SQLite CREATE [TEMP] TRIGGER.
func isRoutineHead(w []string, d db.DriverName) bool {
	if len(w) < 2 || w[0] != "CREATE" {
		return false
	}
	if d == db.SQLite {
		return w[1] == "TRIGGER" || len(w) >= 3 && (w[1] == "TEMP" || w[1] == "TEMPORARY") && w[2] == "TRIGGER"
	}
	if w[1] == "OR" {
		return len(w) >= 4 && (w[3] == "FUNCTION" || w[3] == "PROCEDURE")
	}
	return w[1] == "FUNCTION" || w[1] == "PROCEDURE"
}

// isTSQLRoutineHead reports whether the leading words start CREATE [OR
// ALTER] / ALTER PROC[EDURE] / FUNCTION / TRIGGER, whose body runs to the end
// of the batch.
func isTSQLRoutineHead(w []string) bool {
	if len(w) < 2 || (w[0] != "CREATE" && w[0] != "ALTER") {
		return false
	}
	obj := w[1]
	if w[0] == "CREATE" && w[1] == "OR" {
		if len(w) < 4 {
			return false
		}
		obj = w[3]
	}
	switch obj {
	case "PROC", "PROCEDURE", "FUNCTION", "TRIGGER":
		return true
	}
	return false
}

// nextWord returns the upper-cased next significant word after toks[i].
func nextWord(toks []Token, i int) string {
	for j := i + 1; j < len(toks); j++ {
		if toks[j].Trivia() {
			continue
		}
		if toks[j].IsWord() {
			return toks[j].Upper()
		}
		return ""
	}
	return ""
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
