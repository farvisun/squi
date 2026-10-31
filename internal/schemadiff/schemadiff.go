// Package schemadiff compares the tables and views of two schemas and writes
// the statements that make the target match the source.
package schemadiff

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/farvisun/squi/internal/db"
)

// Schema is a loaded snapshot of one schema's tables and views.
type Schema struct {
	Driver   db.DriverName
	Database string
	Schema   string
	Tables   map[string]*Table
}

// Table is a table or view with its details.
type Table struct {
	Name   string
	Kind   db.ObjectKind
	Detail *db.TableDetail
	DDL    string // CREATE statement, loaded on demand for tables missing in the target
}

// Load reads every table and view of database.schema. For engines without
// schemas, schema is the database (MySQL, ClickHouse) or "main" (SQLite).
func Load(ctx context.Context, conn db.Conn, database, schema string) (*Schema, error) {
	objs, err := conn.Objects(ctx, database, schema)
	if err != nil {
		return nil, err
	}
	s := &Schema{Driver: conn.Dialect().Driver, Database: database, Schema: schema, Tables: map[string]*Table{}}
	for _, o := range objs {
		if o.Kind != db.KindTable && o.Kind != db.KindView && o.Kind != db.KindMatView {
			continue
		}
		ref := o.Ref(database)
		d, err := conn.TableDetail(ctx, ref)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", o.Name, err)
		}
		s.Tables[o.Name] = &Table{Name: o.Name, Kind: o.Kind, Detail: d}
	}
	return s, nil
}

// LoadDDL fills in the CREATE statements of the source tables the target lacks.
func LoadDDL(ctx context.Context, conn db.Conn, src *Schema, changes []Change) error {
	for _, c := range changes {
		if c.Kind != MissingTable {
			continue
		}
		t := src.Tables[c.Table]
		ddl, err := conn.DDL(ctx, db.ObjectRef{Database: src.Database, Schema: src.Schema, Name: t.Name, Kind: t.Kind})
		if err != nil {
			return fmt.Errorf("DDL of %s: %w", t.Name, err)
		}
		t.DDL = ddl
	}
	return nil
}

// ChangeKind classifies a difference, seen from the target.
type ChangeKind string

const (
	MissingTable   ChangeKind = "missing table"
	ExtraTable     ChangeKind = "extra table"
	MissingColumn  ChangeKind = "missing column"
	ExtraColumn    ChangeKind = "extra column"
	ColumnType     ChangeKind = "column type"
	ColumnNull     ChangeKind = "nullability"
	ColumnDefault  ChangeKind = "default"
	PrimaryKey     ChangeKind = "primary key"
	MissingIndex   ChangeKind = "missing index"
	ExtraIndex     ChangeKind = "extra index"
	IndexDiffers   ChangeKind = "index"
	MissingForeign ChangeKind = "missing foreign key"
	ExtraForeign   ChangeKind = "extra foreign key"
	ForeignDiffers ChangeKind = "foreign key"
	KindDiffers    ChangeKind = "object kind"
)

// Change is one difference between source and target.
type Change struct {
	Kind   ChangeKind
	Table  string
	Name   string // column, index or foreign key
	Source string // the source's value, for changed things
	Target string // the target's value
}

// Drops reports changes that remove something from the target.
func (c Change) Drops() bool {
	return c.Kind == ExtraTable || c.Kind == ExtraColumn || c.Kind == ExtraIndex || c.Kind == ExtraForeign
}

