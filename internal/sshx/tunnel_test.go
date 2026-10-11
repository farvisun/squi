package sshx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// echo writes msg through conn and checks it comes back.
func echo(conn net.Conn, msg string) error {
	if _, err := io.WriteString(conn, msg); err != nil {
		return err
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		return err
	}
	if string(buf) != msg {
		return fmt.Errorf("echo = %q, want %q", buf, msg)
	}
	return nil
}

func tryEcho(tun *Tunnel, addr string) error {
	conn, err := tun.DialContext(context.Background(), "tcp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	return echo(conn, "hello through ssh")
}

func dialEcho(t *testing.T, tun *Tunnel, addr string) {
	t.Helper()
	if err := tryEcho(tun, addr); err != nil {
		t.Fatal(err)
	}
}

// stateRecorder collects OnStateChange events.
type stateRecorder struct {
	mu     sync.Mutex
	states []State
	ch     chan State
}

func recordStates(tun *Tunnel) *stateRecorder {
	r := &stateRecorder{ch: make(chan State, 64)}
	tun.OnStateChange(func(s State, _ error) {
		r.mu.Lock()
		r.states = append(r.states, s)
		r.mu.Unlock()
		r.ch <- s
	})
	return r
}

func (r *stateRecorder) wait(t *testing.T, want State) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case s := <-r.ch:
			if s == want {
				return
			}
		case <-timeout:
			t.Fatalf("timed out waiting for state %v", want)
		}
	}
}

func TestDialForward(t *testing.T) {
	s := passwordServer(t, "pw")
	opts := testOptions(t, s.port(), trusting())
	opts.Password = "pw"
	tun := dialOK(t, opts)
	if got, want := tun.Target(), "squi@"+s.addr(); got != want {
		t.Errorf("Target = %q, want %q", got, want)
	}
	conn, err := tun.DialContext(context.Background(), "tcp", echoServer(t))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(time.Minute)); err != nil {
		t.Errorf("SetDeadline: %v", err)
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		t.Errorf("SetReadDeadline: %v", err)
	}
	if err := echo(conn, "ping"); err != nil {
		t.Fatal(err)
	}
	if tun.State() != Connected {
		t.Errorf("State = %v", tun.State())
	}
}

func TestDialRejectedTarget(t *testing.T) {
	s := passwordServer(t, "pw")
	opts := testOptions(t, s.port(), trusting())
	opts.Password = "pw"
	tun := dialOK(t, opts)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := ln.Addr().String()
	ln.Close()
	_, err := tun.DialContext(context.Background(), "tcp", closed)
	if err == nil || !strings.HasPrefix(err.Error(), "ssh: dial "+closed+" via "+s.addr()+": ") {
		t.Fatalf("err = %v", err)
	}
	if n := s.accepted.Load(); n != 1 || tun.State() != Connected {
		t.Errorf("remote refusal caused reconnect: accepted=%d state=%v", n, tun.State())
	}
}

