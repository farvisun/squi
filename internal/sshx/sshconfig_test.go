package sshx

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/farvisun/squi/internal/config"
)

func TestParseHostSpec(t *testing.T) {
	tests := []struct {
		in, user, host string
		port           int
		wantErr        bool
	}{
		{in: "bastion", host: "bastion"},
		{in: "squi@bastion", user: "squi", host: "bastion"},
		{in: "squi@bastion:2222", user: "squi", host: "bastion", port: 2222},
		{in: "ssh://u@h:1", user: "u", host: "h", port: 1},
		{in: "[::1]:2200", host: "::1", port: 2200},
		{in: "u@[fe80::1]", user: "u", host: "fe80::1"},
		{in: "::1", host: "::1"},
		{in: "", wantErr: true},
		{in: "u@", wantErr: true},
		{in: "@h", wantErr: true},
		{in: "h:abc", wantErr: true},
		{in: "h:0", wantErr: true},
		{in: "h:70000", wantErr: true},
	}
	for _, tt := range tests {
		user, host, port, err := parseHostSpec(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseHostSpec(%q): want error", tt.in)
			}
			continue
		}
		if err != nil || user != tt.user || host != tt.host || port != tt.port {
			t.Errorf("parseHostSpec(%q) = %q, %q, %d, %v; want %q, %q, %d",
				tt.in, user, host, port, err, tt.user, tt.host, tt.port)
		}
	}
}

func TestSplitProxyJump(t *testing.T) {
	for in, want := range map[string][]string{
		"":              nil,
		"none":          nil,
		"a":             {"a"},
		" a , b@c:2 , ": {"a", "b@c:2"},
	} {
		if got := splitProxyJump(in); !reflect.DeepEqual(got, want) {
			t.Errorf("splitProxyJump(%q) = %q, want %q", in, got, want)
		}
	}
}

func writeConfig(t *testing.T) *sshConfig {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USER", "local")
	path := filepath.Join(home, "config")
	const cfg = `
Host db
  HostName db-bastion.example.com
  User alice
  Port 2222
  IdentityFile ~/.ssh/work_key
  IdentityFile /keys/%r_%h
  IdentitiesOnly yes
  UserKnownHostsFile ~/kh1 ~/kh2
  ProxyJump jump1,carol@jump2:2022

Host jump1
  HostName j1.example.com
  User bob
  ProxyJump outer

Host outer
  HostName outer.example.com

Host *.internal
  User dave
  Port 2200
  ProxyJump none

Host hn
  HostName %h.example.org
`
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := loadSSHConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func chainStrings(hops []hop) []string {
	var out []string
	for _, h := range hops {
		out = append(out, h.String())
	}
	return out
}

func TestResolveChainAlias(t *testing.T) {
	c := writeConfig(t)
	home := os.Getenv("HOME")
	hops, err := resolveChain(c, config.SSH{Alias: "db"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"local@outer.example.com:22", // jump1's own ProxyJump
		"bob@j1.example.com:22",
		"carol@jump2:2022",
		"alice@db-bastion.example.com:2222",
	}
	if got := chainStrings(hops); !reflect.DeepEqual(got, want) {
		t.Fatalf("chain = %q, want %q", got, want)
	}
	final := hops[len(hops)-1]
	wantIDs := []string{filepath.Join(home, ".ssh/work_key"), "/keys/alice_db-bastion.example.com"}
	if !reflect.DeepEqual(final.identityFiles, wantIDs) {
		t.Errorf("identityFiles = %q, want %q", final.identityFiles, wantIDs)
	}
	if !final.identitiesOnly {
		t.Error("identitiesOnly not set")
	}
	wantKH := []string{filepath.Join(home, "kh1"), filepath.Join(home, "kh2")}
	if !reflect.DeepEqual(final.knownHosts, wantKH) {
		t.Errorf("knownHosts = %q, want %q", final.knownHosts, wantKH)
	}
}

func TestResolveChainOverrides(t *testing.T) {
	c := writeConfig(t)
	hops, err := resolveChain(c, config.SSH{
		Alias: "db", Host: "10.0.0.5", User: "eve", Port: 22,
		JumpHosts: []string{"x@hop:2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"x@hop:2", "eve@10.0.0.5:22"}
	if got := chainStrings(hops); !reflect.DeepEqual(got, want) {
		t.Fatalf("chain = %q, want %q", got, want)
	}
	// Explicit fields still keep config values they don't override.
	if len(hops[1].identityFiles) != 2 {
		t.Errorf("identityFiles = %q", hops[1].identityFiles)
	}
}

func TestResolveChainHostPattern(t *testing.T) {
	c := writeConfig(t)
	for _, tt := range []struct {
		s    config.SSH
		want []string
	}{
		{config.SSH{Host: "pg.internal"}, []string{"dave@pg.internal:2200"}},
		{config.SSH{Host: "pg.internal", User: "u", Port: 1}, []string{"u@pg.internal:1"}},
		{config.SSH{Host: "hn"}, []string{"local@hn.example.org:22"}},
		{config.SSH{Host: "plain"}, []string{"local@plain:22"}},
		{config.SSH{Alias: "db", JumpHosts: []string{"none"}}, []string{"alice@db-bastion.example.com:2222"}},
	} {
		hops, err := resolveChain(c, tt.s)
		if err != nil {
			t.Fatal(err)
		}
		if got := chainStrings(hops); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%+v: chain = %q, want %q", tt.s, got, tt.want)
		}
	}
	if _, err := resolveChain(c, config.SSH{}); err == nil {
		t.Error("empty host: want error")
	}
}

func TestLoadSSHConfigMissingAndNone(t *testing.T) {
	t.Setenv("USER", "local")
	for _, p := range []string{"none", filepath.Join(t.TempDir(), "missing")} {
		c, err := loadSSHConfig(p)
		if err != nil {
			t.Fatal(err)
		}
		hops, err := resolveChain(c, config.SSH{Host: "h"})
		if err != nil || chainStrings(hops)[0] != "local@h:22" {
			t.Errorf("%s: %q, %v", p, chainStrings(hops), err)
		}
	}
}
