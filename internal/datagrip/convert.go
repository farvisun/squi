package datagrip

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/farvisun/squi/internal/config"
)

// Convert maps a data source to a squi profile (without an ID). Notes list
// settings that were not carried over; an error means squi cannot use the
// data source at all.
func Convert(ds DataSource, src Source) (config.Connection, []string, error) {
	c := config.Connection{
		Name:     ds.Name,
		User:     ds.User,
		ReadOnly: ds.ReadOnly,
		Origin:   &config.Origin{Tool: Tool, ID: ds.UUID, Source: src.Key()},
	}
	if ds.Group != "" {
		c.Name = ds.Group + "/" + ds.Name
	}
	if c.Name == "" {
		c.Name = ds.UUID
	}
	c.SSH.Auth = config.SSHAuthAuto
	if !strings.HasPrefix(ds.URL, "jdbc:") {
		return c, nil, fmt.Errorf("no JDBC URL")
	}
	params := map[string]string{}
	for k, v := range ds.Props {
		params[k] = v
	}
	var notes []string
	rest := strings.TrimPrefix(ds.URL, "jdbc:")
	scheme, rest, _ := strings.Cut(rest, ":")
	switch strings.ToLower(scheme) {
	case "postgresql", "pgsql":
		c.Driver, c.Port = "postgres", 5432
		if err := parseHierarchical(&c, rest, params); err != nil {
			return c, nil, err
		}
		notes = postgresParams(&c, params)
	case "mysql", "mariadb":
		c.Driver, c.Port = "mysql", 3306
		for _, sub := range []string{"loadbalance:", "replication:", "aws:"} {
			if strings.HasPrefix(rest, sub) {
				rest = strings.TrimPrefix(rest, sub)
				notes = append(notes, "uses the first host of a "+strings.TrimSuffix(sub, ":")+" URL")
			}
		}
		if err := parseHierarchical(&c, rest, params); err != nil {
			return c, nil, err
		}
		notes = append(notes, mysqlParams(&c, params)...)
	case "clickhouse", "ch":
		c.Driver, c.Port = "clickhouse", 8123
		switch {
		case strings.HasPrefix(rest, "https:"):
			rest, c.SSLMode, c.Port = strings.TrimPrefix(rest, "https:"), "require", 8443
		case strings.HasPrefix(rest, "http:"):
			rest = strings.TrimPrefix(rest, "http:")
		case strings.HasPrefix(rest, "grpc:"):
			return c, nil, fmt.Errorf("ClickHouse gRPC is not supported (squi uses the HTTP interface)")
		}
		if err := parseHierarchical(&c, rest, params); err != nil {
			return c, nil, err
		}
		notes = clickhouseParams(&c, params)
	case "sqlserver":
		c.Driver, c.Port = "sqlserver", 1433
		notes = parseSQLServer(&c, rest)
	case "jtds":
		c.Driver, c.Port = "sqlserver", 1433
		rest = strings.TrimPrefix(rest, "sqlserver:")
		addr, props, _ := strings.Cut(rest, ";")
		if err := parseHierarchical(&c, addr, params); err != nil {
			return c, nil, err
		}
		notes = sqlServerProps(&c, splitProps(props))
	case "sqlite":
		c.Driver, c.Port, c.User = "sqlite", 0, ""
		path := strings.TrimPrefix(rest, "file:")
		if p, q, ok := strings.Cut(path, "?"); ok {
			path = p
			if q != "" {
				notes = append(notes, "ignored URL parameters "+q)
			}
		}
		switch path {
		case "", ":memory:":
			c.Database = ":memory:"
		default:
			path = expandMacros(path, src.Project)
			if !filepath.IsAbs(path) && src.Project != "" {
				path = filepath.Join(src.Project, path)
			}
			c.Database = filepath.Clean(path)
		}
		if ds.SSH != nil {
			ds.SSH = nil
			notes = append(notes, "SSH tunnel dropped: squi opens SQLite files locally")
		}
	default:
		return c, nil, fmt.Errorf("unsupported driver %q (squi supports PostgreSQL, MySQL/MariaDB, ClickHouse, SQL Server and SQLite)", scheme)
	}
	if ds.SSL != nil && c.Driver != "sqlite" {
		switch strings.ToUpper(ds.SSL.Mode) {
		case "VERIFY_CA":
			c.SSLMode = "verify-ca"
		case "VERIFY_FULL", "VERIFY_IDENTITY":
			c.SSLMode = "verify-full"
		default:
			c.SSLMode = "require"
		}
		c.SSLRootCert = expandMacros(ds.SSL.CACert, src.Project)
		c.SSLCert = expandMacros(ds.SSL.ClientCert, src.Project)
		c.SSLKey = expandMacros(ds.SSL.ClientKey, src.Project)
	}
	if ds.SSH != nil {
		t := ds.SSH
		c.SSH = config.SSH{Enabled: true, Host: t.Host, User: t.User, Auth: config.SSHAuthAuto}
		if t.Port != 0 && t.Port != 22 {
			c.SSH.Port = t.Port
		}
		switch t.AuthType {
		case "KEY_PAIR":
			c.SSH.Auth = config.SSHAuthKey
			c.SSH.KeyPath = expandMacros(t.KeyPath, src.Project)
		case "PASSWORD":
			c.SSH.Auth = config.SSHAuthPassword
		}
	}
	if ds.SSHMissing != "" && c.Driver != "sqlite" {
		notes = append(notes, "SSH tunnel "+ds.SSHMissing+" is not defined in DataGrip; imported without SSH")
	}
	if len(c.Params) == 0 {
		c.Params = nil
	}
	return c, notes, nil
}

