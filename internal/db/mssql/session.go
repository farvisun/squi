package mssql

import (
	"context"
	"database/sql"
	"io"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/farvisun/squi/internal/db"
)

// session is a db.Session backed by one dedicated server connection.
type session struct {
	c    *conn
	conn *sql.Conn
	spid int64
	db   string
	inTx bool

	mu     sync.Mutex
	cancel context.CancelFunc // aborts the running statement
}

func (c *conn) NewSession(ctx context.Context, database string) (db.Session, error) {
	sc, err := c.sess.Conn(ctx)
	if err != nil {
		return nil, wrapErr(err)
	}
	s := &session{c: c, conn: sc}
	err = sc.QueryRowContext(ctx, "SELECT CAST(@@SPID AS bigint), DB_NAME()").Scan(&s.spid, &s.db)
	if err == nil && database != "" && !strings.EqualFold(database, s.db) {
		err = s.UseDatabase(ctx, database)
	}
	if err != nil {
		sc.Close()
		return nil, wrapErr(err)
	}
	return s, nil
}

// watch derives the context of one statement. Cancelling it (through
// Cancel, or when ctx ends) makes the driver send a TDS attention: the
// server stops the statement and the connection stays usable. release must
// be called once the statement and its result are finished.
func (s *session) watch(ctx context.Context) (qctx context.Context, release func()) {
	qctx, abort := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, abort)
	s.mu.Lock()
	s.cancel = abort
	s.mu.Unlock()
	return qctx, func() {
		stop()
		abort()
		s.mu.Lock()
		s.cancel = nil
		s.mu.Unlock()
	}
}

// execStatements report rows affected and return no result set, unless
// they have an OUTPUT clause.
var execStatements = map[string]bool{
	"INSERT": true, "UPDATE": true, "DELETE": true, "MERGE": true, "CREATE": true, "ALTER": true,
	"DROP": true, "TRUNCATE": true, "GRANT": true, "REVOKE": true, "DENY": true, "USE": true,
	"SET": true, "BEGIN": true, "COMMIT": true, "ROLLBACK": true, "SAVE": true, "KILL": true,
	"BULK": true, "ENABLE": true, "DISABLE": true, "CHECKPOINT": true,
}

func (s *session) Run(ctx context.Context, q string) (db.ResultStream, error) {
	kw := keywords(q, 2)
	first := ""
	if len(kw) > 0 {
		first = kw[0]
	}
	exec := execStatements[first] && !hasWord(q, "OUTPUT")
	if first == "BEGIN" && len(kw) > 1 && kw[1] != "TRAN" && kw[1] != "TRANSACTION" && kw[1] != "DISTRIBUTED" {
		exec = false // BEGIN … END / BEGIN TRY blocks may return rows
	}
	qctx, release := s.watch(ctx)
	if exec {
		defer release()
		res, err := s.conn.ExecContext(qctx, q)
		if err != nil {
			err = wrapErr(err)
			s.syncState(ctx, first == "USE", s.inTx)
			return nil, err
		}
		s.syncState(ctx, first == "USE", txWords(q))
		n, _ := res.RowsAffected()
		tag := "OK"
		switch first {
		case "INSERT", "UPDATE", "DELETE", "MERGE":
			tag = first + " " + strconv.FormatInt(n, 10)
		}
		return &stream{exhausted: true, affected: n, tag: tag}, nil
	}

	rows, err := s.conn.QueryContext(qctx, q)
	if err != nil {
		release()
		err = wrapErr(err)
		s.syncState(ctx, hasWord(q, "USE"), s.inTx || txWords(q))
		return nil, err
	}
	st := &stream{s: s, rows: rows, release: release, tag: first,
		useDB: hasWord(q, "USE"), tx: txWords(q)}
	if err := st.init(); err != nil {
		st.Close()
		return nil, err
	}
	return st, nil
}

// syncState re-reads the current database and the transaction state after
// statements that may have changed them.
func (s *session) syncState(ctx context.Context, useDB, tx bool) {
	ctx = context.WithoutCancel(ctx)
	if useDB {
		var name string
		if s.conn.QueryRowContext(ctx, "SELECT DB_NAME()").Scan(&name) == nil {
			s.db = name
		}
	}
	if tx {
		var n int
		if s.conn.QueryRowContext(ctx, "SELECT @@TRANCOUNT").Scan(&n) == nil {
			s.inTx = n > 0
		}
	}
}

