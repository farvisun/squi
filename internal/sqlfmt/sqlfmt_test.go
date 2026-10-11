package sqlfmt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/farvisun/squi/internal/db"
	"github.com/farvisun/squi/internal/sqltext"
)

func TestFormat(t *testing.T) {
	cases := []struct {
		d        db.DriverName
		in, want string
	}{
		{db.Postgres, "select id, email from users where active and created_at > now() - interval '1 day' order by id desc limit 10",
			`SELECT
  id,
  email
FROM users
WHERE active
  AND created_at > now() - interval '1 day'
ORDER BY id DESC
LIMIT 10`},
		{db.Postgres, "select count(*) from orders o left join users u on u.id = o.user_id and u.active where o.total between 1 and 10 or o.total is null",
			`SELECT count(*)
FROM orders o
LEFT JOIN users u ON u.id = o.user_id
  AND u.active
WHERE o.total BETWEEN 1 AND 10
  OR o.total IS NULL`},
		{db.Postgres, "select * from users where id in (select user_id from orders where total > 100) and x::text = 'a'",
			`SELECT *
FROM users
WHERE id IN (
    SELECT user_id
    FROM orders
    WHERE total > 100
  )
  AND x::text = 'a'`},
		{db.Postgres, "with paid as (select * from orders where status = 'paid'), big as (select * from paid where total > -5) select * from big",
			`WITH paid AS (
  SELECT *
  FROM orders
  WHERE status = 'paid'
),
big AS (
  SELECT *
  FROM paid
  WHERE total > -5
)
SELECT *
FROM big`},
		{db.MySQL, "update `t` set a = 1, b = 'x' where id = 3",
			"UPDATE `t`\nSET\n  a = 1,\n  b = 'x'\nWHERE id = 3"},
		{db.Postgres, "insert into t (a, b) values (1, 2), (3, 4) on conflict (a) do update set b = excluded.b returning *",
			"INSERT INTO t (a, b)\nVALUES (1, 2),\n  (3, 4) ON CONFLICT (a) DO UPDATE SET b = excluded.b\nRETURNING *"},
		{db.ClickHouse, "select toDate(ts) d, count() from events prewhere d > today() - 7 group by d format JSON",
			"SELECT\n  toDate(ts) d,\n  count()\nFROM events\nPREWHERE d > today() - 7\nGROUP BY d\nFORMAT JSON"},
		{db.Postgres, "select a -- the a column\n, b from t",
			"SELECT\n  a -- the a column\n  , b\nFROM t"},
		{db.Postgres, "create table t (id int primary key)", "create table t (id int primary key)"},
		{db.Postgres, "select distinct a, b from t", "SELECT DISTINCT a,\n  b\nFROM t"},
		{db.Postgres, "select a from t union all select b from u", "SELECT a\nFROM t\nUNION ALL\nSELECT b\nFROM u"},
		{db.Postgres, "delete from t where a is distinct from b", "DELETE FROM t\nWHERE a IS DISTINCT FROM b"},
		{db.Postgres, "select * from t for update", "SELECT *\nFROM t FOR UPDATE"},
		{db.SQLServer, "select top (5) [order id], @x from #t o with (nolock) where a = N'x' order by 1 offset 0 rows fetch next 5 rows only",
			"SELECT TOP (5) [order id],\n  @x\nFROM #t o WITH (nolock)\nWHERE a = N'x'\nORDER BY 1\nOFFSET 0 ROWS FETCH NEXT 5 ROWS ONLY"},
	}
	for _, c := range cases {
		got := Format(c.in, c.d)
		if got != c.want {
			t.Errorf("Format(%q)\n--- got\n%s\n--- want\n%s", c.in, got, c.want)
		}
	}
}

// The formatter may only change whitespace and the case of keywords.
func TestFormatKeepsTokens(t *testing.T) {
	inputs := []string{
		"select a.b, \"Mixed Case\", 'it''s; -- not a comment' from s.t where x = $1 and y = E'a\\nb' /* c */ order by 1",
		"SELECT * FROM t WHERE a = -1 AND b = +2 AND c = d - 3 AND arr[1] = 2",
		"with recursive r as (select 1 n union all select n + 1 from r where n < 5) select * from r",
		"select json_agg(x order by y) filter (where z) over (partition by w) from t",
		"select case when a then 'x' else 'y' end as c from t group by 1 having count(*) > 1",
		"select * from a join b using (id) cross join c natural join d where exists (select 1 from e where e.id = a.id)",
	}
	for _, in := range inputs {
		out := Format(in, db.Postgres)
		a, b := sigTexts(in), sigTexts(out)
		if strings.Join(a, " ") != strings.Join(b, " ") {
			t.Errorf("tokens changed:\n in: %v\nout: %v\n%s", a, b, out)
		}
		if again := Format(out, db.Postgres); again != out {
			t.Errorf("not idempotent:\n%s\n---\n%s", out, again)
		}
	}
}

func sigTexts(s string) []string {
	var out []string
	for _, t := range sqltext.Tokenize(s, db.Postgres) {
		if t.Kind == sqltext.TokWhitespace {
			continue
		}
		txt := t.Text
		if t.Kind == sqltext.TokKeyword {
			txt = strings.ToUpper(txt)
		}
		out = append(out, txt)
	}
	return out
}

func TestFormatScript(t *testing.T) {
	src := "-- header\nselect 1 from t;\n\nselect a, b from u;  -- after\nshow tables;"
	got := FormatScript(src, 0, len(src), db.MySQL)
	want := "-- header\nSELECT 1\nFROM t;\n\nSELECT\n  a,\n  b\nFROM u;  -- after\nshow tables;"
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	// only the statement under the cursor
	at := strings.Index(src, "select a")
	got = FormatScript(src, at, at+1, db.MySQL)
	if !strings.HasPrefix(got, "-- header\nselect 1 from t;") || !strings.Contains(got, "SELECT\n  a,") {
		t.Errorf("partial:\n%s", got)
	}
}

// Every statement of the playground seed scripts keeps its tokens and
// formats idempotently.
func TestFormatSeedScripts(t *testing.T) {
	files := map[string]db.DriverName{"postgres.sql": db.Postgres, "mysql.sql": db.MySQL, "clickhouse.sql": db.ClickHouse,
		"sqlserver.sql": db.SQLServer, "sqlite.sql": db.SQLite}
	for name, d := range files {
		b, err := os.ReadFile(filepath.Join("..", "..", "dev", "seed", name))
		if err != nil {
			t.Skipf("seed %s: %v", name, err)
		}
		for _, st := range sqltext.Split(string(b), d) {
			out := Format(st.SQL, d)
			if strings.Join(sigTextsD(st.SQL, d), " ") != strings.Join(sigTextsD(out, d), " ") {
				t.Errorf("%s: tokens changed:\n%s\n---\n%s", name, st.SQL, out)
				continue
			}
			if again := Format(out, d); again != out {
				t.Errorf("%s: not idempotent:\n%s\n---\n%s", name, out, again)
			}
		}
	}
}

func sigTextsD(s string, d db.DriverName) []string {
	var out []string
	for _, t := range sqltext.Tokenize(s, d) {
		if t.Kind == sqltext.TokWhitespace {
			continue
		}
		txt := t.Text
		if t.Kind == sqltext.TokKeyword {
			txt = strings.ToUpper(txt)
		}
		out = append(out, txt)
	}
	return out
}
