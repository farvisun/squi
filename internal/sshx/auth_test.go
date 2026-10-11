package sshx

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/farvisun/squi/internal/config"
)

func dialOK(t *testing.T, opts Options) *Tunnel {
	t.Helper()
	tun, err := Dial(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tun.Close() })
	return tun
}

func checkSecrets(t *testing.T, tun *Tunnel, wantPw, wantPp string) {
	t.Helper()
	if pw, pp := tun.UsedSecrets(); pw != wantPw || pp != wantPp {
		t.Errorf("UsedSecrets = %q, %q; want %q, %q", pw, pp, wantPw, wantPp)
	}
}

// acceptKey accepts public-key auth only for key.
func acceptKey(key ssh.PublicKey) func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
	return func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		if bytes.Equal(k.Marshal(), key.Marshal()) {
			return nil, nil
		}
		return nil, errors.New("unknown key")
	}
}

func TestAuthPasswordPrompt(t *testing.T) {
	s := passwordServer(t, "pw")
	p := trusting()
	p.answers = map[SecretKind][]string{KindPassword: {"bad", "pw"}}
	opts := testOptions(t, s.port(), p)
	opts.Password = "stale"
	tun := dialOK(t, opts)
	checkSecrets(t, tun, "pw", "")
	want := "password: Password for squi@" + s.addr()
	if got := p.promptLog(); len(got) != 2 || got[0] != want {
		t.Errorf("prompts = %q, want 2x %q", got, want)
	}
}

func TestAuthKnownPasswordSilent(t *testing.T) {
	s := passwordServer(t, "pw")
	p := trusting()
	opts := testOptions(t, s.port(), p)
	opts.Password = "pw"
	tun := dialOK(t, opts)
	checkSecrets(t, tun, "pw", "")
	if got := p.promptLog(); len(got) != 0 {
		t.Errorf("unexpected prompts %q", got)
	}
}

func TestAuthNoPrompterFails(t *testing.T) {
	s := passwordServer(t, "pw")
	opts := testOptions(t, s.port(), trusting())
	if _, err := Dial(context.Background(), opts); err == nil || !strings.HasPrefix(err.Error(), "ssh: auth squi@"+s.addr()+": ") {
		t.Fatalf("err = %v", err)
	}
}

func TestAuthKeyboardInteractive(t *testing.T) {
	s := newTestServer(t, func(c *ssh.ServerConfig) {
		c.KeyboardInteractiveCallback = func(_ ssh.ConnMetadata, ch ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
			ans, err := ch("", "2FA", []string{"Password: ", "Verification code: "}, []bool{false, true})
			if err != nil {
				return nil, err
			}
			if len(ans) == 2 && ans[0] == "pw" && ans[1] == "123456" {
				return nil, nil
			}
			return nil, errors.New("denied")
		}
	})
	p := trusting()
	p.answers = map[SecretKind][]string{KindKeyboardInteractive: {"123456"}}
	opts := testOptions(t, s.port(), p)
	opts.Password = "pw"
	tun := dialOK(t, opts)
	checkSecrets(t, tun, "pw", "")
	got := p.promptLog()
	if len(got) != 1 || !strings.HasPrefix(got[0], "keyboard-interactive: 2FA Verification code:") {
		t.Errorf("prompts = %q", got)
	}
}

func TestAuthEncryptedKey(t *testing.T) {
	path, pub := writeKey(t, "secret")
	s := newTestServer(t, func(c *ssh.ServerConfig) { c.PublicKeyCallback = acceptKey(pub) })

	t.Run("prompt", func(t *testing.T) {
		p := trusting()
		p.answers = map[SecretKind][]string{KindPassphrase: {"wrong", "secret"}}
		opts := testOptions(t, s.port(), p)
		opts.SSH.Auth, opts.SSH.KeyPath = config.SSHAuthKey, path
		tun := dialOK(t, opts)
		checkSecrets(t, tun, "", "secret")
		got := p.promptLog()
		if len(got) != 2 || !strings.Contains(got[0], path) || !strings.HasPrefix(got[0], "passphrase: ") {
			t.Errorf("prompts = %q", got)
		}
	})
	t.Run("known", func(t *testing.T) {
		p := trusting()
		opts := testOptions(t, s.port(), p)
		opts.SSH.KeyPath, opts.Passphrase = path, "secret"
		tun := dialOK(t, opts)
		checkSecrets(t, tun, "", "secret")
		if got := p.promptLog(); len(got) != 0 {
			t.Errorf("unexpected prompts %q", got)
		}
	})
}

