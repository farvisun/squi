package sshx

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/farvisun/squi/internal/config"
)

// Integration tests run against the dev/ docker compose bastions:
//
//	SQUI_TEST_SSH=1 go test ./internal/sshx/ -run Integration

const bastion1Port = 22222

func integrationOptions(t *testing.T, p Prompter) Options {
	t.Helper()
	if os.Getenv("SQUI_TEST_SSH") != "1" {
		t.Skip("set SQUI_TEST_SSH=1 to run against the dev bastions")
	}
	opts := testOptions(t, bastion1Port, p)
	opts.Timeout = 10 * time.Second
	return opts
}

func devKeyPath(t *testing.T) string {
	p, err := filepath.Abs("../../dev/keys/id_ed25519")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func integrationDial(t *testing.T, tun *Tunnel, addr string) net.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := tun.DialContext(ctx, "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// checkPostgres sends an SSLRequest and expects 'S' or 'N'.
func checkPostgres(t *testing.T, conn net.Conn) {
	t.Helper()
	if _, err := conn.Write([]byte{0, 0, 0, 8, 0x04, 0xd2, 0x16, 0x2f}); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 1)
	if _, err := io.ReadFull(conn, b); err != nil {
		t.Fatal(err)
	}
	if b[0] != 'S' && b[0] != 'N' {
		t.Fatalf("SSLRequest reply = %q, want S or N", b)
	}
}

func TestIntegrationPassword(t *testing.T) {
	opts := integrationOptions(t, trusting())
	opts.SSH.Auth, opts.Password = config.SSHAuthPassword, "squi"
	tun := dialOK(t, opts)
	checkSecrets(t, tun, "squi", "")
	checkPostgres(t, integrationDial(t, tun, "postgres:5432"))
}

func TestIntegrationKey(t *testing.T) {
	opts := integrationOptions(t, trusting())
	opts.SSH.Auth, opts.SSH.KeyPath = config.SSHAuthKey, devKeyPath(t)
	tun := dialOK(t, opts)
	checkSecrets(t, tun, "", "")
	checkPostgres(t, integrationDial(t, tun, "postgres:5432"))
}

func TestIntegrationTwoHops(t *testing.T) {
	p := trusting()
	p.answers = map[SecretKind][]string{KindPassword: {"squi"}} // for bastion1
	opts := integrationOptions(t, p)
	opts.SSH.JumpHosts = []string{"squi@127.0.0.1:22222"}
	opts.SSH.Host, opts.SSH.Port = "bastion2", 22
	opts.SSH.Auth, opts.Password = config.SSHAuthPassword, "squi"
	tun := dialOK(t, opts)
	if tun.Target() != "squi@bastion2:22" {
		t.Errorf("Target = %q", tun.Target())
	}

	conn := integrationDial(t, tun, "mysql:3306")
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		t.Fatal(err)
	}
	n := int(hdr[0]) | int(hdr[1])<<8 | int(hdr[2])<<16
	payload := make([]byte, n)
	if _, err := io.ReadFull(conn, payload); err != nil {
		t.Fatal(err)
	}
	if hdr[3] != 0 || n < 2 || payload[0] != 10 {
		t.Fatalf("not a MySQL v10 greeting: hdr=%x payload[0]=%d", hdr, payload[0])
	}
	if i := bytes.IndexByte(payload[1:], 0); i > 0 {
		t.Logf("MySQL server version %s via %s", payload[1:1+i], tun.Target())
	}
	if p.hostKeyCalls != 2 {
		t.Errorf("ConfirmHostKey calls = %d, want 2", p.hostKeyCalls)
	}
}
