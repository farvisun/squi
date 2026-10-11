package ui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/farvisun/squi/internal/db"
	"github.com/farvisun/squi/internal/schema"
	"github.com/farvisun/squi/internal/ui/components"
)

// tab is one query workspace bound to a connection. It owns a dedicated
// database session; all session work runs in background ops, one at a time.
type tab struct {
	id     int
	ac     *activeConn
	sess   db.Session
	db     string // current database
	schema string // schema used for completion (pg)

	editor *components.Editor
	out    *output

	busy      bool
	busyWhat  string
	busySince time.Time
	opID      int

	manualTx bool
	inTx     bool
	txSince  time.Time

	toClose []*openStream
	lastRun string // summary of the last run for the status bar
	label   string // optional name shown in the tab bar (e.g. the opened table)
	rank    int    // sort key used while restoring a saved session
}

// openStream is a result stream kept open for lazy loading.
type openStream struct {
	rs     db.ResultStream
	cancel context.CancelFunc
	owner  *tab
}

// close hands the stream to its tab; the next op closes it in the
// background (closing may block briefly while the server is cancelled).
func (s *openStream) close() { s.owner.toClose = append(s.owner.toClose, s) }

func (s *openStream) closeNow() {
	_ = s.rs.Close()
	s.cancel()
}

func (t *tab) title() string {
	name := t.ac.cfg.Title()
	if t.db != "" && t.db != t.ac.cfg.Database {
		name += ":" + t.db
	}
	if t.label != "" {
		name += " · " + t.label
	}
	return name
}

func (t *tab) completionContext() schema.Context {
	ctx := schema.Context{Database: t.db}
	if t.ac.dialect().HasSchemas() {
		s := t.schema
		if s == "" {
			s = "public"
		}
		ctx.SearchPath = []string{s}
		if s != "public" {
			ctx.SearchPath = append(ctx.SearchPath, "public")
		}
	} else {
		ctx.SearchPath = []string{t.db}
	}
	return ctx
}

// opDoneMsg is delivered when a tab op finishes.
type opDoneMsg struct {
	tab   *tab
	id    int
	sess  db.Session
	inTx  bool
	curDB string
	err   error // session creation error
	inner tea.Msg
}

// op runs fn against the tab's session in the background. Open result
// streams hold the session, so they are closed first; the session itself is
// created on first use.
func (m *Model) op(t *tab, what string, fn func(ctx context.Context, s db.Session) tea.Msg) tea.Cmd {
	return m.opKeep(t, nil, what, fn)
}

// opKeep is op, but leaves keep open (used to fetch more rows from it).
func (m *Model) opKeep(t *tab, keep *openStream, what string, fn func(ctx context.Context, s db.Session) tea.Msg) tea.Cmd {
	if t.busy {
		return m.toast("Tab is busy ("+t.busyWhat+") — ctrl+c cancels", toastWarn)
	}
	for _, r := range t.out.results() {
		if r.stream != nil && r.stream != keep {
			r.closeStream()
		}
	}
	t.busy, t.busyWhat, t.busySince = true, what, time.Now()
	t.opID++
	id := t.opID
	pending := t.toClose
	t.toClose = nil
	sess, ac, dbName := t.sess, t.ac, t.db
	return tea.Batch(m.spinner.Tick, func() tea.Msg {
		for _, s := range pending {
			s.closeNow()
		}
		if sess == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			s, err := ac.conn.NewSession(ctx, dbName)
			cancel()
			if err != nil {
				return opDoneMsg{tab: t, id: id, err: err}
			}
			sess = s
		}
		inner := fn(context.Background(), sess)
		return opDoneMsg{tab: t, id: id, sess: sess, inTx: sess.InTx(), curDB: sess.CurrentDatabase(), inner: inner}
	})
}

func (m *Model) handleOpDone(msg opDoneMsg) tea.Cmd {
	t := msg.tab
	if !m.hasTab(t) {
		// tab closed while the op ran
		if msg.sess != nil {
			go msg.sess.Close()
		}
		return nil
	}
	t.busy = false
	if msg.err != nil {
		t.out.msgs.log(logError, "Could not open session: "+msg.err.Error())
		t.out.ix = 0
		return m.toast("Session error: "+msg.err.Error(), toastError)
	}
	if t.sess == nil {
		t.sess = msg.sess
	}
	if msg.curDB != "" {
		if t.db != msg.curDB {
			if t.db != "" {
				t.label = "" // it was named after a table of the previous database
			}
			t.db = msg.curDB
			m.ensureCache(t)
		}
	}
	if msg.inTx && !t.inTx {
		t.txSince = time.Now()
	}
	t.inTx = msg.inTx
	if msg.inner != nil {
		return func() tea.Msg { return msg.inner }
	}
	return nil
}

// cancel aborts the running statement of t.
func (m *Model) cancel(t *tab) tea.Cmd {
	if !t.busy || t.sess == nil {
		return nil
	}
	s := t.sess
	go func() {
		ctx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		_ = s.Cancel(ctx)
	}()
	return m.toast("Cancelling…", toastInfo)
}

// closeTab releases a tab's session and its connection when unused.
func (m *Model) closeTab(t *tab) {
	for _, r := range t.out.results() {
		r.closeStream()
	}
	pending, sess := t.toClose, t.sess
	busy := t.busy
	go func() {
		if busy && sess != nil {
			ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
			_ = sess.Cancel(ctx)
			c()
		}
		for _, s := range pending {
			s.closeNow()
		}
		if sess != nil && !busy {
			_ = sess.Close()
		}
	}()
	for i, x := range m.tabs {
		if x == t {
			m.tabs = append(m.tabs[:i], m.tabs[i+1:]...)
			break
		}
	}
	t.ac.refs--
	if t.ac.refs <= 0 {
		ac := t.ac
		delete(m.active, ac.cfg.ID)
		go ac.close()
	}
	m.cur = clampIx(m.cur, len(m.tabs))
	if len(m.tabs) == 0 {
		m.openConnections()
	}
}

func (m *Model) hasTab(t *tab) bool {
	for _, x := range m.tabs {
		if x == t {
			return true
		}
	}
	return false
}
