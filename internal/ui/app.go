// Package ui implements squi's terminal user interface.
package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/farvisun/squi/internal/config"
	"github.com/farvisun/squi/internal/db"
	"github.com/farvisun/squi/internal/history"
	"github.com/farvisun/squi/internal/schema"
	"github.com/farvisun/squi/internal/secrets"
	"github.com/farvisun/squi/internal/termkeys"
	"github.com/farvisun/squi/internal/ui/components"
	"github.com/farvisun/squi/internal/ui/theme"
)

type screen int

const (
	screenMain screen = iota
	screenConnections
)

type focusArea int

const (
	focusExplorer focusArea = iota
	focusEditor
	focusOutput
)

// Options configures the UI.
type Options struct {
	Store   *config.Store
	Secrets secrets.Store
	Bridge  *Bridge
	Connect string // connection name to open at startup
	// TermWrite writes raw control sequences to the terminal; set when the
	// input is wrapped by termkeys (enables extended key reporting).
	TermWrite func(string)
}

// Model is the root Bubble Tea model.
type Model struct {
	store     *config.Store
	settings  config.Settings
	conns     []config.Connection
	connector *connector
	bridge    *Bridge
	history   *history.Store
	snippets  []history.Snippet

	active     map[string]*activeConn
	connecting map[string]bool
	tabs       []*tab
	cur        int
	nextTabID  int

	screen      screen
	mgr         *connManager
	focus       focusArea
	zoom        bool
	hideExplore bool
	explorerW   int
	editorPct   int

	modals     []modal
	cmdline    *textinput.Model
	winPrefix  bool
	toastText  string
	toastLevel toastLevel
	toastUntil time.Time
	spinner    spinner.Model
	jobs       int
	width      int
	height     int
	startErr   string
	autoConn   string
	termWrite  func(string)

	// pending holds saved tabs whose connection is not open yet; they are
	// recreated when it connects and kept in the session file until then.
	pending     []config.SessionTab
	lastSession []byte
	ticks       int
}

// New creates the model, loading config, history and snippets.
func New(o Options) (*Model, error) {
	st, err := o.Store.LoadSettings()
	m := &Model{
		store:      o.Store,
		settings:   st,
		bridge:     o.Bridge,
		connector:  &connector{secrets: o.Secrets, memory: &secrets.Memory{}, bridge: o.Bridge},
		active:     map[string]*activeConn{},
		connecting: map[string]bool{},
		explorerW:  st.ExplorerWidth,
		editorPct:  40,
		spinner:    spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		autoConn:   o.Connect,
		termWrite:  o.TermWrite,
	}
	if err != nil {
		m.startErr = err.Error()
	}
	m.spinner.Style = lipgloss.NewStyle().Foreground(theme.Accent)
	if err := m.reloadConns(); err != nil {
		return nil, err
	}
	if h, err := history.Open(o.Store.HistoryDir(), st.HistoryLimit); err == nil {
		m.history = h
	} else {
		m.startErr = "history: " + err.Error()
	}
	if sn, err := history.LoadSnippets(o.Store.SnippetsPath()); err == nil {
		m.snippets = sn
	}
	if st.RestoreSession {
		if sess, err := o.Store.LoadSession(); err == nil {
			m.pending = sess.Tabs
		} else {
			m.startErr = "session: " + err.Error()
		}
	}
	m.openConnections()
	return m, nil
}

func (m *Model) reloadConns() error {
	conns, err := m.store.LoadConnections()
	if err != nil {
		return err
	}
	m.conns = conns
	if m.mgr != nil {
		m.mgr.reload(m)
	}
	return nil
}

type tickMsg time.Time

func tick() tea.Cmd { return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) }) }

