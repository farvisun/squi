package db

import "testing"

func TestSelect(t *testing.T) {
	pg := Dialect{Driver: Postgres}
	my := Dialect{Driver: MySQL}
	ref := ObjectRef{Database: "app", Schema: "public", Name: "orders"}
	cases := []struct {
		d     Dialect
		b     Browse
		limit int
		want  string
	}{
		{pg, Browse{Ref: ref}, 0, `SELECT * FROM "public"."orders"`},
		{pg, Browse{Ref: ref}, 100, `SELECT * FROM "public"."orders" LIMIT 100`},
		{pg, Browse{Ref: ref, Where: "status = 'paid'", Order: []Order{{Column: "id", Desc: true}, {Column: "user id"}}}, 0,
			`SELECT * FROM "public"."orders" WHERE (status = 'paid') ORDER BY "id" DESC, "user id"`},
		{my, Browse{Ref: ObjectRef{Database: "shop", Schema: "shop", Name: "orders"}, Where: "total > 10"}, 0,
			"SELECT * FROM `shop`.`orders` WHERE (total > 10)"},
	}
	for _, c := range cases {
		if got := c.d.Select(c.b, c.limit); got != c.want {
			t.Errorf("got  %s\nwant %s", got, c.want)
		}
	}
	if got := pg.SelectAll(ref, 5); got != `SELECT * FROM "public"."orders" LIMIT 5` {
		t.Errorf("SelectAll = %s", got)
	}
}

func TestEquals(t *testing.T) {
	pg := Dialect{Driver: Postgres}
	got := pg.Equals([]string{"id", "tenant"}, []Cell{Text("42"), NullCell})
	if want := `"id" = '42' AND "tenant" IS NULL`; got != want {
		t.Errorf("got %s want %s", got, want)
	}
	my := Dialect{Driver: MySQL}
	if got := my.Equals([]string{"code"}, []Cell{Text("it's")}); got != "`code` = 'it''s'" {
		t.Errorf("mysql: %s", got)
	}
}
