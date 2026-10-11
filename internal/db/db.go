// Package db defines the driver-neutral interfaces and types squi uses to
// talk to database engines. Engine-specific code lives in sub-packages.
package db

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"
)

// DriverName identifies a database engine.
type DriverName string

const (
	Postgres   DriverName = "postgres"
	MySQL      DriverName = "mysql"
	ClickHouse DriverName = "clickhouse"
	SQLServer  DriverName = "sqlserver"
	SQLite     DriverName = "sqlite"
)

// MySQLStyle reports engines with MySQL's SQL surface: databases instead of
// schemas, backtick-quoted identifiers and backslash escapes in strings.
func (d DriverName) MySQLStyle() bool { return d == MySQL || d == ClickHouse }

// DialFunc opens a raw network connection. It is used to route database
// traffic through an SSH tunnel; nil means a plain net.Dialer.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// ConnConfig is everything a driver needs to open a connection.
type ConnConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	Database string

	SSLMode     string // disable | prefer | require | verify-ca | verify-full
	SSLRootCert string
	SSLCert     string
	SSLKey      string

	ReadOnly bool
	Params   map[string]string // extra driver parameters
	Timeout  time.Duration     // connect timeout; 0 = driver default (10s)
}

// Driver opens connections for one engine.
type Driver interface {
	Name() DriverName
	Open(ctx context.Context, cfg ConnConfig, dial DialFunc) (Conn, error)
}

// Conn is a connection "profile" opened against a server. It owns a small
// metadata pool shared by every tab and hands out dedicated Sessions.
//
// For Postgres a database is a separate connection target, so methods that
// take a db name transparently connect to that database. For MySQL, databases
// and schemas are the same thing: Schemas(db) returns []string{db}.
type Conn interface {
	Dialect() Dialect
	ServerVersion() string
	Ping(ctx context.Context) error

	Databases(ctx context.Context) ([]Database, error)
	Schemas(ctx context.Context, database string) ([]string, error)
	Objects(ctx context.Context, database, schema string) ([]Object, error)
	// AllColumns returns table/view name -> ordered column names for a
	// schema; used by autocomplete.
	AllColumns(ctx context.Context, database, schema string) (map[string][]string, error)
	TableDetail(ctx context.Context, ref ObjectRef) (*TableDetail, error)
	DDL(ctx context.Context, ref ObjectRef) (string, error)

	NewSession(ctx context.Context, database string) (Session, error)
	Admin() Admin
	Close() error
}

// Session is a dedicated server connection backing one query tab. It keeps
// transaction state, the current database / search_path and SET variables.
// A Session is not safe for concurrent use, except Cancel, which may be
// called from any goroutine while Run or a ResultStream is in progress.
type Session interface {
	// Run executes a single statement. The returned stream must be closed
	// before the next call to Run.
	Run(ctx context.Context, sql string) (ResultStream, error)
	Begin(ctx context.Context) error
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
	InTx() bool
	// Cancel aborts the statement currently running on this session.
	Cancel(ctx context.Context) error
	UseDatabase(ctx context.Context, name string) error
	CurrentDatabase() string
	// Explain returns the plan for sql. With analyze, the statement is
	// actually executed; callers are responsible for wrapping writes in a
	// transaction that is rolled back.
	Explain(ctx context.Context, sql string, analyze bool) (*PlanNode, error)
	// BulkInsert loads rows into a table (COPY for Postgres, batched
	// INSERTs for MySQL). Values are text; Null cells become NULL.
	BulkInsert(ctx context.Context, ref ObjectRef, columns []string, rows [][]Cell) (int64, error)
	Close() error
}

// RowEstimator is implemented by connections that can cheaply estimate a
// table's row count from statistics (no table scan). ok is false when the
// server has no estimate (e.g. a view, or a table never analyzed).
type RowEstimator interface {
	EstimateRows(ctx context.Context, ref ObjectRef) (n int64, exact, ok bool, err error)
}

// Cell is a single value in its text representation.
type Cell struct {
	S    string
	Null bool
}

// NullCell is the SQL NULL value.
var NullCell = Cell{Null: true}

// Text returns a non-null cell.
func Text(s string) Cell { return Cell{S: s} }

// ResultColumn describes a result set column.
type ResultColumn struct {
	Name string
	Type string // engine type name, e.g. "int4", "varchar"
}

// ResultStream is a lazily-consumed result of one statement.
type ResultStream interface {
	// Columns is empty for statements that return no rows.
	Columns() []ResultColumn
	// Next returns up to n further rows. It returns io.EOF (and possibly a
	// final short batch) once the result is exhausted.
	Next(n int) ([][]Cell, error)
	// RowsAffected is valid after the stream is exhausted.
	RowsAffected() int64
	// CommandTag is a short status like "SELECT 10" or "UPDATE 3".
	CommandTag() string
	Close() error
}

// Database is a database on the server.
type Database struct {
	Name    string
	Size    string // human readable, may be empty
	Comment string
}

// ObjectKind is the kind of a schema object.
type ObjectKind string

