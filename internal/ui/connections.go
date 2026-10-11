package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/farvisun/squi/internal/config"
	"github.com/farvisun/squi/internal/datagrip"
	"github.com/farvisun/squi/internal/db"
	"github.com/farvisun/squi/internal/secrets"
	"github.com/farvisun/squi/internal/ui/components"
	"github.com/farvisun/squi/internal/ui/theme"
)

// connManager is the full-screen connection list and editor.
type connManager struct {
	picker  *components.Picker
	form    *components.Form
	editing *config.Connection // nil = list mode
	testing bool
	status  string
}

func (m *Model) openConnections() {
	m.screen = screenConnections
	m.mgr = &connManager{}
	m.mgr.reload(m)
}

func (c *connManager) reload(m *Model) {
	var items []components.PickerItem
	for _, cfg := range m.conns {
		detail := fmt.Sprintf("%s %s@%s:%d", cfg.Driver, cfg.User, cfg.Host, cfg.Port)
		if cfg.Driver == string(db.SQLite) {
			detail = "sqlite " + cfg.Database
		} else if cfg.Database != "" {
			detail += "/" + cfg.Database
		}
		if cfg.SSH.Enabled {
			detail += " via ssh"
		}
		badge := theme.ConnBadge(cfg)
		if cfg.Transient {
			badge = theme.Dim.Render("unsaved") + " " + badge
		}
		if _, open := m.active[cfg.ID]; open {
			badge = theme.OK.Render("●") + " " + badge
		}
		items = append(items, components.PickerItem{Title: cfg.Title(), Detail: detail, Badge: strings.TrimSpace(badge), Value: cfg.ID})
	}
	if c.picker == nil {
		c.picker = components.NewPicker("Connections", items)
	} else {
		c.picker.SetItems(items)
	}
}

func (m *Model) connByID(id string) (config.Connection, bool) {
	for _, c := range m.conns {
		if c.ID == id {
			return c, true
		}
	}
	return config.Connection{}, false
}

func (c *connManager) selected(m *Model) (config.Connection, bool) {
	it, ok := c.picker.Selected()
	if !ok {
		return config.Connection{}, false
	}
	return m.connByID(it.Value.(string))
}

func (c *connManager) update(m *Model, msg tea.Msg) tea.Cmd {
	if c.editing != nil {
		return c.updateForm(m, msg)
	}
	km, isKey := msg.(tea.KeyMsg)
	if isKey {
		switch km.String() {
		case "esc":
			if len(m.tabs) > 0 {
				m.screen = screenMain
			}
			return nil
		case "enter":
			if cfg, ok := c.selected(m); ok {
				return m.connect(cfg)
			}
			if len(m.conns) == 0 {
				c.edit(m, config.Connection{Driver: "postgres", Host: "localhost", Port: 5432})
			}
			return nil
		case "ctrl+n":
			c.edit(m, config.Connection{Driver: "postgres", Host: "localhost", Port: 5432})
			return nil
		case "ctrl+e":
			if cfg, ok := c.selected(m); ok {
				c.edit(m, cfg)
			}
			return nil
		case "ctrl+y":
			if cfg, ok := c.selected(m); ok {
				cfg.ID = ""
				cfg.Name += " (copy)"
				c.edit(m, cfg)
			}
			return nil
		case "ctrl+g":
			return c.syncDataGrip(m)
		case "ctrl+d":
			if cfg, ok := c.selected(m); ok {
				m.push(&confirmModal{title: "Delete connection", danger: true,
					body: fmt.Sprintf("Delete %q and its saved passwords?", cfg.Title()),
					onYes: func() tea.Cmd {
						if err := m.store.Delete(cfg.ID); err != nil {
							return m.toast(err.Error(), toastError)
						}
						m.dropTransient(cfg.ID)
						_ = secrets.DeleteAll(m.connector.secrets, cfg.ID)
						m.reloadConns()
						return m.toast("Deleted "+cfg.Title(), toastInfo)
					}})
			}
			return nil
		case "ctrl+t":
			if cfg, ok := c.selected(m); ok {
				return c.test(m, cfg)
			}
			return nil
		case "ctrl+u":
			m.push(newPrompt("Open connection URL", "postgres://user@host/db · mysql://… · clickhouse://… · sqlserver://… · sqlite:path.db (?ssh=bastion&env=prod)", "", false,
				func(v string) tea.Cmd {
					if strings.TrimSpace(v) == "" {
						return nil
					}
					return m.connectURL(v)
				}))
			return nil
		}
	}
	_, _, cmd := c.picker.Update(msg)
	return cmd
}

