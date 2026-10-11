package mysql

import (
	"context"
	"database/sql"
	"strings"

	"github.com/farvisun/squi/internal/db"
)

// EstimateRows reads information_schema.TABLES.TABLE_ROWS: an estimate for
// InnoDB, exact for MyISAM, NULL for views.
func (c *conn) EstimateRows(ctx context.Context, ref db.ObjectRef) (int64, bool, bool, error) {
	var rows sql.NullInt64
	var engine sql.NullString
	err := c.meta.QueryRowContext(ctx, `SELECT TABLE_ROWS, ENGINE FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`,
		refSchema(ref), ref.Name).Scan(&rows, &engine)
	if err == sql.ErrNoRows {
		return 0, false, false, nil
	}
	if err != nil {
		return 0, false, false, wrapErr(err)
	}
	if !rows.Valid {
		return 0, false, false, nil
	}
	exact := strings.EqualFold(engine.String, "MyISAM") || strings.EqualFold(engine.String, "Aria")
	return rows.Int64, exact, true, nil
}
