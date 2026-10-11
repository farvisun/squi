package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// IsURL reports whether s looks like a connection URL rather than a name:
// scheme://…, sqlite:path or the path of a SQLite file (.db, .sqlite,
// .sqlite3).
func IsURL(s string) bool {
	return strings.Contains(s, "://") || hasSQLiteScheme(s) || isSQLitePath(s)
}

func hasSQLiteScheme(s string) bool {
	l := strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(l, "sqlite:") || strings.HasPrefix(l, "sqlite3:")
}

func isSQLitePath(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return !strings.Contains(s, "://") && !hasSQLiteScheme(s) &&
		(strings.HasSuffix(s, ".db") || strings.HasSuffix(s, ".sqlite") || strings.HasSuffix(s, ".sqlite3"))
}

// ParseURL builds a connection from a URL such as
//
//	postgres://app@db.internal:5432/api?sslmode=require
//	mysql://root:secret@127.0.0.1/shop        (also mariadb://)
//	clickhouse://default@ch:8123/logs         (https: ?sslmode=require or ?secure=true)
//	sqlserver://sa@mssql:1433/shop            (also mssql://; ?database= works too)
//	sqlite:app.db, sqlite:///var/lib/app.db   (or just the path of a .db / .sqlite file)
//
// Query parameters configure the profile: sslmode, sslrootcert, sslcert,
// sslkey, env, read_only, statement_timeout (or timeout), fetch_size, name,
// label, color,
// and an SSH tunnel with ssh=[user@]host[:port] (or an ~/.ssh/config
// alias), ssh_jump=a,b and ssh_key. host overrides the URL host (e.g. a
// Unix socket directory). Anything else becomes a driver parameter. The
// password is returned separately: it is never stored in the profile.
func ParseURL(raw string) (Connection, string, error) {
	raw = strings.TrimSpace(raw)
	if isSQLitePath(raw) {
		c := Connection{Driver: "sqlite", Database: raw, Name: filepath.Base(raw)}
		normalize(&c)
		c.ID = ""
		return c, "", nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return Connection{}, "", fmt.Errorf("connection URL: %w", err)
	}
	var c Connection
	switch strings.ToLower(u.Scheme) {
	case "sqlite", "sqlite3":
		// sqlite:rel.db, sqlite://rel.db, sqlite:///abs.db, sqlite://~/x.db
		c.Driver = "sqlite"
		path := u.Host + u.Path
		if u.Opaque != "" {
			if path, err = url.PathUnescape(u.Opaque); err != nil {
				return Connection{}, "", fmt.Errorf("connection URL: %w", err)
			}
		}
		if path == "" {
			return Connection{}, "", fmt.Errorf("connection URL: sqlite needs a file path")
		}
		u.Host, u.Path, u.User = "", "", nil
		c.Database = path
	case "postgres", "postgresql", "pg":
		c.Driver = "postgres"
	case "mysql", "mariadb":
		c.Driver = "mysql"
	case "clickhouse", "ch":
		c.Driver = "clickhouse"
	case "clickhouses", "clickhouse+https":
		c.Driver, c.SSLMode = "clickhouse", "require"
	case "sqlserver", "mssql":
		c.Driver = "sqlserver"
	default:
		return Connection{}, "", fmt.Errorf("connection URL: unknown scheme %q (use postgres, mysql, mariadb, clickhouse, sqlserver or sqlite)", u.Scheme)
	}
	if c.Driver != "sqlite" {
		c.Host = u.Hostname()
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 || n > 65535 {
			return Connection{}, "", fmt.Errorf("connection URL: bad port %q", p)
		}
		c.Port = n
	}
	var password string
	if u.User != nil {
		c.User = u.User.Username()
		password, _ = u.User.Password()
	}
	if c.Driver != "sqlite" {
		c.Database = strings.TrimPrefix(u.Path, "/")
	}

	for k, vs := range u.Query() {
		v := ""
		if len(vs) > 0 {
			v = vs[len(vs)-1]
		}
		switch strings.ToLower(k) {
		case "sslmode", "ssl_mode", "ssl-mode":
			c.SSLMode = strings.ToLower(v)
		case "sslrootcert", "ssl_root_cert":
			c.SSLRootCert = v
		case "sslcert", "ssl_cert":
			c.SSLCert = v
		case "sslkey", "ssl_key":
			c.SSLKey = v
		case "secure":
			if b, _ := strconv.ParseBool(v); b && c.SSLMode == "" {
				c.SSLMode = "require"
			}
		case "env":
			c.Env = Env(strings.ToLower(v))
			if !validEnv(c.Env) {
				return Connection{}, "", fmt.Errorf("connection URL: unknown env %q", v)
			}
		case "read_only", "readonly":
			c.ReadOnly = v == "" || parseBool(v)
		case "statement_timeout", "timeout":
			if _, err := ParseTimeout(v); err != nil {
				return Connection{}, "", fmt.Errorf("connection URL: %w", err)
			}
			c.StatementTimeout = v
		case "fetch_size":
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				return Connection{}, "", fmt.Errorf("connection URL: bad fetch_size %q", v)
			}
			c.FetchSize = n
		case "name":
			c.Name = v
		case "label":
			c.Label = v
		case "color", "colour":
			c.Color = v
		case "host":
			c.Host = v
		case "database", "dbname":
			c.Database = v
		case "ssh":
			if err := parseSSHTarget(&c.SSH, v); err != nil {
				return Connection{}, "", err
			}
		case "ssh_jump":
			for _, j := range strings.Split(v, ",") {
				if j = strings.TrimSpace(j); j != "" {
					c.SSH.JumpHosts = append(c.SSH.JumpHosts, j)
				}
			}
		case "ssh_key":
			c.SSH.KeyPath = v
		default:
			if c.Params == nil {
				c.Params = map[string]string{}
			}
			c.Params[k] = v
		}
	}
	if c.Driver == "sqlite" && c.SSH.Enabled {
		return Connection{}, "", fmt.Errorf("connection URL: sqlite opens local files and cannot use ssh")
	}
	if c.Host == "" && !c.SSH.Enabled && c.Driver != "sqlite" {
		c.Host = "localhost"
	}
	if c.Name == "" {
		c.Name = defaultName(c)
	}
	normalize(&c)
	c.ID = "" // callers assign one
	return c, password, nil
}

