// Package update finds out whether a newer squi release exists and replaces a
// manually installed binary with it. Downloads are checked against the
// release's checksums.txt, which protects against corrupt and tampered
// downloads but not against a compromise of the release itself.
package update

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	apiURL         = "https://api.github.com/repos/farvisun/squi/releases/latest"
	downloadPrefix = "https://github.com/farvisun/squi/releases/download/"

	maxAPIBytes      = 2 << 20
	maxChecksumBytes = 1 << 20
	maxArchiveBytes  = 200 << 20
	maxBinaryBytes   = 300 << 20

	// CheckEvery is how often the background check calls out.
	CheckEvery = 24 * time.Hour
)

// Release describes a published release.
type Release struct {
	Version string            `json:"version"`
	URL     string            `json:"url"`
	Notes   string            `json:"notes,omitempty"`
	Assets  map[string]string `json:"assets,omitempty"` // file name -> download URL
}

// Client talks to the release host.
type Client struct {
	Current string // the running version
	HTTP    *http.Client
	API     string // the latest-release endpoint
	Prefix  string // downloads must start with this
}

// New returns a client for the published releases.
func New(current string) *Client {
	return &Client{Current: current, HTTP: &http.Client{Timeout: 5 * time.Minute}, API: apiURL, Prefix: downloadPrefix}
}

