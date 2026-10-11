package mssql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/farvisun/squi/internal/db"
)

// Catalog queries use three-part names ([database].sys.objects) so one
// metadata pool serves every database without USE.

func (c *conn) Databases(ctx context.Context) ([]db.Database, error) {
	const base = `
		SELECT d.name, %s
		FROM sys.databases d %s
		WHERE d.state = 0 AND HAS_DBACCESS(d.name) = 1
		GROUP BY d.name, d.database_id
		ORDER BY CASE WHEN d.database_id <= 4 THEN 1 ELSE 0 END, d.name`
	rows, err := c.meta.QueryContext(ctx, fmt.Sprintf(base, "CAST(SUM(CAST(f.size AS bigint)) * 8192 AS bigint)",
		"LEFT JOIN sys.master_files f ON f.database_id = d.database_id"))
	if err != nil {
		// Azure SQL Database has no sys.master_files
		rows, err = c.meta.QueryContext(ctx, fmt.Sprintf(base, "CAST(NULL AS bigint)", ""))
	}
	if err != nil {
		return nil, wrapErr(err)
	}
	defer rows.Close()
	var out []db.Database
	for rows.Next() {
		var d db.Database
		var size sql.NullInt64
		if err := rows.Scan(&d.Name, &size); err != nil {
			return nil, err
		}
		if size.Valid {
			d.Size = humanSize(size.Int64)
		}
		out = append(out, d)
	}
	return out, wrapErr(rows.Err())
}

// Schemas lists the schemas of a database without the fixed database role
// schemas (db_owner…) and guest; dbo comes first, sys and
// INFORMATION_SCHEMA last.
func (c *conn) Schemas(ctx context.Context, database string) ([]string, error) {
	p := c.prefix(database)
	rows, err := c.meta.QueryContext(ctx, `
		SELECT s.name FROM `+p+`sys.schemas s
		WHERE s.schema_id < 16384 AND s.name <> 'guest'
		ORDER BY CASE s.name WHEN 'dbo' THEN 0 WHEN 'sys' THEN 2 WHEN 'INFORMATION_SCHEMA' THEN 2 ELSE 1 END, s.name`)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, wrapErr(rows.Err())
}

// objectKind maps sys.objects.type to an explorer kind.
func objectKind(t string) (db.ObjectKind, bool) {
	switch strings.TrimSpace(t) {
	case "U":
		return db.KindTable, true
	case "V":
		return db.KindView, true
	case "P", "PC", "X":
		return db.KindProcedure, true
	case "FN", "IF", "TF", "FS", "FT", "AF":
		return db.KindFunction, true
	case "SO":
		return db.KindSequence, true
	case "TR", "TA":
		return db.KindTrigger, true
	}
	return "", false
}

var kindRank = map[db.ObjectKind]int{
	db.KindTable: 0, db.KindView: 1, db.KindSequence: 2,
	db.KindFunction: 3, db.KindProcedure: 4, db.KindTrigger: 5,
}

func (c *conn) Objects(ctx context.Context, database, schema string) ([]db.Object, error) {
	p := c.prefix(database)
	rows, err := c.meta.QueryContext(ctx, `
		SELECT o.name, o.type, CAST(ep.value AS nvarchar(4000))
		FROM `+p+`sys.all_objects o
		JOIN `+p+`sys.schemas s ON s.schema_id = o.schema_id
		LEFT JOIN `+p+`sys.extended_properties ep
		       ON ep.class = 1 AND ep.major_id = o.object_id AND ep.minor_id = 0 AND ep.name = 'MS_Description'
		WHERE s.name = @p1 AND o.type IN ('U','V','P','PC','X','FN','IF','TF','FS','FT','AF','SO','TR','TA')
		ORDER BY o.name`, schema)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer rows.Close()
	var out []db.Object
	for rows.Next() {
		var name, typ string
		var comment sql.NullString
		if err := rows.Scan(&name, &typ, &comment); err != nil {
			return nil, err
		}
		kind, ok := objectKind(typ)
		if !ok {
			continue
		}
		out = append(out, db.Object{Schema: schema, Name: name, Kind: kind, Comment: comment.String})
	}
	if err := rows.Err(); err != nil {
		return nil, wrapErr(err)
	}
	sortObjects(out)
	return out, nil
}

func sortObjects(objs []db.Object) {
	for i := 1; i < len(objs); i++ { // insertion sort keeps name order within a kind
		for j := i; j > 0 && kindRank[objs[j].Kind] < kindRank[objs[j-1].Kind]; j-- {
			objs[j], objs[j-1] = objs[j-1], objs[j]
		}
	}
}

