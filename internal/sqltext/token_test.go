package sqltext

import (
	"fmt"
	"strings"
	"testing"

	"github.com/farvisun/squi/internal/db"
)

const (
	pg = db.Postgres
	my = db.MySQL
)

// sig renders the non-whitespace tokens as "kind:text" for compact asserts.
func sig(src string, d db.DriverName) string {
	var parts []string
	for _, t := range Tokenize(src, d) {
		if t.Kind != TokWhitespace {
			parts = append(parts, fmt.Sprintf("%s:%s", t.Kind, t.Text))
		}
	}
	return strings.Join(parts, " ")
}

func TestTokenize(t *testing.T) {
	tests := []struct {
		d    db.DriverName
		src  string
		want string
	}{
		{pg, "SELECT a FROM t", "keyword:SELECT ident:a keyword:FROM ident:t"},
		{pg, "select t.user from t", "keyword:select ident:t punct:. ident:user keyword:from ident:t"},
		{pg, "a -- c\nb", "ident:a comment:-- c ident:b"},
		{my, "a -- c\nb", "ident:a comment:-- c ident:b"},
		{my, "a --c", "ident:a operator:- operator:- ident:c"},
		{pg, "a --c", "ident:a comment:--c"},
		{my, "a # c\nb", "ident:a comment:# c ident:b"},
		{pg, "a #> b", "ident:a operator:#> ident:b"},
		{pg, "/* a /* nested */ still */ x", "comment:/* a /* nested */ still */ ident:x"},
		{my, "/* a /* b */ x */", "comment:/* a /* b */ ident:x operator:*/"},
		{my, "/*!40101 SET NAMES utf8 */", "comment:/*!40101 SET NAMES utf8 */"},
		{pg, "'it''s' x", "string:'it''s' ident:x"},
		{pg, `'a\' b`, `string:'a\' ident:b`},
		{my, `'a\' b' c`, `string:'a\' b' ident:c`},
		{pg, `E'a\'b' c`, `string:E'a\'b' ident:c`},
		{pg, `e'x' y`, `string:e'x' ident:y`},
		{pg, "$$ a ; ' b $$ c", "string:$$ a ; ' b $$ ident:c"},
		{pg, "$fn$ body $$ inner $$ $fn$;", "string:$fn$ body $$ inner $$ $fn$ punct:;"},
		{pg, "$1 + $23", "param:$1 operator:+ param:$23"},
		{pg, "a$b", "ident:a$b"},
		{pg, `"My Table"."Col"`, `quoted-ident:"My Table" punct:. quoted-ident:"Col"`},
		{pg, `"a""b"`, `quoted-ident:"a""b"`},
		{my, "`a``b`.`c`", "quoted-ident:`a``b` punct:. quoted-ident:`c`"},
		{my, `"str" x`, `string:"str" ident:x`},
		{pg, "1 2.5 .5 1e10 1.5E-3 0xFF", "number:1 number:2.5 number:.5 number:1e10 number:1.5E-3 number:0xFF"},
		{pg, "a::int", "ident:a operator::: ident:int"},
		{pg, ":name = :other", "param::name operator:= param::other"},
		{pg, "a::text", "ident:a operator::: ident:text"},
		{my, "a = ?", "ident:a operator:= param:?"},
		{pg, "a ? 'k'", "ident:a operator:? string:'k'"},
		{my, "@x := @@session.y", "ident:@x operator::= ident:@@session punct:. ident:y"},
		{pg, "a <> b != c >= d", "ident:a operator:<> ident:b operator:!= ident:c operator:>= ident:d"},
		{pg, "a=-1", "ident:a operator:= operator:- number:1"},
		{pg, "j->>'k'", "ident:j operator:->> string:'k'"},
		{pg, "a||b", "ident:a operator:|| ident:b"},
		{pg, "f(a, b);", "ident:f punct:( ident:a punct:, ident:b punct:) punct:;"},
		{pg, "x[1]", "ident:x punct:[ number:1 punct:]"},
		{pg, "'unterminated", "string:'unterminated"},
		{pg, "/* open", "comment:/* open"},
		{pg, "$$ open", "string:$$ open"},
		{pg, "U&'d\\0061t' U&\"x\"", `string:U&'d\0061t' quoted-ident:U&"x"`},
		{my, "X'0A' N'abc' B'01'", "string:X'0A' string:N'abc' string:B'01'"},
		{my, "DELIMITER //\nSELECT 1//", "delimiter:DELIMITER // keyword:SELECT number:1 delimiter://"},
		{my, "delimiter $$\nx;y$$\ndelimiter ;\nz;", "delimiter:delimiter $$ ident:x punct:; ident:y delimiter:$$ delimiter:delimiter ; ident:z punct:;"},
		{my, "SELECT 'DELIMITER //'", "keyword:SELECT string:'DELIMITER //'"},
		{pg, "DELIMITER //", "ident:DELIMITER operator://"},
		{pg, "café", "ident:café"},
	}
	for _, tt := range tests {
		if got := sig(tt.src, tt.d); got != tt.want {
			t.Errorf("Tokenize(%q, %s)\n got: %s\nwant: %s", tt.src, tt.d, got, tt.want)
		}
	}
}

func TestTokenizeRoundTrip(t *testing.T) {
	srcs := []string{
		"SELECT 'a''b', \"c\", $x$ y $x$ -- z\n/* w */;",
		"DELIMITER //\nCREATE PROCEDURE p() BEGIN SELECT 1; END//\nDELIMITER ;",
		"日本 = 'ü' ; `x`",
	}
	for _, s := range srcs {
		for _, d := range []db.DriverName{pg, my} {
			var b strings.Builder
			pos := 0
			for _, tk := range Tokenize(s, d) {
				if tk.Start != pos || s[tk.Start:tk.End] != tk.Text {
					t.Fatalf("bad offsets for %q: %+v", s, tk)
				}
				pos = tk.End
				b.WriteString(tk.Text)
			}
			if b.String() != s {
				t.Errorf("round trip %s: got %q want %q", d, b.String(), s)
			}
		}
	}
}

func TestKeywords(t *testing.T) {
	if !IsKeyword("select", pg) || !IsKeyword("RETURNING", pg) || IsKeyword("RETURNING", my) || !IsKeyword("use", my) {
		t.Error("IsKeyword mismatch")
	}
	kw := Keywords(my)
	for i := 1; i < len(kw); i++ {
		if kw[i-1] >= kw[i] {
			t.Fatalf("Keywords not sorted/unique at %q", kw[i])
		}
	}
}
