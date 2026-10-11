package postgres

import (
	"context"

	"github.com/farvisun/squi/internal/db"
)

// EstimateRows reads the planner's row estimate (pg_class.reltuples).
// Tables never vacuumed or analyzed report -1 (PostgreSQL 14+) or 0.
func (c *conn) EstimateRows(ctx context.Context, ref db.ObjectRef) (int64, bool, bool, error) {
	q, rel, err := c.relation(ctx, ref)
	if err != nil {
		return 0, false, false, err
	}
	var n float64
	var pages int64
	if err := q.QueryRow(ctx, `SELECT reltuples::float8, relpages::int8 FROM pg_class WHERE oid = $1`, rel.oid).Scan(&n, &pages); err != nil {
		return 0, false, false, err
	}
	if n < 0 || (n == 0 && pages == 0 && rel.kind != 'r') {
		return 0, false, false, nil
	}
	return int64(n), false, true, nil
}
