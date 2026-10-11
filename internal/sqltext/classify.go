package sqltext

import (
	"strings"

	"github.com/farvisun/squi/internal/db"
)

// Kind is the broad category of a statement.
type Kind int

const (
	KindOther   Kind = iota
	KindRead         // SELECT, SHOW, EXPLAIN, …
	KindWrite        // DML and other data-modifying statements
	KindDDL          // schema changes, privileges, maintenance
	KindTx           // transaction control
	KindSession      // SET, USE, RESET
)

var kindNames = [...]string{"other", "read", "write", "ddl", "tx", "session"}

func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "unknown"
}

// TxOp is the transaction effect of a statement.
type TxOp int

const (
	TxNone TxOp = iota
	TxBegin
	TxCommit
	TxRollback
)

// Class is the result of Classify.
type Class struct {
	Kind        Kind
	TxOp        TxOp
	Destructive bool   // likely to lose data; warrants confirmation
	Reason      string // why the statement is destructive or a write, if not obvious
	Keyword     string // main verb, upper case (e.g. "SELECT", "DELETE")

	write bool // modifies state although Kind is not Write/DDL
}

// IsWrite reports whether the statement may modify data, schema or the
// session's read-only state. Read-only mode blocks these.
func (c Class) IsWrite() bool { return c.Kind == KindWrite || c.Kind == KindDDL || c.write }

// Classify categorizes a single statement. Leading comments and parentheses
// are skipped and MySQL versioned comments (/*!…*/) are treated as code.
func Classify(stmt string, d db.DriverName) Class {
	ts := significant(Tokenize(stmt, d), d)
	if d == db.SQLServer {
		return classifyBatch(ts)
	}
	return classify(ts, d)
}

// significant drops whitespace and comments, expanding MySQL /*!NNNNN … */
// comments into their tokens, and drops a trailing ';'.
func significant(toks []Token, d db.DriverName) []Token {
	var out []Token
	for _, t := range toks {
		switch {
		case d == db.MySQL && t.Kind == TokComment && strings.HasPrefix(t.Text, "/*!"):
			inner := strings.TrimSuffix(t.Text[3:], "*/")
			inner = strings.TrimLeft(inner, "0123456789")
			out = append(out, significant(Tokenize(inner, d), d)...)
		case !t.Trivia() && t.Kind != TokDelimiter:
			out = append(out, t)
		}
	}
	for len(out) > 0 && out[len(out)-1].Text == ";" {
		out = out[:len(out)-1]
	}
	return out
}

func word(ts []Token, i int) string {
	if i < len(ts) && ts[i].IsWord() {
		return ts[i].Upper()
	}
	return ""
}

