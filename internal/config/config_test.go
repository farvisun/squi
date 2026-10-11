package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestConnectionsRoundTrip(t *testing.T) {
	s := NewStore(t.TempDir())
	conns, err := s.LoadConnections()
	if err != nil || len(conns) != 0 {
		t.Fatalf("empty load: %v %v", conns, err)
	}
	c, err := s.Upsert(Connection{Name: "prod", Driver: "mysql", Host: "db", User: "app", Env: EnvProd,
		SSH: SSH{Enabled: true, Alias: "bastion", JumpHosts: []string{"jump1", "u@jump2:2222"}}})
	if err != nil {
		t.Fatal(err)
	}
	if c.ID == "" || c.Port != 3306 || c.SSH.Auth != SSHAuthAuto {
		t.Fatalf("not normalized: %+v", c)
	}
	if _, err := s.Upsert(Connection{Name: "a-local", Host: "localhost"}); err != nil {
		t.Fatal(err)
	}
	conns, err = s.LoadConnections()
	if err != nil {
		t.Fatal(err)
	}
	if len(conns) != 2 || conns[0].Name != "a-local" || conns[1].SSH.JumpHosts[1] != "u@jump2:2222" {
		t.Fatalf("bad load: %+v", conns)
	}
	c.Host = "db2"
	if _, err := s.Upsert(c); err != nil {
		t.Fatal(err)
	}
	conns, _ = s.LoadConnections()
	if len(conns) != 2 || conns[1].Host != "db2" {
		t.Fatalf("upsert did not replace: %+v", conns)
	}
	if err := s.Delete(c.ID); err != nil {
		t.Fatal(err)
	}
	conns, _ = s.LoadConnections()
	if len(conns) != 1 {
		t.Fatalf("delete failed: %+v", conns)
	}
	raw, _ := os.ReadFile(s.connectionsPath())
	if strings.Contains(strings.ToLower(string(raw)), "password =") {
		t.Fatal("password must never be written to config")
	}
	fi, _ := os.Stat(s.connectionsPath())
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %v", fi.Mode().Perm())
	}
}

func TestSettingsDefaults(t *testing.T) {
	s := NewStore(t.TempDir())
	st, err := s.LoadSettings()
	if err != nil || st.FetchSize != 1000 || !st.VimMode {
		t.Fatalf("%+v %v", st, err)
	}
	st.VimMode = false
	if err := s.SaveSettings(st); err != nil {
		t.Fatal(err)
	}
	st2, _ := s.LoadSettings()
	if st2.VimMode {
		t.Fatal("vim mode not persisted")
	}
}

func TestSessionRoundTrip(t *testing.T) {
	s := NewStore(t.TempDir())
	if got, err := s.LoadSession(); err != nil || len(got.Tabs) != 0 {
		t.Fatalf("empty: %v %v", got, err)
	}
	in := Session{Tabs: []SessionTab{{Conn: "a", Database: "app", SQL: "select 1", Cursor: 3, Active: true}}}
	if err := s.SaveSession(in); err != nil {
		t.Fatal(err)
	}
	out, err := s.LoadSession()
	if err != nil || len(out.Tabs) != 1 || out.Tabs[0] != in.Tabs[0] {
		t.Fatalf("got %+v %v", out, err)
	}
	if st, _ := os.Stat(s.SessionPath()); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
}

func TestQueryTimeout(t *testing.T) {
	cases := []struct {
		c    Connection
		want time.Duration
	}{
		{Connection{}, 0},
		{Connection{Env: EnvProd}, DefaultProdTimeout},
		{Connection{Env: EnvProd, StatementTimeout: "0"}, 0},
		{Connection{Env: EnvProd, StatementTimeout: "off"}, 0},
		{Connection{Env: EnvDev, StatementTimeout: "2m"}, 2 * time.Minute},
		{Connection{StatementTimeout: "45"}, 45 * time.Second},
		{Connection{StatementTimeout: "1.5"}, 1500 * time.Millisecond},
		{Connection{Env: EnvProd, StatementTimeout: "bogus"}, DefaultProdTimeout},
	}
	for _, c := range cases {
		if got := c.c.QueryTimeout(); got != c.want {
			t.Errorf("%+v: got %v want %v", c.c, got, c.want)
		}
	}
	for _, bad := range []string{"-1", "-5s", "soon"} {
		if _, err := ParseTimeout(bad); err == nil {
			t.Errorf("ParseTimeout(%q) accepted", bad)
		}
	}
}

func TestFavoritesRoundTrip(t *testing.T) {
	s := NewStore(t.TempDir())
	f, err := s.LoadFavorites()
	if err != nil || len(f) != 0 {
		t.Fatalf("empty: %v %v", f, err)
	}
	users := Favorite{Database: "app", Schema: "public", Name: "users", Kind: "table"}
	paid := Favorite{Database: "app", Schema: "public", Name: "orders", Kind: "table", Where: "status = 'paid'", OrderBy: "id", Desc: true}
	if !f.Toggle("c1", users) || !f.Toggle("c1", paid) || !f.Toggle("c2", users) {
		t.Fatal("toggle add")
	}
	if err := s.SaveFavorites(f); err != nil {
		t.Fatal(err)
	}
	g, err := s.LoadFavorites()
	if err != nil || len(g["c1"]) != 2 || g["c1"][1] != paid || !g["c1"][1].IsView() || g["c1"][0].IsView() {
		t.Fatalf("load: %+v %v", g, err)
	}
	if g.Toggle("c2", users) || len(g["c2"]) != 0 {
		t.Fatal("toggle remove")
	}
	_ = s.SaveFavorites(g)
	h, _ := s.LoadFavorites()
	if _, ok := h["c2"]; ok {
		t.Fatal("empty lists should be dropped")
	}
	if fi, _ := os.Stat(s.FavoritesPath()); fi.Mode().Perm() != 0o600 {
		t.Fatalf("perm %v", fi.Mode().Perm())
	}
}