func (c *conn) AllColumns(ctx context.Context, database, schema string) (map[string][]string, error) {
	p := c.prefix(database)
	rows, err := c.meta.QueryContext(ctx, `
		SELECT o.name, c.name
		FROM `+p+`sys.all_columns c
		JOIN `+p+`sys.all_objects o ON o.object_id = c.object_id
		JOIN `+p+`sys.schemas s ON s.schema_id = o.schema_id
		WHERE s.name = @p1 AND o.type IN ('U','V','IF','TF','FT')
		ORDER BY o.name, c.column_id`, schema)
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

// object is a resolved catalog object.
type object struct {
	id     int64
	typ    string
	prefix string // "[db]." or ""
}

// ErrNotFound is returned for objects that do not exist.
var ErrNotFound = errors.New("object not found")

func (c *conn) object(ctx context.Context, ref db.ObjectRef) (object, error) {
	o := object{prefix: c.prefix(ref.Database)}
	schema := ref.Schema
	if schema == "" {
		schema = "dbo"
	}
	err := c.meta.QueryRowContext(ctx, `
		SELECT CAST(o.object_id AS bigint), o.type FROM `+o.prefix+`sys.all_objects o
		JOIN `+o.prefix+`sys.schemas s ON s.schema_id = o.schema_id
		WHERE s.name = @p1 AND o.name = @p2`, schema, ref.Name).Scan(&o.id, &o.typ)
	if errors.Is(err, sql.ErrNoRows) {
		return o, fmt.Errorf("%s.%s: %w", schema, ref.Name, ErrNotFound)
	}
	o.typ = strings.TrimSpace(o.typ)
	return o, wrapErr(err)
}

// TableDetail describes a table or view.
func (c *conn) TableDetail(ctx context.Context, ref db.ObjectRef) (*db.TableDetail, error) {
	o, err := c.object(ctx, ref)
	if err != nil {
		return nil, err
	}
	if k, ok := objectKind(o.typ); ok {
		ref.Kind = k
	}
	if ref.Schema == "" {
		ref.Schema = "dbo"
	}
	t := &db.TableDetail{Ref: ref}
	var comment sql.NullString
	if err := c.meta.QueryRowContext(ctx, `
		SELECT CAST((SELECT value FROM `+o.prefix+`sys.extended_properties
		             WHERE class = 1 AND major_id = @p1 AND minor_id = 0 AND name = 'MS_Description') AS nvarchar(4000))`,
		o.id).Scan(&comment); err != nil {
		return nil, wrapErr(err)
	}
	t.Comment = comment.String
	if t.Columns, err = c.columns(ctx, o); err != nil {
		return nil, err
	}
	if t.Indexes, err = c.indexes(ctx, o); err != nil {
		return nil, err
	}
	if o.typ == "U" {
		if t.ForeignKeys, err = c.foreignKeys(ctx, o, "fk.parent_object_id"); err != nil {
			return nil, err
		}
		if t.ReferencedBy, err = c.foreignKeys(ctx, o, "fk.referenced_object_id"); err != nil {
			return nil, err
		}
		if t.Constraints, err = c.constraints(ctx, o, t.Indexes); err != nil {
			return nil, err
		}
	}
	if t.Triggers, err = c.triggers(ctx, o); err != nil {
		return nil, err
	}
	t.Stats = c.stats(ctx, o)
	return t, nil
}

// typeName renders a column type like the server's DDL: nvarchar(50),
// varbinary(max), decimal(10,2), datetime2(3).
func typeName(name string, maxLen, precision, scale int, userType bool) string {
	if userType {
		return name
	}
	switch name {
	case "varchar", "char", "varbinary", "binary":
		if maxLen < 0 {
			return name + "(max)"
		}
		return name + "(" + strconv.Itoa(maxLen) + ")"
	case "nvarchar", "nchar":
		if maxLen < 0 {
			return name + "(max)"
		}
		return name + "(" + strconv.Itoa(maxLen/2) + ")"
	case "decimal", "numeric":
		return name + "(" + strconv.Itoa(precision) + "," + strconv.Itoa(scale) + ")"
	case "datetime2", "time", "datetimeoffset":
		if scale != 7 {
			return name + "(" + strconv.Itoa(scale) + ")"
		}
	}
	return name
}

func (c *conn) columns(ctx context.Context, o object) ([]db.Column, error) {
	p := o.prefix
	rows, err := c.meta.QueryContext(ctx, `
		SELECT c.column_id, c.name, t.name, CAST(c.max_length AS int), CAST(c.precision AS int), CAST(c.scale AS int),
		       CAST(t.is_user_defined AS bit), c.is_nullable, dc.definition,
		       CAST(CASE WHEN EXISTS (
		            SELECT 1 FROM `+p+`sys.indexes i
		            JOIN `+p+`sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id
		            WHERE i.object_id = c.object_id AND i.is_primary_key = 1 AND ic.column_id = c.column_id) THEN 1 ELSE 0 END AS bit),
		       c.is_identity, CAST(idc.seed_value AS nvarchar(40)), CAST(idc.increment_value AS nvarchar(40)),
		       cc.definition, cc.is_persisted,
		       CAST(ep.value AS nvarchar(4000))
		FROM `+p+`sys.all_columns c
		JOIN `+p+`sys.types t ON t.user_type_id = c.user_type_id
		LEFT JOIN `+p+`sys.default_constraints dc ON dc.object_id = c.default_object_id
		LEFT JOIN `+p+`sys.identity_columns idc ON idc.object_id = c.object_id AND idc.column_id = c.column_id
		LEFT JOIN `+p+`sys.computed_columns cc ON cc.object_id = c.object_id AND cc.column_id = c.column_id
		LEFT JOIN `+p+`sys.extended_properties ep
		       ON ep.class = 1 AND ep.major_id = c.object_id AND ep.minor_id = c.column_id AND ep.name = 'MS_Description'
		WHERE c.object_id = @p1
		ORDER BY c.column_id`, o.id)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer rows.Close()
	var out []db.Column
	for rows.Next() {
		var (
			col                  db.Column
			typ                  string
			maxLen, prec, scale  int
			udt, identity        bool
			def, seed, inc, comp sql.NullString
			persisted            sql.NullBool
			comment              sql.NullString
		)
		if err := rows.Scan(&col.Position, &col.Name, &typ, &maxLen, &prec, &scale, &udt, &col.Nullable, &def,
			&col.IsPK, &identity, &seed, &inc, &comp, &persisted, &comment); err != nil {
			return nil, err
		}
		col.Type = typeName(typ, maxLen, prec, scale, udt)
		if def.Valid {
			d := def.String
			col.Default = &d
		}
		switch {
		case identity:
			col.Extra = "IDENTITY(" + seed.String + "," + inc.String + ")"
		case comp.Valid:
			col.Extra = "AS " + comp.String
			if persisted.Bool {
				col.Extra += " PERSISTED"
			}
		}
		col.Comment = comment.String
		out = append(out, col)
	}
	return out, wrapErr(rows.Err())
}

func (c *conn) indexes(ctx context.Context, o object) ([]db.Index, error) {
	p := o.prefix
	rows, err := c.meta.QueryContext(ctx, `
		SELECT i.name, i.is_unique, i.is_primary_key, i.is_unique_constraint, i.type_desc, i.filter_definition,
		       col.name, ic.is_descending_key, ic.is_included_column,
		       (SELECT CAST(SUM(a.total_pages) AS bigint) * 8192
		        FROM `+p+`sys.partitions pt JOIN `+p+`sys.allocation_units a ON a.container_id = pt.partition_id
		        WHERE pt.object_id = i.object_id AND pt.index_id = i.index_id)
		FROM `+p+`sys.indexes i
		JOIN `+p+`sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id
		JOIN `+p+`sys.all_columns col ON col.object_id = ic.object_id AND col.column_id = ic.column_id
		WHERE i.object_id = @p1 AND i.type > 0 AND i.is_hypothetical = 0
		ORDER BY i.is_primary_key DESC, i.name, ic.is_included_column, ic.key_ordinal, ic.index_column_id`, o.id)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer rows.Close()
	var out []db.Index
	var keys, include []string
	var filter sql.NullString
	var uniqueConstraint bool
	finish := func() {
		if len(out) == 0 {
			return
		}
		ix := &out[len(out)-1]
		ix.Columns = keys
		kind := strings.ToUpper(ix.Method)
		def := ""
		switch {
		case ix.Primary:
			def = "PRIMARY KEY " + kind + " (" + strings.Join(keyList(keys), ", ") + ")"
		case uniqueConstraint:
			def = "UNIQUE " + kind + " (" + strings.Join(keyList(keys), ", ") + ")"
		default:
			def = "CREATE "
			if ix.Unique {
				def += "UNIQUE "
			}
			def += kind + " INDEX " + bracket(ix.Name) + " (" + strings.Join(keyList(keys), ", ") + ")"
			if len(include) > 0 {
				def += " INCLUDE (" + strings.Join(keyList(include), ", ") + ")"
			}
			if filter.Valid {
				def += " WHERE " + filter.String
			}
		}
		ix.Definition = def
		for i, k := range keys {
			keys[i] = strings.TrimSuffix(k, " DESC")
		}
	}
	for rows.Next() {
		var (
			name, typ, col      string
			unique, primary, uq bool
			desc, included      bool
			f                   sql.NullString
			size                sql.NullInt64
		)
		if err := rows.Scan(&name, &unique, &primary, &uq, &typ, &f, &col, &desc, &included, &size); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].Name != name {
			finish()
			ix := db.Index{Name: name, Unique: unique, Primary: primary, Method: strings.ReplaceAll(strings.ToLower(typ), "_", " ")}
			if size.Valid {
				ix.Size = humanSize(size.Int64)
			}
			out = append(out, ix)
			keys, include, filter, uniqueConstraint = nil, nil, f, uq
		}
		switch {
		case included:
			include = append(include, col)
		case desc:
			keys = append(keys, col+" DESC")
		default:
			keys = append(keys, col)
		}
	}
	finish()
	return out, wrapErr(rows.Err())
}

