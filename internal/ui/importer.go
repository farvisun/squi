package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/farvisun/squi/internal/db"
	"github.com/farvisun/squi/internal/ui/components"
	"github.com/farvisun/squi/internal/ui/theme"
)

var nullChoices = []string{"", "NULL", `\N`}

// importModal maps CSV columns to table columns before an import.
type importModal struct {
	hits    []hintHit
	ac      *activeConn
	ref     db.ObjectRef
	path    string
	header  []string
	preview [][]db.Cell
	targets []string // table columns
	mapping []int    // per CSV column: index into targets, -1 = skip
	ix      int
	nullIx  int
	warning string
}

func newImportModal(m *Model, ac *activeConn, ref db.ObjectRef, d *db.TableDetail, path string, header []string, preview [][]db.Cell) *importModal {
	im := &importModal{ac: ac, ref: ref, path: path, header: header, preview: preview}
	for _, c := range d.Columns {
		im.targets = append(im.targets, c.Name)
	}
	im.mapping = make([]int, len(header))
	for i, h := range header {
		im.mapping[i] = -1
		for j, t := range im.targets {
			if strings.EqualFold(strings.TrimSpace(h), t) {
				im.mapping[i] = j
			}
		}
	}
	return im
}

func (im *importModal) update(m *Model, msg tea.Msg) (bool, tea.Cmd) {
	if me, ok := msg.(tea.MouseMsg); ok {
		if k := bodyClick(im.hits, me); k != "" {
			return im.update(m, keyPress(k))
		}
		switch me.Button {
		case tea.MouseButtonWheelUp:
			im.ix = max(im.ix-1, 0)
		case tea.MouseButtonWheelDown:
			im.ix = min(im.ix+1, len(im.header)-1)
		case tea.MouseButtonLeft:
			// the path and a blank line sit above the rows; a click on the
			// selected row moves it to the next target
			if i := me.Y - 1 - 2; i >= 0 && i < len(im.header) {
				if i == im.ix {
					return im.update(m, keyPress("l"))
				}
				im.ix = i
			}
		}
		return false, nil
	}
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return false, nil
	}
	im.warning = ""
	switch km.String() {
	case "esc", "ctrl+c", "q":
		return true, nil
	case "j", "down", "tab":
		im.ix = min(im.ix+1, len(im.header)-1)
	case "k", "up", "shift+tab":
		im.ix = max(im.ix-1, 0)
	case "l", "right", " ":
		im.mapping[im.ix] = (im.mapping[im.ix]+2)%(len(im.targets)+1) - 1
	case "h", "left":
		im.mapping[im.ix] = (im.mapping[im.ix]+len(im.targets)+1)%(len(im.targets)+1) - 1
	case "s":
		im.mapping[im.ix] = -1
	case "n":
		im.nullIx = (im.nullIx + 1) % len(nullChoices)
	case "enter":
		var targets []string
		var srcs []int
		used := map[int]bool{}
		for i, t := range im.mapping {
			if t < 0 {
				continue
			}
			if used[t] {
				im.warning = fmt.Sprintf("Column %q is mapped twice", im.targets[t])
				return false, nil
			}
			used[t] = true
			targets = append(targets, im.targets[t])
			srcs = append(srcs, i)
		}
		if len(targets) == 0 {
			im.warning = "Map at least one column"
			return false, nil
		}
		m.push(&confirmModal{title: "Import CSV",
			body: fmt.Sprintf("Import %s into %s on %s (%d columns) in one transaction?", im.path, im.ref.Name, envLabel(im.ac.cfg), len(targets)),
			onYes: func() tea.Cmd {
				return m.runImport(im.ac, im.ref, im.path, srcs, targets, nullChoices[im.nullIx])
			}})
		return true, nil
	}
	return false, nil
}

func (im *importModal) view(w, h int) string {
	bw := min(w-4, 100)
	var lines []string
	lines = append(lines, theme.Dim.Render(components.Fit(im.path, bw-4)))
	lines = append(lines, "")
	lw := 20
	for i, hname := range im.header {
		sample := ""
		if len(im.preview) > 0 && i < len(im.preview[0]) {
			sample = components.Sanitize(im.preview[0][i].S)
		}
		target := theme.Dim.Render("skip")
		if t := im.mapping[i]; t >= 0 {
			target = theme.OK.Render(im.targets[t])
		}
		line := components.Fit(hname, lw) + " → " + components.FitStyled(target, lw) + "  " + theme.Dim.Render(components.Fit(sample, max(4, bw-2*lw-12)))
		if i == im.ix {
			line = lipgloss.NewStyle().Background(theme.SelBg).Render(components.Fit(stripANSI(line), bw-4))
		}
		lines = append(lines, line)
	}
	null := nullChoices[im.nullIx]
	if null == "" {
		null = "(none: empty stays empty)"
	}
	lines = append(lines, "", "NULL text: "+theme.Bold.Render(null))
	if im.warning != "" {
		lines = append(lines, theme.Error.Render(im.warning))
	}
	footer, hits := renderHints([]hint{{key: "h/l", label: "change target", inert: true}, {key: "s", label: "skip"},
		{key: "n", label: "NULL text"}, {key: "enter", label: "import"}, {key: "esc", label: "cancel"}}, bw-2, false)
	im.hits = offsetHints(hits, lipgloss.Height(strings.Join(lines, "\n"))+1)
	lines = append(lines, "", footer)
	if len(lines) > h-4 {
		lines = lines[:h-4]
	}
	return modalBox("Import into "+im.ref.Name, strings.Join(lines, "\n"), bw, theme.Accent)
}
