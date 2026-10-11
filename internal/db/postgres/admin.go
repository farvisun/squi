package postgres

import (
	"context"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/farvisun/squi/internal/db"
)

// admin implements db.Admin against the default database.
type admin struct{ c *conn }

// table runs sql with the simple protocol, so every value arrives as text.
func (a admin) table(ctx context.Context, sql string, args ...any) (*db.Table, error) {
	p, err := a.c.pool("")
	if err != nil {
		return nil, err
	}
	rows, err := p.Query(ctx, sql, append([]any{pgx.QueryExecModeSimpleProtocol}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	t := &db.Table{}
	for _, f := range rows.FieldDescriptions() {
		t.Columns = append(t.Columns, f.Name)
	}
	for rows.Next() {
		raw := rows.RawValues()
		row := make([]db.Cell, len(raw))
		for i, v := range raw {
			if v == nil {
				row[i] = db.NullCell
			} else {
				row[i] = db.Text(string(v))
			}
		}
		t.Rows = append(t.Rows, row)
	}
	return t, rows.Err()
}

// Processes lists other server sessions from pg_stat_activity.
func (a admin) Processes(ctx context.Context) (*db.Table, error) {
	return a.table(ctx, `
		SELECT pid, usename AS user, datname AS database, client_addr AS client,
		       application_name AS application, state, backend_type,
		       concat_ws(':', wait_event_type, wait_event) AS wait_event,
		       date_trunc('second', now() - query_start) AS query_age,
		       date_trunc('second', now() - xact_start) AS xact_age,
		       query
		FROM pg_stat_activity
		WHERE pid <> pg_backend_pid()
		ORDER BY query_start NULLS LAST, pid`)
}

func (a admin) CancelQuery(ctx context.Context, id string) error {
	return a.signal(ctx, "pg_cancel_backend", id)
}

func (a admin) Kill(ctx context.Context, id string) error {
	return a.signal(ctx, "pg_terminate_backend", id)
}

// signal calls fn (pg_cancel_backend or pg_terminate_backend) for pid id.
func (a admin) signal(ctx context.Context, fn, id string) error {
	pid, err := strconv.Atoi(id)
	if err != nil {
		return fmt.Errorf("postgres: invalid pid %q", id)
	}
	p, err := a.c.pool("")
	if err != nil {
		return err
	}
	var ok bool
	if err := p.QueryRow(ctx, "SELECT "+fn+"($1)", pid).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("postgres: %s(%d) failed: no such process or permission denied", fn, pid)
	}
	return nil
}

// Variables lists server settings like SHOW ALL.
func (a admin) Variables(ctx context.Context) (*db.Table, error) {
	return a.table(ctx, `SELECT name, setting, unit, source, short_desc AS description FROM pg_settings ORDER BY name`)
}

// Users lists roles, built-in pg_* roles last.
func (a admin) Users(ctx context.Context) (*db.Table, error) {
	return a.table(ctx, `
		SELECT r.rolname AS name, r.rolsuper AS superuser, r.rolcreatedb AS createdb,
		       r.rolcreaterole AS createrole, r.rolcanlogin AS login, r.rolreplication AS replication,
		       r.rolconnlimit AS conn_limit, r.rolvaliduntil AS valid_until,
		       (SELECT string_agg(g.rolname, ', ' ORDER BY g.rolname)
		        FROM pg_auth_members m JOIN pg_roles g ON g.oid = m.roleid
		        WHERE m.member = r.oid) AS member_of
		FROM pg_roles r
		ORDER BY r.rolname LIKE 'pg\_%', r.rolname`)
}

// Grants lists privileges held by user (directly or via PUBLIC) on
// databases, schemas and, in the default database, tables and sequences.
func (a admin) Grants(ctx context.Context, user string) (*db.Table, error) {
	return a.table(ctx, `
		WITH r AS (SELECT oid FROM pg_roles WHERE rolname = $1),
		acl AS (
		    SELECT 'database' AS kind, quote_ident(d.datname) AS object, x.*
		    FROM pg_database d, aclexplode(coalesce(d.datacl, acldefault('d', d.datdba))) x
		  UNION ALL
		    SELECT 'schema', quote_ident(n.nspname), x.*
		    FROM pg_namespace n, aclexplode(coalesce(n.nspacl, acldefault('n', n.nspowner))) x
		    WHERE n.nspname NOT LIKE 'pg\_%'
		  UNION ALL
		    SELECT CASE c.relkind WHEN 'S' THEN 'sequence' WHEN 'v' THEN 'view' ELSE 'table' END,
		           quote_ident(n.nspname) || '.' || quote_ident(c.relname), x.*
		    FROM pg_class c
		    JOIN pg_namespace n ON n.oid = c.relnamespace,
		         aclexplode(coalesce(c.relacl, acldefault(CASE c.relkind WHEN 'S' THEN 's' ELSE 'r' END::"char", c.relowner))) x
		    WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S')
		      AND n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg\_toast%'
		)
		SELECT kind, object,
		       string_agg(privilege_type, ', ' ORDER BY privilege_type) AS privileges,
		       CASE WHEN grantee = 0 THEN 'PUBLIC' ELSE pg_get_userbyid(grantee)::text END AS grantee,
		       pg_get_userbyid(grantor) AS grantor,
		       bool_or(is_grantable) AS grantable
		FROM acl
		WHERE grantee IN (SELECT oid FROM r) OR grantee = 0
		GROUP BY kind, object, grantee, grantor
		ORDER BY array_position(ARRAY['database', 'schema', 'table', 'view', 'sequence'], kind), object`, user)
}

// Sizes lists database sizes followed by the 50 largest relations of the
// default database.
func (a admin) Sizes(ctx context.Context) (*db.Table, error) {
	return a.table(ctx, `
		SELECT kind, schema, name, pg_size_pretty(total) AS total_size, table_size, index_size, rows_estimate
		FROM (
		    SELECT 'database' AS kind, NULL::text AS schema, datname::text AS name,
		           pg_database_size(oid) AS total, NULL::text AS table_size, NULL::text AS index_size,
		           NULL::bigint AS rows_estimate, 0 AS rank
		    FROM pg_database
		    WHERE NOT datistemplate AND has_database_privilege(oid, 'CONNECT')
		  UNION ALL
		    SELECT * FROM (
		        SELECT CASE c.relkind WHEN 'm' THEN 'matview' ELSE 'table' END, n.nspname::text, c.relname::text,
		               pg_total_relation_size(c.oid), pg_size_pretty(pg_table_size(c.oid)),
		               pg_size_pretty(pg_indexes_size(c.oid)),
		               CASE WHEN c.reltuples < 0 THEN NULL ELSE c.reltuples::bigint END, 1
		        FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		        WHERE c.relkind IN ('r', 'm') AND n.nspname NOT IN ('pg_catalog', 'information_schema')
		          AND n.nspname NOT LIKE 'pg\_toast%'
		        ORDER BY 4 DESC
		        LIMIT 50
		    ) t
		) s
		ORDER BY rank, total DESC, name`)
}