const (
	KindTable     ObjectKind = "table"
	KindView      ObjectKind = "view"
	KindMatView   ObjectKind = "matview"
	KindFunction  ObjectKind = "function"
	KindProcedure ObjectKind = "procedure"
	KindSequence  ObjectKind = "sequence"
	KindTrigger   ObjectKind = "trigger"
	KindEvent     ObjectKind = "event"
)

// Object is an item listed in the explorer.
type Object struct {
	Schema  string
	Name    string
	Kind    ObjectKind
	Comment string
}

// ObjectRef identifies a schema object.
type ObjectRef struct {
	Database string
	Schema   string
	Name     string
	Kind     ObjectKind
}

// Ref returns a reference to o inside database.
func (o Object) Ref(database string) ObjectRef {
	return ObjectRef{Database: database, Schema: o.Schema, Name: o.Name, Kind: o.Kind}
}

// Column describes a table column.
type Column struct {
	Position int
	Name     string
	Type     string
	Nullable bool
	Default  *string
	IsPK     bool
	Extra    string // e.g. auto_increment, identity, generated
	Comment  string
}

// Index describes a table index.
type Index struct {
	Name       string
	Columns    []string
	Unique     bool
	Primary    bool
	Method     string // btree, hash, gin...
	Size       string
	Definition string
}

// ForeignKey describes a foreign key. For incoming keys (TableDetail.ReferencedBy)
// Schema/Table identify the referencing table.
type ForeignKey struct {
	Name       string
	Schema     string
	Table      string
	Columns    []string
	RefSchema  string
	RefTable   string
	RefColumns []string
	OnUpdate   string
	OnDelete   string
}

// Constraint is a check/unique/exclusion/other constraint.
type Constraint struct {
	Name       string
	Type       string
	Definition string
}

// Trigger describes a table trigger.
type Trigger struct {
	Name       string
	Timing     string // BEFORE, AFTER, INSTEAD OF
	Event      string // INSERT, UPDATE...
	Definition string
}

// KV is an ordered key/value pair used for stats.
type KV struct {
	Key   string
	Value string
}

// TableDetail is everything the detail view shows about a table or view.
type TableDetail struct {
	Ref          ObjectRef
	Comment      string
	Columns      []Column
	Indexes      []Index
	ForeignKeys  []ForeignKey
	ReferencedBy []ForeignKey
	Constraints  []Constraint
	Triggers     []Trigger
	Stats        []KV
}

// PrimaryKey returns the primary key column names, falling back to the
// columns of the first unique index whose columns are all NOT NULL.
func (t *TableDetail) PrimaryKey() []string {
	var pk []string
	for _, c := range t.Columns {
		if c.IsPK {
			pk = append(pk, c.Name)
		}
	}
	if len(pk) > 0 {
		return pk
	}
	for _, ix := range t.Indexes {
		if ix.Primary && len(ix.Columns) > 0 {
			return ix.Columns
		}
	}
	notNull := map[string]bool{}
	for _, c := range t.Columns {
		notNull[c.Name] = !c.Nullable
	}
	for _, ix := range t.Indexes {
		if !ix.Unique || len(ix.Columns) == 0 {
			continue
		}
		ok := true
		for _, c := range ix.Columns {
			if !notNull[c] {
				ok = false
				break
			}
		}
		if ok {
			return ix.Columns
		}
	}
	return nil
}

// Table is a generic grid of values, used by admin views.
type Table struct {
	Columns []string
	Rows    [][]Cell
}

// Admin exposes server administration views.
type Admin interface {
	// Processes lists server sessions. The first column is the id that
	// CancelQuery / Kill accept.
	Processes(ctx context.Context) (*Table, error)
	CancelQuery(ctx context.Context, id string) error
	Kill(ctx context.Context, id string) error
	Variables(ctx context.Context) (*Table, error)
	Users(ctx context.Context) (*Table, error)
	Grants(ctx context.Context, user string) (*Table, error)
	Sizes(ctx context.Context) (*Table, error)
}

// PlanNode is one node of an EXPLAIN plan tree.
type PlanNode struct {
	Title    string  // e.g. "Seq Scan on orders"
	Cost     float64 // total cost (or 0 if unknown); used for hot-path highlighting
	Rows     float64
	Time     float64 // actual total time in ms (ANALYZE only)
	Details  []KV
	Children []*PlanNode
	Raw      string // raw plan text/JSON (root only)
}

// Dialect holds engine-specific SQL formatting rules.
type Dialect struct {
	Driver DriverName
}

// HasSchemas reports whether the engine has a schema level below databases.
func (d Dialect) HasSchemas() bool { return d.Driver == Postgres || d.Driver == SQLServer }

// DefaultSchema is the schema unqualified names resolve to by default
// ("public", "dbo"), or "" for engines without schemas.
func (d Dialect) DefaultSchema() string {
	switch d.Driver {
	case Postgres:
		return "public"
	case SQLServer:
		return "dbo"
	case SQLite:
		return "main"
	}
	return ""
}

