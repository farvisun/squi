package schema

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/farvisun/squi/internal/db"
)

// fakeConn implements the metadata part of db.Conn; other methods panic.
type fakeConn struct {
	db.Conn
	schemas []string
	objects map[string][]db.Object
	columns map[string]map[string][]string
	fail    map[string]bool // schema -> fail Objects
	calls   []string
}

func (f *fakeConn) Schemas(_ context.Context, database string) ([]string, error) {
	f.calls = append(f.calls, "schemas:"+database)
	return f.schemas, nil
}

func (f *fakeConn) Objects(_ context.Context, database, schema string) ([]db.Object, error) {
	f.calls = append(f.calls, "objects:"+database+"."+schema)
	if f.fail[schema] {
		return nil, errors.New("permission denied")
	}
	return f.objects[schema], nil
}

func (f *fakeConn) AllColumns(_ context.Context, database, schema string) (map[string][]string, error) {
	return f.columns[schema], nil
}

func pgCache() *Cache {
	c := NewCache(db.Postgres)
	c.SetDatabases([]string{"shop", "postgres"})
	c.SetSchemas("shop", []string{"public", "sales", "pg_catalog", "information_schema"})
	c.SetObjects("shop", "public", []db.Object{
		{Schema: "public", Name: "users", Kind: db.KindTable},
		{Schema: "public", Name: "orders", Kind: db.KindTable},
		{Schema: "public", Name: "Order Items", Kind: db.KindTable},
		{Schema: "public", Name: "active_users", Kind: db.KindView},
		{Schema: "public", Name: "calc_total", Kind: db.KindFunction},
		{Schema: "public", Name: "users_id_seq", Kind: db.KindSequence},
	})
	c.SetColumns("shop", "public", map[string][]string{
		"users":        {"id", "email", "CreatedAt", "user"},
		"orders":       {"id", "user_id", "total"},
		"Order Items":  {"order_id", "sku"},
		"active_users": {"id", "email"},
	})
	c.SetObjects("shop", "sales", []db.Object{{Schema: "sales", Name: "invoices", Kind: db.KindTable}})
	c.SetColumns("shop", "sales", map[string][]string{"invoices": {"invoice_no", "amount"}})
	return c
}

func myCache() *Cache {
	c := NewCache(db.MySQL)
	c.SetDatabases([]string{"shop", "crm"})
	c.SetSchemas("shop", []string{"shop"})
	c.SetObjects("shop", "shop", []db.Object{
		{Schema: "shop", Name: "users", Kind: db.KindTable},
		{Schema: "shop", Name: "Orders", Kind: db.KindTable},
	})
	c.SetColumns("shop", "shop", map[string][]string{"users": {"id", "name", "order"}, "Orders": {"id", "user_id"}})
	c.SetObjects("crm", "crm", []db.Object{{Schema: "crm", Name: "leads", Kind: db.KindTable}})
	c.SetColumns("crm", "crm", map[string][]string{"leads": {"lead_id", "source"}})
	return c
}

var (
	pgCtx = Context{Database: "shop", SearchPath: []string{"public"}}
	myCtx = Context{Database: "shop", SearchPath: []string{"shop"}}
)

// complete runs Complete with '|' marking the cursor.
func complete(t *testing.T, c *Cache, ctx Context, src string) Completion {
	t.Helper()
	i := strings.Index(src, "|")
	if i < 0 {
		t.Fatalf("no cursor in %q", src)
	}
	return Complete(src[:i]+src[i+1:], i, c, ctx)
}

func labels(comp Completion, kinds ...ItemKind) []string {
	out := []string{}
	for _, it := range comp.Items {
		if len(kinds) == 0 || containsKind(kinds, it.Kind) {
			out = append(out, it.Label)
		}
	}
	return out
}

func containsKind(ks []ItemKind, k ItemKind) bool {
	for _, x := range ks {
		if x == k {
			return true
		}
	}
	return false
}

