// Package sshx connects to SSH servers, optionally through ProxyJump chains,
// and dials TCP addresses through the final hop so database drivers can reach
// hosts behind a bastion without local port forwarding.
package sshx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/farvisun/squi/internal/config"
)

// Defaults for Options.
const (
	DefaultTimeout   = 15 * time.Second
	DefaultKeepAlive = 30 * time.Second
)

// ErrClosed is returned by DialContext after Close.
var ErrClosed = errors.New("ssh: tunnel closed")

// Options configures Dial.
type Options struct {
	SSH            config.SSH
	Password       string        // known SSH password for the final hop, tried before prompting
	Passphrase     string        // known key passphrase, tried before prompting
	Prompter       Prompter      // nil disables prompts; unknown host keys are then rejected
	KnownHostsPath string        // default: ssh config UserKnownHostsFile, else ~/.ssh/known_hosts
	SSHConfigPath  string        // default ~/.ssh/config; "none" disables it
	Timeout        time.Duration // per-hop connect+handshake timeout (excluding prompts), default 15s
	KeepAlive      time.Duration // 0 => 30s, <0 disables
}

// State is the connection state of a Tunnel.
type State int

// Tunnel states.
const (
	Connected    State = iota
	Reconnecting       // connection lost; the next DialContext reconnects
	Closed
	Failed // the last reconnect failed; the next DialContext retries
)

func (s State) String() string {
	switch s {
	case Connected:
		return "connected"
	case Reconnecting:
		return "reconnecting"
	case Closed:
		return "closed"
	case Failed:
		return "failed"
	}
	return fmt.Sprintf("State(%d)", int(s))
}

// Tunnel is an authenticated SSH connection chain. It is safe for
// concurrent use.
type Tunnel struct {
	opts      Options
	hops      []hop
	auths     []*authenticator
	hostKeys  []*knownHosts
	keepAlive time.Duration

	mu       sync.Mutex
	clients  []*ssh.Client // nil while disconnected
	gen      uint64        // incremented per established connection
	pending  *reconnect
	state    State
	closed   bool
	onChange func(State, error)
	usedPw   string
	usedPp   string
}

// reconnect is a single in-flight reconnect shared by concurrent dialers.
type reconnect struct {
	done   chan struct{}
	client *ssh.Client
	gen    uint64
	err    error
}

// Dial connects to the configured host through all jump hosts.
func Dial(ctx context.Context, opts Options) (*Tunnel, error) {
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	cfg, err := loadSSHConfig(opts.SSHConfigPath)
	if err != nil {
		return nil, err
	}
	hops, err := resolveChain(cfg, opts.SSH)
	if err != nil {
		return nil, err
	}
	t := &Tunnel{opts: opts, hops: hops, keepAlive: opts.KeepAlive}
	if t.keepAlive == 0 {
		t.keepAlive = DefaultKeepAlive
	}
	for i, h := range hops {
		t.auths = append(t.auths, newAuthenticator(h, opts, i == len(hops)-1))
		t.hostKeys = append(t.hostKeys, newKnownHosts(knownHostsFiles(opts.KnownHostsPath, h)))
	}
	clients, err := t.connect(ctx)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	t.install(clients)
	t.mu.Unlock()
	return t, nil
}

func knownHostsFiles(explicit string, h hop) []string {
	switch {
	case explicit != "":
		return []string{explicit}
	case len(h.knownHosts) > 0:
		return h.knownHosts
	}
	return []string{filepath.Join(homeDir(), ".ssh", "known_hosts")}
}

// Target returns "user@host:port" of the final hop.
func (t *Tunnel) Target() string { return t.hops[len(t.hops)-1].String() }

// State returns the current connection state.
func (t *Tunnel) State() State {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state
}

// OnStateChange registers fn to be called (from any goroutine) on every
// state change, with the error that caused it if any.
func (t *Tunnel) OnStateChange(fn func(State, error)) {
	t.mu.Lock()
	t.onChange = fn
	t.mu.Unlock()
}

// UsedSecrets returns the password and passphrase that authenticated the
// final hop (empty if none), so the UI can offer to save them.
func (t *Tunnel) UsedSecrets() (password, passphrase string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.usedPw, t.usedPp
}

// Close disconnects all hops. Connections returned by DialContext are closed too.
func (t *Tunnel) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	clients := t.clients
	t.clients = nil
	notify := t.setState(Closed, nil)
	t.mu.Unlock()
	notify()
	return closeAll(clients)
}

