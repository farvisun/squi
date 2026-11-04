package ui

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/farvisun/squi/internal/update"
)

// updateModel is a model on 0.6.0 whose checks hit a local server offering
// latest. calls counts them.
func updateModel(t *testing.T, latest, notes string, calls *int) *Model {
	t.Helper()
	m, _, _ := newTestModel(t, devConn())
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*calls++
		json.NewEncoder(w).Encode(map[string]any{"tag_name": "v" + latest, "html_url": "https://example/rel", "body": notes})
	}))
	t.Cleanup(s.Close)
	c := update.New("0.6.0")
	c.API, c.HTTP = s.URL, s.Client()
	m.updater = c
	m.exePath = filepath.Join(t.TempDir(), "squi")
	return m
}

func TestAutoCheckNotifiesAndShowsBadge(t *testing.T) {
	calls := 0
	m := updateModel(t, "0.7.0", "notes", &calls)
	drain(m, m.autoUpdateCheck())
	if calls != 1 || m.updateAvail == nil || m.updateAvail.Version != "0.7.0" {
		t.Fatalf("calls %d, avail %+v", calls, m.updateAvail)
	}
	if !strings.Contains(m.toastText, "0.7.0") {
		t.Fatalf("toast %q", m.toastText)
	}
	x, y := locate(t, m, "↑ 0.7.0")
	click(m, x, y)
	if _, ok := m.modals[0].(*textModal); !ok {
		t.Fatalf("modal %T", m.modals[0])
	}
}

func TestAutoCheckAtMostOncePerDay(t *testing.T) {
	calls := 0
	m := updateModel(t, "0.7.0", "", &calls)
	drain(m, m.autoUpdateCheck())
	m.updateAvail = nil
	drain(m, m.autoUpdateCheck())
	if calls != 1 {
		t.Fatalf("calls = %d", calls)
	}
	if m.updateAvail == nil {
		t.Fatal("the remembered release should still be offered without calling out")
	}
	st := update.LoadState(m.updateStatePath())
	st.CheckedAt = time.Now().Add(-25 * time.Hour)
	if err := update.SaveState(m.updateStatePath(), st); err != nil {
		t.Fatal(err)
	}
	drain(m, m.autoUpdateCheck())
	if calls != 2 {
		t.Fatalf("after a day: calls = %d", calls)
	}
}