// keyList brackets index key columns, keeping a " DESC" suffix outside.
func keyList(cols []string) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		if name, ok := strings.CutSuffix(c, " DESC"); ok {
			out[i] = bracket(name) + " DESC"
		} else {
			out[i] = bracket(c)
		}
	}
	return out
}

func bracket(s string) string { return db.Dialect{Driver: db.SQLServer}.QuoteIdent(s) }

// foreignKeys lists the foreign keys whose `side` (parent_object_id for
// outgoing, referenced_object_id for incoming) is the object.
func (c *conn) foreignKeys(ctx context.Context, o object, side string) ([]db.ForeignKey, error) {
	p := o.prefix
	rows, err := c.meta.QueryContext(ctx, `
		SELECT fk.name, ps.name, pt.name, pc.name, rs.name, rt.name, rc.name,
		       fk.update_referential_action_desc, fk.delete_referential_action_desc
		FROM `+p+`sys.foreign_keys fk
		JOIN `+p+`sys.foreign_key_columns fkc ON fkc.constraint_object_id = fk.object_id
		JOIN `+p+`sys.objects pt ON pt.object_id = fk.parent_object_id
		JOIN `+p+`sys.schemas ps ON ps.schema_id = pt.schema_id
		JOIN `+p+`sys.all_columns pc ON pc.object_id = fkc.parent_object_id AND pc.column_id = fkc.parent_column_id
		JOIN `+p+`sys.objects rt ON rt.object_id = fk.referenced_object_id
		JOIN `+p+`sys.schemas rs ON rs.schema_id = rt.schema_id
		JOIN `+p+`sys.all_columns rc ON rc.object_id = fkc.referenced_object_id AND rc.column_id = fkc.referenced_column_id
		WHERE `+side+` = @p1
		ORDER BY ps.name, pt.name, fk.name, fkc.constraint_column_id`, o.id)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer rows.Close()
	var out []db.ForeignKey
	for rows.Next() {
		var name, schema, table, col, rschema, rtable, rcol, onUpd, onDel string
		if err := rows.Scan(&name, &schema, &table, &col, &rschema, &rtable, &rcol, &onUpd, &onDel); err != nil {
			return nil, err
		}
		if n := len(out); n == 0 || out[n-1].Name != name || out[n-1].Schema != schema || out[n-1].Table != table {
			out = append(out, db.ForeignKey{Name: name, Schema: schema, Table: table, RefSchema: rschema, RefTable: rtable,
				OnUpdate: strings.ReplaceAll(onUpd, "_", " "), OnDelete: strings.ReplaceAll(onDel, "_", " ")})
		}
		fk := &out[len(out)-1]
		fk.Columns = append(fk.Columns, col)
		fk.RefColumns = append(fk.RefColumns, rcol)
	}
	return out, wrapErr(rows.Err())
}