func (c *connManager) edit(m *Model, cfg config.Connection) {
	c.editing = &cfg
	c.status = ""
	f := components.NewForm()
	f.Section("Connection")
	f.Text("name", "Name", cfg.Name, "e.g. prod-api")
	f.Select("driver", "Driver", []string{"postgres", "mysql", "clickhouse", "sqlserver", "sqlite"}, cfg.Driver).Hint =
		"mysql also covers MariaDB; clickhouse uses the HTTP port; sqlserver also covers Azure SQL; sqlite opens a local file"
	isFile := func() bool { return f.Value("driver") == string(db.SQLite) }
	server := func() bool { return !isFile() }
	file := ""
	if cfg.Driver == string(db.SQLite) {
		file = cfg.Database
	}
	ff := f.Text("file", "Database file", file, "~/data/app.db")
	ff.Hint, ff.Visible = "the file must exist; :memory: opens a scratch database", isFile
	f.Text("host", "Host", cfg.Host, "localhost").Visible = server
	f.Number("port", "Port", cfg.Port, "5432").Visible = server
	f.Text("user", "User", cfg.User, "").Visible = server
	pwHint := "leave empty to be asked when connecting"
	if _, ok, _ := m.connector.secrets.Get(cfg.ID, secrets.DBPassword); ok && cfg.ID != "" {
		pwHint = "saved in keychain — leave empty to keep"
	}
	if cfg.PasswordEnv != "" {
		pwHint = "taken from $" + cfg.PasswordEnv + " when set"
	}
	f.Password("password", "Password", "", pwHint).Visible = server
	pe := f.Text("password_env", "Password env var", cfg.PasswordEnv, "e.g. PGPASSWORD (optional)")
	pe.Hint, pe.Visible = "read the password from this environment variable", server
	sp := f.Bool("save_password", "Save passwords", cfg.SavePassword)
	sp.Hint, sp.Visible = "store DB/SSH secrets in the OS keychain", server
	dbName := cfg.Database
	if file != "" {
		dbName = ""
	}
	dbf := f.Text("database", "Database", dbName, "default database")
	dbf.Hint, dbf.Visible = "ctrl+l picks one from the server", server

	f.Section("Safety")
	envs := make([]string, len(config.Envs))
	for i, e := range config.Envs {
		envs[i] = string(e)
	}
	f.Select("env", "Environment", envs, string(cfg.Env)).Hint = "prod = red status bar + confirm writes"
	f.Text("label", "Label", cfg.Label, "e.g. EU (optional)").Hint = "short tag shown next to the environment"
	f.Text("color", "Color", cfg.Color, "env colour").Hint = "red orange yellow green cyan blue purple gray or #rrggbb"
	f.Bool("read_only", "Read-only", cfg.ReadOnly).Hint = "block all writes"
	f.Text("timeout", "Statement timeout", cfg.StatementTimeout, "default: "+fmtTimeout(config.DefaultProdTimeout)+" on prod, none elsewhere").Hint =
		"cancel statements still running after e.g. 30s or 2m; 0 = no limit"

	sshOn := func() bool { return f.BoolValue("ssh") && server() }
	f.Section("SSH tunnel").Visible = server
	f.Bool("ssh", "Use SSH", cfg.SSH.Enabled).Visible = server
	f.Text("ssh_alias", "Host alias", cfg.SSH.Alias, "Host from ~/.ssh/config (optional)").Visible = sshOn
	f.Text("ssh_host", "SSH host", cfg.SSH.Host, "bastion.example.com").Visible = sshOn
	f.Number("ssh_port", "SSH port", cfg.SSH.Port, "22").Visible = sshOn
	f.Text("ssh_user", "SSH user", cfg.SSH.User, "$USER").Visible = sshOn
	f.Select("ssh_auth", "Auth", []string{config.SSHAuthAuto, config.SSHAuthAgent, config.SSHAuthKey, config.SSHAuthPassword}, cfg.SSH.Auth).Visible = sshOn
	f.Text("ssh_key", "Key file", cfg.SSH.KeyPath, "~/.ssh/id_ed25519").Visible = sshOn
	f.Password("ssh_password", "SSH password", "", "or key passphrase; leave empty to be asked").Visible = sshOn
	f.Text("ssh_jump", "Jump hosts", strings.Join(cfg.SSH.JumpHosts, ", "), "user@jump1:22, jump2").Visible = sshOn

	f.Section("TLS").Visible = server
	f.Select("ssl_mode", "SSL mode", []string{"", "disable", "prefer", "require", "verify-ca", "verify-full"}, cfg.SSLMode).Visible = server
	f.Text("ssl_root", "CA cert", cfg.SSLRootCert, "").Visible = server
	f.Text("ssl_cert", "Client cert", cfg.SSLCert, "").Visible = server
	f.Text("ssl_key", "Client key", cfg.SSLKey, "").Visible = server

	f.Section("Options")
	f.Number("fetch", "Fetch size", cfg.FetchSize, fmt.Sprintf("%d (default)", m.settings.FetchSize))
	f.FocusKey("name")
	c.form = f
}