// DialContext connects to addr from the final hop. If the SSH connection has
// died it reconnects once and retries. ctx bounds the dial only.
func (t *Tunnel) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	for retried := false; ; retried = true {
		client, gen, fresh, err := t.client(ctx)
		if err != nil {
			return nil, err
		}
		conn, err := client.DialContext(ctx, network, addr)
		if err == nil {
			return deadlineConn{conn}, nil
		}
		var rejected *ssh.OpenChannelError
		if retried || fresh || ctx.Err() != nil || errors.As(err, &rejected) {
			return nil, fmt.Errorf("ssh: dial %s via %s: %w", addr, t.hops[len(t.hops)-1].addr(), err)
		}
		t.markDead(gen, err)
	}
}

// client returns the final-hop client, reconnecting if needed. fresh reports
// whether this call waited for a reconnect.
func (t *Tunnel) client(ctx context.Context) (c *ssh.Client, gen uint64, fresh bool, err error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, 0, false, ErrClosed
	}
	if t.clients != nil {
		defer t.mu.Unlock()
		return t.clients[len(t.clients)-1], t.gen, false, nil
	}
	r := t.pending
	if r == nil {
		r = &reconnect{done: make(chan struct{})}
		t.pending = r
		go t.reconnect(r)
	}
	t.mu.Unlock()
	select {
	case <-r.done:
		return r.client, r.gen, true, r.err
	case <-ctx.Done():
		return nil, 0, false, ctx.Err()
	}
}

// reconnect re-establishes all hops. It runs detached from any caller's
// context so an impatient caller cannot fail it for others.
func (t *Tunnel) reconnect(r *reconnect) {
	clients, err := t.connect(context.Background())
	t.mu.Lock()
	t.pending = nil
	var notify func()
	switch {
	case t.closed:
		err = ErrClosed
	case err != nil:
		notify = t.setState(Failed, err)
	default:
		notify = t.install(clients)
		r.client, r.gen = t.clients[len(t.clients)-1], t.gen
		clients = nil
	}
	r.err = err
	t.mu.Unlock()
	closeAll(clients)
	close(r.done)
	if notify != nil {
		notify()
	}
}

// install makes clients current and starts monitoring them. t.mu must be held.
func (t *Tunnel) install(clients []*ssh.Client) (notify func()) {
	t.clients = clients
	t.gen++
	t.usedPw, t.usedPp = t.auths[len(t.auths)-1].usedPassword, t.auths[len(t.auths)-1].usedPassphrase
	go t.monitor(t.gen, clients[len(clients)-1])
	return t.setState(Connected, nil)
}

// monitor marks the connection dead when it closes or keepalives fail.
func (t *Tunnel) monitor(gen uint64, c *ssh.Client) {
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	var tick <-chan time.Time
	if t.keepAlive > 0 {
		ticker := time.NewTicker(t.keepAlive)
		defer ticker.Stop()
		tick = ticker.C
	}
	for {
		select {
		case err := <-done:
			if err == nil {
				err = errors.New("ssh: connection closed")
			}
			t.markDead(gen, err)
			return
		case <-tick:
			if err := keepAlive(c, 2*t.keepAlive); err != nil {
				t.markDead(gen, err)
				return
			}
		}
	}
}

func keepAlive(c *ssh.Client, timeout time.Duration) error {
	res := make(chan error, 1)
	go func() {
		_, _, err := c.SendRequest("keepalive@openssh.com", true, nil)
		res <- err
	}()
	select {
	case err := <-res:
		if err != nil {
			return fmt.Errorf("ssh: keepalive: %w", err)
		}
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("ssh: keepalive: no reply within %s", timeout)
	}
}

// markDead drops connection generation gen (if still current).
func (t *Tunnel) markDead(gen uint64, cause error) {
	t.mu.Lock()
	if t.closed || t.clients == nil || t.gen != gen {
		t.mu.Unlock()
		return
	}
	clients := t.clients
	t.clients = nil
	notify := t.setState(Reconnecting, cause)
	t.mu.Unlock()
	closeAll(clients)
	notify()
}

// setState records s and returns a func that notifies the listener; call it
// after releasing t.mu.
func (t *Tunnel) setState(s State, err error) func() {
	t.state = s
	fn := t.onChange
	return func() {
		if fn != nil {
			fn(s, err)
		}
	}
}

