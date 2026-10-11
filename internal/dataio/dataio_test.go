package dataio

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/farvisun/squi/internal/db"
)

var (
	testCols = []db.ResultColumn{
		{Name: "id", Type: "int4"}, {Name: "name", Type: "text"}, {Name: "ok", Type: "bool"},
		{Name: "doc", Type: "jsonb"}, {Name: "amount", Type: "NUMERIC(10,2)"},
	}
	testRows = [][]db.Cell{
		{db.Text("1"), db.Text("plain"), db.Text("t"), db.Text(`{"a": [1, 2]}`), db.Text("1.50")},
		{db.Text("2"), db.Text("a,b \"q\"\nline|pipe"), db.Text("f"), db.Text("not json"), db.Text("NaN")},
		{db.NullCell, db.Text(""), db.NullCell, db.NullCell, db.NullCell},
	}
)

func TestCSV(t *testing.T) {
	got := FormatRows(FormatCSV, testCols, testRows, DefaultOptions())
	want := "id,name,ok,doc,amount\n" +
		`1,plain,t,"{""a"": [1, 2]}",1.50` + "\n" +
		`2,"a,b ""q""` + "\n" + `line|pipe",f,not json,NaN` + "\n" +
		`,"",,,` + "\n"
	if got != want {
		t.Errorf("CSV:\n got: %q\nwant: %q", got, want)
	}

	o := DefaultOptions()
	o.NullText, o.Header = `\N`, false
	got = FormatRows(FormatCSV, testCols[:2], [][]db.Cell{{db.NullCell, db.Text(`\N`)}, {db.Text(""), db.Text("x")}}, o)
	if want := `\N,"\N"` + "\n,x\n"; got != want {
		t.Errorf("CSV NullText:\n got: %q\nwant: %q", got, want)
	}
}

func TestTSV(t *testing.T) {
	got := FormatRows(FormatTSV, testCols[:2], [][]db.Cell{{db.Text("1"), db.Text("a\tb")}, {db.Text("2"), db.Text("a,b")}}, DefaultOptions())
	if want := "id\tname\n1\t\"a\tb\"\n2\ta,b\n"; got != want {
		t.Errorf("TSV:\n got: %q\nwant: %q", got, want)
	}
}

func TestJSON(t *testing.T) {
	got := FormatRows(FormatJSON, testCols, testRows, DefaultOptions())
	want := `[
  {"id":1,"name":"plain","ok":true,"doc":{"a":[1,2]},"amount":1.50},
  {"id":2,"name":"a,b \"q\"\nline|pipe","ok":false,"doc":"not json","amount":"NaN"},
  {"id":null,"name":"","ok":null,"doc":null,"amount":null}
]
`
	if got != want {
		t.Errorf("JSON:\n got: %s\nwant: %s", got, want)
	}
	var v []map[string]any
	if err := json.Unmarshal([]byte(got), &v); err != nil || len(v) != 3 {
		t.Errorf("invalid JSON: %v", err)
	}
	if got := FormatRows(FormatJSON, testCols, nil, DefaultOptions()); got != "[]\n" {
		t.Errorf("empty JSON = %q", got)
	}
	dup := []db.ResultColumn{{Name: "id"}, {Name: "id"}, {Name: "<b>"}}
	got = FormatRows(FormatJSON, dup, [][]db.Cell{{db.Text("1"), db.Text("2"), db.Text("<&>")}}, DefaultOptions())
	if want := "[\n  {\"id\":\"1\",\"id_2\":\"2\",\"<b>\":\"<&>\"}\n]\n"; got != want {
		t.Errorf("JSON dup keys:\n got: %q\nwant: %q", got, want)
	}
}

func TestJSONL(t *testing.T) {
	got := FormatRows(FormatJSONL, testCols[:3], testRows[:2], DefaultOptions())
	want := `{"id":1,"name":"plain","ok":true}` + "\n" + `{"id":2,"name":"a,b \"q\"\nline|pipe","ok":false}` + "\n"
	if got != want {
		t.Errorf("JSONL:\n got: %q\nwant: %q", got, want)
	}
}

func TestSQL(t *testing.T) {
	o := DefaultOptions()
	o.BatchRows = 2
	got := FormatRows(FormatSQL, testCols[:3], testRows, o)
	want := `INSERT INTO "export" ("id", "name", "ok") VALUES
  (1, 'plain', 't'),
  (2, 'a,b "q"
line|pipe', 'f');
INSERT INTO "export" ("id", "name", "ok") VALUES
  (NULL, '', NULL);
`
	if got != want {
		t.Errorf("SQL pg:\n got: %s\nwant: %s", got, want)
	}

	o = Options{Dialect: db.Dialect{Driver: db.MySQL}, Table: db.ObjectRef{Database: "shop", Name: "t"}}
	got = FormatRows(FormatSQL, testCols[:2], [][]db.Cell{{db.Text("x"), db.Text(`it's \ ok`)}}, o)
	want = "INSERT INTO `shop`.`t` (`id`, `name`) VALUES\n  ('x', 'it''s \\\\ ok');\n"
	if got != want {
		t.Errorf("SQL mysql:\n got: %s\nwant: %s", got, want)
	}
	if got := FormatRows(FormatSQL, testCols, nil, o); got != "" {
		t.Errorf("empty SQL = %q", got)
	}
}