// formConn builds a connection from the form plus any typed secrets.
func (c *connManager) formConn() (config.Connection, string, string) {
	f := c.form
	cfg := *c.editing
	cfg.Name = f.Value("name")
	cfg.Driver = f.Value("driver")
	cfg.Host = f.Value("host")
	cfg.Port = f.Int("port")
	cfg.User = f.Value("user")
	cfg.Database = f.Value("database")
	cfg.SavePassword = f.BoolValue("save_password")
	cfg.PasswordEnv = strings.TrimPrefix(strings.TrimSpace(f.Value("password_env")), "$")
	cfg.Env = config.Env(f.Value("env"))
	cfg.ReadOnly = f.BoolValue("read_only")
	cfg.Label = strings.TrimSpace(f.Value("label"))
	cfg.Color = strings.TrimSpace(f.Value("color"))
	cfg.StatementTimeout = strings.TrimSpace(f.Value("timeout"))
	cfg.SSH.Enabled = f.BoolValue("ssh")
	cfg.SSH.Alias = f.Value("ssh_alias")
	cfg.SSH.Host = f.Value("ssh_host")
	cfg.SSH.Port = f.Int("ssh_port")
	cfg.SSH.User = f.Value("ssh_user")
	cfg.SSH.Auth = f.Value("ssh_auth")
	cfg.SSH.KeyPath = f.Value("ssh_key")
	cfg.SSH.JumpHosts = nil
	for _, j := range strings.Split(f.Value("ssh_jump"), ",") {
		if j = strings.TrimSpace(j); j != "" {
			cfg.SSH.JumpHosts = append(cfg.SSH.JumpHosts, j)
		}
	}
	cfg.SSLMode = f.Value("ssl_mode")
	cfg.SSLRootCert = f.Value("ssl_root")
	cfg.SSLCert = f.Value("ssl_cert")
	cfg.SSLKey = f.Value("ssl_key")
	cfg.FetchSize = f.Int("fetch")
	if cfg.Driver == string(db.SQLite) {
		// only the file matters; drop server settings left from another driver
		cfg.Database = strings.TrimSpace(f.Value("file"))
		cfg.Host, cfg.Port, cfg.User, cfg.PasswordEnv, cfg.SavePassword = "", 0, "", "", false
		cfg.SSH.Enabled, cfg.SSLMode, cfg.SSLRootCert, cfg.SSLCert, cfg.SSLKey = false, "", "", "", ""
		return cfg, "", ""
	}
	if cfg.Port == 0 {
		cfg.Port = db.DefaultPort(db.DriverName(cfg.Driver))
	}
	return cfg, f.Value("password"), f.Value("ssh_password")
}

