// Package postgres implements the squi database driver for PostgreSQL on top
// of jackc/pgx. Metadata is read through small per-database pools; query tabs
// get dedicated connections that use the simple query protocol so every value
// arrives in PostgreSQL's text format, exactly as psql shows it.
package postgres

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/farvisun/squi/internal/db"
)

func init() { db.Register(Driver{}) }

const (
	defaultDatabase = "postgres"
	defaultSSLMode  = "prefer"
	defaultTimeout  = 10 * time.Second
	poolMaxConns    = 3
)

// dialect is the shared Postgres dialect value.
var dialect = db.Dialect{Driver: db.Postgres}

// Driver is the PostgreSQL db.Driver.
type Driver struct{}

// Name implements db.Driver.
func (Driver) Name() db.DriverName { return db.Postgres }

// Open connects to the server described by cfg, optionally routing all
// traffic (including cancel requests) through dial, and pings it.
func (Driver) Open(ctx context.Context, cfg db.ConnConfig, dial db.DialFunc) (db.Conn, error) {
	if cfg.Database == "" {
		cfg.Database = defaultDatabase
	}
	c := &conn{cfg: cfg, dial: dial, pools: map[string]*pgxpool.Pool{}}
	p, err := c.pool("")
	if err != nil {
		return nil, err
	}
	err = p.AcquireFunc(ctx, func(pc *pgxpool.Conn) error {
		v, _, _ := strings.Cut(pc.Conn().PgConn().ParameterStatus("server_version"), " ")
		c.version = "PostgreSQL " + v
		return pc.Ping(ctx)
	})
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("postgres: connect to %s: %w", c.target(cfg.Database), err)
	}
	return c, nil
}

// conn implements db.Conn.
type conn struct {
	cfg     db.ConnConfig
	dial    db.DialFunc
	version string

	mu    sync.Mutex
	pools map[string]*pgxpool.Pool
}

func (c *conn) Dialect() db.Dialect   { return dialect }
func (c *conn) ServerVersion() string { return c.version }
func (c *conn) Admin() db.Admin       { return admin{c} }

func (c *conn) Ping(ctx context.Context) error {
	p, err := c.pool("")
	if err != nil {
		return err
	}
	return p.Ping(ctx)
}

// Close closes every metadata pool.
func (c *conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for name, p := range c.pools {
		p.Close()
		delete(c.pools, name)
	}
	return nil
}

// pool returns the metadata pool for database ("" = the default database),
// creating it on first use.
func (c *conn) pool(database string) (*pgxpool.Pool, error) {
	if database == "" {
		database = c.cfg.Database
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if p, ok := c.pools[database]; ok {
		return p, nil
	}
	pc, err := pgxpool.ParseConfig(c.connString(database))
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	pc.MaxConns = poolMaxConns
	c.configure(pc.ConnConfig)
	// NewWithConfig does not connect eagerly, so the context is irrelevant
	// here; connection attempts use the context of each query.
	p, err := pgxpool.NewWithConfig(context.Background(), pc)
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	c.pools[database] = p
	return p, nil
}

// connConfig returns a single-connection config for database.
func (c *conn) connConfig(database string) (*pgx.ConnConfig, error) {
	if database == "" {
		database = c.cfg.Database
	}
	cc, err := pgx.ParseConfig(c.connString(database))
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	c.configure(cc)
	return cc, nil
}

// connString renders cfg as a keyword/value connection string. Extra
// Params are appended verbatim, so unknown keys become runtime parameters.
func (c *conn) connString(database string) string {
	kv := map[string]string{
		"host":             c.cfg.Host,
		"user":             c.cfg.User,
		"password":         c.cfg.Password,
		"dbname":           database,
		"sslmode":          c.cfg.SSLMode,
		"sslrootcert":      c.cfg.SSLRootCert,
		"sslcert":          c.cfg.SSLCert,
		"sslkey":           c.cfg.SSLKey,
		"application_name": "squi",
	}
	if c.cfg.Port > 0 {
		kv["port"] = strconv.Itoa(c.cfg.Port)
	}
	if kv["sslmode"] == "" {
		kv["sslmode"] = defaultSSLMode
	}
	for k, v := range c.cfg.Params {
		kv[k] = v
	}
	keys := make([]string, 0, len(kv))
	for k, v := range kv {
		if v != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(k + "='" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(kv[k]) + "'")
	}
	return b.String()
}

// configure applies the connect timeout and the custom dialer to cc and to
// its TLS fallbacks.
func (c *conn) configure(cc *pgx.ConnConfig) {
	cc.ConnectTimeout = c.cfg.Timeout
	if cc.ConnectTimeout <= 0 {
		cc.ConnectTimeout = defaultTimeout
	}
	if c.dial == nil {
		return
	}
	cc.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) {
		nc, err := c.dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return addrConn{nc, tunnelAddr{network, addr}}, nil
	}
	// Resolve host names on the far side of the tunnel.
	cc.LookupFunc = func(_ context.Context, host string) ([]string, error) {
		return []string{host}, nil
	}
}

func (c *conn) target(database string) string {
	host := c.cfg.Host
	if host == "" {
		host = "localhost"
	}
	if c.cfg.Port > 0 {
		host = net.JoinHostPort(host, strconv.Itoa(c.cfg.Port))
	}
	return host + "/" + database
}

// addrConn reports the address it was dialed with as its remote address.
// pgconn sends cancel requests to RemoteAddr(), which for tunnelled
// connections is otherwise often unusable (e.g. an unresolved host name).
type addrConn struct {
	net.Conn
	raddr tunnelAddr
}

func (a addrConn) RemoteAddr() net.Addr { return a.raddr }

type tunnelAddr struct{ network, addr string }

func (t tunnelAddr) Network() string { return t.network }
func (t tunnelAddr) String() string  { return t.addr }
