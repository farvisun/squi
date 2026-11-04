package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Layout is the pane arrangement kept between runs. It is separate from the
// session because it applies even with restore_session off.
type Layout struct {
	ExplorerWidth int  `json:"explorer_width,omitempty"`
	EditorPct     int  `json:"editor_percent,omitempty"`
	HideExplorer  bool `json:"hide_explorer,omitempty"`
}

// LayoutPath is where the pane arrangement is saved.
func (s *Store) LayoutPath() string { return filepath.Join(s.StateDir, "layout.json") }

// LoadLayout returns the saved layout (zero if none).
func (s *Store) LoadLayout() (Layout, error) {
	var lo Layout
	b, err := os.ReadFile(s.LayoutPath())
	if err != nil {
		if os.IsNotExist(err) {
			return lo, nil
		}
		return lo, err
	}
	if err := json.Unmarshal(b, &lo); err != nil {
		return Layout{}, err
	}
	return lo, nil
}

// SaveLayout writes the layout atomically.
func (s *Store) SaveLayout(lo Layout) error {
	b, err := json.MarshalIndent(lo, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(s.LayoutPath(), b)
}