func find(comp Completion, label string, kind ItemKind) (Item, bool) {
	for _, it := range comp.Items {
		if it.Label == label && it.Kind == kind {
			return it, true
		}
	}
	return Item{}, false
}

func TestCompleteTables(t *testing.T) {
	c := pgCache()
	comp := complete(t, c, pgCtx, "SELECT * FROM |")
	if got, want := labels(comp, ItemTable, ItemView), []string{"users", "orders", "Order Items", "active_users", "sales.invoices"}; !reflect.DeepEqual(got, want) {
		t.Errorf("tables = %q, want %q", got, want)
	}
	if got := labels(comp, ItemSchema); !reflect.DeepEqual(got, []string{"public", "sales"}) {
		t.Errorf("schemas = %q", got)
	}
	if len(labels(comp, ItemKeyword, ItemColumn, ItemFunction)) != 0 {
		t.Errorf("unexpected non-table items: %q", labels(comp))
	}
	if it, _ := find(comp, "Order Items", ItemTable); it.Insert != `"Order Items"` {
		t.Errorf("quoted insert = %q", it.Insert)
	}

	comp = complete(t, c, pgCtx, "select * from us|")
	if comp.Prefix != "us" || comp.Start != 14 || comp.End != 16 {
		t.Errorf("prefix/range = %q %d-%d", comp.Prefix, comp.Start, comp.End)
	}
	if got := labels(comp); len(got) < 2 || got[0] != "users" {
		t.Errorf("prefix tables = %q", got)
	}

	comp = complete(t, c, pgCtx, "SELECT * FROM users u, ord|")
	if got := labels(comp); len(got) == 0 || got[0] != "orders" {
		t.Errorf("comma in FROM = %q", got)
	}
	comp = complete(t, c, pgCtx, "SELECT * FROM users u JOIN |")
	if _, ok := find(comp, "orders", ItemTable); !ok {
		t.Errorf("JOIN = %q", labels(comp))
	}
	comp = complete(t, c, pgCtx, "INSERT INTO |")
	if _, ok := find(comp, "users", ItemTable); !ok {
		t.Errorf("INTO = %q", labels(comp))
	}
	comp = complete(t, c, pgCtx, "UPDATE inv|")
	if got := labels(comp); len(got) == 0 || got[0] != "sales.invoices" {
		t.Errorf("fuzzy qualified table = %q", got)
	}
	if it, _ := find(comp, "sales.invoices", ItemTable); it.Insert != "sales.invoices" {
		t.Errorf("qualified insert = %q", it.Insert)
	}
}

