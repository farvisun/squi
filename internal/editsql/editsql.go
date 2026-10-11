// Package editsql turns staged result-grid edits into DML statements.
package editsql

import (
	"errors"
	"fmt"
	"strings"

	"github.com/farvisun/squi/internal/db"
)

// ChangeKind is the kind of a staged row change.
type ChangeKind int

const (
	Update ChangeKind = iota
	Insert
	Delete
)

func (k ChangeKind) String() string {
	switch k {
	case Update:
		return "update"
	case Insert:
		return "insert"
	case Delete:
		return "delete"
	}
	return "unknown"
}

// Change is one staged row change.
type Change struct {
	Kind    ChangeKind
	KeyCols []string  // primary/unique key columns identifying the row (Update, Delete)
	KeyVals []db.Cell // original key values
	// Cols are the columns to set: for Update the changed columns, for Insert
	// the columns the user provided (untouched columns are omitted so their
	// defaults apply).
	Cols []string
	Vals []db.Cell
}

// Errors returned by Generate (wrapped with the change position).
var (
	ErrNoTable   = errors.New("no target table")
	ErrNoKey     = errors.New("row has no key; cannot identify it")
	ErrNullKey   = errors.New("key value is NULL; cannot identify the row")
	ErrNoColumns = errors.New("update changes no columns")
	ErrMismatch  = errors.New("column and value counts differ")
)

// Generate returns one statement per change, in the given order, without
// trailing semicolons. Values are rendered as quoted literals (Postgres
// resolves them against the column type, MySQL coerces). MySQL UPDATE and
// DELETE statements get LIMIT 1 as a safety net, SQL Server ones TOP (1).
func Generate(d db.Dialect, table db.ObjectRef, changes []Change) ([]string, error) {
	if table.Name == "" {
		return nil, ErrNoTable
	}
	target := d.Qualified(table)
	out := make([]string, 0, len(changes))
	for i, c := range changes {
		s, err := generate(d, target, c)
		if err != nil {
			return nil, fmt.Errorf("change %d (%s): %w", i+1, c.Kind, err)
		}
		out = append(out, s)
	}
	return out, nil
}

func generate(d db.Dialect, target string, c Change) (string, error) {
	if len(c.Cols) != len(c.Vals) {
		return "", ErrMismatch
	}
	var b strings.Builder
	switch c.Kind {
	case Insert:
		b.WriteString("INSERT INTO " + target)
		switch {
		case len(c.Cols) > 0:
			b.WriteString(" (")
			for i, col := range c.Cols {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(d.QuoteIdent(col))
			}
			b.WriteString(") VALUES (")
			for i, v := range c.Vals {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(d.Value(v))
			}
			b.WriteString(")")
		case d.Driver == db.MySQL:
			b.WriteString(" () VALUES ()")
		default:
			b.WriteString(" DEFAULT VALUES")
		}
		return b.String(), nil
	case Update:
		if len(c.Cols) == 0 {
			return "", ErrNoColumns
		}
		b.WriteString("UPDATE " + top(d) + target + " SET ")
		for i, col := range c.Cols {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(d.QuoteIdent(col) + " = " + d.Value(c.Vals[i]))
		}
	case Delete:
		b.WriteString("DELETE " + top(d) + "FROM " + target)
	default:
		return "", fmt.Errorf("unknown change kind %d", c.Kind)
	}
	where, err := whereKey(d, c)
	if err != nil {
		return "", err
	}
	b.WriteString(where)
	if d.Driver == db.MySQL {
		b.WriteString(" LIMIT 1")
	}
	return b.String(), nil
}

func top(d db.Dialect) string {
	if d.Driver == db.SQLServer {
		return "TOP (1) "
	}
	return ""
}

// whereKey renders " WHERE k1 = v1 AND k2 = v2" for the row's key.
func whereKey(d db.Dialect, c Change) (string, error) {
	if len(c.KeyCols) == 0 {
		return "", ErrNoKey
	}
	if len(c.KeyCols) != len(c.KeyVals) {
		return "", ErrMismatch
	}
	var b strings.Builder
	b.WriteString(" WHERE ")
	for i, col := range c.KeyCols {
		if c.KeyVals[i].Null {
			return "", fmt.Errorf("%w (%s)", ErrNullKey, col)
		}
		if i > 0 {
			b.WriteString(" AND ")
		}
		b.WriteString(d.QuoteIdent(col) + " = " + d.Value(c.KeyVals[i]))
	}
	return b.String(), nil
}
