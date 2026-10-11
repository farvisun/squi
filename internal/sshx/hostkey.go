package sshx

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

var (
	// ErrHostKeyMismatch means the server's key differs from known_hosts.
	ErrHostKeyMismatch = errors.New("ssh: host key mismatch")
	// ErrHostKeyUnknown means the host is not in known_hosts and was not trusted.
	ErrHostKeyUnknown = errors.New("ssh: unknown host key")
)

// knownHostsMu serializes known_hosts reads and appends within the process.
var knownHostsMu sync.Mutex

// knownHosts verifies host keys against a set of known_hosts files.
type knownHosts struct {
	files []string // consulted in order; new keys are appended to files[0]
}

func newKnownHosts(files []string) *knownHosts { return &knownHosts{files: files} }

// check verifies key for hostname ("host:port") against all existing files.
func (k *knownHosts) check(hostname string, remote net.Addr, key ssh.PublicKey) error {
	knownHostsMu.Lock()
	defer knownHostsMu.Unlock()
	if err := ensureFile(k.files[0]); err != nil {
		return err
	}
	var existing []string
	for _, f := range k.files {
		if _, err := os.Stat(f); err == nil {
			existing = append(existing, f)
		}
	}
	cb, err := loadKnownHosts(existing)
	if err != nil {
		return fmt.Errorf("ssh: read known_hosts: %w", err)
	}
	return cb(hostname, remote, key)
}

// loadKnownHosts is knownhosts.New, except that lines it cannot parse (a key
// pasted without a host, a truncated entry, …) are skipped the way OpenSSH
// skips them, instead of failing every connection.
func loadKnownHosts(files []string) (ssh.HostKeyCallback, error) {
	cb, err := knownhosts.New(files...)
	if err == nil {
		return cb, nil
	}
	// Retry on private copies with each offending line blanked; blanking
	// rather than deleting keeps line numbers for mismatch reports.
	dir, derr := os.MkdirTemp("", "squi-known-hosts-")
	if derr != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	copies := make([]string, len(files))
	lines := make([][]string, len(files))
	total := 0
	for i, f := range files {
		b, rerr := os.ReadFile(f)
		if rerr != nil {
			return nil, err
		}
		lines[i] = strings.Split(string(b), "\n")
		total += len(lines[i])
		copies[i] = filepath.Join(dir, strconv.Itoa(i))
		if werr := os.WriteFile(copies[i], b, 0o600); werr != nil {
			return nil, err
		}
	}
	for range total {
		cb, err = knownhosts.New(copies...)
		if err == nil {
			return renameKnownHosts(cb, copies, files), nil
		}
		i, n := badLine(err.Error(), copies)
		if i < 0 || n < 1 || n > len(lines[i]) || lines[i][n-1] == "" {
			return nil, err
		}
		lines[i][n-1] = ""
		if werr := os.WriteFile(copies[i], []byte(strings.Join(lines[i], "\n")), 0o600); werr != nil {
			return nil, err
		}
	}
	return nil, err
}

// badLine finds the file and 1-based line in a knownhosts parse error
// ("knownhosts: FILE:LINE: …").
func badLine(msg string, files []string) (int, int) {
	for i, f := range files {
		rest, ok := strings.CutPrefix(msg, "knownhosts: "+f+":")
		if !ok {
			continue
		}
		num, _, _ := strings.Cut(rest, ":")
		n, err := strconv.Atoi(num)
		if err != nil {
			return -1, 0
		}
		return i, n
	}
	return -1, 0
}

// renameKnownHosts makes KeyErrors from cb name the original files.
func renameKnownHosts(cb ssh.HostKeyCallback, copies, files []string) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := cb(hostname, remote, key)
		var ke *knownhosts.KeyError
		if errors.As(err, &ke) {
			for j := range ke.Want {
				if i := slices.Index(copies, ke.Want[j].Filename); i >= 0 {
					ke.Want[j].Filename = files[i]
				}
			}
		}
		return err
	}
}

