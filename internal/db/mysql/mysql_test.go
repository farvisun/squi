package mysql

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/farvisun/squi/internal/db"
)

// Integration tests run against the dev containers when configured:
//
//	SQUI_TEST_MYSQL=squi:squi@127.0.0.1:33069/squi
//	SQUI_TEST_MARIADB=squi:squi@127.0.0.1:33079/squi
func TestMySQL(t *testing.T)   { runSuite(t, "SQUI_TEST_MYSQL") }
func TestMariaDB(t *testing.T) { runSuite(t, "SQUI_TEST_MARIADB") }

// parseTestDSN parses user:password@host:port/database.
func parseTestDSN(t *testing.T, dsn string) db.ConnConfig {
	t.Helper()
	at := strings.LastIndex(dsn, "@")
	slash := strings.LastIndex(dsn, "/")
	if at < 0 || slash < at {
		t.Fatalf("bad DSN %q, want user:password@host:port/database", dsn)
	}
	user, pass, _ := strings.Cut(dsn[:at], ":")
	host, portStr, err := net.SplitHostPort(dsn[at+1 : slash])
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portStr)
	return db.ConnConfig{Host: host, Port: port, User: user, Password: pass, Database: dsn[slash+1:]}
}

type suite struct {
	t   *testing.T
	cfg db.ConnConfig
	c   db.Conn
}

func runSuite(t *testing.T, env string) {
	dsn := os.Getenv(env)
	if dsn == "" {
		t.Skipf("%s not set", env)
	}
	s := &suite{t: t, cfg: parseTestDSN(t, dsn)}
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
	t.Run("NullsAndTags", s.testNullsAndTags)
	t.Run("ErrorLine", s.testErrorLine)
	t.Run("ReadOnly", s.testReadOnly)
	t.Run("Transactions", s.testTransactions)
	t.Run("Cancel", s.testCancel)
	t.Run("ContextCancel", s.testContextCancel)
	t.Run("UseDatabase", s.testUseDatabase)
	t.Run("Call", s.testCall)
	t.Run("Explain", s.testExplain)
	t.Run("BulkInsert", s.testBulkInsert)
	t.Run("Admin", s.testAdmin)
	t.Run("Dial", s.testDial)
}

func (s *suite) open(cfg db.ConnConfig, dial db.DialFunc) db.Conn {
	s.t.Helper()
	drv, ok := db.Get(db.MySQL)
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

func (s *suite) mariadb() bool { return s.c.(*conn).mariadb }

// scalar reads a single value through the metadata pool.
func (s *suite) scalar(q string, dest any) {
	s.t.Helper()
	if err := s.c.(*conn).meta.QueryRow(q).Scan(dest); err != nil {
		s.t.Fatal(err)
	}
}

// all runs q on sess and returns every row.
func all(t *testing.T, sess db.Session, q string) ([]db.ResultColumn, [][]db.Cell) {
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
			return st.Columns(), rows
		}
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func exec(t *testing.T, sess db.Session, q string) db.ResultStream {
	t.Helper()
	st, err := sess.Run(context.Background(), q)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	st.Close()
	return st
}

func mustUsable(t *testing.T, sess db.Session) {
	t.Helper()
	if _, rows := all(t, sess, "SELECT 42"); len(rows) != 1 || rows[0][0].S != "42" {
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
	iSqui, iWh, iIS := slices.Index(names, "squi"), slices.Index(names, "warehouse"), slices.Index(names, "information_schema")
	if iSqui < 0 || iWh < 0 || iIS < 0 || iIS < iSqui || iIS < iWh {
		t.Fatalf("databases = %v", names)
	}
	if dbs[iSqui].Size == "" || !strings.HasSuffix(dbs[iSqui].Size, "B") {
		t.Errorf("size = %q", dbs[iSqui].Size)
	}
	if sc, _ := s.c.Schemas(context.Background(), "squi"); !slices.Equal(sc, []string{"squi"}) {
		t.Errorf("schemas = %v", sc)
	}
}

func (s *suite) testObjects(t *testing.T) {
	objs, err := s.c.Objects(context.Background(), "squi", "squi")
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
		"users": db.KindTable, "orders": db.KindTable, "order_items": db.KindTable,
		"paid_orders": db.KindView, "user_orders": db.KindProcedure, "order_count": db.KindFunction,
	}
	for name, k := range want {
		if kinds[name] != k {
			t.Errorf("%s kind = %q, want %q", name, kinds[name], k)
		}
	}
	if comments["users"] != "Registered customers" {
		t.Errorf("users comment = %q", comments["users"])
	}
	if !slices.IsSortedFunc(objs, func(a, b db.Object) int { return kindRank[a.Kind] - kindRank[b.Kind] }) {
		t.Error("objects not sorted by kind")
	}
}

func (s *suite) testAllColumns(t *testing.T) {
	cols, err := s.c.AllColumns(context.Background(), "squi", "squi")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"order_id", "product_id", "qty", "unit_price"}
	if !slices.Equal(cols["order_items"], want) {
		t.Errorf("order_items = %v", cols["order_items"])
	}
	if len(cols["paid_orders"]) == 0 {
		t.Error("view columns missing")
	}
}

