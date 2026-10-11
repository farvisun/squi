package postgres

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/farvisun/squi/internal/db"
)

// closeDrainBudget is how long ResultStream.Close drains remaining rows
// before it asks the server to cancel the statement.
const closeDrainBudget = 250 * time.Millisecond

// QueryError is a server error raised by a statement run in a Session.
// Position is the 1-based character offset into the SQL text (0 if unknown).
type QueryError struct {
	Message, Detail, Hint, Code string
	Position                    int

	pg *pgconn.PgError
}

func (e *QueryError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "ERROR: %s (SQLSTATE %s)", e.Message, e.Code)
	if e.Position > 0 {
		fmt.Fprintf(&b, " at character %d", e.Position)
	}
	if e.Detail != "" {
		b.WriteString("\nDETAIL: " + e.Detail)
	}
	if e.Hint != "" {
		b.WriteString("\nHINT: " + e.Hint)
	}
	return b.String()
}

// Unwrap returns the underlying *pgconn.PgError.
func (e *QueryError) Unwrap() error { return e.pg }

// queryError converts a *pgconn.PgError inside err into a *QueryError,
// shifting its position by -offset characters. Other errors pass through.
func queryError(err error, offset int) error {
	var pe *pgconn.PgError
	if !errors.As(err, &pe) {
		return err
	}
	pos := int(pe.Position)
	if pos > offset {
		pos -= offset
	}
	return &QueryError{Message: pe.Message, Detail: pe.Detail, Hint: pe.Hint, Code: pe.Code, Position: pos, pg: pe}
}

// NewSession opens a dedicated connection to database ("" = default).
func (c *conn) NewSession(ctx context.Context, database string) (db.Session, error) {
	if database == "" {
		database = c.cfg.Database
	}
	s := &session{c: c}
	if err := s.connect(ctx, database); err != nil {
		return nil, err
	}
	return s, nil
}

// session implements db.Session on a single *pgx.Conn.
type session struct {
	c        *conn
	pc       *pgx.Conn
	database string
	types    map[uint32]string // type names resolved via pg_type
}

func (s *session) connect(ctx context.Context, database string) error {
	cc, err := s.c.connConfig(database)
	if err != nil {
		return err
	}
	if s.c.cfg.ReadOnly {
		cc.RuntimeParams["default_transaction_read_only"] = "on"
	}
	pc, err := pgx.ConnectConfig(ctx, cc)
	if err != nil {
		return fmt.Errorf("postgres: connect to %s: %w", s.c.target(database), err)
	}
	if s.c.cfg.ReadOnly {
		if _, err := pc.Exec(ctx, "SET SESSION CHARACTERISTICS AS TRANSACTION READ ONLY"); err != nil {
			pc.Close(ctx)
			return err
		}
	}
	s.pc, s.database, s.types = pc, database, map[uint32]string{}
	return nil
}

func (s *session) exec(ctx context.Context, sql string) error {
	_, err := s.pc.PgConn().Exec(ctx, sql).ReadAll()
	return queryError(err, 0)
}

func (s *session) Begin(ctx context.Context) error    { return s.exec(ctx, "BEGIN") }
func (s *session) Commit(ctx context.Context) error   { return s.exec(ctx, "COMMIT") }
func (s *session) Rollback(ctx context.Context) error { return s.exec(ctx, "ROLLBACK") }

// InTx reports whether a transaction (possibly failed) is open.
func (s *session) InTx() bool {
	st := s.pc.PgConn().TxStatus()
	return st == 'T' || st == 'E'
}

// Cancel asks the server to abort the running statement.
func (s *session) Cancel(ctx context.Context) error { return s.pc.PgConn().CancelRequest(ctx) }

// UseDatabase reconnects the session to another database.
func (s *session) UseDatabase(ctx context.Context, name string) error {
	if s.InTx() {
		return errors.New("postgres: cannot switch database inside a transaction")
	}
	old := s.pc
	if err := s.connect(ctx, name); err != nil {
		return err
	}
	return old.Close(ctx)
}

func (s *session) CurrentDatabase() string { return s.database }

func (s *session) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.pc.Close(ctx)
}

// Run executes sql with the simple query protocol and streams the first
// result that returns rows. Results before it are drained; results after it
// are drained when the stream is exhausted, and their errors are reported.
// ctx must stay valid until the stream is closed.
func (s *session) Run(ctx context.Context, sql string) (db.ResultStream, error) {
	mrr := s.pc.PgConn().Exec(ctx, sql)
	var tag pgconn.CommandTag
	for mrr.NextResult() {
		rr := mrr.ResultReader()
		fields := rr.FieldDescriptions()
		if len(fields) == 0 {
			var err error
			if tag, err = rr.Close(); err != nil {
				mrr.Close()
				return nil, queryError(err, 0)
			}
			continue
		}
		return &stream{s: s, mrr: mrr, rr: rr, cols: s.columns(ctx, fields)}, nil
	}
	if err := mrr.Close(); err != nil {
		return nil, queryError(err, 0)
	}
	return &stream{done: true, tag: tag}, nil
}

