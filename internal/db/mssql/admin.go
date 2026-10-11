package mssql

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/farvisun/squi/internal/db"
)

// admin implements db.Admin on the metadata pool.
type admin struct{ c *conn }

func (c *conn) Admin() db.Admin { return admin{c} }

// Processes lists user sessions except the querying one, longest running
// request first. Without VIEW SERVER STATE the server shows only the
// login's own sessions and no statement text.
func (a admin) Processes(ctx context.Context) (*db.Table, error) {
	const q = `
		SELECT s.session_id AS [id], s.login_name AS [login], s.host_name AS [host],
		       DB_NAME(COALESCE(r.database_id, s.database_id)) AS [database], s.program_name AS [program],
		       COALESCE(r.status, s.status) AS [status], r.command, r.wait_type AS [wait],
		       NULLIF(r.blocking_session_id, 0) AS [blocked by], r.total_elapsed_time / 1000 AS [secs],
		       %s AS [query]
		FROM sys.dm_exec_sessions s
		LEFT JOIN sys.dm_exec_requests r ON r.session_id = s.session_id %s
		WHERE s.is_user_process = 1 AND s.session_id <> @@SPID
		ORDER BY CASE WHEN r.session_id IS NULL THEN 1 ELSE 0 END, r.total_elapsed_time DESC, s.session_id`
	t, err := a.c.queryTable(ctx, fmt.Sprintf(q, "t.text", "OUTER APPLY sys.dm_exec_sql_text(r.sql_handle) t"))
	if n := errNumber(err); n == errNoPermission || n == errNoServerState {
		return a.c.queryTable(ctx, fmt.Sprintf(q, "CAST(NULL AS nvarchar(max))", ""))
	}
	return t, err
}

// Permission errors of the DMVs.
const (
	errNoPermission  = 297
	errNoServerState = 300
)

// errNoCancel explains why another session's query cannot be cancelled.
var errNoCancel = errors.New("SQL Server cannot cancel another session's statement: kill the session instead (it rolls back its transaction)")

func (a admin) CancelQuery(context.Context, string) error { return errNoCancel }

// Kill ends a session with KILL; its open transaction is rolled back.
func (a admin) Kill(ctx context.Context, id string) error {
	n, err := strconv.ParseUint(strings.TrimSpace(id), 10, 16)
	if err != nil {
		return fmt.Errorf("invalid session id %q", id)
	}
	_, err = a.c.meta.ExecContext(ctx, "KILL "+strconv.FormatUint(n, 10))
	return wrapErr(err)
}

func (a admin) Variables(ctx context.Context) (*db.Table, error) {
	return a.c.queryTable(ctx, `
		SELECT name AS [Name], CAST(value_in_use AS nvarchar(100)) AS [Value], description AS [Description]
		FROM sys.configurations ORDER BY name`)
}

// Users lists server logins, or the users of the current database where
// logins are not visible (Azure SQL Database, contained users).
func (a admin) Users(ctx context.Context) (*db.Table, error) {
	t, err := a.c.queryTable(ctx, `
		SELECT name AS [Login], type_desc AS [Type], is_disabled AS [Disabled],
		       default_database_name AS [Default database], CONVERT(nvarchar(19), create_date, 120) AS [Created]
		FROM sys.server_principals
		WHERE type IN ('S', 'U', 'G', 'E', 'X') AND name NOT LIKE '##%'
		ORDER BY name`)
	if err == nil && len(t.Rows) > 0 {
		return t, nil
	}
	return a.c.queryTable(ctx, `
		SELECT name AS [User], type_desc AS [Type], default_schema_name AS [Default schema],
		       CONVERT(nvarchar(19), create_date, 120) AS [Created]
		FROM sys.database_principals
		WHERE type IN ('S', 'U', 'G', 'E', 'X') AND principal_id > 4
		ORDER BY name`)
}

