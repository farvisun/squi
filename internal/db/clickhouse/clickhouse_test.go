package clickhouse

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/farvisun/squi/internal/db"
)

// ---- unit tests ----

func TestParseError(t *testing.T) {
	body := "Code: 62. DB::Exception: Syntax error: failed at position 8 (fro): selec 1 fro x. Expected one of: Query, EXPLAIN. (SYNTAX_ERROR) (version 25.8.33.6 (official build))\n"
	var e *QueryError
	if !errors.As(parseError(body, "400"), &e) {
		t.Fatal("not a QueryError")
	}
	if e.Code != 62 || e.Name != "SYNTAX_ERROR" || e.Position != 8 || e.Message != "Syntax error: failed at position 8 (fro): selec 1 fro x" {
		t.Fatalf("%+v", e)
	}
	if err := parseError("", "502 Bad Gateway"); err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("plain error: %v", err)
	}
}

func TestUnescape(t *testing.T) {
	cases := map[string]string{`a\tb\nc\\d\'e`: "a\tb\nc\\d'e", `plain`: "plain", `\0x`: "\x00x"}
	for in, want := range cases {
		if got := unescape(in); got != want {
			t.Errorf("unescape(%q) = %q, want %q", in, got, want)
		}
	}
	if !cell(`\N`).Null || cell(`\\N`).Null {
		t.Fatal("NULL decoding")
	}
	for _, s := range []string{"a\tb", "x\ny\\z", ""} {
		if got := unescape(escape(db.Text(s))); got != s {
			t.Errorf("round trip %q -> %q", s, got)
		}
	}
}

func TestPlanTree(t *testing.T) {
	root := planTree([]string{"Expression", "  Aggregating", "    ReadFromMergeTree (t)", "    Indexes:",
		"      PrimaryKey", "        Condition: true", "    Prewhere info:", "  Limit"})
	if root.Title != "Expression" || len(root.Children) != 2 {
		t.Fatalf("tree: %+v", root)
	}
	agg := root.Children[0]
	if len(agg.Children) != 1 || agg.Children[0].Title != "ReadFromMergeTree (t)" {
		t.Fatalf("aggregating: %+v", agg)
	}
	read := agg.Children[0]
	if len(read.Children) != 2 || read.Children[0].Title != "Indexes:" || read.Children[1].Title != "Prewhere info:" {
		t.Fatalf("details belong under the step: %+v", read.Children)
	}
	if pk := read.Children[0].Children; len(pk) != 1 || len(pk[0].Children) != 1 {
		t.Fatalf("indexes: %+v", pk)
	}
}

func TestUseTarget(t *testing.T) {
	for in, want := range map[string]string{"USE squi": "squi", "use `my db`": "my db", `USE "x"`: "x"} {
		if got, ok := useTarget(in); !ok || got != want {
			t.Errorf("useTarget(%q) = %q, %v", in, got, ok)
		}
	}
	if _, ok := useTarget("SELECT 1"); ok {
		t.Error("SELECT is not USE")
	}
}

// ---- integration tests ----
//
//	SQUI_TEST_CLICKHOUSE=http://squi:squi@127.0.0.1:18123/squi

