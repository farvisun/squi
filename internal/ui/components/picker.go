package components

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sahilm/fuzzy"

	"github.com/farvisun/squi/internal/ui/theme"
)

// PickerItem is an entry in a Picker.
type PickerItem struct {
	Title  string
	Detail string
	Badge  string // rendered (styled) prefix, e.g. an env badge
	Value  any
}

// Picker is a fuzzy-filterable list.
type Picker struct {
	Title   string
	items   []PickerItem
	visible []int
	input   textinput.Model
	ix      int
	top     int
	width   int
	height  int
	// MultiLine shows Detail on a second line (e.g. for SQL history).
	MultiLine bool
}

// NewPicker creates a picker over items.
func NewPicker(title string, items []PickerItem) *Picker {
	ti := textinput.New()
	ti.Prompt = "› "
	ti.Placeholder = "type to filter"
	ti.Focus()
	p := &Picker{Title: title, items: items, input: ti}
	p.refilter()
	return p
}

// SetItems replaces items keeping the query.
func (p *Picker) SetItems(items []PickerItem) { p.items = items; p.refilter() }

// SetSize sets the inner size.
func (p *Picker) SetSize(w, h int) { p.width, p.height = w, h; p.input.Width = w - 4 }

// Query returns the filter text.
func (p *Picker) Query() string { return p.input.Value() }

func (p *Picker) refilter() {
	q := p.input.Value()
	p.visible = p.visible[:0]
	if q == "" {
		for i := range p.items {
			p.visible = append(p.visible, i)
		}
	} else {
		src := make([]string, len(p.items))
		for i, it := range p.items {
			src[i] = it.Title + " " + it.Detail
		}
		for _, m := range fuzzy.Find(q, src) {
			p.visible = append(p.visible, m.Index)
		}
	}
	p.ix = clampInt(p.ix, 0, len(p.visible)-1)
}

// SetIndex highlights the i-th visible item.
func (p *Picker) SetIndex(i int) { p.ix = clampInt(i, 0, len(p.visible)-1) }

// Selected returns the highlighted item.
func (p *Picker) Selected() (PickerItem, bool) {
	if len(p.visible) == 0 {
		return PickerItem{}, false
	}
	return p.items[p.visible[p.ix]], true
}

// Update handles a key. done is true when the user chose (ok) or cancelled.
func (p *Picker) Update(msg tea.Msg) (done, ok bool, cmd tea.Cmd) {
	if km, isKey := msg.(tea.KeyMsg); isKey {
		switch km.String() {
		case "enter":
			_, has := p.Selected()
			return true, has, nil
		case "esc", "ctrl+c":
			return true, false, nil
		case "up", "ctrl+p", "ctrl+k", "shift+tab":
			p.ix = max(0, p.ix-1)
			return false, false, nil
		case "down", "ctrl+n", "ctrl+j", "tab":
			p.ix = min(len(p.visible)-1, p.ix+1)
			return false, false, nil
		case "pgup":
			p.ix = max(0, p.ix-p.rowsPerPage())
			return false, false, nil
		case "pgdown":
			p.ix = min(len(p.visible)-1, p.ix+p.rowsPerPage())
			return false, false, nil
		}
	}
	before := p.input.Value()
	p.input, cmd = p.input.Update(msg)
	if p.input.Value() != before {
		p.ix = 0
		p.refilter()
	}
	return false, false, cmd
}

func (p *Picker) rowsPerPage() int {
	per := 1
	if p.MultiLine {
		per = 2
	}
	return max(1, (p.height-2)/per)
}

// View renders the picker content (without border).
func (p *Picker) View() string {
	lines := []string{p.input.View(), theme.Dim.Render(strings.Repeat("─", max(0, p.width)))}
	n := p.rowsPerPage()
	if p.ix < p.top {
		p.top = p.ix
	}
	if p.ix >= p.top+n {
		p.top = p.ix - n + 1
	}
	for i := p.top; i < len(p.visible) && i < p.top+n; i++ {
		it := p.items[p.visible[i]]
		sel := i == p.ix
		prefix := "  "
		if sel {
			prefix = theme.Key.Render("▌ ")
		}
		title := Sanitize(it.Title)
		if it.Badge != "" {
			title = it.Badge + " " + title
		}
		st := lipgloss.NewStyle()
		if sel {
			st = st.Bold(true).Foreground(theme.Accent)
		}
		if p.MultiLine {
			lines = append(lines, prefix+FitStyled(st.Render(title), p.width-2))
			lines = append(lines, "  "+theme.Dim.Render(Fit(Sanitize(it.Detail), p.width-2)))
			continue
		}
		titleW := p.width - 2
		detail := ""
		if it.Detail != "" {
			dw := min(len(it.Detail)+2, p.width/2)
			titleW -= dw
			detail = theme.Dim.Render(Fit("  "+Sanitize(it.Detail), dw))
		}
		lines = append(lines, prefix+FitStyled(st.Render(title), titleW)+detail)
	}
	if len(p.visible) == 0 {
		lines = append(lines, theme.Dim.Render("  no matches"))
	}
	return strings.Join(lines, "\n")
}

// Items returns all items (unfiltered).
func (p *Picker) Items() []PickerItem { return p.items }
