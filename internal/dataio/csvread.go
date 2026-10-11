package dataio

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/farvisun/squi/internal/db"
)

// CSVOptions controls CSV parsing.
type CSVOptions struct {
	// Delimiter separates fields; 0 auto-detects among , ; \t and |.
	Delimiter rune
	// Header treats the first record as column names. Otherwise names are
	// synthesized as col1..colN and the first record is data.
	Header bool
	// NullText is the unquoted field value that means NULL (e.g. `\N` or
	// "NULL"). Empty disables NULL conversion. Quoted fields are never NULL.
	NullText string
}

const sniffSize = 64 << 10

// CSVReader reads RFC 4180 CSV (lenient about stray quotes, CRLF, blank
// lines and a UTF-8 BOM) as rows of cells.
type CSVReader struct {
	br      *bufio.Reader
	delim   string
	null    string
	header  []string
	pending []db.Cell // first data row read while synthesizing the header
	line    int
}

type field struct {
	s      string
	quoted bool
}

// NewCSVReader reads the header (or the first record, when o.Header is
// false) and detects the delimiter if needed.
func NewCSVReader(r io.Reader, o CSVOptions) (*CSVReader, error) {
	br := bufio.NewReaderSize(r, sniffSize)
	if bom, _ := br.Peek(3); string(bom) == "\xef\xbb\xbf" {
		_, _ = br.Discard(3)
	}
	cr := &CSVReader{br: br, null: o.NullText, delim: string(o.Delimiter)}
	if o.Delimiter == 0 {
		sample, err := br.Peek(sniffSize)
		cr.delim = string(sniffDelimiter(string(sample), err != nil))
	}
	first, err := cr.record()
	if err == io.EOF {
		return cr, nil
	}
	if err != nil {
		return nil, err
	}
	cr.header = make([]string, len(first))
	for i, f := range first {
		cr.header[i] = strings.TrimSpace(f.s)
		if !o.Header || cr.header[i] == "" {
			cr.header[i] = "col" + strconv.Itoa(i+1)
		}
	}
	if !o.Header {
		cr.pending = cr.cells(first)
	}
	return cr, nil
}

// Header returns the column names.
func (r *CSVReader) Header() []string { return r.header }

// Delimiter returns the field delimiter in use.
func (r *CSVReader) Delimiter() rune { return []rune(r.delim)[0] }

// Next returns up to n rows, each padded with NULLs to the header width. It
// returns io.EOF (possibly with a final short batch) at the end of input.
func (r *CSVReader) Next(n int) ([][]db.Cell, error) {
	var rows [][]db.Cell
	if r.pending != nil {
		rows = append(rows, r.pending)
		r.pending = nil
	}
	for len(rows) < max(n, 1) {
		rec, err := r.record()
		if err != nil {
			return rows, err
		}
		for len(rec) > len(r.header) && rec[len(rec)-1] == (field{}) {
			rec = rec[:len(rec)-1] // trailing delimiters
		}
		if len(rec) > len(r.header) {
			return rows, fmt.Errorf("line %d: %d fields, expected %d", r.line, len(rec), len(r.header))
		}
		rows = append(rows, r.cells(rec))
	}
	return rows, nil
}

func (r *CSVReader) cells(rec []field) []db.Cell {
	out := make([]db.Cell, len(r.header))
	for i := range out {
		switch {
		case i >= len(rec):
			out[i] = db.NullCell
		case !rec[i].quoted && r.null != "" && rec[i].s == r.null:
			out[i] = db.NullCell
		default:
			out[i] = db.Text(rec[i].s)
		}
	}
	return out
}

func (r *CSVReader) readLine() (string, error) {
	s, err := r.br.ReadString('\n')
	if s == "" && err == nil {
		err = io.EOF
	}
	if err == io.EOF && s != "" {
		err = nil
	}
	if err == nil {
		r.line++
	}
	return s, err
}

// record reads one record, skipping blank lines; quoted fields may span
// lines.
func (r *CSVReader) record() ([]field, error) {
	var line string
	for {
		var err error
		if line, err = r.readLine(); err != nil {
			return nil, err
		}
		if strings.TrimRight(line, "\r\n") != "" {
			break
		}
	}
	start := r.line
	var rec []field
	i := 0
	for {
		if i < len(line) && line[i] == '"' {
			var b strings.Builder
			i++
			for {
				if i >= len(line) {
					next, err := r.readLine()
					if err == io.EOF {
						return nil, fmt.Errorf("line %d: unterminated quoted field", start)
					}
					if err != nil {
						return nil, err
					}
					line, i = next, 0
					continue
				}
				if line[i] == '"' {
					if i+1 < len(line) && line[i+1] == '"' {
						b.WriteByte('"')
						i += 2
						continue
					}
					i++
					break
				}
				b.WriteByte(line[i])
				i++
			}
			rest, j := r.until(line, i) // tolerate text after the closing quote
			b.WriteString(rest)
			rec, i = append(rec, field{b.String(), true}), j
		} else {
			s, j := r.until(line, i)
			rec, i = append(rec, field{s, false}), j
		}
		if i >= len(line) {
			return rec, nil
		}
		i += len(r.delim)
	}
}

// until returns the text from i up to the next delimiter or the line end
// (without the line break) and the index of that delimiter or len(line).
func (r *CSVReader) until(line string, i int) (string, int) {
	if j := strings.Index(line[i:], r.delim); j >= 0 {
		return line[i : i+j], i + j
	}
	return strings.TrimRight(line[i:], "\r\n"), len(line)
}

// sniffDelimiter picks the candidate that splits the sampled lines into the
// most consistent, then the largest, number of fields. complete reports
// whether sample holds the whole input (so its last line is not truncated).
func sniffDelimiter(sample string, complete bool) rune {
	cands := []byte{',', ';', '\t', '|'}
	var lines [][4]int
	var cur [4]int
	inQuote := false
	for i := 0; i < len(sample) && len(lines) < 50; i++ {
		switch c := sample[i]; {
		case c == '"':
			inQuote = !inQuote
		case inQuote:
		case c == '\n':
			if cur != ([4]int{}) {
				lines = append(lines, cur)
			}
			cur = [4]int{}
		default:
			for j, d := range cands {
				if c == d {
					cur[j]++
				}
			}
		}
	}
	if complete && cur != ([4]int{}) {
		lines = append(lines, cur)
	}
	best, bestScore := ',', -1
	for j, d := range cands {
		if len(lines) == 0 || lines[0][j] == 0 {
			continue
		}
		consistent := 0
		for _, l := range lines {
			if l[j] == lines[0][j] {
				consistent++
			}
		}
		if score := consistent*10000 + lines[0][j]; score > bestScore {
			best, bestScore = rune(d), score
		}
	}
	return best
}

// Preview reads the header and up to n rows of a CSV file.
func Preview(path string, o CSVOptions, n int) (header []string, rows [][]db.Cell, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	r, err := NewCSVReader(f, o)
	if err != nil {
		return nil, nil, err
	}
	rows, err = r.Next(n)
	if err == io.EOF {
		err = nil
	}
	return r.Header(), rows, err
}
