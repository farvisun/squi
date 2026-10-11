package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/farvisun/squi/internal/db"
)

// Databases lists main and the attached databases (temp is left out).
func (c *conn) Databases(ctx context.Context) ([]db.Database, error) {
	rows, err := c.meta.QueryContext(ctx, "SELECT name, file FROM pragma_database_list WHERE name <> 'temp' ORDER BY seq")
	if err != nil {
		return nil, wrapErr(err)
	}
	defer rows.Close()
	var out []db.Database
	for rows.Next() {
		var d db.Database
		var file sql.NullString
		if err := rows.Scan(&d.Name, &file); err != nil {
			return nil, err
		}
		d.Comment = file.String
		if fi, err := os.Stat(file.String); err == nil && file.String != "" {
			d.Size = humanSize(fi.Size())
		}
		out = append(out, d)
	}
	return out, wrapErr(rows.Err())
}

// Schemas returns []string{database}: SQLite has no schema level.
func (c *conn) Schemas(_ context.Context, database string) ([]string, error) {
	return []string{schemaName(database)}, nil
}

var kindRank = map[db.ObjectKind]int{db.KindTable: 0, db.KindView: 1, db.KindTrigger: 2}

func (c *conn) Objects(ctx context.Context, database, schema string) ([]db.Object, error) {
	if schema == "" {
		schema = schemaName(database)
	}
	rows, err := c.meta.QueryContext(ctx, `
		SELECT name, type FROM `+c.quote(schema)+`.sqlite_schema
		WHERE type IN ('table', 'view', 'trigger') AND name NOT LIKE 'sqlite\_%' ESCAPE '\'
		ORDER BY CASE type WHEN 'table' THEN 0 WHEN 'view' THEN 1 ELSE 2 END, name`)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer rows.Close()
	var out []db.Object
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			return nil, err
		}
		out = append(out, db.Object{Schema: schema, Name: name, Kind: db.ObjectKind(typ)})
	}
	return out, wrapErr(rows.Err())
}

func (c *conn) AllColumns(ctx context.Context, database, schema string) (map[string][]string, error) {
	if schema == "" {
		schema = schemaName(database)
	}
	rows, err := c.meta.QueryContext(ctx, `
		SELECT m.name, p.name FROM `+c.quote(schema)+`.sqlite_schema m
		JOIN pragma_table_info(m.name, ?1) p
		WHERE m.type IN ('table', 'view') AND m.name NOT LIKE 'sqlite\_%' ESCAPE '\'
		ORDER BY m.name, p.cid`, schema)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var t, col string
		if err := rows.Scan(&t, &col); err != nil {
			return nil, err
		}
		out[t] = append(out[t], col)
	}
	return out, wrapErr(rows.Err())
}

// ErrNotFound is returned for objects that do not exist.
var ErrNotFound = errors.New("object not found")

// object is a row of sqlite_schema.
type object struct {
	schema, name, typ, sql string
}

func (c *conn) object(ctx context.Context, ref db.ObjectRef) (object, error) {
	o := object{schema: ref.Schema}
	if o.schema == "" {
		o.schema = schemaName(ref.Database)
	}
	var def sql.NullString
	err := c.meta.QueryRowContext(ctx, `SELECT name, type, sql FROM `+c.quote(o.schema)+`.sqlite_schema
		WHERE name = ?1 COLLATE NOCASE AND type IN ('table', 'view', 'trigger', 'index')`, ref.Name).Scan(&o.name, &o.typ, &def)
	if errors.Is(err, sql.ErrNoRows) {
		return o, fmt.Errorf("%s.%s: %w", o.schema, ref.Name, ErrNotFound)
	}
	o.sql = def.String
	return o, wrapErr(err)
}

// TableDetail describes a table or view.
func (c *conn) TableDetail(ctx context.Context, ref db.ObjectRef) (*db.TableDetail, error) {
	o, err := c.object(ctx, ref)
	if err != nil {
		return nil, err
	}
	ref.Name, ref.Schema, ref.Kind = o.name, o.schema, db.ObjectKind(o.typ)
	if ref.Database == "" {
		ref.Database = o.schema
	}
	t := &db.TableDetail{Ref: ref}
	if t.Columns, err = c.columns(ctx, o); err != nil {
		return nil, err
	}
	if o.typ != "table" {
		t.Triggers, err = c.triggers(ctx, o)
		return t, err
	}
	if t.Indexes, err = c.indexes(ctx, o); err != nil {
		return nil, err
	}
	if t.ForeignKeys, err = c.foreignKeys(ctx, o); err != nil {
		return nil, err
	}
	if t.ReferencedBy, err = c.referencedBy(ctx, o); err != nil {
		return nil, err
	}
	t.Constraints = checkConstraints(o.sql)
	if t.Triggers, err = c.triggers(ctx, o); err != nil {
		return nil, err
	}
	t.Stats = c.stats(ctx, o)
	return t, nil
}