func testConfig(t *testing.T) db.ConnConfig {
	raw := os.Getenv("SQUI_TEST_CLICKHOUSE")
	if raw == "" {
		t.Skip("SQUI_TEST_CLICKHOUSE not set")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(u.Port())
	pw, _ := u.User.Password()
	return db.ConnConfig{Host: u.Hostname(), Port: port, User: u.User.Username(), Password: pw, Database: strings.Trim(u.Path, "/")}
}

func open(t *testing.T, cfg db.ConnConfig) db.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := Driver{}.Open(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func newSess(t *testing.T, c db.Conn, database string) db.Session {
	t.Helper()
	s, err := c.NewSession(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func all(t *testing.T, rs db.ResultStream) [][]db.Cell {
	t.Helper()
	defer rs.Close()
	var out [][]db.Cell
	for {
		rows, err := rs.Next(1000)
		out = append(out, rows...)
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

func run(t *testing.T, s db.Session, sql string) [][]db.Cell {
	t.Helper()
	rs, err := s.Run(context.Background(), sql)
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return all(t, rs)
}

func TestClickHouse(t *testing.T) {
	cfg := testConfig(t)
	c := open(t, cfg)
	ctx := context.Background()
	if !strings.HasPrefix(c.ServerVersion(), "ClickHouse ") {
		t.Fatalf("version %q", c.ServerVersion())
	}

	t.Run("EstimateRows", func(t *testing.T) {
		est := c.(db.RowEstimator)
		n, exact, ok, err := est.EstimateRows(ctx, db.ObjectRef{Database: "squi", Name: "users"})
		if err != nil || !ok || !exact || n != 500 {
			t.Fatalf("users: n=%d exact=%v ok=%v err=%v", n, exact, ok, err)
		}
		if _, _, ok, err := est.EstimateRows(ctx, db.ObjectRef{Database: "squi", Name: "paid_orders"}); err != nil || ok {
			t.Fatalf("view: ok=%v err=%v", ok, err)
		}
	})

	t.Run("Catalog", func(t *testing.T) {
		dbs, err := c.Databases(ctx)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, d := range dbs {
			found = found || d.Name == "squi"
		}
		if !found {
			t.Fatalf("databases: %v", dbs)
		}
		objs, err := c.Objects(ctx, "squi", "squi")
		if err != nil {
			t.Fatal(err)
		}
		kinds := map[string]db.ObjectKind{}
		for _, o := range objs {
			kinds[o.Name] = o.Kind
		}
		if kinds["users"] != db.KindTable || kinds["paid_orders"] != db.KindView || kinds["daily_revenue_mv"] != db.KindMatView {
			t.Fatalf("objects: %v", kinds)
		}
		cols, err := c.AllColumns(ctx, "squi", "squi")
		if err != nil || strings.Join(cols["users"], ",") != "id,email,name,country,created_at,tags" {
			t.Fatalf("columns: %v %v", cols["users"], err)
		}
	})

	t.Run("TableDetail", func(t *testing.T) {
		d, err := c.TableDetail(ctx, db.ObjectRef{Database: "squi", Name: "orders", Kind: db.KindTable})
		if err != nil {
			t.Fatal(err)
		}
		if pk := d.PrimaryKey(); strings.Join(pk, ",") != "id,user_id" && strings.Join(pk, ",") != "user_id,id" {
			t.Fatalf("pk %v", pk)
		}
		var note *db.Column
		for i := range d.Columns {
			if d.Columns[i].Name == "note" {
				note = &d.Columns[i]
			}
		}
		if note == nil || !note.Nullable {
			t.Fatalf("note column: %+v", note)
		}
		names := ""
		for _, ix := range d.Indexes {
			names += ix.Name + ";"
		}
		if !strings.Contains(names, "idx_status") || !strings.Contains(names, "PRIMARY KEY") {
			t.Fatalf("indexes %s", names)
		}
		stats := map[string]string{}
		for _, kv := range d.Stats {
			stats[kv.Key] = kv.Value
		}
		if stats["Engine"] != "MergeTree" || stats["Rows"] != "20000" || stats["Partition key"] == "" {
			t.Fatalf("stats %v", stats)
		}
		ddl, err := c.DDL(ctx, db.ObjectRef{Database: "squi", Name: "orders"})
		if err != nil || !strings.Contains(ddl, "CREATE TABLE squi.orders") {
			t.Fatalf("ddl %q %v", ddl, err)
		}
	})

	t.Run("Streaming", func(t *testing.T) {
		s := newSess(t, c, "squi")
		rs, err := s.Run(ctx, "SELECT id, user_id, note FROM orders ORDER BY id LIMIT 2500")
		if err != nil {
			t.Fatal(err)
		}
		defer rs.Close()
		if cols := rs.Columns(); len(cols) != 3 || cols[2].Type != "Nullable(String)" {
			t.Fatalf("cols %v", cols)
		}
		rows, err := rs.Next(1000)
		if err != nil || len(rows) != 1000 || rows[0][2].S != "gift" || !rows[1][2].Null {
			t.Fatalf("first batch: %d %v", len(rows), err)
		}
		rows, _ = rs.Next(10000)
		if len(rows) != 1500 {
			t.Fatalf("second batch %d", len(rows))
		}
		if _, err := rs.Next(10); !errors.Is(err, io.EOF) {
			t.Fatalf("want EOF, got %v", err)
		}
		if rs.RowsAffected() != 2500 {
			t.Fatalf("rows %d", rs.RowsAffected())
		}
		// closing early leaves the session usable
		rs2, err := s.Run(ctx, "SELECT number FROM numbers(1000000)")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := rs2.Next(10); err != nil {
			t.Fatal(err)
		}
		rs2.Close()
		if got := run(t, s, "SELECT 1"); len(got) != 1 {
			t.Fatal("session unusable after early close")
		}
	})

	t.Run("Values", func(t *testing.T) {
		s := newSess(t, c, "squi")
		rows := run(t, s, "SELECT 'a\\tb\\nc' AS s, [1, 2] AS arr, map('k', 'v') AS m, NULL AS n, toDecimal64(1.5, 2) AS d")
		r := rows[0]
		if r[0].S != "a\tb\nc" || r[1].S != "[1,2]" || r[2].S != "{'k':'v'}" || !r[3].Null || r[4].S != "1.5" {
			t.Fatalf("values %v", r)
		}
	})

	t.Run("WritesAndUse", func(t *testing.T) {
		s := newSess(t, c, "squi")
		run(t, s, "DROP TABLE IF EXISTS squi_test_w")
		rs, err := s.Run(ctx, "CREATE TABLE squi_test_w (a UInt32, s Nullable(String)) ENGINE = MergeTree ORDER BY a")
		if err != nil {
			t.Fatal(err)
		}
		if len(rs.Columns()) != 0 || rs.CommandTag() != "CREATE" {
			t.Fatalf("create: cols=%v tag=%q", rs.Columns(), rs.CommandTag())
		}
		rs.Close()
		rs, err = s.Run(ctx, "INSERT INTO squi_test_w VALUES (1, 'x'), (2, NULL), (3, 'y');")
		if err != nil {
			t.Fatal(err)
		}
		all(t, rs)
		if rs.RowsAffected() != 3 {
			t.Fatalf("affected %d", rs.RowsAffected())
		}
		n, err := s.BulkInsert(ctx, db.ObjectRef{Name: "squi_test_w"}, []string{"a", "s"},
			[][]db.Cell{{db.Text("4"), db.Text("tab\there")}, {db.Text("5"), db.NullCell}})
		if err != nil || n != 2 {
			t.Fatalf("bulk %d %v", n, err)
		}
		got := run(t, s, "SELECT s FROM squi_test_w WHERE a >= 4 ORDER BY a")
		if got[0][0].S != "tab\there" || !got[1][0].Null {
			t.Fatalf("bulk values %v", got)
		}
		run(t, s, "USE system")
		if s.CurrentDatabase() != "system" || run(t, s, "SELECT currentDatabase()")[0][0].S != "system" {
			t.Fatalf("USE: %s", s.CurrentDatabase())
		}
		if err := s.UseDatabase(ctx, "squi"); err != nil || run(t, s, "SELECT count() FROM squi_test_w")[0][0].S != "5" {
			t.Fatalf("UseDatabase: %v", err)
		}
		if err := s.UseDatabase(ctx, "nope_db"); err == nil {
			t.Fatal("unknown database accepted")
		}
		run(t, s, "SET max_threads = 2")
		if run(t, s, "SELECT getSetting('max_threads')")[0][0].S != "2" {
			t.Fatal("SET did not persist in the session")
		}
		run(t, s, "DROP TABLE squi_test_w")
	})

	t.Run("Errors", func(t *testing.T) {
		s := newSess(t, c, "squi")
		_, err := s.Run(ctx, "SELECT * FRM orders")
		var qe *QueryError
		if !errors.As(err, &qe) || qe.Name != "SYNTAX_ERROR" || qe.Position != 14 {
			t.Fatalf("syntax error: %#v", err)
		}
		rs, err := s.Run(ctx, "SELECT number, throwIf(number = 300000, 'boom') FROM numbers(400000) SETTINGS max_block_size = 1000")
		if err != nil {
			t.Fatal(err)
		}
		defer rs.Close()
		for {
			_, err = rs.Next(50000)
			if err != nil {
				break
			}
		}
		if !errors.As(err, &qe) || !strings.Contains(qe.Message, "boom") {
			t.Fatalf("mid-stream error: %v", err)
		}
		if err := s.Begin(ctx); !errors.Is(err, db.ErrNotSupported) {
			t.Fatalf("Begin: %v", err)
		}
		if _, err := s.Explain(ctx, "SELECT 1", true); !errors.Is(err, db.ErrNotSupported) {
			t.Fatalf("explain analyze: %v", err)
		}
		bad := cfg
		bad.Password = "wrong"
		_, err = Driver{}.Open(ctx, bad, nil)
		if !errors.As(err, &qe) || qe.Code != CodeAuthFailed {
			t.Fatalf("auth: %v", err)
		}
	})

	t.Run("Cancel", func(t *testing.T) {
		s := newSess(t, c, "squi")
		done := make(chan error, 1)
		go func() {
			rs, err := s.Run(ctx, "SELECT sum(number) FROM numbers(100000000000)")
			if err == nil {
				_, err = rs.Next(1)
				rs.Close()
			}
			done <- err
		}()
		time.Sleep(700 * time.Millisecond)
		if err := s.Cancel(ctx); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("query was not cancelled")
			}
		case <-time.After(15 * time.Second):
			t.Fatal("cancel did not stop the query")
		}
	})

	t.Run("Explain", func(t *testing.T) {
		s := newSess(t, c, "squi")
		plan, err := s.Explain(ctx, "SELECT count() FROM orders WHERE user_id = 5", false)
		if err != nil || !strings.Contains(plan.Raw, "ReadFromMergeTree") || len(plan.Children) == 0 {
			t.Fatalf("plan %v %v", plan, err)
		}
	})

	t.Run("ReadOnly", func(t *testing.T) {
		ro := cfg
		ro.ReadOnly = true
		s := newSess(t, open(t, ro), "squi")
		if _, err := s.Run(ctx, "CREATE TABLE ro_x (a UInt8) ENGINE = Memory"); err == nil {
			t.Fatal("write allowed on read-only connection")
		}
		if got := run(t, s, "SELECT count() FROM users"); got[0][0].S != "500" {
			t.Fatal("read failed on read-only connection")
		}
	})

	t.Run("Admin", func(t *testing.T) {
		a := c.Admin()
		for name, f := range map[string]func(context.Context) (*db.Table, error){
			"processes": a.Processes, "variables": a.Variables, "users": a.Users, "sizes": a.Sizes,
		} {
			tb, err := f(ctx)
			if err != nil || len(tb.Columns) == 0 {
				t.Errorf("%s: %v", name, err)
			}
		}
		if _, err := a.Grants(ctx, cfg.User); err != nil {
			t.Errorf("grants: %v", err)
		}
	})

	t.Run("CustomDialer", func(t *testing.T) {
		dials := 0
		d := &net.Dialer{}
		dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
			dials++
			return d.DialContext(ctx, network, addr)
		}
		cc, err := Driver{}.Open(ctx, cfg, dial)
		if err != nil || dials == 0 {
			t.Fatalf("dial used %d times: %v", dials, err)
		}
		cc.Close()
	})
}

func TestParseFramedError(t *testing.T) {
	body := "\r\nab12cd34\r\nCode: 395. DB::Exception: boom: while executing x. (FUNCTION_THROW_IF_VALUE_IS_NON_ZERO) (version 25.8.33.6 (official build))\r\n77 ab12cd34\r\n__exception__\r\n"
	var e *QueryError
	if !errors.As(parseError(body, ""), &e) || e.Code != 395 || e.Name != "FUNCTION_THROW_IF_VALUE_IS_NON_ZERO" || e.Message != "boom: while executing x." {
		t.Fatalf("%#v", parseError(body, ""))
	}
}