// constraints lists check constraints and the unique constraints found
// among the indexes.
func (c *conn) constraints(ctx context.Context, o object, ixs []db.Index) ([]db.Constraint, error) {
	var out []db.Constraint
	rows, err := c.meta.QueryContext(ctx, `
		SELECT name, definition FROM `+o.prefix+`sys.check_constraints WHERE parent_object_id = @p1 ORDER BY name`, o.id)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var con db.Constraint
		if err := rows.Scan(&con.Name, &con.Definition); err != nil {
			return nil, err
		}
		con.Type = "CHECK"
		con.Definition = "CHECK " + con.Definition
		out = append(out, con)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapErr(err)
	}
	for _, ix := range ixs {
		if strings.HasPrefix(ix.Definition, "UNIQUE ") {
			out = append(out, db.Constraint{Name: ix.Name, Type: "UNIQUE", Definition: ix.Definition})
		}
	}
	return out, nil
}

func (c *conn) triggers(ctx context.Context, o object) ([]db.Trigger, error) {
	p := o.prefix
	rows, err := c.meta.QueryContext(ctx, `
		SELECT t.name, t.is_instead_of_trigger, te.type_desc, m.definition
		FROM `+p+`sys.triggers t
		JOIN `+p+`sys.trigger_events te ON te.object_id = t.object_id
		LEFT JOIN `+p+`sys.sql_modules m ON m.object_id = t.object_id
		WHERE t.parent_id = @p1
		ORDER BY t.name, te.type`, o.id)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer rows.Close()
	var out []db.Trigger
	for rows.Next() {
		var name, event string
		var instead bool
		var def sql.NullString
		if err := rows.Scan(&name, &instead, &event, &def); err != nil {
			return nil, err
		}
		if n := len(out); n > 0 && out[n-1].Name == name {
			out[n-1].Event += " OR " + event
			continue
		}
		tr := db.Trigger{Name: name, Timing: "AFTER", Event: event, Definition: def.String}
		if instead {
			tr.Timing = "INSTEAD OF"
		}
		out = append(out, tr)
	}
	return out, wrapErr(rows.Err())
}

