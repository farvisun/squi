package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/farvisun/squi/internal/config"
	"github.com/farvisun/squi/internal/db"
)

// sessionState captures the open tabs (and tabs still waiting for their
// connection) for the next start.
func (m *Model) sessionState() config.Session {
	var sess config.Session
	for i, t := range m.tabs {
		sess.Tabs = append(sess.Tabs, config.SessionTab{
			Conn:     t.ac.cfg.ID,
			Database: t.db,
			Schema:   t.schema,
			Label:    t.label,
			SQL:      t.editor.Value(),
			Cursor:   t.editor.CursorOffset(),
			ManualTx: t.manualTx,
			Active:   i == m.cur,
		})
	}
	for _, st := range m.pending {
		if _, ok := m.connByID(st.Conn); ok {
			st.Active = st.Active && len(m.tabs) == 0
			sess.Tabs = append(sess.Tabs, st)
		}
	}
	return sess
}

// saveSession writes the session file when it changed.
func (m *Model) saveSession() error {
	if !m.settings.RestoreSession || m.store == nil {
		return nil
	}
	sess := m.sessionState()
	b, err := json.Marshal(sess)
	if err != nil || bytes.Equal(b, m.lastSession) {
		return err
	}
	if err := m.store.SaveSession(sess); err != nil {
		return err
	}
	m.lastSession = b
	return nil
}

// restoreTabs recreates the saved tabs of ac. It returns nil when there
// were none.
func (m *Model) restoreTabs(ac *activeConn) tea.Cmd {
	var mine, rest []config.SessionTab
	for _, st := range m.pending {
		if st.Conn == ac.cfg.ID {
			mine = append(mine, st)
		} else {
			rest = append(rest, st)
		}
	}
	if len(mine) == 0 {
		return nil
	}
	m.pending = rest
	cur := m.currentTab()
	var cmds []tea.Cmd
	for _, st := range mine {
		t := m.createTab(ac)
		t.rank = st.Seq
		if st.Database != "" {
			t.db = st.Database
		}
		t.schema, t.label = st.Schema, st.Label
		t.manualTx = st.ManualTx && ac.dialect().Transactions()
		if st.SQL != "" {
			t.editor.SetValue(st.SQL)
			t.editor.SetCursorOffset(st.Cursor)
		}
		if st.Active || cur == nil {
			cur = t
		}
		schema := t.schema
		cmds = append(cmds, m.op(t, "opening session", func(ctx context.Context, s db.Session) tea.Msg {
			if schema != "" && ac.dialect().HasSchemas() {
				if rs, err := s.Run(ctx, "SET search_path TO "+ac.dialect().QuoteIdent(schema)+", public"); err == nil {
					_, _ = rs.Next(1)
					_ = rs.Close()
				}
			}
			return nil
		}))
	}
	// keep the saved order across connections that finish in any order
	sort.SliceStable(m.tabs, func(i, j int) bool { return m.tabs[i].rank < m.tabs[j].rank })
	for i, t := range m.tabs {
		if t == cur {
			m.cur = i
		}
	}
	if m.mgr != nil && len(m.pending) == 0 {
		m.mgr.status = ""
	}
	return tea.Batch(cmds...)
}
