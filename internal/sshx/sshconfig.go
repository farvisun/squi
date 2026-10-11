package sshx

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kevinburke/ssh_config"

	"github.com/farvisun/squi/internal/config"
)

// maxJumpDepth bounds recursive ProxyJump expansion.
const maxJumpDepth = 8

// hop is one fully resolved SSH server in a connection chain.
type hop struct {
	host           string // network host name or IP
	port           int
	user           string
	identityFiles  []string // from ssh config, ~ expanded
	identitiesOnly bool
	knownHosts     []string // UserKnownHostsFile entries, ~ expanded
	proxyJump      string
}

func (h hop) addr() string { return net.JoinHostPort(h.host, strconv.Itoa(h.port)) }

func (h hop) String() string { return h.user + "@" + h.addr() }

// sshConfig wraps an optional parsed ssh_config file.
type sshConfig struct {
	cfg *ssh_config.Config // nil when disabled or missing
}

// loadSSHConfig reads path ("" = ~/.ssh/config, "none" = disabled).
// A missing file is not an error.
func loadSSHConfig(path string) (*sshConfig, error) {
	if path == "none" {
		return &sshConfig{}, nil
	}
	if path == "" {
		path = filepath.Join(homeDir(), ".ssh", "config")
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return &sshConfig{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("ssh: read config: %w", err)
	}
	defer f.Close()
	cfg, err := ssh_config.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("ssh: parse %s: %w", path, err)
	}
	return &sshConfig{cfg: cfg}, nil
}

// get returns the first value of key for alias. ssh_config panics on Match
// blocks, which are treated as "not set".
func (c *sshConfig) get(alias, key string) (v string) {
	if c.cfg == nil {
		return ""
	}
	defer func() {
		if recover() != nil {
			v = ""
		}
	}()
	v, _ = c.cfg.Get(alias, key)
	return v
}

func (c *sshConfig) getAll(alias, key string) (vs []string) {
	if c.cfg == nil {
		return nil
	}
	defer func() {
		if recover() != nil {
			vs = nil
		}
	}()
	vs, _ = c.cfg.GetAll(alias, key)
	return vs
}

// resolve turns a "[user@]host[:port]" spec or alias into a hop, applying
// matching ssh config entries. Values in the spec win over the config.
func (c *sshConfig) resolve(spec string) (hop, error) {
	u, name, port, err := parseHostSpec(spec)
	if err != nil {
		return hop{}, err
	}
	return c.lookup(name, u, port)
}

// lookup builds a hop for name; non-empty user/port override the config.
func (c *sshConfig) lookup(name, user string, port int) (hop, error) {
	h := hop{host: name, port: port, user: user}
	if hn := c.get(name, "HostName"); hn != "" {
		h.host = strings.ReplaceAll(hn, "%h", name)
	}
	if h.user == "" {
		h.user = c.get(name, "User")
	}
	if h.user == "" {
		h.user = localUser()
	}
	if h.port == 0 {
		if p := c.get(name, "Port"); p != "" {
			n, err := strconv.Atoi(p)
			if err != nil || n <= 0 || n > 65535 {
				return hop{}, fmt.Errorf("ssh: config Host %s: bad Port %q", name, p)
			}
			h.port = n
		}
	}
	if h.port == 0 {
		h.port = 22
	}
	for _, f := range c.getAll(name, "IdentityFile") {
		h.identityFiles = append(h.identityFiles, expandPath(f, h))
	}
	h.identitiesOnly = strings.EqualFold(c.get(name, "IdentitiesOnly"), "yes")
	for _, f := range strings.Fields(c.get(name, "UserKnownHostsFile")) {
		h.knownHosts = append(h.knownHosts, expandPath(f, h))
	}
	h.proxyJump = c.get(name, "ProxyJump")
	return h, nil
}

// resolveChain returns all hops for s, jump hosts first and the target last.
func resolveChain(c *sshConfig, s config.SSH) ([]hop, error) {
	name := s.Alias
	if name == "" {
		name = s.Host
	}
	if name == "" {
		return nil, errors.New("ssh: no host configured")
	}
	final, err := c.lookup(name, s.User, s.Port)
	if err != nil {
		return nil, err
	}
	if s.Alias != "" && s.Host != "" {
		final.host = s.Host
	}
	jumps := splitProxyJump(strings.Join(s.JumpHosts, ","))
	if len(s.JumpHosts) == 0 {
		jumps = splitProxyJump(final.proxyJump)
	}
	chain, err := c.jumpChain(jumps, 0)
	if err != nil {
		return nil, err
	}
	return append(chain, final), nil
}

// jumpChain resolves jump specs in order. Like OpenSSH, the first jump host's
// own ProxyJump is honored (recursively), later ones are reached via the chain.
func (c *sshConfig) jumpChain(specs []string, depth int) ([]hop, error) {
	if depth > maxJumpDepth {
		return nil, errors.New("ssh: ProxyJump nesting too deep")
	}
	var out []hop
	for i, spec := range specs {
		h, err := c.resolve(spec)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			pre, err := c.jumpChain(splitProxyJump(h.proxyJump), depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, pre...)
		}
		out = append(out, h)
	}
	return out, nil
}

// splitProxyJump splits a comma-separated ProxyJump value; "none" means none.
func splitProxyJump(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		if strings.EqualFold(p, "none") {
			return nil
		}
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseHostSpec parses "[ssh://][user@]host[:port]"; IPv6 hosts use brackets.
func parseHostSpec(spec string) (user, host string, port int, err error) {
	s := strings.TrimPrefix(strings.TrimSpace(spec), "ssh://")
	if i := strings.LastIndex(s, "@"); i >= 0 {
		user, s = s[:i], s[i+1:]
		if user == "" {
			return "", "", 0, fmt.Errorf("ssh: bad host %q: empty user", spec)
		}
	}
	host = s
	if strings.HasPrefix(s, "[") || strings.Count(s, ":") == 1 {
		h, p, splitErr := net.SplitHostPort(s)
		if splitErr != nil {
			if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
				return "", "", 0, fmt.Errorf("ssh: bad host %q: %v", spec, splitErr)
			}
			h = s[1 : len(s)-1] // "[::1]" without port
		} else {
			port, err = strconv.Atoi(p)
			if err != nil || port <= 0 || port > 65535 {
				return "", "", 0, fmt.Errorf("ssh: bad host %q: invalid port", spec)
			}
		}
		host = h
	}
	if host == "" {
		return "", "", 0, fmt.Errorf("ssh: bad host %q: empty host", spec)
	}
	return user, host, port, nil
}

// expandPath expands ~ and the ssh_config tokens %d %u %h %r %p %%.
func expandPath(p string, h hop) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		p = "%d" + p[1:]
	}
	if !strings.Contains(p, "%") {
		return p
	}
	r := strings.NewReplacer(
		"%%", "%",
		"%d", homeDir(),
		"%u", localUser(),
		"%h", h.host,
		"%r", h.user,
		"%p", strconv.Itoa(h.port),
	)
	return r.Replace(p)
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "."
}

func localUser() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return ""
}