// txWords reports whether q may start or end a transaction.
func txWords(q string) bool {
	return hasWord(q, "TRAN") || hasWord(q, "TRANSACTION") || hasWord(q, "COMMIT") || hasWord(q, "ROLLBACK")
}

// hasWord reports whether q contains the keyword w outside strings and
// comments (a cheap scan; identifiers in brackets or quotes are skipped).
func hasWord(q string, w string) bool {
	i := 0
	for i < len(q) {
		c := q[i]
		switch {
		case c == '\'' || c == '[' || c == '"':
			end := byte('\'')
			if c == '[' {
				end = ']'
			} else if c == '"' {
				end = '"'
			}
			i++
			for i < len(q) {
				if q[i] == end {
					if i+1 < len(q) && q[i+1] == end {
						i += 2
						continue
					}
					break
				}
				i++
			}
			i++
		case c == '-' && i+1 < len(q) && q[i+1] == '-':
			for i < len(q) && q[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(q) && q[i+1] == '*':
			end := strings.Index(q[i+2:], "*/")
			if end < 0 {
				return false
			}
			i += end + 4
		case isWordChar(c):
			j := i
			for j < len(q) && isWordChar(q[j]) {
				j++
			}
			if strings.EqualFold(q[i:j], w) && (i == 0 || (q[i-1] != '@' && q[i-1] != '#' && q[i-1] != '.')) {
				return true
			}
			i = j
		default:
			i++
		}
	}
	return false
}

func isWordChar(c byte) bool {
	return c == '_' || c == '@' || c == '#' || c == '$' || (c|0x20 >= 'a' && c|0x20 <= 'z') || (c >= '0' && c <= '9') || c >= 0x80
}

// keywords returns up to n leading upper-cased keywords of q, skipping
// whitespace, comments and opening parentheses.
func keywords(q string, n int) []string {
	var out []string
	for len(out) < n {
		q = skipNoise(q)
		end := strings.IndexFunc(q, func(r rune) bool { return !unicode.IsLetter(r) && r != '_' })
		if end < 0 {
			end = len(q)
		}
		if end == 0 {
			break
		}
		out = append(out, strings.ToUpper(q[:end]))
		q = q[end:]
	}
	return out
}

func skipNoise(q string) string {
	for {
		q = strings.TrimLeftFunc(q, func(r rune) bool { return unicode.IsSpace(r) || r == '(' || r == ';' })
		switch {
		case strings.HasPrefix(q, "/*"):
			end := strings.Index(q[2:], "*/")
			if end < 0 {
				return ""
			}
			q = q[end+4:]
		case strings.HasPrefix(q, "--"):
			end := strings.IndexByte(q, '\n')
			if end < 0 {
				return ""
			}
			q = q[end+1:]
		default:
			return q
		}
	}
}

func (s *session) exec(ctx context.Context, q string) error {
	qctx, release := s.watch(ctx)
	defer release()
	_, err := s.conn.ExecContext(qctx, q)
	return wrapErr(err)
}

func (s *session) Begin(ctx context.Context) error {
	if err := s.exec(ctx, "BEGIN TRANSACTION"); err != nil {
		return err
	}
	s.inTx = true
	return nil
}

func (s *session) Commit(ctx context.Context) error {
	err := s.exec(ctx, "IF @@TRANCOUNT > 0 COMMIT TRANSACTION")
	s.syncState(ctx, false, true)
	return err
}

func (s *session) Rollback(ctx context.Context) error {
	err := s.exec(ctx, "IF @@TRANCOUNT > 0 ROLLBACK TRANSACTION")
	s.syncState(ctx, false, true)
	return err
}

func (s *session) InTx() bool { return s.inTx }

// Cancel stops the statement running on this session; safe for concurrent
// use. The attention leaves an open transaction intact.
func (s *session) Cancel(context.Context) error {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

func (s *session) UseDatabase(ctx context.Context, name string) error {
	if err := s.exec(ctx, "USE "+s.c.Dialect().QuoteIdent(name)); err != nil {
		return err
	}
	s.db = name
	return nil
}

func (s *session) CurrentDatabase() string { return s.db }

// BulkInsert inserts rows with multi-row INSERT statements of at most 500
// rows (the server allows 1000) or about 1 MB each.
func (s *session) BulkInsert(ctx context.Context, ref db.ObjectRef, columns []string, rows [][]db.Cell) (int64, error) {
	const maxRows, maxBytes = 500, 1 << 20
	d := s.c.Dialect()
	cols := make([]string, len(columns))
	for i, c := range columns {
		cols[i] = d.QuoteIdent(c)
	}
	target := d.Qualified(ref)
	if ref.Database != "" && !strings.EqualFold(ref.Database, s.db) {
		target = d.QuoteIdent(ref.Database) + "." + target
	}
	head := "INSERT INTO " + target + " (" + strings.Join(cols, ", ") + ") VALUES "

	var total int64
	var b strings.Builder
	n := 0
	flush := func() error {
		if n == 0 {
			return nil
		}
		qctx, release := s.watch(ctx)
		defer release()
		res, err := s.conn.ExecContext(qctx, b.String())
		if err != nil {
			return wrapErr(err)
		}
		affected, _ := res.RowsAffected()
		total += affected
		b.Reset()
		n = 0
		return nil
	}
	for _, row := range rows {
		if n == 0 {
			b.WriteString(head)
		} else {
			b.WriteString(", ")
		}
		b.WriteByte('(')
		for i, cell := range row {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(d.Value(cell))
		}
		b.WriteByte(')')
		n++
		if n >= maxRows || b.Len() >= maxBytes {
			if err := flush(); err != nil {
				return total, err
			}
		}
	}
	return total, flush()
}

// Close interrupts a running statement first: an open result set would
// otherwise block closing the connection.
func (s *session) Close() error {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	return s.conn.Close()
}

// stream is a db.ResultStream over the first result set with columns.
// Statements without a result set produce an already exhausted stream
// carrying rows affected.
type stream struct {
	s       *session
	rows    *sql.Rows
	release func()
	cols    []db.ResultColumn
	types   []string
	vals    []any
	ptrs    []any

	useDB, tx bool // re-read the session state once finished

	read      int64
	affected  int64
	tag       string
	exhausted bool
	closed    bool
}

// init reads the column metadata, skipping leading result sets without
// columns (DECLARE, SET, DML before the first SELECT of a batch…).
func (st *stream) init() error {
	for {
		types, err := st.rows.ColumnTypes()
		if err != nil {
			return wrapErr(err)
		}
		if len(types) > 0 {
			st.cols = make([]db.ResultColumn, len(types))
			st.types = make([]string, len(types))
			for i, t := range types {
				st.types[i] = strings.ToUpper(t.DatabaseTypeName())
				st.cols[i] = db.ResultColumn{Name: t.Name(), Type: strings.ToLower(t.DatabaseTypeName())}
			}
			st.vals = make([]any, len(types))
			st.ptrs = make([]any, len(types))
			for i := range st.vals {
				st.ptrs[i] = &st.vals[i]
			}
			return nil
		}
		if !st.rows.NextResultSet() {
			st.exhausted = true
			st.tag = "OK"
			return wrapErr(st.rows.Err())
		}
	}
}

func (st *stream) Columns() []db.ResultColumn { return st.cols }

func (st *stream) Next(n int) ([][]db.Cell, error) {
	if st.exhausted || st.closed {
		return nil, io.EOF
	}
	var out [][]db.Cell
	for len(out) < n {
		if !st.rows.Next() {
			st.exhausted = true
			if err := st.rows.Err(); err != nil {
				return out, wrapErr(err)
			}
			st.tag += " " + strconv.FormatInt(st.read, 10)
			if st.rows.NextResultSet() {
				st.tag += " (further result sets not shown)"
			}
			return out, io.EOF
		}
		if err := st.rows.Scan(st.ptrs...); err != nil {
			return out, err
		}
		row := make([]db.Cell, len(st.vals))
		for i, v := range st.vals {
			row[i] = cellOf(v, st.types[i])
		}
		out = append(out, row)
		st.read++
	}
	return out, nil
}

func (st *stream) RowsAffected() int64 {
	if st.rows != nil {
		return st.read
	}
	return st.affected
}

func (st *stream) CommandTag() string { return st.tag }

// Close releases the result. The driver cancels an unfinished result with
// an attention instead of reading the remaining rows.
func (st *stream) Close() error {
	if st.closed || st.rows == nil {
		st.closed = true
		return nil
	}
	st.closed = true
	defer st.release()
	err := st.rows.Close()
	if !st.exhausted {
		err = nil // the attention's "cancelled" is expected
	}
	st.s.syncState(context.Background(), st.useDB, st.tx || st.s.inTx)
	return wrapErr(err)
}
