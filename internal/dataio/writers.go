package dataio

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/farvisun/squi/internal/db"
)

// valueKind is how a column's values are rendered in typed formats.
type valueKind int

const (
	kindString valueKind = iota
	kindNumber
	kindBool
	kindJSON
)

var numericTypes = map[string]bool{
	"int": true, "int2": true, "int4": true, "int8": true, "integer": true, "smallint": true,
	"bigint": true, "tinyint": true, "mediumint": true, "serial": true, "smallserial": true,
	"bigserial": true, "float": true, "float4": true, "float8": true, "double": true, "real": true,
	"numeric": true, "decimal": true, "dec": true, "fixed": true, "oid": true,
}

// kindOf maps an engine type name ("int4", "DECIMAL(10,2)", "BIGINT
// UNSIGNED", "double precision") to a value kind.
func kindOf(typ string) valueKind {
	t := strings.ToLower(strings.TrimSpace(typ))
	if i := strings.IndexAny(t, " ("); i >= 0 {
		t = t[:i]
	}
	switch {
	case numericTypes[t]:
		return kindNumber
	case t == "bool" || t == "boolean":
		return kindBool
	case t == "json" || t == "jsonb":
		return kindJSON
	}
	return kindString
}

func columnKinds(cols []db.ResultColumn) []valueKind {
	k := make([]valueKind, len(cols))
	for i, c := range cols {
		k[i] = kindOf(c.Type)
	}
	return k
}

// isJSONNumber reports whether s is a number in JSON syntax.
func isJSONNumber(s string) bool {
	if s == "" || (s[0] != '-' && (s[0] < '0' || s[0] > '9')) {
		return false
	}
	var n json.Number
	return json.Unmarshal([]byte(s), &n) == nil
}

func parseBool(s string) (v, ok bool) {
	switch strings.ToLower(s) {
	case "t", "true", "1", "y", "yes", "on":
		return true, true
	case "f", "false", "0", "n", "no", "off":
		return false, true
	}
	return false, false
}

// cell returns the i-th cell of row, or NULL for short rows.
func cell(row []db.Cell, i int) db.Cell {
	if i < len(row) {
		return row[i]
	}
	return db.NullCell
}

// delimitedWriter writes CSV/TSV with RFC 4180 quoting.
type delimitedWriter struct {
	bw    *bufio.Writer
	delim byte
	n     int
	null  string
}

func newDelimited(bw *bufio.Writer, delim byte, cols []db.ResultColumn, o Options) *delimitedWriter {
	w := &delimitedWriter{bw: bw, delim: delim, n: len(cols), null: o.NullText}
	if o.Header {
		for i, c := range cols {
			w.field(i, db.Text(c.Name))
		}
		w.bw.WriteByte('\n')
	}
	return w
}

func (w *delimitedWriter) field(i int, c db.Cell) {
	if i > 0 {
		w.bw.WriteByte(w.delim)
	}
	switch {
	case c.Null:
		w.bw.WriteString(w.null)
	case c.S == w.null || strings.ContainsAny(c.S, string(w.delim)+"\"\r\n"):
		w.bw.WriteByte('"')
		w.bw.WriteString(strings.ReplaceAll(c.S, `"`, `""`))
		w.bw.WriteByte('"')
	default:
		w.bw.WriteString(c.S)
	}
}

func (w *delimitedWriter) WriteRows(rows [][]db.Cell) error {
	for _, r := range rows {
		for i := 0; i < w.n; i++ {
			w.field(i, cell(r, i))
		}
		w.bw.WriteByte('\n')
	}
	return w.bw.Flush()
}

func (w *delimitedWriter) Close() error { return w.bw.Flush() }

// jsonWriter writes a JSON array of objects or JSON Lines.
type jsonWriter struct {
	bw    *bufio.Writer
	keys  []string // pre-encoded, de-duplicated object keys
	kinds []valueKind
	lines bool
	rows  int64
	buf   bytes.Buffer
	enc   *json.Encoder
}

func newJSONWriter(bw *bufio.Writer, cols []db.ResultColumn, lines bool) *jsonWriter {
	w := &jsonWriter{bw: bw, kinds: columnKinds(cols), lines: lines}
	w.enc = json.NewEncoder(&w.buf)
	w.enc.SetEscapeHTML(false)
	seen := map[string]int{}
	for _, c := range cols {
		name := c.Name
		if seen[name]++; seen[name] > 1 {
			name += "_" + strconv.Itoa(seen[name])
		}
		w.keys = append(w.keys, w.str(name))
	}
	if !lines {
		bw.WriteByte('[')
	}
	return w
}