func (s *suite) detail(t *testing.T, name string) *db.TableDetail {
	t.Helper()
	td, err := s.c.TableDetail(context.Background(), db.ObjectRef{Database: "squi", Name: name, Kind: db.KindTable})
	if err != nil {
		t.Fatal(err)
	}
	return td
}

func (s *suite) testTableDetail(t *testing.T) {
	orders := s.detail(t, "orders")
	if pk := orders.PrimaryKey(); !slices.Equal(pk, []string{"id"}) {
		t.Errorf("orders pk = %v", pk)
	}
	col := func(td *db.TableDetail, name string) db.Column {
		for _, c := range td.Columns {
			if c.Name == name {
				return c
			}
		}
		t.Fatalf("column %s missing", name)
		return db.Column{}
	}
	if c := col(orders, "status"); c.Default == nil || *c.Default != "pending" || !strings.HasPrefix(c.Type, "enum(") {
		t.Errorf("status = %+v", c)
	}
	if c := col(orders, "note"); c.Default != nil || !c.Nullable {
		t.Errorf("note default = %v nullable %v", c.Default, c.Nullable)
	}
	if c := col(orders, "id"); c.Extra != "auto_increment" {
		t.Errorf("id extra = %q", c.Extra)
	}
	if c := col(orders, "updated_at"); !strings.Contains(strings.ToLower(c.Extra), "on update") {
		t.Errorf("updated_at extra = %q", c.Extra)
	}
	if len(orders.ForeignKeys) != 1 || orders.ForeignKeys[0].Name != "orders_user_fk" ||
		orders.ForeignKeys[0].RefTable != "users" || orders.ForeignKeys[0].OnDelete != "CASCADE" {
		t.Errorf("orders fks = %+v", orders.ForeignKeys)
	}
	if len(orders.ReferencedBy) != 1 || orders.ReferencedBy[0].Table != "order_items" ||
		!slices.Equal(orders.ReferencedBy[0].Columns, []string{"order_id"}) {
		t.Errorf("orders referenced by = %+v", orders.ReferencedBy)
	}
	idx := slices.IndexFunc(orders.Indexes, func(ix db.Index) bool { return ix.Name == "orders_status_created_idx" })
	if idx < 0 || !slices.Equal(orders.Indexes[idx].Columns, []string{"status", "created_at"}) || orders.Indexes[idx].Method != "btree" {
		t.Errorf("indexes = %+v", orders.Indexes)
	}
	if !orders.Indexes[0].Primary {
		t.Errorf("primary index not first: %+v", orders.Indexes[0])
	}
	if len(orders.Triggers) != 1 || orders.Triggers[0].Name != "orders_audit" ||
		orders.Triggers[0].Timing != "AFTER" || orders.Triggers[0].Event != "UPDATE" {
		t.Errorf("triggers = %+v", orders.Triggers)
	}
	if !slices.ContainsFunc(orders.Stats, func(kv db.KV) bool { return kv.Key == "Engine" && kv.Value == "InnoDB" }) {
		t.Errorf("stats = %+v", orders.Stats)
	}

	items := s.detail(t, "order_items")
	if pk := items.PrimaryKey(); !slices.Equal(pk, []string{"order_id", "product_id"}) {
		t.Errorf("order_items pk = %v", pk)
	}
	if len(items.ForeignKeys) != 2 {
		t.Errorf("order_items fks = %+v", items.ForeignKeys)
	}

	products := s.detail(t, "products")
	hasCons := func(typ, name, def string) bool {
		return slices.ContainsFunc(products.Constraints, func(c db.Constraint) bool {
			return c.Type == typ && c.Name == name && strings.Contains(c.Definition, def)
		})
	}
	if !hasCons("CHECK", "price_positive", "`price` >= 0") || !hasCons("UNIQUE", "sku", "`sku`") {
		t.Errorf("products constraints = %+v", products.Constraints)
	}

	users := s.detail(t, "users")
	if users.Comment != "Registered customers" || col(users, "email").Comment != "Login email, unique" {
		t.Errorf("users comments = %q / %q", users.Comment, col(users, "email").Comment)
	}

	view := s.detail(t, "paid_orders")
	if len(view.Columns) != 8 || view.Comment != "" || view.Stats[0].Value != "VIEW" {
		t.Errorf("view = %+v", view)
	}

	if _, err := s.c.TableDetail(context.Background(), db.ObjectRef{Schema: "squi", Name: "nope"}); err == nil {
		t.Error("missing table: no error")
	}
}

