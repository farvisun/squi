package sqltext

import (
	"reflect"
	"strings"
	"testing"

	"github.com/farvisun/squi/internal/db"
)

func sqls(stmts []Statement) []string {
	out := []string{}
	for _, s := range stmts {
		out = append(out, s.SQL)
	}
	return out
}

func TestSplit(t *testing.T) {
	tests := []struct {
		name string
		d    db.DriverName
		src  string
		want []string
	}{
		{"simple", pg, "SELECT 1; SELECT 2;", []string{"SELECT 1", "SELECT 2"}},
		{"no trailing delimiter", pg, "SELECT 1;\nSELECT 2", []string{"SELECT 1", "SELECT 2"}},
		{"empty statements", pg, ";;  ; SELECT 1;;", []string{"SELECT 1"}},
		{"only comments dropped", pg, "-- hello\n/* x */;\nSELECT 1", []string{"SELECT 1"}},
		{"leading comment trimmed", pg, "-- users\nSELECT 1; -- trailing\n", []string{"SELECT 1"}},
		{"inner comment kept", pg, "SELECT /* ; */ 1 -- ;\n, 2;", []string{"SELECT /* ; */ 1 -- ;\n, 2"}},
		{"semicolon in string", pg, "SELECT ';', 'a''b;'; SELECT 2", []string{"SELECT ';', 'a''b;'", "SELECT 2"}},
		{"semicolon in quoted ident", pg, `SELECT 1 AS "a;b"; SELECT 2`, []string{`SELECT 1 AS "a;b"`, "SELECT 2"}},
		{"mysql backslash string", my, `SELECT 'a\';b'; SELECT 2`, []string{`SELECT 'a\';b'`, "SELECT 2"}},
		{"pg backslash is literal", pg, `SELECT 'a\'; SELECT 2`, []string{`SELECT 'a\'`, "SELECT 2"}},
		{"pg E string", pg, `SELECT E'a\';b'; SELECT 2`, []string{`SELECT E'a\';b'`, "SELECT 2"}},
		{"mysql double-quoted string", my, `SELECT "a;b"; SELECT 2`, []string{`SELECT "a;b"`, "SELECT 2"}},
		{"mysql hash comment", my, "SELECT 1 # ; not split\n, 2; SELECT 2", []string{"SELECT 1 # ; not split\n, 2", "SELECT 2"}},
		{"pg hash is operator", pg, "SELECT '{}'::jsonb #> '{a}'; SELECT 2", []string{"SELECT '{}'::jsonb #> '{a}'", "SELECT 2"}},
		{"nested block comment", pg, "SELECT 1 /* a /* ; */ ; */ + 1; SELECT 2", []string{"SELECT 1 /* a /* ; */ ; */ + 1", "SELECT 2"}},
		{"dollar function", pg, "CREATE FUNCTION f() RETURNS int AS $$ BEGIN RETURN 1; END; $$ LANGUAGE plpgsql; SELECT f();",
			[]string{"CREATE FUNCTION f() RETURNS int AS $$ BEGIN RETURN 1; END; $$ LANGUAGE plpgsql", "SELECT f()"}},
		{"tagged dollar", pg, "DO $body$ BEGIN PERFORM 1; $$x$$; END $body$; SELECT 1",
			[]string{"DO $body$ BEGIN PERFORM 1; $$x$$; END $body$", "SELECT 1"}},
		{"params not dollar quotes", pg, "SELECT $1; SELECT $2", []string{"SELECT $1", "SELECT $2"}},
		{"begin atomic", pg, "CREATE OR REPLACE FUNCTION f() RETURNS int LANGUAGE sql BEGIN ATOMIC SELECT 1; SELECT CASE WHEN true THEN 2 END; END; SELECT 3;",
			[]string{"CREATE OR REPLACE FUNCTION f() RETURNS int LANGUAGE sql BEGIN ATOMIC SELECT 1; SELECT CASE WHEN true THEN 2 END; END", "SELECT 3"}},
		{"pg tx begin", pg, "BEGIN; UPDATE t SET a=1; END;", []string{"BEGIN", "UPDATE t SET a=1", "END"}},
		{"mysql delimiter", my, "DELIMITER //\nCREATE PROCEDURE p()\nBEGIN\n  SELECT 1;\n  SELECT 2;\nEND//\nDELIMITER ;\nCALL p();",
			[]string{"CREATE PROCEDURE p()\nBEGIN\n  SELECT 1;\n  SELECT 2;\nEND", "CALL p()"}},
		{"mysql delimiter $$", my, "delimiter $$\nCREATE TRIGGER t BEFORE INSERT ON x FOR EACH ROW BEGIN SET NEW.a = 1; END$$\ndelimiter ;\nSELECT 1;",
			[]string{"CREATE TRIGGER t BEFORE INSERT ON x FOR EACH ROW BEGIN SET NEW.a = 1; END", "SELECT 1"}},
		{"mysql delimiter unterminated tail", my, "DELIMITER ;;\nSELECT 1;;\nSELECT 2", []string{"SELECT 1", "SELECT 2"}},
		{"mysql begin without delimiter", my, "CREATE PROCEDURE p() BEGIN SELECT 1; END;", []string{"CREATE PROCEDURE p() BEGIN SELECT 1", "END"}},
		{"mysql versioned comment kept", my, "/*!40101 SET NAMES utf8 */;\nSELECT 1;", []string{"/*!40101 SET NAMES utf8 */", "SELECT 1"}},
		{"hint kept", pg, "/*+ SeqScan(t) */ SELECT * FROM t;", []string{"/*+ SeqScan(t) */ SELECT * FROM t"}},
		{"unterminated string", pg, "SELECT 'abc; SELECT 2", []string{"SELECT 'abc; SELECT 2"}},
		{"empty", pg, "  \n ", []string{}},
		{"crlf", pg, "SELECT 1;\r\nSELECT 2;\r\n", []string{"SELECT 1", "SELECT 2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sqls(Split(tt.src, tt.d))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Split(%q)\n got: %q\nwant: %q", tt.src, got, tt.want)
			}
		})
	}
}

