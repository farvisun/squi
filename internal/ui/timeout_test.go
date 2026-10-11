package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/farvisun/squi/internal/config"
)

func TestStatementTimeoutCancels(t *testing.T) {
	m, tb, _ := newTestModel(t, config.Connection{ID: "c", Name: "p", Driver: "postgres", Env: config.EnvProd})
	if got := tb.queryTimeout(); got != config.DefaultProdTimeout {
		t.Fatalf("prod default timeout = %v", got)
	}
	tb.timeout, tb.timeoutSet = 20*time.Millisecond, true
	setSQL(tb, "SELECT pg_sleep(10)")
	key(m, "f5")
	if !strings.Contains(m.toastText, "Statement timeout") {
		t.Fatalf("toast = %q", m.toastText)
	}
	last := tb.out.msgs.entries[len(tb.out.msgs.entries)-1]
	if last.level != logError || !strings.Contains(last.text, "statement timeout: cancelled after 20ms") {
		t.Fatalf("message = %+v", last)
	}
	if tb.busy {
		t.Fatal("tab still busy after timeout")
	}
}

func TestStatementTimeoutSparesFastQueries(t *testing.T) {
	m, tb, _ := newTestModel(t, config.Connection{ID: "c", Name: "dev", Driver: "postgres"})
	tb.timeout, tb.timeoutSet = 50*time.Millisecond, true
	setSQL(tb, "SELECT * FROM users")
	key(m, "f5")
	r := tb.out.results()[0]
	time.Sleep(80 * time.Millisecond) // the watchdog must not fire on the open stream
	drain(m, m.fetchMore(tb, r, 1<<30))
	if len(r.rows()) != 2500 || !r.done {
		t.Fatalf("rows=%d done=%v", len(r.rows()), r.done)
	}
}

func TestTimeoutCommand(t *testing.T) {
	m, tb, _ := newTestModel(t, config.Connection{ID: "c", Name: "p", Driver: "postgres", Env: config.EnvProd})
	drain(m, m.runCommand("timeout 0"))
	if tb.queryTimeout() != 0 || strings.Contains(m.View(), "⏱") {
		t.Fatalf("timeout 0: %v", tb.queryTimeout())
	}
	drain(m, m.runCommand("timeout 2m"))
	if tb.queryTimeout() != 2*time.Minute || !strings.Contains(m.View(), "⏱ 2m") {
		t.Fatalf("timeout 2m: %v", tb.queryTimeout())
	}
	drain(m, m.runCommand("timeout default"))
	if tb.queryTimeout() != config.DefaultProdTimeout {
		t.Fatalf("default: %v", tb.queryTimeout())
	}
	drain(m, m.runCommand("timeout soon"))
	if m.toastLevel != toastWarn {
		t.Fatalf("bad value should warn: %q", m.toastText)
	}
}

func TestFmtTimeout(t *testing.T) {
	for d, want := range map[time.Duration]string{
		30 * time.Second: "30s", 2 * time.Minute: "2m", 90 * time.Minute: "1h30m", time.Hour: "1h",
		1500 * time.Millisecond: "1.5s", 20 * time.Millisecond: "20ms",
	} {
		if got := fmtTimeout(d); got != want {
			t.Errorf("fmtTimeout(%v) = %q want %q", d, got, want)
		}
	}
}