func (s *suite) testDDL(t *testing.T) {
	ctx := context.Background()
	ddl, err := s.c.DDL(ctx, db.ObjectRef{Database: "squi", Name: "orders", Kind: db.KindTable})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"CREATE TABLE `orders`", "orders_user_fk", "CREATE DEFINER", "TRIGGER", "orders_audit"} {
		if !strings.Contains(ddl, want) {
			t.Errorf("orders DDL lacks %q:\n%s", want, ddl)
		}
	}
	ddl, err = s.c.DDL(ctx, db.ObjectRef{Database: "squi", Name: "paid_orders", Kind: db.KindView})
	if err != nil || !strings.Contains(ddl, "VIEW `paid_orders`") {
		t.Errorf("view DDL = %q, %v", ddl, err)
	}

	sess := s.session(t, nil)
	exec(t, sess, "DROP PROCEDURE IF EXISTS squi_test_proc")
	exec(t, sess, "CREATE PROCEDURE squi_test_proc() SELECT 1")
	defer exec(t, sess, "DROP PROCEDURE squi_test_proc")
	ddl, err = s.c.DDL(ctx, db.ObjectRef{Schema: "squi", Name: "squi_test_proc", Kind: db.KindProcedure})
	if err != nil || !strings.Contains(ddl, "PROCEDURE `squi_test_proc`") {
		t.Errorf("procedure DDL = %q, %v", ddl, err)
	}
	// Routines defined by root are only visible to privileged users on MySQL.
	ddl, err = s.c.DDL(ctx, db.ObjectRef{Schema: "squi", Name: "order_count", Kind: db.KindFunction})
	if err != nil && !strings.Contains(err.Error(), "privileges") || err == nil && !strings.Contains(ddl, "FUNCTION `order_count`") {
		t.Errorf("function DDL = %q, %v", ddl, err)
	}
}

