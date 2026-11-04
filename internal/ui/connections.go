package ui

import (
	"context"
	"fmt"
	"reflect"
	"sort"
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
	initial formSnapshot // the form as opened, to notice edits
	listTop int          // screen row of the list box
	listW   int
	formTop int // screen row of the form's first line
	hits    []hintHit
}

// formSnapshot is what the connection form holds.
type formSnapshot struct {
	cfg       config.Connection
	pw, sshPw string
}

func (c *connManager) snapshot() formSnapshot {
	cfg, pw, sshPw := c.formConn()
	return formSnapshot{cfg, pw, sshPw}
}

func (c *connManager) formDirty() bool {
	return !reflect.DeepEqual(c.snapshot(), c.initial)
}

// closeForm leaves the form, asking first if it holds edits.
func (c *connManager) closeForm(m *Model) tea.Cmd {
	if !c.formDirty() {
		c.editing, c.form = nil, nil
		return nil
	}
	m.push(&confirmModal{title: "Discard changes", body: "This connection has unsaved changes. Discard them?",
		onYes: func() tea.Cmd {
			c.editing, c.form = nil, nil
			return nil
		}})
	return nil
}

// statusLine is what the manager shows above its keys: work in progress,
// then the latest toast, then the result of the last action.
func (c *connManager) statusLine(m *Model) string {
	if len(m.connecting) > 0 {
		var names []string
		for id := range m.connecting {
			name := id
			if cfg, ok := m.connByID(id); ok {
				name = cfg.Title()
			}
			names = append(names, name)
		}
		sort.Strings(names)
		return m.spinner.View() + " Connecting to " + strings.Join(names, ", ") + "… " +
			fmtDuration(time.Since(m.connectSince).Truncate(time.Second))
	}
	if time.Now().Before(m.toastUntil) {
		switch m.toastLevel {
		case toastError:
			return theme.Error.Render(m.toastText)
		case toastWarn:
			return theme.Warn.Render(m.toastText)
		}
		return m.toastText
	}
	return c.status
}

// mouse handles a press or wheel step at screen coordinates.
func (c *connManager) mouse(m *Model, ev mouseEvent) tea.Cmd {
	if ev.y == 0 && ev.btn == tea.MouseButtonLeft && m.updateAvail != nil {
		return m.showUpdate()
	}
	if !ev.wheel && ev.btn == tea.MouseButtonLeft {
		if k := hintAt(c.hits, ev.x, ev.y); k != "" {
			return m.handleKey(keyPress(k))
		}
	}
	if c.editing != nil {
		switch ev.btn {
		case tea.MouseButtonWheelUp:
			c.form.Wheel(-1)
		case tea.MouseButtonWheelDown:
			c.form.Wheel(1)
		case tea.MouseButtonLeft:
			old := c.form.Value("driver")
			c.form.Click(ev.x-1, ev.y-c.formTop)
			c.driverChanged(old)
		}
		return nil
	}
	if ev.wheel {
		if d := ev.delta(); d != 0 {
			c.picker.ScrollBy(d)
		}
		return nil
	}
	if ev.x >= c.listW {
		return nil
	}
	i := c.picker.ItemAt(ev.y - c.listTop - 1) // inside the top border
	if i < 0 {
		return nil
	}
	c.picker.SetIndex(i)
	switch ev.btn {
	case tea.MouseButtonLeft:
		if ev.clicks == 2 {
			if cfg, ok := c.selected(m); ok {
				return m.connect(cfg)
			}
		}
	case tea.MouseButtonRight:
		press := func(k string) func() tea.Cmd {
			return func() tea.Cmd { return c.update(m, keyPress(k)) }
		}
		m.pushMenu("Connection", []menuItem{
			{"enter", "Connect", press("enter")},
			{"e", "Edit", press("ctrl+e")},
			{"y", "Duplicate", press("ctrl+y")},
			{"t", "Test", press("ctrl+t")},
			{"d", "Delete", press("ctrl+d")},
		}, ev.x, ev.y)
	}
	return nil
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
	defer func() { c.initial = c.snapshot() }()
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
			return c.closeForm(m)
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
	c.driverChanged(oldDriver)
	return cmd
}

