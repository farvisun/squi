package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/farvisun/squi/internal/config"
	"github.com/farvisun/squi/internal/db"
	"github.com/farvisun/squi/internal/schemadiff"
	"github.com/farvisun/squi/internal/secrets"
	"github.com/farvisun/squi/internal/ui"
)

// errDiffers makes "squi diff -exit-code" exit 1, like git diff.
var errDiffers = errors.New("schemas differ")

// runDiff implements "squi diff SOURCE TARGET".
func runDiff(args []string) error {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), `usage: squi diff [flags] SOURCE TARGET

Compares the tables and views of two schemas and prints what differs in
TARGET, then the statements that make TARGET match SOURCE. SOURCE and TARGET
are saved connection names or connection URLs; use the same connection twice
with -source-schema / -target-schema to compare two schemas of one database.

Nothing is changed: review the script and run it yourself. DROP statements
are written as comments unless -drop is given.

Flags:`)
		fs.PrintDefaults()
	}
	configDir := fs.String("config", "", "squi config directory (default $XDG_CONFIG_HOME/squi)")
	srcDB := fs.String("source-db", "", "database on SOURCE (default: the connection's)")
	srcSchema := fs.String("source-schema", "", "schema on SOURCE (default: public, dbo, main, or the database)")
	dstDB := fs.String("target-db", "", "database on TARGET (default: the connection's)")
	dstSchema := fs.String("target-schema", "", "schema on TARGET (default: as for SOURCE)")
	sqlOnly := fs.Bool("sql", false, "print only the script")
	reportOnly := fs.Bool("report", false, "print only the list of differences")
	drops := fs.Bool("drop", false, "write DROP statements for what only TARGET has (default: commented out)")
	exitCode := fs.Bool("exit-code", false, "exit with 1 when the schemas differ (for CI)")
	timeout := fs.Duration("timeout", 2*time.Minute, "give up after this long")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		fs.Usage()
		return errors.New("need SOURCE and TARGET")
	}
	store, err := config.DefaultStore()
	if err != nil {
		return err
	}
	if *configDir != "" {
		store = config.NewStore(*configDir)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	bridge := &ui.Bridge{Term: termPrompt}

	src, srcDone, err := loadSide(ctx, store, bridge, fs.Arg(0), *srcDB, *srcSchema)
	if err != nil {
		return fmt.Errorf("source: %w", err)
	}
	defer srcDone()
	if *dstSchema == "" && *srcSchema != "" {
		*dstSchema = *srcSchema
	}
	dst, dstDone, err := loadSide(ctx, store, bridge, fs.Arg(1), *dstDB, *dstSchema)
	if err != nil {
		return fmt.Errorf("target: %w", err)
	}
	defer dstDone()

	changes := schemadiff.Diff(src.schema, dst.schema)
	if !*reportOnly {
		if err := schemadiff.LoadDDL(ctx, src.conn, src.schema, changes); err != nil {
			return err
		}
	}
	if !*sqlOnly {
		fmt.Printf("-- %s → %s: %d difference(s)\n", src.label, dst.label, len(changes))
		for _, c := range changes {
			fmt.Println("--   " + c.String())
		}
		if !*reportOnly {
			fmt.Println()
		}
	}
	if !*reportOnly {
		fmt.Print(schemadiff.Script(changes, src.schema, dst.schema, schemadiff.Options{Drops: *drops}))
	}
	if *exitCode && len(changes) > 0 {
		return errDiffers
	}
	return nil
}

type side struct {
	conn   db.Conn
	schema *schemadiff.Schema
	label  string
}

func loadSide(ctx context.Context, store *config.Store, bridge *ui.Bridge, spec, database, schema string) (side, func(), error) {
	cfg, password, err := resolveProfile(store, spec)
	if err != nil {
		return side{}, nil, err
	}
	conn, connDB, done, err := ui.Open(ctx, cfg, password, secrets.Keyring{}, bridge)
	if err != nil {
		return side{}, nil, err
	}
	if database == "" {
		database = connDB
	}
	d := conn.Dialect()
	if schema == "" {
		schema = d.DefaultSchema()
		if schema == "" {
			schema = database
		}
	}
	if database == "" && !d.HasSchemas() {
		database = schema
	}
	s, err := schemadiff.Load(ctx, conn, database, schema)
	if err != nil {
		done()
		return side{}, nil, err
	}
	label := cfg.Title()
	if database != cfg.Expanded().Database {
		label += " " + database
	}
	if d.HasSchemas() && schema != "" {
		label += " (" + schema + ")"
	}
	return side{conn: conn, schema: s, label: label}, done, nil
}

// resolveProfile finds a saved connection by name or id, or parses a URL.
func resolveProfile(store *config.Store, spec string) (config.Connection, string, error) {
	if config.IsURL(spec) {
		cfg, pw, err := config.ParseURL(spec)
		if err != nil {
			return cfg, "", err
		}
		cfg.ID = config.NewID()
		cfg.Transient = true
		return cfg, pw, nil
	}
	conns, err := store.LoadConnections()
	if err != nil {
		return config.Connection{}, "", err
	}
	for _, c := range conns {
		if strings.EqualFold(c.Name, spec) || c.ID == spec {
			return c, "", nil
		}
	}
	return config.Connection{}, "", fmt.Errorf("no saved connection named %q", spec)
}

// termPrompt asks on the terminal: a hidden password, or a yes/no question.
func termPrompt(secret bool, title, text string) (string, bool) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", false
	}
	defer tty.Close()
	if secret {
		fmt.Fprintf(tty, "%s — %s: ", title, text)
		pw, err := term.ReadPassword(int(tty.Fd()))
		fmt.Fprintln(tty)
		return string(pw), err == nil
	}
	fmt.Fprintf(tty, "%s\n%s [y/N] ", title, text)
	line, _ := bufio.NewReader(tty).ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return "", answer == "y" || answer == "yes"
}