// Init starts the clock and the optional auto-connect.
func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{tick(), textinput.Blink}
	if m.termWrite != nil {
		w := m.termWrite
		cmds = append(cmds, func() tea.Msg { w(termkeys.Enable); return nil })
	}
	if m.startErr != "" {
		cmds = append(cmds, m.toast(m.startErr, toastError))
	}
	restoring := map[string]bool{}
	for _, st := range m.pending {
		if cfg, ok := m.connByID(st.Conn); ok && !restoring[cfg.ID] {
			restoring[cfg.ID] = true
			cmds = append(cmds, m.connect(cfg))
		}
	}
	if len(restoring) > 0 && m.mgr != nil {
		m.mgr.status = theme.Dim.Render(fmt.Sprintf("Restoring %d tab(s) from the last session…", len(m.pending)))
	}
	if m.autoConn != "" {
		found := false
		for _, c := range m.conns {
			if strings.EqualFold(c.Name, m.autoConn) || c.ID == m.autoConn {
				if !restoring[c.ID] {
					cmds = append(cmds, m.connect(c))
				}
				found = true
			}
		}
		if !found {
			cmds = append(cmds, m.toast("No connection named "+m.autoConn, toastError))
		}
	}
	return tea.Batch(cmds...)
}

// Shutdown saves the open tabs and closes all sessions and connections.
func (m *Model) Shutdown() {
	_ = m.saveSession()
	for _, t := range m.tabs {
		for _, s := range t.toClose {
			s.closeNow()
		}
		for _, r := range t.out.results() {
			if r.stream != nil {
				r.stream.closeNow()
			}
		}
		if t.sess != nil {
			_ = t.sess.Close()
		}
	}
	for _, ac := range m.active {
		ac.close()
	}
	if m.history != nil {
		_ = m.history.Close()
	}
}

func (m *Model) currentTab() *tab {
	if m.cur < 0 || m.cur >= len(m.tabs) {
		return nil
	}
	return m.tabs[m.cur]
}

func (m *Model) anyBusy() bool {
	if m.jobs > 0 || len(m.connecting) > 0 || (m.mgr != nil && m.mgr.testing) {
		return true
	}
	for _, t := range m.tabs {
		if t.busy {
			return true
		}
	}
	return false
}

// ---- connecting ----

type connOpenedMsg struct {
	cfg config.Connection
	ac  *activeConn
	err error
}

func (m *Model) connect(cfg config.Connection) tea.Cmd {
	if ac, ok := m.active[cfg.ID]; ok {
		return m.newTab(ac)
	}
	if m.connecting[cfg.ID] {
		return m.toast("Already connecting to "+cfg.Title(), toastInfo)
	}
	m.connecting[cfg.ID] = true
	conn := m.connector
	m.toast("Connecting to "+cfg.Title()+"…", toastInfo)
	return tea.Batch(m.spinner.Tick, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		ac, err := conn.open(ctx, cfg)
		return connOpenedMsg{cfg: cfg, ac: ac, err: err}
	})
}

func (m *Model) newTab(ac *activeConn) tea.Cmd {
	t := m.createTab(ac)
	return m.op(t, "opening session", func(ctx context.Context, s db.Session) tea.Msg { return nil })
}

// createTab adds and focuses a tab on ac; its session opens with the first op.
func (m *Model) createTab(ac *activeConn) *tab {
	m.nextTabID++
	t := &tab{
		id:       m.nextTabID,
		rank:     1<<20 + m.nextTabID,
		ac:       ac,
		db:       ac.cfg.Database,
		editor:   components.NewEditor(m.settings.VimMode, components.LexerFor(ac.cfg.Driver)),
		out:      newOutput(),
		manualTx: m.settings.DefaultTxManual,
	}
	t.editor.Placeholder = "-- write SQL here; ctrl+enter or F5 runs the statement under the cursor"
	ac.refs++
	m.tabs = append(m.tabs, t)
	m.cur = len(m.tabs) - 1
	m.screen = screenMain
	m.focus = focusEditor
	if m.settings.VimMode {
		// start in insert mode so new users can type immediately
		t.editor.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	}
	return t
}

type cacheMsg struct {
	ac  *activeConn
	db  string
	err error
}

// ensureCache loads autocomplete metadata for the tab's database.
func (m *Model) ensureCache(t *tab) tea.Cmd {
	ac, dbName := t.ac, t.db
	if ac.cacheLoaded == nil {
		ac.cacheLoaded = map[string]bool{}
	}
	if ac.cacheLoaded[dbName] {
		return nil
	}
	ac.cacheLoaded[dbName] = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		return cacheMsg{ac: ac, db: dbName, err: ac.cache.Load(ctx, ac.conn, dbName)}
	}
}

