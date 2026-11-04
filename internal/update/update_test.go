package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		cur, latest string
		want        bool
	}{
		{"0.6.0", "0.7.0", true},
		{"0.6.0", "v0.7.0", true},
		{"v0.9.0", "0.10.0", true},
		{"0.7.0", "0.7.0", false},
		{"0.7.1", "0.7.0", false},
		{"1.0.0", "0.99.0", false},
		{"dev", "0.7.0", false},
		{"v0.6.0-6-ga4fd005-dirty", "0.7.0", false},
		{"0.6.0", "0.7.0-rc1", false},
	}
	for _, c := range cases {
		if got := Newer(c.cur, c.latest); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.cur, c.latest, got)
		}
	}
	if IsRelease("dev") || IsRelease("(devel)") || !IsRelease("v1.2.3") {
		t.Error("IsRelease")
	}
}

func TestDetect(t *testing.T) {
	t.Setenv("GOBIN", "")
	t.Setenv("GOPATH", "/home/a/gopath")
	cases := []struct {
		path string
		want Method
	}{
		{"/opt/homebrew/Cellar/squi/0.6.0/bin/squi", Homebrew},
		{"/home/linuxbrew/.linuxbrew/bin/squi", Homebrew},
		{"/usr/bin/squi", Package},
		{"/home/a/gopath/bin/squi", GoInstall},
		{"/home/a/scoop/apps/squi/current/squi", Scoop},
		{"/usr/local/bin/squi", Manual},
		{"/home/a/bin/squi", Manual},
	}
	for _, c := range cases {
		got := Detect(c.path)
		if got.Method != c.want {
			t.Errorf("Detect(%q) = %v, want %v", c.path, got.Method, c.want)
		}
		if got.SelfUpdates() != (c.want == Manual) {
			t.Errorf("%s: SelfUpdates = %v", c.path, got.SelfUpdates())
		}
	}
}

// releaseServer serves a release whose binary is a script printing version.
type releaseServer struct {
	*httptest.Server
	archive   []byte
	checksums string
}

func newRelease(t *testing.T, version string, reports string, tamper func(*releaseServer)) (*Client, Release) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as the binary")
	}
	script := []byte("#!/bin/sh\necho squi " + reports + "\n")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct {
		name string
		body []byte
	}{{"README.md", []byte("readme")}, {"squi", script}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write(f.body)
	}
	tw.Close()
	gz.Close()
	name := ArchiveName(version, runtime.GOOS, runtime.GOARCH)
	sum := sha256.Sum256(buf.Bytes())
	rs := &releaseServer{archive: buf.Bytes(), checksums: hex.EncodeToString(sum[:]) + "  " + name + "\n"}
	if tamper != nil {
		tamper(rs)
	}
	mux := http.NewServeMux()
	rs.Server = httptest.NewServer(mux)
	t.Cleanup(rs.Close)
	mux.HandleFunc("/dl/"+name, func(w http.ResponseWriter, _ *http.Request) { w.Write(rs.archive) })
	mux.HandleFunc("/dl/checksums.txt", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, rs.checksums) })
	mux.HandleFunc("/latest", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"tag_name": "v" + version, "html_url": "https://example/rel", "body": "notes",
			"assets": []map[string]string{
				{"name": name, "browser_download_url": rs.URL + "/dl/" + name},
				{"name": "checksums.txt", "browser_download_url": rs.URL + "/dl/checksums.txt"},
			}})
	})
	c := &Client{Current: "0.6.0", HTTP: rs.Client(), API: rs.URL + "/latest", Prefix: rs.URL + "/dl/"}
	rel, err := c.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return c, rel
}

func installed(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "squi")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

func assertUntouched(t *testing.T, exe string) {
	t.Helper()
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Fatalf("the installed binary changed: %q", b)
	}
	if names := dirNames(t, filepath.Dir(exe)); len(names) != 1 {
		t.Fatalf("left files behind: %v", names)
	}
}

func TestApplyReplacesBinary(t *testing.T) {
	c, rel := newRelease(t, "0.7.0", "0.7.0", nil)
	if rel.Version != "0.7.0" || rel.Notes != "notes" {
		t.Fatalf("release %+v", rel)
	}
	exe := installed(t)
	if err := c.Apply(context.Background(), rel, exe); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(exe)
	if !strings.Contains(string(b), "squi 0.7.0") {
		t.Fatalf("binary not replaced: %q", b)
	}
	info, _ := os.Stat(exe)
	if info.Mode()&0o111 == 0 {
		t.Fatal("not executable")
	}
	if names := dirNames(t, filepath.Dir(exe)); len(names) != 1 {
		t.Fatalf("left files behind: %v", names)
	}
}