// Transactions reports whether the engine supports BEGIN/COMMIT/ROLLBACK.
func (d Dialect) Transactions() bool { return d.Driver != ClickHouse }

// QuoteIdent quotes an identifier.
func (d Dialect) QuoteIdent(s string) string {
	if d.Driver.MySQLStyle() {
		return "`" + strings.ReplaceAll(s, "`", "``") + "`"
	}
	if d.Driver == SQLServer {
		return "[" + strings.ReplaceAll(s, "]", "]]") + "]"
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// QuoteLiteral quotes a string literal.
func (d Dialect) QuoteLiteral(s string) string {
	if d.Driver == ClickHouse {
		return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
	}
	if d.Driver == MySQL {
		r := strings.NewReplacer(`\`, `\\`, `'`, `''`, "\x00", `\0`, "\n", `\n`, "\r", `\r`, "\x1a", `\Z`)
		return "'" + r.Replace(s) + "'"
	}
	if d.Driver == SQLServer {
		// N'' only when needed: comparing an N'' literal with a varchar
		// column can turn an index seek into a scan
		q := "'" + strings.ReplaceAll(s, "'", "''") + "'"
		for i := 0; i < len(s); i++ {
			if s[i] >= 0x80 {
				return "N" + q
			}
		}
		return q
	}
	if strings.Contains(s, `\`) && d.Driver != SQLite {
		return "E'" + strings.NewReplacer(`\`, `\\`, `'`, `''`).Replace(s) + "'"
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// Value renders a cell as a SQL literal.
func (d Dialect) Value(c Cell) string {
	if c.Null {
		return "NULL"
	}
	return d.QuoteLiteral(c.S)
}

// Qualified returns the schema-qualified, quoted name of ref.
func (d Dialect) Qualified(ref ObjectRef) string {
	switch {
	case d.Driver.MySQLStyle() && ref.Database != "":
		return d.QuoteIdent(ref.Database) + "." + d.QuoteIdent(ref.Name)
	case d.Driver.MySQLStyle() && ref.Schema != "":
		return d.QuoteIdent(ref.Schema) + "." + d.QuoteIdent(ref.Name)
	case ref.Schema != "":
		return d.QuoteIdent(ref.Schema) + "." + d.QuoteIdent(ref.Name)
	}
	return d.QuoteIdent(ref.Name)
}

// SelectAll returns a SELECT * … LIMIT n statement for ref.
func (d Dialect) SelectAll(ref ObjectRef, limit int) string {
	return d.Select(Browse{Ref: ref}, limit)
}

// Order is one ORDER BY term.
type Order struct {
	Column string
	Desc   bool
}

// Browse is a server-side view of a table's rows: an optional WHERE
// condition (SQL typed by the user or generated, already checked) and an
// ORDER BY.
type Browse struct {
	Ref   ObjectRef
	Where string
	Order []Order
}

// Select renders b as "SELECT * FROM … [WHERE (…)] [ORDER BY …] [LIMIT n]"
// (SQL Server: SELECT TOP (n) *).
func (d Dialect) Select(b Browse, limit int) string {
	q := "SELECT * FROM " + d.Qualified(b.Ref)
	if d.Driver == SQLServer && limit > 0 {
		q = "SELECT TOP (" + itoa(limit) + ") * FROM " + d.Qualified(b.Ref)
	}
	if w := strings.TrimSpace(b.Where); w != "" {
		q += " WHERE (" + w + ")"
	}
	if len(b.Order) > 0 {
		terms := make([]string, len(b.Order))
		for i, o := range b.Order {
			terms[i] = d.QuoteIdent(o.Column)
			if o.Desc {
				terms[i] += " DESC"
			}
		}
		q += " ORDER BY " + strings.Join(terms, ", ")
	}
	if limit > 0 && d.Driver != SQLServer {
		q += " LIMIT " + itoa(limit)
	}
	return q
}

// Equals renders "col1 = v1 AND col2 = v2"; a NULL value compares with
// IS NULL.
func (d Dialect) Equals(cols []string, vals []Cell) string {
	terms := make([]string, len(cols))
	for i, c := range cols {
		if vals[i].Null {
			terms[i] = d.QuoteIdent(c) + " IS NULL"
		} else {
			terms[i] = d.QuoteIdent(c) + " = " + d.Value(vals[i])
		}
	}
	return strings.Join(terms, " AND ")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// ErrNotSupported is returned for features an engine/version lacks.
var ErrNotSupported = errors.New("not supported by this server")

var drivers = map[DriverName]Driver{}

// Register makes a driver available by name. Called from driver init().
func Register(d Driver) { drivers[d.Name()] = d }

// Get returns a registered driver.
func Get(name DriverName) (Driver, bool) {
	d, ok := drivers[name]
	return d, ok
}

// DefaultPort returns the default TCP port of an engine.
func DefaultPort(name DriverName) int {
	switch name {
	case MySQL:
		return 3306
	case ClickHouse:
		return 8123 // HTTP interface
	case SQLServer:
		return 1433
	case SQLite:
		return 0 // a file
	}
	return 5432
}
