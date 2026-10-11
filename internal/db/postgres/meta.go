package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/farvisun/squi/internal/db"
)

// Databases lists non-template databases with their size (empty when the
// user may not connect) and comment.
func (c *conn) Databases(ctx context.Context) ([]db.Database, error) {
	p, err := c.pool("")
	if err != nil {
		return nil, err
	}
	rows, _ := p.Query(ctx, `
		SELECT datname::text,
		       CASE WHEN has_database_privilege(oid, 'CONNECT')
		            THEN pg_size_pretty(pg_database_size(oid)) ELSE '' END,
		       coalesce(shobj_description(oid, 'pg_database'), '')
		FROM pg_database
		WHERE NOT datistemplate
		ORDER BY datname`)
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (d db.Database, err error) {
		err = r.Scan(&d.Name, &d.Size, &d.Comment)
		return d, err
	})
}

// Schemas lists schemas of database, user schemas first and the system
// schemas information_schema and pg_catalog last.
func (c *conn) Schemas(ctx context.Context, database string) ([]string, error) {
	p, err := c.pool(database)
	if err != nil {
		return nil, err
	}
	rows, _ := p.Query(ctx, `
		SELECT nspname::text
		FROM pg_namespace
		WHERE nspname <> 'pg_toast'
		  AND nspname NOT LIKE 'pg\_temp\_%'
		  AND nspname NOT LIKE 'pg\_toast\_temp\_%'
		ORDER BY nspname IN ('information_schema', 'pg_catalog'), nspname`)
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// Objects lists relations, functions and procedures of a schema, ordered by
// kind then name. Overloaded routines are named "fname(argtypes)".
func (c *conn) Objects(ctx context.Context, database, schema string) ([]db.Object, error) {
	p, err := c.pool(database)
	if err != nil {
		return nil, err
	}
	rows, _ := p.Query(ctx, `
		SELECT name, kind, comment FROM (
		    SELECT c.relname::text AS name,
		           CASE c.relkind WHEN 'v' THEN 'view' WHEN 'm' THEN 'matview'
		                          WHEN 'S' THEN 'sequence' ELSE 'table' END AS kind,
		           CASE c.relkind WHEN 'v' THEN 2 WHEN 'm' THEN 3 WHEN 'S' THEN 4 ELSE 1 END AS rank,
		           coalesce(obj_description(c.oid, 'pg_class'), '') AS comment
		    FROM pg_class c
		    JOIN pg_namespace n ON n.oid = c.relnamespace
		    WHERE n.nspname = $1 AND c.relkind IN ('r', 'p', 'f', 'v', 'm', 'S')
		  UNION ALL
		    SELECT CASE WHEN count(*) OVER (PARTITION BY p.proname) > 1
		                THEN p.proname || '(' || pg_get_function_identity_arguments(p.oid) || ')'
		                ELSE p.proname::text END,
		           CASE p.prokind WHEN 'p' THEN 'procedure' ELSE 'function' END,
		           CASE p.prokind WHEN 'p' THEN 6 ELSE 5 END,
		           coalesce(obj_description(p.oid, 'pg_proc'), '')
		    FROM pg_proc p
		    JOIN pg_namespace n ON n.oid = p.pronamespace
		    WHERE n.nspname = $1 AND p.prokind IN ('f', 'p')
		) o
		ORDER BY rank, name`, schema)
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (db.Object, error) {
		o := db.Object{Schema: schema}
		err := r.Scan(&o.Name, &o.Kind, &o.Comment)
		return o, err
	})
}

// AllColumns maps every table, view and matview of schema to its column
// names in definition order.
func (c *conn) AllColumns(ctx context.Context, database, schema string) (map[string][]string, error) {
	p, err := c.pool(database)
	if err != nil {
		return nil, err
	}
	rows, _ := p.Query(ctx, `
		SELECT c.relname::text, array_agg(a.attname::text ORDER BY a.attnum)
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relkind IN ('r', 'p', 'f', 'v', 'm')
		  AND a.attnum > 0 AND NOT a.attisdropped
		GROUP BY c.relname`, schema)
	out := map[string][]string{}
	var (
		name string
		cols []string
	)
	_, err = pgx.ForEachRow(rows, []any{&name, &cols}, func() error {
		out[name] = cols
		return nil
	})
	return out, err
}

