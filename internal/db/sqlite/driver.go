// Package sqlite implements the squi database driver for SQLite files on
// top of modernc.org/sqlite (pure Go: the binary stays static).
//
// A connection's Database is the file path (":memory:" for a scratch
// database). SQLite databases are local files, so SSH tunnels do not
// apply. Attached databases appear as further databases.
package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"modernc.org/sqlite"

	"github.com/farvisun/squi/internal/db"
)

func init() { db.Register(Driver{}) }

// Driver opens SQLite files.
type Driver struct{}

// Name implements db.Driver.
func (Driver) Name() db.DriverName { return db.SQLite }

// ErrNoFile is returned when the database file does not exist; set the
// "mode" parameter to "rwc" to create it.
var ErrNoFile = errors.New("database file not found (add ?mode=rwc to a sqlite: URL to create it)")

// Open implements db.Driver.
func (Driver) Open(ctx context.Context, cfg db.ConnConfig, dial db.DialFunc) (db.Conn, error) {
	if dial != nil {
		return nil, errors.New("sqlite opens local files: disable the SSH tunnel for this connection")
	}
	dsn, path, err := dataSource(cfg)
	if err != nil {
		return nil, err
	}
	c := &conn{cfg: cfg, path: path}
	if c.meta, err = sql.Open("sqlite", dsn); err != nil {
		return nil, err
	}
	if c.sess, err = sql.Open("sqlite", dsn); err != nil {
		c.meta.Close()
		return nil, err
	}
	c.meta.SetMaxOpenConns(2)
	c.meta.SetConnMaxIdleTime(5 * time.Minute)
	c.sess.SetMaxIdleConns(0)
	if path == "" {
		// a shared in-memory database lives as long as a connection to it
		c.meta.SetMaxIdleConns(2)
		c.meta.SetConnMaxIdleTime(0)
	}
	if err := c.meta.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&c.version); err != nil {
		c.Close()
		return nil, fmt.Errorf("open %s: %w", cfg.Database, wrapErr(err))
	}
	return c, nil
}

// dataSource builds the driver DSN: a file: URI with the open mode and
// pragmas. Extra cfg.Params are URI parameters (mode, cache, immutable,
// vfs) or _pragma=name(value) settings.
func dataSource(cfg db.ConnConfig) (dsn, path string, err error) {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	switch name := strings.TrimSpace(cfg.Database); name {
	case "":
		return "", "", errors.New("sqlite: set Database to the file path (or :memory:)")
	case ":memory:":
		var b [6]byte
		_, _ = rand.Read(b[:])
		q.Set("mode", "memory")
		q.Set("cache", "shared") // the metadata pool and the sessions see one database
		for k, v := range cfg.Params {
			q.Set(k, v)
		}
		return "file:squi-" + hex.EncodeToString(b[:]) + "?" + q.Encode(), "", nil
	default:
		path, err = filepath.Abs(name)
		if err != nil {
			return "", "", err
		}
	}
	q.Set("mode", "rw") // a typo should not create an empty database
	if cfg.ReadOnly {
		q.Set("mode", "ro")
		q.Add("_pragma", "query_only(1)")
	}
	for k, v := range cfg.Params {
		if k == "_pragma" {
			q.Add(k, v)
		} else {
			q.Set(k, v)
		}
	}
	if q.Get("mode") != "rwc" && q.Get("mode") != "memory" {
		if _, err := os.Stat(path); err != nil {
			return "", "", fmt.Errorf("%s: %w", path, ErrNoFile)
		}
	}
	u := url.URL{Scheme: "file", Opaque: (&url.URL{Path: path}).EscapedPath(), RawQuery: q.Encode()}
	return u.String(), path, nil
}

// conn is a db.Conn. meta serves catalog queries; sess hands out the
// dedicated connections behind Sessions.
type conn struct {
	cfg     db.ConnConfig
	path    string // absolute file path; "" for :memory:
	meta    *sql.DB
	sess    *sql.DB
	version string
}

func (c *conn) Dialect() db.Dialect { return db.Dialect{Driver: db.SQLite} }

