// Package config loads and saves squi's connection profiles and settings.
// Files live in $XDG_CONFIG_HOME/squi (default ~/.config/squi); state such as
// query history lives in $XDG_STATE_HOME/squi (default ~/.local/state/squi).
// Secrets are never written here; see package secrets.
package config

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Env tags a connection with the environment it points at.
type Env string

const (
	EnvNone    Env = ""
	EnvLocal   Env = "local"
	EnvDev     Env = "dev"
	EnvStaging Env = "staging"
	EnvProd    Env = "prod"
)

// Envs lists selectable environments in display order.
var Envs = []Env{EnvNone, EnvLocal, EnvDev, EnvStaging, EnvProd}

// SSH auth methods.
const (
	SSHAuthAuto     = "auto" // agent -> key -> password -> keyboard-interactive
	SSHAuthAgent    = "agent"
	SSHAuthKey      = "key"
	SSHAuthPassword = "password"
)

// SSH describes how to reach the database through an SSH bastion.
type SSH struct {
	Enabled bool   `toml:"enabled"`
	Alias   string `toml:"alias,omitempty"` // Host entry in ~/.ssh/config
	Host    string `toml:"host,omitempty"`
	Port    int    `toml:"port,omitempty"`
	User    string `toml:"user,omitempty"`
	Auth    string `toml:"auth,omitempty"`
	KeyPath string `toml:"key_path,omitempty"`
	// JumpHosts are dialed in order before Host, like ProxyJump. Each entry
	// is "[user@]host[:port]" or an ~/.ssh/config alias.
	JumpHosts []string `toml:"jump_hosts,omitempty"`
}

// Connection is a saved connection profile.
type Connection struct {
	ID       string `toml:"id"`
	Name     string `toml:"name"`
	Driver   string `toml:"driver"` // postgres | mysql
	Host     string `toml:"host"`
	Port     int    `toml:"port"`
	User     string `toml:"user"`
	Database string `toml:"database,omitempty"`

	SSLMode     string `toml:"ssl_mode,omitempty"`
	SSLRootCert string `toml:"ssl_root_cert,omitempty"`
	SSLCert     string `toml:"ssl_cert,omitempty"`
	SSLKey      string `toml:"ssl_key,omitempty"`

	Env       Env  `toml:"env,omitempty"`
	ReadOnly  bool `toml:"read_only,omitempty"`
	FetchSize int  `toml:"fetch_size,omitempty"`
	// SavePassword stores the DB password in the OS keychain.
	SavePassword bool `toml:"save_password,omitempty"`

	SSH SSH `toml:"ssh"`
}

// Title is the label shown in tabs and lists.
func (c Connection) Title() string {
	if c.Name != "" {
		return c.Name
	}
	return fmt.Sprintf("%s@%s", c.User, c.Host)
}

// Settings are user preferences.
type Settings struct {
	FetchSize       int  `toml:"fetch_size"`
	VimMode         bool `toml:"vim_mode"`
	ConfirmProdAll  bool `toml:"confirm_all_writes_on_prod"`
	HistoryLimit    int  `toml:"history_limit"`
	MaxColumnWidth  int  `toml:"max_column_width"`
	ExplorerWidth   int  `toml:"explorer_width"`
	DefaultTxManual bool `toml:"manual_transactions"`
	// RestoreSession reopens the previous run's tabs and queries.
	RestoreSession bool `toml:"restore_session"`
	// Keys overrides key bindings: action name -> list of keys.
	Keys map[string][]string `toml:"keys,omitempty"`
}

// DefaultSettings returns the built-in defaults.
func DefaultSettings() Settings {
	return Settings{
		FetchSize:      1000,
		VimMode:        true,
		ConfirmProdAll: true,
		HistoryLimit:   5000,
		MaxColumnWidth: 40,
		ExplorerWidth:  32,
		RestoreSession: true,
	}
}

type connectionsFile struct {
	Connections []Connection `toml:"connection"`
}

// Store reads and writes config files in a directory.
type Store struct {
	Dir      string // config dir
	StateDir string // history etc.
}