// add appends a known_hosts line for hostname to the primary file.
func (k *knownHosts) add(hostname string, key ssh.PublicKey) error {
	knownHostsMu.Lock()
	defer knownHostsMu.Unlock()
	f, err := os.OpenFile(k.files[0], os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("ssh: update known_hosts: %w", err)
	}
	defer f.Close()
	line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key) + "\n"
	if missingTrailingNewline(f) {
		line = "\n" + line
	}
	if _, err := f.WriteString(line); err != nil {
		return fmt.Errorf("ssh: update known_hosts: %w", err)
	}
	return f.Close()
}

// callback returns a HostKeyCallback that asks p about unknown hosts and sets
// *verified once the key is trusted.
func (k *knownHosts) callback(p Prompter, wd *watchdog, verified *bool) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := k.check(hostname, remote, key)
		var ke *knownhosts.KeyError
		switch {
		case err == nil:
		case errors.As(err, &ke) && len(ke.Want) > 0:
			return mismatchError(hostname, key, ke.Want)
		case errors.As(err, &ke):
			fp := ssh.FingerprintSHA256(key)
			if p == nil {
				return fmt.Errorf("%w: %s presented %s key %s, which is not in %s",
					ErrHostKeyUnknown, hostname, key.Type(), fp, k.files[0])
			}
			wd.pause()
			ok, err := p.ConfirmHostKey(hostname, key, fp)
			wd.resume()
			if err != nil {
				return fmt.Errorf("%w: %v", ErrHostKeyUnknown, err)
			}
			if !ok {
				return fmt.Errorf("%w: %s (%s) was not trusted", ErrHostKeyUnknown, hostname, fp)
			}
			if err := k.add(hostname, key); err != nil {
				return err
			}
		default:
			return err
		}
		*verified = true
		return nil
	}
}

// algorithms returns host key algorithms with the ones known for hostname
// first, so the server offers a key we can verify. nil means "no preference".
func (k *knownHosts) algorithms(hostname string) []string {
	var ke *knownhosts.KeyError
	if !errors.As(k.check(hostname, &net.TCPAddr{}, probeKey()), &ke) || len(ke.Want) == 0 {
		return nil
	}
	var known []string
	for _, w := range ke.Want {
		if t := w.Key.Type(); t == ssh.KeyAlgoRSA {
			known = append(known, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256)
		} else {
			known = append(known, t)
		}
	}
	var algos []string
	for _, a := range slices.Concat(known, ssh.SupportedAlgorithms().HostKeys) {
		if !slices.Contains(algos, a) {
			algos = append(algos, a)
		}
	}
	return algos
}

// probeKey is a throwaway key that never matches, used to list known keys.
var probeKey = sync.OnceValue(func() ssh.PublicKey {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		panic(err)
	}
	return k
})

func mismatchError(hostname string, got ssh.PublicKey, want []knownhosts.KnownKey) error {
	var b strings.Builder
	b.WriteString("WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED. Someone could be eavesdropping on you\n")
	b.WriteString("right now (man-in-the-middle attack), or the host key has just been changed.\n")
	fmt.Fprintf(&b, "  server presented: %s %s\n", got.Type(), ssh.FingerprintSHA256(got))
	for _, w := range want {
		fmt.Fprintf(&b, "  known_hosts has:  %s %s (%s:%d)\n", w.Key.Type(), ssh.FingerprintSHA256(w.Key), w.Filename, w.Line)
	}
	b.WriteString("If the change is expected, remove the old entry from known_hosts and reconnect.")
	return fmt.Errorf("%w for %s!\n%s", ErrHostKeyMismatch, hostname, b.String())
}

// ensureFile creates path (mode 0600) and its directory (0700) if missing.
func ensureFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("ssh: create known_hosts dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDONLY|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("ssh: create known_hosts: %w", err)
	}
	return f.Close()
}

func missingTrailingNewline(f *os.File) bool {
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return false
	}
	b := make([]byte, 1)
	if _, err := f.ReadAt(b, st.Size()-1); err != nil && err != io.EOF {
		return false
	}
	return b[0] != '\n'
}