func (c *connManager) updateForm(m *Model, msg tea.Msg) tea.Cmd {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "esc":
			c.editing, c.form = nil, nil
			return nil
		case "ctrl+s":
			return c.save(m)
		case "ctrl+t":
			return c.test(m, c.formTestConn(m))
		case "ctrl+l":
			return c.listDatabases(m, c.formTestConn(m))
		case "ctrl+u":
			m.push(newPrompt("Fill from URL", "postgres://user:pass@host:5432/db?sslmode=require&ssh=bastion", "", false,
				func(v string) tea.Cmd { return c.fillFromURL(m, v) }))
			return nil
		}
	}
	oldDriver := c.form.Value("driver")
	cmd := c.form.Update(msg)
	if nd := c.form.Value("driver"); nd != oldDriver {
		if p := c.form.Int("port"); p == 0 || p == db.DefaultPort(db.DriverName(oldDriver)) {
			c.form.SetValue("port", fmt.Sprint(db.DefaultPort(db.DriverName(nd))))
		}
	}
	return cmd
}

// fillFromURL replaces the form's fields with those of a connection URL,
// keeping the profile's name and id.
func (c *connManager) fillFromURL(m *Model, raw string) tea.Cmd {
	if strings.TrimSpace(raw) == "" || c.form == nil {
		return nil
	}
	u, pw, err := config.ParseURL(raw)
	if err != nil {
		c.status = theme.Error.Render(err.Error())
		return nil
	}
	cur, _, _ := c.formConn()
	u.ID, u.SavePassword, u.PasswordEnv = cur.ID, cur.SavePassword, cur.PasswordEnv
	if cur.Name != "" {
		u.Name = cur.Name
	}
	if u.SSH.Auth == "" {
		u.SSH.Auth = config.SSHAuthAuto
	}
	c.edit(m, u)
	if pw != "" {
		c.form.SetValue("password", pw)
	}
	c.status = theme.OK.Render("Filled from URL — check the fields, then ctrl+s to save")
	return nil
}

// formTestConn returns the connection being edited, with typed secrets
// available to the connector, for testing it before it is saved.
func (c *connManager) formTestConn(m *Model) config.Connection {
	cfg, pw, sshPw := c.formConn()
	if cfg.ID == "" {
		cfg.ID = config.NewID()
		c.editing.ID = cfg.ID
	}
	c.stashSecrets(m, cfg, pw, sshPw, false)
	return cfg
}

type dbListMsg struct {
	names []string
	err   error
}

// listDatabases connects with the form's settings and lists the databases
// on the server so one can be picked.
func (c *connManager) listDatabases(m *Model, cfg config.Connection) tea.Cmd {
	if c.testing {
		return nil
	}
	c.testing = true
	c.status = theme.Dim.Render("Loading databases from " + cfg.Title() + "…")
	conn := m.connector
	return tea.Batch(m.spinner.Tick, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		ac, err := conn.open(ctx, cfg)
		if err != nil && cfg.Database != "" && isUnknownDatabase(err) {
			cfg.Database = "" // list from the server's default database
			ac, err = conn.open(ctx, cfg)
		}
		if err != nil {
			return dbListMsg{err: err}
		}
		defer ac.close()
		dbs, err := ac.conn.Databases(ctx)
		names := make([]string, len(dbs))
		for i, d := range dbs {
			names[i] = d.Name
		}
		return dbListMsg{names: names, err: err}
	})
}