// parseHierarchical reads "//host[:port][,host2…][/db][?k=v&…]" (or a bare
// "db") into c and params.
func parseHierarchical(c *config.Connection, s string, params map[string]string) error {
	s, query, _ := strings.Cut(s, "?")
	if q, err := url.ParseQuery(query); err == nil {
		for k, vs := range q {
			if len(vs) > 0 {
				params[k] = vs[len(vs)-1]
			}
		}
	}
	if !strings.HasPrefix(s, "//") {
		c.Host, c.Database = "localhost", strings.Trim(s, "/")
		return nil
	}
	authority, path, _ := strings.Cut(strings.TrimPrefix(s, "//"), "/")
	c.Database = strings.Trim(path, "/")
	if i := strings.LastIndex(authority, "@"); i >= 0 {
		if user, _, _ := strings.Cut(authority[:i], ":"); c.User == "" {
			c.User, _ = url.PathUnescape(user)
		}
		authority = authority[i+1:]
	}
	authority, _, _ = strings.Cut(authority, ",") // first host of a list
	host, port := authority, ""
	if strings.HasPrefix(authority, "[") || strings.Count(authority, ":") == 1 {
		if h, p, err := net.SplitHostPort(authority); err == nil {
			host, port = h, p
		}
	}
	if host == "" {
		host = "localhost"
	}
	c.Host = host
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n <= 0 || n > 65535 {
			return fmt.Errorf("bad port %q", port)
		}
		c.Port = n
	}
	return nil
}