// str JSON-encodes s without HTML escaping.
func (w *jsonWriter) str(s string) string {
	w.buf.Reset()
	_ = w.enc.Encode(s)
	return strings.TrimSuffix(w.buf.String(), "\n")
}

func (w *jsonWriter) value(k valueKind, c db.Cell) string {
	if c.Null {
		return "null"
	}
	switch k {
	case kindNumber:
		if isJSONNumber(c.S) {
			return c.S
		}
	case kindBool:
		if v, ok := parseBool(c.S); ok {
			return strconv.FormatBool(v)
		}
	case kindJSON:
		w.buf.Reset()
		if json.Compact(&w.buf, []byte(c.S)) == nil {
			return w.buf.String()
		}
	}
	return w.str(c.S)
}

func (w *jsonWriter) WriteRows(rows [][]db.Cell) error {
	for _, r := range rows {
		switch {
		case w.lines:
		case w.rows == 0:
			w.bw.WriteString("\n  ")
		default:
			w.bw.WriteString(",\n  ")
		}
		w.bw.WriteByte('{')
		for i, k := range w.keys {
			if i > 0 {
				w.bw.WriteByte(',')
			}
			w.bw.WriteString(k)
			w.bw.WriteByte(':')
			w.bw.WriteString(w.value(w.kinds[i], cell(r, i)))
		}
		w.bw.WriteByte('}')
		if w.lines {
			w.bw.WriteByte('\n')
		}
		w.rows++
	}
	return w.bw.Flush()
}

func (w *jsonWriter) Close() error {
	if !w.lines {
		if w.rows > 0 {
			w.bw.WriteByte('\n')
		}
		w.bw.WriteString("]\n")
	}
	return w.bw.Flush()
}

// sqlWriter writes batched multi-row INSERT statements.
type sqlWriter struct {
	bw      *bufio.Writer
	cols    []db.ResultColumn
	kinds   []valueKind
	o       Options
	inBatch int
}

func (w *sqlWriter) WriteRows(rows [][]db.Cell) error {
	d := w.o.Dialect
	for _, r := range rows {
		if w.inBatch == 0 {
			w.bw.WriteString("INSERT INTO " + d.Qualified(w.o.Table) + " (")
			for i, c := range w.cols {
				if i > 0 {
					w.bw.WriteString(", ")
				}
				w.bw.WriteString(d.QuoteIdent(c.Name))
			}
			w.bw.WriteString(") VALUES\n  (")
		} else {
			w.bw.WriteString(",\n  (")
		}
		for i, k := range w.kinds {
			if i > 0 {
				w.bw.WriteString(", ")
			}
			c := cell(r, i)
			if k == kindNumber && !c.Null && isJSONNumber(c.S) {
				w.bw.WriteString(c.S)
			} else {
				w.bw.WriteString(d.Value(c))
			}
		}
		w.bw.WriteByte(')')
		if w.inBatch++; w.inBatch == w.o.BatchRows {
			w.endBatch()
		}
	}
	return w.bw.Flush()
}

func (w *sqlWriter) endBatch() {
	if w.inBatch > 0 {
		w.bw.WriteString(";\n")
		w.inBatch = 0
	}
}

func (w *sqlWriter) Close() error {
	w.endBatch()
	return w.bw.Flush()
}

// markdownWriter writes a GitHub-flavoured Markdown table.
type markdownWriter struct {
	bw   *bufio.Writer
	n    int
	null string
}

var mdEscaper = strings.NewReplacer(`\`, `\\`, "|", `\|`, "\r\n", "<br>", "\n", "<br>", "\r", "<br>")

func newMarkdown(bw *bufio.Writer, cols []db.ResultColumn, o Options) *markdownWriter {
	w := &markdownWriter{bw: bw, n: len(cols), null: o.NullText}
	if w.null == "" {
		w.null = "NULL"
	}
	if w.n == 0 {
		return w
	}
	names := make([]db.Cell, len(cols))
	for i, c := range cols {
		names[i] = db.Text(c.Name)
	}
	w.row(names)
	w.bw.WriteString("|" + strings.Repeat(" --- |", w.n) + "\n")
	return w
}

func (w *markdownWriter) row(r []db.Cell) {
	w.bw.WriteByte('|')
	for i := 0; i < w.n; i++ {
		c := cell(r, i)
		s := w.null
		if !c.Null {
			s = mdEscaper.Replace(c.S)
		}
		w.bw.WriteString(" " + s + " |")
	}
	w.bw.WriteByte('\n')
}

func (w *markdownWriter) WriteRows(rows [][]db.Cell) error {
	for _, r := range rows {
		w.row(r)
	}
	return w.bw.Flush()
}

func (w *markdownWriter) Close() error { return w.bw.Flush() }
