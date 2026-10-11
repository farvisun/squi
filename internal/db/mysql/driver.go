// Package mysql implements the squi database driver for MySQL and MariaDB
// on top of go-sql-driver/mysql.
package mysql

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/farvisun/squi/internal/db"
)

func init() { db.Register(Driver{}) }

// Driver opens MySQL and MariaDB connections.
type Driver struct{}

// Name implements db.Driver.
func (Driver) Name() db.DriverName { return db.MySQL }

// Open implements db.Driver. It verifies connectivity and detects the
// server flavour (MySQL or MariaDB) and version.
func (Driver) Open(ctx context.Context, cfg db.ConnConfig, dial db.DialFunc) (db.Conn, error) {
	mc, err := driverConfig(cfg, dial)
	if err != nil {
		return nil, err
	}
	metaCfg := mc.Clone()
	metaCfg.InterpolateParams = true // fewer round trips for catalog queries
	metaConnector, err := mysql.NewConnector(metaCfg)
	if err != nil {
		return nil, err
	}
	sessConnector, err := mysql.NewConnector(mc)
	if err != nil {
		return nil, err
	}

	c := &conn{
		cfg:  cfg,
		meta: sql.OpenDB(metaConnector),
		sess: sql.OpenDB(sessConnector),
	}
	c.meta.SetMaxOpenConns(3)
	c.meta.SetConnMaxIdleTime(5 * time.Minute)
	c.sess.SetMaxIdleConns(0) // a closed session releases its server connection

	var version string
	if err := c.meta.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version); err != nil {
		c.Close()
		return nil, fmt.Errorf("connect to %s as %s: %w", mc.Addr, cfg.User, wrapErr(err))
	}
	c.parseVersion(version)
	return c, nil
}

// driverConfig maps a db.ConnConfig onto a go-sql-driver configuration.
// Extra cfg.Params are applied as DSN parameters (driver options or
// session system variables) and override squi's defaults.
func driverConfig(cfg db.ConnConfig, dial db.DialFunc) (*mysql.Config, error) {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	params := url.Values{
		"parseTime":            {"false"},
		"charset":              {"utf8mb4"},
		"interpolateParams":    {"false"},
		"multiStatements":      {"false"},
		"allowNativePasswords": {"true"},
		"timeout":              {timeout.String()},
	}
	for k, v := range cfg.Params {
		params.Set(k, v)
	}
	mc, err := mysql.ParseDSN("/?" + params.Encode())
	if err != nil {
		return nil, fmt.Errorf("mysql parameters: %w", err)
	}

	port := cfg.Port
	if port == 0 {
		port = db.DefaultPort(db.MySQL)
	}
	host := cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	mc.Logger = &mysql.NopLogger{} // never write to the terminal behind the TUI
	mc.Net = "tcp"
	mc.Addr = net.JoinHostPort(host, strconv.Itoa(port))
	mc.User = cfg.User
	mc.Passwd = cfg.Password
	mc.DBName = cfg.Database
	if dial != nil {
		mc.DialFunc = dial
	}
	if err := applyTLS(mc, cfg, host); err != nil {
		return nil, err
	}
	return mc, nil
}