// relation identifies a pg_class row.
type relation struct {
	oid  uint32
	kind byte // pg_class.relkind
}

const relationSQL = `
	SELECT c.oid, c.relkind::text
	FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
	WHERE n.nspname = $1 AND c.relname = $2`

// relation resolves ref to a pg_class row and returns the pool of its
// database.
func (c *conn) relation(ctx context.Context, ref db.ObjectRef) (querier, relation, error) {
	var rel relation
	p, err := c.pool(ref.Database)
	if err != nil {
		return nil, rel, err
	}
	var kind string
	err = p.QueryRow(ctx, relationSQL, ref.Schema, ref.Name).Scan(&rel.oid, &kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, rel, fmt.Errorf("postgres: relation %s.%s not found", ref.Schema, ref.Name)
	}
	if err != nil {
		return nil, rel, err
	}
	rel.kind = kind[0]
	return p, rel, nil
}

// querier is the subset of pgxpool.Pool used by metadata code.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// TableDetail describes a table, view or materialized view.
func (c *conn) TableDetail(ctx context.Context, ref db.ObjectRef) (*db.TableDetail, error) {
	q, rel, err := c.relation(ctx, ref)
	if err != nil {
		return nil, err
	}
	ref.Kind = relKind(rel.kind)
	t := &db.TableDetail{Ref: ref}
	if err := q.QueryRow(ctx, `SELECT coalesce(obj_description($1, 'pg_class'), '')`, rel.oid).Scan(&t.Comment); err != nil {
		return nil, err
	}
	if t.Columns, err = columns(ctx, q, rel.oid); err != nil {
		return nil, err
	}
	if rel.kind != 'v' {
		if t.Indexes, err = indexes(ctx, q, rel.oid); err != nil {
			return nil, err
		}
	}
	if rel.kind == 'r' || rel.kind == 'p' || rel.kind == 'f' {
		if t.ForeignKeys, err = foreignKeys(ctx, q, "con.conrelid = $1", rel.oid); err != nil {
			return nil, err
		}
		if t.ReferencedBy, err = foreignKeys(ctx, q, "con.confrelid = $1 AND con.conparentid = 0", rel.oid); err != nil {
			return nil, err
		}
		if t.Constraints, err = constraints(ctx, q, rel.oid); err != nil {
			return nil, err
		}
	}
	if t.Triggers, err = triggers(ctx, q, rel.oid); err != nil {
		return nil, err
	}
	if t.Stats, err = stats(ctx, q, rel); err != nil {
		return nil, err
	}
	return t, nil
}

func relKind(k byte) db.ObjectKind {
	switch k {
	case 'v':
		return db.KindView
	case 'm':
		return db.KindMatView
	case 'S':
		return db.KindSequence
	}
	return db.KindTable
}

func columns(ctx context.Context, q querier, oid uint32) ([]db.Column, error) {
	rows, _ := q.Query(ctx, `
		SELECT row_number() OVER (ORDER BY a.attnum)::int,
		       a.attname::text,
		       format_type(a.atttypid, a.atttypmod),
		       NOT a.attnotnull,
		       CASE WHEN a.attgenerated = '' THEN pg_get_expr(d.adbin, d.adrelid) END,
		       coalesce(a.attnum = ANY (pk.conkey), false),
		       CASE WHEN a.attidentity = 'a' THEN 'identity always'
		            WHEN a.attidentity = 'd' THEN 'identity by default'
		            WHEN a.attgenerated = 's' THEN 'generated: ' || pg_get_expr(d.adbin, d.adrelid)
		            ELSE '' END,
		       coalesce(col_description(a.attrelid, a.attnum), '')
		FROM pg_attribute a
		LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		LEFT JOIN pg_constraint pk ON pk.conrelid = a.attrelid AND pk.contype = 'p'
		WHERE a.attrelid = $1 AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY a.attnum`, oid)
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (c db.Column, err error) {
		err = r.Scan(&c.Position, &c.Name, &c.Type, &c.Nullable, &c.Default, &c.IsPK, &c.Extra, &c.Comment)
		return c, err
	})
}

