// Command squi is a lightweight terminal SQL client for PostgreSQL, MySQL,
// MariaDB, ClickHouse, SQL Server and SQLite.
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"github.com/farvisun/squi/internal/config"
	"github.com/farvisun/squi/internal/datagrip"
	"github.com/farvisun/squi/internal/secrets"
	"github.com/farvisun/squi/internal/termkeys"
	"github.com/farvisun/squi/internal/ui"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "import" {
		if err := runImport(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "squi import:", err)
			os.Exit(1)
		}
		return
	}
	var (
		configDir = flag.String("config", "", "config directory (default $XDG_CONFIG_HOME/squi)")
		connect   = flag.String("connect", "", "open a saved connection (by name) or a connection URL at startup")
		noMouse   = flag.Bool("no-mouse", false, "disable mouse support (keeps native terminal selection)")
		showVer   = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()
	if *showVer {
		fmt.Println("squi", version)
		return
	}
	if err := run(*configDir, *connect, !*noMouse); err != nil {
		fmt.Fprintln(os.Stderr, "squi:", err)
		os.Exit(1)
	}
}

// runImport implements "squi import datagrip".
func runImport(args []string) error {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), `usage: squi import datagrip [flags]

Imports DataGrip data sources (with their SSH tunnels) as connections, or
updates those imported before. Settings changed in squi (environment, label,
colour, timeout, read-only) are kept. Passwords stay in DataGrip's keychain
entry and are read when connecting.

Flags:`)
		fs.PrintDefaults()
	}
	var projects multiFlag
	configDir := fs.String("config", "", "squi config directory (default $XDG_CONFIG_HOME/squi)")
	fs.Var(&projects, "project", "DataGrip project directory, or a JetBrains IDE options directory (repeatable; default: DataGrip's recent projects)")
	prune := fs.Bool("prune", false, "remove imported connections whose data source was deleted in DataGrip")
	dry := fs.Bool("dry-run", false, "show what would change without saving")
	verbose := fs.Bool("v", false, "list every change and note")
	if len(args) == 0 || args[0] != "datagrip" {
		fs.Usage()
		return fmt.Errorf("unknown source; only datagrip is supported")
	}
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	store, err := config.DefaultStore()
	if err != nil {
		return err
	}
	if *configDir != "" {
		store = config.NewStore(*configDir)
	}
	rep, removed, err := datagrip.Import(store, datagrip.Options{Projects: projects, Prune: *prune, DryRun: *dry})
	if err != nil {
		return err
	}
	for _, id := range removed {
		_ = secrets.DeleteAll(secrets.Keyring{}, id)
	}
	fmt.Println("read", strings.Join(rep.Sources, ", "))
	if *dry {
		fmt.Println("dry run:", rep.Summary())
	} else {
		fmt.Println(rep.Summary())
	}
	if *verbose || *dry {
		if d := rep.Details(); d != "" {
			fmt.Println(d)
		}
	} else if len(rep.Skipped)+len(rep.Notes)+len(rep.Stale) > 0 {
		fmt.Println("run with -v for skipped data sources and notes")
	}
	return nil
}

type multiFlag []string

func (f *multiFlag) String() string     { return strings.Join(*f, ",") }
func (f *multiFlag) Set(v string) error { *f = append(*f, v); return nil }

func run(configDir, connect string, mouse bool) error {
	store, err := config.DefaultStore()
	if err != nil {
		return err
	}
	if configDir != "" {
		store = config.NewStore(configDir)
	}
	bridge := &ui.Bridge{}
	o := ui.Options{Store: store, Secrets: secrets.Keyring{}, Bridge: bridge, Connect: connect}
	opts := []tea.ProgramOption{tea.WithAltScreen()}
	// Extended key reporting (for ctrl+enter) needs our own input filter;
	// Windows consoles deliver keys through a different API.
	extKeys := runtime.GOOS != "windows" && term.IsTerminal(int(os.Stdin.Fd())) && os.Getenv("SQUI_LEGACY_KEYS") == ""
	if extKeys {
		opts = append(opts, tea.WithInput(termkeys.Wrap(os.Stdin)))
		o.TermWrite = func(s string) { _, _ = os.Stdout.WriteString(s) }
	}
	m, err := ui.New(o)
	if err != nil {
		return err
	}
	if mouse {
		opts = append(opts, tea.WithMouseCellMotion())
	}
	p := tea.NewProgram(m, opts...)
	bridge.Send = p.Send
	_, err = p.Run()
	if extKeys {
		_, _ = os.Stdout.WriteString(termkeys.ResetOtherKeys)
	}
	m.Shutdown()
	return err
}
