// Package dataio exports result sets to files and clipboard text (CSV, TSV,
// JSON, JSON Lines, SQL INSERTs, Markdown) and reads CSV files for import.
package dataio

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/farvisun/squi/internal/db"
)

// Format is an export format.
type Format string

const (
	FormatCSV      Format = "csv"
	FormatTSV      Format = "tsv"
	FormatJSON     Format = "json"  // array of objects
	FormatJSONL    Format = "jsonl" // one object per line (NDJSON)
	FormatSQL      Format = "sql"   // multi-row INSERT statements
	FormatMarkdown Format = "markdown"
)

// Formats lists the supported formats in display order.
var Formats = []Format{FormatCSV, FormatTSV, FormatJSON, FormatJSONL, FormatSQL, FormatMarkdown}

// Ext returns the conventional file extension of f, including the dot.
func (f Format) Ext() string {
	if f == FormatMarkdown {
		return ".md"
	}
	return "." + string(f)
}

// FormatFromPath infers the format from a file extension.
func FormatFromPath(path string) (Format, bool) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".csv":
		return FormatCSV, true
	case ".tsv", ".tab":
		return FormatTSV, true
	case ".json":
		return FormatJSON, true
	case ".jsonl", ".ndjson":
		return FormatJSONL, true
	case ".sql":
		return FormatSQL, true
	case ".md", ".markdown":
		return FormatMarkdown, true
	}
	return "", false
}

// Options controls export output.
type Options struct {
	// Dialect formats identifiers and literals of SQL output.
	Dialect db.Dialect
	// Table is the INSERT target of SQL output; an empty name means "export".
	Table db.ObjectRef
	// NullText is written for NULL in CSV/TSV (default empty, unquoted; an
	// empty string is then written as "" to stay distinguishable) and in
	// Markdown (where empty means "NULL").
	NullText string
	// Header writes a header row in CSV/TSV.
	Header bool
	// BatchRows is the number of rows per INSERT statement (default 100).
	BatchRows int
}

// DefaultOptions returns Postgres-flavoured options with a header row.
func DefaultOptions() Options {
	return Options{Dialect: db.Dialect{Driver: db.Postgres}, Header: true, BatchRows: 100}
}

func (o Options) withDefaults() Options {
	if o.BatchRows <= 0 {
		o.BatchRows = 100
	}
	if o.Table.Name == "" {
		o.Table = db.ObjectRef{Name: "export"}
	}
	return o
}

// RowWriter writes rows in some format.
type RowWriter interface {
	WriteRows(rows [][]db.Cell) error
	// Close writes any footer and flushes buffered output. It does not
	// close the underlying writer.
	Close() error
}

// NewWriter returns a RowWriter for format f. Any header is written
// immediately, so even an empty result yields valid output.
func NewWriter(w io.Writer, f Format, cols []db.ResultColumn, o Options) (RowWriter, error) {
	o = o.withDefaults()
	bw := bufio.NewWriter(w)
	var rw RowWriter
	switch f {
	case FormatCSV:
		rw = newDelimited(bw, ',', cols, o)
	case FormatTSV:
		rw = newDelimited(bw, '\t', cols, o)
	case FormatJSON, FormatJSONL:
		rw = newJSONWriter(bw, cols, f == FormatJSONL)
	case FormatSQL:
		rw = &sqlWriter{bw: bw, cols: cols, o: o, kinds: columnKinds(cols)}
	case FormatMarkdown:
		rw = newMarkdown(bw, cols, o)
	default:
		return nil, fmt.Errorf("unsupported export format %q", f)
	}
	return rw, nil
}

// Export streams rs to w until the result is exhausted, calling progress
// (if non-nil) with the running row count after each batch. It does not
// close rs.
func Export(ctx context.Context, rs db.ResultStream, w io.Writer, f Format, o Options, progress func(rows int64)) (int64, error) {
	rw, err := NewWriter(w, f, rs.Columns(), o)
	if err != nil {
		return 0, err
	}
	var n int64
	for {
		if err := ctx.Err(); err != nil {
			return n, err
		}
		rows, err := rs.Next(1000)
		if len(rows) > 0 {
			if werr := rw.WriteRows(rows); werr != nil {
				return n, werr
			}
			n += int64(len(rows))
			if progress != nil {
				progress(n)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return n, err
		}
	}
	return n, rw.Close()
}

// FormatRows renders rows in memory, e.g. for copying to the clipboard.
// Unknown formats fall back to TSV.
func FormatRows(f Format, cols []db.ResultColumn, rows [][]db.Cell, o Options) string {
	var b strings.Builder
	rw, err := NewWriter(&b, f, cols, o)
	if err != nil {
		rw, _ = NewWriter(&b, FormatTSV, cols, o)
	}
	_ = rw.WriteRows(rows) // strings.Builder never fails
	_ = rw.Close()
	return b.String()
}