func TestAutoCheckCanBeTurnedOff(t *testing.T) {
	calls := 0
	m := updateModel(t, "0.7.0", "", &calls)
	m.settings.CheckUpdates = false
	drain(m, m.autoUpdateCheck())
	m.settings.CheckUpdates = true
	t.Setenv("SQUI_NO_UPDATE_CHECK", "1")
	drain(m, m.autoUpdateCheck())
	if calls != 0 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestDevBuildsDoNotCheck(t *testing.T) {
	m, _, _ := newTestModel(t, devConn())
	if m.updater != nil || m.autoUpdateCheck() != nil {
		t.Fatal("a build without a release version must not call out")
	}
	drain(m, m.checkForUpdate())
	if !strings.Contains(m.toastText, "release version") {
		t.Fatalf(":update should say why it can't check: %q", m.toastText)
	}
}

func TestAutoCheckFailureIsSilentManualIsNot(t *testing.T) {
	calls := 0
	m := updateModel(t, "0.7.0", "", &calls)
	m.toastText = ""
	drain(m, m.handleUpdateChecked(updateCheckedMsg{err: errors.New("offline")}))
	if m.toastText != "" {
		t.Fatalf("toast %q", m.toastText)
	}
	drain(m, m.handleUpdateChecked(updateCheckedMsg{err: errors.New("offline"), manual: true}))
	if !strings.Contains(m.toastText, "offline") {
		t.Fatalf("toast %q", m.toastText)
	}
}

func TestManualCheckWhenCurrent(t *testing.T) {
	calls := 0
	m := updateModel(t, "0.6.0", "", &calls)
	drain(m, m.checkForUpdate())
	if m.updateAvail != nil || !strings.Contains(m.toastText, "latest") {
		t.Fatalf("avail %+v, toast %q", m.updateAvail, m.toastText)
	}
}

func TestSkipHidesVersionUntilNewerOne(t *testing.T) {
	calls := 0
	m := updateModel(t, "0.7.0", "", &calls)
	drain(m, m.fetchRelease(false))
	drain(m, m.showUpdate())
	key(m, "s")
	if m.updateAvail != nil || len(m.modals) != 0 {
		t.Fatal("skip should clear the notice")
	}
	if m.offerUpdate(update.Release{Version: "0.7.0"}, update.LoadState(m.updateStatePath()).Skipped) {
		t.Fatal("a skipped version must stay hidden")
	}
	if !m.offerUpdate(update.Release{Version: "0.7.1"}, update.LoadState(m.updateStatePath()).Skipped) {
		t.Fatal("a newer version should show again")
	}
}

func TestReleaseNotesCannotDriveTheTerminal(t *testing.T) {
	calls := 0
	m := updateModel(t, "0.7.0", "evil \x1b]52;c;aGk=\x07 text", &calls)
	drain(m, m.fetchRelease(false))
	drain(m, m.showUpdate())
	if strings.Contains(m.modals[0].(*textModal).tv.Text(), "\x1b") {
		t.Fatal("escape sequence reached the view")
	}
}

func TestPackageInstallIsNeverReplaced(t *testing.T) {
	calls := 0
	m := updateModel(t, "0.7.0", "", &calls)
	m.exePath = "/opt/homebrew/Cellar/squi/0.6.0/bin/squi"
	drain(m, m.fetchRelease(false))
	drain(m, m.showUpdate())
	v := ansi.Strip(m.View())
	if !strings.Contains(v, "brew upgrade farvisun/tap/squi") || strings.Contains(v, "update now") {
		t.Fatalf("view:\n%s", v)
	}
	key(m, "u")
	if len(m.modals) != 1 || m.modals[0].(*textModal) == nil {
		t.Fatal("u must not start an update on a package install")
	}
}

func TestSelfUpdateAsksFirst(t *testing.T) {
	calls := 0
	m := updateModel(t, "0.7.0", "", &calls)
	m.exePath = filepath.Join(t.TempDir(), "squi")
	if err := os.WriteFile(m.exePath, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	drain(m, m.fetchRelease(false))
	drain(m, m.showUpdate())
	x, y := locate(t, m, "u update now")
	click(m, x, y)
	if _, ok := m.modals[0].(*confirmModal); !ok || len(m.modals) != 1 {
		t.Fatalf("want a confirmation, got %T", m.modals[0])
	}
	if b, _ := os.ReadFile(m.exePath); string(b) != "old" {
		t.Fatal("nothing may change before the confirmation")
	}
	key(m, "n")
	if b, _ := os.ReadFile(m.exePath); string(b) != "old" {
		t.Fatal("no should leave the binary alone")
	}
}

func TestSelfUpdateFailureIsReported(t *testing.T) {
	calls := 0
	m := updateModel(t, "0.7.0", "", &calls)
	m.jobs = 1
	drain(m, m.handleUpdateApplied(updateAppliedMsg{version: "0.7.0", err: errors.New("checksum")}))
	if m.jobs != 0 || !strings.Contains(m.toastText, "checksum") {
		t.Fatalf("jobs %d toast %q", m.jobs, m.toastText)
	}
	m.jobs = 1
	m.updateAvail = &update.Release{Version: "0.7.0"}
	drain(m, m.handleUpdateApplied(updateAppliedMsg{version: "0.7.0"}))
	if m.updateAvail != nil || !strings.Contains(m.toastText, "Restart") {
		t.Fatalf("avail %+v toast %q", m.updateAvail, m.toastText)
	}
}

func TestManagerShowsAvailableUpdate(t *testing.T) {
	m, _ := managerSetup(t)
	m.updater = update.New("0.6.0")
	m.exePath = "/usr/local/bin/squi"
	m.updateAvail = &update.Release{Version: "0.7.0"}
	x, y := locate(t, m, "↑ 0.7.0")
	click(m, x, y)
	if len(m.modals) != 1 {
		t.Fatal("clicking the notice should open the update dialog")
	}
}
