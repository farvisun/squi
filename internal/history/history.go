// Package history persists executed queries and user snippets.
package history

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sahilm/fuzzy"
)

// Entry is one executed statement.
type Entry struct {
	Time       time.Time `json:"t"`
	ConnID     string    `json:"c"`
	ConnName   string    `json:"n"`
	Database   string    `json:"d,omitempty"`
	SQL        string    `json:"q"`
	DurationMS int64     `json:"ms"`
	Rows       int64     `json:"r"`
	Error      string    `json:"e,omitempty"`
}

// DefaultLimit is used when Open is given a non-positive limit.
const DefaultLimit = 10000

const fileName = "history.jsonl"

// Store is an append-only JSON Lines history file with an in-memory index.
// It is safe for concurrent use.
type Store struct {
	mu      sync.Mutex
	f       *os.File
	limit   int
	entries []Entry // oldest first
}

// Open loads dir/history.jsonl, creating dir (0700) and the file (0600) if
// needed. When the file holds more than 1.25×limit entries it is compacted
// to the newest limit entries. Malformed lines are skipped.
func Open(dir string, limit int) (*Store, error) {
	if limit <= 0 {
		limit = DefaultLimit
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, fileName)
	entries, err := load(path)
	if err != nil {
		return nil, err
	}
	if len(entries) > limit*5/4 {
		entries = entries[len(entries)-limit:]
		if err := rewrite(path, entries); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	return &Store{f: f, limit: limit, entries: entries}, nil
}

func load(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 64<<20)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.SQL != "" {
			out = append(out, e)
		}
	}
	return out, sc.Err()
}

// rewrite atomically replaces path with entries.
func rewrite(path string, entries []Entry) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), fileName+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	w := bufio.NewWriter(tmp)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for _, e := range entries {
		if err := enc.Encode(e); err != nil {
			tmp.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
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

// Add appends e (stamping the current time if e.Time is zero).
func (s *Store) Add(e Entry) error {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return os.ErrClosed
	}
	if _, err := s.f.Write(append(b, '\n')); err != nil {
		return err
	}
	s.entries = append(s.entries, e)
	if len(s.entries) > s.limit*5/4 {
		s.entries = append([]Entry(nil), s.entries[len(s.entries)-s.limit:]...)
	}
	return nil
}

// List returns up to n entries (n <= 0: all), newest first, for connID ("" =
// all connections).
func (s *Store) List(connID string, n int) []Entry {
	return s.collect(connID, n, false)
}

// collect walks entries newest first, optionally keeping only the newest
// entry per distinct SQL text.
func (s *Store) collect(connID string, n int, dedup bool) []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Entry
	seen := map[string]bool{}
	for i := len(s.entries) - 1; i >= 0 && (n <= 0 || len(out) < n); i-- {
		e := s.entries[i]
		if connID != "" && e.ConnID != connID {
			continue
		}
		if dedup {
			key := strings.TrimSpace(e.SQL)
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		out = append(out, e)
	}
	return out
}

type entrySource []Entry

func (s entrySource) String(i int) string { return s[i].SQL }
func (s entrySource) Len() int            { return len(s) }

// Search fuzzy-matches query against the SQL of connID's entries ("" = all)
// and returns up to n (n <= 0: all) best matches, one per distinct SQL text
// (the newest). Equal scores keep newest first. An empty query lists the
// distinct entries newest first.
func (s *Store) Search(query, connID string, n int) []Entry {
	query = strings.TrimSpace(query)
	if query == "" {
		return s.collect(connID, n, true)
	}
	cands := s.collect(connID, 0, true)
	matches := fuzzy.FindFromNoSort(query, entrySource(cands))
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].Score > matches[j].Score })
	if n > 0 && len(matches) > n {
		matches = matches[:n]
	}
	out := make([]Entry, len(matches))
	for i, m := range matches {
		out[i] = cands[m.Index]
	}
	return out
}

// Close closes the history file.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return nil
	}
	err := s.f.Close()
	s.f = nil
	return err
}