func (c *conn) columns(ctx context.Context, o object) ([]db.Column, error) {
	rows, err := c.meta.QueryContext(ctx, `SELECT cid, name, type, "notnull", dflt_value, pk, hidden FROM pragma_table_xinfo(?1, ?2) ORDER BY cid`, o.name, o.schema)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer rows.Close()
	autoinc := regexp.MustCompile(`(?i)\bAUTOINCREMENT\b`).MatchString(o.sql)
	var out []db.Column
	var pks int
	for rows.Next() {
		var col db.Column
		var notNull bool
		var def sql.NullString
		var pk, hidden int
		if err := rows.Scan(&col.Position, &col.Name, &col.Type, &notNull, &def, &pk, &hidden); err != nil {
			return nil, err
		}
		col.Position++
		col.Nullable = !notNull && pk == 0
		col.IsPK = pk > 0
		if pk > 0 {
			pks++
		}
		if def.Valid {
			d := def.String
			col.Default = &d
		}
		switch hidden {
		case 1:
			col.Extra = "hidden"
		case 2:
			col.Extra = "generated (virtual)"
		case 3:
			col.Extra = "generated (stored)"
		}
		out = append(out, col)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapErr(err)
	}
	// INTEGER PRIMARY KEY is an alias of the rowid
	if pks == 1 {
		for i := range out {
			if out[i].IsPK && strings.EqualFold(out[i].Type, "INTEGER") && !strings.Contains(strings.ToUpper(o.sql), "WITHOUT ROWID") {
				out[i].Extra = "rowid"
				if autoinc {
					out[i].Extra = "rowid, autoincrement"
				}
			}
		}
	}
	return out, nil
}

func (c *conn) indexes(ctx context.Context, o object) ([]db.Index, error) {
	rows, err := c.meta.QueryContext(ctx, `
		SELECT l.name, l."unique", l.origin, s.sql
		FROM pragma_index_list(?1, ?2) l
		LEFT JOIN `+c.quote(o.schema)+`.sqlite_schema s ON s.type = 'index' AND s.name = l.name
		ORDER BY l.origin = 'pk' DESC, l.name`, o.name, o.schema)
	if err != nil {
		return nil, wrapErr(err)
	}
	var out []db.Index
	for rows.Next() {
		var ix db.Index
		var origin string
		var def sql.NullString
		if err := rows.Scan(&ix.Name, &ix.Unique, &origin, &def); err != nil {
			rows.Close()
			return nil, err
		}
		ix.Primary, ix.Method, ix.Definition = origin == "pk", "btree", def.String
		out = append(out, ix)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, wrapErr(err)
	}
	for i := range out {
		cols, err := c.meta.QueryContext(ctx, `SELECT name, "desc" FROM pragma_index_xinfo(?1, ?2) WHERE key = 1 ORDER BY seqno`, out[i].Name, o.schema)
		if err != nil {
			return nil, wrapErr(err)
		}
		var defs []string
		for cols.Next() {
			var name sql.NullString
			var desc bool
			if err := cols.Scan(&name, &desc); err != nil {
				cols.Close()
				return nil, err
			}
			n := name.String
			if !name.Valid {
				n = "<expr>"
			}
			out[i].Columns = append(out[i].Columns, n)
			d := c.quote(n)
			if desc {
				d += " DESC"
			}
			defs = append(defs, d)
		}
		cols.Close()
		if out[i].Definition == "" { // automatic index of a PRIMARY KEY / UNIQUE constraint
			kind := "UNIQUE"
			if out[i].Primary {
				kind = "PRIMARY KEY"
			}
			out[i].Definition = kind + " (" + strings.Join(defs, ", ") + ")"
		}
	}
	return out, nil
}