func TestMarkdown(t *testing.T) {
	got := FormatRows(FormatMarkdown, testCols[:2], testRows, DefaultOptions())
	want := "| id | name |\n| --- | --- |\n| 1 | plain |\n| 2 | a,b \"q\"<br>line\\|pipe |\n| NULL |  |\n"
	if got != want {
		t.Errorf("Markdown:\n got: %q\nwant: %q", got, want)
	}
}

func TestFormatFromPath(t *testing.T) {
	for path, want := range map[string]Format{"a.CSV": FormatCSV, "x/y.tsv": FormatTSV, "a.json": FormatJSON,
		"a.ndjson": FormatJSONL, "a.jsonl": FormatJSONL, "dump.sql": FormatSQL, "README.md": FormatMarkdown} {
		if got, ok := FormatFromPath(path); !ok || got != want {
			t.Errorf("FormatFromPath(%q) = %q, %v", path, got, ok)
		}
	}
	if _, ok := FormatFromPath("a.xlsx"); ok {
		t.Error("xlsx should be unknown")
	}
	for _, f := range Formats {
		if got, ok := FormatFromPath("x" + f.Ext()); !ok || got != f {
			t.Errorf("Ext round trip for %s", f)
		}
	}
	if _, err := NewWriter(io.Discard, "xml", nil, DefaultOptions()); err == nil {
		t.Error("expected error for unknown format")
	}
}

type fakeStream struct {
	cols []db.ResultColumn
	rows [][]db.Cell
	err  error
}

func (s *fakeStream) Columns() []db.ResultColumn { return s.cols }
func (s *fakeStream) Next(n int) ([][]db.Cell, error) {
	if len(s.rows) == 0 {
		if s.err != nil {
			return nil, s.err
		}
		return nil, io.EOF
	}
	k := min(n, len(s.rows))
	out := s.rows[:k]
	s.rows = s.rows[k:]
	return out, nil
}
func (s *fakeStream) RowsAffected() int64 { return 0 }
func (s *fakeStream) CommandTag() string  { return "" }
func (s *fakeStream) Close() error        { return nil }

func TestExport(t *testing.T) {
	rows := make([][]db.Cell, 2500)
	for i := range rows {
		rows[i] = []db.Cell{db.Text("v")}
	}
	var b strings.Builder
	var progress []int64
	n, err := Export(context.Background(), &fakeStream{cols: []db.ResultColumn{{Name: "c"}}, rows: rows}, &b, FormatCSV,
		DefaultOptions(), func(r int64) { progress = append(progress, r) })
	if err != nil || n != 2500 {
		t.Fatalf("Export = %d, %v", n, err)
	}
	if !reflect.DeepEqual(progress, []int64{1000, 2000, 2500}) {
		t.Errorf("progress = %v", progress)
	}
	if strings.Count(b.String(), "\n") != 2501 {
		t.Errorf("unexpected line count")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Export(ctx, &fakeStream{rows: rows}, io.Discard, FormatCSV, DefaultOptions(), nil); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled export err = %v", err)
	}
	boom := errors.New("boom")
	if _, err := Export(context.Background(), &fakeStream{err: boom}, io.Discard, FormatJSON, DefaultOptions(), nil); !errors.Is(err, boom) {
		t.Errorf("stream error = %v", err)
	}
}