// stats are best effort: row counts and sizes need no special permission,
// but are skipped silently on error.
func (c *conn) stats(ctx context.Context, o object) []db.KV {
	p := o.prefix
	var out []db.KV
	var created, modified string
	if c.meta.QueryRowContext(ctx, `
		SELECT CONVERT(nvarchar(19), create_date, 120), CONVERT(nvarchar(19), modify_date, 120)
		FROM `+p+`sys.all_objects WHERE object_id = @p1`, o.id).Scan(&created, &modified) == nil {
		out = append(out, db.KV{Key: "Created", Value: created}, db.KV{Key: "Modified", Value: modified})
	}
	if o.typ != "U" && o.typ != "V" {
		return out
	}
	var rowCount, data, index, total sql.NullInt64
	err := c.meta.QueryRowContext(ctx, `
		SELECT SUM(CASE WHEN pt.index_id IN (0, 1) THEN pt.rows END),
		       (SELECT CAST(SUM(a.data_pages) AS bigint) * 8192 FROM `+p+`sys.partitions x
		        JOIN `+p+`sys.allocation_units a ON a.container_id = x.partition_id WHERE x.object_id = @p1),
		       (SELECT CAST(SUM(a.used_pages - a.data_pages) AS bigint) * 8192 FROM `+p+`sys.partitions x
		        JOIN `+p+`sys.allocation_units a ON a.container_id = x.partition_id WHERE x.object_id = @p1),
		       (SELECT CAST(SUM(a.total_pages) AS bigint) * 8192 FROM `+p+`sys.partitions x
		        JOIN `+p+`sys.allocation_units a ON a.container_id = x.partition_id WHERE x.object_id = @p1)
		FROM `+p+`sys.partitions pt WHERE pt.object_id = @p1`, o.id).Scan(&rowCount, &data, &index, &total)
	if err != nil || !rowCount.Valid {
		return out
	}
	out = append(out, db.KV{Key: "Rows (approx.)", Value: strconv.FormatInt(rowCount.Int64, 10)})
	for _, kv := range []struct {
		k string
		v sql.NullInt64
	}{{"Data size", data}, {"Index size", index}, {"Total size", total}} {
		if kv.v.Valid {
			out = append(out, db.KV{Key: kv.k, Value: humanSize(kv.v.Int64)})
		}
	}
	return out
}

