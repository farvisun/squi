package mysql

import (
	"context"
	"database/sql"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/farvisun/squi/internal/db"
)

// session is a db.Session backed by one dedicated server connection.
type session struct {
	c    *conn
	conn *sql.Conn
	id   uint64 // server CONNECTION_ID(), target of KILL QUERY
	db   string
	inTx bool
}

func (c *conn) NewSession(ctx context.Context, database string) (db.Session, error) {
	sc, err := c.sess.Conn(ctx)
	if err != nil {
		return nil, wrapErr(err)
	}
	s := &session{c: c, conn: sc}
	err = sc.QueryRowContext(ctx, "SELECT CONNECTION_ID(), COALESCE(DATABASE(), '')").Scan(&s.id, &s.db)
	if err == nil && c.cfg.ReadOnly {
		_, err = sc.ExecContext(ctx, "SET SESSION TRANSACTION READ ONLY")
	}
	if err == nil && database != "" {
		err = s.UseDatabase(ctx, database)
	}
	if err != nil {
		sc.Close()
		return nil, wrapErr(err)
	}
	return s, nil
}

// watch derives the context used for driver calls. The driver reacts to
// a cancelled context by dropping the connection, which would lose the
// session state; instead cancellation of ctx issues KILL QUERY and only
// falls back to aborting the connection if that fails. release must be
// called once the statement (and its result) is finished.
func (s *session) watch(ctx context.Context) (qctx context.Context, release func()) {
	qctx, abort := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, func() {
		kctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if s.Cancel(kctx) != nil {
			abort()
		}
	})
	return qctx, func() { stop(); abort() }
}

// rowStatements return result sets and are run with QueryContext; all other
// statements go through ExecContext to report rows affected.
var rowStatements = map[string]bool{
	"SELECT": true, "SHOW": true, "DESCRIBE": true, "DESC": true, "EXPLAIN": true,
	"WITH": true, "VALUES": true, "TABLE": true, "CALL": true, "HANDLER": true,
	"CHECK": true, "ANALYZE": true, "OPTIMIZE": true, "REPAIR": true, "CHECKSUM": true,
	"EXECUTE": true, "HELP": true,
}

func (s *session) Run(ctx context.Context, q string) (db.ResultStream, error) {
	kw := keywords(q, 2)
	first := ""
	if len(kw) > 0 {
		first = kw[0]
	}
	qctx, release := s.watch(ctx)
	if !rowStatements[first] {
		defer release()
		res, err := s.conn.ExecContext(qctx, q)
		if err != nil {
			return nil, wrapErr(err)
		}
		s.trackTx(kw)
		if first == "USE" {
			if err := s.conn.QueryRowContext(qctx, "SELECT COALESCE(DATABASE(), '')").Scan(&s.db); err != nil {
				return nil, wrapErr(err)
			}
		}
		n, _ := res.RowsAffected()
		tag := "OK"
		switch first {
		case "INSERT", "UPDATE", "DELETE", "REPLACE", "LOAD":
			tag = first + " " + strconv.FormatInt(n, 10)
		}
		return &stream{exhausted: true, affected: n, tag: tag}, nil
	}

	rows, err := s.conn.QueryContext(qctx, q)
	if err != nil {
		release()
		return nil, wrapErr(err)
	}
	st := &stream{s: s, rows: rows, release: release, tag: first}
	if err := st.init(); err != nil {
		st.Close()
		return nil, err
	}
	return st, nil
}