func (c *conn) ServerVersion() string { return "SQLite " + c.version }

func (c *conn) Ping(ctx context.Context) error { return wrapErr(c.meta.PingContext(ctx)) }

func (c *conn) Close() error { return errors.Join(c.sess.Close(), c.meta.Close()) }

// QueryError is an error reported by SQLite.
type QueryError struct {
	Code    int // primary result code, e.g. 1 (SQLITE_ERROR), 19 (constraint)
	Message string
}

func (e *QueryError) Error() string { return e.Message }

// Result codes the UI distinguishes.
const (
	CodeInterrupt = 9
	CodeBusy      = 5
)

// wrapErr converts driver errors into *QueryError; others pass through.
func wrapErr(err error) error {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return err
	}
	msg := se.Error()
	// "SQL logic error: no such table: x (1)" → "no such table: x"
	if i := strings.LastIndex(msg, " ("); i > 0 && strings.HasSuffix(msg, ")") {
		msg = msg[:i]
	}
	if _, rest, ok := strings.Cut(msg, ": "); ok && strings.Contains(sqliteGeneric, msg[:len(msg)-len(rest)-2]) {
		msg = rest
	}
	return &QueryError{Code: se.Code() & 0xff, Message: msg}
}

// sqliteGeneric are sqlite3_errstr texts that prefix detailed messages.
const sqliteGeneric = "SQL logic error|constraint failed|database is locked|attempt to write a readonly database|interrupted"

// IsInterrupted reports a statement stopped by Cancel: the driver returns
// the context's error, or SQLITE_INTERRUPT when the interrupt lands first.
func IsInterrupted(err error) bool {
	var qe *QueryError
	return errors.Is(err, context.Canceled) || errors.As(err, &qe) && qe.Code == CodeInterrupt
}

// humanSize formats a byte count like "12.3 MB".
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// cellOf renders a value scanned from the driver as text. typ is the
// upper-case declared type of the column.
func cellOf(v any, typ string) db.Cell {
	switch x := v.(type) {
	case nil:
		return db.NullCell
	case string:
		return db.Text(x)
	case []byte:
		if !strings.Contains(typ, "BLOB") && utf8.Valid(x) {
			return db.Text(string(x))
		}
		return db.Text("x'" + strings.ToUpper(hex.EncodeToString(x)) + "'")
	case int64:
		return db.Text(strconv.FormatInt(x, 10))
	case float64:
		return db.Text(strconv.FormatFloat(x, 'g', -1, 64))
	case bool:
		if x {
			return db.Text("1")
		}
		return db.Text("0")
	case time.Time:
		if strings.Contains(typ, "DATE") && !strings.Contains(typ, "TIME") && x.Hour() == 0 && x.Minute() == 0 && x.Second() == 0 && x.Nanosecond() == 0 {
			return db.Text(x.Format("2006-01-02"))
		}
		if x.Location() == time.UTC {
			return db.Text(x.Format("2006-01-02 15:04:05.999999999"))
		}
		return db.Text(x.Format("2006-01-02 15:04:05.999999999-07:00"))
	}
	return db.Text(fmt.Sprint(v))
}

// queryTable runs q on the metadata pool and returns all rows as text.
func (c *conn) queryTable(ctx context.Context, q string, args ...any) (*db.Table, error) {
	rows, err := c.meta.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer rows.Close()
	types, err := rows.ColumnTypes()
	if err != nil {
		return nil, wrapErr(err)
	}
	t := &db.Table{}
	for _, ct := range types {
		t.Columns = append(t.Columns, ct.Name())
	}
	vals := make([]any, len(types))
	ptrs := make([]any, len(types))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make([]db.Cell, len(types))
		for i, v := range vals {
			row[i] = cellOf(v, strings.ToUpper(types[i].DatabaseTypeName()))
		}
		t.Rows = append(t.Rows, row)
	}
	return t, wrapErr(rows.Err())
}

// schemaName defaults an empty database name to "main".
func schemaName(database string) string {
	if database == "" {
		return "main"
	}
	return database
}

func (c *conn) quote(s string) string { return c.Dialect().QuoteIdent(s) }