// ---- update ----

// Update dispatches messages.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case promptMsg:
		m.handlePrompt(msg)
		return m, nil
	case spinner.TickMsg:
		if !m.anyBusy() {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case tickMsg:
		var cmds []tea.Cmd
		cmds = append(cmds, tick())
		if m.ticks++; m.ticks%5 == 0 {
			if err := m.saveSession(); err != nil {
				m.toast("Could not save session: "+err.Error(), toastWarn)
			}
		}
		if t := m.currentTab(); t != nil {
			for _, p := range t.out.panels {
				if a, ok := p.(*adminPanel); ok && t.out.current() == p {
					cmds = append(cmds, a.tick())
				}
			}
		}
		return m, tea.Batch(cmds...)
	case toastMsg:
		return m, m.toast(msg.text, msg.level)
	case connOpenedMsg:
		delete(m.connecting, msg.cfg.ID)
		if msg.err != nil {
			if m.mgr != nil {
				m.mgr.status = theme.Error.Render("✗ " + msg.err.Error())
			}
			return m, m.toast("Connect failed: "+msg.err.Error(), toastError)
		}
		ac := msg.ac
		ac.explorer = newExplorer(ac)
		m.active[ac.cfg.ID] = ac
		if m.mgr != nil {
			m.mgr.status = ""
			m.mgr.reload(m)
		}
		m.toast(fmt.Sprintf("Connected to %s — %s", ac.cfg.Title(), ac.conn.ServerVersion()), toastInfo)
		if cmd := m.restoreTabs(ac); cmd != nil {
			return m, tea.Batch(cmd, ac.explorer.load(nil))
		}
		return m, tea.Batch(m.newTab(ac), ac.explorer.load(nil))
	case explorerMsg:
		if msg.ac.explorer != nil {
			return m, msg.ac.explorer.apply(msg)
		}
		return m, nil
	case cacheMsg:
		if msg.err != nil {
			msg.ac.cacheLoaded[msg.db] = false
		}
		return m, nil
	case opDoneMsg:
		cmd := m.handleOpDone(msg)
		var cache tea.Cmd
		if m.hasTab(msg.tab) && msg.err == nil {
			cache = m.ensureCache(msg.tab)
		}
		return m, tea.Batch(cmd, cache)
	case runMsg:
		return m, m.handleRun(msg)
	case fetchMsg:
		if !m.hasTab(msg.tab) {
			return m, nil
		}
		return m, m.handleFetch(msg)
	case detailMsg:
		if msg.err != nil {
			return m, m.toast("Detail: "+msg.err.Error(), toastError)
		}
		return m, msg.then(msg.d)
	case ddlMsg:
		if msg.err != nil {
			msg.panel.ddlErr = msg.err.Error()
		} else {
			msg.panel.ddl = components.NewTextView(msg.ddl, components.LexerFor(msg.panel.ac.cfg.Driver))
		}
		return m, nil
	case ddlTextMsg:
		if msg.err != nil {
			return m, m.toast("DDL: "+msg.err.Error(), toastError)
		}
		if m.hasTab(msg.tab) {
			msg.tab.out.add(&textPanel{name: "DDL " + msg.name, tv: components.NewTextView(msg.ddl, components.LexerFor(msg.tab.ac.cfg.Driver))})
			m.focus = focusOutput
		}
		return m, nil
	case grantsMsg:
		if msg.err != nil {
			return m, m.toast("Grants: "+msg.err.Error(), toastError)
		}
		var lines []string
		for _, r := range msg.table.Rows {
			var parts []string
			for _, c := range r {
				parts = append(parts, c.S)
			}
			lines = append(lines, strings.Join(parts, "  "))
		}
		m.push(newTextModal("Grants for "+msg.user, strings.Join(lines, "\n"), ""))
		return m, nil
	case adminMsg:
		return m, msg.panel.apply(msg, m.settings.MaxColumnWidth)
	case explainMsg:
		t := msg.tab
		if !m.hasTab(t) {
			return m, nil
		}
		if msg.err != nil {
			t.out.msgs.log(logError, "EXPLAIN failed: "+msg.err.Error())
			t.out.ix = 0
			return m, m.toast("Explain failed — see Messages", toastError)
		}
		p := newExplainPanel(msg.sql, msg.plan, msg.analyze)
		t.lastRun = "plan ready"
		for _, x := range t.out.panels {
			if _, ok := x.(*explainPanel); ok {
				t.out.replace(x, p)
				m.focus = focusOutput
				return m, nil
			}
		}
		t.out.add(p)
		m.focus = focusOutput
		return m, nil
	case txMsg:
		if !m.hasTab(msg.tab) {
			return m, nil
		}
		if msg.err != nil {
			msg.tab.out.msgs.log(logError, msg.what+" failed: "+msg.err.Error())
			return m, m.toast(msg.what+" failed: "+msg.err.Error(), toastError)
		}
		msg.tab.out.msgs.log(logOK, msg.what)
		return m, m.toast(msg.what+" done", toastInfo)
	case editsAppliedMsg:
		if !m.hasTab(msg.tab) {
			return m, nil
		}
		return m, m.handleEditsApplied(msg)
	case jobDoneMsg:
		m.jobs--
		if msg.err != nil {
			if t := m.currentTab(); t != nil {
				t.out.msgs.log(logError, msg.err.Error())
			}
			return m, m.toast(msg.err.Error(), toastError)
		}
		if t := m.currentTab(); t != nil {
			t.out.msgs.log(logOK, msg.text)
		}
		return m, m.toast(msg.text, toastInfo)
	case testMsg:
		if m.mgr != nil {
			m.mgr.applyTest(msg)
		}
		return m, nil
	case dbPickMsg:
		return m, m.handleDBPick(msg)
	case dbListMsg:
		if m.mgr != nil {
			m.mgr.applyDBList(m, msg)
		}
		return m, nil
	case tea.MouseMsg:
		return m, m.handleMouse(msg)
	case tea.KeyMsg:
		// Fast typing, SSH latency or an unbracketed paste can deliver several
		// runes in one message. Its String() is the text itself, so "end",
		// "up" or "esc" would match key bindings; handle each rune on its own.
		if msg.Type == tea.KeyRunes && len(msg.Runes) > 1 && !msg.Paste {
			var cmds []tea.Cmd
			for _, r := range msg.Runes {
				cmds = append(cmds, m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Alt: msg.Alt}))
			}
			return m, tea.Batch(cmds...)
		}
		return m, m.handleKey(msg)
	}
	return m, nil
}