func TestCompleteColumns(t *testing.T) {
	c := pgCache()
	comp := complete(t, c, pgCtx, "SELECT u.| FROM users u")
	if got := labels(comp); !reflect.DeepEqual(got, []string{"id", "email", "CreatedAt", "user"}) {
		t.Errorf("alias columns = %q", got)
	}
	if it, _ := find(comp, "CreatedAt", ItemColumn); it.Insert != `"CreatedAt"` || it.Detail != "users" {
		t.Errorf("mixed-case column = %+v", it)
	}
	if it, _ := find(comp, "user", ItemColumn); it.Insert != `"user"` {
		t.Errorf("reserved column = %+v", it)
	}

	comp = complete(t, c, pgCtx, "SELECT * FROM users AS u JOIN orders o ON o.us| = u.id")
	if got := labels(comp); len(got) == 0 || got[0] != "user_id" || comp.Prefix != "us" {
		t.Errorf("join alias prefix = %q (%q)", got, comp.Prefix)
	}

	comp = complete(t, c, pgCtx, "SELECT | FROM users u JOIN orders o ON o.user_id = u.id")
	cols := map[string]bool{}
	for _, it := range comp.Items {
		if it.Kind == ItemColumn {
			cols[it.Label+"@"+it.Detail] = true
		}
	}
	for _, want := range []string{"email@users", "id@users", "total@orders", "id@orders"} {
		if !cols[want] {
			t.Errorf("missing column %s in %v", want, cols)
		}
	}
	if _, ok := find(comp, "u", ItemAlias); !ok {
		t.Errorf("missing alias u")
	}

	comp = complete(t, c, pgCtx, "SELECT * FROM users WHERE em|")
	if got := labels(comp); len(got) == 0 || got[0] != "email" {
		t.Errorf("WHERE prefix = %q", got)
	}

	comp = complete(t, c, pgCtx, "SELECT * FROM sales.invoices i WHERE i.|")
	if got := labels(comp); !reflect.DeepEqual(got, []string{"invoice_no", "amount"}) {
		t.Errorf("schema-qualified ref = %q", got)
	}
	comp = complete(t, c, pgCtx, "SELECT sales.invoices.| FROM sales.invoices")
	if got := labels(comp); !reflect.DeepEqual(got, []string{"invoice_no", "amount"}) {
		t.Errorf("schema.table. = %q", got)
	}
	comp = complete(t, c, pgCtx, `SELECT oi.| FROM "Order Items" oi`)
	if got := labels(comp); !reflect.DeepEqual(got, []string{"order_id", "sku"}) {
		t.Errorf("quoted table alias = %q", got)
	}
	comp = complete(t, c, pgCtx, "SELECT users.| FROM users")
	if got := labels(comp); len(got) != 4 {
		t.Errorf("unaliased table qualifier = %q", got)
	}
	comp = complete(t, c, pgCtx, "UPDATE users SET |")
	if _, ok := find(comp, "email", ItemColumn); !ok {
		t.Errorf("SET columns = %q", labels(comp))
	}
	comp = complete(t, c, pgCtx, "INSERT INTO orders (|")
	if _, ok := find(comp, "total", ItemColumn); !ok {
		t.Errorf("INSERT column list = %q", labels(comp))
	}
	comp = complete(t, c, pgCtx, "SELECT 1;\nSELECT * FROM orders WHERE |;\nSELECT * FROM users")
	if _, ok := find(comp, "email", ItemColumn); ok {
		t.Errorf("columns leaked from another statement")
	}
}

func TestCompleteSchemaMembers(t *testing.T) {
	c := pgCache()
	comp := complete(t, c, pgCtx, "SELECT * FROM sales.|")
	if got := labels(comp); !reflect.DeepEqual(got, []string{"invoices"}) {
		t.Errorf("schema members = %q", got)
	}
	comp = complete(t, c, pgCtx, "SELECT * FROM public.act|")
	if got := labels(comp); len(got) == 0 || got[0] != "active_users" {
		t.Errorf("public.act = %q", got)
	}
}

func TestCompleteMySQL(t *testing.T) {
	c := myCache()
	comp := complete(t, c, myCtx, "SELECT * FROM crm.|")
	if got := labels(comp); !reflect.DeepEqual(got, []string{"leads"}) {
		t.Errorf("db members = %q", got)
	}
	comp = complete(t, c, myCtx, "SELECT l.| FROM crm.leads l")
	if got := labels(comp); !reflect.DeepEqual(got, []string{"lead_id", "source"}) {
		t.Errorf("db-qualified alias = %q", got)
	}
	comp = complete(t, c, myCtx, "SELECT * FROM |")
	if got := labels(comp, ItemDatabase); !reflect.DeepEqual(got, []string{"shop", "crm"}) {
		t.Errorf("mysql databases = %q", got)
	}
	if _, ok := find(comp, "crm.leads", ItemTable); !ok {
		t.Errorf("other database tables = %q", labels(comp))
	}
	if it, _ := find(comp, "Orders", ItemTable); it.Insert != "Orders" {
		t.Errorf("mysql mixed case should not be quoted: %q", it.Insert)
	}
	comp = complete(t, c, myCtx, "SELECT u.| FROM users u")
	if it, _ := find(comp, "order", ItemColumn); it.Insert != "`order`" {
		t.Errorf("mysql reserved column = %+v", it)
	}
	comp = complete(t, c, myCtx, "USE |")
	if got := labels(comp); !reflect.DeepEqual(got, []string{"shop", "crm"}) {
		t.Errorf("USE = %q", got)
	}
	comp = complete(t, c, myCtx, "SELECT ifn|")
	if it, ok := find(comp, "ifnull", ItemFunction); !ok || it.Insert != "ifnull(" {
		t.Errorf("mysql function = %+v", comp.Items)
	}
	comp = complete(t, c, myCtx, "DELIMITER //\nCREATE PROCEDURE p() BEGIN SELECT * FROM us|; END//")
	if got := labels(comp); len(got) == 0 || got[0] != "users" {
		t.Errorf("inside procedure body = %q", got)
	}
}

