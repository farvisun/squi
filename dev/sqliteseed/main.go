// Command sqliteseed builds the SQLite playground database from a seed
// script with squi's own driver, so no sqlite3 binary is needed:
//
//	go run ./dev/sqliteseed dev/seed/sqlite.sql dev/squi.db
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/farvisun/squi/internal/db"
	_ "github.com/farvisun/squi/internal/db/sqlite"
	"github.com/farvisun/squi/internal/sqltext"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: sqliteseed SEED.sql OUT.db")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, "sqliteseed:", err)
		os.Exit(1)
	}
}

func run(seed, out string) error {
	src, err := os.ReadFile(seed)
	if err != nil {
		return err
	}
	if err := os.Remove(out); err != nil && !os.IsNotExist(err) {
		return err
	}
	drv, _ := db.Get(db.SQLite)
	ctx := context.Background()
	c, err := drv.Open(ctx, db.ConnConfig{Database: out, Params: map[string]string{"mode": "rwc"}}, nil)
	if err != nil {
		return err
	}
	defer c.Close()
	s, err := c.NewSession(ctx, "")
	if err != nil {
		return err
	}
	defer s.Close()
	for _, st := range sqltext.Split(string(src), db.SQLite) {
		rs, err := s.Run(ctx, st.SQL)
		if err != nil {
			return fmt.Errorf("line %d: %w", st.Line, err)
		}
		rs.Close()
	}
	fmt.Println("wrote", out)
	return nil
}
