package sshx

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestHostKeyUnknownAcceptAppends(t *testing.T) {
	s := passwordServer(t, "pw")
	p := trusting()
	opts := testOptions(t, s.port(), p)
	opts.Password = "pw"
	opts.KnownHostsPath = filepath.Join(t.TempDir(), "new", "dir", "known_hosts")

	tun, err := Dial(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	tun.Close()
	if p.hostKeyCalls != 1 {
		t.Fatalf("ConfirmHostKey calls = %d, want 1", p.hostKeyCalls)
	}
	b, err := os.ReadFile(opts.KnownHostsPath)
	if err != nil {
		t.Fatal(err)
	}
	want := knownhosts.Line([]string{knownhosts.Normalize(s.addr())}, s.hostKey.PublicKey())
	if strings.TrimSpace(string(b)) != want {
		t.Fatalf("known_hosts = %q, want %q", b, want)
	}
	for path, mode := range map[string]os.FileMode{opts.KnownHostsPath: 0o600, filepath.Dir(opts.KnownHostsPath): 0o700} {
		if st, _ := os.Stat(path); st.Mode().Perm() != mode {
			t.Errorf("%s mode = %v, want %v", path, st.Mode().Perm(), mode)
		}
	}

	// The key is now known: no prompter needed.
	opts.Prompter = nil
	tun, err = Dial(context.Background(), opts)
	if err != nil {
		t.Fatalf("second dial: %v", err)
	}
	tun.Close()
}

func TestHostKeyRejected(t *testing.T) {
	s := passwordServer(t, "pw")
	for name, p := range map[string]Prompter{"declined": &fakePrompter{}, "no prompter": nil} {
		opts := testOptions(t, s.port(), p)
		opts.Password = "pw"
		_, err := Dial(context.Background(), opts)
		if !errors.Is(err, ErrHostKeyUnknown) {
			t.Errorf("%s: err = %v, want ErrHostKeyUnknown", name, err)
		}
		if b, _ := os.ReadFile(opts.KnownHostsPath); len(b) != 0 {
			t.Errorf("%s: known_hosts written: %q", name, b)
		}
	}
}

func TestHostKeyMismatch(t *testing.T) {
	s := passwordServer(t, "pw")
	p := trusting()
	opts := testOptions(t, s.port(), p)
	opts.Password = "pw"
	other := newSigner(t).PublicKey()
	line := knownhosts.Line([]string{knownhosts.Normalize(s.addr())}, other)
	if err := os.WriteFile(opts.KnownHostsPath, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Dial(context.Background(), opts)
	if !errors.Is(err, ErrHostKeyMismatch) {
		t.Fatalf("err = %v, want ErrHostKeyMismatch", err)
	}
	if p.hostKeyCalls != 0 {
		t.Error("prompted on mismatch")
	}
	for _, fp := range []string{ssh.FingerprintSHA256(other), ssh.FingerprintSHA256(s.hostKey.PublicKey())} {
		if !strings.Contains(err.Error(), fp) {
			t.Errorf("error lacks fingerprint %s:\n%v", fp, err)
		}
	}
}

func TestHostKeySkipsMalformedLines(t *testing.T) {
	s := passwordServer(t, "pw")
	opts := testOptions(t, s.port(), nil)
	opts.Password = "pw"
	other := newSigner(t).PublicKey()
	good := knownhosts.Line([]string{knownhosts.Normalize(s.addr())}, s.hostKey.PublicKey())
	bad := strings.Fields(knownhosts.Line([]string{"x"}, other))[1:] // key without host
	content := "# comment\n" + strings.Join(bad, " ") + "\nonlyhost\n" + good + "\n"
	if err := os.WriteFile(opts.KnownHostsPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	tun, err := Dial(context.Background(), opts)
	if err != nil {
		t.Fatalf("malformed lines should be skipped: %v", err)
	}
	tun.Close()

	// A mismatch still names the real file and line.
	line := knownhosts.Line([]string{knownhosts.Normalize(s.addr())}, other)
	if err := os.WriteFile(opts.KnownHostsPath, []byte(strings.Join(bad, " ")+"\n"+line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Dial(context.Background(), opts)
	if !errors.Is(err, ErrHostKeyMismatch) || !strings.Contains(err.Error(), opts.KnownHostsPath+":2") {
		t.Fatalf("err = %v, want mismatch at %s:2", err, opts.KnownHostsPath)
	}
}

func TestHostKeyAppendAddsMissingNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	first := knownhosts.Line([]string{"a"}, newSigner(t).PublicKey())
	if err := os.WriteFile(path, []byte(first), 0o600); err != nil {
		t.Fatal(err)
	}
	k := newKnownHosts([]string{path})
	key := newSigner(t).PublicKey()
	if err := k.add("b:22", key); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if lines := strings.Split(strings.TrimSpace(string(b)), "\n"); len(lines) != 2 || !strings.HasPrefix(lines[1], "b ") {
		t.Fatalf("known_hosts = %q", b)
	}
	if err := k.check("b:22", &net.TCPAddr{}, key); err != nil {
		t.Fatalf("check after add: %v", err)
	}
}

func TestHostKeyAlgorithms(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaPub, err := ssh.NewPublicKey(&rsaKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "known_hosts")
	content := knownhosts.Line([]string{"rsa.example"}, rsaPub) + "\n" +
		knownhosts.Line([]string{"[ed.example]:2222"}, newSigner(t).PublicKey()) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	k := newKnownHosts([]string{path, filepath.Join(t.TempDir(), "absent")})
	if got := k.algorithms("rsa.example:22"); len(got) < 2 || got[0] != ssh.KeyAlgoRSASHA512 || got[1] != ssh.KeyAlgoRSASHA256 {
		t.Errorf("rsa host: %q", got)
	}
	got := k.algorithms("ed.example:2222")
	if len(got) == 0 || got[0] != ssh.KeyAlgoED25519 {
		t.Errorf("ed25519 host: %q", got)
	}
	for i, a := range got {
		for _, b := range got[i+1:] {
			if a == b {
				t.Errorf("duplicate algorithm %s", a)
			}
		}
	}
	if got := k.algorithms("unknown.example:22"); got != nil {
		t.Errorf("unknown host: %q, want nil", got)
	}
}