func TestCompleteNoSuggestions(t *testing.T) {
	c := pgCache()
	for _, src := range []string{
		"SELECT 'us|'",
		"SELECT 'abc|",
		"SELECT 1 -- FROM us|",
		"SELECT /* FROM | */ 1",
		"SELECT $$ FROM |",
		"SELECT $1|",
		"SELECT 12|",
	} {
		if comp := complete(t, c, pgCtx, src); len(comp.Items) != 0 {
			t.Errorf("%q: expected no items, got %q", src, labels(comp))
		}
	}
	my := myCache()
	if comp := complete(t, my, myCtx, "SELECT 1 # FROM |"); len(comp.Items) != 0 {
		t.Errorf("mysql hash comment: %q", labels(comp))
	}
	if comp := complete(t, my, myCtx, "SET @us|"); len(comp.Items) != 0 {
		t.Errorf("mysql variable: %q", labels(comp))
	}
	// A closed string or comment before the cursor does not block completion.
	if comp := complete(t, c, pgCtx, "SELECT 'x' /* c */ FROM |"); len(comp.Items) == 0 {
		t.Error("expected tables after closed string/comment")
	}
}

func TestCompleteKeywords(t *testing.T) {
	c := pgCache()
	comp := complete(t, c, pgCtx, "sel|")
	if got := labels(comp); len(got) == 0 || got[0] != "select" || comp.Items[0].Insert != "select" {
		t.Errorf("lowercase keyword = %q", got)
	}
	comp = complete(t, c, pgCtx, "SEL|")
	if comp.Items[0].Insert != "SELECT" {
		t.Errorf("uppercase keyword = %q", comp.Items[0].Insert)
	}
	comp = complete(t, c, pgCtx, "Sel|")
	if comp.Items[0].Insert != "SELECT" {
		t.Errorf("mixed-case keyword = %q", comp.Items[0].Insert)
	}
	comp = complete(t, c, pgCtx, "select * from users wh|")
	if got := labels(comp); len(got) == 0 || got[0] != "where" {
		t.Errorf("clause keyword = %q", got)
	}
	comp = complete(t, c, pgCtx, "select * from users |")
	if got := labels(comp, ItemKeyword); len(got) == 0 || got[0] != strings.ToLower(got[0]) {
		t.Errorf("keyword case from previous keyword = %q", got)
	}
	if len(labels(comp, ItemColumn)) != 0 {
		t.Errorf("no columns expected after table name")
	}
	comp = complete(t, c, pgCtx, "SELECT co|")
	if it, ok := find(comp, "count", ItemFunction); !ok || it.Insert != "count(" {
		t.Errorf("function case = %+v", labels(comp))
	}
	comp = complete(t, c, pgCtx, "SELECT CO|")
	if it, ok := find(comp, "COUNT", ItemFunction); !ok || it.Insert != "COUNT(" {
		t.Errorf("function case = %+v", labels(comp))
	}
	comp = complete(t, c, pgCtx, "SELECT calc|(1)")
	if it, ok := find(comp, "calc_total", ItemFunction); !ok || it.Insert != "calc_total" {
		t.Errorf("user function before paren = %+v", it)
	}
	comp = complete(t, c, pgCtx, "|")
	if len(comp.Items) != maxItems || comp.Items[0].Kind != ItemKeyword {
		t.Errorf("empty statement = %d items", len(comp.Items))
	}
}