func TestSplitOffsets(t *testing.T) {
	src := "-- c\n  SELECT 1;\n\nSELECT\n  2 ;"
	stmts := Split(src, pg)
	if len(stmts) != 2 {
		t.Fatalf("got %d statements", len(stmts))
	}
	for _, s := range stmts {
		if src[s.Start:s.End] != s.SQL {
			t.Errorf("offsets mismatch: %+v", s)
		}
	}
	if s := stmts[0]; s.Line != 2 || s.Col != 3 {
		t.Errorf("stmt0 at %d:%d", s.Line, s.Col)
	}
	if s := stmts[1]; s.Line != 4 || s.Col != 1 || s.SQL != "SELECT\n  2" {
		t.Errorf("stmt1 = %+v", s)
	}
}

func TestStatementAt(t *testing.T) {
	// '|' marks the cursor; want is the expected statement SQL ("" = none).
	tests := []struct {
		d    db.DriverName
		src  string
		want string
	}{
		{pg, "SEL|ECT 1; SELECT 2;", "SELECT 1"},
		{pg, "|SELECT 1; SELECT 2;", "SELECT 1"},
		{pg, "SELECT 1|; SELECT 2;", "SELECT 1"},
		{pg, "SELECT 1;| SELECT 2;", "SELECT 1"},
		{pg, "SELECT 1; |SELECT 2;", "SELECT 2"},
		{pg, "SELECT 1; SELECT 2;|", "SELECT 2"},
		{pg, "SELECT 1;\n|\nSELECT 2;", "SELECT 1"},
		{pg, "SELECT 1;\n\n|SELECT 2;", "SELECT 2"},
		{pg, "SELECT 1;\n\n  |  SELECT 2;", "SELECT 2"},
		{pg, "SELECT 1;\n\nSELECT 2;\n\n\n|", "SELECT 2"},
		{pg, "\n\n|\nSELECT 1;", "SELECT 1"},
		{pg, "SELECT 1;\n-- next|\nSELECT 2;", "SELECT 1"},
		{pg, "SELECT\n  a,\n  |b\nFROM t;\nSELECT 2", "SELECT\n  a,\n  b\nFROM t"},
		{pg, "SELECT 1;\nSELECT 2|", "SELECT 2"},
		{pg, "|", ""},
		{pg, "-- only| comment", ""},
		{my, "DELIMITER //\nCREATE PROCEDURE p() BEGIN SELECT 1;| END//\nDELIMITER ;", "CREATE PROCEDURE p() BEGIN SELECT 1; END"},
		{my, "SELECT 1;\nDELIMITER //\n|\nSELECT 2//", "SELECT 1"},
	}
	for _, tt := range tests {
		off := strings.Index(tt.src, "|")
		src := tt.src[:off] + tt.src[off+1:]
		st, ok := StatementAt(src, off, tt.d)
		if tt.want == "" {
			if ok {
				t.Errorf("StatementAt(%q): got %q, want none", tt.src, st.SQL)
			}
			continue
		}
		if !ok || st.SQL != tt.want {
			t.Errorf("StatementAt(%q) = %q, %v; want %q", tt.src, st.SQL, ok, tt.want)
		}
	}
}

func TestLineCol(t *testing.T) {
	src := "ab\nçd\n\nx"
	tests := []struct{ off, line, col int }{
		{0, 1, 1}, {2, 1, 3}, {3, 2, 1}, {5, 2, 2}, {6, 2, 3}, {7, 3, 1}, {8, 4, 1}, {9, 4, 2}, {100, 4, 2}, {-1, 1, 1},
	}
	for _, tt := range tests {
		l, c := LineCol(src, tt.off)
		if l != tt.line || c != tt.col {
			t.Errorf("LineCol(%d) = %d:%d, want %d:%d", tt.off, l, c, tt.line, tt.col)
		}
		if tt.off >= 0 && tt.off <= len(src) && tt.off != 4 {
			if o := OffsetOf(src, l, c); o != tt.off {
				t.Errorf("OffsetOf(%d:%d) = %d, want %d", l, c, o, tt.off)
			}
		}
	}
	if o := OffsetOf(src, 1, 99); o != 2 {
		t.Errorf("OffsetOf clamps to line end: got %d", o)
	}
	if o := OffsetOf(src, 99, 1); o != len(src) {
		t.Errorf("OffsetOf past last line: got %d", o)
	}
}
