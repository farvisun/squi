package config

import (
	"os"
	"strings"
	"testing"
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