func classify(ts []Token, d db.DriverName) Class {
	for len(ts) > 0 && ts[0].Text == "(" {
		ts = ts[1:]
	}
	if len(ts) == 0 {
		return Class{}
	}
	kw := word(ts, 0)
	c := Class{Keyword: kw}
	switch kw {
	case "WITH":
		return classifyWith(ts, d)
	case "SELECT":
		c.Kind = KindRead
		if i := topLevel(ts, "INTO"); i >= 0 {
			switch {
			case !d.MySQLStyle():
				c.Kind, c.Reason = KindWrite, "SELECT INTO creates a table"
			case word(ts, i+1) == "OUTFILE" || word(ts, i+1) == "DUMPFILE":
				c.Kind, c.Reason = KindWrite, "SELECT INTO OUTFILE writes a file on the server"
			}
		}
	case "SHOW", "DESCRIBE", "DESC", "VALUES", "TABLE", "DECLARE", "FETCH", "MOVE", "CLOSE", "HANDLER", "CHECKSUM", "HELP":
		c.Kind = KindRead
	case "PRINT", "WAITFOR", "OPEN", "DEALLOCATE", "RAISERROR", "THROW":
		if d == db.SQLServer {
			c.Kind = KindRead
		}
	case "KILL":
		if d == db.SQLServer {
			c.Kind, c.write, c.Destructive, c.Reason = KindOther, true, true, "KILL ends a session and rolls back its transaction"
		}
	case "DBCC":
		if d != db.SQLServer {
			break
		}
		c.Kind = KindRead
		switch word(ts, 1) {
		case "SHOW_STATISTICS", "INPUTBUFFER", "OUTPUTBUFFER", "OPENTRAN", "SQLPERF", "USEROPTIONS", "TRACESTATUS",
			"CHECKDB", "CHECKTABLE", "CHECKALLOC", "CHECKCATALOG", "SHOWCONTIG", "HELP":
			if topLevelAny(ts, "REPAIR_ALLOW_DATA_LOSS", "REPAIR_REBUILD") < 0 {
				break
			}
			fallthrough
		default:
			c.Kind, c.write, c.Reason = KindOther, true, "DBCC "+word(ts, 1)+" may change server state"
		}
	case "BULK":
		if d == db.SQLServer {
			c.Kind = KindWrite
		}
	case "DENY", "SHUTDOWN":
		if d == db.SQLServer {
			c.Kind = KindDDL
			if kw == "SHUTDOWN" {
				c.Destructive, c.Reason = true, "SHUTDOWN stops the server"
			}
		}
	case "EXISTS", "CHECK", "WATCH":
		if d == db.ClickHouse {
			c.Kind = KindRead
		}
	case "EXPLAIN":
		return classifyExplain(ts, d)
	case "INSERT", "REPLACE", "UPSERT", "MERGE":
		c.Kind = KindWrite
	case "UPDATE", "DELETE":
		c.Kind = KindWrite
		c.Destructive, c.Reason = unfiltered(ts, kw)
	case "COPY":
		c.Kind = KindOther
		if topLevel(ts, "FROM") >= 0 {
			c.Kind = KindWrite
		} else if topLevel(ts, "TO") >= 0 {
			c.Kind = KindRead
		}
	case "LOAD":
		if w := word(ts, 1); w == "DATA" || w == "XML" {
			c.Kind = KindWrite
		}
	case "CALL":
		c.Kind, c.Reason = KindWrite, "procedure calls may modify data"
	case "DO":
		c.Kind, c.Reason = KindWrite, "anonymous code blocks may modify data"
	case "LOCK":
		c.Kind = KindWrite
	case "EXECUTE", "EXEC":
		c.Kind, c.write, c.Reason = KindOther, true, "prepared statements may modify data"
	case "DROP":
		c.Kind, c.Destructive, c.Reason = KindDDL, true, "DROP "+dropTarget(ts)+" cannot be undone"
	case "TRUNCATE":
		c.Kind, c.Destructive, c.Reason = KindDDL, true, "TRUNCATE removes all rows"
	case "ALTER":
		c.Kind = KindDDL
		c.Destructive, c.Reason = alterDrops(ts)
		if d == db.ClickHouse && !c.Destructive {
			// mutations: ALTER TABLE t DELETE / UPDATE … WHERE cond
			if i := topLevelAny(ts, "DELETE", "UPDATE"); i >= 0 {
				if bad, why := unfiltered(ts[i:], word(ts, i)); bad {
					c.Destructive, c.Reason = true, "ALTER TABLE … "+why
				}
			}
		}
	case "CREATE", "RENAME", "COMMENT", "GRANT", "REVOKE", "REINDEX", "VACUUM", "ANALYZE", "CLUSTER",
		"REFRESH", "OPTIMIZE", "REPAIR", "SECURITY", "IMPORT", "REASSIGN", "INSTALL", "UNINSTALL":
		c.Kind = KindDDL
	case "PRAGMA":
		if d == db.SQLite {
			c.Kind = KindRead
			switch {
			case hasPunct(ts, "="):
				c.Kind, c.write, c.Reason = KindSession, true, "PRAGMA assignments change the database or session"
			case sqliteWritePragmas[pragmaName(ts)]:
				c.Kind = KindDDL
			}
		}
	case "SYSTEM", "ATTACH", "DETACH", "EXCHANGE", "UNDROP", "BACKUP", "RESTORE":
		if d == db.SQLite && (kw == "ATTACH" || kw == "DETACH") {
			c.Kind = KindSession
		}
		if d == db.ClickHouse {
			c.Kind = KindDDL
		}
		if d == db.SQLServer && kw == "BACKUP" {
			c.Kind = KindDDL
		}
		if d == db.SQLServer && kw == "RESTORE" {
			switch word(ts, 1) {
			case "DATABASE", "LOG":
				c.Kind, c.Destructive, c.Reason = KindDDL, true, "RESTORE overwrites the database"
			default: // FILELISTONLY, HEADERONLY, VERIFYONLY, LABELONLY
				c.Kind = KindRead
			}
		}
	case "BEGIN":
		if d == db.SQLServer {
			switch word(ts, 1) {
			case "TRAN", "TRANSACTION", "DISTRIBUTED":
				c.Kind, c.TxOp = KindTx, TxBegin
			}
			break // BEGIN … END and BEGIN TRY blocks: their statements are classified one by one
		}
		c.Kind, c.TxOp = KindTx, TxBegin
		c.markReadWrite(ts)
	case "START":
		if word(ts, 1) == "TRANSACTION" {
			c.Kind, c.TxOp, c.Keyword = KindTx, TxBegin, "START TRANSACTION"
			c.markReadWrite(ts)
		}
	case "COMMIT", "END":
		if kw == "END" && (d.MySQLStyle() || d == db.SQLServer) {
			break
		}
		c.Kind, c.TxOp = KindTx, TxCommit
		if word(ts, 1) == "PREPARED" {
			c.TxOp = TxNone
		}
	case "ROLLBACK", "ABORT":
		c.Kind, c.TxOp = KindTx, TxRollback
		if word(ts, 1) == "PREPARED" || word(ts, 1) == "TO" || word(ts, 2) == "TO" {
			c.TxOp = TxNone
		}
	case "SAVEPOINT", "RELEASE", "XA":
		c.Kind = KindTx
	case "SAVE":
		if d == db.SQLServer {
			c.Kind = KindTx
		}
	case "PREPARE":
		if word(ts, 1) == "TRANSACTION" {
			c.Kind = KindTx
		}
	case "SET":
		c.Kind = KindSession
		c.markReadWrite(ts)
	case "USE", "RESET", "DISCARD":
		c.Kind = KindSession
	case "IF", "ELSE", "WHILE", "RETURN", "BREAK", "CONTINUE", "GOTO":
		// SQL Server control flow; the statements it guards are classified separately
	default:
		if d == db.SQLServer && (ts[0].Kind == TokIdent || ts[0].Kind == TokQuotedIdent) && !strings.HasPrefix(ts[0].Text, "@") {
			// a batch may start with a bare procedure name: sp_who2 'active'
			c.Kind, c.write, c.Reason = KindOther, true, "procedure calls may modify data"
		}
	}
	if d == db.SQLServer && (kw == "EXEC" || kw == "EXECUTE") {
		c.Reason = "procedure calls may modify data"
	}
	return c
}

