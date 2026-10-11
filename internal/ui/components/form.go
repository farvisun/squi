package components

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/farvisun/squi/internal/ui/theme"
)

// FieldKind is the type of a form field.
type FieldKind int

const (
	FieldText FieldKind = iota
	FieldPassword
	FieldNumber
	FieldSelect
	FieldBool
	FieldSection
)

// Field is one row of a Form.
type Field struct {
	Key     string
	Label   string
	Kind    FieldKind
	Options []string
	Hint    string
	// Visible hides the field when it returns false.
	Visible func() bool

	input textinput.Model
	on    bool
	sel   int
}

// Form is a vertical list of labelled inputs.
type Form struct {
	fields []*Field
	ix     int
	top    int
	width  int
	height int
	LabelW int
}

// NewForm creates an empty form.
func NewForm() *Form { return &Form{LabelW: 18} }

// Section adds a heading.
func (f *Form) Section(label string) *Field {
	return f.add(&Field{Label: label, Kind: FieldSection})
}

// Text adds a text input.
func (f *Form) Text(key, label, value, placeholder string) *Field {
	return f.add(&Field{Key: key, Label: label, Kind: FieldText, input: newInput(value, placeholder, false)})
}

// Password adds a masked input.
func (f *Form) Password(key, label, value, placeholder string) *Field {
	return f.add(&Field{Key: key, Label: label, Kind: FieldPassword, input: newInput(value, placeholder, true)})
}

// Number adds a numeric input.
func (f *Form) Number(key, label string, value int, placeholder string) *Field {
	v := ""
	if value != 0 {
		v = strconv.Itoa(value)
	}
	fl := &Field{Key: key, Label: label, Kind: FieldNumber, input: newInput(v, placeholder, false)}
	fl.input.Validate = func(s string) error {
		if s == "" {
			return nil
		}
		_, err := strconv.Atoi(s)
		return err
	}
	return f.add(fl)
}

// Select adds a choice among options.
func (f *Form) Select(key, label string, options []string, value string) *Field {
	fl := &Field{Key: key, Label: label, Kind: FieldSelect, Options: options}
	for i, o := range options {
		if o == value {
			fl.sel = i
		}
	}
	return f.add(fl)
}

// Bool adds a toggle.
func (f *Form) Bool(key, label string, value bool) *Field {
	return f.add(&Field{Key: key, Label: label, Kind: FieldBool, on: value})
}

func newInput(value, placeholder string, masked bool) textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Placeholder = placeholder
	ti.SetValue(value)
	if masked {
		ti.EchoMode = textinput.EchoPassword
		ti.EchoCharacter = '•'
	}
	return ti
}

func (f *Form) add(fl *Field) *Field {
	f.fields = append(f.fields, fl)
	if fl.Kind != FieldSection && f.current() == nil {
		f.ix = len(f.fields) - 1
	}
	f.syncFocus()
	return fl
}

func (f *Form) field(key string) *Field {
	for _, fl := range f.fields {
		if fl.Key == key {
			return fl
		}
	}
	return nil
}

// Value returns a field value (text, number as text, selected option, or "true"/"false").
func (f *Form) Value(key string) string {
	fl := f.field(key)
	if fl == nil {
		return ""
	}
	switch fl.Kind {
	case FieldSelect:
		if len(fl.Options) == 0 {
			return ""
		}
		return fl.Options[fl.sel]
	case FieldBool:
		return strconv.FormatBool(fl.on)
	}
	return strings.TrimSpace(fl.input.Value())
}

// Int returns a numeric field value (0 if empty/invalid).
func (f *Form) Int(key string) int { n, _ := strconv.Atoi(f.Value(key)); return n }

// Bool returns a toggle value.
func (f *Form) BoolValue(key string) bool { return f.Value(key) == "true" }

// SetValue sets a text field value.
func (f *Form) SetValue(key, v string) {
	if fl := f.field(key); fl != nil {
		fl.input.SetValue(v)
	}
}

// FocusKey focuses the field with key.
func (f *Form) FocusKey(key string) {
	for i, fl := range f.fields {
		if fl.Key == key {
			f.ix = i
		}
	}
	f.syncFocus()
}