func (m *Model) removeModal(md modal) {
	for i, x := range m.modals {
		if x == md {
			m.modals = append(m.modals[:i], m.modals[i+1:]...)
			return
		}
	}
}

func (m *Model) handleKey(km tea.KeyMsg) tea.Cmd {
	if n := len(m.modals); n > 0 {
		md := m.modals[n-1]
		done, cmd := md.update(m, km)
		if done {
			m.removeModal(md)
		}
		return cmd
	}
	k := km.String()
	if k == "ctrl+q" {
		return m.quit()
	}
	if m.screen == screenConnections {
		return m.mgr.update(m, km)
	}
	if m.cmdline != nil {
		return m.updateCmdline(km)
	}
	t := m.currentTab()
	if m.winPrefix {
		m.winPrefix = false
		return m.windowKey(k)
	}
	inserting := m.focus == focusEditor && t != nil && t.editor.Inserting()
	switch k {
	case "f1":
		return m.showHelp()
	case "ctrl+o":
		m.openConnections()
		return nil
	case "ctrl+p":
		return m.palette()
	case "ctrl+t":
		return m.newTabPicker()
	case "ctrl+w":
		m.winPrefix = true
		return nil
	case "alt+]", "ctrl+pgdown", "alt+right":
		if len(m.tabs) > 0 && !(inserting && k == "alt+right") {
			m.cur = (m.cur + 1) % len(m.tabs)
			return nil
		}
	case "alt+[", "ctrl+pgup", "alt+left":
		if len(m.tabs) > 0 && !(inserting && k == "alt+left") {
			m.cur = (m.cur - 1 + len(m.tabs)) % len(m.tabs)
			return nil
		}
	case "ctrl+b":
		m.hideExplore = !m.hideExplore
		if m.hideExplore && m.focus == focusExplorer {
			m.focus = focusEditor
		}
		return nil
	case "shift+tab":
		m.cycleFocus(-1)
		return nil
	}
	if len(k) == 5 && strings.HasPrefix(k, "alt+") && k[4] >= '1' && k[4] <= '9' {
		if i := int(k[4] - '1'); i < len(m.tabs) {
			m.cur = i
		}
		return nil
	}
	if t == nil {
		return nil
	}
	switch k {
	case "f5", "ctrl+j", "alt+enter":
		return m.runCurrent(t)
	case "f6":
		return m.runAll(t)
	case "f7":
		return m.explain(t, false)
	case "f8":
		return m.explain(t, true)
	case "ctrl+g":
		return m.txMenu(t)
	case "ctrl+c":
		if t.busy {
			return m.cancel(t)
		}
		if m.focus == focusEditor && !m.settings.VimMode {
			break // editor copies the selection
		}
		return m.toast("Nothing to cancel — ctrl+q quits", toastInfo)
	case "?":
		if !inserting {
			return m.showHelp()
		}
	case ":":
		if m.focus != focusEditor {
			m.openCmdline("")
			return nil
		}
	case "tab":
		if !inserting {
			m.cycleFocus(1)
			return nil
		}
	}
	switch m.focus {
	case focusExplorer:
		if t.ac.explorer != nil {
			return t.ac.explorer.update(m, km)
		}
	case focusEditor:
		return m.editorKey(t, km)
	case focusOutput:
		if k == "esc" && !t.out.hasFilter() {
			m.focus = focusEditor
			return nil
		}
		return t.out.update(m, t, km)
	}
	return nil
}

