package history

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func sqlsOf(es []Entry) []string {
	out := []string{}
	for _, e := range es {
		out = append(out, e.SQL)
	}
	return out
}

func TestStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	s, err := Open(dir, 100)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	add := func(conn, sql string) {
		base = base.Add(time.Second)
		if err := s.Add(Entry{Time: base, ConnID: conn, ConnName: conn, SQL: sql, DurationMS: 3, Rows: 1}); err != nil {
			t.Fatal(err)
		}
	}
	add("a", "SELECT * FROM users")
	add("b", "SELECT * FROM orders")
	add("a", "SELECT count(*) FROM users")
	add("a", "SELECT * FROM users")
	add("a", "DELETE FROM sessions")

	if got, want := sqlsOf(s.List("a", 0)), []string{"DELETE FROM sessions", "SELECT * FROM users", "SELECT count(*) FROM users", "SELECT * FROM users"}; !reflect.DeepEqual(got, want) {
		t.Errorf("List(a) = %q", got)
	}
	if got := s.List("", 2); len(got) != 2 || got[0].SQL != "DELETE FROM sessions" {
		t.Errorf("List(all, 2) = %q", sqlsOf(got))
	}
	if got, want := sqlsOf(s.Search("", "a", 0)), []string{"DELETE FROM sessions", "SELECT * FROM users", "SELECT count(*) FROM users"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Search('') = %q", got)
	}
	got := s.Search("users", "", 0)
	if len(got) != 2 || !strings.Contains(got[0].SQL, "users") || !strings.Contains(got[1].SQL, "users") {
		t.Errorf("Search(users) = %q", sqlsOf(got))
	}
	if got := s.Search("fromorders", "", 1); len(got) != 1 || got[0].SQL != "SELECT * FROM orders" {
		t.Errorf("fuzzy Search = %q", sqlsOf(got))
	}
	if got := s.Search("zzz", "", 0); len(got) != 0 {
		t.Errorf("Search(zzz) = %q", sqlsOf(got))
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(Entry{SQL: "x"}); err == nil {
		t.Error("Add after Close should fail")
	}

	fi, err := os.Stat(filepath.Join(dir, fileName))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, %v", fi.Mode(), err)
	}
	if di, _ := os.Stat(dir); di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v", di.Mode())
	}

	s2, err := Open(dir, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	all := s2.List("", 0)
	if len(all) != 5 || all[0].SQL != "DELETE FROM sessions" || all[0].ConnName != "a" || all[0].DurationMS != 3 || !all[0].Time.Equal(base) {
		t.Errorf("reloaded = %+v", all)
	}
}

func TestCompaction(t *testing.T) {
	dir := t.TempDir()
	lines := []string{"not json", `{"q":""}`}
	for i := 0; i < 30; i++ {
		lines = append(lines, `{"t":"2026-01-01T00:00:00Z","c":"x","q":"SELECT `+strconv.Itoa(i)+`"}`)
	}
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir, 20)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := s.List("", 0); len(got) != 20 || got[0].SQL != "SELECT 29" || got[19].SQL != "SELECT 10" {
		t.Errorf("compacted list = %q", sqlsOf(got))
	}
	b, _ := os.ReadFile(filepath.Join(dir, fileName))
	if n := strings.Count(string(b), "\n"); n != 20 {
		t.Errorf("file has %d lines after compaction", n)
	}
	// Below the 1.25× threshold nothing is rewritten.
	s.Add(Entry{SQL: "SELECT 1"})
	if n := len(s.List("", 0)); n != 21 {
		t.Errorf("in-memory entries = %d", n)
	}
}

func TestConcurrentAdd(t *testing.T) {
	s, err := Open(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = s.Add(Entry{ConnID: "c", SQL: "SELECT 1"})
				_ = s.Search("sel", "c", 5)
			}
		}()
	}
	wg.Wait()
	if n := len(s.List("c", 0)); n != 400 {
		t.Errorf("entries = %d", n)
	}
}

func TestSnippets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg", "snippets.toml")
	if s, err := LoadSnippets(path); err != nil || s != nil {
		t.Fatalf("missing file: %v, %v", s, err)
	}
	want := []Snippet{
		{Name: "active users", SQL: "SELECT *\nFROM users\nWHERE active", Driver: "postgres", Description: "who's \"on\""},
		{Name: "locks", SQL: "SHOW ENGINE INNODB STATUS"},
	}
	if err := SaveSnippets(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSnippets(path)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("LoadSnippets = %+v, %v", got, err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi.Mode())
	}
	if err := os.WriteFile(path, []byte("[[snippet]]\nname = \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSnippets(path); err == nil {
		t.Error("expected parse error")
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
}
