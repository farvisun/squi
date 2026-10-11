package sshx

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/farvisun/squi/internal/config"
)

// testServer is an in-process SSH server supporting direct-tcpip forwarding.
type testServer struct {
	t       *testing.T
	ln      net.Listener
	cfg     *ssh.ServerConfig
	hostKey ssh.Signer

	accepted     atomic.Int32
	ignoreGlobal atomic.Bool // never answer global requests (simulates a hung server)

	mu      sync.Mutex
	conns   []net.Conn
	methods []string // auth methods attempted, in order
}

func newTestServer(t *testing.T, setup func(*ssh.ServerConfig)) *testServer {
	t.Helper()
	s := &testServer{t: t, hostKey: newSigner(t)}
	s.cfg = &ssh.ServerConfig{
		AuthLogCallback: func(_ ssh.ConnMetadata, method string, _ error) {
			s.mu.Lock()
			s.methods = append(s.methods, method)
			s.mu.Unlock()
		},
	}
	s.cfg.AddHostKey(s.hostKey)
	setup(s.cfg)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.ln = ln
	go s.serve()
	t.Cleanup(func() {
		ln.Close()
		s.dropAll()
	})
	return s
}

func (s *testServer) port() int { return s.ln.Addr().(*net.TCPAddr).Port }

func (s *testServer) addr() string { return s.ln.Addr().String() }

func (s *testServer) authMethods() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.methods...)
}

// dropAll abruptly closes every client connection.
func (s *testServer) dropAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		c.Close()
	}
	s.conns = nil
}

func (s *testServer) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.accepted.Add(1)
		s.mu.Lock()
		s.conns = append(s.conns, c)
		s.mu.Unlock()
		go s.handle(c)
	}
}

func (s *testServer) handle(c net.Conn) {
	sc, chans, reqs, err := ssh.NewServerConn(c, s.cfg)
	if err != nil {
		c.Close()
		return
	}
	defer sc.Close()
	go func() {
		for r := range reqs {
			if r.WantReply && !s.ignoreGlobal.Load() {
				r.Reply(r.Type == "keepalive@openssh.com", nil)
			}
		}
	}()
	for nc := range chans {
		if nc.ChannelType() != "direct-tcpip" {
			nc.Reject(ssh.UnknownChannelType, "only direct-tcpip")
			continue
		}
		var p struct {
			Host     string
			Port     uint32
			OrigHost string
			OrigPort uint32
		}
		if err := ssh.Unmarshal(nc.ExtraData(), &p); err != nil {
			nc.Reject(ssh.ConnectionFailed, err.Error())
			continue
		}
		target, err := net.Dial("tcp", net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))))
		if err != nil {
			nc.Reject(ssh.ConnectionFailed, err.Error())
			continue
		}
		ch, creqs, err := nc.Accept()
		if err != nil {
			target.Close()
			continue
		}
		go ssh.DiscardRequests(creqs)
		go func() {
			io.Copy(ch, target)
			ch.Close()
		}()
		go func() {
			io.Copy(target, ch)
			target.Close()
		}()
	}
}

// passwordServer accepts password, plus whatever extra configures.
func passwordServer(t *testing.T, password string, extra ...func(*ssh.ServerConfig)) *testServer {
	return newTestServer(t, func(c *ssh.ServerConfig) {
		for _, f := range extra {
			f(c)
		}
		c.PasswordCallback = func(_ ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if string(pw) == password {
				return nil, nil
			}
			return nil, errors.New("wrong password")
		}
	})
}

// echoServer returns the address of a local TCP echo server.
func echoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				io.Copy(c, c)
			}()
		}
	}()
	return ln.Addr().String()
}

func newSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// writeKey writes a new ed25519 key (encrypted if passphrase != "") and
// returns its path and public key.
func writeKey(t *testing.T, passphrase string) (string, ssh.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var block *pem.Block
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(priv, "test")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "test", []byte(passphrase))
	}
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "id_test")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return path, sshPub
}

// fakePrompter trusts host keys if trust is set and answers secrets from
// answers in order; a missing answer behaves like a cancelled prompt.
type fakePrompter struct {
	trust bool

	mu           sync.Mutex
	answers      map[SecretKind][]string
	hostKeyCalls int
	prompts      []string // "kind: prompt"
}

func (p *fakePrompter) ConfirmHostKey(string, ssh.PublicKey, string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hostKeyCalls++
	return p.trust, nil
}

func (p *fakePrompter) Secret(kind SecretKind, prompt string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prompts = append(p.prompts, fmt.Sprintf("%s: %s", kind, prompt))
	if len(p.answers[kind]) == 0 {
		return "", errors.New("cancelled")
	}
	a := p.answers[kind][0]
	p.answers[kind] = p.answers[kind][1:]
	return a, nil
}

func (p *fakePrompter) promptLog() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.prompts...)
}

// isolate keeps tests away from the user's agent and ~/.ssh.
func isolate(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("HOME", t.TempDir())
}

func testOptions(t *testing.T, port int, p Prompter) Options {
	t.Helper()
	isolate(t)
	return Options{
		SSH:            config.SSH{Enabled: true, Host: "127.0.0.1", Port: port, User: "squi"},
		KnownHostsPath: filepath.Join(t.TempDir(), "known_hosts"),
		SSHConfigPath:  "none",
		Timeout:        5 * time.Second,
		KeepAlive:      -1,
		Prompter:       p,
	}
}

func trusting() *fakePrompter { return &fakePrompter{trust: true} }
