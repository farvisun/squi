package ui

import (
	"context"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/farvisun/squi/internal/datagrip"
	"github.com/farvisun/squi/internal/db"
	"github.com/farvisun/squi/internal/ui/components"
	"github.com/farvisun/squi/internal/ui/theme"
)

// consolePicker lists the DataGrip query consoles and project files of the
// tab's connection, read live from DataGrip's files; the picked one opens
// in a new tab without running.
func (m *Model) consolePicker(t *tab) tea.Cmd {
	o := t.ac.cfg.Origin
	if o == nil || o.Tool != datagrip.Tool {
		return m.toast("This connection was not imported from DataGrip — see squi import datagrip", toastWarn)
	}
	qs := datagrip.Queries(m.dataGripRoot, o.ID, o.Source)
	if len(qs) == 0 {
		return m.toast("DataGrip has no consoles or query files for "+t.ac.cfg.Title(), toastInfo)
	}
	var items []components.PickerItem
	for _, q := range qs {
		kind := "file"
		if q.Console {
			kind = "console"
		}
		title := q.Title
		if pre := sqlPreview(q.Path); pre != "" {
			title += " · " + pre
		}
		items = append(items, components.PickerItem{Title: title,
			Detail: kind + " · " + q.Modified.Format("2006-01-02 15:04"), Value: q})
	}
	m.push(newPickerModal("DataGrip queries · "+t.ac.cfg.Title(), items, func(it components.PickerItem) tea.Cmd {
		q := it.Value.(datagrip.Query)
		b, err := os.ReadFile(q.Path)
		if err != nil {
			return m.toast(err.Error(), toastError)
		}
		nt := m.createTab(t.ac)
		nt.db, nt.label = t.db, q.Title
		nt.editor.SetValue(string(b))
		return m.op(nt, "opening session", func(ctx context.Context, s db.Session) tea.Msg { return nil })
	}))
	return nil
}

// sqlPreview returns the first line of a SQL file that is not blank or a
// comment, shortened.
func sqlPreview(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 4096)
	n, _ := f.Read(buf)
	for _, l := range strings.Split(string(buf[:n]), "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "--") && !strings.HasPrefix(l, "#") {
			return abbrev(oneLine(l), 60)
		}
	}
	return ""
}

// syncDataGrip previews an import from DataGrip and applies it once
// confirmed.
func (c *connManager) syncDataGrip(m *Model) tea.Cmd {
	opts := datagrip.Options{Root: m.dataGripRoot, DryRun: true}
	rep, _, err := datagrip.Import(m.store, opts)
	if err != nil {
		c.status = theme.Error.Render("DataGrip: " + err.Error())
		return nil
	}
	if !rep.Changed() {
		c.status = theme.OK.Render("DataGrip is in sync — " + rep.Summary())
		return nil
	}
	lines := strings.Split(rep.Details(), "\n")
	if len(lines) > 18 {
		lines = append(lines[:18], fmt.Sprintf("… %d more lines (squi import datagrip --dry-run)", len(lines)-18))
	}
	m.push(&confirmModal{title: "Import from DataGrip",
		body: rep.Summary() + "\n\n" + strings.Join(lines, "\n") + "\n\nApply? Passwords stay in DataGrip's keychain entries.",
		onYes: func() tea.Cmd {
			opts.DryRun = false
			rep, _, err := datagrip.Import(m.store, opts)
			if err != nil {
				c.status = theme.Error.Render("DataGrip: " + err.Error())
				return nil
			}
			m.reloadConns()
			c.status = theme.OK.Render("Imported from DataGrip: " + rep.Summary())
			return nil
		}})
	return nil
}
