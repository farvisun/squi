package clickhouse

import (
	"context"
	"strconv"

	"github.com/farvisun/squi/internal/db"
)

// EstimateRows reads system.tables.total_rows, which MergeTree tables keep
// exactly; other engines report NULL.
func (c *conn) EstimateRows(ctx context.Context, ref db.ObjectRef) (int64, bool, bool, error) {
	database := ref.Database
	if database == "" {
		database = ref.Schema
	}
	t, err := c.cl.table(ctx, `SELECT total_rows FROM system.tables WHERE database = {db:String} AND name = {t:String}`,
		"", p("db", database, "t", ref.Name))
	if err != nil {
		return 0, false, false, err
	}
	if len(t.Rows) == 0 || t.Rows[0][0].Null {
		return 0, false, false, nil
	}
	n, err := strconv.ParseInt(t.Rows[0][0].S, 10, 64)
	if err != nil {
		return 0, false, false, nil
	}
	return n, true, true, nil
}
