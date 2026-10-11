// Package clickhouse implements the squi database driver for ClickHouse
// over its HTTP interface (port 8123, or 8443 with TLS).
//
// The HTTP interface returns every value as text exactly as ClickHouse
// formats it, needs nothing beyond the standard library and works through
// SSH tunnels like any TCP service. Query tabs use server-side HTTP
// sessions so SET and temporary tables behave as in clickhouse-client.
// ClickHouse has no interactive transactions: Begin returns an error.
package clickhouse

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/farvisun/squi/internal/db"
	"github.com/farvisun/squi/internal/sqltext"
)

func init() { db.Register(Driver{}) }

// Driver opens ClickHouse connections.
type Driver struct{}

// Name implements db.Driver.
func (Driver) Name() db.DriverName { return db.ClickHouse }

// Open implements db.Driver and checks the server is reachable.
func (Driver) Open(ctx context.Context, cfg db.ConnConfig, dial db.DialFunc) (db.Conn, error) {
	cl, err := newClient(cfg, dial)
	if err != nil {
		return nil, err
	}
	c := &conn{cfg: cfg, cl: cl}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	vctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	t, err := cl.table(vctx, "SELECT version()", cfg.Database, nil)
	if err != nil {
		cl.http.CloseIdleConnections()
		return nil, fmt.Errorf("connect to %s as %s: %w", strings.TrimSuffix(cl.base, "/"), cfg.User, err)
	}
	if len(t.Rows) > 0 && len(t.Rows[0]) > 0 {
		c.version = t.Rows[0][0].S
	}
	return c, nil
}

func newClient(cfg db.ConnConfig, dial db.DialFunc) (*client, error) {
	host := cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	scheme, tc, err := tlsConfig(cfg, host)
	if err != nil {
		return nil, err
	}
	port := cfg.Port
	if port == 0 {
		port = db.DefaultPort(db.ClickHouse)
		if scheme == "https" {
			port = 8443
		}
	}
	if dial == nil {
		d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
		dial = d.DialContext
	}
	tr := &http.Transport{
		DialContext:         dial,
		TLSClientConfig:     tc,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     90 * time.Second,
		// Proxy is deliberately nil: traffic goes straight to the server
		// (or through the SSH dialer), never via HTTP(S)_PROXY.
	}
	settings := map[string]string{
		// closing a result early cancels the query on the server
		"cancel_http_readonly_queries_on_client_close": "1",
	}
	if cfg.ReadOnly {
		settings["readonly"] = "2" // no writes; SET is still allowed
	}
	for k, v := range cfg.Params {
		settings[k] = v
	}
	return &client{
		base:     fmt.Sprintf("%s://%s/", scheme, net.JoinHostPort(host, strconv.Itoa(port))),
		user:     cfg.User,
		password: cfg.Password,
		http:     &http.Client{Transport: tr},
		settings: settings,
	}, nil
}

