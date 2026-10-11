package sqlite

import (
	"context"
	"errors"
	"os"
	"strconv"

	"github.com/farvisun/squi/internal/db"
)

// admin implements db.Admin. An embedded database has no server sessions,
// users or grants; Variables shows the pragmas and Sizes the dbstat table.
type admin struct{ c *conn }

func (c *conn) Admin() db.Admin { return admin{c} }

var errEmbedded = errors.New("SQLite is an embedded database: it has no server sessions, users or grants")

func (admin) Processes(context.Context) (*db.Table, error)      { return nil, errEmbedded }
func (admin) CancelQuery(context.Context, string) error         { return errEmbedded }
func (admin) Kill(context.Context, string) error                { return errEmbedded }
func (admin) Users(context.Context) (*db.Table, error)          { return nil, errEmbedded }
func (admin) Grants(context.Context, string) (*db.Table, error) { return nil, errEmbedded }

// pragmas shown by Variables, in order.
var pragmas = []string{
	"journal_mode", "synchronous", "foreign_keys", "page_size", "page_count", "freelist_count", "cache_size",
	"auto_vacuum", "encoding", "user_version", "application_id", "busy_timeout", "locking_mode", "query_only",
	"wal_autocheckpoint", "max_page_count", "secure_delete", "temp_store",
}

// Variables lists the main database's pragmas and the library's compile
// options.
func (a admin) Variables(ctx context.Context) (*db.Table, error) {
	t := &db.Table{Columns: []string{"Name", "Value"}}
	for _, p := range pragmas {
		var v string
		if a.c.meta.QueryRowContext(ctx, "PRAGMA "+p).Scan(&v) == nil {
			t.Rows = append(t.Rows, []db.Cell{db.Text(p), db.Text(v)})
		}
	}
	opts, err := a.c.queryTable(ctx, "SELECT 'compile_option', compile_options FROM pragma_compile_options")
	if err == nil {
		t.Rows = append(t.Rows, opts.Rows...)
	}
	return t, nil
}

// Sizes lists the file size and the 50 largest tables and indexes.
func (a admin) Sizes(ctx context.Context) (*db.Table, error) {
	t := &db.Table{Columns: []string{"Kind", "Name", "Table", "Pages", "Size"}}
	if a.c.path != "" {
		if fi, err := os.Stat(a.c.path); err == nil {
			t.Rows = append(t.Rows, []db.Cell{db.Text("file"), db.Text(a.c.path), db.NullCell, db.NullCell, db.Text(humanSize(fi.Size()))})
		}
	}
	res, err := a.c.queryTable(ctx, `
		SELECT s.type, d.name, s.tbl_name, COUNT(*), SUM(d.pgsize)
		FROM dbstat d LEFT JOIN sqlite_schema s ON s.name = d.name
		GROUP BY d.name ORDER BY SUM(d.pgsize) DESC LIMIT 50`)
	if err != nil {
		return nil, err
	}
	for _, row := range res.Rows {
		if n, err := strconv.ParseInt(row[4].S, 10, 64); err == nil {
			row[4] = db.Text(humanSize(n))
		}
		t.Rows = append(t.Rows, row)
	}
	return t, nil
}
