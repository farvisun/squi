package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/farvisun/squi/internal/config"
	"github.com/farvisun/squi/internal/db"
	"github.com/farvisun/squi/internal/db/clickhouse" // registers the driver
	"github.com/farvisun/squi/internal/db/mssql"
	_ "github.com/farvisun/squi/internal/db/mysql" // register driver
	_ "github.com/farvisun/squi/internal/db/postgres"
	_ "github.com/farvisun/squi/internal/db/sqlite"
	"github.com/farvisun/squi/internal/schema"
	"github.com/farvisun/squi/internal/secrets"
	"github.com/farvisun/squi/internal/sshx"
)

// activeConn is an open connection profile shared by the tabs using it.
type activeConn struct {
	cfg      config.Connection
	conn     db.Conn
	tunnel   *sshx.Tunnel
	cache    *schema.Cache
	explorer *explorer
	refs     int
	// cacheLoaded records databases whose autocomplete metadata is loaded
	// (or loading).
	cacheLoaded map[string]bool
}

func (a *activeConn) dialect() db.Dialect { return a.conn.Dialect() }

func (a *activeConn) driver() db.DriverName { return db.DriverName(a.cfg.Driver) }

// database is the database new tabs start in: the profile's, except for
// SQLite, whose profile names the file opened as "main".
func (a *activeConn) database() string {
	if a.driver() == db.SQLite {
		return "main"
	}
	return a.cfg.Database
}

func (a *activeConn) close() {
	if a.conn != nil {
		_ = a.conn.Close()
	}
	if a.tunnel != nil {
		_ = a.tunnel.Close()
	}
}

// connector opens connections, resolving secrets and prompting as needed.
type connector struct {
	secrets secrets.Store
	memory  *secrets.Memory // secrets entered this run but not saved
	bridge  *Bridge
}

func (c *connector) secret(id string, k secrets.Kind) string {
	if v, ok, _ := c.memory.Get(id, k); ok {
		return v
	}
	if v, ok, err := c.secrets.Get(id, k); err == nil && ok {
		return v
	}
	return ""
}

func (c *connector) remember(cfg config.Connection, k secrets.Kind, v string) {
	if v == "" {
		return
	}
	_ = c.memory.Set(cfg.ID, k, v)
	if cfg.SavePassword {
		_ = c.secrets.Set(cfg.ID, k, v)
	}
}

// open connects cfg: SSH tunnel first (if enabled), then the database,
// prompting for a password when authentication fails.
func (c *connector) open(ctx context.Context, cfg config.Connection) (*activeConn, error) {
	drv, ok := db.Get(db.DriverName(cfg.Driver))
	if !ok {
		return nil, fmt.Errorf("unknown driver %q", cfg.Driver)
	}
	if cfg.Driver == string(db.SQLite) && cfg.SSH.Enabled {
		return nil, errors.New("sqlite opens local files and cannot connect through SSH")
	}
	ac := &activeConn{cfg: cfg}
	cfg = cfg.Expanded() // ${VAR} references; ac keeps the profile as written
	var dial db.DialFunc
	if cfg.SSH.Enabled {
		t, err := sshx.Dial(ctx, sshx.Options{
			SSH:        cfg.SSH,
			Password:   c.secret(cfg.ID, secrets.SSHPassword),
			Passphrase: c.secret(cfg.ID, secrets.SSHPassphrase),
			Prompter:   sshPrompter{ctx: context.Background(), b: c.bridge},
		})
		if err != nil {
			return nil, err
		}
		pw, pp := t.UsedSecrets()
		c.remember(cfg, secrets.SSHPassword, pw)
		c.remember(cfg, secrets.SSHPassphrase, pp)
		ac.tunnel = t
		dial = t.DialContext
	}
	password := c.secret(cfg.ID, secrets.DBPassword)
	if pw, ok := cfg.EnvPassword(); ok {
		password = pw
	}
	prompted := false
	for attempt := 0; ; attempt++ {
		conn, err := drv.Open(ctx, connConfig(cfg, password), dial)
		if err == nil {
			ac.conn = conn
			break
		}
		if !isAuthError(err) || attempt >= 3 {
			ac.close()
			return nil, fmt.Errorf("database: %w", err)
		}
		msg := fmt.Sprintf("Password for %s@%s:%d", cfg.User, cfg.Host, cfg.Port)
		if attempt > 0 || password != "" {
			msg = "Authentication failed. " + msg
		}
		pw, perr := c.bridge.Secret(ctx, cfg.Title(), msg)
		if perr != nil {
			ac.close()
			return nil, perr
		}
		password, prompted = pw, true
	}
	if prompted {
		c.remember(cfg, secrets.DBPassword, password)
	}
	ac.cache = schema.NewCache(db.DriverName(cfg.Driver))
	return ac, nil
}

func connConfig(cfg config.Connection, password string) db.ConnConfig {
	if cfg.Driver == string(db.SQLite) {
		cfg.Database = expandHome(cfg.Database)
	}
	return db.ConnConfig{
		Host:        cfg.Host,
		Port:        cfg.Port,
		User:        cfg.User,
		Password:    password,
		Database:    cfg.Database,
		SSLMode:     cfg.SSLMode,
		SSLRootCert: expandHome(cfg.SSLRootCert),
		SSLCert:     expandHome(cfg.SSLCert),
		SSLKey:      expandHome(cfg.SSLKey),
		ReadOnly:    cfg.ReadOnly,
		Params:      cfg.Params,
		Timeout:     15 * time.Second,
	}
}

func isAuthError(err error) bool {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code == "28P01" || pe.Code == "28000"
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		return me.Number == 1045
	}
	var ce *clickhouse.QueryError
	if errors.As(err, &ce) {
		return ce.Code == clickhouse.CodeAuthFailed || ce.Code == clickhouse.CodeRequiredPassword
	}
	if mssql.IsAuthError(err) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "password authentication failed") || strings.Contains(s, "no password supplied") ||
		strings.Contains(s, "Access denied for user")
}

// isUnknownDatabase reports a login that failed only because the database
// does not exist.
func isUnknownDatabase(err error) bool {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code == "3D000"
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		return me.Number == 1049
	}
	var ce *clickhouse.QueryError
	if errors.As(err, &ce) {
		return ce.Code == clickhouse.CodeUnknownDatabase
	}
	if mssql.IsUnknownDatabase(err) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "SQLSTATE 3D000") || strings.Contains(s, "Unknown database")
}
