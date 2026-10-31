package sqltext

import (
	"reflect"
	"testing"

	"github.com/farvisun/squi/internal/db"
)

func TestParams(t *testing.T) {
	cases := []struct {
		d    db.DriverName
		sql  string
		want []string
	}{
		{db.Postgres, "SELECT * FROM users WHERE id = :id AND org = :org OR id = :id", []string{":id", ":org"}},
		{db.Postgres, "SELECT $1::int, $2, $1", []string{"$1", "$2"}},
		{db.Postgres, "SELECT ':id', \"x:y\", $$ :body $$ -- :comment\n/* :c */ FROM t", nil},
		{db.Postgres, "SELECT created_at::date, a[1:n], a[i:j] FROM t", nil},
		{db.Postgres, "SELECT (:a) + :b", []string{":a", ":b"}},
		{db.MySQL, "SELECT * FROM t WHERE a = ? AND b = ?", []string{"?1", "?2"}},
		{db.MySQL, "SELECT @x := 1, @@version", nil},
		{db.SQLite, "SELECT ?1, @name, $other, :third", []string{"?1", "@name", "$other", ":third"}},
		{db.ClickHouse, "SELECT {id:UInt32}", nil},
		{db.SQLServer, "SELECT @id, :name", []string{":name"}},
	}
	for _, c := range cases {
		if got := Params(c.sql, c.d); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s %q: got %q, want %q", c.d, c.sql, got, c.want)
		}
	}
}

func TestBindParams(t *testing.T) {
	got := BindParams("SELECT * FROM t WHERE id = :id AND name = :name OR id = :id", db.Postgres,
		map[string]string{":id": "42", ":name": "'Ada'"})
	if want := "SELECT * FROM t WHERE id = 42 AND name = 'Ada' OR id = 42"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	got = BindParams("SELECT ? , ?", db.MySQL, map[string]string{"?1": "1", "?2": "NULL"})
	if want := "SELECT 1 , NULL"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	got = BindParams("SELECT :a, ':a', :b", db.Postgres, map[string]string{":a": "'x'"})
	if want := "SELECT 'x', ':a', :b"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
