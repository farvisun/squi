package mssql

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

// Integration tests run against the dev container when configured:
//
//	SQUI_TEST_MSSQL=sqlserver://squi:squi@127.0.0.1:14339/squi
func TestSQLServer(t *testing.T) {
	dsn := os.Getenv("SQUI_TEST_MSSQL")
	if dsn == "" {
		t.Skip("SQUI_TEST_MSSQL not set")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(u.Port())
	pass, _ := u.User.Password()
	s := &suite{t: t, cfg: db.ConnConfig{Host: u.Hostname(), Port: port, User: u.User.Username(), Password: pass,
		Database: strings.TrimPrefix(u.Path, "/")}}
	s.c = s.open(s.cfg, nil)
	t.Logf("server: %s", s.c.ServerVersion())

	var orders int
	s.scalar("SELECT COUNT(*) FROM orders", &orders)
	if orders != 20000 {
		t.Skipf("seed incomplete: %d orders", orders)
	}

	t.Run("Databases", s.testDatabases)
	t.Run("Objects", s.testObjects)
	t.Run("AllColumns", s.testAllColumns)
	t.Run("TableDetail", s.testTableDetail)
	t.Run("DDL", s.testDDL)
	t.Run("Streaming", s.testStreaming)
	t.Run("CloseMidStream", s.testCloseMidStream)
	t.Run("Types", s.testTypes)
	t.Run("Literals", s.testLiterals)
	t.Run("TagsAndBatches", s.testTagsAndBatches)
	t.Run("ErrorLine", s.testErrorLine)
	t.Run("Transactions", s.testTransactions)
	t.Run("Cancel", s.testCancel)
	t.Run("ContextCancel", s.testContextCancel)
	t.Run("UseDatabase", s.testUseDatabase)
	t.Run("Explain", s.testExplain)
	t.Run("ExplainAnalyze", s.testExplainAnalyze)
	t.Run("BulkInsert", s.testBulkInsert)
	t.Run("Admin", s.testAdmin)
	t.Run("Dial", s.testDial)
	t.Run("EstimateRows", s.testEstimateRows)
	t.Run("BadLogin", s.testBadLogin)
}

type suite struct {
	t   *testing.T
	cfg db.ConnConfig
	c   db.Conn
}

func (s *suite) open(cfg db.ConnConfig, dial db.DialFunc) db.Conn {
	s.t.Helper()
	drv, ok := db.Get(db.SQLServer)
	if !ok {
		s.t.Fatal("driver not registered")
	}
	c, err := drv.Open(context.Background(), cfg, dial)
	if err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(func() { c.Close() })
	return c
}

func (s *suite) session(t *testing.T, c db.Conn) db.Session {
	t.Helper()
	if c == nil {
		c = s.c
	}
	sess, err := c.NewSession(context.Background(), s.cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

func (s *suite) scalar(q string, dest any) {
	s.t.Helper()
	if err := s.c.(*conn).meta.QueryRow(q).Scan(dest); err != nil {
		s.t.Fatal(err)
	}
}

func all(t *testing.T, sess db.Session, q string) ([]db.ResultColumn, [][]db.Cell, db.ResultStream) {
	t.Helper()
	st, err := sess.Run(context.Background(), q)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer st.Close()
	var rows [][]db.Cell
	for {
		batch, err := st.Next(100)
		rows = append(rows, batch...)
		if err == io.EOF {
			return st.Columns(), rows, st
		}
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func mustUsable(t *testing.T, sess db.Session) {
	t.Helper()
	if _, rows, _ := all(t, sess, "SELECT 42"); len(rows) != 1 || rows[0][0].S != "42" {
		t.Fatalf("session unusable: %v", rows)
	}
}

func (s *suite) testDatabases(t *testing.T) {
	dbs, err := s.c.Databases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range dbs {
		names = append(names, d.Name)
	}
	iSqui, iWh, iMaster := slices.Index(names, "squi"), slices.Index(names, "warehouse"), slices.Index(names, "master")
	if iSqui < 0 || iWh < 0 || iMaster < 0 || iMaster < iSqui || iMaster < iWh {
		t.Fatalf("databases = %v", names)
	}
	sc, err := s.c.Schemas(context.Background(), "squi")
	if err != nil {
		t.Fatal(err)
	}
	if len(sc) < 3 || sc[0] != "dbo" || !slices.Contains(sc, "analytics") || slices.Contains(sc, "db_owner") || slices.Contains(sc, "guest") {
		t.Errorf("schemas = %v", sc)
	}
}

func (s *suite) testObjects(t *testing.T) {
	objs, err := s.c.Objects(context.Background(), "squi", "dbo")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]db.ObjectKind{}
	var order []db.ObjectKind
	for _, o := range objs {
		got[o.Name] = o.Kind
		order = append(order, o.Kind)
	}
	want := map[string]db.ObjectKind{"users": db.KindTable, "orders": db.KindTable, "paid_orders": db.KindView,
		"invoice_seq": db.KindSequence, "orders_for_user": db.KindProcedure, "orders_touch": db.KindTrigger}
	for n, k := range want {
		if got[n] != k {
			t.Errorf("%s: kind %q, want %q", n, got[n], k)
		}
	}
	if !slices.IsSortedFunc(order, func(a, b db.ObjectKind) int { return kindRank[a] - kindRank[b] }) {
		t.Errorf("not grouped by kind: %v", order)
	}
	for _, o := range objs {
		if o.Name == "users" && o.Comment != "Registered customers" {
			t.Errorf("users comment = %q", o.Comment)
		}
	}
	// other database, other schema
	objs, err = s.c.Objects(context.Background(), "warehouse", "dbo")
	if err != nil || len(objs) != 1 || objs[0].Name != "facts" {
		t.Errorf("warehouse objects = %v, %v", objs, err)
	}
	objs, err = s.c.Objects(context.Background(), "squi", "analytics")
	if err != nil || len(objs) != 1 || objs[0].Name != "events" {
		t.Errorf("analytics objects = %v, %v", objs, err)
	}
}

func (s *suite) testAllColumns(t *testing.T) {
	cols, err := s.c.AllColumns(context.Background(), "squi", "dbo")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cols["users"], []string{"id", "email", "name", "country", "meta", "created_at"}) {
		t.Errorf("users = %v", cols["users"])
	}
	if len(cols["paid_orders"]) != 8 {
		t.Errorf("paid_orders = %v", cols["paid_orders"])
	}
}

func (s *suite) testTableDetail(t *testing.T) {
	ctx := context.Background()
	td, err := s.c.TableDetail(ctx, db.ObjectRef{Database: "squi", Schema: "dbo", Name: "orders", Kind: db.KindTable})
	if err != nil {
		t.Fatal(err)
	}
	if pk := td.PrimaryKey(); !slices.Equal(pk, []string{"id"}) {
		t.Errorf("pk = %v", pk)
	}
	byName := map[string]db.Column{}
	for _, c := range td.Columns {
		byName[c.Name] = c
	}
	if c := byName["id"]; c.Extra != "IDENTITY(1,1)" || c.Type != "bigint" || c.Nullable {
		t.Errorf("id = %+v", c)
	}
	if c := byName["total"]; c.Type != "decimal(12,2)" || c.Default == nil || *c.Default != "((0))" {
		t.Errorf("total = %+v", c)
	}
	if c := byName["note"]; c.Type != "nvarchar(max)" || !c.Nullable {
		t.Errorf("note = %+v", c)
	}
	if c := byName["created_at"]; c.Type != "datetime2(3)" {
		t.Errorf("created_at = %+v", c)
	}
	var ix *db.Index
	for i := range td.Indexes {
		if td.Indexes[i].Name == "orders_status_created_idx" {
			ix = &td.Indexes[i]
		}
	}
	if ix == nil || !slices.Equal(ix.Columns, []string{"status", "created_at"}) ||
		ix.Definition != "CREATE NONCLUSTERED INDEX [orders_status_created_idx] ([status], [created_at] DESC) INCLUDE ([total])" {
		t.Errorf("index = %+v", ix)
	}
	if len(td.ForeignKeys) != 1 || td.ForeignKeys[0].RefTable != "users" || !slices.Equal(td.ForeignKeys[0].Columns, []string{"user_id"}) ||
		td.ForeignKeys[0].OnDelete != "CASCADE" {
		t.Errorf("fks = %+v", td.ForeignKeys)
	}
	if len(td.ReferencedBy) != 1 || td.ReferencedBy[0].Table != "order_items" || !slices.Equal(td.ReferencedBy[0].RefColumns, []string{"id"}) {
		t.Errorf("referenced by = %+v", td.ReferencedBy)
	}
	if len(td.Constraints) != 1 || td.Constraints[0].Type != "CHECK" || !strings.Contains(td.Constraints[0].Definition, "pending") {
		t.Errorf("constraints = %+v", td.Constraints)
	}
	if len(td.Triggers) != 1 || td.Triggers[0].Event != "UPDATE" || td.Triggers[0].Timing != "AFTER" {
		t.Errorf("triggers = %+v", td.Triggers)
	}
	var rows string
	for _, kv := range td.Stats {
		if kv.Key == "Rows (approx.)" {
			rows = kv.Value
		}
	}
	if rows != "20000" {
		t.Errorf("stats = %v", td.Stats)
	}

	users, err := s.c.TableDetail(ctx, db.ObjectRef{Database: "squi", Schema: "dbo", Name: "users"})
	if err != nil {
		t.Fatal(err)
	}
	if users.Comment != "Registered customers" || users.Columns[1].Comment != "Login email, unique" || users.Columns[1].Type != "nvarchar(200)" {
		t.Errorf("users = %q %+v", users.Comment, users.Columns[1])
	}
	if len(users.Constraints) != 1 || users.Constraints[0].Type != "UNIQUE" {
		t.Errorf("users constraints = %+v", users.Constraints)
	}
	items, err := s.c.TableDetail(ctx, db.ObjectRef{Database: "squi", Schema: "dbo", Name: "order_items"})
	if err != nil {
		t.Fatal(err)
	}
	if pk := items.PrimaryKey(); !slices.Equal(pk, []string{"order_id", "product_id"}) {
		t.Errorf("order_items pk = %v", pk)
	}
	view, err := s.c.TableDetail(ctx, db.ObjectRef{Database: "squi", Schema: "dbo", Name: "paid_orders"})
	if err != nil || view.Ref.Kind != db.KindView || len(view.Columns) != 8 {
		t.Errorf("view = %+v, %v", view, err)
	}
	if _, err := s.c.TableDetail(ctx, db.ObjectRef{Database: "squi", Schema: "dbo", Name: "nope"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
}

func (s *suite) testDDL(t *testing.T) {
	ctx := context.Background()
	ddl := func(name string) string {
		t.Helper()
		d, err := s.c.DDL(ctx, db.ObjectRef{Database: "squi", Schema: "dbo", Name: name})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return d
	}
	tbl := ddl("orders")
	for _, want := range []string{"CREATE TABLE [dbo].[orders]", "[id] bigint IDENTITY(1,1) NOT NULL", "PRIMARY KEY CLUSTERED ([id])",
		"FOREIGN KEY ([user_id]) REFERENCES [dbo].[users] ([id]) ON DELETE CASCADE",
		"CREATE NONCLUSTERED INDEX [orders_user_idx] ON [dbo].[orders] ([user_id]);", "CREATE TRIGGER orders_touch"} {
		if !strings.Contains(tbl, want) {
			t.Errorf("table DDL lacks %q:\n%s", want, tbl)
		}
	}
	// the scripted table can be created
	sess := s.session(t, nil)
	script := strings.ReplaceAll(strings.Split(tbl, "\nGO")[0], "[dbo].[orders]", "[#orders_copy]")
	script = strings.ReplaceAll(script, "REFERENCES [dbo].[users] ([id]) ON DELETE CASCADE", "REFERENCES [dbo].[users] ([id])")
	for _, st := range strings.Split(script, ";\n") {
		if st = strings.TrimSpace(strings.TrimSuffix(st, ";")); st != "" && !strings.HasPrefix(st, "--") || strings.Contains(st, "CREATE") {
			if _, err := sess.Run(ctx, st); err != nil {
				t.Errorf("replay %q: %v", st, err)
			}
		}
	}
	if v := ddl("paid_orders"); !strings.HasPrefix(v, "CREATE VIEW paid_orders") {
		t.Errorf("view DDL = %q", v)
	}
	if p := ddl("orders_for_user"); !strings.HasPrefix(p, "CREATE PROCEDURE orders_for_user") {
		t.Errorf("proc DDL = %q", p)
	}
	if q := ddl("invoice_seq"); !strings.Contains(q, "START WITH 1000") {
		t.Errorf("sequence DDL = %q", q)
	}
}

func (s *suite) testStreaming(t *testing.T) {
	sess := s.session(t, nil)
	st, err := sess.Run(context.Background(), "SELECT id, status, total, created_at FROM orders ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cols := st.Columns()
	if len(cols) != 4 || cols[0].Type != "bigint" || cols[2].Type != "decimal" || cols[3].Type != "datetime2" {
		t.Fatalf("columns = %+v", cols)
	}
	first, err := st.Next(500)
	if err != nil || len(first) != 500 || first[0][0].S != "1" {
		t.Fatalf("first batch: %d rows, %v", len(first), err)
	}
	if total := first[0][2].S; total != "1.31" {
		t.Errorf("decimal = %q", total)
	}
	n := len(first)
	for {
		b, err := st.Next(5000)
		n += len(b)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if n != 20000 || st.CommandTag() != "SELECT 20000" {
		t.Fatalf("rows = %d, tag %q", n, st.CommandTag())
	}
}

func (s *suite) testCloseMidStream(t *testing.T) {
	sess := s.session(t, nil)
	st, err := sess.Run(context.Background(), "SELECT a.id FROM orders a CROSS JOIN orders b")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Next(10); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("close took %v", d)
	}
	mustUsable(t, sess)
}

func (s *suite) testTypes(t *testing.T) {
	sess := s.session(t, nil)
	_, rows, _ := all(t, sess, `SELECT CAST('6F9619FF-8B86-D011-B42D-00C04FC964FF' AS uniqueidentifier), 0xCAFEBABE,
		CAST('2024-05-06 07:08:09.1234567 +03:30' AS datetimeoffset), CAST('2024-05-06' AS date), CAST(1 AS bit),
		N'caf\u00e9 ✓', CAST(1.5 AS real), CAST(NULL AS int), CAST('12:30:00' AS time(0)), CAST(12.5 AS money),
		CAST('2024-05-06 07:08:09' AS datetime)`)
	got := make([]string, len(rows[0]))
	for i, c := range rows[0] {
		got[i] = c.S
		if c.Null {
			got[i] = "NULL"
		}
	}
	want := []string{"6F9619FF-8B86-D011-B42D-00C04FC964FF", "0xCAFEBABE", "2024-05-06 07:08:09.1234567 +03:30",
		"2024-05-06", "1", `caf\u00e9 ✓`, "1.5", "NULL", "12:30:00", "12.5000", "2024-05-06 07:08:09"}
	if !slices.Equal(got, want) {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func (s *suite) testLiterals(t *testing.T) {
	sess := s.session(t, nil)
	d := s.c.Dialect()
	for _, v := range []string{"plain", "it's", `back\slash`, "naïve ✓", "", "line\nbreak"} {
		_, rows, _ := all(t, sess, "SELECT "+d.QuoteLiteral(v))
		if rows[0][0].S != v {
			t.Errorf("literal %q came back as %q", v, rows[0][0].S)
		}
	}
	_, rows, _ := all(t, sess, "SELECT 1 AS "+d.QuoteIdent("we]ird"))
	if len(rows) != 1 {
		t.Fatal("quoted ident")
	}
	_, rows, _ = all(t, sess, d.Select(db.Browse{Ref: db.ObjectRef{Schema: "dbo", Name: "orders"}, Where: "status = 'paid'",
		Order: []db.Order{{Column: "id", Desc: true}}}, 3))
	if len(rows) != 3 || rows[0][0].S != "19997" {
		t.Errorf("select top = %v", rows)
	}
}

func (s *suite) testTagsAndBatches(t *testing.T) {
	sess := s.session(t, nil)
	ctx := context.Background()
	if _, err := sess.Run(ctx, "CREATE TABLE #t (id int PRIMARY KEY, v nvarchar(10))"); err != nil {
		t.Fatal(err)
	}
	st, err := sess.Run(ctx, "INSERT INTO #t VALUES (1, 'a'), (2, NULL), (3, 'c')")
	if err != nil || st.CommandTag() != "INSERT 3" || st.RowsAffected() != 3 {
		t.Fatalf("insert: %v %q", err, st.CommandTag())
	}
	st, err = sess.Run(ctx, "UPDATE #t SET v = 'x' WHERE id > 1")
	if err != nil || st.CommandTag() != "UPDATE 2" {
		t.Fatalf("update: %v %q", err, st.CommandTag())
	}
	// OUTPUT returns rows
	cols, rows, _ := all(t, sess, "DELETE FROM #t OUTPUT deleted.id WHERE id = 3")
	if len(cols) != 1 || len(rows) != 1 || rows[0][0].S != "3" {
		t.Fatalf("output: %v %v", cols, rows)
	}
	// a batch with variables returns its first result set
	_, rows, _ = all(t, sess, "DECLARE @n int = 1; SET @n = @n + 1; SELECT v FROM #t WHERE id = @n")
	if len(rows) != 1 || rows[0][0].S != "x" {
		t.Fatalf("batch: %v", rows)
	}
	_, rows, st = all(t, sess, "SELECT 1; SELECT 2")
	if len(rows) != 1 || !strings.Contains(st.CommandTag(), "further result sets") {
		t.Fatalf("multi: %v %q", rows, st.CommandTag())
	}
	_, rows, _ = all(t, sess, "EXEC orders_for_user @user_id = 7")
	if len(rows) != 40 {
		t.Fatalf("procedure rows = %d", len(rows))
	}
	_, rows, _ = all(t, sess, "SELECT v FROM #t WHERE id = 2")
	if rows[0][0].Null || rows[0][0].S != "x" {
		t.Fatalf("null: %v", rows)
	}
}

func (s *suite) testErrorLine(t *testing.T) {
	sess := s.session(t, nil)
	_, err := sess.Run(context.Background(), "SELECT 1\nFROM orders\nWHERE = 1")
	var qe *QueryError
	if !errors.As(err, &qe) || qe.Number != 102 || qe.Line != 3 {
		t.Fatalf("err = %#v", err)
	}
	mustUsable(t, sess)
}

func (s *suite) testTransactions(t *testing.T) {
	sess := s.session(t, nil)
	ctx := context.Background()
	all(t, sess, "CREATE TABLE #tx (id int)")
	if err := sess.Begin(ctx); err != nil || !sess.InTx() {
		t.Fatalf("begin: %v %v", err, sess.InTx())
	}
	all(t, sess, "INSERT INTO #tx VALUES (1)")
	if err := sess.Rollback(ctx); err != nil || sess.InTx() {
		t.Fatalf("rollback: %v %v", err, sess.InTx())
	}
	if _, rows, _ := all(t, sess, "SELECT COUNT(*) FROM #tx"); rows[0][0].S != "0" {
		t.Fatalf("rolled back rows = %v", rows)
	}
	// typed statements are tracked
	all(t, sess, "BEGIN TRAN")
	if !sess.InTx() {
		t.Fatal("BEGIN TRAN not tracked")
	}
	all(t, sess, "INSERT INTO #tx VALUES (2)")
	all(t, sess, "COMMIT")
	if sess.InTx() {
		t.Fatal("COMMIT not tracked")
	}
	// an error under XACT_ABORT ends the transaction
	all(t, sess, "SET XACT_ABORT ON")
	all(t, sess, "BEGIN TRANSACTION")
	if _, err := sess.Run(ctx, "INSERT INTO #tx VALUES ('x')"); err == nil {
		t.Fatal("want conversion error")
	}
	if sess.InTx() {
		t.Fatal("aborted transaction still tracked")
	}
	if err := sess.Commit(ctx); err != nil {
		t.Fatalf("commit without transaction: %v", err)
	}
}

func (s *suite) testCancel(t *testing.T) {
	sess := s.session(t, nil)
	all(t, sess, "CREATE TABLE #kept (v int); INSERT INTO #kept VALUES (7)")
	go func() {
		time.Sleep(300 * time.Millisecond)
		if err := sess.Cancel(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	start := time.Now()
	st, err := sess.Run(context.Background(), "WAITFOR DELAY '00:00:10'")
	if err == nil {
		_, err = st.Next(10)
		st.Close()
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("cancel took %v (err %v)", d, err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
	if _, rows, _ := all(t, sess, "SELECT v FROM #kept"); rows[0][0].S != "7" {
		t.Fatalf("session state lost: %v", rows)
	}
}

func (s *suite) testContextCancel(t *testing.T) {
	sess := s.session(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := sess.Run(ctx, "WAITFOR DELAY '00:00:10'")
	if d := time.Since(start); d > 3*time.Second || err == nil {
		t.Fatalf("context cancel took %v (err %v)", d, err)
	}
	mustUsable(t, sess)
}

func (s *suite) testUseDatabase(t *testing.T) {
	sess := s.session(t, nil)
	if sess.CurrentDatabase() != "squi" {
		t.Fatalf("current = %q", sess.CurrentDatabase())
	}
	if err := sess.UseDatabase(context.Background(), "warehouse"); err != nil {
		t.Fatal(err)
	}
	if _, rows, _ := all(t, sess, "SELECT COUNT(*) FROM facts"); rows[0][0].S != "2" {
		t.Fatalf("facts = %v", rows)
	}
	all(t, sess, "USE squi")
	if sess.CurrentDatabase() != "squi" {
		t.Fatalf("after USE: %q", sess.CurrentDatabase())
	}
	other, err := s.c.NewSession(context.Background(), "warehouse")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if other.CurrentDatabase() != "warehouse" {
		t.Fatalf("new session db = %q", other.CurrentDatabase())
	}
}

func (s *suite) testExplain(t *testing.T) {
	sess := s.session(t, nil)
	plan, err := sess.Explain(context.Background(), "SELECT * FROM orders o JOIN users u ON u.id = o.user_id WHERE o.user_id = 7;", false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Title != "SELECT" || len(plan.Children) != 1 || plan.Cost <= 0 || !strings.Contains(plan.Raw, "<ShowPlanXML") {
		t.Fatalf("plan = %+v", plan)
	}
	var titles []string
	var walk func(*db.PlanNode)
	walk = func(n *db.PlanNode) {
		titles = append(titles, n.Title)
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(plan)
	if !strings.Contains(strings.Join(titles, "|"), "orders_user_idx") {
		t.Errorf("titles = %q", titles)
	}
	// SHOWPLAN is off again: statements run
	mustUsable(t, sess)
	// several statements
	plan, err = sess.Explain(context.Background(), "SELECT 1 FROM users; SELECT 2 FROM products", false)
	if err != nil || plan.Title != "Batch" || len(plan.Children) != 2 {
		t.Fatalf("batch plan = %+v, %v", plan, err)
	}
}

func (s *suite) testExplainAnalyze(t *testing.T) {
	sess := s.session(t, nil)
	plan, err := sess.Explain(context.Background(), "SELECT COUNT(*) FROM orders WHERE status = 'paid'", true)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Rows != 1 || len(plan.Children) != 1 {
		t.Fatalf("plan = %+v", plan)
	}
	found := false
	var walk func(*db.PlanNode)
	walk = func(n *db.PlanNode) {
		for _, kv := range n.Details {
			if kv.Key == "Actual executions" {
				found = true
			}
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(plan)
	if !found {
		t.Errorf("no actual counters: %+v", plan)
	}
	mustUsable(t, sess)
}

func (s *suite) testBulkInsert(t *testing.T) {
	sess := s.session(t, nil)
	all(t, sess, "CREATE TABLE #bulk (id int, v nvarchar(20))")
	var rows [][]db.Cell
	for i := range 1234 {
		v := db.Text("v" + strconv.Itoa(i))
		if i%100 == 0 {
			v = db.NullCell
		}
		rows = append(rows, []db.Cell{db.Text(strconv.Itoa(i)), v})
	}
	n, err := sess.BulkInsert(context.Background(), db.ObjectRef{Name: "#bulk"}, []string{"id", "v"}, rows)
	if err != nil || n != 1234 {
		t.Fatalf("bulk: %d %v", n, err)
	}
	if _, r, _ := all(t, sess, "SELECT COUNT(*), COUNT(v) FROM #bulk"); r[0][0].S != "1234" || r[0][1].S != "1221" {
		t.Fatalf("counts = %v", r)
	}
}

func (s *suite) testAdmin(t *testing.T) {
	ctx := context.Background()
	a := s.c.Admin()
	other := s.session(t, nil)
	procs, err := a.Processes(ctx)
	// without VIEW SERVER STATE a login sees only its own session, which
	// the list leaves out
	if err != nil || procs.Columns[0] != "id" {
		t.Fatalf("processes = %+v, %v", procs, err)
	}
	vars, err := a.Variables(ctx)
	if err != nil || len(vars.Rows) < 10 {
		t.Fatalf("variables: %v", err)
	}
	users, err := a.Users(ctx)
	if err != nil || len(users.Rows) == 0 {
		t.Fatalf("users = %+v, %v", users, err)
	}
	grants, err := a.Grants(ctx, "squi")
	if err != nil || len(grants.Rows) == 0 {
		t.Fatalf("grants = %+v, %v", grants, err)
	}
	sizes, err := a.Sizes(ctx)
	if err != nil || len(sizes.Rows) == 0 {
		t.Fatalf("sizes = %+v, %v", sizes, err)
	}
	if err := a.CancelQuery(ctx, "1"); err == nil {
		t.Error("cancel of another session should be refused")
	}
	if err := a.Kill(ctx, "1; DROP TABLE x"); err == nil {
		t.Error("bad id accepted")
	}
	// kill the other session (a login may kill its own sessions? needs ALTER ANY CONNECTION)
	spid := other.(*session).spid
	if err := a.Kill(ctx, strconv.FormatInt(spid, 10)); err != nil {
		var qe *QueryError
		if errors.As(err, &qe) && qe.Number == 6102 { // no permission: fine for the squi login
			return
		}
		t.Fatal(err)
	}
}

func (s *suite) testDial(t *testing.T) {
	var dials atomic.Int32
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		dials.Add(1)
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	cfg := s.cfg
	cfg.Host = "localhost" // resolved by the dialer, not the driver
	c := s.open(cfg, dial)
	sess := s.session(t, c)
	mustUsable(t, sess)
	if n := dials.Load(); n < 2 {
		t.Fatalf("dials = %d, want >= 2", n)
	}
}

func (s *suite) testEstimateRows(t *testing.T) {
	est := s.c.(db.RowEstimator)
	ctx := context.Background()
	n, exact, ok, err := est.EstimateRows(ctx, db.ObjectRef{Database: "squi", Schema: "dbo", Name: "orders"})
	if err != nil || !ok || exact || n != 20000 {
		t.Fatalf("orders: n=%d ok=%v err=%v", n, ok, err)
	}
	if _, _, ok, err := est.EstimateRows(ctx, db.ObjectRef{Database: "squi", Schema: "dbo", Name: "paid_orders"}); err != nil || ok {
		t.Fatalf("view: ok=%v err=%v", ok, err)
	}
	if _, _, ok, err := est.EstimateRows(ctx, db.ObjectRef{Database: "squi", Schema: "dbo", Name: "no_such_table"}); err != nil || ok {
		t.Fatalf("missing: ok=%v err=%v", ok, err)
	}
}

func (s *suite) testBadLogin(t *testing.T) {
	drv, _ := db.Get(db.SQLServer)
	cfg := s.cfg
	cfg.Password = "wrong"
	if _, err := drv.Open(context.Background(), cfg, nil); !IsAuthError(err) {
		t.Errorf("bad password: %v", err)
	}
	cfg = s.cfg
	cfg.Database = "no_such_db"
	if _, err := drv.Open(context.Background(), cfg, nil); !IsUnknownDatabase(err) {
		t.Errorf("bad database: %v", err)
	}
}

func TestKeywordsAndWords(t *testing.T) {
	if got := keywords("  /* x */ -- y\n(begin tran", 2); !slices.Equal(got, []string{"BEGIN", "TRAN"}) {
		t.Errorf("keywords = %q", got)
	}
	for q, want := range map[string]bool{
		"INSERT INTO t OUTPUT inserted.id VALUES (1)": true,
		"INSERT INTO [output] VALUES (1)":             false,
		"INSERT INTO t VALUES ('output')":             false,
		"INSERT INTO t VALUES (1) -- output":          false,
		"SELECT @output":                              false,
	} {
		if hasWord(q, "OUTPUT") != want {
			t.Errorf("hasWord(%q) != %v", q, want)
		}
	}
}

func TestTypeName(t *testing.T) {
	cases := []struct {
		name               string
		maxLen, prec, scal int
		want               string
	}{
		{"nvarchar", 100, 0, 0, "nvarchar(50)"},
		{"varchar", -1, 0, 0, "varchar(max)"},
		{"decimal", 9, 10, 2, "decimal(10,2)"},
		{"datetime2", 8, 27, 7, "datetime2"},
		{"datetime2", 7, 23, 3, "datetime2(3)"},
		{"int", 4, 10, 0, "int"},
	}
	for _, c := range cases {
		if got := typeName(c.name, c.maxLen, c.prec, c.scal, false); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestParsePlan(t *testing.T) {
	doc := `<?xml version="1.0" encoding="utf-16"?><ShowPlanXML xmlns="http://schemas.microsoft.com/sqlserver/2004/07/showplan"><BatchSequence><Batch><Statements>
<StmtSimple StatementText="SELECT * FROM t WHERE a = 1" StatementType="SELECT" StatementSubTreeCost="0.5" StatementEstRows="3">
<QueryPlan DegreeOfParallelism="1"><MissingIndexes><MissingIndexGroup Impact="90.5"><MissingIndex Database="[d]" Schema="[dbo]" Table="[t]"><ColumnGroup Usage="EQUALITY"><Column Name="[a]"/></ColumnGroup><ColumnGroup Usage="INCLUDE"><Column Name="[b]"/></ColumnGroup></MissingIndex></MissingIndexGroup></MissingIndexes>
<RelOp NodeId="0" PhysicalOp="Nested Loops" LogicalOp="Inner Join" EstimateRows="3" EstimatedTotalSubtreeCost="0.5"><NestedLoops>
 <RelOp NodeId="1" PhysicalOp="Index Seek" LogicalOp="Index Seek" EstimateRows="3" EstimatedTotalSubtreeCost="0.1"><IndexScan><Object Database="[d]" Schema="[dbo]" Table="[t]" Index="[ix_a]"/><SeekPredicates><SeekPredicateNew><SeekKeys><Prefix ScanType="EQ"><RangeColumns><ColumnReference Column="a"/></RangeColumns><RangeExpressions><ScalarOperator ScalarString="(1)"/></RangeExpressions></Prefix></SeekKeys></SeekPredicateNew></SeekPredicates></IndexScan></RelOp>
 <RelOp NodeId="2" PhysicalOp="Key Lookup" LogicalOp="Key Lookup" EstimateRows="1" EstimateExecutions="3" EstimatedTotalSubtreeCost="0.3"><Warnings><PlanAffectingConvert ConvertIssue="Seek Plan" Expression="CONVERT_IMPLICIT(nvarchar(10),[a],0)"/></Warnings><IndexScan Lookup="1"><Object Table="[t]" Index="[pk]"/></IndexScan></RelOp>
</NestedLoops></RelOp></QueryPlan></StmtSimple></Statements></Batch></BatchSequence></ShowPlanXML>`
	n, err := parsePlan(doc, false)
	if err != nil {
		t.Fatal(err)
	}
	if n.Title != "SELECT" || n.Cost != 0.5 || len(n.Children) != 1 {
		t.Fatalf("root = %+v", n)
	}
	var mi string
	for _, kv := range n.Details {
		if kv.Key == "Missing index" {
			mi = kv.Value
		}
	}
	if mi != "CREATE INDEX ON [dbo].[t] ([a]) INCLUDE ([b]) -- impact 90.5%" {
		t.Errorf("missing index = %q", mi)
	}
	nl := n.Children[0]
	if nl.Title != "Nested Loops (Inner Join)" || len(nl.Children) != 2 {
		t.Fatalf("join = %+v", nl)
	}
	seek, lookup := nl.Children[0], nl.Children[1]
	if seek.Title != "Index Seek on dbo.t [ix_a]" || seek.Details[0] != (db.KV{Key: "Seek", Value: "a, (1)"}) {
		t.Errorf("seek = %+v", seek)
	}
	if lookup.Title != "Key Lookup on t [pk]" || !strings.Contains(detail(lookup, "Warnings"), "CONVERT_IMPLICIT") ||
		detail(lookup, "Estimated executions") != "3" {
		t.Errorf("lookup = %+v", lookup)
	}
	if raw := indentXML(doc); !strings.Contains(raw, "\n  <BatchSequence>") || strings.Contains(raw, "xmlns") {
		t.Errorf("raw = %s", raw[:min(len(raw), 300)])
	}
}

func detail(n *db.PlanNode, k string) string {
	for _, kv := range n.Details {
		if kv.Key == k {
			return kv.Value
		}
	}
	return ""
}
