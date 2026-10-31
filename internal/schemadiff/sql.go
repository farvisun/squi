package schemadiff

import (
	"fmt"
	"slices"
	"strings"

	"github.com/farvisun/squi/internal/db"
)

// Options controls Script.
type Options struct {
	// Drops writes DROP statements for what only the target has; otherwise
	// they are written as comments to review.
	Drops bool
}

// Script returns the statements that make dst match src, in the target's
// dialect. Changes the engine can't express (or that need a table rebuild)
// become comments. Engines must match: across engines only the report is
// meaningful, and Script returns a comment saying so.
func Script(changes []Change, src, dst *Schema, opts Options) string {
	if src.Driver != dst.Driver {
		return fmt.Sprintf("-- %s and %s differ in SQL; compare the report instead.\n", src.Driver, dst.Driver)
	}
	w := writer{d: db.Dialect{Driver: dst.Driver}, src: src, dst: dst, opts: opts, modifiedCols: map[string]bool{}}
	ordered := slices.Clone(changes)
	slices.SortStableFunc(ordered, func(a, b Change) int { return w.phase(a) - w.phase(b) })
	for _, c := range ordered {
		w.change(c)
	}
	if len(w.lines) == 0 {
		return "-- no differences\n"
	}
	return strings.Join(w.lines, "\n") + "\n"
}

type writer struct {
	d        db.Dialect
	src, dst *Schema
	opts     Options
	lines    []string

	modifiedCols map[string]bool
}

// phase orders the script so each statement finds what it needs: tables
// before the foreign keys that reference them, views after the columns they
// read, and foreign keys dropped before the columns and tables they use.
func (w *writer) phase(c Change) int {
	switch c.Kind {
	case MissingTable:
		if t := w.src.Tables[c.Table]; t != nil && t.Kind != db.KindTable {
			return 5
		}
		return 0
	case MissingIndex, IndexDiffers:
		return 2
	case MissingForeign, ForeignDiffers:
		return 3
	case ExtraForeign:
		return 6
	case ExtraIndex:
		return 7
	case ExtraColumn:
		return 8
	case ExtraTable:
		return 9
	}
	return 1 // columns, primary keys, kind changes
}

func (w *writer) stmt(s string) { w.lines = append(w.lines, s+";") }

func (w *writer) note(format string, a ...any) {
	w.lines = append(w.lines, "-- "+fmt.Sprintf(format, a...))
}

// drop writes s, or comments it out unless drops are enabled.
func (w *writer) drop(s string) {
	if w.opts.Drops {
		w.stmt(s)
		return
	}
	w.lines = append(w.lines, "-- "+s+";  (review; run with -drop to include)")
}

func (w *writer) table(name string) string {
	return w.d.Qualified(db.ObjectRef{Database: w.dst.Database, Schema: w.dst.Schema, Name: name})
}

func (w *writer) q(name string) string { return w.d.QuoteIdent(name) }

func (w *writer) column(table, name string) (db.Column, bool) {
	t := w.src.Tables[table]
	if t == nil || t.Detail == nil {
		return db.Column{}, false
	}
	for _, c := range t.Detail.Columns {
		if c.Name == name {
			return c, true
		}
	}
	return db.Column{}, false
}

// colDef is "name type [NOT NULL] [DEFAULT x]" from the source column.
func (w *writer) colDef(c db.Column) string {
	s := w.q(c.Name) + " " + c.Type
	if !c.Nullable && w.d.Driver != db.ClickHouse {
		s += " NOT NULL"
	}
	if c.Default != nil && *c.Default != "" {
		s += " DEFAULT " + *c.Default
	}
	return s
}

func (w *writer) change(c Change) {
	d := w.d.Driver
	t := w.table(c.Table)
	switch c.Kind {
	case MissingTable:
		st := w.src.Tables[c.Table]
		if st == nil || strings.TrimSpace(st.DDL) == "" {
			w.note("create %s %s (no DDL available)", st.Kind, c.Table)
			return
		}
		if w.src.Schema != w.dst.Schema && w.d.HasSchemas() {
			w.note("DDL of %s copied from schema %s: change the schema to %s before running it", c.Table, w.src.Schema, w.dst.Schema)
		}
		w.lines = append(w.lines, strings.TrimRight(strings.TrimSpace(st.DDL), ";")+";")
	case ExtraTable:
		kind := "TABLE"
		if dt := w.dst.Tables[c.Table]; dt != nil && dt.Kind != db.KindTable {
			kind = strings.ToUpper(strings.ReplaceAll(string(dt.Kind), "matview", "materialized view"))
		}
		w.drop("DROP " + kind + " " + t)
	case KindDiffers:
		w.note("%s is a %s in the source but a %s in the target; recreate it by hand", c.Table, c.Source, c.Target)
	case MissingColumn:
		col, _ := w.column(c.Table, c.Name)
		switch d {
		case db.SQLServer:
			w.stmt("ALTER TABLE " + t + " ADD " + w.colDef(col))
		default:
			w.stmt("ALTER TABLE " + t + " ADD COLUMN " + w.colDef(col))
		}
	case ExtraColumn:
		w.drop("ALTER TABLE " + t + " DROP COLUMN " + w.q(c.Name))
	case ColumnType, ColumnNull, ColumnDefault:
		w.alterColumn(c, t)
	case PrimaryKey:
		w.note("primary key of %s: target (%s), source (%s); change it by hand", c.Table, c.Target, c.Source)
	case MissingIndex, IndexDiffers:
		ix := w.srcIndex(c.Table, c.Name)
		if c.Kind == IndexDiffers {
			w.dropIndex(c.Table, c.Name, true)
		}
		w.createIndex(c.Table, ix)
	case ExtraIndex:
		w.dropIndex(c.Table, c.Name, false)
	case MissingForeign, ForeignDiffers:
		if d == db.SQLite || d == db.ClickHouse {
			w.note("foreign key %s on %s: %s can't add one to an existing table", c.Name, c.Table, d)
			return
		}
		if c.Kind == ForeignDiffers {
			w.stmt(w.dropFK(t, c.Name))
		}
		w.stmt(w.addFK(t, w.srcFK(c.Table, c.Name)))
	case ExtraForeign:
		if d == db.SQLite || d == db.ClickHouse {
			w.note("extra foreign key %s on %s: rebuild the table to remove it", c.Name, c.Table)
			return
		}
		w.drop(w.dropFK(t, c.Name))
	}
}