// classifyBatch classifies a SQL Server batch, where statements need no
// ';' (DECLARE @n int SELECT … DELETE …): each statement is classified and
// the most consequential class wins.
func classifyBatch(ts []Token) Class {
	segs := tsqlStatements(ts)
	if len(segs) == 1 {
		return classify(segs[0], db.SQLServer)
	}
	var out Class
	for i, seg := range segs {
		c := classify(seg, db.SQLServer)
		if i == 0 {
			out = c
			continue
		}
		if c.Destructive && !out.Destructive {
			out.Destructive, out.Reason, out.Keyword = true, c.Reason, c.Keyword
		}
		if rank(c) > rank(out) {
			out.Kind, out.write = c.Kind, c.write
			if !out.Destructive {
				out.Reason, out.Keyword = c.Reason, c.Keyword
			}
		}
		if c.TxOp != TxNone {
			out.TxOp = c.TxOp
		}
	}
	return out
}

// rank orders classes by consequence for classifyBatch.
func rank(c Class) int {
	switch {
	case c.Kind == KindDDL:
		return 4
	case c.Kind == KindWrite:
		return 3
	case c.write:
		return 2
	case c.Kind == KindRead || c.Kind == KindSession || c.Kind == KindTx:
		return 1
	}
	return 0
}