func (o *output) hasFilter() bool {
	if r, ok := o.current().(*resultPanel); ok {
		return r.grid.Filter() != ""
	}
	return false
}

func (m *Model) editorKey(t *tab, km tea.KeyMsg) tea.Cmd {
	switch t.editor.Update(km) {
	case components.ActRun:
		return m.runCurrent(t)
	case components.ActRunAll:
		return m.runAll(t)
	case components.ActExplain:
		return m.explain(t, false)
	case components.ActHistory:
		return m.historyPicker(t)
	case components.ActSnippets:
		return m.snippetPicker(t)
	case components.ActComplete:
		m.complete(t, true)
	case components.ActCompleteUpdate:
		m.complete(t, false)
	case components.ActCommand:
		m.openCmdline("")
	case components.ActYank:
		return m.copy(t.editor.Register(), "text")
	}
	return nil
}

// complete shows autocomplete suggestions at the cursor.
func (m *Model) complete(t *tab, explicit bool) {
	src := t.editor.Value()
	c := schema.Complete(src, t.editor.CursorOffset(), t.ac.cache, t.completionContext())
	if len(c.Items) == 0 || (!explicit && len(c.Items) == 1 && strings.EqualFold(c.Items[0].Label, c.Prefix)) {
		t.editor.CloseCompletion()
		return
	}
	items := make([]components.CompletionItem, len(c.Items))
	for i, it := range c.Items {
		items[i] = components.CompletionItem{Label: it.Label, Insert: it.Insert, Kind: string(it.Kind), Detail: it.Detail}
	}
	t.editor.ShowCompletion(items, c.Start, explicit)
}