// connect dials every hop in order.
func (t *Tunnel) connect(ctx context.Context) ([]*ssh.Client, error) {
	var clients []*ssh.Client
	for i := range t.hops {
		var via *ssh.Client
		if i > 0 {
			via = clients[i-1]
		}
		c, err := t.connectHop(ctx, i, via)
		if err != nil {
			closeAll(clients)
			return nil, err
		}
		clients = append(clients, c)
	}
	return clients, nil
}

func (t *Tunnel) connectHop(ctx context.Context, i int, via *ssh.Client) (*ssh.Client, error) {
	h := t.hops[i]
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	wd := newWatchdog(t.opts.Timeout, func() {
		cancel(fmt.Errorf("timed out after %s", t.opts.Timeout))
	})
	defer wd.stop()

	var conn net.Conn
	var err error
	if via == nil {
		var d net.Dialer
		conn, err = d.DialContext(ctx, "tcp", h.addr())
	} else {
		conn, err = via.DialContext(ctx, "tcp", h.addr())
	}
	if err != nil {
		err = causeOf(ctx, err)
		if via != nil {
			return nil, fmt.Errorf("ssh: connect %s via %s: %w", h.addr(), t.hops[i-1].addr(), err)
		}
		return nil, fmt.Errorf("ssh: connect %s: %w", h.addr(), err)
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })

	a := t.auths[i]
	var verified bool
	cfg := &ssh.ClientConfig{
		User:              h.user,
		HostKeyCallback:   t.hostKeys[i].callback(t.opts.Prompter, wd, &verified),
		HostKeyAlgorithms: t.hostKeys[i].algorithms(h.addr()),
		AuthCallback:      a.callback,
	}
	a.begin(wd)
	sc, chans, reqs, err := ssh.NewClientConn(conn, h.addr(), cfg)
	a.end(err == nil)
	if !stop() && err == nil {
		sc.Close()
		err = errors.New("canceled")
	}
	if err != nil {
		conn.Close()
		return nil, handshakeError(h, verified, causeOf(ctx, err))
	}
	return ssh.NewClient(sc, chans, reqs), nil
}

// handshakeError names the failing stage: host key verification, auth or
// the SSH handshake itself.
func handshakeError(h hop, verified bool, err error) error {
	const prefix = "ssh: handshake failed: "
	if u := errors.Unwrap(err); u != nil && strings.HasPrefix(err.Error(), prefix) {
		err = u
	}
	switch {
	case errors.Is(err, ErrHostKeyMismatch), errors.Is(err, ErrHostKeyUnknown):
		return err
	case verified:
		return fmt.Errorf("ssh: auth %s: %w", h, err)
	}
	return fmt.Errorf("ssh: handshake %s: %w", h.addr(), err)
}

// causeOf prefers ctx's cancellation cause (e.g. a timeout) over err.
func causeOf(ctx context.Context, err error) error {
	if c := context.Cause(ctx); c != nil {
		return c
	}
	return err
}

func closeAll(clients []*ssh.Client) error {
	var errs []error
	for i := len(clients) - 1; i >= 0; i-- {
		if err := clients[i].Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// watchdog cancels a connection attempt after d, excluding paused time
// (while the user answers a prompt). A nil watchdog is a no-op.
type watchdog struct {
	d     time.Duration
	timer *time.Timer
}

func newWatchdog(d time.Duration, fire func()) *watchdog {
	return &watchdog{d: d, timer: time.AfterFunc(d, fire)}
}

func (w *watchdog) pause() {
	if w != nil {
		w.timer.Stop()
	}
}

func (w *watchdog) resume() {
	if w != nil {
		w.timer.Reset(w.d)
	}
}

func (w *watchdog) stop() { w.pause() }

// deadlineConn wraps an SSH channel conn, whose deadline methods return
// errors, so drivers that set deadlines keep working (deadlines are ignored).
type deadlineConn struct{ net.Conn }

func (c deadlineConn) SetDeadline(t time.Time) error      { _ = c.Conn.SetDeadline(t); return nil }
func (c deadlineConn) SetReadDeadline(t time.Time) error  { _ = c.Conn.SetReadDeadline(t); return nil }
func (c deadlineConn) SetWriteDeadline(t time.Time) error { _ = c.Conn.SetWriteDeadline(t); return nil }
