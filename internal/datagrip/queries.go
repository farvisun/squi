package datagrip

import (
	"context"
	"encoding/xml"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Query is a SQL file DataGrip attaches to a data source.
type Query struct {
	Path     string
	Title    string
	Console  bool // a query console (else a project file)
	Modified time.Time
}

var uuidName = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Queries lists the query consoles of data source id, from every DataGrip
// configuration directory under root, and the project files attached to it
// in the project at source (.idea/data_source_mapping.xml), newest first.
// Empty files are left out.
func Queries(root, id, source string) []Query {
	var out []Query
	seen := map[string]bool{}
	add := func(path string, console bool) {
		if seen[path] {
			return
		}
		st, err := os.Stat(path)
		if err != nil || st.IsDir() || st.Size() == 0 {
			return
		}
		seen[path] = true
		name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		if uuidName.MatchString(name) {
			name = "console" // newer DataGrip versions name console files by id
		}
		out = append(out, Query{Path: path, Title: name, Console: console, Modified: st.ModTime()})
	}
	if uuidName.MatchString(id) {
		for _, cfg := range ConfigDirs(root) {
			files, _ := filepath.Glob(filepath.Join(cfg, "consoles", "db", id, "*.sql"))
			for _, f := range files {
				add(f, true)
			}
		}
	}
	if source != "" {
		for _, f := range mappedFiles(source, id) {
			add(f, false)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out
}

// mappedFiles returns the files of project that DataGrip runs against data
// source id.
func mappedFiles(project, id string) []string {
	b, err := os.ReadFile(filepath.Join(project, ".idea", "data_source_mapping.xml"))
	if err != nil {
		return nil
	}
	var f struct {
		Files []struct {
			URL   string `xml:"url,attr"`
			Value string `xml:"value,attr"`
		} `xml:"component>file"`
	}
	if xml.Unmarshal(b, &f) != nil {
		return nil
	}
	var out []string
	for _, e := range f.Files {
		ds, _, _ := strings.Cut(e.Value, "/")
		if ds != id || !strings.HasPrefix(e.URL, "file://") {
			continue
		}
		out = append(out, filepath.Clean(expandMacros(strings.TrimPrefix(e.URL, "file://"), project)))
	}
	return out
}

// ServiceName is the macOS Keychain item under which DataGrip stores the
// password of data source id.
func ServiceName(id string) string { return "IntelliJ Platform DB — " + id }

// ErrNoPassword means DataGrip has no readable saved password.
var ErrNoPassword = errors.New("no DataGrip password")

// Password reads the password DataGrip saved for data source id from the
// macOS Keychain; macOS asks the user to allow the access. Other platforms
// and data sources without a saved password return ErrNoPassword.
func Password(ctx context.Context, id string) (string, error) {
	if runtime.GOOS != "darwin" || !uuidName.MatchString(id) {
		return "", ErrNoPassword
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute) // the Keychain dialog waits for the user
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/security", "find-generic-password", "-s", ServiceName(id), "-w").Output()
	if err != nil {
		return "", ErrNoPassword
	}
	pw := strings.TrimSuffix(string(out), "\n")
	if pw == "" {
		return "", ErrNoPassword
	}
	return pw, nil
}