func (s *suite) testStreaming(t *testing.T) {
	sess := s.session(t, nil)
	st, err := sess.Run(context.Background(), "SELECT * FROM orders ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if cols := st.Columns(); len(cols) != 7 || cols[0].Name != "id" || cols[0].Type != "bigint" {
		t.Fatalf("columns = %+v", cols)
	}
	total, batches := 0, 0
	for {
		rows, err := st.Next(1000)
		total += len(rows)
		if len(rows) > 0 {
			batches++
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1000 {
			t.Fatalf("short batch %d before EOF", len(rows))
		}
		if batches == 1 && (rows[0][0].S != "1" || !rows[0][4].Null || rows[9][4].S != "gift wrap") {
			t.Fatalf("first rows = %v / %v", rows[0], rows[9])
		}
	}
	if total != 20000 || st.CommandTag() != "SELECT 20000" || st.RowsAffected() != 20000 {
		t.Fatalf("total %d tag %q", total, st.CommandTag())
	}
	if rows, err := st.Next(10); err != io.EOF || len(rows) != 0 {
		t.Fatalf("after EOF: %v %v", rows, err)
	}
}

func (s *suite) testCloseMidStream(t *testing.T) {
	sess := s.session(t, nil)
	// 400M rows: draining instead of killing would take minutes.
	st, err := sess.Run(context.Background(), "SELECT o.id, o2.note FROM orders o CROSS JOIN orders o2")
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := st.Next(500); err != nil || len(rows) != 500 {
		t.Fatalf("next: %d %v", len(rows), err)
	}
	start := time.Now()
	st.Close()
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("close took %v", d)
	}
	mustUsable(t, sess)

	// Closing an unread but already finished result kills an idle
	// connection; the next statement must not be affected.
	st, err = sess.Run(context.Background(), "SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	st.Close()
	mustUsable(t, sess)
}

func (s *suite) testNullsAndTags(t *testing.T) {
	sess := s.session(t, nil)
	_, rows := all(t, sess, "SELECT '', NULL, 'x', CAST(1.50 AS DECIMAL(5,2)), CAST('2024-01-02 03:04:05' AS DATETIME)")
	want := []db.Cell{db.Text(""), db.NullCell, db.Text("x"), db.Text("1.50"), db.Text("2024-01-02 03:04:05")}
	if len(rows) != 1 || !slices.Equal(rows[0], want) {
		t.Fatalf("rows = %#v", rows)
	}
	st := exec(t, sess, "UPDATE orders SET note = note WHERE id <= 3")
	if st.CommandTag() != "UPDATE 0" || len(st.Columns()) != 0 {
		t.Errorf("tag = %q", st.CommandTag())
	}
	if st := exec(t, sess, "/* c */ SET @x = 1"); st.CommandTag() != "OK" {
		t.Errorf("tag = %q", st.CommandTag())
	}
	if cols, rows := all(t, sess, "  -- comment\n(SELECT @x AS x)"); cols[0].Name != "x" || rows[0][0].S != "1" {
		t.Errorf("paren select = %v %v", cols, rows)
	}
}

func (s *suite) testErrorLine(t *testing.T) {
	sess := s.session(t, nil)
	_, err := sess.Run(context.Background(), "SELECT 1\nFROM WHERE x")
	var qe *QueryError
	if !errors.As(err, &qe) || qe.Number != 1064 || qe.Line != 2 || qe.SQLState != "42000" {
		t.Fatalf("err = %#v", err)
	}
	_, err = sess.Run(context.Background(), "SELECT * FROM no_such_table")
	if !errors.As(err, &qe) || qe.Number != 1146 || qe.Line != 0 {
		t.Fatalf("err = %#v", err)
	}
	mustUsable(t, sess)
}

func (s *suite) testReadOnly(t *testing.T) {
	cfg := s.cfg
	cfg.ReadOnly = true
	sess := s.session(t, s.open(cfg, nil))
	if _, err := sess.Run(context.Background(), "INSERT INTO audit_log (message) VALUES ('ro')"); err == nil {
		t.Fatal("insert succeeded in read-only session")
	}
	mustUsable(t, sess)
}

func (s *suite) testTransactions(t *testing.T) {
	ctx := context.Background()
	sess := s.session(t, nil)
	check := func(want bool, after string) {
		t.Helper()
		if sess.InTx() != want {
			t.Fatalf("InTx = %v after %s", !want, after)
		}
	}
	check(false, "open")
	if err := sess.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	check(true, "Begin")
	exec(t, sess, "INSERT INTO audit_log (message) VALUES ('tx test')")
	if err := sess.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	check(false, "Rollback")
	var n int
	s.scalar("SELECT COUNT(*) FROM audit_log WHERE message = 'tx test'", &n)
	if n != 0 {
		t.Fatal("rollback did not undo insert")
	}

	exec(t, sess, "start transaction")
	check(true, "START TRANSACTION")
	exec(t, sess, "COMMIT")
	check(false, "COMMIT")
	exec(t, sess, "BEGIN")
	exec(t, sess, "SAVEPOINT a")
	exec(t, sess, "ROLLBACK TO SAVEPOINT a")
	check(true, "ROLLBACK TO SAVEPOINT")
	exec(t, sess, "CREATE TEMPORARY TABLE squi_tmp (x INT)")
	check(true, "CREATE TEMPORARY TABLE")
	exec(t, sess, "CREATE TABLE squi_tx_test (x INT)")
	check(false, "CREATE TABLE (implicit commit)")
	exec(t, sess, "DROP TABLE squi_tx_test")
	if err := sess.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	if err := sess.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	check(false, "Commit")
}

func (s *suite) testCancel(t *testing.T) {
	sess := s.session(t, nil)
	go func() {
		time.Sleep(300 * time.Millisecond)
		if err := sess.Cancel(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	start := time.Now()
	st, err := sess.Run(context.Background(), "SELECT SLEEP(10)")
	if err == nil {
		_, err = st.Next(10)
		st.Close()
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("cancel took %v (err %v)", d, err)
	}
	mustUsable(t, sess)
}

func (s *suite) testContextCancel(t *testing.T) {
	sess := s.session(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := sess.Run(ctx, "DO SLEEP(10)")
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("context cancel took %v (err %v)", d, err)
	}
	exec(t, sess, "SET @kept = 7") // the connection (and its state) survived
	if _, rows := all(t, sess, "SELECT @kept"); rows[0][0].S != "7" {
		t.Fatalf("state lost: %v", rows)
	}
}

func (s *suite) testUseDatabase(t *testing.T) {
	sess := s.session(t, nil)
	if sess.CurrentDatabase() != "squi" {
		t.Fatalf("current = %q", sess.CurrentDatabase())
	}
	if err := sess.UseDatabase(context.Background(), "warehouse"); err != nil {
		t.Fatal(err)
	}
	if sess.CurrentDatabase() != "warehouse" {
		t.Fatalf("current = %q", sess.CurrentDatabase())
	}
	all(t, sess, "SELECT * FROM stock")
	exec(t, sess, "USE `squi`")
	if sess.CurrentDatabase() != "squi" {
		t.Fatalf("current after USE = %q", sess.CurrentDatabase())
	}
}

func (s *suite) testCall(t *testing.T) {
	sess := s.session(t, nil)
	cols, rows := all(t, sess, "CALL user_orders(1)")
	if len(cols) != 7 || len(rows) != 40 {
		t.Fatalf("call: %d cols %d rows", len(cols), len(rows))
	}
	// Abandon a CALL result early as well.
	st, err := sess.Run(context.Background(), "CALL user_orders(2)")
	if err != nil {
		t.Fatal(err)
	}
	st.Next(1)
	st.Close()
	mustUsable(t, sess)
}

func walk(n *db.PlanNode, f func(*db.PlanNode)) {
	f(n)
	for _, c := range n.Children {
		walk(c, f)
	}
}

func (s *suite) testExplain(t *testing.T) {
	ctx := context.Background()
	sess := s.session(t, nil)
	q := "SELECT o.id, u.email FROM orders o JOIN users u ON u.id = o.user_id WHERE o.status = 'paid'"
	plan, err := sess.Explain(ctx, q, false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Raw == "" {
		t.Error("raw plan missing")
	}
	var titles []string
	walk(plan, func(n *db.PlanNode) { titles = append(titles, n.Title) })
	if len(titles) < 3 || !strings.Contains(strings.Join(titles, "|"), "o") {
		t.Errorf("plan titles = %q", titles)
	}

	plan, err = sess.Explain(ctx, q+";", true)
	if err != nil {
		t.Fatal(err)
	}
	var timed bool
	walk(plan, func(n *db.PlanNode) { timed = timed || n.Time > 0 })
	if !timed {
		t.Errorf("analyze has no timings: %s", plan.Raw)
	}
	mustUsable(t, sess)
}

func (s *suite) testBulkInsert(t *testing.T) {
	ctx := context.Background()
	sess := s.session(t, nil)
	exec(t, sess, "DROP TABLE IF EXISTS squi_bulk_test")
	exec(t, sess, "CREATE TABLE squi_bulk_test (id INT PRIMARY KEY, s TEXT NULL, n INT NULL)")
	defer exec(t, sess, "DROP TABLE squi_bulk_test")

	tricky := []string{"it's", `back\slash`, "line\nbreak\r\n", "nul\x00byte", "ctrl-z\x1a", "", `\'; DROP TABLE x; --`, "ünïcødé 🚀"}
	var rows [][]db.Cell
	for i := range 1200 {
		s := db.Text(tricky[i%len(tricky)])
		if i%7 == 3 {
			s = db.NullCell
		}
		n := db.Text(strconv.Itoa(i * 3))
		if i%5 == 0 {
			n = db.NullCell
		}
		rows = append(rows, []db.Cell{db.Text(strconv.Itoa(i)), s, n})
	}
	ref := db.ObjectRef{Database: "squi", Name: "squi_bulk_test", Kind: db.KindTable}
	n, err := sess.BulkInsert(ctx, ref, []string{"id", "s", "n"}, rows)
	if err != nil || n != int64(len(rows)) {
		t.Fatalf("inserted %d, %v", n, err)
	}
	_, got := all(t, sess, "SELECT id, s, n FROM squi_bulk_test ORDER BY id")
	if len(got) != len(rows) {
		t.Fatalf("read back %d rows", len(got))
	}
	for i := range rows {
		if !slices.Equal(got[i], rows[i]) {
			t.Fatalf("row %d = %#v, want %#v", i, got[i], rows[i])
		}
	}
}

func (s *suite) testAdmin(t *testing.T) {
	ctx := context.Background()
	a := s.c.Admin()
	sess := s.session(t, nil)
	sid := strconv.FormatUint(sess.(*session).id, 10)

	procs, err := a.Processes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if procs.Columns[0] != "ID" || !slices.ContainsFunc(procs.Rows, func(r []db.Cell) bool { return r[0].S == sid }) {
		t.Errorf("processes lack session %s: %v", sid, procs.Rows)
	}

	vars, err := a.Variables(ctx)
	if err != nil || !slices.ContainsFunc(vars.Rows, func(r []db.Cell) bool { return r[0].S == "version" }) {
		t.Errorf("variables: %v", err)
	}
	users, err := a.Users(ctx)
	if err != nil || len(users.Rows) == 0 || users.Rows[0][0].S == "" {
		t.Errorf("users = %v, %v", users, err)
	}
	for _, u := range []string{"squi", "squi@%", "'squi'@'%'", "`squi`@`%`"} {
		g, err := a.Grants(ctx, u)
		if err != nil || len(g.Rows) == 0 || !strings.HasPrefix(g.Rows[0][0].S, "GRANT") {
			t.Errorf("grants %s = %v, %v", u, g, err)
		}
	}
	sizes, err := a.Sizes(ctx)
	if err != nil || !slices.ContainsFunc(sizes.Rows, func(r []db.Cell) bool { return r[0].S == "table" && r[2].S == "orders" }) {
		t.Errorf("sizes = %v, %v", sizes, err)
	}

	go func() {
		time.Sleep(300 * time.Millisecond)
		if err := a.CancelQuery(ctx, sid); err != nil {
			t.Error(err)
		}
	}()
	start := time.Now()
	exec(t, sess, "DO SLEEP(10)")
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("CancelQuery took %v", d)
	}
	mustUsable(t, sess)

	if err := a.Kill(ctx, sid); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Run(ctx, "SELECT 1"); err == nil {
		t.Error("killed session still works")
	}
	if err := a.Kill(ctx, "1; DROP TABLE x"); err == nil {
		t.Error("invalid id accepted")
	}
}

func (s *suite) testDial(t *testing.T) {
	var dials atomic.Int32
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		dials.Add(1)
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	c := s.open(s.cfg, dial)
	sess := s.session(t, c)
	mustUsable(t, sess)
	if n := dials.Load(); n < 2 {
		t.Fatalf("dials = %d, want >= 2", n)
	}
}

func TestKeywords(t *testing.T) {
	cases := map[string][]string{
		"select 1":                  {"SELECT", ""},
		"  /* x */ -- y\n# z\n(sel": {"SEL", ""},
		"ROLLBACK TO SAVEPOINT a":   {"ROLLBACK", "TO"},
		"--\nupdate t":              {"UPDATE", "T"},
		"create temporary table t":  {"CREATE", "TEMPORARY"},
		"":                          {"", ""},
	}
	for q, want := range cases {
		got := append(keywords(q, 2), "", "")[:2]
		if !slices.Equal(got, want) {
			t.Errorf("keywords(%q) = %q, want %q", q, got, want)
		}
	}
}

func TestParseTree(t *testing.T) {
	raw := "-> Limit: 5 row(s)  (cost=2.2 rows=1) (actual time=10.7..10.7 rows=5 loops=1)\n" +
		"    -> Nested loop inner join  (cost=0.25..2.2 rows=1) (actual time=10.7..10.7 rows=5 loops=1)\n" +
		"        -> Table scan on o  (cost=1.1 rows=1) (actual time=0.834..8.42 rows=20000 loops=1)\n" +
		"        -> Single-row index lookup on u using PRIMARY (id=o.user_id)  (cost=1.1 rows=1) (actual time=684e-6..716e-6 rows=1 loops=5)\n"
	root := parseTree(raw)
	if root.Title != "Limit: 5 row(s)" || root.Cost != 2.2 || root.Rows != 5 || len(root.Children) != 1 {
		t.Fatalf("root = %+v", root)
	}
	join := root.Children[0]
	if len(join.Children) != 2 || join.Cost != 2.2 {
		t.Fatalf("join = %+v", join)
	}
	lookup := join.Children[1]
	if lookup.Title != "Single-row index lookup on u using PRIMARY (id=o.user_id)" || lookup.Time < 0.00357 || lookup.Time > 0.00359 {
		t.Fatalf("lookup = %+v", lookup)
	}
}

func TestParseJSONPlan(t *testing.T) {
	raw := `{"query_block": {"select_id": 1, "cost": 0.1, "nested_loop": [
		{"table": {"table_name": "u", "access_type": "ALL", "rows": 25, "possible_keys": ["PRIMARY"]}},
		{"table": {"table_name": "o", "access_type": "ref", "key": "fk", "rows": 3, "cost_info": {"prefix_cost": "1.5"}}}]}}`
	root, err := parseJSONPlan(raw, false)
	if err != nil {
		t.Fatal(err)
	}
	if root.Title != "query block #1" || root.Cost != 0.1 || len(root.Children) != 1 {
		t.Fatalf("root = %+v", root)
	}
	loop := root.Children[0]
	if loop.Title != "nested loop" || len(loop.Children) != 2 {
		t.Fatalf("loop = %+v", loop)
	}
	o := loop.Children[1]
	if o.Title != "o: ref" || o.Rows != 3 || o.Cost != 1.5 {
		t.Fatalf("o = %+v", o)
	}
	if u := loop.Children[0]; !slices.Contains(u.Details, db.KV{Key: "possible_keys", Value: "PRIMARY"}) {
		t.Fatalf("u details = %v", u.Details)
	}
}

func TestSplitAccount(t *testing.T) {
	cases := map[string][2]string{
		"squi":             {"squi", "%"},
		"squi@localhost":   {"squi", "localhost"},
		"'a@b'@'10.0.0.%'": {"a@b", "10.0.0.%"},
		"`it``s`@`%`":      {"it`s", "%"},
	}
	for in, want := range cases {
		if u, h := splitAccount(in); u != want[0] || h != want[1] {
			t.Errorf("splitAccount(%q) = %q, %q", in, u, h)
		}
	}
}

func TestHumanSize(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1536: "1.5 KB", 12897484: "12.3 MB"} {
		if got := humanSize(n); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", n, got, want)
		}
	}
}
