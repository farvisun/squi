// Package mssql implements the squi database driver for Microsoft SQL
// Server and Azure SQL on top of microsoft/go-mssqldb (pure Go, TDS).
package mssql

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	mssql "github.com/microsoft/go-mssqldb"
	"github.com/microsoft/go-mssqldb/msdsn"

	"github.com/farvisun/squi/internal/db"
)

func init() { db.Register(Driver{}) }

// Driver opens SQL Server connections.
type Driver struct{}

// Name implements db.Driver.
func (Driver) Name() db.DriverName { return db.SQLServer }

// Open implements db.Driver. It verifies connectivity and reads the
// server version.
func (Driver) Open(ctx context.Context, cfg db.ConnConfig, dial db.DialFunc) (db.Conn, error) {
	mc, err := driverConfig(cfg)
	if err != nil {
		return nil, err
	}
	connector := func() *mssql.Connector {
		cn := mssql.NewConnectorConfig(mc)
		if dial != nil {
			// a HostDialer gets the unresolved host name, so DNS is
			// resolved on the far side of an SSH tunnel
			cn.Dialer = hostDialer{dial: dial, host: mc.Host}
		}
		return cn
	}
	c := &conn{
		cfg:  cfg,
		meta: sql.OpenDB(connector()),
		sess: sql.OpenDB(connector()),
	}
	c.meta.SetMaxOpenConns(3)
	c.meta.SetConnMaxIdleTime(5 * time.Minute)
	c.sess.SetMaxIdleConns(0) // a closed session releases its server connection

	var version, edition string
	var engine int
	err = c.meta.QueryRowContext(ctx, `SELECT CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(128)),
		CAST(SERVERPROPERTY('Edition') AS nvarchar(128)), CAST(SERVERPROPERTY('EngineEdition') AS int)`).
		Scan(&version, &edition, &engine)
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("connect to %s as %s: %w", net.JoinHostPort(mc.Host, strconv.Itoa(int(mc.Port))), cfg.User, wrapErr(err))
	}
	c.version, c.edition, c.azure = version, edition, engine == 5 || engine == 8
	return c, nil
}

// hostDialer routes connections through a db.DialFunc.
type hostDialer struct {
	dial db.DialFunc
	host string
}

func (d hostDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return d.dial(ctx, network, addr)
}

func (d hostDialer) HostName() string { return d.host }

// driverConfig maps a db.ConnConfig onto a go-mssqldb configuration. Extra
// cfg.Params are connection string parameters (e.g. "app name",
// ApplicationIntent, "packet size") and override squi's defaults.
func driverConfig(cfg db.ConnConfig) (msdsn.Config, error) {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	host := cfg.Host
	if host == "" {
		host = "localhost"
	}
	port := cfg.Port
	if port == 0 {
		port = db.DefaultPort(db.SQLServer)
	}
	q := url.Values{
		"app name":     {"squi"},
		"dial timeout": {strconv.Itoa(int(timeout.Seconds()))},
	}
	switch cfg.SSLMode {
	case "disable": // only the login packet is encrypted
		q.Set("encrypt", "false")
		q.Set("trustservercertificate", "true")
	case "", "prefer", "require":
		q.Set("encrypt", "true")
		q.Set("trustservercertificate", "true")
	case "verify-ca", "verify-full":
		q.Set("encrypt", "true")
		q.Set("trustservercertificate", "false")
	default:
		return msdsn.Config{}, fmt.Errorf("unknown sslmode %q", cfg.SSLMode)
	}
	if cfg.Database != "" {
		q.Set("database", cfg.Database)
	}
	for k, v := range cfg.Params {
		q.Set(k, v)
	}
	u := url.URL{
		Scheme:   "sqlserver",
		User:     url.UserPassword(cfg.User, cfg.Password),
		Host:     net.JoinHostPort(host, strconv.Itoa(port)),
		RawQuery: q.Encode(),
	}
	mc, err := msdsn.Parse(u.String())
	if err != nil {
		return msdsn.Config{}, fmt.Errorf("sqlserver parameters: %w", err)
	}
	if cfg.SSLMode == "verify-ca" || cfg.SSLMode == "verify-full" {
		tc, err := verifyingTLS(cfg, host)
		if err != nil {
			return msdsn.Config{}, err
		}
		mc.TLSConfig = tc
	}
	return mc, nil
}

// verifyingTLS builds the TLS configuration of the verify-ca (chain only)
// and verify-full (chain and host name) modes.
func verifyingTLS(cfg db.ConnConfig, host string) (*tls.Config, error) {
	tc := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if cfg.SSLRootCert != "" {
		pem, err := os.ReadFile(cfg.SSLRootCert)
		if err != nil {
			return nil, fmt.Errorf("read sslrootcert: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("sslrootcert %s: no certificates found", cfg.SSLRootCert)
		}
		tc.RootCAs = pool
	}
	if cfg.SSLMode == "verify-ca" {
		roots := tc.RootCAs
		tc.InsecureSkipVerify = true
		tc.VerifyPeerCertificate = func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("server sent no certificate")
			}
			certs := make([]*x509.Certificate, len(raw))
			for i, b := range raw {
				c, err := x509.ParseCertificate(b)
				if err != nil {
					return err
				}
				certs[i] = c
			}
			opts := x509.VerifyOptions{Roots: roots, Intermediates: x509.NewCertPool()}
			for _, c := range certs[1:] {
				opts.Intermediates.AddCert(c)
			}
			_, err := certs[0].Verify(opts)
			return err
		}
	}
	return tc, nil
}