func (m *Model) windowKey(k string) tea.Cmd {
	switch k {
	case "h", "left":
		if !m.hideExplore {
			m.focus = focusExplorer
		}
	case "l", "right":
		if m.focus == focusExplorer {
			m.focus = focusEditor
		}
	case "j", "down":
		m.focus = focusOutput
	case "k", "up":
		m.focus = focusEditor
	case "w", "ctrl+w":
		m.cycleFocus(1)
	case "z", "o":
		m.zoom = !m.zoom
	case "<":
		m.explorerW = max(16, m.explorerW-4)
	case ">":
		m.explorerW = min(m.width/2, m.explorerW+4)
	case "-":
		m.editorPct = max(15, m.editorPct-5)
	case "+", "=":
		m.editorPct = min(85, m.editorPct+5)
	case "q", "c":
		if t := m.currentTab(); t != nil {
			return m.confirmCloseTab(t)
		}
	case "n":
		return m.newTabPicker()
	}
	return nil
}

func (m *Model) cycleFocus(d int) {
	order := []focusArea{focusExplorer, focusEditor, focusOutput}
	if m.hideExplore {
		order = order[1:]
	}
	ix := 0
	for i, f := range order {
		if f == m.focus {
			ix = i
		}
	}
	m.focus = order[(ix+d+len(order))%len(order)]
}

func (m *Model) confirmCloseTab(t *tab) tea.Cmd {
	dirty := false
	for _, r := range t.out.results() {
		dirty = dirty || r.dirty()
	}
	if !t.inTx && !dirty {
		m.closeTab(t)
		return nil
	}
	why := "an open transaction (it will be rolled back)"
	if dirty {
		why = "unsaved edits"
	}
	m.push(&confirmModal{title: "Close tab", danger: true, body: fmt.Sprintf("Tab %q has %s. Close anyway?", t.title(), why),
		onYes: func() tea.Cmd { m.closeTab(t); return nil }})
	return nil
}

func (m *Model) quit() tea.Cmd {
	var open []string
	for _, t := range m.tabs {
		dirty := false
		for _, r := range t.out.results() {
			dirty = dirty || r.dirty()
		}
		if t.inTx || dirty {
			open = append(open, t.title())
		}
	}
	if len(open) == 0 {
		return m.quitNow()
	}
	m.push(&confirmModal{title: "Quit squi", danger: true,
		body:  "Open transactions or unsaved edits in: " + strings.Join(open, ", ") + ".\nThey will be rolled back. Quit anyway?",
		onYes: m.quitNow})
	return nil
}

// quitNow restores the terminal's key reporting (while still on the
// alternate screen) and exits.
func (m *Model) quitNow() tea.Cmd {
	if m.termWrite == nil {
		return tea.Quit
	}
	w := m.termWrite
	return tea.Sequence(func() tea.Msg { w(termkeys.Disable); return nil }, tea.Quit)
}

func (m *Model) newTabPicker() tea.Cmd {
	var items []components.PickerItem
	if t := m.currentTab(); t != nil {
		items = append(items, components.PickerItem{Title: "New tab on " + t.ac.cfg.Title(), Badge: theme.EnvBadge(t.ac.cfg.Env), Value: t.ac.cfg})
	}
	for _, c := range m.conns {
		items = append(items, components.PickerItem{Title: c.Title(), Detail: c.Driver + " " + c.Host, Badge: theme.EnvBadge(c.Env), Value: c})
	}
	m.push(newPickerModal("Open tab", items, func(it components.PickerItem) tea.Cmd {
		return m.connect(it.Value.(config.Connection))
	}))
	return nil
}

func (m *Model) handleMouse(msg tea.MouseMsg) tea.Cmd {
	if len(m.modals) > 0 {
		md := m.modals[len(m.modals)-1]
		if done, cmd := md.update(m, msg); done {
			m.removeModal(md)
			return cmd
		}
		return nil
	}
	if m.screen != screenMain {
		return nil
	}
	t := m.currentTab()
	if t == nil {
		return nil
	}
	l := m.layout()
	area := focusOutput
	switch {
	case !l.hideExplorer && msg.X < l.explorerW:
		area = focusExplorer
	case msg.Y < 1+l.editorH:
		area = focusEditor
	}
	if msg.Y == 0 && msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
		m.clickTabBar(msg.X)
		return nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
		n := 3
		if msg.Button == tea.MouseButtonWheelUp {
			n = -3
		}
		switch area {
		case focusOutput:
			if s, ok := t.out.current().(scroller); ok {
				s.scroll(n)
			}
			if r, ok := t.out.current().(*resultPanel); ok {
				return r.maybeFetch(m, t)
			}
		case focusExplorer:
			if e := t.ac.explorer; e != nil {
				e.ix = clampIx(e.ix+n, len(e.flat))
			}
		}
	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionPress {
			return nil
		}
		m.focus = area
		if area == focusExplorer && t.ac.explorer != nil {
			return t.ac.explorer.click(m, msg.Y-2, msg.X)
		}
	}
	return nil
}

