package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/farvisun/squi/internal/db"
)

// session is a db.Session backed by one dedicated connection.
type session struct {
	c    *conn
	conn *sql.Conn
	db   string // database unqualified names are shown under (main)
	inTx bool

	mu     sync.Mutex
	cancel context.CancelFunc // interrupts the running statement
}

func (c *conn) NewSession(ctx context.Context, database string) (db.Session, error) {
	sc, err := c.sess.Conn(ctx)
	if err != nil {
		return nil, wrapErr(err)
	}
	s := &session{c: c, conn: sc, db: "main"}
	if database != "" {
		if err := s.UseDatabase(ctx, database); err != nil {
			sc.Close()
			return nil, err
		}
	}
	return s, nil
}

// watch derives the context of one statement; cancelling it interrupts
// the statement (sqlite3_interrupt) and the connection stays usable.
func (s *session) watch(ctx context.Context) (context.Context, func()) {
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

// rowStatements return rows and run with QueryContext; others go through
// ExecContext to report rows affected (unless they have RETURNING).
var rowStatements = map[string]bool{"SELECT": true, "WITH": true, "VALUES": true, "PRAGMA": true, "EXPLAIN": true}

func (s *session) Run(ctx context.Context, q string) (db.ResultStream, error) {
	kw := keywords(q, 2)
	first := ""
	if len(kw) > 0 {
		first = kw[0]
	}
	qctx, release := s.watch(ctx)
	if !rowStatements[first] && !hasReturning(q) {
		defer release()
		res, err := s.conn.ExecContext(qctx, q)
		if err != nil {
			return nil, wrapErr(err)
		}
		s.trackTx(kw)
		n, _ := res.RowsAffected()
		tag := "OK"
		switch first {
		case "INSERT", "UPDATE", "DELETE", "REPLACE":
			tag = first + " " + strconv.FormatInt(n, 10)
		}
		return &stream{exhausted: true, affected: n, tag: tag}, nil
	}
	rows, err := s.conn.QueryContext(qctx, q)
	if err != nil {
		release()
		return nil, wrapErr(err)
	}
	st := &stream{rows: rows, release: release, tag: first}
	if err := st.init(); err != nil {
		st.Close()
		return nil, err
	}
	return st, nil
}

// trackTx follows BEGIN / COMMIT / END / ROLLBACK after a successful
// statement (SQLite has no SQL function for the autocommit state).
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
		s.inTx = true
	case "COMMIT", "END":
		s.inTx = false
	case "ROLLBACK":
		if second != "TO" {
			s.inTx = false
		}
	}
}

func hasReturning(q string) bool {
	up := strings.ToUpper(q)
	i := strings.Index(up, "RETURNING")
	return i > 0 && (unicode.IsSpace(rune(up[i-1])) || up[i-1] == ')')
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
	if err := s.exec(ctx, "BEGIN"); err != nil {
		return err
	}
	s.inTx = true
	return nil
}

func (s *session) Commit(ctx context.Context) error {
	if !s.inTx {
		return nil
	}
	err := s.exec(ctx, "COMMIT")
	if err == nil {
		s.inTx = false
	}
	return err
}

func (s *session) Rollback(ctx context.Context) error {
	if !s.inTx {
		return nil
	}
	err := s.exec(ctx, "ROLLBACK")
	if err == nil {
		s.inTx = false
	}
	return err
}

func (s *session) InTx() bool { return s.inTx }