// conn is a db.Conn. meta serves catalog/admin queries; sess hands out the
// dedicated connections behind Sessions.
type conn struct {
	cfg     db.ConnConfig
	meta    *sql.DB
	sess    *sql.DB
	version string // ProductVersion, e.g. "16.0.4135.4"
	edition string
	azure   bool
}

func (c *conn) Dialect() db.Dialect { return db.Dialect{Driver: db.SQLServer} }

// releases maps major versions to product names.
var releases = map[string]string{"17": "2025", "16": "2022", "15": "2019", "14": "2017", "13": "2016", "12": "2014", "11": "2012"}

// ServerVersion returns e.g. "SQL Server 2022 (16.0.4135.4)" or
// "Azure SQL 12.0.2000.8".
func (c *conn) ServerVersion() string {
	if c.azure {
		return "Azure SQL " + c.version
	}
	major, _, _ := strings.Cut(c.version, ".")
	if r, ok := releases[major]; ok {
		return "SQL Server " + r + " (" + c.version + ")"
	}
	return "SQL Server " + c.version
}

func (c *conn) Ping(ctx context.Context) error { return wrapErr(c.meta.PingContext(ctx)) }

func (c *conn) Close() error { return errors.Join(c.meta.Close(), c.sess.Close()) }

// QueryError is a server error reported for a statement.
type QueryError struct {
	Number  int32
	State   uint8
	Class   uint8 // severity
	Message string
	Proc    string
	Line    int // 1-based line in the batch, 0 if unknown
}

func (e *QueryError) Error() string {
	s := fmt.Sprintf("Msg %d, Level %d, State %d", e.Number, e.Class, e.State)
	if e.Proc != "" {
		s += ", Procedure " + e.Proc
	}
	if e.Line > 0 {
		s += ", Line " + strconv.Itoa(e.Line)
	}
	return s + ": " + e.Message
}

// Error numbers the UI distinguishes.
const (
	ErrLoginFailed     = 18456
	ErrCannotOpenDB    = 4060
	ErrQueryCancelled  = 3617 // the attention a cancel sends
	ErrUnknownDatabase = 911
	ErrLoginDBFallback = 4063 // requested database not opened at login
)

// wrapErr converts driver errors into *QueryError; others pass through.
func wrapErr(err error) error {
	var me mssql.Error
	if !errors.As(err, &me) {
		return err
	}
	// with several messages (e.g. a batch), report the first real error
	e := me
	for _, x := range me.All {
		if x.Class > 10 {
			e = x
			break
		}
	}
	return &QueryError{Number: e.Number, State: e.State, Class: e.Class, Message: e.Message, Proc: e.ProcName, Line: int(e.LineNo)}
}

// errNumber returns the server error number of err, or 0.
func errNumber(err error) int32 {
	var qe *QueryError
	if errors.As(err, &qe) {
		return qe.Number
	}
	var me mssql.Error
	if errors.As(err, &me) {
		return me.Number
	}
	return 0
}

// IsAuthError reports a failed login.
func IsAuthError(err error) bool { return errNumber(err) == ErrLoginFailed }

// IsUnknownDatabase reports a login that failed because the database does
// not exist or is not accessible.
func IsUnknownDatabase(err error) bool {
	n := errNumber(err)
	return n == ErrCannotOpenDB || n == ErrUnknownDatabase || n == ErrLoginDBFallback
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

// cellOf renders a value scanned from go-mssqldb as text. typ is the
// upper-case database type name of the column.
func cellOf(v any, typ string) db.Cell {
	switch x := v.(type) {
	case nil:
		return db.NullCell
	case string:
		return db.Text(x)
	case []byte:
		switch typ {
		case "UNIQUEIDENTIFIER":
			var u mssql.UniqueIdentifier
			if u.Scan(x) == nil {
				return db.Text(u.String())
			}
		case "BINARY", "VARBINARY", "IMAGE", "TIMESTAMP", "ROWVERSION", "UDT":
			return db.Text("0x" + strings.ToUpper(hex.EncodeToString(x)))
		}
		return db.Text(string(x))
	case int64:
		return db.Text(strconv.FormatInt(x, 10))
	case float64:
		bits := 64
		if typ == "REAL" {
			bits = 32
		}
		return db.Text(strconv.FormatFloat(x, 'g', -1, bits))
	case float32:
		return db.Text(strconv.FormatFloat(float64(x), 'g', -1, 32))
	case bool:
		if x {
			return db.Text("1")
		}
		return db.Text("0")
	case time.Time:
		return db.Text(formatTime(x, typ))
	}
	return db.Text(fmt.Sprint(v))
}

func formatTime(t time.Time, typ string) string {
	switch typ {
	case "DATE":
		return t.Format("2006-01-02")
	case "TIME":
		return t.Format("15:04:05.9999999")
	case "DATETIMEOFFSET":
		return t.Format("2006-01-02 15:04:05.9999999 -07:00")
	case "SMALLDATETIME":
		return t.Format("2006-01-02 15:04:05")
	}
	return t.Format("2006-01-02 15:04:05.9999999")
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
			row[i] = cellOf(v, types[i].DatabaseTypeName())
		}
		t.Rows = append(t.Rows, row)
	}
	return t, wrapErr(rows.Err())
}

// prefix returns "[database]." for three-part catalog names, or "" for the
// connection's own database.
func (c *conn) prefix(database string) string {
	if database == "" {
		return ""
	}
	return c.Dialect().QuoteIdent(database) + "."
}