// DefaultStore uses XDG locations.
func DefaultStore() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		state = filepath.Join(home, ".local", "state")
	}
	return &Store{Dir: filepath.Join(cfg, "squi"), StateDir: filepath.Join(state, "squi")}, nil
}

// NewStore uses dir for both config and state (useful for --config and tests).
func NewStore(dir string) *Store {
	return &Store{Dir: dir, StateDir: filepath.Join(dir, "state")}
}

func (s *Store) connectionsPath() string { return filepath.Join(s.Dir, "connections.toml") }
func (s *Store) settingsPath() string    { return filepath.Join(s.Dir, "settings.toml") }

// SnippetsPath is where named snippets are stored.
func (s *Store) SnippetsPath() string { return filepath.Join(s.Dir, "snippets.toml") }

// HistoryDir is where query history files are stored.
func (s *Store) HistoryDir() string { return filepath.Join(s.StateDir, "history") }

// LoadConnections returns saved connections sorted by name.
func (s *Store) LoadConnections() ([]Connection, error) {
	var f connectionsFile
	if _, err := toml.DecodeFile(s.connectionsPath(), &f); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", s.connectionsPath(), err)
	}
	for i := range f.Connections {
		normalize(&f.Connections[i])
	}
	sort.SliceStable(f.Connections, func(i, j int) bool {
		return strings.ToLower(f.Connections[i].Name) < strings.ToLower(f.Connections[j].Name)
	})
	return f.Connections, nil
}

// SaveConnections writes all connections atomically.
func (s *Store) SaveConnections(conns []Connection) error {
	for i := range conns {
		normalize(&conns[i])
	}
	return writeTOML(s.connectionsPath(), connectionsFile{Connections: conns})
}

// Upsert inserts or replaces c (matched by ID) and saves.
func (s *Store) Upsert(c Connection) (Connection, error) {
	conns, err := s.LoadConnections()
	if err != nil {
		return c, err
	}
	normalize(&c)
	replaced := false
	for i := range conns {
		if conns[i].ID == c.ID {
			conns[i] = c
			replaced = true
		}
	}
	if !replaced {
		conns = append(conns, c)
	}
	return c, s.SaveConnections(conns)
}

// Delete removes the connection with id and saves.
func (s *Store) Delete(id string) error {
	conns, err := s.LoadConnections()
	if err != nil {
		return err
	}
	out := conns[:0]
	for _, c := range conns {
		if c.ID != id {
			out = append(out, c)
		}
	}
	return s.SaveConnections(out)
}

// LoadSettings returns settings merged over defaults.
func (s *Store) LoadSettings() (Settings, error) {
	st := DefaultSettings()
	if _, err := toml.DecodeFile(s.settingsPath(), &st); err != nil && !os.IsNotExist(err) {
		return DefaultSettings(), fmt.Errorf("read %s: %w", s.settingsPath(), err)
	}
	if st.FetchSize <= 0 {
		st.FetchSize = 1000
	}
	if st.MaxColumnWidth <= 0 {
		st.MaxColumnWidth = 40
	}
	if st.ExplorerWidth <= 0 {
		st.ExplorerWidth = 32
	}
	return st, nil
}

// SaveSettings writes settings.
func (s *Store) SaveSettings(st Settings) error { return writeTOML(s.settingsPath(), st) }

func normalize(c *Connection) {
	if c.ID == "" {
		c.ID = NewID()
	}
	if c.Driver == "" {
		c.Driver = "postgres"
	}
	if c.Port == 0 {
		switch c.Driver {
		case "mysql":
			c.Port = 3306
		case "clickhouse":
			c.Port = 8123
		default:
			c.Port = 5432
		}
	}
	if c.SSH.Auth == "" {
		c.SSH.Auth = SSHAuthAuto
	}
}

// NewID returns a random 16-hex-char identifier.
func NewID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func writeTOML(path string, v any) error {
	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf)
	enc.Indent = ""
	if err := enc.Encode(v); err != nil {
		return err
	}
	return writeFile(path, buf.Bytes())
}

// writeFile replaces path atomically with a 0600 file.
func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
