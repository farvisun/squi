package ui

import (
	"testing"

	"github.com/farvisun/squi/internal/config"
	"github.com/farvisun/squi/internal/secrets"
)

func TestConnectURLIsTransient(t *testing.T) {
	m, tb, _ := newTestModel(t, config.Connection{ID: "c", Name: "dev", Driver: "postgres"})
	_ = m.connectURL("mysql://root:pw@127.0.0.1:1/shop?env=dev") // the connect itself is not run
	if len(m.transient) != 1 {
		t.Fatalf("transient = %+v", m.transient)
	}
	u := m.transient[0]
	if u.Driver != "mysql" || u.Database != "shop" || !u.Transient || u.ID == "" {
		t.Fatalf("profile = %+v", u)
	}
	if pw, ok, _ := m.connector.memory.Get(u.ID, secrets.DBPassword); !ok || pw != "pw" {
		t.Fatal("URL password should be kept in memory")
	}
	// survives a reload of the saved profiles, but is never written
	if err := m.reloadConns(); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.connByID(u.ID); !ok {
		t.Fatal("transient profile lost on reload")
	}
	if saved, _ := m.store.LoadConnections(); len(saved) != 0 {
		t.Fatalf("transient profile was saved: %+v", saved)
	}
	// saving it from the form makes it a regular profile
	if _, err := m.store.Upsert(u); err != nil {
		t.Fatal(err)
	}
	_ = m.reloadConns()
	if len(m.transient) != 0 || len(m.conns) != 1 {
		t.Fatalf("after save: transient=%d conns=%d", len(m.transient), len(m.conns))
	}
	// tabs on transient connections are not restored next time
	tb.ac.cfg.Transient = true
	if n := len(m.sessionState().Tabs); n != 0 {
		t.Fatalf("session has %d tabs", n)
	}
}

func TestConnectURLRejectsBadURL(t *testing.T) {
	m, _, _ := newTestModel(t, config.Connection{ID: "c", Name: "dev", Driver: "postgres"})
	_ = m.connectURL("oracle://x/y")
	if len(m.transient) != 0 || m.toastLevel != toastError {
		t.Fatalf("transient=%d toast=%q", len(m.transient), m.toastText)
	}
}

func TestFormFillFromURL(t *testing.T) {
	m, _, _ := newTestModel(t, config.Connection{ID: "c", Name: "dev", Driver: "postgres"})
	m.openConnections()
	key(m, "ctrl+n")
	mgr := m.mgr
	mgr.form.SetValue("name", "my-ch")
	_ = mgr.fillFromURL(m, "clickhouse://default:pw@ch.internal:8443/logs?secure=1&env=staging&ssh=bastion")
	cfg, pw, _ := mgr.formConn()
	if cfg.Name != "my-ch" || cfg.Driver != "clickhouse" || cfg.Host != "ch.internal" || cfg.Port != 8443 || cfg.SSLMode != "require" ||
		cfg.Env != config.EnvStaging || !cfg.SSH.Enabled || cfg.SSH.Host != "bastion" || pw != "pw" {
		t.Fatalf("form = %+v pw=%q", cfg, pw)
	}
}