func (m *Model) clickTabBar(x int) {
	pos := 0
	for i, t := range m.tabs {
		w := lipgloss.Width(m.tabLabel(i, t)) + 1
		if x >= pos && x < pos+w {
			m.cur = i
			return
		}
		pos += w
	}
}

// ---- view ----

type layout struct {
	hideExplorer bool
	explorerW    int
	rightW       int
	editorH      int
	outputH      int
	bodyH        int
}

func (m *Model) layout() layout {
	bodyH := m.height - 2
	l := layout{bodyH: bodyH, hideExplorer: m.hideExplore}
	if !l.hideExplorer {
		l.explorerW = min(m.explorerW, m.width/3)
	}
	l.rightW = m.width - l.explorerW
	l.editorH = max(5, bodyH*m.editorPct/100)
	l.outputH = bodyH - l.editorH
	return l
}

// View renders the UI.
func (m *Model) View() string {
	if m.width < 50 || m.height < 14 {
		return "Terminal too small for squi (need 50×14)."
	}
	var base string
	switch {
	case m.screen == screenConnections || len(m.tabs) == 0:
		if m.mgr == nil {
			m.openConnections()
		}
		base = components.PadLines(m.mgr.view(m, m.width, m.height), m.width, m.height)
	default:
		base = m.mainView()
	}
	for _, md := range m.modals {
		box := md.view(m.width, m.height)
		bw, bh := lipgloss.Width(box), lipgloss.Height(box)
		x, y := components.Center(m.width, m.height, bw, bh)
		base = components.Overlay(base, box, x, y)
	}
	return base
}

func (m *Model) tabLabel(i int, t *tab) string {
	label := fmt.Sprintf("%d %s", i+1, t.title())
	if t.busy {
		label += " " + m.spinner.View()
	}
	if t.inTx {
		label += " ●"
	}
	st := theme.InactiveTab
	if i == m.cur {
		st = theme.ActiveTab
		if t.ac.cfg.Env != config.EnvNone {
			st = st.Background(theme.EnvColor(t.ac.cfg.Env))
		}
	} else if t.ac.cfg.Env == config.EnvProd {
		st = st.Foreground(theme.Red)
	}
	return st.Render(label)
}

func (m *Model) mainView() string {
	t := m.currentTab()
	l := m.layout()

	// tab bar
	var tabs []string
	for i, x := range m.tabs {
		tabs = append(tabs, m.tabLabel(i, x))
	}
	bar := strings.Join(tabs, " ") + " " + theme.Dim.Render("ctrl+t new")
	bar = components.FitStyled(bar, m.width)

	var body string
	explorerBox := func(w, h int) string {
		content := ""
		if e := t.ac.explorer; e != nil {
			content = e.view(w-2, h-2, m.focus == focusExplorer, t.db)
		}
		return components.Box(t.ac.cfg.Title(), content, w, h, m.focus == focusExplorer, theme.Accent)
	}
	editorBox := func(w, h int) string {
		t.editor.SetSize(w-2, h-2)
		if m.focus == focusEditor {
			t.editor.Focus()
		} else {
			t.editor.Blur()
		}
		title := "Query"
		if m.settings.VimMode {
			title += " · " + t.editor.Mode().String()
		}
		return components.Box(title, t.editor.View(), w, h, m.focus == focusEditor, theme.Accent)
	}
	outputBox := func(w, h int) string {
		return components.Box("", t.out.view(w-2, h-2, m.focus == focusOutput), w, h, m.focus == focusOutput, theme.Accent)
	}
	if m.zoom {
		switch m.focus {
		case focusExplorer:
			body = explorerBox(m.width, l.bodyH)
		case focusEditor:
			body = editorBox(m.width, l.bodyH)
		default:
			body = outputBox(m.width, l.bodyH)
		}
	} else {
		right := editorBox(l.rightW, l.editorH) + "\n" + outputBox(l.rightW, l.outputH)
		if l.hideExplorer {
			body = right
		} else {
			body = lipgloss.JoinHorizontal(lipgloss.Top, explorerBox(l.explorerW, l.bodyH), right)
		}
	}
	return bar + "\n" + body + "\n" + m.statusBar(t)
}

