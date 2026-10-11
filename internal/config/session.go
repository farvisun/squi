package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// SessionTab is one query tab saved between runs.
type SessionTab struct {
	Conn     string `json:"conn"` // connection ID
	Database string `json:"database,omitempty"`
	Schema   string `json:"schema,omitempty"`
	Label    string `json:"label,omitempty"`
	SQL      string `json:"sql"`
	Cursor   int    `json:"cursor,omitempty"`
	ManualTx bool   `json:"manual_tx,omitempty"`
	Active   bool   `json:"active,omitempty"`
	// Seq is the tab's position in the saved session (not stored).
	Seq int `json:"-"`
}

// Session is the set of open tabs, restored at the next start.
type Session struct {
	Tabs []SessionTab `json:"tabs"`
}

// SessionPath is where the open tabs are saved.
func (s *Store) SessionPath() string { return filepath.Join(s.StateDir, "session.json") }

// LoadSession returns the saved session (empty if none).
func (s *Store) LoadSession() (Session, error) {
	var sess Session
	b, err := os.ReadFile(s.SessionPath())
	if err != nil {
		if os.IsNotExist(err) {
			return sess, nil
		}
		return sess, err
	}
	if err := json.Unmarshal(b, &sess); err != nil {
		return Session{}, err
	}
	for i := range sess.Tabs {
		sess.Tabs[i].Seq = i
	}
	return sess, nil
}

// SaveSession writes the session atomically (mode 0600: it holds SQL text).
func (s *Store) SaveSession(sess Session) error {
	b, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(s.SessionPath(), b)
}