// parseSQLServer reads "//host[\instance][:port][;key=value…]".
func parseSQLServer(c *config.Connection, s string) []string {
	addr, props, _ := strings.Cut(strings.TrimPrefix(s, "//"), ";")
	p := splitProps(props)
	var notes []string
	host := addr
	if h, port, err := net.SplitHostPort(addr); err == nil {
		host = h
		if n, err := strconv.Atoi(port); err == nil {
			c.Port = n
		}
	}
	if h, inst, ok := strings.Cut(host, `\`); ok {
		host = h
		notes = append(notes, "named instance "+inst+" is not supported; set the instance's port")
	}
	c.Host = host
	if c.Host == "" {
		c.Host = "localhost"
	}
	return append(notes, sqlServerProps(c, p)...)
}

func splitProps(s string) map[string]string {
	out := map[string]string{}
	for _, kv := range strings.Split(s, ";") {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.TrimSpace(k) != "" {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

// The parameter translators move JDBC parameters squi understands into the
// profile and report the others, except those that only tune the JDBC
// driver itself.

func postgresParams(c *config.Connection, p map[string]string) []string {
	return translate(p, func(k, v string) bool {
		switch strings.ToLower(k) {
		case "sslmode":
			c.SSLMode = strings.ToLower(v)
		case "ssl":
			if isTrue(v) && c.SSLMode == "" {
				c.SSLMode = "require"
			}
		case "sslrootcert":
			c.SSLRootCert = v
		case "sslcert":
			c.SSLCert = v
		case "sslkey":
			c.SSLKey = v
		case "applicationname":
			setParam(c, "application_name", v)
		case "currentschema":
			setParam(c, "search_path", v)
		case "options":
			setParam(c, "options", v)
		case "user":
			if c.User == "" {
				c.User = v
			}
		case "prepthreshold", "preparethreshold", "logintimeout", "connecttimeout", "sockettimeout", "tcpkeepalive",
			"stringtype", "rewritebatchedinserts", "loglevel", "readonly", "sslfactory", "sslpassword", "password",
			"assumeminserverversion", "defaultrowfetchsize", "binarytransfer":
		default:
			return false
		}
		return true
	})
}

func mysqlParams(c *config.Connection, p map[string]string) []string {
	return translate(p, func(k, v string) bool {
		switch strings.ToLower(k) {
		case "usessl":
			if !isTrue(v) {
				c.SSLMode = "disable"
			} else if c.SSLMode == "" {
				c.SSLMode = "require"
			}
		case "requiressl":
			if isTrue(v) {
				c.SSLMode = "require"
			}
		case "sslmode":
			switch strings.ToUpper(v) {
			case "DISABLED":
				c.SSLMode = "disable"
			case "PREFERRED":
				c.SSLMode = "prefer"
			case "REQUIRED":
				c.SSLMode = "require"
			case "VERIFY_CA":
				c.SSLMode = "verify-ca"
			case "VERIFY_IDENTITY":
				c.SSLMode = "verify-full"
			}
		case "user":
			if c.User == "" {
				c.User = v
			}
		case "characterencoding", "useunicode", "zerodatetimebehavior", "servertimezone", "allowpublickeyretrieval",
			"autoreconnect", "tinyint1isbit", "uselegacydatetimecode", "usejdbccomplianttimezoneshift", "connecttimeout",
			"sockettimeout", "verifyservercertificate", "rewritebatchedstatements", "useserverprepstmts",
			"cacheprepstmts", "useaffectedrows", "connectioncollation", "password", "useinformationschema",
			"nullcatalogmeanscurrent", "allowmultiqueries", "permitmysqlscheme", "enabledtlsprotocols":
		default:
			return false
		}
		return true
	})
}

func clickhouseParams(c *config.Connection, p map[string]string) []string {
	return translate(p, func(k, v string) bool {
		switch strings.ToLower(k) {
		case "ssl", "secure":
			if isTrue(v) {
				c.SSLMode = "require"
				if c.Port == 8123 {
					c.Port = 8443
				}
			}
		case "sslmode":
			switch strings.ToLower(v) {
			case "strict":
				c.SSLMode = "verify-full"
			case "none":
				c.SSLMode = "require"
			}
		case "database":
			c.Database = v
		case "user":
			if c.User == "" {
				c.User = v
			}
		case "compress", "socket_timeout", "connect_timeout", "http_connection_provider", "client_name", "password",
			"use_server_time_zone", "use_time_zone", "session_id", "async", "max_execution_time":
		default:
			return false
		}
		return true
	})
}

func sqlServerProps(c *config.Connection, p map[string]string) []string {
	trust := ""
	encrypt := ""
	notes := translate(p, func(k, v string) bool {
		switch strings.ToLower(k) {
		case "databasename", "database":
			c.Database = v
		case "encrypt":
			encrypt = strings.ToLower(v)
		case "trustservercertificate":
			trust = strings.ToLower(v)
		case "applicationintent":
			setParam(c, "ApplicationIntent", v)
		case "user", "username":
			if c.User == "" {
				c.User = v
			}
		case "portnumber", "port":
			if n, err := strconv.Atoi(v); err == nil {
				c.Port = n
			}
		case "servername":
			if c.Host == "" || c.Host == "localhost" {
				c.Host = v
			}
		case "instancename", "instance":
			return false
		case "logintimeout", "applicationname", "sendstringparametersasunicode", "password", "hostnameincertificate",
			"sockettimeout", "locktimeout", "selectmethod", "ssl", "authenticationscheme":
		default:
			return false
		}
		return true
	})
	switch {
	case encrypt == "false":
		c.SSLMode = "disable"
	case encrypt == "true" || encrypt == "strict" || encrypt == "mandatory":
		c.SSLMode = "require"
		if trust == "false" {
			c.SSLMode = "verify-full"
		}
	}
	return notes
}

// translate applies fn to every parameter in a stable order and returns a
// note naming the parameters fn did not handle.
func translate(p map[string]string, fn func(k, v string) bool) []string {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var ignored []string
	for _, k := range keys {
		if !fn(k, p[k]) {
			ignored = append(ignored, k)
		}
	}
	if len(ignored) == 0 {
		return nil
	}
	return []string{"ignored JDBC parameters: " + strings.Join(ignored, ", ")}
}

func setParam(c *config.Connection, k, v string) {
	if c.Params == nil {
		c.Params = map[string]string{}
	}
	c.Params[k] = v
}
