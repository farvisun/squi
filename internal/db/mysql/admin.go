package mysql

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/farvisun/squi/internal/db"
)

// admin implements db.Admin on the metadata pool.
type admin struct{ c *conn }

func (c *conn) Admin() db.Admin { return admin{c} }

// Processes lists all server threads except the querying one, longest
// running first.
func (a admin) Processes(ctx context.Context) (*db.Table, error) {
	return a.c.queryTable(ctx, `
		SELECT ID, USER, HOST, DB, COMMAND, TIME, STATE, INFO
		FROM information_schema.PROCESSLIST WHERE ID <> CONNECTION_ID()
		ORDER BY TIME DESC, ID`)
}

func (a admin) CancelQuery(ctx context.Context, id string) error { return a.killID(ctx, id, true) }

func (a admin) Kill(ctx context.Context, id string) error { return a.killID(ctx, id, false) }

func (a admin) killID(ctx context.Context, id string, queryOnly bool) error {
	n, err := strconv.ParseUint(strings.TrimSpace(id), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid process id %q", id)
	}
	return a.c.kill(ctx, n, queryOnly)
}

func (a admin) Variables(ctx context.Context) (*db.Table, error) {
	t, err := a.c.queryTable(ctx, "SHOW GLOBAL VARIABLES")
	if err != nil {
		return nil, err
	}
	t.Columns = []string{"Name", "Value"}
	return t, nil
}

// Users lists accounts from mysql.user, or only the current account when
// the user may not read mysql.user.
func (a admin) Users(ctx context.Context) (*db.Table, error) {
	q := "SELECT User, Host, plugin, account_locked, password_expired FROM mysql.user ORDER BY User, Host"
	if a.c.mariadb {
		q = "SELECT User, Host, plugin, password_expired, is_role FROM mysql.user ORDER BY User, Host"
	}
	t, err := a.c.queryTable(ctx, q)
	if n := errNumber(err); n == 1142 || n == 1044 || n == 1227 {
		var cur string
		if err := a.c.meta.QueryRowContext(ctx, "SELECT CURRENT_USER()").Scan(&cur); err != nil {
			return nil, wrapErr(err)
		}
		user, host := splitAccount(cur)
		return &db.Table{Columns: []string{"User", "Host"}, Rows: [][]db.Cell{{db.Text(user), db.Text(host)}}}, nil
	}
	return t, err
}

// Grants runs SHOW GRANTS for an account given as user@host, 'user'@'host'
// or just user (host '%').
func (a admin) Grants(ctx context.Context, user string) (*db.Table, error) {
	u, h := splitAccount(user)
	d := a.c.Dialect()
	t, err := a.c.queryTable(ctx, "SHOW GRANTS FOR "+d.QuoteLiteral(u)+"@"+d.QuoteLiteral(h))
	if err != nil {
		return nil, err
	}
	t.Columns = []string{"Grant"}
	return t, nil
}

// splitAccount splits "user@host" (optionally quoted) at the last @ that
// is not inside quotes; the host defaults to '%'.
func splitAccount(s string) (user, host string) {
	s = strings.TrimSpace(s)
	at := -1
	var quote rune
	for i, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"' || r == '`':
			quote = r
		case r == '@':
			at = i
		}
	}
	host = "%"
	if at >= 0 {
		s, host = s[:at], unquote(s[at+1:])
	}
	return unquote(s), host
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && strings.ContainsRune("'\"`", rune(s[0])) && s[len(s)-1] == s[0] {
		q := s[:1]
		return strings.ReplaceAll(s[1:len(s)-1], q+q, q)
	}
	return s
}

// Sizes lists per-schema totals, largest first, followed by the 50
// largest tables.
func (a admin) Sizes(ctx context.Context) (*db.Table, error) {
	t := &db.Table{Columns: []string{"Kind", "Schema", "Table", "Rows", "Data", "Index", "Total"}}
	err := a.sizeRows(ctx, t, `
		SELECT 'schema', TABLE_SCHEMA, NULL, SUM(TABLE_ROWS), SUM(DATA_LENGTH), SUM(INDEX_LENGTH),
		       SUM(DATA_LENGTH + INDEX_LENGTH) AS total
		FROM information_schema.TABLES GROUP BY TABLE_SCHEMA ORDER BY total DESC, TABLE_SCHEMA`)
	if err != nil {
		return nil, err
	}
	err = a.sizeRows(ctx, t, `
		SELECT 'table', TABLE_SCHEMA, TABLE_NAME, TABLE_ROWS, DATA_LENGTH, INDEX_LENGTH,
		       DATA_LENGTH + INDEX_LENGTH AS total
		FROM information_schema.TABLES WHERE TABLE_TYPE = 'BASE TABLE'
		ORDER BY total DESC, TABLE_SCHEMA, TABLE_NAME LIMIT 50`)
	if err != nil {
		return nil, err
	}
	return t, nil
}

// sizeRows appends the rows of a size query (kind, schema, table, rows,
// data, index, total) to t with byte counts humanized.
func (a admin) sizeRows(ctx context.Context, t *db.Table, q string) error {
	res, err := a.c.queryTable(ctx, q)
	if err != nil {
		return err
	}
	for _, row := range res.Rows {
		for i := 4; i < len(row); i++ {
			if n, err := strconv.ParseInt(row[i].S, 10, 64); err == nil {
				row[i] = db.Text(humanSize(n))
			}
		}
		t.Rows = append(t.Rows, row)
	}
	return nil
}
