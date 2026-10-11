package config

import (
	"reflect"
	"testing"
)

func TestParseURL(t *testing.T) {
	cases := []struct {
		in   string
		want Connection
		pw   string
	}{
		{"postgres://app:s%40cret@db.internal:6432/api?sslmode=require&env=prod",
			Connection{Name: "app@db.internal/api", Driver: "postgres", Host: "db.internal", Port: 6432, User: "app", Database: "api",
				SSLMode: "require", Env: EnvProd, SSH: SSH{Auth: SSHAuthAuto}}, "s@cret"},
		{"postgresql:///shop?host=/var/run/postgresql",
			Connection{Name: "/var/run/postgresql/shop", Driver: "postgres", Host: "/var/run/postgresql", Port: 5432, Database: "shop",
				SSH: SSH{Auth: SSHAuthAuto}}, ""},
		{"mariadb://root@127.0.0.1/shop?read_only=1&timeout=1m&name=local-shop&label=EU&color=purple",
			Connection{Name: "local-shop", Label: "EU", Color: "purple", Driver: "mysql", Host: "127.0.0.1", Port: 3306, User: "root", Database: "shop",
				ReadOnly: true, StatementTimeout: "1m", SSH: SSH{Auth: SSHAuthAuto}}, ""},
		{"clickhouse://default@ch:8443/logs?secure=true&max_threads=4",
			Connection{Name: "default@ch/logs", Driver: "clickhouse", Host: "ch", Port: 8443, User: "default", Database: "logs",
				SSLMode: "require", Params: map[string]string{"max_threads": "4"}, SSH: SSH{Auth: SSHAuthAuto}}, ""},
		{"clickhouse://ch/default",
			Connection{Name: "ch/default", Driver: "clickhouse", Host: "ch", Port: 8123, Database: "default", SSH: SSH{Auth: SSHAuthAuto}}, ""},
		{"sqlserver://sa:pw@mssql?database=shop&ApplicationIntent=ReadOnly",
			Connection{Name: "sa@mssql/shop", Driver: "sqlserver", Host: "mssql", Port: 1433, User: "sa", Database: "shop",
				Params: map[string]string{"ApplicationIntent": "ReadOnly"}, SSH: SSH{Auth: SSHAuthAuto}}, "pw"},
		{"mssql://app@db:14330/erp",
			Connection{Name: "app@db/erp", Driver: "sqlserver", Host: "db", Port: 14330, User: "app", Database: "erp", SSH: SSH{Auth: SSHAuthAuto}}, ""},
		{"sqlite:///var/lib/app/data.db?read_only=1&mode=rwc",
			Connection{Name: "data.db", Driver: "sqlite", Database: "/var/lib/app/data.db", ReadOnly: true,
				Params: map[string]string{"mode": "rwc"}, SSH: SSH{Auth: SSHAuthAuto}}, ""},
		{"sqlite:rel%20dir/x.sqlite", Connection{Name: "x.sqlite", Driver: "sqlite", Database: "rel dir/x.sqlite", SSH: SSH{Auth: SSHAuthAuto}}, ""},
		{"sqlite://~/notes.db?env=local", Connection{Name: "notes.db", Driver: "sqlite", Database: "~/notes.db", Env: EnvLocal, SSH: SSH{Auth: SSHAuthAuto}}, ""},
		{"./dev/squi.db", Connection{Name: "squi.db", Driver: "sqlite", Database: "./dev/squi.db", SSH: SSH{Auth: SSHAuthAuto}}, ""},
		{"postgres://app@10.0.0.5/api?ssh=ops@bastion:2222&ssh_jump=jump1,u@jump2&ssh_key=~/.ssh/prod",
			Connection{Name: "app@10.0.0.5/api", Driver: "postgres", Host: "10.0.0.5", Port: 5432, User: "app", Database: "api",
				SSH: SSH{Enabled: true, Host: "bastion", Port: 2222, User: "ops", Auth: SSHAuthAuto, KeyPath: "~/.ssh/prod",
					JumpHosts: []string{"jump1", "u@jump2"}}}, ""},
	}
	for _, c := range cases {
		got, pw, err := ParseURL(c.in)
		if err != nil {
			t.Errorf("%s: %v", c.in, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) || pw != c.pw {
			t.Errorf("%s:\n got %+v pw=%q\nwant %+v pw=%q", c.in, got, pw, c.want, c.pw)
		}
	}
	for _, bad := range []string{
		"oracle://x/y", "postgres://h:99999/db", "postgres://h/db?env=moon", "postgres://h/db?timeout=-3",
		"postgres://h/db?ssh=:22", "mysql://h/db?fetch_size=x", "sqlite:", "sqlite:x.db?ssh=bastion",
	} {
		if _, _, err := ParseURL(bad); err == nil {
			t.Errorf("ParseURL(%q) accepted", bad)
		}
	}
	if IsURL("prod-api") || !IsURL("mysql://x") || !IsURL("sqlite:x") || !IsURL("app.SQLite3") {
		t.Error("IsURL")
	}
}

func TestExpandEnv(t *testing.T) {
	t.Setenv("SQUI_T_HOST", "db.example")
	t.Setenv("SQUI_T_EMPTY", "")
	t.Setenv("SQUI_T_PW", "hunter2")
	for in, want := range map[string]string{
		"${SQUI_T_HOST}":               "db.example",
		"${SQUI_T_HOST}:5432":          "db.example:5432",
		"${SQUI_T_MISSING:-localhost}": "localhost",
		"${SQUI_T_EMPTY:-fallback}":    "fallback",
		"${SQUI_T_MISSING}":            "",
		"pa$$word $HOME":               "pa$$word $HOME",
	} {
		if got := ExpandEnv(in); got != want {
			t.Errorf("ExpandEnv(%q) = %q want %q", in, got, want)
		}
	}
	c := Connection{Host: "${SQUI_T_HOST}", User: "u", Params: map[string]string{"service": "${SQUI_T_HOST}"},
		SSH: SSH{JumpHosts: []string{"${SQUI_T_HOST}"}}, PasswordEnv: "SQUI_T_PW"}
	e := c.Expanded()
	if e.Host != "db.example" || e.Params["service"] != "db.example" || e.SSH.JumpHosts[0] != "db.example" {
		t.Fatalf("Expanded: %+v", e)
	}
	if c.Host != "${SQUI_T_HOST}" || c.Params["service"] != "${SQUI_T_HOST}" || c.SSH.JumpHosts[0] != "${SQUI_T_HOST}" {
		t.Fatal("Expanded must not modify the profile")
	}
	if pw, ok := c.EnvPassword(); !ok || pw != "hunter2" {
		t.Fatalf("EnvPassword = %q %v", pw, ok)
	}
	if _, ok := (Connection{PasswordEnv: "SQUI_T_EMPTY"}).EnvPassword(); ok {
		t.Fatal("empty env password should not count")
	}
}

func TestParamsRoundTrip(t *testing.T) {
	s := NewStore(t.TempDir())
	c, err := s.Upsert(Connection{Name: "svc", Host: "h", PasswordEnv: "PGPASSWORD", Params: map[string]string{"service": "prod"}, Transient: true})
	if err != nil {
		t.Fatal(err)
	}
	conns, _ := s.LoadConnections()
	if len(conns) != 1 || conns[0].ID != c.ID || conns[0].Params["service"] != "prod" || conns[0].PasswordEnv != "PGPASSWORD" || conns[0].Transient {
		t.Fatalf("%+v", conns)
	}
}