func TestCompleteQuoted(t *testing.T) {
	c := pgCache()
	comp := complete(t, c, pgCtx, `SELECT * FROM "Ord|`)
	if comp.Prefix != "Ord" || comp.Start != 14 {
		t.Errorf("quoted prefix = %q at %d", comp.Prefix, comp.Start)
	}
	it, ok := find(comp, "Order Items", ItemTable)
	if !ok || it.Insert != `"Order Items"` {
		t.Errorf("quoted completion = %+v", comp.Items)
	}
	if o, _ := find(comp, "orders", ItemTable); o.Insert != `"orders"` {
		t.Errorf("forced quoting = %q", o.Insert)
	}
	if len(labels(comp, ItemKeyword)) != 0 {
		t.Errorf("keywords offered inside quotes")
	}
	my := myCache()
	comp = complete(t, my, myCtx, "SELECT * FROM `us|")
	if it, ok := find(comp, "users", ItemTable); !ok || it.Insert != "`users`" || comp.Prefix != "us" {
		t.Errorf("backtick completion = %+v", comp)
	}
}

func TestCache(t *testing.T) {
	c := pgCache()
	if got := c.Columns("SHOP", "Public", "USERS"); len(got) != 4 {
		t.Errorf("case-insensitive Columns = %q", got)
	}
	if got := c.Columns("shop", "public", "missing"); got != nil {
		t.Errorf("missing table = %q", got)
	}
	if got := c.Objects("shop", "sales"); len(got) != 1 {
		t.Errorf("Objects = %v", got)
	}
	c.Invalidate("shop")
	if c.Objects("shop", "public") != nil || len(c.Databases()) != 2 {
		t.Error("Invalidate(shop)")
	}
	c.Invalidate("")
	if c.Databases() != nil {
		t.Error("Invalidate(all)")
	}
}

func TestLoad(t *testing.T) {
	conn := &fakeConn{
		schemas: []string{"public", "audit", "pg_catalog", "pg_toast", "broken"},
		objects: map[string][]db.Object{
			"public": {{Schema: "public", Name: "users", Kind: db.KindTable}},
			"audit":  {{Schema: "audit", Name: "log", Kind: db.KindTable}},
		},
		columns: map[string]map[string][]string{"public": {"users": {"id"}}, "audit": {"log": {"at"}}},
		fail:    map[string]bool{"broken": true},
	}
	c := NewCache(db.Postgres)
	err := c.Load(context.Background(), conn, "shop")
	if err == nil || !strings.Contains(err.Error(), "broken") {
		t.Errorf("expected collected error, got %v", err)
	}
	if got := c.Columns("shop", "audit", "log"); !reflect.DeepEqual(got, []string{"at"}) {
		t.Errorf("audit columns = %q", got)
	}
	if got := c.Schemas("shop"); len(got) != 5 {
		t.Errorf("schemas = %q", got)
	}
	for _, call := range conn.calls {
		if strings.Contains(call, "pg_") {
			t.Errorf("system schema loaded: %s", call)
		}
	}

	myConn := &fakeConn{
		objects: map[string][]db.Object{"shop": {{Schema: "shop", Name: "users", Kind: db.KindTable}}},
		columns: map[string]map[string][]string{"shop": {"users": {"id", "name"}}},
	}
	mc := NewCache(db.MySQL)
	if err := mc.Load(context.Background(), myConn, "shop"); err != nil {
		t.Fatal(err)
	}
	if got := mc.Columns("shop", "shop", "users"); len(got) != 2 {
		t.Errorf("mysql columns = %q", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := mc.Load(ctx, myConn, "shop"); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled Load = %v", err)
	}
}
