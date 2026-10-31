package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/farvisun/squi/internal/sqltext"
	"github.com/farvisun/squi/internal/ui/components"
	"github.com/farvisun/squi/internal/ui/theme"
)

// withParams asks for the values of the statements' placeholders (:name, $1,
// ?), then runs them with the values filled in. Statements without
// placeholders run right away.
func (m *Model) withParams(t *tab, stmts []sqltext.Statement, opts runOpts) tea.Cmd {
	if !m.settings.QueryParameters {
		return m.execute(t, stmts, opts)
	}
	d := t.ac.driver()
	var names []string
	seen := map[string]bool{}
	for _, st := range stmts {
		for _, n := range sqltext.Params(st.SQL, d) {
			if !seen[n] {
				seen[n] = true
				names = append(names, n)
			}
		}
	}
	if len(names) == 0 {
		return m.execute(t, stmts, opts)
	}
	m.push(newParamsModal(m, names, func(values map[string]string) tea.Cmd {
		bound := make([]sqltext.Statement, len(stmts))
		for i, st := range stmts {
			st.SQL = sqltext.BindParams(st.SQL, d, values)
			bound[i] = st
		}
		return m.execute(t, bound, opts)
	}))
	return nil
}

type paramsModal struct {
	names []string
	form  *components.Form
	run   func(map[string]string) tea.Cmd
	warn  string
}

func newParamsModal(m *Model, names []string, run func(map[string]string) tea.Cmd) *paramsModal {
	f := components.NewForm()
	f.LabelW = 4
	for _, n := range names {
		f.LabelW = max(f.LabelW, len(n)+2)
		f.Text(n, n, m.paramVals[n], "SQL literal: 42, 'text', NULL")
	}
	return &paramsModal{names: names, form: f, run: run}
}

func (p *paramsModal) update(m *Model, msg tea.Msg) (bool, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "esc", "ctrl+c":
			return true, nil
		case "ctrl+s", "ctrl+enter":
			return p.submit(m)
		case "enter":
			if p.form.FocusedKey() == p.names[len(p.names)-1] {
				return p.submit(m)
			}
		}
	}
	p.warn = ""
	return false, p.form.Update(msg)
}

func (p *paramsModal) submit(m *Model) (bool, tea.Cmd) {
	values := map[string]string{}
	for _, n := range p.names {
		v := strings.TrimSpace(p.form.Value(n))
		if v == "" {
			p.warn = fmt.Sprintf("%s has no value (type NULL for a null)", n)
			p.form.FocusKey(n)
			return false, nil
		}
		values[n] = v
	}
	if m.paramVals == nil {
		m.paramVals = map[string]string{}
	}
	for n, v := range values {
		m.paramVals[n] = v
	}
	return true, p.run(values)
}

func (p *paramsModal) view(w, h int) string {
	bw := min(w-4, 80)
	p.form.SetSize(bw-4, min(h-8, 2*len(p.names)+2))
	lines := []string{theme.Dim.Render("Values are inserted as written, so quote text: 'Ada'."), "", p.form.View()}
	if p.warn != "" {
		lines = append(lines, "", theme.Error.Render(p.warn))
	}
	lines = append(lines, "", theme.Key.Render("enter")+" next / run   "+theme.Key.Render("ctrl+s")+" run   "+theme.Key.Render("esc")+" cancel")
	return modalBox("Query parameters", strings.Join(lines, "\n"), bw, theme.Accent)
}