// Grants lists the server and current-database permissions and role
// memberships of a login or user.
func (a admin) Grants(ctx context.Context, user string) (*db.Table, error) {
	// catalog views differ in collation: normalise for the UNION
	const dd = " COLLATE DATABASE_DEFAULT"
	t, err := a.c.queryTable(ctx, `
		SELECT 'server' AS [Scope], p.state_desc`+dd+` AS [State], p.permission_name`+dd+` AS [Permission], p.class_desc`+dd+` AS [On]
		FROM sys.server_permissions p
		JOIN sys.server_principals pr ON pr.principal_id = p.grantee_principal_id
		WHERE pr.name = @p1
		UNION ALL
		SELECT 'server role', 'MEMBER', r.name`+dd+`, ''
		FROM sys.server_role_members m
		JOIN sys.server_principals r ON r.principal_id = m.role_principal_id
		JOIN sys.server_principals pr ON pr.principal_id = m.member_principal_id
		WHERE pr.name = @p1
		UNION ALL
		SELECT 'database ' + DB_NAME(), p.state_desc`+dd+`, p.permission_name`+dd+`,
		       (p.class_desc + COALESCE(' ' + CASE p.class
		           WHEN 1 THEN OBJECT_SCHEMA_NAME(p.major_id) + '.' + OBJECT_NAME(p.major_id)
		           WHEN 3 THEN SCHEMA_NAME(p.major_id) END, ''))`+dd+`
		FROM sys.database_permissions p
		JOIN sys.database_principals dp ON dp.principal_id = p.grantee_principal_id
		WHERE dp.name = @p1 OR dp.sid = SUSER_SID(@p1)
		UNION ALL
		SELECT 'database role', 'MEMBER', r.name`+dd+`, ''
		FROM sys.database_role_members m
		JOIN sys.database_principals r ON r.principal_id = m.role_principal_id
		JOIN sys.database_principals dp ON dp.principal_id = m.member_principal_id
		WHERE dp.name = @p1 OR dp.sid = SUSER_SID(@p1)`, user)
	if err != nil {
		return nil, err
	}
	return t, nil
}

// Sizes lists database file sizes, followed by the 50 largest tables of
// the connection's database.
func (a admin) Sizes(ctx context.Context) (*db.Table, error) {
	t := &db.Table{Columns: []string{"Kind", "Database", "Table", "Rows", "Data", "Index / Log", "Total"}}
	dbs, err := a.c.queryTable(ctx, `
		SELECT 'database', DB_NAME(database_id), NULL, NULL,
		       CAST(SUM(CASE WHEN type = 0 THEN CAST(size AS bigint) ELSE 0 END) * 8192 AS bigint),
		       CAST(SUM(CASE WHEN type = 1 THEN CAST(size AS bigint) ELSE 0 END) * 8192 AS bigint),
		       CAST(SUM(CAST(size AS bigint)) * 8192 AS bigint) AS total
		FROM sys.master_files GROUP BY database_id ORDER BY total DESC`)
	if err == nil { // not on Azure SQL Database
		t.Rows = append(t.Rows, humanize(dbs.Rows, 4)...)
	}
	tables, err := a.c.queryTable(ctx, `
		SELECT TOP (50) 'table', DB_NAME(), s.name + '.' + o.name,
		       SUM(CASE WHEN p.index_id IN (0, 1) THEN p.rows ELSE 0 END),
		       CAST(SUM(a.data_pages) AS bigint) * 8192,
		       CAST(SUM(a.used_pages - a.data_pages) AS bigint) * 8192,
		       CAST(SUM(a.total_pages) AS bigint) * 8192 AS total
		FROM sys.objects o
		JOIN sys.schemas s ON s.schema_id = o.schema_id
		JOIN sys.partitions p ON p.object_id = o.object_id
		JOIN sys.allocation_units a ON a.container_id = p.partition_id
		WHERE o.type = 'U'
		GROUP BY s.name, o.name ORDER BY total DESC`)
	if err != nil {
		return nil, err
	}
	t.Rows = append(t.Rows, humanize(tables.Rows, 4)...)
	return t, nil
}

// humanize formats the byte counts from column from on.
func humanize(rows [][]db.Cell, from int) [][]db.Cell {
	for _, row := range rows {
		for i := from; i < len(row); i++ {
			if n, err := strconv.ParseInt(row[i].S, 10, 64); err == nil {
				row[i] = db.Text(humanSize(n))
			}
		}
	}
	return rows
}