// tlsConfig maps libpq-style SSL modes onto HTTP or HTTPS.
func tlsConfig(cfg db.ConnConfig, host string) (string, *tls.Config, error) {
	switch cfg.SSLMode {
	case "", "disable", "prefer":
		return "http", nil, nil
	case "require":
		return "https", &tls.Config{InsecureSkipVerify: true}, nil
	case "verify-ca", "verify-full":
	default:
		return "", nil, fmt.Errorf("unknown sslmode %q", cfg.SSLMode)
	}
	tc := &tls.Config{ServerName: host}
	if cfg.SSLRootCert != "" {
		pem, err := os.ReadFile(cfg.SSLRootCert)
		if err != nil {
			return "", nil, fmt.Errorf("read sslrootcert: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return "", nil, fmt.Errorf("sslrootcert %s: no certificates found", cfg.SSLRootCert)
		}
		tc.RootCAs = pool
	}
	if cfg.SSLCert != "" || cfg.SSLKey != "" {
		cert, err := tls.LoadX509KeyPair(cfg.SSLCert, cfg.SSLKey)
		if err != nil {
			return "", nil, fmt.Errorf("load client certificate: %w", err)
		}
		tc.Certificates = []tls.Certificate{cert}
	}
	if cfg.SSLMode == "verify-ca" {
		// verify the chain but not the host name
		tc.InsecureSkipVerify = true
		tc.VerifyPeerCertificate = func(raw [][]byte, _ [][]*x509.Certificate) error {
			certs := make([]*x509.Certificate, len(raw))
			for i, r := range raw {
				c, err := x509.ParseCertificate(r)
				if err != nil {
					return err
				}
				certs[i] = c
			}
			opts := x509.VerifyOptions{Roots: tc.RootCAs, Intermediates: x509.NewCertPool()}
			for _, c := range certs[1:] {
				opts.Intermediates.AddCert(c)
			}
			_, err := certs[0].Verify(opts)
			return err
		}
	}
	return "https", tc, nil
}

// conn is an open ClickHouse server profile. Metadata queries are
// stateless HTTP requests; each tab gets its own HTTP session.
type conn struct {
	cfg     db.ConnConfig
	cl      *client
	version string
}

func (c *conn) Dialect() db.Dialect { return db.Dialect{Driver: db.ClickHouse} }

func (c *conn) ServerVersion() string { return "ClickHouse " + c.version }

func (c *conn) Ping(ctx context.Context) error {
	_, err := c.cl.table(ctx, "SELECT 1", "", nil)
	return err
}

func (c *conn) Admin() db.Admin { return admin{c} }

func (c *conn) Close() error {
	c.cl.http.CloseIdleConnections()
	return nil
}

// NewSession opens an HTTP session bound to database.
func (c *conn) NewSession(ctx context.Context, database string) (db.Session, error) {
	s := &session{c: c, id: newID(), db: database}
	t, err := c.cl.table(ctx, "SELECT currentDatabase()", database, nil)
	if err != nil {
		return nil, err
	}
	if s.db == "" && len(t.Rows) > 0 {
		s.db = t.Rows[0][0].S
	}
	return s, nil
}

// errNoTx is returned by the transaction methods.
var errNoTx = fmt.Errorf("ClickHouse has no transactions: %w", db.ErrNotSupported)

// session is one query tab: an HTTP session plus the current database.
type session struct {
	c  *conn
	id string
	db string

	mu      sync.Mutex
	queryID string
	cancel  context.CancelFunc
}

// Run executes one statement. Reads are streamed; anything else waits for
// the server to finish so the written row count is known.
func (s *session) Run(ctx context.Context, sql string) (db.ResultStream, error) {
	sql = strings.TrimRight(strings.TrimSpace(sql), "; \t\r\n")
	cl := sqltext.Classify(sql, db.ClickHouse)
	write := cl.Kind != sqltext.KindRead
	qid := newID()
	rctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.queryID, s.cancel = qid, cancel
	s.mu.Unlock()
	resp, err := s.do(rctx, request{query: sql, database: s.db, session: s.id, queryID: qid, wait: write})
	if err != nil {
		cancel()
		return nil, err
	}
	st, err := newStream(resp, cancel, write)
	if err != nil {
		return nil, err
	}
	st.stop = func() { s.kill(qid, true) }
	if name, ok := useTarget(sql); ok {
		s.db = name // the URL database parameter would otherwise undo USE
	}
	if len(st.cols) == 0 {
		st.tag = strings.ToUpper(firstWord(sql))
		if st.affected > 0 {
			st.tag += " " + strconv.FormatInt(st.affected, 10)
		}
	}
	return st, nil
}

// do sends a session request, waiting briefly while the server still holds
// the session for a query that was just abandoned.
func (s *session) do(ctx context.Context, r request) (*http.Response, error) {
	deadline := time.Now().Add(5 * time.Second)
	for wait := 20 * time.Millisecond; ; wait = min(wait*2, 500*time.Millisecond) {
		resp, err := s.c.cl.do(ctx, r)
		var qe *QueryError
		if err == nil || !errors.As(err, &qe) || qe.Code != CodeSessionLocked || time.Now().After(deadline) {
			return resp, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// kill stops a query by id; with sync it waits until the server is done.
func (s *session) kill(qid string, sync bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mode := "ASYNC"
	if sync {
		mode = "SYNC"
	}
	_, err := s.c.cl.exec(ctx, request{query: "KILL QUERY WHERE query_id = {id:String} " + mode, params: map[string]string{"id": qid}})
	return err
}

// useTarget returns the database of a "USE name" statement.
func useTarget(sql string) (string, bool) {
	var words []sqltext.Token
	for _, t := range sqltext.Tokenize(sql, db.ClickHouse) {
		if t.Kind != sqltext.TokWhitespace && t.Kind != sqltext.TokComment {
			words = append(words, t)
		}
	}
	if len(words) != 2 || words[0].Upper() != "USE" {
		return "", false
	}
	return unquoteIdent(words[1].Text), true
}

func unquoteIdent(s string) string {
	if len(s) >= 2 && (s[0] == '`' || s[0] == '"') && s[len(s)-1] == s[0] {
		q := string(s[0])
		return strings.ReplaceAll(s[1:len(s)-1], q+q, q)
	}
	return s
}

func firstWord(sql string) string {
	for _, t := range sqltext.Tokenize(sql, db.ClickHouse) {
		if t.IsWord() {
			return t.Text
		}
	}
	return "OK"
}

func (s *session) Begin(context.Context) error    { return errNoTx }
func (s *session) Commit(context.Context) error   { return errNoTx }
func (s *session) Rollback(context.Context) error { return errNoTx }
func (s *session) InTx() bool                     { return false }

// Cancel kills the running query on the server and drops its connection.
func (s *session) Cancel(ctx context.Context) error {
	s.mu.Lock()
	qid, cancel := s.queryID, s.cancel
	s.mu.Unlock()
	if qid == "" {
		return nil
	}
	err := s.kill(qid, false)
	if cancel != nil {
		cancel()
	}
	return err
}

func (s *session) UseDatabase(ctx context.Context, name string) error {
	t, err := s.c.cl.table(ctx, "SELECT name FROM system.databases WHERE name = {db:String}", "", map[string]string{"db": name})
	if err != nil {
		return err
	}
	if len(t.Rows) == 0 {
		return &QueryError{Code: CodeUnknownDatabase, Name: "UNKNOWN_DATABASE", Message: "Database " + name + " does not exist"}
	}
	s.db = name
	return nil
}

func (s *session) CurrentDatabase() string { return s.db }

// BulkInsert streams rows as TabSeparated data.
func (s *session) BulkInsert(ctx context.Context, ref db.ObjectRef, columns []string, rows [][]db.Cell) (int64, error) {
	d := s.c.Dialect()
	cols := make([]string, len(columns))
	for i, c := range columns {
		cols[i] = d.QuoteIdent(c)
	}
	if ref.Database == "" {
		ref.Database = s.db
	}
	var b strings.Builder
	for _, r := range rows {
		for i, c := range r {
			if i > 0 {
				b.WriteByte('\t')
			}
			b.WriteString(escape(c))
		}
		b.WriteByte('\n')
	}
	q := "INSERT INTO " + d.Qualified(ref) + " (" + strings.Join(cols, ", ") + ") FORMAT TabSeparated"
	n, err := s.c.cl.exec(ctx, request{query: q, data: strings.NewReader(b.String()), database: s.db})
	if err == nil && n == 0 {
		n = int64(len(rows))
	}
	return n, err
}

// Close ends the tab; the server expires the HTTP session on its own.
func (s *session) Close() error { return nil }