func (c *connManager) applyDBList(m *Model, msg dbListMsg) {
	c.testing = false
	if c.form == nil {
		return
	}
	if msg.err != nil {
		c.status = theme.Error.Render("✗ " + msg.err.Error())
		return
	}
	c.status = ""
	m.push(dbPicker("Database", msg.names, c.form.Value("database"), func(name string) tea.Cmd {
		if c.form != nil {
			c.form.SetValue("database", name)
			c.form.FocusKey("database")
		}
		return nil
	}))
}

// stashSecrets keeps typed secrets for this run and saves them if requested.
func (c *connManager) stashSecrets(m *Model, cfg config.Connection, pw, sshPw string, persist bool) {
	kinds := []struct {
		k secrets.Kind
		v string
	}{{secrets.DBPassword, pw}, {secrets.SSHPassword, sshPw}}
	if cfg.SSH.Auth == config.SSHAuthKey || cfg.SSH.KeyPath != "" {
		kinds[1].k = secrets.SSHPassphrase
	}
	for _, s := range kinds {
		if s.v == "" {
			continue
		}
		_ = m.connector.memory.Set(cfg.ID, s.k, s.v)
		if persist && cfg.SavePassword {
			if err := m.connector.secrets.Set(cfg.ID, s.k, s.v); err != nil {
				c.status = theme.Warn.Render("Keychain unavailable: " + err.Error())
			}
		}
	}
	if persist && !cfg.SavePassword {
		_ = secrets.DeleteAll(m.connector.secrets, cfg.ID)
	}
}

func (c *connManager) save(m *Model) tea.Cmd {
	cfg, pw, sshPw := c.formConn()
	if cfg.Driver == string(db.SQLite) && cfg.Database == "" {
		c.status = theme.Error.Render("Database file is required")
		return nil
	}
	if cfg.Host == "" && !cfg.SSH.Enabled && cfg.Driver != string(db.SQLite) {
		c.status = theme.Error.Render("Host is required")
		return nil
	}
	if _, ok := theme.ParseColor(cfg.Color); cfg.Color != "" && !ok {
		c.status = theme.Error.Render("Unknown color " + cfg.Color + " — use a name like red or #rrggbb")
		return nil
	}
	if cfg.StatementTimeout != "" {
		if _, err := config.ParseTimeout(cfg.StatementTimeout); err != nil {
			c.status = theme.Error.Render("Statement " + err.Error())
			return nil
		}
	}
	saved, err := m.store.Upsert(cfg)
	if err != nil {
		c.status = theme.Error.Render(err.Error())
		return nil
	}
	c.stashSecrets(m, saved, pw, sshPw, true)
	c.editing, c.form = nil, nil
	m.reloadConns()
	return m.toast("Saved "+saved.Title(), toastInfo)
}

type testMsg struct {
	name    string
	version string
	err     error
	took    time.Duration
}

func (c *connManager) test(m *Model, cfg config.Connection) tea.Cmd {
	c.testing = true
	c.status = theme.Dim.Render("Testing " + cfg.Title() + "…")
	conn := m.connector
	return tea.Batch(m.spinner.Tick, func() tea.Msg {
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		ac, err := conn.open(ctx, cfg)
		if err != nil {
			return testMsg{name: cfg.Title(), err: err}
		}
		defer ac.close()
		return testMsg{name: cfg.Title(), version: ac.conn.ServerVersion(), took: time.Since(start)}
	})
}

func (c *connManager) applyTest(msg testMsg) {
	c.testing = false
	if msg.err != nil {
		c.status = theme.Error.Render("✗ " + msg.err.Error())
		return
	}
	c.status = theme.OK.Render(fmt.Sprintf("✓ Connected to %s in %s — %s", msg.name, fmtDuration(msg.took), msg.version))
}