func TestApplyRefusesTamperedDownload(t *testing.T) {
	c, rel := newRelease(t, "0.7.0", "0.7.0", func(rs *releaseServer) { rs.archive = append(rs.archive, 0) })
	exe := installed(t)
	err := c.Apply(context.Background(), rel, exe)
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("err = %v", err)
	}
	assertUntouched(t, exe)
}

func TestApplyRefusesUnlistedArchive(t *testing.T) {
	c, rel := newRelease(t, "0.7.0", "0.7.0", func(rs *releaseServer) { rs.checksums = "00  other.tar.gz\n" })
	exe := installed(t)
	if err := c.Apply(context.Background(), rel, exe); err == nil {
		t.Fatal("no checksum entry must fail")
	}
	assertUntouched(t, exe)
}

func TestApplyRefusesOtherHosts(t *testing.T) {
	c, rel := newRelease(t, "0.7.0", "0.7.0", nil)
	rel.Assets["checksums.txt"] = "https://evil.example/checksums.txt"
	exe := installed(t)
	err := c.Apply(context.Background(), rel, exe)
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("err = %v", err)
	}
	assertUntouched(t, exe)
}

func TestApplyRefusesWrongVersionBinary(t *testing.T) {
	c, rel := newRelease(t, "0.7.0", "0.5.0", nil)
	exe := installed(t)
	if err := c.Apply(context.Background(), rel, exe); err == nil {
		t.Fatal("a binary reporting another version must not be installed")
	}
	assertUntouched(t, exe)
}

func TestApplyReportsUnwritableDir(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root writes anywhere")
	}
	c, rel := newRelease(t, "0.7.0", "0.7.0", nil)
	exe := installed(t)
	os.Chmod(filepath.Dir(exe), 0o555)
	defer os.Chmod(filepath.Dir(exe), 0o755)
	err := c.Apply(context.Background(), rel, exe)
	if err == nil || !strings.Contains(err.Error(), "not writable") {
		t.Fatalf("err = %v", err)
	}
}

func TestExtractZip(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, _ := zw.Create(binaryName())
	f.Write([]byte("payload"))
	zw.Close()
	dir := t.TempDir()
	arch := filepath.Join(dir, "a.zip")
	os.WriteFile(arch, buf.Bytes(), 0o600)
	af, _ := os.Open(arch)
	defer af.Close()
	bin, err := extract(af, int64(buf.Len()), "a.zip", dir)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(bin); string(b) != "payload" {
		t.Fatalf("got %q", b)
	}
}

func TestLatestRejectsOddTag(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"tag_name":"nightly"}`)
	}))
	defer s.Close()
	c := &Client{Current: "0.6.0", HTTP: s.Client(), API: s.URL}
	if _, err := c.Latest(context.Background()); err == nil {
		t.Fatal("want an error")
	}
}

func TestStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "update.json")
	if st := LoadState(path); !st.CheckedAt.IsZero() {
		t.Fatalf("empty state: %+v", st)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := SaveState(path, State{CheckedAt: now, Latest: Release{Version: "1.2.3"}, Skipped: "1.2.3"}); err != nil {
		t.Fatal(err)
	}
	st := LoadState(path)
	if !st.CheckedAt.Equal(now) || st.Latest.Version != "1.2.3" || st.Skipped != "1.2.3" {
		t.Fatalf("state %+v", st)
	}
	os.WriteFile(path, []byte("{nope"), 0o600)
	if st := LoadState(path); st.Latest.Version != "" {
		t.Fatal("a damaged file should read as empty")
	}
}

func TestCleanupRemovesStaleFilesOnly(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "squi")
	write := func(name string, age time.Duration) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(-age)
		os.Chtimes(p, at, at)
		return p
	}
	write("squi", 48*time.Hour)
	write("other", 48*time.Hour)
	stale := write(".squi-update-1", 2*time.Hour)
	fresh := write(".squi-new-2", time.Minute)
	old := write("squi.old", time.Minute)
	Cleanup(exe)
	for _, p := range []string{stale, old} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s should be gone", p)
		}
	}
	for _, p := range []string{exe, filepath.Join(dir, "other"), fresh} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s should stay", p)
		}
	}
}