func indexes(ctx context.Context, q querier, oid uint32) ([]db.Index, error) {
	rows, _ := q.Query(ctx, `
		SELECT ic.relname::text,
		       ARRAY(SELECT pg_get_indexdef(i.indexrelid, k, true)
		             FROM generate_series(1, i.indnkeyatts) k ORDER BY k),
		       i.indisunique, i.indisprimary, am.amname::text,
		       pg_size_pretty(pg_relation_size(i.indexrelid)),
		       pg_get_indexdef(i.indexrelid)
		FROM pg_index i
		JOIN pg_class ic ON ic.oid = i.indexrelid
		JOIN pg_am am ON am.oid = ic.relam
		WHERE i.indrelid = $1
		ORDER BY i.indisprimary DESC, ic.relname`, oid)
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ix db.Index, err error) {
		err = r.Scan(&ix.Name, &ix.Columns, &ix.Unique, &ix.Primary, &ix.Method, &ix.Size, &ix.Definition)
		return ix, err
	})
}

// fkAction renders a pg_constraint action code column as SQL words.
func fkAction(col string) string {
	return `CASE ` + col + ` WHEN 'c' THEN 'CASCADE' WHEN 'n' THEN 'SET NULL' WHEN 'd' THEN 'SET DEFAULT'
	        WHEN 'r' THEN 'RESTRICT' ELSE 'NO ACTION' END`
}

// foreignKeys lists foreign keys matching where (with $1 = relation oid).
func foreignKeys(ctx context.Context, q querier, where string, oid uint32) ([]db.ForeignKey, error) {
	rows, _ := q.Query(ctx, `
		SELECT con.conname::text, n.nspname::text, c.relname::text,
		       ARRAY(SELECT a.attname::text
		             FROM unnest(con.conkey) WITH ORDINALITY k(attnum, ord)
		             JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum
		             ORDER BY k.ord),
		       rn.nspname::text, rc.relname::text,
		       ARRAY(SELECT a.attname::text
		             FROM unnest(con.confkey) WITH ORDINALITY k(attnum, ord)
		             JOIN pg_attribute a ON a.attrelid = con.confrelid AND a.attnum = k.attnum
		             ORDER BY k.ord),
		       `+fkAction("con.confupdtype")+`,
		       `+fkAction("con.confdeltype")+`
		FROM pg_constraint con
		JOIN pg_class c ON c.oid = con.conrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_class rc ON rc.oid = con.confrelid
		JOIN pg_namespace rn ON rn.oid = rc.relnamespace
		WHERE con.contype = 'f' AND `+where+`
		ORDER BY n.nspname, c.relname, con.conname`, oid)
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (fk db.ForeignKey, err error) {
		err = r.Scan(&fk.Name, &fk.Schema, &fk.Table, &fk.Columns, &fk.RefSchema, &fk.RefTable,
			&fk.RefColumns, &fk.OnUpdate, &fk.OnDelete)
		return fk, err
	})
}

// constraints lists check, unique and exclusion constraints.
func constraints(ctx context.Context, q querier, oid uint32) ([]db.Constraint, error) {
	rows, _ := q.Query(ctx, `
		SELECT conname::text,
		       CASE contype WHEN 'c' THEN 'CHECK' WHEN 'u' THEN 'UNIQUE' ELSE 'EXCLUDE' END,
		       pg_get_constraintdef(oid, true)
		FROM pg_constraint
		WHERE conrelid = $1 AND contype IN ('c', 'u', 'x')
		ORDER BY contype, conname`, oid)
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (c db.Constraint, err error) {
		err = r.Scan(&c.Name, &c.Type, &c.Definition)
		return c, err
	})
}

// Bits of pg_trigger.tgtype.
const (
	tgBefore   = 1 << 1
	tgInsert   = 1 << 2
	tgDelete   = 1 << 3
	tgUpdate   = 1 << 4
	tgTruncate = 1 << 5
	tgInstead  = 1 << 6
)

