// Command squi is a lightweight terminal SQL client for PostgreSQL and MySQL.
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"github.com/farvisun/squi/internal/config"
	"github.com/farvisun/squi/internal/secrets"
	"github.com/farvisun/squi/internal/termkeys"
	"github.com/farvisun/squi/internal/ui"
)

var version = "dev"

func main() {
	var (
		configDir = flag.String("config", "", "config directory (default $XDG_CONFIG_HOME/squi)")
		connect   = flag.String("connect", "", "open the named connection at startup")
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