// parseSSHTarget reads "[user@]host[:port]"; a bare name may be an
// ~/.ssh/config alias, which sshx resolves either way.
func parseSSHTarget(s *SSH, v string) error {
	s.Enabled = true
	if user, rest, ok := strings.Cut(v, "@"); ok {
		s.User, v = user, rest
	}
	if h, p, ok := strings.Cut(v, ":"); ok {
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 || n > 65535 {
			return fmt.Errorf("connection URL: bad ssh port %q", p)
		}
		s.Port, v = n, h
	}
	if v == "" {
		return fmt.Errorf("connection URL: ssh needs a host")
	}
	s.Host = v
	return nil
}

func defaultName(c Connection) string {
	if c.Driver == "sqlite" {
		return filepath.Base(c.Database)
	}
	n := c.Host
	if c.User != "" {
		n = c.User + "@" + n
	}
	if c.Database != "" {
		n += "/" + c.Database
	}
	return n
}

func validEnv(e Env) bool {
	for _, x := range Envs {
		if x == e {
			return true
		}
	}
	return false
}

func parseBool(v string) bool {
	b, err := strconv.ParseBool(v)
	return err == nil && b || v == "yes" || v == "on"
}

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)

// ExpandEnv replaces ${VAR} and ${VAR:-default} with environment values.
// A bare $VAR is left alone, so values that merely contain "$" survive.
func ExpandEnv(s string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	return envRef.ReplaceAllStringFunc(s, func(ref string) string {
		m := envRef.FindStringSubmatch(ref)
		if v, ok := os.LookupEnv(m[1]); ok && v != "" {
			return v
		}
		return m[2]
	})
}

// Expanded returns c with ${VAR} references resolved in its address,
// credentials, TLS files, SSH settings and driver parameters. Profiles keep
// the references; this is applied when connecting.
func (c Connection) Expanded() Connection {
	c.Host = ExpandEnv(c.Host)
	c.User = ExpandEnv(c.User)
	c.Database = ExpandEnv(c.Database)
	c.SSLRootCert = ExpandEnv(c.SSLRootCert)
	c.SSLCert = ExpandEnv(c.SSLCert)
	c.SSLKey = ExpandEnv(c.SSLKey)
	c.SSH.Alias = ExpandEnv(c.SSH.Alias)
	c.SSH.Host = ExpandEnv(c.SSH.Host)
	c.SSH.User = ExpandEnv(c.SSH.User)
	c.SSH.KeyPath = ExpandEnv(c.SSH.KeyPath)
	if len(c.SSH.JumpHosts) > 0 {
		jumps := make([]string, len(c.SSH.JumpHosts))
		for i, j := range c.SSH.JumpHosts {
			jumps[i] = ExpandEnv(j)
		}
		c.SSH.JumpHosts = jumps
	}
	if len(c.Params) > 0 {
		params := make(map[string]string, len(c.Params))
		for k, v := range c.Params {
			params[k] = ExpandEnv(v)
		}
		c.Params = params
	}
	return c
}

// EnvPassword returns the password from the PasswordEnv variable, if set.
func (c Connection) EnvPassword() (string, bool) {
	if c.PasswordEnv == "" {
		return "", false
	}
	v, ok := os.LookupEnv(strings.TrimSpace(strings.Trim(c.PasswordEnv, "${}")))
	return v, ok && v != ""
}
