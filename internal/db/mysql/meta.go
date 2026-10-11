package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"github.com/farvisun/squi/internal/db"
)

// systemSchemas are listed after user databases, in this order.
var systemSchemas = []string{"information_schema", "mysql", "performance_schema", "sys"}

func (c *conn) Databases(ctx context.Context) ([]db.Database, error) {
	rows, err := c.meta.QueryContext(ctx, `
		SELECT s.SCHEMA_NAME, COALESCE(SUM(t.DATA_LENGTH + t.INDEX_LENGTH), 0)
		FROM information_schema.SCHEMATA s
		LEFT JOIN information_schema.TABLES t ON t.TABLE_SCHEMA = s.SCHEMA_NAME
		GROUP BY s.SCHEMA_NAME`)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer rows.Close()
	var out []db.Database
	for rows.Next() {
		var d db.Database
		var size int64
		if err := rows.Scan(&d.Name, &size); err != nil {
			return nil, err
		}
		d.Size = humanSize(size)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapErr(err)
	}
	rank := func(name string) int { return slices.Index(systemSchemas, strings.ToLower(name)) }
	slices.SortFunc(out, func(a, b db.Database) int {
		if ra, rb := rank(a.Name), rank(b.Name); ra != rb {
			return ra - rb
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out, nil
}

// Schemas returns []string{database}: in MySQL databases are schemas.
func (c *conn) Schemas(_ context.Context, database string) ([]string, error) {
	return []string{database}, nil
}

var kindRank = map[db.ObjectKind]int{
	db.KindTable: 0, db.KindView: 1, db.KindSequence: 2,
	db.KindFunction: 3, db.KindProcedure: 4, db.KindEvent: 5,
}

func (c *conn) Objects(ctx context.Context, database, schema string) ([]db.Object, error) {
	if schema == "" {
		schema = database
	}
	rows, err := c.meta.QueryContext(ctx, `
		SELECT TABLE_NAME, TABLE_TYPE, TABLE_COMMENT FROM information_schema.TABLES WHERE TABLE_SCHEMA = ?
		UNION ALL
		SELECT ROUTINE_NAME, ROUTINE_TYPE, ROUTINE_COMMENT FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = ?
		UNION ALL
		SELECT EVENT_NAME, 'EVENT', EVENT_COMMENT FROM information_schema.EVENTS WHERE EVENT_SCHEMA = ?`,
		schema, schema, schema)
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
		o := db.Object{Schema: schema, Name: name, Comment: comment.String}
		switch typ {
		case "BASE TABLE", "SYSTEM VERSIONED":
			o.Kind = db.KindTable
		case "VIEW", "SYSTEM VIEW":
			o.Kind, o.Comment = db.KindView, viewComment(o.Comment)
		case "SEQUENCE":
			o.Kind = db.KindSequence
		case "FUNCTION":
			o.Kind = db.KindFunction
		case "PROCEDURE":
			o.Kind = db.KindProcedure
		case "EVENT":
			o.Kind = db.KindEvent
		default:
			continue
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapErr(err)
	}
	slices.SortFunc(out, func(a, b db.Object) int {
		if a.Kind != b.Kind {
			return kindRank[a.Kind] - kindRank[b.Kind]
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out, nil
}

// viewComment drops the placeholder comment MySQL reports for views.
func viewComment(s string) string {
	if s == "VIEW" {
		return ""
	}
	return s
}

func (c *conn) AllColumns(ctx context.Context, database, schema string) (map[string][]string, error) {
	if schema == "" {
		schema = database
	}
	rows, err := c.meta.QueryContext(ctx, `
		SELECT TABLE_NAME, COLUMN_NAME FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME, ORDINAL_POSITION`, schema)
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

// refSchema returns the schema (= database) a ref points into.
func refSchema(ref db.ObjectRef) string {
	if ref.Database != "" {
		return ref.Database
	}
	return ref.Schema
}

func (c *conn) TableDetail(ctx context.Context, ref db.ObjectRef) (*db.TableDetail, error) {
	schema := refSchema(ref)
	td := &db.TableDetail{Ref: ref}
	steps := []func(context.Context, string, string, *db.TableDetail) error{
		c.tableStats, c.tableColumns, c.tableIndexes, c.tableForeignKeys,
		c.tableReferencedBy, c.tableChecks, c.tableTriggers,
	}
	for _, step := range steps {
		if err := step(ctx, schema, ref.Name, td); err != nil {
			return nil, err
		}
	}
	return td, nil
}

func (c *conn) tableStats(ctx context.Context, schema, table string, td *db.TableDetail) error {
	var (
		typ                                   string
		engine, rowFormat, coll, comment      sql.NullString
		created, updated                      sql.NullString
		rows, avg, data, index, free, autoInc sql.NullInt64
	)
	err := c.meta.QueryRowContext(ctx, `
		SELECT TABLE_TYPE, ENGINE, ROW_FORMAT, TABLE_ROWS, AVG_ROW_LENGTH, DATA_LENGTH, INDEX_LENGTH,
		       DATA_FREE, AUTO_INCREMENT, TABLE_COLLATION, CREATE_TIME, UPDATE_TIME, TABLE_COMMENT
		FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`, schema, table).
		Scan(&typ, &engine, &rowFormat, &rows, &avg, &data, &index, &free, &autoInc, &coll, &created, &updated, &comment)
	if err == sql.ErrNoRows {
		return fmt.Errorf("table %s.%s not found", schema, table)
	}
	if err != nil {
		return wrapErr(err)
	}
	td.Comment = comment.String
	if strings.Contains(typ, "VIEW") {
		td.Comment = viewComment(td.Comment)
	}
	add := func(k, v string, ok bool) {
		if ok && v != "" {
			td.Stats = append(td.Stats, db.KV{Key: k, Value: v})
		}
	}
	num := func(n sql.NullInt64) string { return fmt.Sprint(n.Int64) }
	size := func(n sql.NullInt64) string { return humanSize(n.Int64) }
	add("Type", typ, true)
	add("Engine", engine.String, engine.Valid)
	add("Row format", rowFormat.String, rowFormat.Valid)
	add("Rows (estimate)", num(rows), rows.Valid)
	add("Avg row length", size(avg), avg.Valid)
	add("Data size", size(data), data.Valid)
	add("Index size", size(index), index.Valid)
	add("Data free", size(free), free.Valid)
	add("Auto increment", num(autoInc), autoInc.Valid)
	add("Collation", coll.String, coll.Valid)
	add("Created", created.String, created.Valid)
	add("Updated", updated.String, updated.Valid)
	add("Comment", td.Comment, true)
	return nil
}

func (c *conn) tableColumns(ctx context.Context, schema, table string, td *db.TableDetail) error {
	rows, err := c.meta.QueryContext(ctx, `
		SELECT ORDINAL_POSITION, COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT, COLUMN_KEY,
		       EXTRA, GENERATION_EXPRESSION, COLUMN_COMMENT
		FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
		ORDER BY ORDINAL_POSITION`, schema, table)
	if err != nil {
		return wrapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var col db.Column
		var nullable, key string
		var def, extra, gen, comment sql.NullString
		if err := rows.Scan(&col.Position, &col.Name, &col.Type, &nullable, &def, &key, &extra, &gen, &comment); err != nil {
			return err
		}
		col.Nullable = nullable == "YES"
		col.IsPK = key == "PRI"
		col.Comment = comment.String
		col.Default = c.normalizeDefault(def)
		col.Extra = strings.TrimSpace(strings.ReplaceAll(extra.String, "DEFAULT_GENERATED", ""))
		if gen.String != "" {
			col.Extra = strings.TrimSpace(col.Extra + " AS (" + gen.String + ")")
		}
		td.Columns = append(td.Columns, col)
	}
	return wrapErr(rows.Err())
}

// normalizeDefault returns a column default as MySQL reports it. MariaDB
// reports a NULL default as the string "NULL" and quotes string literals.
func (c *conn) normalizeDefault(def sql.NullString) *string {
	if !def.Valid {
		return nil
	}
	s := def.String
	if c.mariadb {
		if s == "NULL" {
			return nil
		}
		if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
			s = strings.ReplaceAll(s[1:len(s)-1], "''", "'")
		}
	}
	return &s
}

func (c *conn) tableIndexes(ctx context.Context, schema, table string, td *db.TableDetail) error {
	expr := "NULL"
	if c.mysqlAtLeast(8, 0, 13) {
		expr = "EXPRESSION"
	}
	rows, err := c.meta.QueryContext(ctx, `
		SELECT INDEX_NAME, NON_UNIQUE, COLUMN_NAME, `+expr+`, SUB_PART, INDEX_TYPE
		FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
		ORDER BY INDEX_NAME <> 'PRIMARY', INDEX_NAME, SEQ_IN_INDEX`, schema, table)
	if err != nil {
		return wrapErr(err)
	}
	defer rows.Close()
	d := c.Dialect()
	var defs []string
	for rows.Next() {
		var name, typ string
		var nonUnique int
		var col, expression, subPart sql.NullString
		if err := rows.Scan(&name, &nonUnique, &col, &expression, &subPart, &typ); err != nil {
			return err
		}
		part, quoted := col.String, d.QuoteIdent(col.String)
		if !col.Valid {
			part = "(" + expression.String + ")"
			quoted = part
		} else if subPart.Valid {
			part += "(" + subPart.String + ")"
			quoted += "(" + subPart.String + ")"
		}
		n := len(td.Indexes)
		if n == 0 || td.Indexes[n-1].Name != name {
			td.Indexes = append(td.Indexes, db.Index{
				Name:    name,
				Unique:  nonUnique == 0,
				Primary: name == "PRIMARY",
				Method:  strings.ToLower(typ),
			})
			defs = append(defs, "")
			n++
		}
		ix := &td.Indexes[n-1]
		ix.Columns = append(ix.Columns, part)
		if defs[n-1] != "" {
			defs[n-1] += ", "
		}
		defs[n-1] += quoted
	}
	if err := rows.Err(); err != nil {
		return wrapErr(err)
	}
	for i := range td.Indexes {
		ix := &td.Indexes[i]
		switch {
		case ix.Primary:
			ix.Definition = "PRIMARY KEY (" + defs[i] + ")"
		case ix.Unique:
			ix.Definition = "UNIQUE KEY " + d.QuoteIdent(ix.Name) + " (" + defs[i] + ")"
			td.Constraints = append(td.Constraints, db.Constraint{Name: ix.Name, Type: "UNIQUE", Definition: "UNIQUE (" + defs[i] + ")"})
		case ix.Method == "fulltext" || ix.Method == "spatial":
			ix.Definition = strings.ToUpper(ix.Method) + " KEY " + d.QuoteIdent(ix.Name) + " (" + defs[i] + ")"
		default:
			ix.Definition = "KEY " + d.QuoteIdent(ix.Name) + " (" + defs[i] + ") USING " + strings.ToUpper(ix.Method)
		}
	}
	return nil
}

const fkSelect = `
	SELECT k.CONSTRAINT_NAME, k.TABLE_SCHEMA, k.TABLE_NAME, k.COLUMN_NAME,
	       k.REFERENCED_TABLE_SCHEMA, k.REFERENCED_TABLE_NAME, k.REFERENCED_COLUMN_NAME,
	       r.UPDATE_RULE, r.DELETE_RULE
	FROM information_schema.KEY_COLUMN_USAGE k
	JOIN information_schema.REFERENTIAL_CONSTRAINTS r
	  ON r.CONSTRAINT_SCHEMA = k.CONSTRAINT_SCHEMA AND r.CONSTRAINT_NAME = k.CONSTRAINT_NAME
	 AND r.TABLE_NAME = k.TABLE_NAME
	WHERE `

func (c *conn) tableForeignKeys(ctx context.Context, schema, table string, td *db.TableDetail) error {
	var err error
	td.ForeignKeys, err = c.foreignKeys(ctx, fkSelect+`k.TABLE_SCHEMA = ? AND k.TABLE_NAME = ?
		AND k.REFERENCED_TABLE_NAME IS NOT NULL
		ORDER BY k.CONSTRAINT_NAME, k.ORDINAL_POSITION`, schema, table)
	return err
}

func (c *conn) tableReferencedBy(ctx context.Context, schema, table string, td *db.TableDetail) error {
	var err error
	td.ReferencedBy, err = c.foreignKeys(ctx, fkSelect+`k.REFERENCED_TABLE_SCHEMA = ? AND k.REFERENCED_TABLE_NAME = ?
		ORDER BY k.TABLE_SCHEMA, k.TABLE_NAME, k.CONSTRAINT_NAME, k.ORDINAL_POSITION`, schema, table)
	return err
}

// foreignKeys groups KEY_COLUMN_USAGE rows (ordered by constraint and
// position) into multi-column keys.
func (c *conn) foreignKeys(ctx context.Context, q string, args ...any) ([]db.ForeignKey, error) {
	rows, err := c.meta.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer rows.Close()
	var out []db.ForeignKey
	for rows.Next() {
		var fk db.ForeignKey
		var col, refCol string
		if err := rows.Scan(&fk.Name, &fk.Schema, &fk.Table, &col, &fk.RefSchema, &fk.RefTable, &refCol, &fk.OnUpdate, &fk.OnDelete); err != nil {
			return nil, err
		}
		if n := len(out); n > 0 {
			last := &out[n-1]
			if last.Name == fk.Name && last.Schema == fk.Schema && last.Table == fk.Table {
				last.Columns = append(last.Columns, col)
				last.RefColumns = append(last.RefColumns, refCol)
				continue
			}
		}
		fk.Columns, fk.RefColumns = []string{col}, []string{refCol}
		out = append(out, fk)
	}
	return out, wrapErr(rows.Err())
}

// tableChecks loads CHECK constraints (MySQL 8.0.16+, MariaDB 10.2+).
// MariaDB names column-level checks after the column, so names are only
// unique per table there and CHECK_CONSTRAINTS carries TABLE_NAME.
func (c *conn) tableChecks(ctx context.Context, schema, table string, td *db.TableDetail) error {
	q := `SELECT tc.CONSTRAINT_NAME, cc.CHECK_CLAUSE
		FROM information_schema.TABLE_CONSTRAINTS tc
		JOIN information_schema.CHECK_CONSTRAINTS cc
		  ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
		WHERE tc.TABLE_SCHEMA = ? AND tc.TABLE_NAME = ? AND tc.CONSTRAINT_TYPE = 'CHECK'
		ORDER BY tc.CONSTRAINT_NAME`
	if c.mariadb {
		q = `SELECT CONSTRAINT_NAME, CHECK_CLAUSE FROM information_schema.CHECK_CONSTRAINTS
			WHERE CONSTRAINT_SCHEMA = ? AND TABLE_NAME = ? ORDER BY CONSTRAINT_NAME`
	}
	rows, err := c.meta.QueryContext(ctx, q, schema, table)
	if errNumber(err) == 1109 { // unknown table: server predates CHECK constraints
		return nil
	}
	if err != nil {
		return wrapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, clause string
		if err := rows.Scan(&name, &clause); err != nil {
			return err
		}
		if !strings.HasPrefix(clause, "(") {
			clause = "(" + clause + ")"
		}
		td.Constraints = append(td.Constraints, db.Constraint{Name: name, Type: "CHECK", Definition: "CHECK " + clause})
	}
	return wrapErr(rows.Err())
}

const triggerSelect = `
	SELECT TRIGGER_NAME, ACTION_TIMING, EVENT_MANIPULATION, ACTION_STATEMENT
	FROM information_schema.TRIGGERS WHERE EVENT_OBJECT_SCHEMA = ? AND EVENT_OBJECT_TABLE = ?
	ORDER BY ACTION_TIMING, EVENT_MANIPULATION, ACTION_ORDER`

func (c *conn) tableTriggers(ctx context.Context, schema, table string, td *db.TableDetail) error {
	rows, err := c.meta.QueryContext(ctx, triggerSelect, schema, table)
	if err != nil {
		return wrapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var t db.Trigger
		if err := rows.Scan(&t.Name, &t.Timing, &t.Event, &t.Definition); err != nil {
			return err
		}
		td.Triggers = append(td.Triggers, t)
	}
	return wrapErr(rows.Err())
}

// DDL returns the CREATE statement of ref; for tables it is followed by
// the CREATE TRIGGER statements of the table's triggers.
func (c *conn) DDL(ctx context.Context, ref db.ObjectRef) (string, error) {
	d := c.Dialect()
	schema := refSchema(ref)
	name := d.QuoteIdent(schema) + "." + d.QuoteIdent(ref.Name)
	what := map[db.ObjectKind]string{
		db.KindView:      "VIEW",
		db.KindFunction:  "FUNCTION",
		db.KindProcedure: "PROCEDURE",
		db.KindEvent:     "EVENT",
		db.KindTrigger:   "TRIGGER",
		db.KindSequence:  "SEQUENCE",
	}[ref.Kind]
	if what == "" {
		what = "TABLE"
	}
	stmt, err := c.showCreate(ctx, "SHOW CREATE "+what+" "+name)
	if err != nil || what != "TABLE" {
		return stmt, err
	}

	t := &db.TableDetail{}
	if err := c.tableTriggers(ctx, schema, ref.Name, t); err != nil {
		return "", err
	}
	parts := []string{stmt}
	for _, tr := range t.Triggers {
		s, err := c.showCreate(ctx, "SHOW CREATE TRIGGER "+d.QuoteIdent(schema)+"."+d.QuoteIdent(tr.Name))
		if err != nil {
			return "", err
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "\n\n"), nil
}

// showCreate runs a SHOW CREATE statement and returns its definition
// column ("Create Table", "Create View", "SQL Original Statement", …)
// terminated by a semicolon.
func (c *conn) showCreate(ctx context.Context, q string) (string, error) {
	t, err := c.queryTable(ctx, q)
	if err != nil {
		return "", err
	}
	if len(t.Rows) == 0 {
		return "", fmt.Errorf("%s: no result", q)
	}
	for i, col := range t.Columns {
		if !strings.HasPrefix(col, "Create ") && col != "SQL Original Statement" {
			continue
		}
		if cell := t.Rows[0][i]; !cell.Null {
			return strings.TrimRight(cell.S, "; \n") + ";", nil
		}
		return "", fmt.Errorf("%s: definition not visible (insufficient privileges)", q)
	}
	return "", fmt.Errorf("%s: unexpected result columns %v", q, t.Columns)
}
