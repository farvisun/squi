package components

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// press feeds keys to the editor. Single characters are rune keys; names
// like "esc", "enter", "ctrl+r" map to special keys.
func press(e *Editor, keys ...string) EditorAction {
	var act EditorAction
	for _, k := range keys {
		act = e.Update(keyMsg(k))
	}
	return act
}

func keyMsg(k string) tea.KeyMsg {
	special := map[string]tea.KeyType{
		"esc": tea.KeyEsc, "enter": tea.KeyEnter, "backspace": tea.KeyBackspace, "tab": tea.KeyTab,
		"left": tea.KeyLeft, "right": tea.KeyRight, "up": tea.KeyUp, "down": tea.KeyDown,
		"ctrl+r": tea.KeyCtrlR, "ctrl+z": tea.KeyCtrlZ, "ctrl+y": tea.KeyCtrlY, "ctrl+a": tea.KeyCtrlA,
		"ctrl+x": tea.KeyCtrlX, "ctrl+v": tea.KeyCtrlV, "shift+right": tea.KeyShiftRight, "space": tea.KeySpace,
		"delete": tea.KeyDelete, "home": tea.KeyHome, "end": tea.KeyEnd,
	}
	if t, ok := special[k]; ok {
		return tea.KeyMsg{Type: t}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

func typeText(e *Editor, s string) {
	for _, r := range s {
		if r == '\n' {
			press(e, "enter")
			continue
		}
		e.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

func newVim(text string) *Editor {
	e := NewEditor(true, "postgresql")
	e.SetSize(80, 20)
	e.setLines(text)
	e.cur = pos{0, 0}
	return e
}

func TestInsertAndEscape(t *testing.T) {
	e := NewEditor(true, "postgresql")
	e.SetSize(80, 10)
	if e.Mode() != ModeNormal {
		t.Fatal("vim editor should start in normal mode")
	}
	press(e, "i")
	typeText(e, "select 1\n  from t")
	press(e, "enter")
	typeText(e, "x")
	if got, want := e.Value(), "select 1\n  from t\n  x"; got != want {
		t.Fatalf("auto-indent: got %q want %q", got, want)
	}
	press(e, "esc")
	if e.Mode() != ModeNormal || e.cur.c != 2 {
		t.Fatalf("esc should enter normal mode and step back: mode=%v cur=%v", e.Mode(), e.cur)
	}
}

func TestVimOperators(t *testing.T) {
	cases := []struct {
		name string
		text string
		keys []string
		want string
	}{
		{"dd", "a\nb\nc", []string{"j", "d", "d"}, "a\nc"},
		{"2dd", "a\nb\nc", []string{"2", "d", "d"}, "c"},
		{"dw", "foo bar baz", []string{"d", "w"}, "bar baz"},
		{"cw", "foo bar", []string{"c", "w", "x", "esc"}, "x bar"},
		{"ciw", "foo bar baz", []string{"w", "c", "i", "w", "Q", "esc"}, "foo Q baz"},
		{"diw", "foo bar", []string{"d", "i", "w"}, " bar"},
		{"d$", "foo bar", []string{"w", "d", "$"}, "foo "},
		{"D", "foo bar", []string{"l", "D"}, "f"},
		{"x", "abc", []string{"l", "x"}, "ac"},
		{"3x", "abcdef", []string{"3", "x"}, "def"},
		{"dj", "a\nb\nc", []string{"d", "j"}, "c"},
		{"dG", "a\nb\nc", []string{"j", "d", "G"}, "a"},
		{"dgg", "a\nb\nc", []string{"j", "d", "g", "g"}, "c"},
		{"yyp", "a\nb", []string{"y", "y", "p"}, "a\na\nb"},
		{"yyP", "a\nb", []string{"j", "y", "y", "P"}, "a\nb\nb"},
		{"yw p", "foo bar", []string{"y", "w", "$", "p"}, "foo barfoo "},
		{"o", "a\nb", []string{"o", "x", "esc"}, "a\nx\nb"},
		{"O", "a\nb", []string{"j", "O", "x", "esc"}, "a\nx\nb"},
		{"A", "ab", []string{"A", "c", "esc"}, "abc"},
		{"I", "  ab", []string{"$", "I", "c", "esc"}, "  cab"},
		{"J", "a\n  b", []string{"J"}, "a b"},
		{">>", "a", []string{">", ">"}, "  a"},
		{"<<", "    a", []string{"<", "<"}, "  a"},
		{"r", "abc", []string{"l", "r", "X"}, "aXc"},
		{"u", "abc", []string{"x", "x", "u"}, "bc"},
		{"u ctrl+r", "abc", []string{"x", "u", "ctrl+r"}, "bc"},
		{"insert undo groups", "", []string{"i", "a", "b", "c", "esc", "u"}, ""},
		{"visual d", "hello world", []string{"v", "e", "d"}, " world"},
		{"visual line d", "a\nb\nc", []string{"V", "j", "d"}, "c"},
		{"visual y p", "ab", []string{"v", "y", "$", "p"}, "aba"},
		{"visual >", "a\nb", []string{"V", "j", ">"}, "  a\n  b"},
		{"c$ via C", "foo bar", []string{"w", "C", "x", "esc"}, "foo x"},
		{"cc", "  foo\nbar", []string{"c", "c", "x", "esc"}, "  x\nbar"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newVim(c.text)
			press(e, c.keys...)
			if got := e.Value(); got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestVimMotions(t *testing.T) {
	e := newVim("select a, b\nfrom tab\n\nwhere x = 1")
	press(e, "w")
	if e.cur != (pos{0, 7}) {
		t.Fatalf("w: %v", e.cur)
	}
	press(e, "e") // from the one-letter word "a", e moves to the end of ","
	if e.cur != (pos{0, 8}) {
		t.Fatalf("e: %v", e.cur)
	}
	press(e, "b")
	if e.cur != (pos{0, 7}) {
		t.Fatalf("b: %v", e.cur)
	}
	press(e, "$")
	if e.cur != (pos{0, 10}) {
		t.Fatalf("$: %v", e.cur)
	}
	press(e, "0", "j")
	if e.cur.r != 1 {
		t.Fatalf("j: %v", e.cur)
	}
	press(e, "G")
	if e.cur.r != 3 {
		t.Fatalf("G: %v", e.cur)
	}
	press(e, "g", "g")
	if e.cur != (pos{0, 0}) {
		t.Fatalf("gg: %v", e.cur)
	}
	press(e, "}")
	if e.cur.r != 2 {
		t.Fatalf("}: %v", e.cur)
	}
	press(e, "2", "G")
	if e.cur.r != 1 {
		t.Fatalf("2G: %v", e.cur)
	}
}

func TestLeaderActions(t *testing.T) {
	e := newVim("select 1")
	if act := press(e, " ", "r"); act != ActRun {
		t.Fatalf("space r = %v", act)
	}
	if act := press(e, " ", "a"); act != ActRunAll {
		t.Fatalf("space a = %v", act)
	}
	if act := press(e, ":"); act != ActCommand {
		t.Fatalf(": = %v", act)
	}
	if act := press(e, "y", "y"); act != ActYank || e.Register() != "select 1" {
		t.Fatalf("yy = %v %q", act, e.Register())
	}
}

func TestPlainMode(t *testing.T) {
	e := NewEditor(false, "postgresql")
	e.SetSize(80, 10)
	typeText(e, "hello world")
	press(e, "home", "shift+right", "shift+right", "shift+right", "shift+right", "shift+right")
	a, b, ok := e.Selection()
	if !ok || a != 0 || b != 5 {
		t.Fatalf("selection %d %d %v", a, b, ok)
	}
	press(e, "ctrl+x")
	if e.Value() != " world" || e.Register() != "hello" {
		t.Fatalf("cut: %q %q", e.Value(), e.Register())
	}
	press(e, "end", "ctrl+v")
	if e.Value() != " worldhello" {
		t.Fatalf("paste: %q", e.Value())
	}
	press(e, "ctrl+z")
	if e.Value() != " world" {
		t.Fatalf("undo: %q", e.Value())
	}
	press(e, "ctrl+y")
	if e.Value() != " worldhello" {
		t.Fatalf("redo: %q", e.Value())
	}
	press(e, "ctrl+a")
	typeText(e, "x")
	if e.Value() != "x" {
		t.Fatalf("select all + type: %q", e.Value())
	}
}

func TestOffsetsAndPaste(t *testing.T) {
	e := NewEditor(false, "postgresql")
	e.SetSize(80, 10)
	e.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("é1\r\nab\tc"), Paste: true})
	if e.Value() != "é1\nab    c" {
		t.Fatalf("paste normalisation: %q", e.Value())
	}
	if off := e.CursorOffset(); off != len(e.Value()) {
		t.Fatalf("offset %d want %d", off, len(e.Value()))
	}
	e.SetCursorOffset(len("é"))
	if e.cur != (pos{0, 1}) {
		t.Fatalf("SetCursorOffset multibyte: %v", e.cur)
	}
}

func TestCompletionAccept(t *testing.T) {
	e := NewEditor(false, "postgresql")
	e.SetSize(80, 10)
	typeText(e, "select * from ord")
	start := len("select * from ")
	items := []CompletionItem{{Label: "orders"}, {Label: "order_items"}}

	// auto popup: enter inserts a newline instead of accepting
	e.ShowCompletion(items, start, false)
	press(e, "enter")
	if e.Value() != "select * from ord\n" || e.CompletionOpen() {
		t.Fatalf("enter on auto popup: %q open=%v", e.Value(), e.CompletionOpen())
	}
	press(e, "backspace")

	// explicit popup: tab accepts the selected item
	e.ShowCompletion(items, start, true)
	press(e, "down", "tab")
	if e.Value() != "select * from order_items" {
		t.Fatalf("accept: %q", e.Value())
	}
}

func TestViewRenders(t *testing.T) {
	e := newVim("select 1;\n-- comment\nselect 'x'")
	e.Focus()
	out := e.View()
	if out == "" {
		t.Fatal("empty view")
	}
	e.MarkError(7)
	if e.cur != (pos{0, 7}) {
		t.Fatalf("MarkError moves cursor: %v", e.cur)
	}
	_ = e.View()
}