// DDL returns the definition of a module (view, procedure, function,
// trigger) as stored by the server; tables and sequences are scripted from
// the catalog.
func (c *conn) DDL(ctx context.Context, ref db.ObjectRef) (string, error) {
	o, err := c.object(ctx, ref)
	if err != nil {
		return "", err
	}
	if ref.Schema == "" {
		ref.Schema = "dbo"
	}
	switch o.typ {
	case "U":
		return c.tableDDL(ctx, ref)
	case "SO":
		return c.sequenceDDL(ctx, o, ref)
	}
	var def sql.NullString
	if err := c.meta.QueryRowContext(ctx, `SELECT definition FROM `+o.prefix+`sys.sql_modules WHERE object_id = @p1`, o.id).Scan(&def); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", wrapErr(err)
	}
	if !def.Valid {
		return "", fmt.Errorf("the definition of %s.%s is not available (encrypted, or no VIEW DEFINITION permission)", ref.Schema, ref.Name)
	}
	return strings.TrimSpace(def.String), nil
}

func (c *conn) tableDDL(ctx context.Context, ref db.ObjectRef) (string, error) {
	t, err := c.TableDetail(ctx, ref)
	if err != nil {
		return "", err
	}
	d := c.Dialect()
	name := d.Qualified(ref)
	var lines []string
	for _, col := range t.Columns {
		l := "    " + d.QuoteIdent(col.Name) + " "
		if strings.HasPrefix(col.Extra, "AS ") {
			lines = append(lines, l+col.Extra)
			continue
		}
		l += col.Type
		if col.Extra != "" {
			l += " " + col.Extra
		}
		if col.Nullable {
			l += " NULL"
		} else {
			l += " NOT NULL"
		}
		if col.Default != nil {
			l += " DEFAULT " + *col.Default
		}
		lines = append(lines, l)
	}
	var after []string
	for _, ix := range t.Indexes {
		switch {
		case ix.Primary, strings.HasPrefix(ix.Definition, "UNIQUE "):
			lines = append(lines, "    CONSTRAINT "+d.QuoteIdent(ix.Name)+" "+ix.Definition)
		default:
			after = append(after, strings.Replace(ix.Definition, " (", " ON "+name+" (", 1)+";")
		}
	}
	for _, con := range t.Constraints {
		if con.Type == "CHECK" {
			lines = append(lines, "    CONSTRAINT "+d.QuoteIdent(con.Name)+" "+con.Definition)
		}
	}
	for _, fk := range t.ForeignKeys {
		l := "    CONSTRAINT " + d.QuoteIdent(fk.Name) + " FOREIGN KEY (" + strings.Join(keyList(fk.Columns), ", ") +
			") REFERENCES " + d.Qualified(db.ObjectRef{Schema: fk.RefSchema, Name: fk.RefTable}) +
			" (" + strings.Join(keyList(fk.RefColumns), ", ") + ")"
		if fk.OnDelete != "" && fk.OnDelete != "NO ACTION" {
			l += " ON DELETE " + fk.OnDelete
		}
		if fk.OnUpdate != "" && fk.OnUpdate != "NO ACTION" {
			l += " ON UPDATE " + fk.OnUpdate
		}
		lines = append(lines, l)
	}
	var b strings.Builder
	b.WriteString("-- scripted from the catalog by squi\nCREATE TABLE " + name + " (\n" + strings.Join(lines, ",\n") + "\n);\n")
	for _, a := range after {
		b.WriteString(a + "\n")
	}
	for _, tr := range t.Triggers {
		if tr.Definition != "" {
			b.WriteString("GO\n" + strings.TrimSpace(tr.Definition) + "\nGO\n")
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func (c *conn) sequenceDDL(ctx context.Context, o object, ref db.ObjectRef) (string, error) {
	var typ, start, inc, minV, maxV string
	var cycle bool
	var cache sql.NullInt64
	err := c.meta.QueryRowContext(ctx, `
		SELECT TYPE_NAME(system_type_id), CAST(start_value AS nvarchar(40)), CAST(increment AS nvarchar(40)),
		       CAST(minimum_value AS nvarchar(40)), CAST(maximum_value AS nvarchar(40)), is_cycling, CAST(cache_size AS bigint)
		FROM `+o.prefix+`sys.sequences WHERE object_id = @p1`, o.id).Scan(&typ, &start, &inc, &minV, &maxV, &cycle, &cache)
	if err != nil {
		return "", wrapErr(err)
	}
	s := "CREATE SEQUENCE " + c.Dialect().Qualified(ref) + " AS " + typ + "\n    START WITH " + start +
		"\n    INCREMENT BY " + inc + "\n    MINVALUE " + minV + "\n    MAXVALUE " + maxV
	if cycle {
		s += "\n    CYCLE"
	}
	if cache.Valid {
		s += "\n    CACHE " + strconv.FormatInt(cache.Int64, 10)
	}
	return s + ";", nil
}