// parse reads "1.2.3" or "v1.2.3". Anything else, such as a pre-release or a
// build from a work tree, is not a released version.
func parse(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// IsRelease reports whether v is a released version. Other builds never check.
func IsRelease(v string) bool {
	_, ok := parse(v)
	return ok
}

// Newer reports whether latest is a later release than current.
func Newer(current, latest string) bool {
	c, ok1 := parse(current)
	l, ok2 := parse(latest)
	if !ok1 || !ok2 {
		return false
	}
	for i := range c {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

func (c *Client) get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "squi/"+c.Current)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return resp, nil
}

// Latest fetches the latest published release.
func (c *Client) Latest(ctx context.Context) (Release, error) {
	resp, err := c.get(ctx, c.API)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	var body struct {
		Tag    string `json:"tag_name"`
		URL    string `json:"html_url"`
		Notes  string `json:"body"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAPIBytes)).Decode(&body); err != nil {
		return Release{}, fmt.Errorf("read release: %w", err)
	}
	if !IsRelease(body.Tag) {
		return Release{}, fmt.Errorf("unexpected release tag %q", body.Tag)
	}
	rel := Release{Version: strings.TrimPrefix(body.Tag, "v"), URL: body.URL, Notes: body.Notes, Assets: map[string]string{}}
	for _, a := range body.Assets {
		rel.Assets[a.Name] = a.URL
	}
	return rel, nil
}

// ArchiveName is the release file holding the binary for a platform.
func ArchiveName(version, goos, goarch string) string {
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("squi_%s_%s_%s.%s", version, goos, goarch, ext)
}

func (c *Client) assetURL(rel Release, name string) (string, error) {
	u, ok := rel.Assets[name]
	if !ok {
		return "", fmt.Errorf("release %s has no %s", rel.Version, name)
	}
	if !strings.HasPrefix(u, c.Prefix) {
		return "", fmt.Errorf("refusing to download %s from %s", name, u)
	}
	return u, nil
}

// checksum returns the SHA-256 listed for name in the release's checksums.txt.
func (c *Client) checksum(ctx context.Context, rel Release, name string) ([]byte, error) {
	u, err := c.assetURL(rel, "checksums.txt")
	if err != nil {
		return nil, err
	}
	resp, err := c.get(ctx, u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(io.LimitReader(resp.Body, maxChecksumBytes))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && f[1] == name {
			return hex.DecodeString(f[0])
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("checksums.txt does not list %s", name)
}

// Apply downloads rel, verifies it and replaces the binary at exe. The running
// program keeps working; the new version starts with the next run.
func (c *Client) Apply(ctx context.Context, rel Release, exe string) error {
	name := ArchiveName(rel.Version, runtime.GOOS, runtime.GOARCH)
	u, err := c.assetURL(rel, name)
	if err != nil {
		return err
	}
	want, err := c.checksum(ctx, rel, name)
	if err != nil {
		return err
	}
	dir := filepath.Dir(exe)
	archive, err := os.CreateTemp(dir, ".squi-update-*")
	if err != nil {
		return fmt.Errorf("%s is not writable: %w", dir, err)
	}
	defer os.Remove(archive.Name())
	defer archive.Close()
	resp, err := c.get(ctx, u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(archive, h), io.LimitReader(resp.Body, maxArchiveBytes+1))
	if err != nil {
		return fmt.Errorf("download %s: %w", name, err)
	}
	if n > maxArchiveBytes {
		return fmt.Errorf("%s is larger than expected", name)
	}
	if subtle.ConstantTimeCompare(h.Sum(nil), want) != 1 {
		return fmt.Errorf("%s does not match its checksum", name)
	}
	bin, err := extract(archive, n, name, dir)
	if err != nil {
		return err
	}
	defer os.Remove(bin)
	if err := verifyRuns(ctx, bin, rel.Version); err != nil {
		return err
	}
	return replace(bin, exe)
}

func binaryName() string {
	if runtime.GOOS == "windows" {
		return "squi.exe"
	}
	return "squi"
}

// extract writes the binary inside the archive to a new executable file in dir.
func extract(archive *os.File, size int64, name, dir string) (string, error) {
	var src io.Reader
	if strings.HasSuffix(name, ".zip") {
		zr, err := zip.NewReader(archive, size)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", name, err)
		}
		f, err := zr.Open(binaryName())
		if err != nil {
			return "", fmt.Errorf("%s has no %s", name, binaryName())
		}
		defer f.Close()
		src = f
	} else {
		if _, err := archive.Seek(0, io.SeekStart); err != nil {
			return "", err
		}
		gz, err := gzip.NewReader(archive)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", name, err)
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		for src == nil {
			hdr, err := tr.Next()
			if errors.Is(err, io.EOF) {
				return "", fmt.Errorf("%s has no %s", name, binaryName())
			}
			if err != nil {
				return "", fmt.Errorf("read %s: %w", name, err)
			}
			if hdr.Name == binaryName() && hdr.Typeflag == tar.TypeReg {
				src = tr
			}
		}
	}
	pattern := ".squi-new-*"
	if runtime.GOOS == "windows" {
		pattern += ".exe" // Windows only runs files with this suffix
	}
	out, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	n, err := io.Copy(out, io.LimitReader(src, maxBinaryBytes+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > maxBinaryBytes {
		err = errors.New("binary is larger than expected")
	}
	if err == nil {
		err = os.Chmod(out.Name(), 0o755)
	}
	if err != nil {
		os.Remove(out.Name())
		return "", err
	}
	return out.Name(), nil
}

// verifyRuns makes sure the new binary starts here and is the expected version.
func verifyRuns(ctx context.Context, bin, version string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return fmt.Errorf("the new version does not run on this machine: %w", err)
	}
	if !bytes.Contains(out, []byte(version)) {
		return fmt.Errorf("the new binary reports %q, expected %s", strings.TrimSpace(string(out)), version)
	}
	return nil
}

// replace puts bin in exe's place atomically where the platform allows.
func replace(bin, exe string) error {
	if runtime.GOOS != "windows" {
		return os.Rename(bin, exe)
	}
	// A running .exe can be renamed but not overwritten.
	old := exe + ".old"
	os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return err
	}
	if err := os.Rename(bin, exe); err != nil {
		if rerr := os.Rename(old, exe); rerr != nil {
			return fmt.Errorf("%w (and restoring the old binary failed: %v; it is at %s)", err, rerr, old)
		}
		return err
	}
	return nil
}

// Cleanup removes what earlier updates left behind: the old binary a Windows
// update has to keep, and temporary files of an update that was interrupted.
// Recent temporary files may belong to an update still running in another squi.
func Cleanup(exe string) {
	os.Remove(exe + ".old")
	dir := filepath.Dir(exe)
	for _, pattern := range []string{".squi-update-*", ".squi-new-*"} {
		files, _ := filepath.Glob(filepath.Join(dir, pattern))
		for _, f := range files {
			if info, err := os.Stat(f); err == nil && time.Since(info.ModTime()) > time.Hour {
				os.Remove(f)
			}
		}
	}
}

// Executable returns the real path of the running binary.
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// Method is how squi was installed.
type Method int

const (
	Manual Method = iota
	Homebrew
	Scoop
	Package
	GoInstall
)

// Install describes an installation.
type Install struct {
	Method  Method
	Name    string // shown to the user
	Command string // upgrades it; empty for Manual and Package
}

// SelfUpdates reports whether squi may replace this binary itself. Package
// managers own their files, so those installs only get told what to run.
func (i Install) SelfUpdates() bool { return i.Method == Manual }

// Detect works out how the binary at exe was installed, from its path.
func Detect(exe string) Install {
	p := filepath.ToSlash(exe)
	switch {
	case strings.Contains(p, "/Cellar/"), strings.Contains(p, "/homebrew/"), strings.Contains(p, "/linuxbrew/"):
		return Install{Homebrew, "Homebrew", "brew upgrade farvisun/tap/squi"}
	case strings.Contains(strings.ToLower(p), "/scoop/"):
		return Install{Scoop, "Scoop", "scoop update squi"}
	case strings.HasPrefix(p, "/usr/bin/"), strings.HasPrefix(p, "/usr/sbin/"):
		return Install{Package, "a system package", ""}
	}
	if isGoBin(exe) {
		return Install{GoInstall, "go install", "go install github.com/farvisun/squi/cmd/squi@latest"}
	}
	return Install{Method: Manual, Name: "a downloaded binary"}
}

func isGoBin(exe string) bool {
	dir := filepath.Dir(exe)
	if gb := os.Getenv("GOBIN"); gb != "" && dir == filepath.Clean(gb) {
		return true
	}
	if gp := os.Getenv("GOPATH"); gp != "" {
		for _, p := range filepath.SplitList(gp) {
			if dir == filepath.Join(p, "bin") {
				return true
			}
		}
		return false
	}
	home, err := os.UserHomeDir()
	return err == nil && dir == filepath.Join(home, "go", "bin")
}

// State is what the checker remembers between runs.
type State struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    Release   `json:"latest"`
	Skipped   string    `json:"skipped,omitempty"` // a version the user does not want to hear about
}

// LoadState reads the state; a missing or damaged file is an empty state.
func LoadState(path string) State {
	var st State
	if b, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(b, &st) != nil {
			return State{}
		}
	}
	return st
}

// SaveState writes the state atomically.
func SaveState(path string, st State) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".update-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
