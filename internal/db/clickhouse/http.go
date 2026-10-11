package clickhouse

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/farvisun/squi/internal/db"
)

// textFormat is the output format squi asks for: tab-separated values
// with a header line of names and one of types, all rendered as text the
// way clickhouse-client shows them.
const textFormat = "TabSeparatedWithNamesAndTypes"

// client talks to the ClickHouse HTTP interface.
type client struct {
	base     string // scheme://host:port/
	user     string
	password string
	http     *http.Client
	settings map[string]string // applied to every query
}

// request is one HTTP query.
type request struct {
	query    string
	data     io.Reader // INSERT payload; the query then travels in the URL
	database string
	session  string
	queryID  string
	params   map[string]string // values for {name:Type} placeholders
	wait     bool              // buffer the result server-side (for writes)
}

func (c *client) do(ctx context.Context, r request) (*http.Response, error) {
	v := url.Values{}
	for k, s := range c.settings {
		v.Set(k, s)
	}
	v.Set("default_format", textFormat)
	if r.database != "" {
		v.Set("database", r.database)
	}
	if r.session != "" {
		v.Set("session_id", r.session)
		v.Set("session_timeout", "3600")
	}
	if r.queryID != "" {
		v.Set("query_id", r.queryID)
	}
	if r.wait {
		v.Set("wait_end_of_query", "1")
	}
	for k, s := range r.params {
		v.Set("param_"+k, s)
	}
	body := io.Reader(strings.NewReader(r.query))
	if r.data != nil {
		v.Set("query", r.query)
		body = r.data
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"?"+v.Encode(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	req.Header.Set("User-Agent", "squi")
	if c.user != "" {
		req.Header.Set("X-ClickHouse-User", c.user)
	}
	if c.password != "" {
		req.Header.Set("X-ClickHouse-Key", c.password)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return nil, parseError(string(msg), resp.Status)
	}
	return resp, nil
}

// table runs a metadata query and reads the whole result.
func (c *client) table(ctx context.Context, query, database string, params map[string]string) (*db.Table, error) {
	resp, err := c.do(ctx, request{query: query, database: database, params: params})
	if err != nil {
		return nil, err
	}
	st, err := newStream(resp, nil, false)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	t := &db.Table{}
	for _, col := range st.Columns() {
		t.Columns = append(t.Columns, col.Name)
	}
	for {
		rows, err := st.Next(5000)
		t.Rows = append(t.Rows, rows...)
		if errors.Is(err, io.EOF) {
			return t, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// exec runs a statement that returns no rows and reports written rows.
func (c *client) exec(ctx context.Context, r request) (int64, error) {
	r.wait = true
	resp, err := c.do(ctx, r)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return writtenRows(resp), nil
}

// writtenRows reads written_rows from the X-ClickHouse-Summary header.
func writtenRows(resp *http.Response) int64 {
	var sum struct {
		WrittenRows string `json:"written_rows"`
	}
	if h := resp.Header.Get("X-ClickHouse-Summary"); h != "" && json.Unmarshal([]byte(h), &sum) == nil {
		n, _ := strconv.ParseInt(sum.WrittenRows, 10, 64)
		return n
	}
	return 0
}

// ---- results ----

// stream reads a TabSeparatedWithNamesAndTypes response lazily. Responses
// in another format (the query had its own FORMAT clause) are shown as one
// "output" column, a row per line.
type stream struct {
	resp     *http.Response
	rd       *bufio.Reader
	cols     []db.ResultColumn
	raw      bool
	rows     int64
	affected int64
	tag      string
	done     bool
	cancel   context.CancelFunc
	// stop ends the query on the server when the stream is closed before
	// the end, so its HTTP session is free for the next statement
	stop func()
}

func newStream(resp *http.Response, cancel context.CancelFunc, write bool) (*stream, error) {
	s := &stream{resp: resp, rd: bufio.NewReaderSize(resp.Body, 64<<10), cancel: cancel}
	if write {
		s.affected = writtenRows(resp)
	}
	if f := resp.Header.Get("X-ClickHouse-Format"); f != "" && f != textFormat {
		s.raw = true
		s.cols = []db.ResultColumn{{Name: "output", Type: f}}
		return s, nil
	}
	names, err := s.line()
	if errors.Is(err, io.EOF) && names == "" {
		s.done = true // statement without a result set
		return s, nil
	}
	if err != nil {
		s.Close()
		return nil, err
	}
	types, err := s.line()
	if err != nil && !errors.Is(err, io.EOF) {
		s.Close()
		return nil, err
	}
	ns, ts := strings.Split(names, "\t"), strings.Split(types, "\t")
	for i, n := range ns {
		col := db.ResultColumn{Name: unescape(n)}
		if i < len(ts) {
			col.Type = unescape(ts[i])
		}
		s.cols = append(s.cols, col)
	}
	return s, nil
}

// line returns the next line without its newline. A mid-stream exception
// is returned as an error.
func (s *stream) line() (string, error) {
	l, err := s.rd.ReadString('\n')
	l = strings.TrimSuffix(l, "\n")
	// the server marks an exception after the headers were sent, then
	// drops the connection (so err is usually io.ErrUnexpectedEOF)
	if strings.TrimSuffix(l, "\r") == "__exception__" {
		rest, _ := io.ReadAll(io.LimitReader(s.rd, 1<<16))
		return "", parseError(string(rest), "")
	}
	if err != nil && l != "" && errors.Is(err, io.EOF) {
		return l, nil
	}
	return l, err
}

func (s *stream) Columns() []db.ResultColumn { return s.cols }

func (s *stream) Next(n int) ([][]db.Cell, error) {
	if s.done {
		return nil, io.EOF
	}
	var out [][]db.Cell
	for len(out) < n {
		l, err := s.line()
		if errors.Is(err, io.EOF) {
			s.done = true
			s.tag = fmt.Sprintf("SELECT %d", s.rows)
			return out, io.EOF
		}
		if err != nil {
			return out, err
		}
		s.rows++
		if s.raw {
			out = append(out, []db.Cell{db.Text(l)})
			continue
		}
		fields := strings.Split(l, "\t")
		row := make([]db.Cell, len(s.cols))
		for i := range row {
			if i < len(fields) {
				row[i] = cell(fields[i])
			} else {
				row[i] = db.NullCell
			}
		}
		out = append(out, row)
	}
	return out, nil
}

func (s *stream) RowsAffected() int64 {
	if len(s.cols) > 0 {
		return s.rows
	}
	return s.affected
}

func (s *stream) CommandTag() string { return s.tag }

func (s *stream) Close() error {
	err := s.resp.Body.Close()
	if s.cancel != nil {
		s.cancel()
	}
	if !s.done && s.stop != nil {
		s.stop()
		s.stop = nil
	}
	return err
}

// cell decodes one TSV field; a bare \N is NULL.
func cell(f string) db.Cell {
	if f == `\N` {
		return db.NullCell
	}
	return db.Text(unescape(f))
}

func unescape(f string) string {
	if !strings.Contains(f, `\`) {
		return f
	}
	var b strings.Builder
	for i := 0; i < len(f); i++ {
		c := f[i]
		if c != '\\' || i+1 == len(f) {
			b.WriteByte(c)
			continue
		}
		i++
		switch f[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case '0':
			b.WriteByte(0)
		case 'a':
			b.WriteByte('\a')
		case 'v':
			b.WriteByte('\v')
		default: // \\ \' and anything else: the character itself
			b.WriteByte(f[i])
		}
	}
	return b.String()
}

// escape encodes a value for TabSeparated input.
func escape(c db.Cell) string {
	if c.Null {
		return `\N`
	}
	return strings.NewReplacer(`\`, `\\`, "\t", `\t`, "\n", `\n`, "\r", `\r`).Replace(c.S)
}

// ---- errors ----

// QueryError is an exception reported by the server.
type QueryError struct {
	Code     int
	Name     string // e.g. SYNTAX_ERROR
	Message  string
	Position int // 1-based character position of a syntax error, 0 if unknown
}

func (e *QueryError) Error() string {
	if e.Name != "" {
		return fmt.Sprintf("Code %d (%s): %s", e.Code, e.Name, e.Message)
	}
	return fmt.Sprintf("Code %d: %s", e.Code, e.Message)
}

// Error codes squi reacts to.
const (
	CodeSessionLocked    = 373
	CodeUnknownDatabase  = 81
	CodeQueryCancelled   = 394
	CodeAuthFailed       = 516
	CodeRequiredPassword = 194
)

var (
	errHeadRe    = regexp.MustCompile(`(?s)^Code: (\d+)\. (?:DB::Exception: )?(.*)$`)
	errVersionRe = regexp.MustCompile(`\s*\(version [^()]*(?:\([^()]*\))?\)\s*$`)
	errNameRe    = regexp.MustCompile(`\s*\(([A-Z][A-Z0-9_]+)\)\s*$`)
	errPosRe     = regexp.MustCompile(`failed at position (\d+)`)
)

// parseError turns "Code: 62. DB::Exception: … (SYNTAX_ERROR) (version …)"
// into a *QueryError; other bodies become plain errors.
func parseError(body, status string) error {
	if i := strings.Index(body, "Code: "); i > 0 {
		body = body[i:] // skip framing such as a random tag line
	}
	if i := strings.IndexAny(body, "\r\n"); i > 0 && strings.HasPrefix(body, "Code: ") {
		body = body[:i]
	}
	body = strings.TrimSpace(body)
	m := errHeadRe.FindStringSubmatch(body)
	if m == nil {
		if body == "" {
			body = status
		}
		return errors.New("clickhouse: " + body)
	}
	e := &QueryError{}
	e.Code, _ = strconv.Atoi(m[1])
	msg := errVersionRe.ReplaceAllString(m[2], "")
	if n := errNameRe.FindStringSubmatch(msg); n != nil {
		e.Name = n[1]
		msg = msg[:len(msg)-len(n[0])]
	}
	// syntax errors list every possible token; keep the useful part
	if i := strings.Index(msg, ". Expected one of:"); i >= 0 {
		msg = msg[:i]
	}
	if p := errPosRe.FindStringSubmatch(msg); p != nil {
		e.Position, _ = strconv.Atoi(p[1])
	}
	e.Message = strings.TrimSpace(msg)
	return e
}

func newID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "squi-" + hex.EncodeToString(b[:])
}
