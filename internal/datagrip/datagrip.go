// Package datagrip reads JetBrains DataGrip data sources, SSH configs and
// query consoles so squi can import them and keep them in sync. The files
// are undocumented IDE state, so parsing is lenient: unknown elements are
// ignored and a data source squi cannot use is skipped with a reason.
//
// Data sources live in a project's .idea/dataSources.xml (shared part) and
// .idea/dataSources.local.xml (user name, SSH tunnel, secret storage), or in
// an IDE's options directory for global data sources. SSH tunnels are
// defined once per IDE in options/sshConfigs.xml and referenced by id.
// Consoles are stored per data source in consoles/db/<uuid>/ of the IDE
// configuration directory.
package datagrip

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// Tool is the config.Origin tool name of imported profiles.
const Tool = "datagrip"

// Root returns the JetBrains configuration root of this platform.
func Root() string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "JetBrains")
	case "windows":
		if a := os.Getenv("APPDATA"); a != "" {
			return filepath.Join(a, "JetBrains")
		}
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "JetBrains")
	}
	return filepath.Join(home, ".config", "JetBrains")
}

// ConfigDirs returns the DataGrip configuration directories under root
// (DataGrip2026.2, DataGrip2025.3, …), newest version first.
func ConfigDirs(root string) []string {
	entries, _ := os.ReadDir(root)
	var dirs []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "DataGrip") {
			dirs = append(dirs, filepath.Join(root, e.Name()))
		}
	}
	sort.SliceStable(dirs, func(i, j int) bool {
		a, b := versionOf(filepath.Base(dirs[i])), versionOf(filepath.Base(dirs[j]))
		for k := 0; k < len(a) && k < len(b); k++ {
			if a[k] != b[k] {
				return a[k] > b[k]
			}
		}
		return len(a) > len(b)
	})
	return dirs
}

var digits = regexp.MustCompile(`\d+`)

func versionOf(name string) []int {
	var v []int
	for _, d := range digits.FindAllString(name, -1) {
		n, _ := strconv.Atoi(d)
		v = append(v, n)
	}
	return v
}

// Source is a place data sources are defined.
type Source struct {
	// Dir holds dataSources.xml and dataSources.local.xml: a project's .idea
	// directory or an IDE's options directory.
	Dir string
	// Project is the project directory ($PROJECT_DIR$); empty for global
	// data sources.
	Project string
	// Config is the IDE configuration directory with the SSH configs.
	Config string
}

// Key identifies the source in config.Origin.Source.
func (s Source) Key() string {
	if s.Project != "" {
		return s.Project
	}
	return s.Dir
}

// Discover finds the data source definitions of the newest DataGrip under
// root: its global data sources and those of its recent projects.
func Discover(root string) []Source {
	dirs := ConfigDirs(root)
	if len(dirs) == 0 {
		return nil
	}
	cfg := dirs[0]
	var out []Source
	if exists(filepath.Join(cfg, "options", "dataSources.xml")) {
		out = append(out, Source{Dir: filepath.Join(cfg, "options"), Config: cfg})
	}
	seen := map[string]bool{}
	for _, p := range recentProjects(cfg) {
		idea := filepath.Join(p, ".idea")
		if !seen[p] && exists(filepath.Join(idea, "dataSources.xml")) {
			seen[p] = true
			out = append(out, Source{Dir: idea, Project: p, Config: cfg})
		}
	}
	return out
}

// SourceAt returns the source at path: a DataGrip project directory, its
// .idea directory, or a JetBrains IDE options directory with global data
// sources (whose SSH configs are read from the same IDE).
func SourceAt(path, root string) (Source, error) {
	path, err := filepath.Abs(expandHome(path))
	if err != nil {
		return Source{}, err
	}
	cfg := ""
	if dirs := ConfigDirs(root); len(dirs) > 0 {
		cfg = dirs[0]
	}
	switch {
	case exists(filepath.Join(path, ".idea", "dataSources.xml")):
		return Source{Dir: filepath.Join(path, ".idea"), Project: path, Config: cfg}, nil
	case filepath.Base(path) == ".idea" && exists(filepath.Join(path, "dataSources.xml")):
		return Source{Dir: path, Project: filepath.Dir(path), Config: cfg}, nil
	case exists(filepath.Join(path, "dataSources.xml")):
		return Source{Dir: path, Config: filepath.Dir(path)}, nil
	}
	return Source{}, fmt.Errorf("%s: no DataGrip data sources (dataSources.xml) found", path)
}

var entryKey = regexp.MustCompile(`<entry key="([^"]+)"`)

// recentProjects lists the project directories in recentProjects.xml.
func recentProjects(cfg string) []string {
	b, err := os.ReadFile(filepath.Join(cfg, "options", "recentProjects.xml"))
	if err != nil {
		return nil
	}
	var out []string
	for _, m := range entryKey.FindAllStringSubmatch(string(b), -1) {
		out = append(out, filepath.Clean(expandMacros(xmlUnescape(m[1]), "")))
	}
	return out
}

// DataSource is a DataGrip data source with the settings squi uses.
type DataSource struct {
	UUID, Name, Group string
	DriverRef         string // DataGrip driver id, e.g. postgresql, mysql.8
	URL               string // JDBC URL
	User              string
	ReadOnly          bool
	NoAuth            bool              // authentication "No auth"
	Props             map[string]string // driver properties
	SSL               *SSL
	SSH               *SSHConfig // nil without a tunnel
	SSHMissing        string     // id of a tunnel that is not defined
}

// SSL is a data source's SSL tab.
type SSL struct {
	Mode                          string // REQUIRE, VERIFY_CA, VERIFY_FULL
	CACert, ClientCert, ClientKey string
}