func readAll(t *testing.T, src string, o CSVOptions) ([]string, [][]db.Cell) {
	t.Helper()
	r, err := NewCSVReader(strings.NewReader(src), o)
	if err != nil {
		t.Fatal(err)
	}
	var all [][]db.Cell
	for {
		rows, err := r.Next(2)
		all = append(all, rows...)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	return r.Header(), all
}

func texts(ss ...string) []db.Cell {
	out := make([]db.Cell, len(ss))
	for i, s := range ss {
		if s == "<null>" {
			out[i] = db.NullCell
		} else {
			out[i] = db.Text(s)
		}
	}
	return out
}

func TestCSVReader(t *testing.T) {
	tests := []struct {
		name   string
		src    string
		o      CSVOptions
		header []string
		rows   [][]db.Cell
	}{
		{"basic", "a,b\n1,2\n3,4\n", CSVOptions{Header: true}, []string{"a", "b"},
			[][]db.Cell{texts("1", "2"), texts("3", "4")}},
		{"quotes and newlines", "a,b\r\n\"x,\"\"y\"\"\",\"multi\r\nline\"\r\n\r\n\"\",z", CSVOptions{Header: true}, []string{"a", "b"},
			[][]db.Cell{texts(`x,"y"`, "multi\r\nline"), texts("", "z")}},
		{"bom and no header", "\xef\xbb\xbf1;2\n3;4", CSVOptions{}, []string{"col1", "col2"},
			[][]db.Cell{texts("1", "2"), texts("3", "4")}},
		{"sniff tab", "a\tb\tc,d\n1\t2\t3,4\n", CSVOptions{Header: true}, []string{"a", "b", "c,d"},
			[][]db.Cell{texts("1", "2", "3,4")}},
		{"sniff consistency beats count", "a;b,c,d\n1;2\n3;4,5\n", CSVOptions{Header: true}, []string{"a", "b,c,d"},
			[][]db.Cell{texts("1", "2"), texts("3", "4,5")}},
		{"sniff pipe", "a|b|c\n1|2|3\n", CSVOptions{Header: true}, []string{"a", "b", "c"},
			[][]db.Cell{texts("1", "2", "3")}},
		{"sniff ignores quoted", "\"a,b\";c\n\"1,2\";3\n", CSVOptions{Header: true}, []string{"a,b", "c"},
			[][]db.Cell{texts("1,2", "3")}},
		{"explicit delimiter", "a;b\n1;2\n", CSVOptions{Delimiter: ',', Header: true}, []string{"a;b"},
			[][]db.Cell{texts("1;2")}},
		{"null text", "a,b\n\\N,\"\\N\"\n,x\n", CSVOptions{Header: true, NullText: `\N`}, []string{"a", "b"},
			[][]db.Cell{texts("<null>", `\N`), texts("", "x")}},
		{"NULL word", "a,b\nNULL,null\n", CSVOptions{Header: true, NullText: "NULL"}, []string{"a", "b"},
			[][]db.Cell{texts("<null>", "null")}},
		{"no null conversion", "a\n\n\"\"\nx\n", CSVOptions{Header: true}, []string{"a"},
			[][]db.Cell{texts(""), texts("x")}},
		{"short rows padded, trailing delimiter", "a,b,c\n1\n1,2,3,\n", CSVOptions{Header: true}, []string{"a", "b", "c"},
			[][]db.Cell{texts("1", "<null>", "<null>"), texts("1", "2", "3")}},
		{"empty header name", "a,,c\n1,2,3\n", CSVOptions{Header: true}, []string{"a", "col2", "c"},
			[][]db.Cell{texts("1", "2", "3")}},
		{"empty input", "", CSVOptions{Header: true}, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, rows := readAll(t, tt.src, tt.o)
			if !reflect.DeepEqual(h, tt.header) {
				t.Errorf("header = %q, want %q", h, tt.header)
			}
			if !reflect.DeepEqual(rows, tt.rows) {
				t.Errorf("rows = %+v, want %+v", rows, tt.rows)
			}
		})
	}
}

func TestCSVReaderErrors(t *testing.T) {
	r, err := NewCSVReader(strings.NewReader("a,b\n1,2,3\n"), CSVOptions{Header: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Next(10); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("too many fields err = %v", err)
	}
	r, _ = NewCSVReader(strings.NewReader("a\n\"open\n"), CSVOptions{Header: true})
	if _, err := r.Next(10); err == nil || !strings.Contains(err.Error(), "unterminated") {
		t.Errorf("unterminated err = %v", err)
	}
}

func TestCSVRoundTrip(t *testing.T) {
	o := DefaultOptions()
	o.NullText = `\N`
	out := FormatRows(FormatCSV, testCols, testRows, o)
	h, rows := readAll(t, out, CSVOptions{Header: true, NullText: `\N`})
	if len(h) != len(testCols) || !reflect.DeepEqual(rows, testRows) {
		t.Errorf("round trip mismatch:\n%+v\n%+v", rows, testRows)
	}
}

func TestPreview(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.csv")
	if err := os.WriteFile(path, []byte("id;name\n1;a\n2;b\n3;c\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, rows, err := Preview(path, CSVOptions{Header: true}, 2)
	if err != nil || !reflect.DeepEqual(h, []string{"id", "name"}) || len(rows) != 2 {
		t.Errorf("Preview = %q, %+v, %v", h, rows, err)
	}
	if _, _, err := Preview(path+".missing", CSVOptions{}, 1); err == nil {
		t.Error("expected error for missing file")
	}
}
