package postgres

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/farvisun/squi/internal/db"
)

// testConfig parses SQUI_TEST_PG (postgres://user:pass@host:port/db) or
// skips the test.
func testConfig(t *testing.T) db.ConnConfig {
	t.Helper()
	dsn := os.Getenv("SQUI_TEST_PG")
	if dsn == "" {
		t.Skip("SQUI_TEST_PG not set")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse SQUI_TEST_PG: %v", err)
	}
	port, _ := strconv.Atoi(u.Port())
	pw, _ := u.User.Password()
	return db.ConnConfig{
		Host:     u.Hostname(),
		Port:     port,
		User:     u.User.Username(),
		Password: pw,
		Database: strings.TrimPrefix(u.Path, "/"),
		SSLMode:  u.Query().Get("sslmode"),
	}
}

func open(t *testing.T, cfg db.ConnConfig, dial db.DialFunc) db.Conn {
	t.Helper()
	c, err := Driver{}.Open(context.Background(), cfg, dial)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func newSession(t *testing.T, c db.Conn, database string) db.Session {
	t.Helper()
	s, err := c.NewSession(context.Background(), database)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// runAll runs sql and returns all rows.
func runAll(t *testing.T, s db.Session, sql string) (db.ResultStream, [][]db.Cell) {
	t.Helper()
	rs, err := s.Run(context.Background(), sql)
	if err != nil {
		t.Fatalf("run %q: %v", sql, err)
	}
	defer rs.Close()
	var all [][]db.Cell
	for {
		rows, err := rs.Next(1000)
		all = append(all, rows...)
		if err == io.EOF {
			return rs, all
		}
		if err != nil {
			t.Fatalf("next %q: %v", sql, err)
		}
	}
}

func TestDriverRegistered(t *testing.T) {
	if _, ok := db.Get(db.Postgres); !ok {
		t.Fatal("postgres driver not registered")
	}
}

func TestMetadata(t *testing.T) {
	c := open(t, testConfig(t), nil)
	ctx := context.Background()

	if v := c.ServerVersion(); !strings.HasPrefix(v, "PostgreSQL 16") {
		t.Errorf("ServerVersion = %q", v)
	}
	if c.Dialect().Driver != db.Postgres {
		t.Error("wrong dialect")
	}

	dbs, err := c.Databases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]db.Database{}
	for _, d := range dbs {
		names[d.Name] = d
	}
	if _, ok := names["warehouse"]; !ok {
		t.Errorf("warehouse missing from %v", dbs)
	}
	if names["squi"].Size == "" {
		t.Errorf("squi size empty")
	}
	if _, ok := names["template1"]; ok {
		t.Error("template db listed")
	}

	schemas, err := c.Schemas(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(schemas); n < 4 || schemas[n-2] != "information_schema" || schemas[n-1] != "pg_catalog" {
		t.Errorf("schemas = %v", schemas)
	}
	if !slices.Contains(schemas, "analytics") || slices.Contains(schemas, "pg_toast") {
		t.Errorf("schemas = %v", schemas)
	}

	objs, err := c.Objects(ctx, "", "public")
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]db.ObjectKind{}
	comments := map[string]string{}
	for _, o := range objs {
		kinds[o.Name] = o.Kind
		comments[o.Name] = o.Comment
	}
	want := map[string]db.ObjectKind{
		"users": db.KindTable, "orders": db.KindTable, "order_items": db.KindTable, "audit_log": db.KindTable,
		"paid_orders": db.KindView, "revenue_by_country": db.KindMatView, "invoice_seq": db.KindSequence,
		"touch_updated_at": db.KindFunction,
	}
	for n, k := range want {
		if kinds[n] != k {
			t.Errorf("object %s kind = %q, want %q", n, kinds[n], k)
		}
	}
	if comments["users"] != "Registered customers" {
		t.Errorf("users comment = %q", comments["users"])
	}
	if objs[0].Kind != db.KindTable || objs[len(objs)-1].Kind != db.KindFunction {
		t.Errorf("objects not sorted by kind: first %v last %v", objs[0], objs[len(objs)-1])
	}

	cols, err := c.AllColumns(ctx, "", "public")
	if err != nil {
		t.Fatal(err)
	}
	if got := cols["order_items"]; !slices.Equal(got, []string{"order_id", "product_id", "qty", "unit_price"}) {
		t.Errorf("order_items columns = %v", got)
	}
	if _, ok := cols["paid_orders"]; !ok {
		t.Error("view columns missing")
	}

	objs, err = c.Objects(ctx, "", "analytics")
	if err != nil || len(objs) == 0 || objs[0].Name != "events" {
		t.Errorf("analytics objects = %v, %v", objs, err)
	}

	if objs, err := c.Objects(ctx, "warehouse", "public"); err != nil || len(objs) != 0 {
		t.Errorf("warehouse objects = %v, %v", objs, err)
	}
}

func TestTableDetail(t *testing.T) {
	c := open(t, testConfig(t), nil)
	ctx := context.Background()

	td, err := c.TableDetail(ctx, db.ObjectRef{Schema: "public", Name: "orders"})
	if err != nil {
		t.Fatal(err)
	}
	if td.Ref.Kind != db.KindTable {
		t.Errorf("kind = %q", td.Ref.Kind)
	}
	col := func(name string) db.Column {
		for _, c := range td.Columns {
			if c.Name == name {
				return c
			}
		}
		t.Fatalf("column %s missing", name)
		return db.Column{}
	}
	if id := col("id"); !id.IsPK || id.Default == nil || !strings.Contains(*id.Default, "nextval") || id.Position != 1 {
		t.Errorf("id column = %+v", id)
	}
	if st := col("status"); st.Type != "order_status" || st.Nullable {
		t.Errorf("status column = %+v", st)
	}
	if n := col("note"); !n.Nullable || n.Default != nil {
		t.Errorf("note column = %+v", n)
	}
	if !slices.Equal(td.PrimaryKey(), []string{"id"}) {
		t.Errorf("pk = %v", td.PrimaryKey())
	}

	var ixNames []string
	for _, ix := range td.Indexes {
		ixNames = append(ixNames, ix.Name)
		if ix.Name == "orders_status_created_idx" {
			if !slices.Equal(ix.Columns, []string{"status", "created_at"}) || ix.Method != "btree" || ix.Unique {
				t.Errorf("index = %+v", ix)
			}
			if !strings.Contains(ix.Definition, "created_at DESC") || ix.Size == "" {
				t.Errorf("index = %+v", ix)
			}
		}
	}
	if len(td.Indexes) != 3 || !td.Indexes[0].Primary {
		t.Errorf("indexes = %v", ixNames)
	}

	if len(td.ForeignKeys) != 1 {
		t.Fatalf("fks = %+v", td.ForeignKeys)
	}
	fk := td.ForeignKeys[0]
	if fk.RefTable != "users" || !slices.Equal(fk.Columns, []string{"user_id"}) ||
		!slices.Equal(fk.RefColumns, []string{"id"}) || fk.OnDelete != "CASCADE" || fk.OnUpdate != "NO ACTION" {
		t.Errorf("fk = %+v", fk)
	}
	if len(td.ReferencedBy) != 1 || td.ReferencedBy[0].Table != "order_items" ||
		!slices.Equal(td.ReferencedBy[0].Columns, []string{"order_id"}) {
		t.Errorf("referenced by = %+v", td.ReferencedBy)
	}
	if len(td.Triggers) != 1 || td.Triggers[0].Name != "orders_touch" ||
		td.Triggers[0].Timing != "BEFORE" || td.Triggers[0].Event != "UPDATE" ||
		!strings.Contains(td.Triggers[0].Definition, "touch_updated_at") {
		t.Errorf("triggers = %+v", td.Triggers)
	}
	stats := map[string]string{}
	for _, kv := range td.Stats {
		stats[kv.Key] = kv.Value
	}
	if stats["Kind"] != "table" || stats["Owner"] != "squi" || stats["Total size"] == "" || stats["Tablespace"] != "pg_default" {
		t.Errorf("stats = %v", td.Stats)
	}

	td, err = c.TableDetail(ctx, db.ObjectRef{Schema: "public", Name: "order_items"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(td.PrimaryKey(), []string{"order_id", "product_id"}) {
		t.Errorf("order_items pk = %v", td.PrimaryKey())
	}
	if len(td.ForeignKeys) != 2 {
		t.Errorf("order_items fks = %+v", td.ForeignKeys)
	}
	if len(td.Constraints) != 1 || td.Constraints[0].Type != "CHECK" || !strings.Contains(td.Constraints[0].Definition, "qty > 0") {
		t.Errorf("constraints = %+v", td.Constraints)
	}

	for _, name := range []string{"paid_orders", "revenue_by_country"} {
		td, err := c.TableDetail(ctx, db.ObjectRef{Schema: "public", Name: name})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(td.Columns) == 0 || len(td.Stats) == 0 {
			t.Errorf("%s detail = %+v", name, td)
		}
	}

	td, err = c.TableDetail(ctx, db.ObjectRef{Schema: "public", Name: "users"})
	if err != nil {
		t.Fatal(err)
	}
	if td.Comment != "Registered customers" || len(td.Constraints) != 1 || td.Constraints[0].Type != "UNIQUE" {
		t.Errorf("users detail comment=%q constraints=%+v", td.Comment, td.Constraints)
	}

	if _, err := c.TableDetail(ctx, db.ObjectRef{Schema: "public", Name: "nope"}); err == nil {
		t.Error("expected not found error")
	}
}

func TestDDL(t *testing.T) {
	c := open(t, testConfig(t), nil)
	ctx := context.Background()
	cases := []struct {
		ref  db.ObjectRef
		want []string
	}{
		{db.ObjectRef{Schema: "public", Name: "orders", Kind: db.KindTable}, []string{
			"CREATE TABLE public.orders (",
			"id bigint DEFAULT nextval('orders_id_seq'::regclass) NOT NULL",
			"status order_status DEFAULT 'pending'::order_status NOT NULL",
			"CONSTRAINT orders_pkey PRIMARY KEY (id)",
			"CONSTRAINT orders_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE",
			"CREATE INDEX orders_status_created_idx ON public.orders USING btree (status, created_at DESC);",
			"CREATE TRIGGER orders_touch BEFORE UPDATE ON orders",
		}},
		{db.ObjectRef{Schema: "public", Name: "users", Kind: db.KindTable}, []string{
			"COMMENT ON TABLE public.users IS 'Registered customers';",
			"COMMENT ON COLUMN public.users.email IS 'Login email, unique';",
			"CONSTRAINT users_email_key UNIQUE (email)",
		}},
		{db.ObjectRef{Schema: "public", Name: "paid_orders", Kind: db.KindView}, []string{
			"CREATE OR REPLACE VIEW public.paid_orders AS\n", "WHERE o.status = 'paid'::order_status;",
		}},
		{db.ObjectRef{Schema: "public", Name: "revenue_by_country", Kind: db.KindMatView}, []string{
			"CREATE MATERIALIZED VIEW public.revenue_by_country AS\n", "GROUP BY u.country;",
		}},
		{db.ObjectRef{Schema: "public", Name: "touch_updated_at", Kind: db.KindFunction}, []string{
			"CREATE OR REPLACE FUNCTION public.touch_updated_at()", "RETURNS trigger", "$function$;",
		}},
		{db.ObjectRef{Schema: "public", Name: "invoice_seq", Kind: db.KindSequence}, []string{
			"CREATE SEQUENCE public.invoice_seq", "START WITH 1000", "INCREMENT BY 1",
		}},
	}
	for _, tc := range cases {
		ddl, err := c.DDL(ctx, tc.ref)
		if err != nil {
			t.Errorf("DDL %s: %v", tc.ref.Name, err)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(ddl, w) {
				t.Errorf("DDL %s missing %q:\n%s", tc.ref.Name, w, ddl)
			}
		}
	}
	ddl, _ := c.DDL(ctx, db.ObjectRef{Schema: "public", Name: "orders", Kind: db.KindTable})
	if strings.Contains(ddl, "CREATE UNIQUE INDEX orders_pkey") {
		t.Errorf("constraint-backed index emitted:\n%s", ddl)
	}
}

func TestRoutineOverloads(t *testing.T) {
	c := open(t, testConfig(t), nil)
	ctx := context.Background()
	s := newSession(t, c, "")
	runAll(t, s, "CREATE OR REPLACE FUNCTION squi_test_ov(a int) RETURNS int LANGUAGE sql AS 'SELECT a'")
	runAll(t, s, "CREATE OR REPLACE FUNCTION squi_test_ov(a text) RETURNS text LANGUAGE sql AS 'SELECT a'")
	runAll(t, s, "CREATE OR REPLACE PROCEDURE squi_test_proc() LANGUAGE sql AS 'SELECT 1'")
	t.Cleanup(func() {
		runAll(t, s, "DROP FUNCTION squi_test_ov(int); DROP FUNCTION squi_test_ov(text); DROP PROCEDURE squi_test_proc()")
	})
	objs, err := c.Objects(ctx, "", "public")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, o := range objs {
		if strings.HasPrefix(o.Name, "squi_test_") {
			names = append(names, string(o.Kind)+":"+o.Name)
		}
	}
	want := []string{"function:squi_test_ov(a integer)", "function:squi_test_ov(a text)", "procedure:squi_test_proc"}
	if !slices.Equal(names, want) {
		t.Errorf("routines = %v", names)
	}
	ddl, err := c.DDL(ctx, db.ObjectRef{Schema: "public", Name: "squi_test_ov(a text)", Kind: db.KindFunction})
	if err != nil || !strings.Contains(ddl, "RETURNS text") {
		t.Errorf("overload DDL = %q, %v", ddl, err)
	}
	if _, err := c.DDL(ctx, db.ObjectRef{Schema: "public", Name: "squi_test_ov", Kind: db.KindFunction}); err == nil {
		t.Error("expected ambiguity error")
	}
}

func TestRunStreaming(t *testing.T) {
	c := open(t, testConfig(t), nil)
	s := newSession(t, c, "")

	rs, err := s.Run(context.Background(), "SELECT id, status, note, total FROM orders ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	cols := rs.Columns()
	if len(cols) != 4 || cols[0].Type != "int8" || cols[1].Type != "order_status" || cols[3].Type != "numeric" {
		t.Errorf("columns = %+v", cols)
	}
	total, nulls := 0, 0
	for {
		rows, err := rs.Next(500)
		if total == 0 && len(rows) > 0 && rows[0][0].S != "1" {
			t.Errorf("first row = %v", rows[0])
		}
		for _, r := range rows {
			if r[2].Null {
				nulls++
			}
		}
		total += len(rows)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 500 {
			t.Fatalf("short batch %d before EOF", len(rows))
		}
	}
	rs.Close()
	if total != 20000 || nulls != 18000 {
		t.Errorf("rows = %d, nulls = %d", total, nulls)
	}
	if rs.CommandTag() != "SELECT 20000" || rs.RowsAffected() != 20000 {
		t.Errorf("tag = %q rows = %d", rs.CommandTag(), rs.RowsAffected())
	}

	_, rows := runAll(t, s, "SELECT NULL::text, ''::text, 'a\tb', ARRAY[1,NULL], true")
	if r := rows[0]; !r[0].Null || r[1].Null || r[1].S != "" || r[2].S != "a\tb" || r[3].S != "{1,NULL}" || r[4].S != "t" {
		t.Errorf("text values = %+v", r)
	}

	rs, _ = runAll(t, s, "UPDATE audit_log SET message = message WHERE false")
	if rs.CommandTag() != "UPDATE 0" || len(rs.Columns()) != 0 {
		t.Errorf("update tag = %q", rs.CommandTag())
	}

	rs, rows = runAll(t, s, "SET search_path = public; SELECT count(*) FROM users")
	if len(rows) != 1 || rows[0][0].S != "500" {
		t.Errorf("multi-statement rows = %v (tag %q)", rows, rs.CommandTag())
	}
}

func TestCloseMidStream(t *testing.T) {
	c := open(t, testConfig(t), nil)
	s := newSession(t, c, "")
	for _, sql := range []string{
		"SELECT * FROM orders",
		"SELECT generate_series(1, 200000000)",
	} {
		rs, err := s.Run(context.Background(), sql)
		if err != nil {
			t.Fatal(err)
		}
		if rows, err := rs.Next(100); err != nil || len(rows) != 100 {
			t.Fatalf("next: %d rows, %v", len(rows), err)
		}
		start := time.Now()
		if err := rs.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("close of %q took %v", sql, d)
		}
		if _, rows := runAll(t, s, "SELECT 42"); rows[0][0].S != "42" {
			t.Error("session unusable after close")
		}
	}
}

func TestQueryError(t *testing.T) {
	c := open(t, testConfig(t), nil)
	s := newSession(t, c, "")
	_, err := s.Run(context.Background(), "SELECT 1\nFROM no_such_table")
	var qe *QueryError
	if !errors.As(err, &qe) {
		t.Fatalf("err = %v", err)
	}
	if qe.Code != "42P01" || qe.Position != 15 || !strings.Contains(qe.Error(), "at character 15") {
		t.Errorf("query error = %+v (%v)", qe, qe)
	}

	_, err = s.Run(context.Background(), "SELECT 1/0")
	if !errors.As(err, &qe) || qe.Code != "22012" {
		t.Errorf("division error = %v", err)
	}

	_, err = s.Explain(context.Background(), "SELECT * FROM nope", false)
	if !errors.As(err, &qe) || qe.Position != 15 {
		t.Errorf("explain error = %v", err)
	}
}

func TestReadOnly(t *testing.T) {
	cfg := testConfig(t)
	cfg.ReadOnly = true
	c := open(t, cfg, nil)
	s := newSession(t, c, "")
	_, err := s.Run(context.Background(), "INSERT INTO audit_log (message) VALUES ('x')")
	var qe *QueryError
	if !errors.As(err, &qe) || qe.Code != "25006" {
		t.Fatalf("read-only insert err = %v", err)
	}
}

func TestTransactions(t *testing.T) {
	c := open(t, testConfig(t), nil)
	s := newSession(t, c, "")
	ctx := context.Background()
	if s.InTx() {
		t.Fatal("in tx at start")
	}
	if err := s.Begin(ctx); err != nil || !s.InTx() {
		t.Fatalf("begin: %v intx=%v", err, s.InTx())
	}
	if _, err := s.Run(ctx, "SELECT 1/0"); err == nil {
		t.Fatal("expected error")
	}
	if !s.InTx() {
		t.Error("failed tx should count as in tx")
	}
	if err := s.UseDatabase(ctx, "warehouse"); err == nil {
		t.Error("UseDatabase inside tx should fail")
	}
	if err := s.Rollback(ctx); err != nil || s.InTx() {
		t.Fatalf("rollback: %v intx=%v", err, s.InTx())
	}
	runAll(t, s, "BEGIN")
	if !s.InTx() {
		t.Error("typed BEGIN not tracked")
	}
	if err := s.Commit(ctx); err != nil || s.InTx() {
		t.Fatalf("commit: %v", err)
	}
}

func TestCancel(t *testing.T) {
	c := open(t, testConfig(t), nil)
	s := newSession(t, c, "")
	ctx := context.Background()
	start := time.Now()
	go func() {
		time.Sleep(300 * time.Millisecond)
		if err := s.Cancel(ctx); err != nil {
			t.Errorf("cancel: %v", err)
		}
	}()
	rs, err := s.Run(ctx, "SELECT pg_sleep(10)")
	if err == nil {
		_, err = rs.Next(10)
		rs.Close()
	}
	var qe *QueryError
	if !errors.As(err, &qe) || qe.Code != "57014" {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("cancel took %v", d)
	}
}

func TestUseDatabase(t *testing.T) {
	c := open(t, testConfig(t), nil)
	s := newSession(t, c, "")
	if s.CurrentDatabase() != "squi" {
		t.Errorf("current = %q", s.CurrentDatabase())
	}
	if err := s.UseDatabase(context.Background(), "warehouse"); err != nil {
		t.Fatal(err)
	}
	if _, rows := runAll(t, s, "SELECT current_database()"); rows[0][0].S != "warehouse" || s.CurrentDatabase() != "warehouse" {
		t.Errorf("current_database = %v", rows)
	}
	if err := s.UseDatabase(context.Background(), "no_such_db"); err == nil {
		t.Error("expected error")
	}
	if _, rows := runAll(t, s, "SELECT current_database()"); rows[0][0].S != "warehouse" {
		t.Error("failed switch broke the session")
	}
}

func TestExplain(t *testing.T) {
	c := open(t, testConfig(t), nil)
	s := newSession(t, c, "")
	ctx := context.Background()
	sql := "SELECT o.id, u.email FROM orders o JOIN users u ON u.id = o.user_id WHERE o.total > 100;"

	p, err := s.Explain(ctx, sql, false)
	if err != nil {
		t.Fatal(err)
	}
	if p.Cost <= 0 || p.Rows <= 0 || p.Time != 0 || !strings.Contains(p.Raw, `"Node Type"`) {
		t.Errorf("plan root = %+v", p)
	}
	var titles []string
	var walk func(n *db.PlanNode)
	walk = func(n *db.PlanNode) {
		titles = append(titles, n.Title)
		for _, ch := range n.Children {
			walk(ch)
		}
	}
	walk(p)
	if !slices.Contains(titles, "Seq Scan on public.orders o") {
		t.Errorf("titles = %v", titles)
	}

	p, err = s.Explain(ctx, sql, true)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, kv := range p.Details {
		keys[kv.Key] = true
	}
	if p.Time <= 0 || !keys["Execution Time"] || !keys["Planning Time"] {
		t.Errorf("analyze root = %+v", p)
	}
	if s.InTx() {
		t.Error("explain left a transaction open")
	}
}

func TestBulkInsert(t *testing.T) {
	c := open(t, testConfig(t), nil)
	s := newSession(t, c, "")
	ctx := context.Background()
	runAll(t, s, `CREATE TEMP TABLE bulk_t (id int, "Weird Name" text, n numeric)`)
	ref := db.ObjectRef{Schema: "pg_temp", Name: "bulk_t"}
	rows := [][]db.Cell{
		{db.Text("1"), db.Text(`he said "hi", then left`), db.Text("1.5")},
		{db.Text("2"), db.NullCell, db.NullCell},
		{db.Text("3"), db.Text("line1\nline2\r\nline3"), db.Text("0")},
		{db.Text("4"), db.Text(`\N`), db.Text("-2")},
		{db.Text("5"), db.Text(""), db.Text("7")},
	}
	n, err := s.BulkInsert(ctx, ref, []string{"id", "Weird Name", "n"}, rows)
	if err != nil || n != 5 {
		t.Fatalf("bulk insert = %d, %v", n, err)
	}
	_, got := runAll(t, s, `SELECT id, "Weird Name", n FROM bulk_t ORDER BY id`)
	for i, r := range rows {
		for j, cell := range r {
			if got[i][j] != cell {
				t.Errorf("row %d col %d = %+v, want %+v", i, j, got[i][j], cell)
			}
		}
	}
	if _, err := s.BulkInsert(ctx, ref, []string{"id"}, [][]db.Cell{{db.Text("x")}}); err == nil {
		t.Error("expected COPY error")
	}
	if _, err := s.BulkInsert(ctx, ref, []string{"id", "n"}, [][]db.Cell{{db.Text("1")}}); err == nil {
		t.Error("expected row width error")
	}
	if _, rows := runAll(t, s, "SELECT count(*) FROM bulk_t"); rows[0][0].S != "5" {
		t.Errorf("count after failed copies = %v", rows)
	}
}

func TestAdmin(t *testing.T) {
	c := open(t, testConfig(t), nil)
	ctx := context.Background()
	a := c.Admin()

	other := newSession(t, c, "")
	pid := func() string {
		_, rows := runAll(t, other, "SELECT pg_backend_pid()")
		return rows[0][0].S
	}()

	procs, err := a.Processes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if procs.Columns[0] != "pid" {
		t.Errorf("columns = %v", procs.Columns)
	}
	found := false
	for _, r := range procs.Rows {
		found = found || r[0].S == pid
	}
	if !found {
		t.Errorf("session pid %s not in processes", pid)
	}
	if err := a.CancelQuery(ctx, pid); err != nil {
		t.Errorf("cancel: %v", err)
	}
	if err := a.CancelQuery(ctx, "1"); err == nil {
		t.Error("expected error cancelling pid 1")
	}
	if err := a.Kill(ctx, pid); err != nil {
		t.Errorf("kill: %v", err)
	}

	for name, fn := range map[string]func(context.Context) (*db.Table, error){
		"variables": a.Variables,
		"users":     a.Users,
		"sizes":     a.Sizes,
		"grants":    func(ctx context.Context) (*db.Table, error) { return a.Grants(ctx, "squi") },
	} {
		tb, err := fn(ctx)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if len(tb.Columns) == 0 || len(tb.Rows) == 0 {
			t.Errorf("%s empty: %+v", name, tb)
		}
	}
}

func TestCustomDial(t *testing.T) {
	cfg := testConfig(t)
	var dials atomic.Int32
	var d net.Dialer
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		dials.Add(1)
		return d.DialContext(ctx, network, addr)
	}
	c := open(t, cfg, dial)
	if dials.Load() == 0 {
		t.Fatal("dial func not used by Open")
	}
	before := dials.Load()
	s := newSession(t, c, "warehouse")
	if dials.Load() <= before {
		t.Error("dial func not used by NewSession")
	}
	before = dials.Load()
	go func() {
		time.Sleep(200 * time.Millisecond)
		s.Cancel(context.Background())
	}()
	rs, err := s.Run(context.Background(), "SELECT pg_sleep(5)")
	if err == nil {
		_, err = rs.Next(1)
		rs.Close()
	}
	var qe *QueryError
	if !errors.As(err, &qe) || qe.Code != "57014" {
		t.Errorf("cancel through dialer: %v", err)
	}
	if dials.Load() <= before {
		t.Error("cancel request did not use dial func")
	}
}

func TestEstimateRows(t *testing.T) {
	cfg := testConfig(t)
	c := open(t, cfg, nil)
	s := newSession(t, c, "")
	runAll(t, s, "ANALYZE orders")
	est := c.(db.RowEstimator)
	ctx := context.Background()
	n, exact, ok, err := est.EstimateRows(ctx, db.ObjectRef{Database: cfg.Database, Schema: "public", Name: "orders"})
	if err != nil || !ok || exact || n <= 0 {
		t.Fatalf("orders: n=%d exact=%v ok=%v err=%v", n, exact, ok, err)
	}
	if _, _, ok, err := est.EstimateRows(ctx, db.ObjectRef{Database: cfg.Database, Schema: "public", Name: "paid_orders"}); err != nil || ok {
		t.Fatalf("view: ok=%v err=%v", ok, err)
	}
}