func TestAuthKeyNotPromptedWhenRejected(t *testing.T) {
	path, _ := writeKey(t, "secret")
	other := newSigner(t).PublicKey()
	s := passwordServer(t, "pw", func(c *ssh.ServerConfig) { c.PublicKeyCallback = acceptKey(other) })
	p := trusting()
	opts := testOptions(t, s.port(), p)
	opts.SSH.KeyPath, opts.Password = path, "pw"
	dialOK(t, opts)
	if got := p.promptLog(); len(got) != 0 {
		t.Errorf("prompted for a key the server rejects: %q", got)
	}
}

func TestAuthCancelledPassphraseFallsBack(t *testing.T) {
	path, pub := writeKey(t, "secret")
	s := passwordServer(t, "pw", func(c *ssh.ServerConfig) { c.PublicKeyCallback = acceptKey(pub) })
	p := trusting()
	p.answers = map[SecretKind][]string{KindPassword: {"pw"}} // passphrase prompt is cancelled
	opts := testOptions(t, s.port(), p)
	opts.SSH.KeyPath = path
	tun := dialOK(t, opts)
	checkSecrets(t, tun, "pw", "")
	if got := p.promptLog(); len(got) != 2 || !strings.HasPrefix(got[0], "passphrase:") || !strings.HasPrefix(got[1], "password:") {
		t.Errorf("prompts = %q", got)
	}
}

func TestAuthAgent(t *testing.T) {
	priv := newPrivateKey(t)
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t, func(c *ssh.ServerConfig) { c.PublicKeyCallback = acceptKey(signer.PublicKey()) })
	opts := testOptions(t, s.port(), trusting())
	opts.SSH.Auth = config.SSHAuthAgent
	startAgent(t, newPrivateKey(t), priv)
	tun := dialOK(t, opts)
	checkSecrets(t, tun, "", "")

	t.Setenv("SSH_AUTH_SOCK", "")
	if _, err := Dial(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "SSH_AUTH_SOCK") {
		t.Errorf("without agent: err = %v", err)
	}
}

func TestAuthCertificate(t *testing.T) {
	path, pub := writeKey(t, "secret")
	ca := newSigner(t)
	cert := &ssh.Certificate{
		Key: pub, CertType: ssh.UserCert, KeyId: "test",
		ValidPrincipals: []string{"squi"}, ValidBefore: ssh.CertTimeInfinity,
	}
	if err := cert.SignCert(rand.Reader, ca); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+"-cert.pub", ssh.MarshalAuthorizedKey(cert), 0o600); err != nil {
		t.Fatal(err)
	}
	checker := &ssh.CertChecker{IsUserAuthority: func(auth ssh.PublicKey) bool {
		return bytes.Equal(auth.Marshal(), ca.PublicKey().Marshal())
	}}
	s := newTestServer(t, func(c *ssh.ServerConfig) { c.PublicKeyCallback = checker.Authenticate })
	p := trusting()
	p.answers = map[SecretKind][]string{KindPassphrase: {"secret"}}
	opts := testOptions(t, s.port(), p)
	opts.SSH.Auth, opts.SSH.KeyPath = config.SSHAuthKey, path
	tun := dialOK(t, opts)
	checkSecrets(t, tun, "", "secret")
	if got := p.promptLog(); len(got) != 1 {
		t.Errorf("prompts = %q, want exactly one", got)
	}
}

func TestAuthOrder(t *testing.T) {
	keyPath, _ := writeKey(t, "")
	s := newTestServer(t, func(c *ssh.ServerConfig) {
		c.PublicKeyCallback = acceptKey(newSigner(t).PublicKey()) // rejects all our keys
		c.PasswordCallback = func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
			return nil, errors.New("no")
		}
		c.KeyboardInteractiveCallback = func(_ ssh.ConnMetadata, ch ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
			if _, err := ch("", "", nil, nil); err != nil {
				return nil, err
			}
			return nil, nil
		}
	})
	opts := testOptions(t, s.port(), trusting())
	startAgent(t, newPrivateKey(t))
	opts.SSH.KeyPath, opts.Password = keyPath, "wrong"
	dialOK(t, opts)

	var order []string
	for _, m := range s.authMethods() {
		if !slices.Contains(order, m) {
			order = append(order, m)
		}
	}
	want := []string{"none", "publickey", "password", "keyboard-interactive"}
	if !slices.Equal(order, want) {
		t.Fatalf("auth order = %q, want %q", order, want)
	}
}

func newPrivateKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

// startAgent serves an ssh-agent holding keys and points SSH_AUTH_SOCK at it.
func startAgent(t *testing.T, keys ...ed25519.PrivateKey) {
	t.Helper()
	dir, err := os.MkdirTemp("", "sshx") // short path: unix socket limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	ring := agent.NewKeyring()
	for _, k := range keys {
		if err := ring.Add(agent.AddedKey{PrivateKey: k}); err != nil {
			t.Fatal(err)
		}
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				agent.ServeAgent(ring, c)
			}()
		}
	}()
	t.Setenv("SSH_AUTH_SOCK", sock)
}