// foreignKeys reads pragma_foreign_key_list. SQLite foreign keys are
// usually unnamed: they are called fk_<n>.
func (c *conn) foreignKeys(ctx context.Context, o object) ([]db.ForeignKey, error) {
	rows, err := c.meta.QueryContext(ctx, `SELECT id, "table", "from", "to", on_update, on_delete
		FROM pragma_foreign_key_list(?1, ?2) ORDER BY id, seq`, o.name, o.schema)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer rows.Close()
	var out []db.ForeignKey
	lastID := -1
	for rows.Next() {
		var id int
		var table, from, onUpd, onDel string
		var to sql.NullString
		if err := rows.Scan(&id, &table, &from, &to, &onUpd, &onDel); err != nil {
			return nil, err
		}
		if id != lastID {
			out = append(out, db.ForeignKey{Name: "fk_" + strconv.Itoa(id), Schema: o.schema, Table: o.name,
				RefSchema: o.schema, RefTable: table, OnUpdate: onUpd, OnDelete: onDel})
			lastID = id
		}
		fk := &out[len(out)-1]
		fk.Columns = append(fk.Columns, from)
		fk.RefColumns = append(fk.RefColumns, to.String) // "" = the referenced table's primary key
	}
	if err := rows.Err(); err != nil {
		return nil, wrapErr(err)
	}
	for i := range out {
		if err := c.resolvePK(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// resolvePK fills referenced columns left implicit (REFERENCES parent) with
// the parent's primary key.
func (c *conn) resolvePK(ctx context.Context, fk *db.ForeignKey) error {
	missing := false
	for _, rc := range fk.RefColumns {
		missing = missing || rc == ""
	}
	if !missing {
		return nil
	}
	rows, err := c.meta.QueryContext(ctx, `SELECT name FROM pragma_table_info(?1, ?2) WHERE pk > 0 ORDER BY pk`, fk.RefTable, fk.RefSchema)
	if err != nil {
		return wrapErr(err)
	}
	defer rows.Close()
	var pk []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return err
		}
		pk = append(pk, n)
	}
	if len(pk) == len(fk.RefColumns) {
		fk.RefColumns = pk
	}
	return wrapErr(rows.Err())
}

func (c *conn) referencedBy(ctx context.Context, o object) ([]db.ForeignKey, error) {
	rows, err := c.meta.QueryContext(ctx, `
		SELECT m.name, f.id, f."from", f."to", f.on_update, f.on_delete
		FROM `+c.quote(o.schema)+`.sqlite_schema m
		JOIN pragma_foreign_key_list(m.name, ?2) f
		WHERE m.type = 'table' AND f."table" = ?1 COLLATE NOCASE
		ORDER BY m.name, f.id, f.seq`, o.name, o.schema)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer rows.Close()
	var out []db.ForeignKey
	for rows.Next() {
		var table, from, onUpd, onDel string
		var id int
		var to sql.NullString
		if err := rows.Scan(&table, &id, &from, &to, &onUpd, &onDel); err != nil {
			return nil, err
		}
		name := "fk_" + strconv.Itoa(id)
		if n := len(out); n == 0 || out[n-1].Table != table || out[n-1].Name != name {
			out = append(out, db.ForeignKey{Name: name, Schema: o.schema, Table: table, RefSchema: o.schema, RefTable: o.name,
				OnUpdate: onUpd, OnDelete: onDel})
		}
		fk := &out[len(out)-1]
		fk.Columns = append(fk.Columns, from)
		fk.RefColumns = append(fk.RefColumns, to.String)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapErr(err)
	}
	for i := range out {
		if err := c.resolvePK(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

var checkRe = regexp.MustCompile(`(?i)(?:CONSTRAINT\s+("[^"]+"|\[[^\]]+\]|` + "`[^`]+`" + `|\w+)\s+)?CHECK\s*\(`)

// checkConstraints extracts CHECK (…) clauses from a CREATE TABLE
// statement: SQLite keeps no catalog of them.
func checkConstraints(create string) []db.Constraint {
	var out []db.Constraint
	for _, m := range checkRe.FindAllStringSubmatchIndex(create, -1) {
		open := m[1] - 1
		depth, end := 0, -1
		quote := byte(0)
		for i := open; i < len(create) && end < 0; i++ {
			ch := create[i]
			switch {
			case quote != 0:
				if ch == quote {
					quote = 0
				}
			case ch == '\'' || ch == '"':
				quote = ch
			case ch == '(':
				depth++
			case ch == ')':
				depth--
				if depth == 0 {
					end = i
				}
			}
		}
		if end < 0 {
			continue
		}
		name := ""
		if m[2] >= 0 {
			name = strings.Trim(create[m[2]:m[3]], "\"[]`")
		}
		out = append(out, db.Constraint{Name: name, Type: "CHECK", Definition: "CHECK " + create[open:end+1]})
	}
	return out
}

var triggerRe = regexp.MustCompile(`(?is)\b(BEFORE|AFTER|INSTEAD\s+OF)?\s*(DELETE|INSERT|UPDATE(?:\s+OF\s+[^\n]*?)?)\s+ON\b`)

func (c *conn) triggers(ctx context.Context, o object) ([]db.Trigger, error) {
	rows, err := c.meta.QueryContext(ctx, `SELECT name, sql FROM `+c.quote(o.schema)+`.sqlite_schema
		WHERE type = 'trigger' AND tbl_name = ?1 COLLATE NOCASE ORDER BY name`, o.name)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer rows.Close()
	var out []db.Trigger
	for rows.Next() {
		var t db.Trigger
		var def sql.NullString
		if err := rows.Scan(&t.Name, &def); err != nil {
			return nil, err
		}
		t.Definition, t.Timing = def.String, "BEFORE"
		if m := triggerRe.FindStringSubmatch(def.String); m != nil {
			if m[1] != "" {
				t.Timing = strings.ToUpper(strings.Join(strings.Fields(m[1]), " "))
			}
			t.Event = strings.ToUpper(strings.Fields(m[2])[0])
		}
		out = append(out, t)
	}
	return out, wrapErr(rows.Err())
}

// stats are best effort: table flags and the size from the dbstat table.
func (c *conn) stats(ctx context.Context, o object) []db.KV {
	var out []db.KV
	var wr, strict bool
	if c.meta.QueryRowContext(ctx, `SELECT wr, strict FROM pragma_table_list(?1) WHERE schema = ?2`, o.name, o.schema).Scan(&wr, &strict) == nil {
		if wr {
			out = append(out, db.KV{Key: "Without rowid", Value: "yes"})
		}
		if strict {
			out = append(out, db.KV{Key: "Strict", Value: "yes"})
		}
	}
	var size sql.NullInt64
	if c.meta.QueryRowContext(ctx, `SELECT SUM(pgsize) FROM dbstat(?2) WHERE name = ?1`, o.name, o.schema).Scan(&size) == nil && size.Valid {
		out = append(out, db.KV{Key: "Size", Value: humanSize(size.Int64)})
	}
	var idx sql.NullInt64
	if c.meta.QueryRowContext(ctx, `SELECT SUM(d.pgsize) FROM dbstat(?2) d
		JOIN `+c.quote(o.schema)+`.sqlite_schema s ON s.name = d.name
		WHERE s.type = 'index' AND s.tbl_name = ?1`, o.name, o.schema).Scan(&idx) == nil && idx.Valid {
		out = append(out, db.KV{Key: "Index size", Value: humanSize(idx.Int64)})
	}
	return out
}

// DDL returns the CREATE statement SQLite stored, followed (for tables) by
// the table's indexes and triggers.
func (c *conn) DDL(ctx context.Context, ref db.ObjectRef) (string, error) {
	o, err := c.object(ctx, ref)
	if err != nil {
		return "", err
	}
	if o.typ != "table" {
		return strings.TrimSpace(o.sql) + ";", nil
	}
	parts := []string{strings.TrimSpace(o.sql) + ";"}
	rows, err := c.meta.QueryContext(ctx, `SELECT sql FROM `+c.quote(o.schema)+`.sqlite_schema
		WHERE tbl_name = ?1 COLLATE NOCASE AND type IN ('index', 'trigger') AND sql IS NOT NULL
		ORDER BY type, name`, o.name)
	if err != nil {
		return "", wrapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return "", err
		}
		parts = append(parts, strings.TrimSpace(s)+";")
	}
	return strings.Join(parts, "\n\n"), wrapErr(rows.Err())
}

// EstimateRows reads sqlite_stat1, written by ANALYZE; without statistics
// there is no estimate (and an exact COUNT(*) of a local file is cheap).
func (c *conn) EstimateRows(ctx context.Context, ref db.ObjectRef) (int64, bool, bool, error) {
	o, err := c.object(ctx, ref)
	if errors.Is(err, ErrNotFound) {
		return 0, false, false, nil
	}
	if err != nil || o.typ != "table" {
		return 0, false, false, err
	}
	var stat string
	err = c.meta.QueryRowContext(ctx, `SELECT stat FROM `+c.quote(o.schema)+`.sqlite_stat1
		WHERE tbl = ?1 COLLATE NOCASE ORDER BY idx IS NOT NULL LIMIT 1`, o.name).Scan(&stat)
	if err != nil {
		return 0, false, false, nil // not analyzed
	}
	first, _, _ := strings.Cut(stat, " ")
	n, err := strconv.ParseInt(first, 10, 64)
	if err != nil {
		return 0, false, false, nil
	}
	return n, false, true, nil
}