// driverChanged moves the default port along with a new driver.
func (c *connManager) driverChanged(oldDriver string) {
	if nd := c.form.Value("driver"); nd != oldDriver {
		if p := c.form.Int("port"); p == 0 || p == db.DefaultPort(db.DriverName(oldDriver)) {
			c.form.SetValue("port", fmt.Sprint(db.DefaultPort(db.DriverName(nd))))
		}
	}
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
		c.form.FocusKey("file")
		return nil
	}
	if cfg.Host == "" && !cfg.SSH.Enabled && cfg.Driver != string(db.SQLite) {
		c.status = theme.Error.Render("Host is required")
		c.form.FocusKey("host")
		return nil
	}
	if _, ok := theme.ParseColor(cfg.Color); cfg.Color != "" && !ok {
		c.status = theme.Error.Render("Unknown color " + cfg.Color + " — use a name like red or #rrggbb")
		c.form.FocusKey("color")
		return nil
	}
	if cfg.StatementTimeout != "" {
		if _, err := config.ParseTimeout(cfg.StatementTimeout); err != nil {
			c.status = theme.Error.Render("Statement " + err.Error())
			c.form.FocusKey("timeout")
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
	if m.updateAvail != nil {
		title += "  " + m.updateBadge() + theme.Dim.Render(" available")
	}
	if c.editing != nil {
		head := "New connection"
		if c.editing.ID != "" {
			head = "Edit " + c.editing.Title()
		}
		footer, hits := renderHints([]hint{{key: "ctrl+s", label: "save"}, {key: "ctrl+t", label: "test"}, {key: "ctrl+l", label: "databases"},
			{key: "ctrl+u", label: "from URL"}, {key: "↑↓/tab", label: "move", inert: true}, {key: "←→/space", label: "change", inert: true},
			{key: "esc", label: "cancel"}}, w-2, false)
		n := lipgloss.Height(footer)
		c.form.SetSize(w-4, h-6-n)
		body := c.form.View()
		c.formTop = 2 // below the title and the box's top border
		c.hits = offsetHints(hits, c.formTop+lipgloss.Height(body)+2)
		for i := range c.hits {
			c.hits[i].x0++
			c.hits[i].x1++ // inside the box's left border
		}
		content := body + "\n\n" + c.statusLine(m) + "\n" + footer
		return title + "\n" + components.Box(head, content, w, h-1, true, theme.Accent)
	}
	hints := []hint{{key: "enter", label: "connect"}, {key: "ctrl+n", label: "new"}, {key: "ctrl+e", label: "edit"},
		{key: "ctrl+y", label: "duplicate"}, {key: "ctrl+t", label: "test"}, {key: "ctrl+d", label: "delete"},
		{key: "ctrl+u", label: "open URL"}, {key: "ctrl+g", label: "DataGrip sync"}, {key: "ctrl+q", label: "quit"}}
	if len(m.tabs) > 0 {
		hints = append(hints, hint{key: "esc", label: "back"})
	}
	footer, hits := renderHints(hints, w, false)
	n := lipgloss.Height(footer)
	boxH := h - 2 - n // the title and the status line take the rest
	c.hits = offsetHints(hits, 1+boxH+1)
	listW := min(w*3/5, 90)
	detailW := w - listW
	c.listTop, c.listW = 1, listW
	c.picker.SetSize(listW-2, boxH-2)
	list := c.picker.View()
	if len(m.conns) == 0 {
		list = "\n  No saved connections.\n\n  Press " + theme.Key.Render("enter") + " or " + theme.Key.Render("ctrl+n") + " to add one."
	}
	left := components.Box("Connections", list, listW, boxH, true, theme.Accent)
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
	right := components.Box("Details", strings.Join(det, "\n"), detailW, boxH, false, theme.Accent)
	return title + "\n" + lipgloss.JoinHorizontal(lipgloss.Top, left, right) + "\n" +
		components.FitStyled(c.statusLine(m), w) + "\n" + footer
}