func TestReconnectAfterDrop(t *testing.T) {
	s := passwordServer(t, "pw")
	p := trusting()
	p.answers = map[SecretKind][]string{KindPassword: {"pw"}}
	opts := testOptions(t, s.port(), p)
	tun := dialOK(t, opts)
	rec := recordStates(tun)
	addr := echoServer(t)
	dialEcho(t, tun, addr)

	s.dropAll()
	rec.wait(t, Reconnecting)

	errs := make(chan error, 8)
	for range cap(errs) {
		go func() { errs <- tryEcho(tun, addr) }()
	}
	for range cap(errs) {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
	if n := s.accepted.Load(); n != 2 {
		t.Errorf("server connections = %d, want 2 (one shared reconnect)", n)
	}
	if tun.State() != Connected {
		t.Errorf("State = %v", tun.State())
	}
	if got := p.promptLog(); len(got) != 1 {
		t.Errorf("reconnect re-prompted: %q", got)
	}
}

func TestReconnectOnDialError(t *testing.T) {
	s := passwordServer(t, "pw")
	opts := testOptions(t, s.port(), trusting())
	opts.Password = "pw"
	tun := dialOK(t, opts)
	addr := echoServer(t)
	for range 3 {
		s.dropAll()
		dialEcho(t, tun, addr) // whichever notices first, the dial succeeds
	}
}

func TestReconnectFailure(t *testing.T) {
	s := passwordServer(t, "pw")
	opts := testOptions(t, s.port(), trusting())
	opts.Password = "pw"
	tun := dialOK(t, opts)
	rec := recordStates(tun)
	s.ln.Close()
	s.dropAll()
	rec.wait(t, Reconnecting)
	if _, err := tun.DialContext(context.Background(), "tcp", "127.0.0.1:1"); err == nil || !strings.HasPrefix(err.Error(), "ssh: connect ") {
		t.Fatalf("err = %v", err)
	}
	if tun.State() != Failed {
		t.Errorf("State = %v, want Failed", tun.State())
	}
}

func TestKeepAliveDetectsHang(t *testing.T) {
	s := passwordServer(t, "pw")
	opts := testOptions(t, s.port(), trusting())
	opts.Password = "pw"
	opts.KeepAlive = 50 * time.Millisecond
	tun := dialOK(t, opts)
	rec := recordStates(tun)
	time.Sleep(200 * time.Millisecond) // answered keepalives keep it up
	if tun.State() != Connected {
		t.Fatalf("State = %v", tun.State())
	}
	s.ignoreGlobal.Store(true)
	rec.wait(t, Reconnecting)
	s.ignoreGlobal.Store(false)
	dialEcho(t, tun, echoServer(t))
}

func TestJumpHosts(t *testing.T) {
	jump := passwordServer(t, "pw1")
	final := passwordServer(t, "pw2")
	p := trusting()
	p.answers = map[SecretKind][]string{KindPassword: {"pw1"}}
	opts := testOptions(t, final.port(), p)
	opts.SSH.JumpHosts = []string{"squi@" + jump.addr()}
	opts.Password = "pw2" // only used for the final hop
	tun := dialOK(t, opts)
	dialEcho(t, tun, echoServer(t))
	if got := p.promptLog(); len(got) != 1 || got[0] != "password: Password for squi@"+jump.addr() {
		t.Errorf("prompts = %q", got)
	}
	if p.hostKeyCalls != 2 {
		t.Errorf("ConfirmHostKey calls = %d, want 2", p.hostKeyCalls)
	}
	checkSecrets(t, tun, "pw2", "")

	jump.dropAll() // kills the inner connection too
	dialEcho(t, tun, echoServer(t))
	if jump.accepted.Load() != 2 || final.accepted.Load() != 2 {
		t.Errorf("accepted jump=%d final=%d, want 2 each", jump.accepted.Load(), final.accepted.Load())
	}
}

// silentServer accepts TCP connections but never speaks SSH.
func silentServer(t *testing.T) int {
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
			t.Cleanup(func() { c.Close() })
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestDialCancelAndTimeout(t *testing.T) {
	port := silentServer(t)
	opts := testOptions(t, port, trusting())

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := Dial(ctx, opts); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("ctx: err = %v, want DeadlineExceeded", err)
	}
	opts.Timeout = 100 * time.Millisecond
	if _, err := Dial(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("timeout: err = %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %v", d)
	}
}

func TestClose(t *testing.T) {
	s := passwordServer(t, "pw")
	opts := testOptions(t, s.port(), trusting())
	opts.Password = "pw"
	tun := dialOK(t, opts)
	rec := recordStates(tun)
	conn, err := tun.DialContext(context.Background(), "tcp", echoServer(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := tun.Close(); err != nil {
		t.Fatal(err)
	}
	rec.wait(t, Closed)
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Error("forwarded conn still open after Close")
	}
	if _, err := tun.DialContext(context.Background(), "tcp", "x:1"); !errors.Is(err, ErrClosed) {
		t.Errorf("err = %v, want ErrClosed", err)
	}
	if tun.State() != Closed {
		t.Errorf("State = %v", tun.State())
	}
}