// tsqlVerbs start a statement in a SQL Server batch.
var tsqlVerbs = map[string]bool{
	"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true, "MERGE": true, "DROP": true,
	"TRUNCATE": true, "ALTER": true, "CREATE": true, "EXEC": true, "EXECUTE": true, "GRANT": true,
	"REVOKE": true, "DENY": true, "DECLARE": true, "SET": true, "USE": true, "BEGIN": true,
	"COMMIT": true, "ROLLBACK": true, "SAVE": true, "IF": true, "ELSE": true, "WHILE": true,
	"PRINT": true, "RETURN": true, "THROW": true, "RAISERROR": true, "WAITFOR": true, "KILL": true,
	"DBCC": true, "BULK": true, "BACKUP": true, "RESTORE": true, "END": true, "OPEN": true,
	"FETCH": true, "CLOSE": true, "DEALLOCATE": true, "WITH": true, "SHUTDOWN": true,
}

// tsqlStatements splits a batch into statements: at ';' and, since T-SQL
// does not require it, at a verb that cannot continue the current
// statement (UPDATE … SET, INSERT … SELECT, ON DELETE CASCADE and
// MERGE … THEN DELETE continue it).
func tsqlStatements(ts []Token) [][]Token {
	var out [][]Token
	start, depth, cases := 0, 0, 0 // cases: open CASE … END expressions
	head := func() string { return word(ts, start) }
	for i := 0; i < len(ts); i++ {
		t := ts[i]
		switch word(ts, i) {
		case "CASE":
			cases++
			continue
		case "END":
			if cases > 0 {
				cases--
				continue
			}
		}
		switch t.Text {
		case "(":
			depth++
			continue
		case ")":
			depth--
			continue
		case ";":
			if depth == 0 {
				if i > start {
					out = append(out, ts[start:i])
				}
				start = i + 1
			}
			continue
		}
		if depth != 0 || cases != 0 || i == start || !t.IsWord() {
			continue
		}
		w := t.Upper()
		if !tsqlVerbs[w] || continues(ts, start, i, head(), w) {
			continue
		}
		out = append(out, ts[start:i])
		start = i
	}
	if start < len(ts) {
		out = append(out, ts[start:])
	}
	if len(out) == 0 {
		out = append(out, ts)
	}
	return out
}

// continues reports whether verb w at ts[i] belongs to the statement that
// starts at ts[start] with the word head.
func continues(ts []Token, start, i int, head, w string) bool {
	prev := word(ts, i-1)
	switch prev {
	case "ON", "THEN", "FOR", "AFTER", "OF", "INSTEAD", "UNION", "ALL", "EXCEPT", "INTERSECT", "AS", "NOT",
		"GRANT", "DENY", "REVOKE", "WITH":
		return true
	}
	if ts[i-1].Text == "," || ts[i-1].Text == "=" {
		return true
	}
	switch head {
	case "MERGE", "GRANT", "REVOKE", "DENY", "CREATE", "ALTER":
		return true // until ';' (MERGE requires it; DDL bodies are kept whole)
	case "UPDATE":
		return w == "SET" && topLevelAny(ts[start:i], "SET") < 0
	case "INSERT":
		return w == "SELECT" || w == "EXEC" || w == "EXECUTE" || w == "WITH"
	case "WITH":
		return true // a CTE is followed by its statement
	}
	if w == "WITH" && i+1 < len(ts) && ts[i+1].Text == "(" {
		return true // table hint: FROM t WITH (NOLOCK)
	}
	return false
}

