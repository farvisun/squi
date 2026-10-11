package mssql

import (
	"context"
	"database/sql"
	"errors"

	"github.com/farvisun/squi/internal/db"
)

// EstimateRows sums sys.partitions.rows of the heap or clustered index:
// maintained by the engine, but documented as approximate. Views have no
// estimate.
func (c *conn) EstimateRows(ctx context.Context, ref db.ObjectRef) (int64, bool, bool, error) {
	o, err := c.object(ctx, ref)
	if errors.Is(err, ErrNotFound) {
		return 0, false, false, nil
	}
	if err != nil {
		return 0, false, false, err
	}
	var n sql.NullInt64
	err = c.meta.QueryRowContext(ctx, `
		SELECT SUM(rows) FROM `+o.prefix+`sys.partitions WHERE object_id = @p1 AND index_id IN (0, 1)`, o.id).Scan(&n)
	if err != nil {
		return 0, false, false, wrapErr(err)
	}
	if !n.Valid {
		return 0, false, false, nil
	}
	return n.Int64, false, true, nil
}