// SSHConfig is one entry of sshConfigs.xml.
type SSHConfig struct {
	ID, Name, Host string
	Port           int
	User           string
	AuthType       string // OPEN_SSH, KEY_PAIR, PASSWORD
	KeyPath        string
}

type xmlProject struct {
	Components []struct {
		DataSources []xmlDataSource `xml:"data-source"`
	} `xml:"component"`
}

type xmlDataSource struct {
	Name         string `xml:"name,attr"`
	Group        string `xml:"group,attr"`
	UUID         string `xml:"uuid,attr"`
	DriverRef    string `xml:"driver-ref"`
	URL          string `xml:"jdbc-url"`
	User         string `xml:"user-name"`
	ReadOnly     string `xml:"read-only"`
	AuthProvider string `xml:"auth-provider"`
	Props        []struct {
		Name  string `xml:"name,attr"`
		Value string `xml:"value,attr"`
	} `xml:"driver-properties>property"`
	SSH *struct {
		Enabled  string `xml:"enabled"`
		ConfigID string `xml:"ssh-config-id"`
	} `xml:"ssh-properties"`
	SSL *struct {
		Enabled    string `xml:"enabled"`
		Mode       string `xml:"mode"`
		CACert     string `xml:"ca-cert"`
		ClientCert string `xml:"client-cert"`
		ClientKey  string `xml:"client-key"`
	} `xml:"ssl-config"`
}

type xmlSSHConfigs struct {
	Configs []struct {
		ID         string `xml:"id,attr"`
		Host       string `xml:"host,attr"`
		Port       string `xml:"port,attr"`
		Username   string `xml:"username,attr"`
		AuthType   string `xml:"authType,attr"`
		KeyPath    string `xml:"keyPath,attr"`
		CustomName string `xml:"customName,attr"`
	} `xml:"component>configs>sshConfig"`
}

// Load reads the data sources of src, merged with their local settings and
// SSH tunnels.
func Load(src Source) ([]DataSource, error) {
	shared, err := readDataSources(filepath.Join(src.Dir, "dataSources.xml"))
	if err != nil {
		return nil, err
	}
	local, err := readDataSources(filepath.Join(src.Dir, "dataSources.local.xml"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	tunnels := loadSSHConfigs(src.Config)
	byUUID := map[string]xmlDataSource{}
	for _, l := range local {
		byUUID[l.UUID] = l
	}
	var out []DataSource
	for _, x := range shared {
		l := byUUID[x.UUID]
		ds := DataSource{
			UUID: x.UUID, Name: strings.TrimSpace(x.Name), Group: strings.Trim(x.Group, "/ "),
			DriverRef: strings.TrimSpace(x.DriverRef), URL: strings.TrimSpace(x.URL),
			User:     first(l.User, x.User),
			ReadOnly: isTrue(x.ReadOnly) || isTrue(l.ReadOnly),
			NoAuth:   first(l.AuthProvider, x.AuthProvider) == "no-auth",
		}
		for _, p := range append(x.Props, l.Props...) {
			if ds.Props == nil {
				ds.Props = map[string]string{}
			}
			ds.Props[p.Name] = p.Value
		}
		if s := firstPtr(l.SSL, x.SSL); s != nil && isTrue(s.Enabled) {
			ds.SSL = &SSL{Mode: s.Mode, CACert: s.CACert, ClientCert: s.ClientCert, ClientKey: s.ClientKey}
		}
		if s := firstPtr(l.SSH, x.SSH); s != nil && isTrue(s.Enabled) {
			if t, ok := tunnels[s.ConfigID]; ok {
				ds.SSH = &t
			} else {
				ds.SSHMissing = s.ConfigID
				if ds.SSHMissing == "" {
					ds.SSHMissing = "(none)"
				}
			}
		}
		out = append(out, ds)
	}
	return out, nil
}

func readDataSources(path string) ([]xmlDataSource, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p xmlProject
	if err := xml.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var out []xmlDataSource
	for _, c := range p.Components {
		out = append(out, c.DataSources...)
	}
	return out, nil
}

// loadSSHConfigs reads the SSH configs of an IDE configuration directory
// (options/sshConfigs.xml; older versions keep a copy at the top level).
func loadSSHConfigs(cfg string) map[string]SSHConfig {
	out := map[string]SSHConfig{}
	if cfg == "" {
		return out
	}
	for _, p := range []string{filepath.Join(cfg, "sshConfigs.xml"), filepath.Join(cfg, "options", "sshConfigs.xml")} {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var f xmlSSHConfigs
		if xml.Unmarshal(b, &f) != nil {
			continue
		}
		for _, c := range f.Configs {
			port, _ := strconv.Atoi(c.Port)
			out[c.ID] = SSHConfig{ID: c.ID, Name: c.CustomName, Host: c.Host, Port: port, User: c.Username,
				AuthType: strings.ToUpper(c.AuthType), KeyPath: c.KeyPath}
		}
	}
	return out
}

// expandMacros resolves the IDE path macros $USER_HOME$ and $PROJECT_DIR$.
func expandMacros(s, project string) string {
	if home, err := os.UserHomeDir(); err == nil {
		s = strings.ReplaceAll(s, "$USER_HOME$", home)
	}
	if project != "" {
		s = strings.ReplaceAll(s, "$PROJECT_DIR$", project)
	}
	return s
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

func xmlUnescape(s string) string {
	var out string
	if xml.Unmarshal([]byte("<x>"+s+"</x>"), &out) != nil {
		return s
	}
	return out
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func isTrue(s string) bool { return strings.EqualFold(strings.TrimSpace(s), "true") }

func first(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

func firstPtr[T any](vals ...*T) *T {
	for _, v := range vals {
		if v != nil {
			return v
		}
	}
	return nil
}
