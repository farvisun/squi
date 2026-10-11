package sqlite

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/farvisun/squi/internal/db"
	"github.com/farvisun/squi/internal/sqltext"
)

// SQLite needs no server: every test builds a database from the
// playground seed in a temporary directory.

func seeded(t *testing.T) (db.Conn, db.ConnConfig) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "squi.db")
	seed, err := os.ReadFile(filepath.Join("..", "..", "..", "dev", "seed", "sqlite.sql"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := db.ConnConfig{Database: path, Params: map[string]string{"mode": "rwc"}}
	c := open(t, cfg)
	sess := session1(t, c)
	for _, st := range sqltext.Split(string(seed), db.SQLite) {
		rs, err := sess.Run(context.Background(), st.SQL)
		if err != nil {
			t.Fatalf("seed: %s: %v", st.SQL, err)
		}
		rs.Close()
	}
	cfg.Params = nil
	return c, cfg
}

func open(t *testing.T, cfg db.ConnConfig) db.Conn {
	t.Helper()
	drv, ok := db.Get(db.SQLite)
	if !ok {
		t.Fatal("driver not registered")
	}
	c, err := drv.Open(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func session1(t *testing.T, c db.Conn) db.Session {
	t.Helper()
	s, err := c.NewSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func all(t *testing.T, s db.Session, q string) ([]db.ResultColumn, [][]db.Cell, db.ResultStream) {
	t.Helper()
	st, err := s.Run(context.Background(), q)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer st.Close()
	var rows [][]db.Cell
	for {
		b, err := st.Next(1000)
		rows = append(rows, b...)
		if err == io.EOF {
			return st.Columns(), rows, st
		}
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func TestSQLite(t *testing.T) {
	c, cfg := seeded(t)
	ctx := context.Background()
	if !strings.HasPrefix(c.ServerVersion(), "SQLite 3.") {
		t.Errorf("version = %q", c.ServerVersion())
	}

	t.Run("Databases", func(t *testing.T) {
		dbs, err := c.Databases(ctx)
		if err != nil || len(dbs) != 1 || dbs[0].Name != "main" || dbs[0].Size == "" || !strings.HasSuffix(dbs[0].Comment, "squi.db") {
			t.Fatalf("databases = %+v, %v", dbs, err)
		}
		objs, err := c.Objects(ctx, "main", "main")
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, o := range objs {
			names = append(names, string(o.Kind)+":"+o.Name)
		}
		want := []string{"table:audit_log", "table:order_items", "table:orders", "table:products", "table:users",
			"view:paid_orders", "trigger:orders_touch"}
		if !slices.Equal(names, want) {
			t.Errorf("objects = %v", names)
		}
		cols, err := c.AllColumns(ctx, "main", "main")
		if err != nil || !slices.Equal(cols["users"], []string{"id", "email", "name", "country", "meta", "created_at"}) || len(cols["paid_orders"]) != 8 {
			t.Errorf("columns = %v, %v", cols, err)
		}
	})

	t.Run("TableDetail", func(t *testing.T) {
		td, err := c.TableDetail(ctx, db.ObjectRef{Database: "main", Schema: "main", Name: "orders"})
		if err != nil {
			t.Fatal(err)
		}
		if pk := td.PrimaryKey(); !slices.Equal(pk, []string{"id"}) || td.Columns[0].Extra != "rowid" {
			t.Errorf("pk = %v, %+v", pk, td.Columns[0])
		}
		if c := td.Columns[3]; c.Name != "total" || c.Type != "REAL" || c.Nullable || c.Default == nil || *c.Default != "0" {
			t.Errorf("total = %+v", c)
		}
		if len(td.Indexes) != 2 || td.Indexes[1].Name != "orders_user_idx" || !slices.Equal(td.Indexes[0].Columns, []string{"status", "created_at"}) {
			t.Errorf("indexes = %+v", td.Indexes)
		}
		if len(td.ForeignKeys) != 1 || td.ForeignKeys[0].RefTable != "users" || !slices.Equal(td.ForeignKeys[0].RefColumns, []string{"id"}) ||
			td.ForeignKeys[0].OnDelete != "CASCADE" {
			t.Errorf("fks = %+v", td.ForeignKeys)
		}
		if len(td.ReferencedBy) != 1 || td.ReferencedBy[0].Table != "order_items" || !slices.Equal(td.ReferencedBy[0].Columns, []string{"order_id"}) {
			t.Errorf("referenced by = %+v", td.ReferencedBy)
		}
		if len(td.Constraints) != 1 || !strings.Contains(td.Constraints[0].Definition, "'cancelled'") {
			t.Errorf("checks = %+v", td.Constraints)
		}
		if len(td.Triggers) != 1 || td.Triggers[0].Timing != "AFTER" || td.Triggers[0].Event != "UPDATE" {
			t.Errorf("triggers = %+v", td.Triggers)
		}
		users, _ := c.TableDetail(ctx, db.ObjectRef{Name: "USERS"})
		if users == nil || users.Columns[0].Extra != "rowid, autoincrement" || len(users.Constraints) != 1 {
			t.Errorf("users = %+v", users)
		}
		products, _ := c.TableDetail(ctx, db.ObjectRef{Name: "products"})
		if products == nil || products.Constraints[0].Name != "price_positive" {
			t.Errorf("products constraints = %+v", products)
		}
		items, err := c.TableDetail(ctx, db.ObjectRef{Name: "order_items"})
		if err != nil || !slices.Equal(items.PrimaryKey(), []string{"order_id", "product_id"}) || !slices.ContainsFunc(items.Stats, func(kv db.KV) bool { return kv.Key == "Without rowid" }) {
			t.Errorf("items = %+v, %v", items, err)
		}
		if _, err := c.TableDetail(ctx, db.ObjectRef{Name: "nope"}); !errors.Is(err, ErrNotFound) {
			t.Errorf("missing: %v", err)
		}
	})

	t.Run("DDL", func(t *testing.T) {
		d, err := c.DDL(ctx, db.ObjectRef{Name: "orders"})
		if err != nil || !strings.HasPrefix(d, "CREATE TABLE orders") || !strings.Contains(d, "CREATE INDEX orders_user_idx") ||
			!strings.Contains(d, "CREATE TRIGGER orders_touch") {
			t.Errorf("ddl = %q, %v", d, err)
		}
	})

	t.Run("Streaming", func(t *testing.T) {
		s := session1(t, c)
		cols, rows, st := all(t, s, "SELECT id, total, created_at, note FROM orders ORDER BY id")
		if len(rows) != 20000 || st.CommandTag() != "SELECT 20000" || cols[1].Type != "real" {
			t.Fatalf("rows = %d, tag %q, cols %+v", len(rows), st.CommandTag(), cols)
		}
		if rows[0][1].S != "1.31" || !rows[0][3].Null || rows[9][3].S != "gift wrap" {
			t.Errorf("row = %v / %v", rows[0], rows[9])
		}
		_, rows, _ = all(t, s, "SELECT image, sku FROM products WHERE id = 1")
		if rows[0][0].S != "x'CAFEBABE'" {
			t.Errorf("blob = %q", rows[0][0].S)
		}
	})

	t.Run("WritesAndTransactions", func(t *testing.T) {
		s := session1(t, c)
		st, err := s.Run(ctx, "UPDATE orders SET note = 'x' WHERE user_id = 7")
		if err != nil || st.CommandTag() != "UPDATE 40" {
			t.Fatalf("update: %v %q", err, st.CommandTag())
		}
		_, rows, _ := all(t, s, "INSERT INTO audit_log (message) VALUES ('a'), ('b') RETURNING rowid")
		if len(rows) != 2 {
			t.Fatalf("returning = %v", rows)
		}
		if err := s.Begin(ctx); err != nil || !s.InTx() {
			t.Fatal(err)
		}
		all(t, s, "DELETE FROM audit_log")
		if err := s.Rollback(ctx); err != nil || s.InTx() {
			t.Fatal(err)
		}
		if _, rows, _ := all(t, s, "SELECT count(*) FROM audit_log"); rows[0][0].S != "2" {
			t.Fatalf("rolled back: %v", rows)
		}
		all(t, s, "BEGIN IMMEDIATE")
		if !s.InTx() {
			t.Fatal("BEGIN not tracked")
		}
		all(t, s, "END")
		if s.InTx() {
			t.Fatal("END not tracked")
		}
		// foreign keys are enforced
		if _, err := s.Run(ctx, "INSERT INTO orders (user_id) VALUES (999999)"); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
			t.Fatalf("fk: %v", err)
		}
		var qe *QueryError
		if _, err := s.Run(ctx, "SELEC 1"); !errors.As(err, &qe) || !strings.Contains(qe.Message, "syntax error") {
			t.Fatalf("syntax: %#v", err)
		}
	})

	t.Run("Literals", func(t *testing.T) {
		s := session1(t, c)
		d := c.Dialect()
		for _, v := range []string{"plain", "it's", `back\slash`, "naïve ✓", ""} {
			_, rows, _ := all(t, s, "SELECT "+d.QuoteLiteral(v))
			if rows[0][0].S != v {
				t.Errorf("literal %q came back as %q", v, rows[0][0].S)
			}
		}
		q := d.Select(db.Browse{Ref: db.ObjectRef{Schema: "main", Name: "orders"}, Where: "status = 'paid'", Order: []db.Order{{Column: "id", Desc: true}}}, 2)
		if _, rows, _ := all(t, s, q); len(rows) != 2 || rows[0][0].S != "19997" {
			t.Errorf("%s = %v", q, rows)
		}
	})

	t.Run("Cancel", func(t *testing.T) {
		s := session1(t, c)
		go func() {
			time.Sleep(200 * time.Millisecond)
			s.Cancel(ctx)
		}()
		start := time.Now()
		_, err := s.Run(ctx, "WITH RECURSIVE g(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM g) SELECT count(*) FROM g")
		if d := time.Since(start); d > 3*time.Second || !IsInterrupted(err) {
			t.Fatalf("cancel after %v: %v", d, err)
		}
		if _, rows, _ := all(t, s, "SELECT 42"); rows[0][0].S != "42" {
			t.Fatal("session unusable")
		}
	})

	t.Run("CloseMidStream", func(t *testing.T) {
		s := session1(t, c)
		st, err := s.Run(ctx, "SELECT * FROM orders a, orders b")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.Next(5); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		if err := st.Close(); err != nil || time.Since(start) > time.Second {
			t.Fatalf("close: %v after %v", err, time.Since(start))
		}
	})

	t.Run("Explain", func(t *testing.T) {
		s := session1(t, c)
		p, err := s.Explain(ctx, "SELECT * FROM orders o JOIN users u ON u.id = o.user_id WHERE o.user_id = 7;", false)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Children) != 2 || !strings.Contains(p.Raw, "USING INDEX orders_user_idx") || !strings.HasPrefix(p.Raw, "QUERY PLAN\n|--") {
			t.Fatalf("plan = %+v\n%s", p.Children, p.Raw)
		}
		p, err = s.Explain(ctx, "SELECT * FROM orders WHERE status = 'paid'", true)
		if err != nil || p.Rows != 5000 || p.Time <= 0 {
			t.Fatalf("analyze = %+v, %v", p, err)
		}
	})

	t.Run("BulkInsert", func(t *testing.T) {
		s := session1(t, c)
		all(t, s, "CREATE TEMP TABLE bulk (id INTEGER, v TEXT)")
		var rows [][]db.Cell
		for i := range 1234 {
			rows = append(rows, []db.Cell{db.Text(strconv.Itoa(i)), db.Text("v")})
		}
		n, err := s.BulkInsert(ctx, db.ObjectRef{Schema: "temp", Name: "bulk"}, []string{"id", "v"}, rows)
		if err != nil || n != 1234 {
			t.Fatalf("bulk: %d %v", n, err)
		}
		rows = append(rows, []db.Cell{db.Text("x"), db.Text("y"), db.Text("too many")})
		if n, err := s.BulkInsert(ctx, db.ObjectRef{Schema: "temp", Name: "bulk"}, []string{"id", "v"}, rows); err == nil || n != 0 {
			t.Fatalf("bad bulk: %d %v", n, err)
		}
		if _, r, _ := all(t, s, "SELECT count(*) FROM temp.bulk"); r[0][0].S != "1234" {
			t.Fatalf("after failed import: %v", r)
		}
	})

	t.Run("Attach", func(t *testing.T) {
		s := session1(t, c)
		other := filepath.Join(t.TempDir(), "other.db")
		all(t, s, "ATTACH '"+other+"' AS aux")
		if err := s.UseDatabase(ctx, "aux"); err != nil || s.CurrentDatabase() != "aux" {
			t.Fatal(err)
		}
		if err := s.UseDatabase(ctx, "nope"); err == nil {
			t.Fatal("unknown database accepted")
		}
	})

	t.Run("Estimate", func(t *testing.T) {
		est := c.(db.RowEstimator)
		n, exact, ok, err := est.EstimateRows(ctx, db.ObjectRef{Name: "orders"})
		if err != nil || !ok || exact || n != 20000 {
			t.Fatalf("orders: %d %v %v %v", n, exact, ok, err)
		}
		if _, _, ok, err := est.EstimateRows(ctx, db.ObjectRef{Name: "paid_orders"}); ok || err != nil {
			t.Fatalf("view: %v %v", ok, err)
		}
	})

	t.Run("Admin", func(t *testing.T) {
		a := c.Admin()
		if _, err := a.Processes(ctx); err == nil {
			t.Error("processes should be unsupported")
		}
		vars, err := a.Variables(ctx)
		if err != nil || len(vars.Rows) < 10 || vars.Rows[0][0].S != "journal_mode" {
			t.Errorf("variables = %v, %v", vars, err)
		}
		sizes, err := a.Sizes(ctx)
		if err != nil || len(sizes.Rows) < 3 || sizes.Rows[0][0].S != "file" {
			t.Errorf("sizes = %v, %v", sizes, err)
		}
	})

	t.Run("ReadOnly", func(t *testing.T) {
		ro := cfg
		ro.ReadOnly = true
		s := session1(t, open(t, ro))
		if _, err := s.Run(ctx, "DELETE FROM audit_log"); err == nil {
			t.Fatal("write on a read-only connection")
		}
		if _, rows, _ := all(t, s, "SELECT count(*) FROM users"); rows[0][0].S != "500" {
			t.Fatal(rows)
		}
	})
}

func TestOpenErrors(t *testing.T) {
	drv, _ := db.Get(db.SQLite)
	ctx := context.Background()
	if _, err := drv.Open(ctx, db.ConnConfig{Database: filepath.Join(t.TempDir(), "missing.db")}, nil); !errors.Is(err, ErrNoFile) {
		t.Errorf("missing file: %v", err)
	}
	if _, err := drv.Open(ctx, db.ConnConfig{}, nil); err == nil {
		t.Error("empty path accepted")
	}
	dial := func(context.Context, string, string) (net.Conn, error) { return nil, nil }
	if _, err := drv.Open(ctx, db.ConnConfig{Database: ":memory:"}, dial); err == nil {
		t.Error("ssh accepted")
	}
	// :memory: is shared by the metadata pool and the sessions
	c, err := drv.Open(ctx, db.ConnConfig{Database: ":memory:"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s := session1(t, c)
	all(t, s, "CREATE TABLE t (a)")
	if objs, err := c.Objects(ctx, "main", "main"); err != nil || len(objs) != 1 {
		t.Fatalf("objects = %v, %v", objs, err)
	}
}

func TestCheckConstraints(t *testing.T) {
	got := checkConstraints(`CREATE TABLE t (a INT CHECK (a > 0 AND (a < 10)), b TEXT CONSTRAINT "b ok" CHECK(b <> ')'))`)
	if len(got) != 2 || got[0].Definition != "CHECK (a > 0 AND (a < 10))" || got[1].Name != "b ok" || got[1].Definition != "CHECK (b <> ')')" {
		t.Errorf("checks = %+v", got)
	}
}