func (w *writer) alterColumn(c Change, t string) {
	col, ok := w.column(c.Table, c.Name)
	if !ok {
		return
	}
	n := w.q(c.Name)
	switch w.d.Driver {
	case db.Postgres:
		switch c.Kind {
		case ColumnType:
			w.stmt(fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s", t, n, col.Type))
		case ColumnNull:
			if col.Nullable {
				w.stmt(fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP NOT NULL", t, n))
			} else {
				w.stmt(fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET NOT NULL", t, n))
			}
		case ColumnDefault:
			if col.Default == nil || *col.Default == "" {
				w.stmt(fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT", t, n))
			} else {
				w.stmt(fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET DEFAULT %s", t, n, *col.Default))
			}
		}
	case db.MySQL, db.ClickHouse:
		// one MODIFY carries type, nullability and default together
		if w.modified(c.Table, c.Name) {
			return
		}
		w.stmt(fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s", t, w.colDef(col)))
	case db.SQLServer:
		if c.Kind == ColumnDefault {
			w.note("default of %s.%s: target %s, source %s; SQL Server keeps defaults as constraints, change it by hand",
				c.Table, c.Name, orNone(c.Target), orNone(c.Source))
			return
		}
		if w.modified(c.Table, c.Name) {
			return
		}
		null := " NULL"
		if !col.Nullable {
			null = " NOT NULL"
		}
		w.stmt(fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s %s%s", t, n, col.Type, null))
	case db.SQLite:
		w.note("%s of %s.%s: target %s, source %s; SQLite can't alter a column, rebuild the table",
			c.Kind, c.Table, c.Name, orNone(c.Target), orNone(c.Source))
	}
}

// modified reports (and records) that a column already got its full
// redefinition, so several changes to it produce one statement.
func (w *writer) modified(table, col string) bool {
	key := table + "." + col
	if w.modifiedCols[key] {
		return true
	}
	w.modifiedCols[key] = true
	return false
}

func (w *writer) srcIndex(table, name string) db.Index {
	for _, ix := range w.src.Tables[table].Detail.Indexes {
		if ix.Name == name {
			return ix
		}
	}
	return db.Index{Name: name}
}

func (w *writer) createIndex(table string, ix db.Index) {
	if w.d.Driver == db.ClickHouse {
		w.note("index %s on %s: add ClickHouse skipping indexes by hand", ix.Name, table)
		return
	}
	cols := make([]string, len(ix.Columns))
	for i, c := range ix.Columns {
		cols[i] = w.q(c)
	}
	unique := ""
	if ix.Unique {
		unique = "UNIQUE "
	}
	w.stmt(fmt.Sprintf("CREATE %sINDEX %s ON %s (%s)", unique, w.q(ix.Name), w.table(table), strings.Join(cols, ", ")))
}

func (w *writer) dropIndex(table, name string, replace bool) {
	var s string
	switch w.d.Driver {
	case db.Postgres:
		s = "DROP INDEX " + w.d.Qualified(db.ObjectRef{Schema: w.dst.Schema, Name: name})
	case db.MySQL, db.SQLServer:
		s = "DROP INDEX " + w.q(name) + " ON " + w.table(table)
	case db.SQLite:
		s = "DROP INDEX " + w.q(name)
	default:
		w.note("index %s on %s: drop ClickHouse skipping indexes by hand", name, table)
		return
	}
	if replace {
		w.stmt(s) // recreated right after: not a loss
		return
	}
	w.drop(s)
}

func (w *writer) srcFK(table, name string) db.ForeignKey {
	for _, fk := range w.src.Tables[table].Detail.ForeignKeys {
		if fk.Name == name {
			return fk
		}
	}
	return db.ForeignKey{Name: name}
}

func (w *writer) addFK(t string, fk db.ForeignKey) string {
	q := func(cols []string) string {
		out := make([]string, len(cols))
		for i, c := range cols {
			out[i] = w.q(c)
		}
		return strings.Join(out, ", ")
	}
	ref := w.table(fk.RefTable)
	s := fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s (%s)", t, w.q(fk.Name), q(fk.Columns), ref, q(fk.RefColumns))
	if a := strings.ToUpper(fk.OnDelete); a != "" && a != "NO ACTION" {
		s += " ON DELETE " + a
	}
	if a := strings.ToUpper(fk.OnUpdate); a != "" && a != "NO ACTION" {
		s += " ON UPDATE " + a
	}
	return s
}

func (w *writer) dropFK(t, name string) string {
	if w.d.Driver == db.MySQL {
		return "ALTER TABLE " + t + " DROP FOREIGN KEY " + w.q(name)
	}
	return "ALTER TABLE " + t + " DROP CONSTRAINT " + w.q(name)
}