// columns names result columns, resolving types unknown to pgx (enums,
// domains, extension types) through the metadata pool.
func (s *session) columns(ctx context.Context, fields []pgconn.FieldDescription) []db.ResultColumn {
	var unknown []int64
	for _, f := range fields {
		if _, ok := s.types[f.DataTypeOID]; ok {
			continue
		}
		if t, ok := s.pc.TypeMap().TypeForOID(f.DataTypeOID); ok {
			s.types[f.DataTypeOID] = t.Name
		} else {
			unknown = append(unknown, int64(f.DataTypeOID))
		}
	}
	if len(unknown) > 0 {
		s.lookupTypes(ctx, unknown)
	}
	cols := make([]db.ResultColumn, len(fields))
	for i, f := range fields {
		name, ok := s.types[f.DataTypeOID]
		if !ok {
			name = "oid:" + strconv.FormatUint(uint64(f.DataTypeOID), 10)
		}
		cols[i] = db.ResultColumn{Name: f.Name, Type: name}
	}
	return cols
}

func (s *session) lookupTypes(ctx context.Context, oids []int64) {
	p, err := s.c.pool(s.database)
	if err != nil {
		return
	}
	rows, _ := p.Query(ctx, `SELECT oid::int8, typname::text FROM pg_type WHERE oid::int8 = ANY ($1)`, oids)
	var (
		oid  int64
		name string
	)
	_, _ = pgx.ForEachRow(rows, []any{&oid, &name}, func() error {
		s.types[uint32(oid)] = name
		return nil
	})
}

// stream implements db.ResultStream over a pgconn result reader.
type stream struct {
	s    *session
	mrr  *pgconn.MultiResultReader
	rr   *pgconn.ResultReader
	cols []db.ResultColumn
	done bool
	tag  pgconn.CommandTag
}

func (st *stream) Columns() []db.ResultColumn { return st.cols }
func (st *stream) RowsAffected() int64        { return st.tag.RowsAffected() }
func (st *stream) CommandTag() string         { return st.tag.String() }

func (st *stream) Next(n int) ([][]db.Cell, error) {
	if st.done {
		return nil, io.EOF
	}
	if n <= 0 {
		return nil, nil
	}
	rows := make([][]db.Cell, 0, min(n, 1024))
	for len(rows) < n {
		if !st.rr.NextRow() {
			if err := st.finish(); err != nil {
				return rows, queryError(err, 0)
			}
			return rows, io.EOF
		}
		vals := st.rr.Values()
		row := make([]db.Cell, len(vals))
		for i, v := range vals {
			if v == nil {
				row[i] = db.NullCell
			} else {
				row[i] = db.Text(string(v))
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// finish reads the rest of the response, keeping the streamed result's tag
// and the first error encountered.
func (st *stream) finish() error {
	st.done = true
	tag, err := st.rr.Close()
	st.tag = tag
	for st.mrr.NextResult() {
		if _, e := st.mrr.ResultReader().Close(); err == nil {
			err = e
		}
	}
	if e := st.mrr.Close(); err == nil {
		err = e
	}
	return err
}

// Close discards the remaining rows. If draining takes longer than
// closeDrainBudget the statement is cancelled on the server, which aborts an
// enclosing transaction.
func (st *stream) Close() error {
	if st.done {
		return nil
	}
	cancelled := make(chan struct{})
	timer := time.AfterFunc(closeDrainBudget, func() {
		defer close(cancelled)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = st.s.pc.PgConn().CancelRequest(ctx)
	})
	err := st.finish()
	if !timer.Stop() {
		<-cancelled
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return nil // statement errors are irrelevant once rows are discarded
	}
	return err
}

// BulkInsert loads rows into ref with COPY ... FROM STDIN in CSV format.
// The table is resolved in the session's current database.
func (s *session) BulkInsert(ctx context.Context, ref db.ObjectRef, columns []string, rows [][]db.Cell) (int64, error) {
	quoted := make([]string, len(columns))
	for i, c := range columns {
		quoted[i] = dialect.QuoteIdent(c)
	}
	sql := "COPY " + dialect.Qualified(ref) + " (" + strings.Join(quoted, ", ") +
		`) FROM STDIN WITH (FORMAT csv, NULL E'\\N')`

	pr, pw := io.Pipe()
	defer pr.Close()
	go func() { pw.CloseWithError(writeCSV(pw, len(columns), rows)) }()
	tag, err := s.pc.PgConn().CopyFrom(ctx, pr, sql)
	if err != nil {
		return 0, queryError(err, 0)
	}
	return tag.RowsAffected(), nil
}

// writeCSV writes rows as CSV with every non-null value quoted and NULL as
// an unquoted \N.
func writeCSV(w io.Writer, width int, rows [][]db.Cell) error {
	bw := bufio.NewWriterSize(w, 64<<10)
	for n, row := range rows {
		if len(row) != width {
			return fmt.Errorf("postgres: row %d has %d values, want %d", n+1, len(row), width)
		}
		for i, c := range row {
			if i > 0 {
				bw.WriteByte(',')
			}
			if c.Null {
				bw.WriteString(`\N`)
				continue
			}
			bw.WriteByte('"')
			bw.WriteString(strings.ReplaceAll(c.S, `"`, `""`))
			bw.WriteByte('"')
		}
		if err := bw.WriteByte('\n'); err != nil {
			return err
		}
	}
	return bw.Flush()
}