// trackTx updates the transaction flag after a successful statement.
func (s *session) trackTx(kw []string) {
	if len(kw) == 0 {
		return
	}
	second := ""
	if len(kw) > 1 {
		second = kw[1]
	}
	switch kw[0] {
	case "BEGIN":
		if second != "NOT" { // MariaDB "BEGIN NOT ATOMIC" compound statement
			s.inTx = true
		}
	case "START":
		if second == "TRANSACTION" {
			s.inTx = true
		}
	case "COMMIT":
		s.inTx = false
	case "ROLLBACK":
		if second != "TO" {
			s.inTx = false
		}
	case "CREATE", "DROP":
		if second != "TEMPORARY" {
			s.inTx = false
		}
	case "ALTER", "TRUNCATE", "RENAME", "LOCK", "GRANT", "REVOKE", "FLUSH", "INSTALL", "UNINSTALL":
		s.inTx = false
	}
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
		q = strings.TrimLeftFunc(q, func(r rune) bool { return unicode.IsSpace(r) || r == '(' })
		switch {
		case strings.HasPrefix(q, "/*"):
			end := strings.Index(q[2:], "*/")
			if end < 0 {
				return ""
			}
			q = q[end+4:]
		case strings.HasPrefix(q, "#"), strings.HasPrefix(q, "--") && (len(q) == 2 || unicode.IsSpace(rune(q[2]))):
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
	if err := s.exec(ctx, "START TRANSACTION"); err != nil {
		return err
	}
	s.inTx = true
	return nil
}

func (s *session) Commit(ctx context.Context) error {
	err := s.exec(ctx, "COMMIT")
	if err == nil {
		s.inTx = false
	}
	return err
}

func (s *session) Rollback(ctx context.Context) error {
	err := s.exec(ctx, "ROLLBACK")
	if err == nil {
		s.inTx = false
	}
	return err
}

func (s *session) InTx() bool { return s.inTx }

// Cancel kills the statement running on this session; safe for concurrent use.
func (s *session) Cancel(ctx context.Context) error { return s.c.kill(ctx, s.id, true) }

func (s *session) UseDatabase(ctx context.Context, name string) error {
	if err := s.exec(ctx, "USE "+s.c.Dialect().QuoteIdent(name)); err != nil {
		return err
	}
	s.db = name
	return nil
}

func (s *session) CurrentDatabase() string { return s.db }

// BulkInsert inserts rows with multi-row INSERT statements of at most 500
// rows or about 1 MB each.
func (s *session) BulkInsert(ctx context.Context, ref db.ObjectRef, columns []string, rows [][]db.Cell) (int64, error) {
	const maxRows, maxBytes = 500, 1 << 20
	d := s.c.Dialect()
	if ref.Database == "" && ref.Schema == "" {
		ref.Database = s.db
	}
	cols := make([]string, len(columns))
	for i, c := range columns {
		cols[i] = d.QuoteIdent(c)
	}
	head := "INSERT INTO " + d.Qualified(ref) + " (" + strings.Join(cols, ", ") + ") VALUES "

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

func (s *session) Close() error { return s.conn.Close() }

// stream is a db.ResultStream. Statements without a result set produce an
// already exhausted stream carrying rows affected.
type stream struct {
	s       *session
	rows    *sql.Rows
	release func()
	cols    []db.ResultColumn
	raw     []sql.RawBytes
	ptrs    []any

	read      int64
	affected  int64
	tag       string
	exhausted bool
	closed    bool
}

// init reads the column metadata, skipping leading result sets without
// columns (e.g. the status result of a CALL).
func (st *stream) init() error {
	for {
		types, err := st.rows.ColumnTypes()
		if err != nil {
			return wrapErr(err)
		}
		if len(types) > 0 {
			st.cols = make([]db.ResultColumn, len(types))
			for i, t := range types {
				st.cols[i] = db.ResultColumn{Name: t.Name(), Type: strings.ToLower(t.DatabaseTypeName())}
			}
			st.raw = make([]sql.RawBytes, len(types))
			st.ptrs = make([]any, len(types))
			for i := range st.raw {
				st.ptrs[i] = &st.raw[i]
			}
			return nil
		}
		if !st.rows.NextResultSet() {
			st.exhausted = true
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
			return out, io.EOF
		}
		if err := st.rows.Scan(st.ptrs...); err != nil {
			return out, err
		}
		row := make([]db.Cell, len(st.raw))
		for i, v := range st.raw {
			if v == nil {
				row[i] = db.NullCell
			} else {
				row[i] = db.Text(string(v))
			}
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

// Close releases the result. An unfinished result is killed server-side
// first so the driver does not have to drain the remaining rows.
func (st *stream) Close() error {
	if st.closed || st.rows == nil {
		st.closed = true
		return nil
	}
	st.closed = true
	defer st.release()
	if !st.exhausted {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		killErr := st.s.Cancel(ctx)
		cancel()
		st.rows.Close() // reports the interruption; the connection stays usable
		return killErr
	}
	return wrapErr(st.rows.Close())
}
