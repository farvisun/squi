package ui

import (
	"context"

	"github.com/farvisun/squi/internal/config"
	"github.com/farvisun/squi/internal/db"
	"github.com/farvisun/squi/internal/secrets"
)

// Open connects a profile without the UI (squi diff): SSH tunnel, saved or
// environment passwords and DataGrip's keychain entries work as in the app,
// and missing passwords or unknown SSH hosts are asked through bridge.Term.
// password, when set, is used for the database (from a connection URL).
// close releases the connection and its tunnel.
func Open(ctx context.Context, cfg config.Connection, password string, store secrets.Store, bridge *Bridge) (conn db.Conn, database string, done func(), err error) {
	c := &connector{secrets: store, memory: &secrets.Memory{}, bridge: bridge}
	if password != "" {
		_ = c.memory.Set(cfg.ID, secrets.DBPassword, password)
	}
	ac, err := c.open(ctx, cfg)
	if err != nil {
		return nil, "", nil, err
	}
	return ac.conn, ac.database(), ac.close, nil
}