// markReadWrite flags statements that turn off read-only transaction mode,
// e.g. SET TRANSACTION READ WRITE or SET default_transaction_read_only = off.
func (c *Class) markReadWrite(ts []Token) {
	for i := range ts {
		if word(ts, i) == "READ" && word(ts, i+1) == "WRITE" {
			c.write, c.Reason = true, "changes read-only mode"
			return
		}
		name := strings.ToLower(ts[i].Text)
		switch strings.TrimPrefix(name, "@@") {
		case "default_transaction_read_only", "transaction_read_only", "tx_read_only", "read_only", "super_read_only":
		default:
			continue
		}
		j := i + 1
		if j < len(ts) && (ts[j].Text == "=" || ts[j].Text == ":=" || word(ts, j) == "TO") {
			j++
		}
		if j < len(ts) {
			switch strings.ToLower(strings.Trim(ts[j].Text, `'"`)) {
			case "off", "false", "0", "no":
				c.write, c.Reason = true, "changes read-only mode"
				return
			}
		}
	}
}

// classifyWith classifies WITH … <main statement>, accounting for
// data-modifying CTEs (Postgres: WITH x AS (DELETE …) SELECT …).
func classifyWith(ts []Token, d db.DriverName) Class {
	var cte Class
	for i := 1; i < len(ts); i++ {
		t := ts[i]
		if t.Text == "(" {
			j := matchParen(ts, i)
			if w := word(ts, i-1); w == "AS" || w == "MATERIALIZED" {
				if inner := classify(ts[i+1:j], d); inner.IsWrite() {
					cte.Kind, cte.Reason = KindWrite, "data-modifying CTE"
					if inner.Destructive {
						cte.Destructive, cte.Reason = true, inner.Reason
					}
				}
			}
			i = j
			continue
		}
		if ts[i-1].Text != ")" {
			continue
		}
		switch w := word(ts, i); w {
		case "SELECT", "INSERT", "UPDATE", "DELETE", "MERGE", "VALUES", "TABLE", "REPLACE":
			c := classify(ts[i:], d)
			if cte.Kind == KindWrite && !c.IsWrite() {
				c.Kind, c.Reason = KindWrite, cte.Reason
			}
			if cte.Destructive && !c.Destructive {
				c.Destructive, c.Reason = true, cte.Reason
			}
			return c
		}
	}
	return Class{Kind: KindOther, Keyword: "WITH"}
}

// classifyExplain treats EXPLAIN as a read unless ANALYZE executes a write.
func classifyExplain(ts []Token, d db.DriverName) Class {
	c := Class{Kind: KindRead, Keyword: "EXPLAIN"}
	analyze := false
	for i := 1; i < len(ts); i++ {
		switch w := word(ts, i); w {
		case "ANALYZE":
			switch strings.ToUpper(ts[min(i+1, len(ts)-1)].Text) {
			case "FALSE", "OFF", "0":
			default:
				analyze = true
			}
		case "SELECT", "INSERT", "UPDATE", "DELETE", "MERGE", "WITH", "VALUES", "TABLE", "REPLACE",
			"CREATE", "EXECUTE", "DECLARE":
			if !analyze {
				return c
			}
			inner := classify(ts[i:], d)
			if !inner.IsWrite() {
				return c
			}
			inner.Keyword = "EXPLAIN"
			if !inner.Destructive {
				inner.Reason = "EXPLAIN ANALYZE executes the statement"
			}
			return inner
		}
	}
	return c
}

// sqliteWritePragmas modify the database file when called without '='.
var sqliteWritePragmas = map[string]bool{
	"incremental_vacuum": true, "optimize": true, "wal_checkpoint": true, "shrink_memory": true,
}

