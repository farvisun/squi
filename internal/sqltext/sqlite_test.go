package sqltext

import (
	"testing"

	"github.com/farvisun/squi/internal/db"
)

func TestSQLiteTokens(t *testing.T) {
	src := "SELECT [a b], `c`, \"d\", ?, ?2, $x, @y, :z, x'00', 'it''s\\' FROM t /* a /* b */ c"
	var got []string
	for _, tk := range Tokenize(src, db.SQLite) {
		if !tk.Trivia() {
			got = append(got, tk.Kind.String()+":"+tk.Text)
		}
	}
	want := []string{"keyword:SELECT", "quoted-ident:[a b]", "punct:,", "quoted-ident:`c`", "punct:,", "quoted-ident:\"d\"", "punct:,",
		"param:?", "punct:,", "param:?2", "punct:,", "param:$x", "punct:,", "param:@y", "punct:,", "param::z", "punct:,",
		"string:x'00'", "punct:,", "string:'it''s\\'", "keyword:FROM", "ident:t", "ident:c"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("token %d = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestSQLiteSplit(t *testing.T) {
	src := `CREATE TEMP TRIGGER tr AFTER INSERT ON t BEGIN
  UPDATE t SET n = CASE WHEN new.a THEN 1 ELSE 2 END WHERE id = new.id;
  INSERT INTO log VALUES (new.id);
END;
SELECT 1; BEGIN; END;`
	got := Split(src, db.SQLite)
	if len(got) != 4 || got[1].SQL != "SELECT 1" || got[2].SQL != "BEGIN" || got[3].SQL != "END" {
		t.Fatalf("split = %+v", got)
	}
}

func TestSQLiteClassify(t *testing.T) {
	cases := []struct {
		q     string
		kind  Kind
		write bool
		tx    TxOp
	}{
		{"PRAGMA table_info(users)", KindRead, false, TxNone},
		{"pragma main.index_list('t')", KindRead, false, TxNone},
		{"PRAGMA foreign_keys = OFF", KindSession, true, TxNone},
		{"PRAGMA query_only=0", KindSession, true, TxNone},
		{"PRAGMA optimize", KindDDL, true, TxNone},
		{"PRAGMA main.wal_checkpoint(TRUNCATE)", KindDDL, true, TxNone},
		{"ATTACH 'x.db' AS x", KindSession, false, TxNone},
		{"VACUUM", KindDDL, true, TxNone},
		{"BEGIN IMMEDIATE", KindTx, false, TxBegin},
		{"END TRANSACTION", KindTx, false, TxCommit},
		{"INSERT OR REPLACE INTO t VALUES (1)", KindWrite, true, TxNone},
		{"EXPLAIN QUERY PLAN SELECT * FROM t", KindRead, false, TxNone},
		{"CREATE TRIGGER tr AFTER INSERT ON t BEGIN DELETE FROM u; END", KindDDL, true, TxNone},
	}
	for _, c := range cases {
		got := Classify(c.q, db.SQLite)
		if got.Kind != c.kind || got.IsWrite() != c.write || got.TxOp != c.tx {
			t.Errorf("%s = %+v", c.q, got)
		}
	}
	if c := Classify("DELETE FROM t", db.SQLite); !c.Destructive {
		t.Error("unfiltered DELETE not destructive")
	}
}
