package history

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Snippet is a saved, named SQL fragment.
type Snippet struct {
	Name        string `toml:"name"`
	SQL         string `toml:"sql"`
	Driver      string `toml:"driver,omitempty"` // "" = any engine
	Description string `toml:"description,omitempty"`
}

type snippetFile struct {
	Snippet []Snippet `toml:"snippet"`
}

// LoadSnippets reads [[snippet]] tables from a TOML file. A missing file
// yields no snippets and no error.
func LoadSnippets(path string) ([]Snippet, error) {
	var f snippetFile
	if _, err := toml.DecodeFile(path, &f); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return f.Snippet, nil
}

// SaveSnippets atomically writes snippets to path with mode 0600, creating
// the parent directory (0700) if needed.
func SaveSnippets(path string, s []Snippet) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := toml.NewEncoder(tmp).Encode(snippetFile{Snippet: s}); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