func (c *connManager) view(m *Model, w, h int) string {
	title := theme.Title.Render("squi") + theme.Dim.Render(" — terminal SQL client")
	if c.editing != nil {
		head := "New connection"
		if c.editing.ID != "" {
			head = "Edit " + c.editing.Title()
		}
		c.form.SetSize(w-4, h-7)
		body := c.form.View()
		footer := theme.Key.Render("ctrl+s") + " save  " + theme.Key.Render("ctrl+t") + " test  " + theme.Key.Render("ctrl+l") + " databases  " +
			theme.Key.Render("ctrl+u") + " from URL  " +
			theme.Key.Render("↑↓/tab") + " move  " + theme.Key.Render("←→/space") + " change  " + theme.Key.Render("esc") + " cancel"
		content := body + "\n\n" + c.status + "\n" + footer
		return title + "\n" + components.Box(head, content, w, h-1, true, theme.Accent)
	}
	listW := min(w*3/5, 90)
	detailW := w - listW
	c.picker.SetSize(listW-2, h-5)
	list := c.picker.View()
	if len(m.conns) == 0 {
		list = "\n  No saved connections.\n\n  Press " + theme.Key.Render("enter") + " or " + theme.Key.Render("ctrl+n") + " to add one."
	}
	left := components.Box("Connections", list, listW, h-4, true, theme.Accent)
	var det []string
	if cfg, ok := c.selected(m); ok {
		row := func(k, v string) {
			if v != "" {
				det = append(det, theme.Dim.Render(components.Fit(k, 12))+v)
			}
		}
		row("Driver", cfg.Driver)
		if cfg.Driver == string(db.SQLite) {
			row("File", cfg.Database)
		} else {
			row("Host", fmt.Sprintf("%s:%d", cfg.Host, cfg.Port))
		}
		row("User", cfg.User)
		if cfg.Driver != string(db.SQLite) {
			row("Database", cfg.Database)
		}
		row("Env", theme.ConnBadge(cfg))
		row("Read-only", yesNo(cfg.ReadOnly, "yes", ""))
		if cfg.PasswordEnv != "" {
			row("Password", "$"+cfg.PasswordEnv)
		}
		if d := cfg.QueryTimeout(); d > 0 {
			row("Timeout", fmtTimeout(d))
		}
		if cfg.SSH.Enabled {
			target := cfg.SSH.Alias
			if target == "" {
				target = cfg.SSH.Host
			}
			if cfg.SSH.User != "" {
				target = cfg.SSH.User + "@" + target
			}
			row("SSH", target)
			row("Jump", strings.Join(cfg.SSH.JumpHosts, " → "))
			row("SSH auth", cfg.SSH.Auth)
		}
		row("TLS", cfg.SSLMode)
		if cfg.Origin != nil && cfg.Origin.Tool == datagrip.Tool {
			row("Imported", "DataGrip · :consoles opens its queries")
		}
	}
	right := components.Box("Details", strings.Join(det, "\n"), detailW, h-4, false, theme.Accent)
	footer := theme.Key.Render("enter") + " connect  " + theme.Key.Render("ctrl+n") + " new  " + theme.Key.Render("ctrl+e") + " edit  " +
		theme.Key.Render("ctrl+y") + " duplicate  " + theme.Key.Render("ctrl+t") + " test  " + theme.Key.Render("ctrl+d") + " delete  " +
		theme.Key.Render("ctrl+u") + " open URL  " + theme.Key.Render("ctrl+g") + " DataGrip sync  " + theme.Key.Render("ctrl+q") + " quit"
	if len(m.tabs) > 0 {
		footer += "  " + theme.Key.Render("esc") + " back"
	}
	return title + "\n" + lipgloss.JoinHorizontal(lipgloss.Top, left, right) + "\n" +
		components.FitStyled(c.status, w) + "\n" + components.FitStyled(footer, w)
}