// Cancel interrupts the statement running on this session; safe for
// concurrent use.
func (s *session) Cancel(context.Context) error {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

// UseDatabase selects the database (main or an attached one) shown as
// current. SQLite resolves unqualified names itself (temp, main, then
// attached databases), so this only checks that the database exists.
func (s *session) UseDatabase(ctx context.Context, name string) error {
	var n int
	if err := s.conn.QueryRowContext(ctx, "SELECT count(*) FROM pragma_database_list WHERE name = ?1", name).Scan(&n); err != nil {
		return wrapErr(err)
	}
	if n == 0 {
		return fmt.Errorf("no database %q (ATTACH it first)", name)
	}
	s.db = name
	return nil
}

func (s *session) CurrentDatabase() string { return s.db }

// BulkInsert inserts rows with multi-row INSERT statements of at most 500
// rows or about 1 MB each, inside a savepoint (one transaction, much
// faster than autocommit).
func (s *session) BulkInsert(ctx context.Context, ref db.ObjectRef, columns []string, rows [][]db.Cell) (n int64, err error) {
	const maxRows, maxBytes = 500, 1 << 20
	d := s.c.Dialect()
	cols := make([]string, len(columns))
	for i, c := range columns {
		cols[i] = d.QuoteIdent(c)
	}
	if err := s.exec(ctx, "SAVEPOINT squi_import"); err != nil {
		return 0, err
	}
	defer func() {
		if err != nil {
			_ = s.exec(context.WithoutCancel(ctx), "ROLLBACK TO squi_import")
			n = 0
		}
		if rerr := s.exec(context.WithoutCancel(ctx), "RELEASE squi_import"); err == nil {
			err = rerr
		}
	}()
	head := "INSERT INTO " + d.Qualified(ref) + " (" + strings.Join(cols, ", ") + ") VALUES "
	var b strings.Builder
	count := 0
	flush := func() error {
		if count == 0 {
			return nil
		}
		qctx, release := s.watch(ctx)
		defer release()
		res, err := s.conn.ExecContext(qctx, b.String())
		if err != nil {
			return wrapErr(err)
		}
		affected, _ := res.RowsAffected()
		n += affected
		b.Reset()
		count = 0
		return nil
	}
	for _, row := range rows {
		if count == 0 {
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
		count++
		if count >= maxRows || b.Len() >= maxBytes {
			if err := flush(); err != nil {
				return n, err
			}
		}
	}
	return n, flush()
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

// stream is a db.ResultStream.
type stream struct {
	rows    *sql.Rows
	release func()
	cols    []db.ResultColumn
	types   []string
	vals    []any
	ptrs    []any

	read      int64
	affected  int64
	tag       string
	exhausted bool
	closed    bool
}

func (st *stream) init() error {
	types, err := st.rows.ColumnTypes()
	if err != nil {
		return wrapErr(err)
	}
	if len(types) == 0 {
		st.exhausted, st.tag = true, "OK"
		return wrapErr(st.rows.Err())
	}
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

// Close releases the result; an unfinished statement is reset, not run to
// the end.
func (st *stream) Close() error {
	if st.closed || st.rows == nil {
		st.closed = true
		return nil
	}
	st.closed = true
	defer st.release()
	return wrapErr(st.rows.Close())
}

// Explain returns the EXPLAIN QUERY PLAN tree. With analyze the statement
// also runs (its rows are read and discarded) and the root carries the
// elapsed time and row count: SQLite has no per-node runtime statistics.
func (s *session) Explain(ctx context.Context, q string, analyze bool) (*db.PlanNode, error) {
	q = strings.TrimRight(strings.TrimSpace(q), ";")
	qctx, release := s.watch(ctx)
	rows, err := s.conn.QueryContext(qctx, "EXPLAIN QUERY PLAN "+q)
	if err != nil {
		release()
		return nil, wrapErr(err)
	}
	type step struct {
		id, parent int
		detail     string
	}
	var steps []step
	for rows.Next() {
		var st step
		var unused int
		if err := rows.Scan(&st.id, &st.parent, &unused, &st.detail); err != nil {
			rows.Close()
			release()
			return nil, err
		}
		steps = append(steps, st)
	}
	err = rows.Err()
	rows.Close()
	release()
	if err != nil {
		return nil, wrapErr(err)
	}
	root := &db.PlanNode{Title: "QUERY PLAN"}
	nodes := map[int]*db.PlanNode{0: root}
	var raw []string
	for _, st := range steps {
		n := &db.PlanNode{Title: st.detail}
		parent := nodes[st.parent]
		if parent == nil {
			parent = root
		}
		parent.Children = append(parent.Children, n)
		nodes[st.id] = n
		depth := 0
		for p := st.parent; p != 0 && depth < 64; depth++ {
			found := false
			for _, o := range steps {
				if o.id == p {
					p, found = o.parent, true
					break
				}
			}
			if !found {
				break
			}
		}
		raw = append(raw, strings.Repeat("   ", depth)+"|--"+st.detail)
	}
	root.Raw = "QUERY PLAN\n" + strings.Join(raw, "\n")
	if !analyze {
		return root, nil
	}
	start := time.Now()
	res, err := s.Run(ctx, q)
	if err != nil {
		return nil, err
	}
	var count int64
	for {
		batch, err := res.Next(1000)
		count += int64(len(batch))
		if err == io.EOF {
			break
		}
		if err != nil {
			res.Close()
			return nil, err
		}
	}
	res.Close()
	if res.Columns() == nil {
		count = res.RowsAffected()
	}
	root.Time = float64(time.Since(start).Microseconds()) / 1000
	root.Rows = float64(count)
	root.Details = append(root.Details, db.KV{Key: "Executed", Value: fmt.Sprintf("%d rows in %.3f ms", count, root.Time)})
	return root, nil
}