func (c Change) String() string {
	what := c.Table
	if c.Name != "" {
		what += "." + c.Name
	}
	switch {
	case c.Source != "" || c.Target != "":
		return fmt.Sprintf("%-20s %s: %s → %s", c.Kind, what, orNone(c.Target), orNone(c.Source))
	default:
		return fmt.Sprintf("%-20s %s", c.Kind, what)
	}
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// Diff lists what differs in dst compared with src, sorted by table.
func Diff(src, dst *Schema) []Change {
	var out []Change
	for _, name := range sortedKeys(src.Tables) {
		st := src.Tables[name]
		dt, ok := dst.Tables[name]
		if !ok {
			out = append(out, Change{Kind: MissingTable, Table: name})
			continue
		}
		if st.Kind != dt.Kind {
			out = append(out, Change{Kind: KindDiffers, Table: name, Source: string(st.Kind), Target: string(dt.Kind)})
			continue
		}
		if st.Kind != db.KindTable {
			continue // views: existence only
		}
		out = append(out, diffTable(name, st.Detail, dt.Detail)...)
	}
	for _, name := range sortedKeys(dst.Tables) {
		if _, ok := src.Tables[name]; !ok {
			out = append(out, Change{Kind: ExtraTable, Table: name})
		}
	}
	return out
}

func diffTable(table string, s, d *db.TableDetail) []Change {
	var out []Change
	dcols := map[string]db.Column{}
	for _, c := range d.Columns {
		dcols[c.Name] = c
	}
	scols := map[string]bool{}
	for _, sc := range s.Columns {
		scols[sc.Name] = true
		dc, ok := dcols[sc.Name]
		if !ok {
			out = append(out, Change{Kind: MissingColumn, Table: table, Name: sc.Name})
			continue
		}
		if !strings.EqualFold(sc.Type, dc.Type) {
			out = append(out, Change{Kind: ColumnType, Table: table, Name: sc.Name, Source: sc.Type, Target: dc.Type})
		}
		if sc.Nullable != dc.Nullable {
			out = append(out, Change{Kind: ColumnNull, Table: table, Name: sc.Name, Source: nullText(sc.Nullable), Target: nullText(dc.Nullable)})
		}
		if deref(sc.Default) != deref(dc.Default) {
			out = append(out, Change{Kind: ColumnDefault, Table: table, Name: sc.Name, Source: deref(sc.Default), Target: deref(dc.Default)})
		}
	}
	for _, dc := range d.Columns {
		if !scols[dc.Name] {
			out = append(out, Change{Kind: ExtraColumn, Table: table, Name: dc.Name})
		}
	}
	if sp, dp := strings.Join(s.PrimaryKey(), ", "), strings.Join(d.PrimaryKey(), ", "); sp != dp {
		out = append(out, Change{Kind: PrimaryKey, Table: table, Source: sp, Target: dp})
	}

	sidx, didx := indexes(s), indexes(d)
	for _, name := range sortedKeys(sidx) {
		si := sidx[name]
		di, ok := didx[name]
		switch {
		case !ok:
			out = append(out, Change{Kind: MissingIndex, Table: table, Name: name, Source: indexText(si)})
		case indexText(si) != indexText(di):
			out = append(out, Change{Kind: IndexDiffers, Table: table, Name: name, Source: indexText(si), Target: indexText(di)})
		}
	}
	for _, name := range sortedKeys(didx) {
		if _, ok := sidx[name]; !ok {
			out = append(out, Change{Kind: ExtraIndex, Table: table, Name: name, Target: indexText(didx[name])})
		}
	}

	sfk, dfk := foreignKeys(s), foreignKeys(d)
	for _, name := range sortedKeys(sfk) {
		sf := sfk[name]
		df, ok := dfk[name]
		switch {
		case !ok:
			out = append(out, Change{Kind: MissingForeign, Table: table, Name: name, Source: fkText(sf)})
		case fkText(sf) != fkText(df):
			out = append(out, Change{Kind: ForeignDiffers, Table: table, Name: name, Source: fkText(sf), Target: fkText(df)})
		}
	}
	for _, name := range sortedKeys(dfk) {
		if _, ok := sfk[name]; !ok {
			out = append(out, Change{Kind: ExtraForeign, Table: table, Name: name, Target: fkText(dfk[name])})
		}
	}
	return out
}

// indexes keys the secondary indexes by name; the primary key is compared
// through its columns.
func indexes(t *db.TableDetail) map[string]db.Index {
	out := map[string]db.Index{}
	for _, ix := range t.Indexes {
		if !ix.Primary {
			out[ix.Name] = ix
		}
	}
	return out
}

func foreignKeys(t *db.TableDetail) map[string]db.ForeignKey {
	out := map[string]db.ForeignKey{}
	for _, fk := range t.ForeignKeys {
		out[fk.Name] = fk
	}
	return out
}

func indexText(ix db.Index) string {
	s := "(" + strings.Join(ix.Columns, ", ") + ")"
	if ix.Unique {
		s = "unique " + s
	}
	return s
}

func fkText(fk db.ForeignKey) string {
	s := fmt.Sprintf("(%s) → %s(%s)", strings.Join(fk.Columns, ", "), fk.RefTable, strings.Join(fk.RefColumns, ", "))
	if a := strings.ToUpper(fk.OnDelete); a != "" && a != "NO ACTION" {
		s += " on delete " + strings.ToLower(a)
	}
	if a := strings.ToUpper(fk.OnUpdate); a != "" && a != "NO ACTION" {
		s += " on update " + strings.ToLower(a)
	}
	return s
}

func nullText(nullable bool) string {
	if nullable {
		return "null"
	}
	return "not null"
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