// SetSize sets the inner size.
func (f *Form) SetSize(w, h int) {
	f.width, f.height = w, h
	for _, fl := range f.fields {
		fl.input.Width = max(4, w-f.LabelW-4)
	}
}

// FocusedKey returns the key of the focused field ("" if none).
func (f *Form) FocusedKey() string {
	if fl := f.current(); fl != nil {
		return fl.Key
	}
	return ""
}

func (f *Form) current() *Field {
	if f.ix < 0 || f.ix >= len(f.fields) || f.fields[f.ix].Kind == FieldSection {
		return nil
	}
	return f.fields[f.ix]
}

func (fl *Field) visible() bool { return fl.Visible == nil || fl.Visible() }

func (f *Form) move(d int) {
	for i := f.ix + d; i >= 0 && i < len(f.fields); i += d {
		if f.fields[i].Kind != FieldSection && f.fields[i].visible() {
			f.ix = i
			break
		}
	}
	f.syncFocus()
}

func (f *Form) syncFocus() {
	for i, fl := range f.fields {
		if i == f.ix && (fl.Kind == FieldText || fl.Kind == FieldPassword || fl.Kind == FieldNumber) {
			fl.input.Focus()
		} else {
			fl.input.Blur()
		}
	}
}

// Update handles a message for the focused field.
func (f *Form) Update(msg tea.Msg) tea.Cmd {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "up", "shift+tab", "ctrl+p":
			f.move(-1)
			return nil
		case "down", "tab", "ctrl+n", "enter":
			f.move(1)
			return nil
		}
		fl := f.current()
		if fl == nil {
			return nil
		}
		switch fl.Kind {
		case FieldBool:
			if km.String() == " " || km.String() == "left" || km.String() == "right" || km.String() == "x" {
				fl.on = !fl.on
			}
			return nil
		case FieldSelect:
			switch km.String() {
			case "left", "h":
				fl.sel = (fl.sel - 1 + len(fl.Options)) % len(fl.Options)
			case "right", "l", " ":
				fl.sel = (fl.sel + 1) % len(fl.Options)
			}
			return nil
		}
	}
	fl := f.current()
	if fl == nil {
		return nil
	}
	var cmd tea.Cmd
	fl.input, cmd = fl.input.Update(msg)
	return cmd
}

// View renders the form.
func (f *Form) View() string {
	var lines []string
	cursorLine := 0
	for i, fl := range f.fields {
		if !fl.visible() {
			continue
		}
		if fl.Kind == FieldSection {
			if len(lines) > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, theme.Title.Render(fl.Label))
			continue
		}
		focused := i == f.ix
		label := Fit(fl.Label, f.LabelW)
		ls := theme.Dim
		if focused {
			ls = lipgloss.NewStyle().Foreground(theme.Accent).Bold(true)
			cursorLine = len(lines)
		}
		var val string
		switch fl.Kind {
		case FieldBool:
			if fl.on {
				val = theme.OK.Render("[x]")
			} else {
				val = "[ ]"
			}
		case FieldSelect:
			parts := make([]string, len(fl.Options))
			for j, o := range fl.Options {
				if o == "" {
					o = "none"
				}
				if j == fl.sel {
					parts[j] = theme.ActiveTab.Render(o)
				} else {
					parts[j] = theme.Dim.Render(" " + o + " ")
				}
			}
			val = strings.Join(parts, "")
		default:
			val = fl.input.View()
		}
		marker := "  "
		if focused {
			marker = theme.Key.Render("› ")
		}
		line := marker + ls.Render(label) + " " + val
		if focused && fl.Hint != "" {
			line += "  " + theme.Dim.Render(fl.Hint)
		}
		lines = append(lines, line)
	}
	if cursorLine < f.top {
		f.top = cursorLine
	}
	if f.height > 0 && cursorLine >= f.top+f.height {
		f.top = cursorLine - f.height + 1
	}
	end := min(len(lines), f.top+f.height)
	if f.height <= 0 {
		end = len(lines)
	}
	return strings.Join(lines[min(f.top, len(lines)):end], "\n")
}