// applyTLS maps libpq-style SSL modes onto the driver's TLS settings.
func applyTLS(mc *mysql.Config, cfg db.ConnConfig, host string) error {
	mc.TLS, mc.AllowFallbackToPlaintext = nil, false
	switch cfg.SSLMode {
	case "disable":
		return nil
	case "", "prefer":
		mc.TLS = &tls.Config{InsecureSkipVerify: true}
		mc.AllowFallbackToPlaintext = true
		return nil
	case "require":
		mc.TLS = &tls.Config{InsecureSkipVerify: true}
		return nil
	case "verify-ca", "verify-full":
	default:
		return fmt.Errorf("unknown sslmode %q", cfg.SSLMode)
	}

	tc := &tls.Config{ServerName: host}
	if cfg.SSLRootCert != "" {
		pem, err := os.ReadFile(cfg.SSLRootCert)
		if err != nil {
			return fmt.Errorf("read sslrootcert: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return fmt.Errorf("sslrootcert %s: no certificates found", cfg.SSLRootCert)
		}
		tc.RootCAs = pool
	}
	if cfg.SSLCert != "" || cfg.SSLKey != "" {
		cert, err := tls.LoadX509KeyPair(cfg.SSLCert, cfg.SSLKey)
		if err != nil {
			return fmt.Errorf("load client certificate: %w", err)
		}
		tc.Certificates = []tls.Certificate{cert}
	}
	if cfg.SSLMode == "verify-ca" {
		// Verify the chain but not the host name.
		roots := tc.RootCAs
		tc.InsecureSkipVerify = true
		tc.VerifyPeerCertificate = func(raw [][]byte, _ [][]*x509.Certificate) error {
			certs := make([]*x509.Certificate, len(raw))
			for i, b := range raw {
				c, err := x509.ParseCertificate(b)
				if err != nil {
					return err
				}
				certs[i] = c
			}
			if len(certs) == 0 {
				return errors.New("server sent no certificate")
			}
			opts := x509.VerifyOptions{Roots: roots, Intermediates: x509.NewCertPool()}
			for _, c := range certs[1:] {
				opts.Intermediates.AddCert(c)
			}
			_, err := certs[0].Verify(opts)
			return err
		}
	}
	mc.TLS = tc
	return nil
}

// conn is a db.Conn. meta serves catalog/admin queries; sess hands out the
// dedicated connections behind Sessions.
type conn struct {
	cfg     db.ConnConfig
	meta    *sql.DB
	sess    *sql.DB
	version string // e.g. "8.4.2" or "11.4.3"
	mariadb bool
	ver     [3]int
}

var versionRe = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

func (c *conn) parseVersion(v string) {
	c.mariadb = strings.Contains(strings.ToLower(v), "mariadb")
	v = strings.TrimPrefix(v, "5.5.5-") // legacy MariaDB replication prefix
	if m := versionRe.FindStringSubmatch(v); m != nil {
		for i := range c.ver {
			c.ver[i], _ = strconv.Atoi(m[i+1])
		}
		c.version = m[0]
	} else {
		c.version = v
	}
}

// mysqlAtLeast reports whether the server is MySQL (not MariaDB) of at
// least the given version.
func (c *conn) mysqlAtLeast(major, minor, patch int) bool {
	if c.mariadb {
		return false
	}
	want := [3]int{major, minor, patch}
	for i := range want {
		if c.ver[i] != want[i] {
			return c.ver[i] > want[i]
		}
	}
	return true
}

func (c *conn) Dialect() db.Dialect { return db.Dialect{Driver: db.MySQL} }

// ServerVersion returns e.g. "MySQL 8.4.2" or "MariaDB 11.4.3".
func (c *conn) ServerVersion() string {
	if c.mariadb {
		return "MariaDB " + c.version
	}
	return "MySQL " + c.version
}

func (c *conn) Ping(ctx context.Context) error { return wrapErr(c.meta.PingContext(ctx)) }

func (c *conn) Close() error {
	return errors.Join(c.meta.Close(), c.sess.Close())
}

// kill runs KILL [QUERY] id through the metadata pool.
func (c *conn) kill(ctx context.Context, id uint64, queryOnly bool) error {
	q := "KILL "
	if queryOnly {
		q += "QUERY "
	}
	_, err := c.meta.ExecContext(ctx, q+strconv.FormatUint(id, 10))
	return wrapErr(err)
}

// QueryError is a server error reported for a statement.
type QueryError struct {
	Number   uint16
	SQLState string
	Message  string
	Line     int // 1-based line of a syntax error, 0 if unknown
}

func (e *QueryError) Error() string {
	if e.SQLState != "" {
		return fmt.Sprintf("ERROR %d (%s): %s", e.Number, e.SQLState, e.Message)
	}
	return fmt.Sprintf("ERROR %d: %s", e.Number, e.Message)
}

var atLineRe = regexp.MustCompile(`at line (\d+)\s*$`)

// wrapErr converts driver errors into *QueryError; others pass through.
func wrapErr(err error) error {
	var me *mysql.MySQLError
	if !errors.As(err, &me) {
		return err
	}
	qe := &QueryError{Number: me.Number, Message: me.Message}
	if me.SQLState != [5]byte{} {
		qe.SQLState = string(me.SQLState[:])
	}
	if m := atLineRe.FindStringSubmatch(me.Message); m != nil {
		qe.Line, _ = strconv.Atoi(m[1])
	}
	return qe
}

// errNumber returns the server error number of err, or 0.
func errNumber(err error) uint16 {
	var qe *QueryError
	if errors.As(err, &qe) {
		return qe.Number
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		return me.Number
	}
	return 0
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

// queryTable runs q on the metadata pool and returns all rows as text.
func (c *conn) queryTable(ctx context.Context, q string, args ...any) (*db.Table, error) {
	rows, err := c.meta.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	t := &db.Table{Columns: cols}
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make([]db.Cell, len(cols))
		for i, v := range vals {
			row[i] = nullCell(v)
		}
		t.Rows = append(t.Rows, row)
	}
	return t, wrapErr(rows.Err())
}

func nullCell(v sql.NullString) db.Cell {
	if !v.Valid {
		return db.NullCell
	}
	return db.Text(v.String)
}