// pragmaName returns the lower-case name of PRAGMA [schema.]name.
func pragmaName(ts []Token) string {
	if len(ts) > 3 && ts[2].Text == "." {
		return strings.ToLower(ts[3].Text)
	}
	if len(ts) > 1 {
		return strings.ToLower(ts[1].Text)
	}
	return ""
}

func hasPunct(ts []Token, text string) bool {
	for _, t := range ts {
		if (t.Kind == TokOperator || t.Kind == TokPunct) && t.Text == text {
			return true
		}
	}
	return false
}

// unfiltered reports whether an UPDATE/DELETE lacks a WHERE clause at the top
// level or has a trivially true one.
func unfiltered(ts []Token, verb string) (bool, string) {
	w := topLevel(ts, "WHERE")
	if w < 0 {
		if verb == "DELETE" {
			return true, "DELETE without WHERE removes all rows"
		}
		return true, "UPDATE without WHERE changes all rows"
	}
	if trivialCondition(ts[w+1:]) {
		return true, verb + " with an always-true WHERE affects all rows"
	}
	return false, ""
}

// trivialCondition reports whether a WHERE condition is always true:
// TRUE, 1, or <literal> = <same literal>.
func trivialCondition(ts []Token) bool {
	if i := topLevelAny(ts, "ORDER", "LIMIT", "RETURNING"); i >= 0 {
		ts = ts[:i]
	}
	for len(ts) >= 2 && ts[0].Text == "(" && matchParen(ts, 0) == len(ts)-1 {
		ts = ts[1 : len(ts)-1]
	}
	switch len(ts) {
	case 1:
		return word(ts, 0) == "TRUE" || ts[0].Text == "1"
	case 3:
		lit := ts[0].Kind == TokNumber || ts[0].Kind == TokString
		return lit && ts[1].Text == "=" && ts[0].Kind == ts[2].Kind && ts[0].Text == ts[2].Text
	}
	return false
}

// alterDrops reports whether an ALTER statement drops something that holds
// data or structure (columns, constraints, partitions…). DROP DEFAULT and
// DROP NOT NULL are harmless.
func alterDrops(ts []Token) (bool, string) {
	depth := 0
	for i, t := range ts {
		switch {
		case t.Text == "(":
			depth++
		case t.Text == ")":
			depth--
		case depth == 0 && word(ts, i) == "DROP":
			switch next := word(ts, i+1); next {
			case "DEFAULT", "NOT", "IDENTITY", "EXPRESSION":
			default:
				if next == "" {
					next = "COLUMN"
				}
				return true, "ALTER … DROP " + next + " removes it permanently"
			}
		}
	}
	return false, ""
}

// dropTarget names the object kind of a DROP statement.
func dropTarget(ts []Token) string {
	for i := 1; i < len(ts) && i < 4; i++ {
		switch w := word(ts, i); w {
		case "TEMPORARY", "TEMP", "MATERIALIZED", "FOREIGN", "UNIQUE", "UNLOGGED":
		case "":
			return "statement"
		default:
			return w
		}
	}
	return "statement"
}

// matchParen returns the index of the ')' matching ts[i] == "(", or the last
// index if unbalanced.
func matchParen(ts []Token, i int) int {
	depth := 0
	for j := i; j < len(ts); j++ {
		switch ts[j].Text {
		case "(":
			depth++
		case ")":
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return len(ts) - 1
}

// topLevel returns the index of the first word w outside parentheses, or -1.
func topLevel(ts []Token, w string) int { return topLevelAny(ts, w) }

func topLevelAny(ts []Token, words ...string) int {
	depth := 0
	for i, t := range ts {
		switch t.Text {
		case "(":
			depth++
		case ")":
			depth--
		default:
			if depth == 0 && t.IsWord() {
				for _, w := range words {
					if strings.EqualFold(t.Text, w) {
						return i
					}
				}
			}
		}
	}
	return -1
}
