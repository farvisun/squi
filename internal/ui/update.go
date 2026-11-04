package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/farvisun/squi/internal/ui/components"
	"github.com/farvisun/squi/internal/ui/theme"
	"github.com/farvisun/squi/internal/update"
)

const updateCheckTimeout = 5 * time.Second

// updateCheckedMsg reports a finished check. Manual checks were asked for with
// :update, so they answer even when nothing is new or the check failed.
type updateCheckedMsg struct {
	release update.Release
	err     error
	manual  bool
}

// updateAppliedMsg reports a finished self-update.
type updateAppliedMsg struct {
	version string
	err     error
}

func (m *Model) updateStatePath() string {
	return filepath.Join(m.store.StateDir, "update.json")
}

// autoUpdateCheck runs at startup. It shows what an earlier check found, and
// calls out at most once per update.CheckEvery. It never blocks startup, and
// released builds are the only ones that check.
func (m *Model) autoUpdateCheck() tea.Cmd {
	if m.updater == nil || m.store == nil {
		return nil
	}
	update.Cleanup(m.exePath)
	if !m.settings.CheckUpdates || os.Getenv("SQUI_NO_UPDATE_CHECK") != "" {
		return nil
	}
	st := update.LoadState(m.updateStatePath())
	m.offerUpdate(st.Latest, st.Skipped)
	if time.Since(st.CheckedAt) < update.CheckEvery {
		return nil
	}
	return m.fetchRelease(false)
}

func (m *Model) fetchRelease(manual bool) tea.Cmd {
	u := m.updater
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), updateCheckTimeout)
		defer cancel()
		rel, err := u.Latest(ctx)
		return updateCheckedMsg{release: rel, err: err, manual: manual}
	}
}

// offerUpdate remembers rel if it is newer than this build and not skipped.
func (m *Model) offerUpdate(rel update.Release, skipped string) bool {
	if rel.Version == "" || !update.Newer(m.updater.Current, rel.Version) || rel.Version == skipped {
		return false
	}
	m.updateAvail = &rel
	return true
}

func (m *Model) handleUpdateChecked(msg updateCheckedMsg) tea.Cmd {
	if msg.err != nil {
		if msg.manual {
			return m.toast("Update check failed: "+msg.err.Error(), toastError)
		}
		return nil // offline or firewalled: try again next start
	}
	st := update.LoadState(m.updateStatePath())
	st.CheckedAt, st.Latest = time.Now(), msg.release
	_ = update.SaveState(m.updateStatePath(), st)
	if msg.manual {
		st.Skipped = "" // asking again overrides an earlier skip
	}
	if !m.offerUpdate(msg.release, st.Skipped) {
		if msg.manual {
			return m.toast("squi "+m.updater.Current+" is the latest version", toastInfo)
		}
		return nil
	}
	if msg.manual {
		return m.showUpdate()
	}
	return m.toast("squi "+msg.release.Version+" is available: :update", toastInfo)
}

// checkForUpdate is the :update command.
func (m *Model) checkForUpdate() tea.Cmd {
	if m.updater == nil {
		return m.toast("This build has no release version to compare (:update needs a released squi)", toastWarn)
	}
	return tea.Batch(m.fetchRelease(true), m.toast("Checking for updates…", toastInfo))
}

// showUpdate opens the dialog for the available release.
func (m *Model) showUpdate() tea.Cmd {
	rel := m.updateAvail
	if rel == nil {
		return nil
	}
	inst := update.Detect(m.exePath)
	lines := []string{
		"Installed  " + m.updater.Current + " (" + inst.Name + ")",
		"Latest     " + rel.Version,
	}
	if inst.Command != "" {
		lines = append(lines, "", "Update with:", "  "+inst.Command)
	} else if !inst.SelfUpdates() {
		lines = append(lines, "", "Install the new package from the release page.")
	}
	if rel.Notes != "" {
		lines = append(lines, "", "What's new")
		// release text is shown as data: no escape sequences reach the terminal
		for _, l := range strings.Split(strings.ReplaceAll(rel.Notes, "\r\n", "\n"), "\n") {
			lines = append(lines, components.Sanitize(l))
		}
	}
	tm := newTextModal("squi "+rel.Version+" is available", strings.Join(lines, "\n"), "")
	hints := []hint{}
	switch {
	case inst.SelfUpdates():
		hints = append(hints, hint{key: "u", label: "update now"})
	case inst.Command != "":
		hints = append(hints, hint{key: "c", label: "copy command"})
	}
	tm.hints = append(hints, hint{key: "l", label: "copy link"}, hint{key: "s", label: "skip this version"}, hint{key: "esc", label: "later"})
	tm.onKey = func(k string) (bool, tea.Cmd) {
		switch k {
		case "u":
			if inst.SelfUpdates() {
				return true, m.confirmSelfUpdate(*rel)
			}
		case "c":
			if inst.Command != "" {
				return false, m.copy(inst.Command, "update command")
			}
		case "l":
			return false, m.copy(rel.URL, "release link")
		case "s":
			m.skipUpdate(rel.Version)
			return true, nil
		}
		return false, nil
	}
	m.push(tm)
	return nil
}

func (m *Model) skipUpdate(version string) {
	st := update.LoadState(m.updateStatePath())
	st.Skipped = version
	_ = update.SaveState(m.updateStatePath(), st)
	m.updateAvail = nil
}

// confirmSelfUpdate asks before anything is downloaded or replaced.
func (m *Model) confirmSelfUpdate(rel update.Release) tea.Cmd {
	m.push(&confirmModal{title: "Update squi",
		body: "Download squi " + rel.Version + ", verify its checksum and replace " + m.exePath + "?\n" +
			"This session keeps running the current version until you restart.",
		onYes: func() tea.Cmd { return m.applyUpdate(rel) }})
	return nil
}

func (m *Model) applyUpdate(rel update.Release) tea.Cmd {
	u, exe := m.updater, m.exePath
	m.jobs++
	return tea.Batch(m.spinner.Tick, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		return updateAppliedMsg{version: rel.Version, err: u.Apply(ctx, rel, exe)}
	})
}

func (m *Model) handleUpdateApplied(msg updateAppliedMsg) tea.Cmd {
	m.jobs--
	t := m.currentTab()
	if msg.err != nil {
		if t != nil {
			t.out.msgs.log(logError, "Update failed: "+msg.err.Error())
		}
		return m.toast("Update failed: "+msg.err.Error(), toastError)
	}
	m.updateAvail = nil
	text := "Updated to squi " + msg.version + ". Restart squi to use it"
	if t != nil {
		t.out.msgs.log(logOK, text)
	}
	return m.toast(text, toastInfo)
}

// updateBadge is the status bar's notice of an available release.
func (m *Model) updateBadge() string {
	return theme.Key.Render("↑ " + m.updateAvail.Version)
}