func triggers(ctx context.Context, q querier, oid uint32) ([]db.Trigger, error) {
	rows, _ := q.Query(ctx, `
		SELECT tgname::text, tgtype::int, pg_get_triggerdef(oid, true)
		FROM pg_trigger
		WHERE tgrelid = $1 AND NOT tgisinternal
		ORDER BY tgname`, oid)
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (t db.Trigger, err error) {
		var typ int
		if err = r.Scan(&t.Name, &typ, &t.Definition); err != nil {
			return t, err
		}
		switch {
		case typ&tgInstead != 0:
			t.Timing = "INSTEAD OF"
		case typ&tgBefore != 0:
			t.Timing = "BEFORE"
		default:
			t.Timing = "AFTER"
		}
		var ev []string
		for _, e := range []struct {
			bit  int
			name string
		}{{tgInsert, "INSERT"}, {tgUpdate, "UPDATE"}, {tgDelete, "DELETE"}, {tgTruncate, "TRUNCATE"}} {
			if typ&e.bit != 0 {
				ev = append(ev, e.name)
			}
		}
		t.Event = strings.Join(ev, " OR ")
		return t, nil
	})
}

// stats returns the key/value summary shown for a relation. Every value is
// rendered as text by the server; NULL rows are omitted.
func stats(ctx context.Context, q querier, rel relation) ([]db.KV, error) {
	type stat struct{ key, expr string }
	list := []stat{
		{"Kind", `CASE c.relkind WHEN 'r' THEN 'table' WHEN 'p' THEN 'partitioned table'
		          WHEN 'f' THEN 'foreign table' WHEN 'v' THEN 'view' WHEN 'm' THEN 'materialized view'
		          ELSE c.relkind::text END`},
		{"Owner", `pg_get_userbyid(c.relowner)::text`},
	}
	if rel.kind == 'v' {
		list = append(list, stat{"Definition length", `length(pg_get_viewdef(c.oid))::text`})
	} else {
		list = append(list,
			stat{"Rows (estimate)", `CASE WHEN c.reltuples < 0 THEN 'unknown' ELSE c.reltuples::bigint::text END`},
			stat{"Total size", `pg_size_pretty(pg_total_relation_size(c.oid))`},
			stat{"Table size", `pg_size_pretty(pg_table_size(c.oid))`},
			stat{"Index size", `pg_size_pretty(pg_indexes_size(c.oid))`},
			stat{"TOAST size", `CASE WHEN c.reltoastrelid <> 0 THEN pg_size_pretty(pg_total_relation_size(c.reltoastrelid)) END`},
			stat{"Last vacuum", `coalesce(s.last_vacuum::text, 'never')`},
			stat{"Last autovacuum", `coalesce(s.last_autovacuum::text, 'never')`},
			stat{"Last analyze", `coalesce(s.last_analyze::text, 'never')`},
			stat{"Last autoanalyze", `coalesce(s.last_autoanalyze::text, 'never')`},
			stat{"Seq scans", `s.seq_scan::text`},
			stat{"Index scans", `s.idx_scan::text`},
			stat{"Live tuples", `s.n_live_tup::text`},
			stat{"Dead tuples", `s.n_dead_tup::text`},
			stat{"Tablespace", `coalesce(ts.spcname::text, 'pg_default')`},
			stat{"Partition key", `CASE WHEN c.relkind = 'p' THEN pg_get_partkeydef(c.oid) END`},
			stat{"Partitions", `CASE WHEN c.relkind = 'p' THEN (SELECT count(*)::text FROM pg_inherits WHERE inhparent = c.oid) END`},
			stat{"Partition of", `CASE WHEN c.relispartition THEN
			     (SELECT pc.oid::regclass::text FROM pg_inherits i JOIN pg_class pc ON pc.oid = i.inhparent
			      WHERE i.inhrelid = c.oid) || ' ' || pg_get_expr(c.relpartbound, c.oid) END`},
		)
	}
	if rel.kind == 'm' {
		list = append(list, stat{"Populated", `c.relispopulated::text`})
	}
	exprs := make([]string, len(list))
	vals := make([]*string, len(list))
	dest := make([]any, len(list))
	for i, s := range list {
		exprs[i] = s.expr
		dest[i] = &vals[i]
	}
	err := q.QueryRow(ctx, `
		SELECT `+strings.Join(exprs, ",\n")+`
		FROM pg_class c
		LEFT JOIN pg_stat_all_tables s ON s.relid = c.oid
		LEFT JOIN pg_tablespace ts ON ts.oid = c.reltablespace
		WHERE c.oid = $1`, rel.oid).Scan(dest...)
	if err != nil {
		return nil, err
	}
	var out []db.KV
	for i, s := range list {
		if vals[i] != nil {
			out = append(out, db.KV{Key: s.key, Value: *vals[i]})
		}
	}
	return out, nil
}