func (m *Model) statusBar(t *tab) string {
	if m.cmdline != nil {
		return components.FitStyled(m.cmdline.View(), m.width)
	}
	if m.winPrefix {
		return components.FitStyled(theme.Key.Render("ctrl+w")+" h/j/k/l focus · z zoom · </> explorer width · -/+ editor height · q close tab · n new tab", m.width)
	}
	cfg := t.ac.cfg
	bg := lipgloss.TerminalColor(theme.BarBg)
	fg := lipgloss.TerminalColor(theme.Fg)
	if cfg.Env == config.EnvProd {
		bg, fg = theme.Red, lipgloss.Color("#ffffff")
	}
	seg := lipgloss.NewStyle().Background(bg).Foreground(fg)
	var left []string
	if badge := theme.EnvBadge(cfg.Env); badge != "" && cfg.Env != config.EnvProd {
		left = append(left, badge)
	} else if cfg.Env == config.EnvProd {
		left = append(left, seg.Bold(true).Render(" PRODUCTION "))
	}
	left = append(left, seg.Render(" "+cfg.Driver+" "+cfg.Title()))
	if t.db != "" {
		left = append(left, seg.Render("· "+t.db))
	}
	if cfg.SSH.Enabled {
		state := "ssh"
		if t.ac.tunnel != nil {
			state = "ssh " + strings.ToLower(t.ac.tunnel.State().String())
		}
		left = append(left, seg.Render("· "+state))
	}
	if cfg.ReadOnly {
		left = append(left, seg.Bold(true).Render("· READ-ONLY"))
	}
	switch {
	case t.inTx:
		left = append(left, lipgloss.NewStyle().Background(theme.Yellow).Foreground(lipgloss.Color("#000000")).Bold(true).
			Render(fmt.Sprintf(" TX %s ", time.Since(t.txSince).Truncate(time.Second))))
	case t.manualTx:
		left = append(left, seg.Render("· manual tx"))
	}
	right := ""
	switch {
	case time.Now().Before(m.toastUntil) && (!t.busy || m.toastLevel != toastInfo):
		st := seg
		switch m.toastLevel {
		case toastWarn:
			st = st.Foreground(theme.Yellow)
		case toastError:
			st = lipgloss.NewStyle().Background(theme.Red).Foreground(lipgloss.Color("#ffffff"))
		}
		right = st.Render(" " + m.toastText + " ")
	case t.busy:
		right = seg.Render(fmt.Sprintf(" %s %s %s · ctrl+c cancel ", m.spinner.View(), t.busyWhat, fmtDuration(time.Since(t.busySince).Round(100*time.Millisecond))))
	case m.jobs > 0:
		right = seg.Render(fmt.Sprintf(" %s %d background job(s) ", m.spinner.View(), m.jobs))
	default:
		hint := t.lastRun
		if r, ok := t.out.current().(*resultPanel); ok {
			hint = r.status()
		}
		right = seg.Render(" " + hint + " · F1 help ")
	}
	l := strings.Join(left, seg.Render(" "))
	gap := m.width - lipgloss.Width(l) - lipgloss.Width(right)
	if gap < 1 {
		right = components.FitStyled(right, max(0, m.width-lipgloss.Width(l)-1))
		gap = max(1, m.width-lipgloss.Width(l)-lipgloss.Width(right))
	}
	return components.FitStyled(l+seg.Render(strings.Repeat(" ", gap))+right, m.width)
}
