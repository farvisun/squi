package ui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/farvisun/squi/internal/db"
)

// watchdog cancels the statement running on a session once it has run
// longer than the statement timeout. It is enforced client-side through
// Session.Cancel, so it works the same on every engine and only covers the
// time until the first page of rows arrives: a lazily-read result or an
// export is never cut off while it is being consumed.
type watchdog struct {
	mu    sync.Mutex
	timer *time.Timer
	done  bool
	fired bool
}

// startWatchdog arms a watchdog for s; d <= 0 disables it.
func startWatchdog(s db.Session, d time.Duration) *watchdog {
	w := &watchdog{}
	if d <= 0 {
		return w
	}
	w.timer = time.AfterFunc(d, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.done {
			return
		}
		w.fired = true
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.Cancel(ctx)
	})
	return w
}

// stop disarms the watchdog and reports whether it cancelled the statement.
func (w *watchdog) stop() bool {
	if w.timer != nil {
		w.timer.Stop()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.done = true
	return w.fired
}

// timeoutError is a statement cancelled by the statement timeout.
type timeoutError struct {
	after time.Duration
	err   error
}

func (e *timeoutError) Error() string {
	return fmt.Sprintf("statement timeout: cancelled after %s (%v)\n:timeout 0 disables the limit for this tab, :timeout 5m raises it",
		fmtTimeout(e.after), e.err)
}

func (e *timeoutError) Unwrap() error { return e.err }

// queryTimeout is the statement timeout of the tab: its :timeout override,
// or the connection's setting.
func (t *tab) queryTimeout() time.Duration {
	if t.timeoutSet {
		return t.timeout
	}
	return t.ac.cfg.QueryTimeout()
}

// fmtTimeout formats a timeout compactly: 30s, 2m, 1h30m, 1.5s.
func fmtTimeout(d time.Duration) string {
	s := d.Round(time.Millisecond).String()
	if strings.HasSuffix(s, "m0s") {
		s = s[:len(s)-2]
	}
	if strings.HasSuffix(s, "h0m") {
		s = s[:len(s)-2]
	}
	return s
}
