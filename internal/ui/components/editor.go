package components

import (
	"strconv"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/farvisun/squi/internal/ui/theme"
)

// EditorMode is the vim mode of the editor.
type EditorMode int

const (
	ModeInsert EditorMode = iota
	ModeNormal
	ModeVisual
	ModeVisualLine
)

func (m EditorMode) String() string {
	switch m {
	case ModeNormal:
		return "NORMAL"
	case ModeVisual:
		return "VISUAL"
	case ModeVisualLine:
		return "V-LINE"
	}
	return "INSERT"
}

// EditorAction is a request from the editor to its host.
type EditorAction int

const (
	ActNone EditorAction = iota
	ActRun
	ActRunAll
	ActExplain
	ActHistory
	ActComplete       // open completion for the word under the cursor
	ActCompleteUpdate // text changed while completion is open
	ActCommand        // ':' pressed in normal mode
	ActYank           // text was copied into the register (host may copy to clipboard)
	ActEscape         // escape pressed with nothing left to cancel
	ActSnippets
)

// CompletionItem is an entry in the completion popup.
type CompletionItem struct {
	Label  string
	Insert string
	Kind   string
	Detail string
}

type pos struct{ r, c int }

func (a pos) less(b pos) bool { return a.r < b.r || (a.r == b.r && a.c < b.c) }

type snapshot struct {
	lines [][]rune
	cur   pos
}

// Editor is a multi-line SQL editor with optional vim key bindings.
type Editor struct {
	lines   [][]rune
	cur     pos
	wantCol int
	top     int
	left    int
	width   int
	height  int
	focused bool

	Vim         bool
	mode        EditorMode
	anchor      pos
	selecting   bool // shift-selection in non-vim mode
	pending     string
	gOp         string // operator waiting for "gg"
	count       int
	register    string
	regLinewise bool

	undo     []snapshot
	redo     []snapshot
	inInsert bool // an undo snapshot was taken for the current insert session

	lexer   string
	hl      [][]uint8
	dirtyHL bool

	errPos   *pos
	compl    []CompletionItem
	complIx  int
	complAt  pos // start of the replaced word
	complOn  bool
	complNav bool // user picked an item explicitly; enter accepts

	// Placeholder is shown when the buffer is empty.
	Placeholder string
}

// NewEditor creates an editor. vim selects vim key bindings.
func NewEditor(vim bool, lexer string) *Editor {
	e := &Editor{lines: [][]rune{{}}, Vim: vim, lexer: lexer, dirtyHL: true}
	if vim {
		e.mode = ModeNormal
	}
	return e
}

// SetSize sets the outer size of the editor.
func (e *Editor) SetSize(w, h int) { e.width, e.height = w, h; e.scroll() }

// Focus gives the editor keyboard focus.
func (e *Editor) Focus() { e.focused = true }

// Blur removes keyboard focus.
func (e *Editor) Blur() { e.focused = false; e.complOn = false }

// Focused reports focus.
func (e *Editor) Focused() bool { return e.focused }

// Mode returns the current vim mode.
func (e *Editor) Mode() EditorMode { return e.mode }

// Inserting reports whether typed characters are inserted as text.
func (e *Editor) Inserting() bool { return !e.Vim || e.mode == ModeInsert }

// SetLexer changes the highlighting lexer.
func (e *Editor) SetLexer(l string) { e.lexer = l; e.dirtyHL = true }

// Value returns the buffer text.
func (e *Editor) Value() string {
	parts := make([]string, len(e.lines))
	for i, l := range e.lines {
		parts[i] = string(l)
	}
	return strings.Join(parts, "\n")
}

// SetValue replaces the buffer (undoable) and moves the cursor to the end.
func (e *Editor) SetValue(s string) {
	e.pushUndo()
	e.setLines(s)
	e.cur = pos{len(e.lines) - 1, len(e.lines[len(e.lines)-1])}
	e.clampCursor()
	e.changed()
}

// InsertText inserts text at the cursor (undoable).
func (e *Editor) InsertText(s string) {
	e.pushUndo()
	e.cur = e.insertAt(e.cur, s)
	e.changed()
}

func (e *Editor) setLines(s string) {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\t", "    ")
	raw := strings.Split(s, "\n")
	e.lines = make([][]rune, len(raw))
	for i, l := range raw {
		e.lines[i] = []rune(l)
	}
}

// CursorOffset returns the byte offset of the cursor in Value().
func (e *Editor) CursorOffset() int { return e.offsetOf(e.cur) }

func (e *Editor) offsetOf(p pos) int {
	off := 0
	for i := 0; i < p.r && i < len(e.lines); i++ {
		off += len(string(e.lines[i])) + 1
	}
	if p.r < len(e.lines) {
		c := p.c
		if c > len(e.lines[p.r]) {
			c = len(e.lines[p.r])
		}
		off += len(string(e.lines[p.r][:c]))
	}
	return off
}

func (e *Editor) posOf(off int) pos {
	for r, l := range e.lines {
		n := len(string(l))
		if off <= n {
			c := len([]rune(string(l)[:clampInt(off, 0, n)]))
			return pos{r, c}
		}
		off -= n + 1
	}
	last := len(e.lines) - 1
	return pos{last, len(e.lines[last])}
}

// SetCursorOffset moves the cursor to a byte offset.
func (e *Editor) SetCursorOffset(off int) {
	e.cur = e.posOf(off)
	e.clampCursor()
	e.scroll()
}

// Selection returns the selected byte range, if any.
func (e *Editor) Selection() (start, end int, ok bool) {
	if !e.hasSelection() {
		return 0, 0, false
	}
	a, b := e.selRange()
	return e.offsetOf(a), e.offsetOf(b), true
}

// MarkError highlights the error position (byte offset); -1 clears.
func (e *Editor) MarkError(off int) {
	if off < 0 {
		e.errPos = nil
		return
	}
	p := e.posOf(off)
	e.errPos = &p
	e.cur = p
	e.clampCursor()
	e.scroll()
}

// ShowCompletion opens the completion popup replacing [start, cursor).
// For popups opened automatically (explicit=false), Enter inserts a newline
// unless the user moved through the list first.
func (e *Editor) ShowCompletion(items []CompletionItem, start int, explicit bool) {
	if len(items) == 0 {
		e.complOn = false
		return
	}
	if !e.complOn {
		e.complNav = explicit
	}
	e.complNav = e.complNav || explicit
	e.compl, e.complIx, e.complAt, e.complOn = items, 0, e.posOf(start), true
}

// CompletionOpen reports whether the popup is visible.
func (e *Editor) CompletionOpen() bool { return e.complOn }

// CloseCompletion hides the popup.
func (e *Editor) CloseCompletion() { e.complOn = false }

func (e *Editor) acceptCompletion() {
	it := e.compl[e.complIx]
	ins := it.Insert
	if ins == "" {
		ins = it.Label
	}
	e.complOn = false
	start := e.complAt
	if start.r != e.cur.r || e.cur.less(start) {
		start = e.cur
	}
	e.pushUndo()
	e.deleteRange(start, e.cur)
	e.cur = e.insertAt(start, ins)
	e.changed()
}

// Update handles a key and reports what the host should do.
func (e *Editor) Update(msg tea.KeyMsg) EditorAction {
	if e.complOn {
		switch msg.String() {
		case "enter":
			if !e.complNav {
				e.complOn = false
				break
			}
			e.acceptCompletion()
			return ActNone
		case "tab":
			e.acceptCompletion()
			return ActNone
		case "down", "ctrl+n":
			e.complIx = (e.complIx + 1) % len(e.compl)
			e.complNav = true
			return ActNone
		case "up", "ctrl+p":
			e.complIx = (e.complIx - 1 + len(e.compl)) % len(e.compl)
			e.complNav = true
			return ActNone
		case "esc":
			e.complOn = false
			return ActNone
		}
	}
	var act EditorAction
	if e.Inserting() {
		act = e.insertKey(msg)
	} else {
		act = e.normalKey(msg)
	}
	e.clampCursor()
	e.scroll()
	return act
}

func (e *Editor) changed() {
	e.dirtyHL = true
	e.errPos = nil
	e.redo = nil
}

func (e *Editor) pushUndo() {
	cp := make([][]rune, len(e.lines))
	for i, l := range e.lines {
		cp[i] = append([]rune(nil), l...)
	}
	e.undo = append(e.undo, snapshot{cp, e.cur})
	if len(e.undo) > 300 {
		e.undo = e.undo[1:]
	}
}

func (e *Editor) restore(from, to *[]snapshot) {
	if len(*from) == 0 {
		return
	}
	cp := make([][]rune, len(e.lines))
	for i, l := range e.lines {
		cp[i] = append([]rune(nil), l...)
	}
	*to = append(*to, snapshot{cp, e.cur})
	s := (*from)[len(*from)-1]
	*from = (*from)[:len(*from)-1]
	e.lines, e.cur = s.lines, s.cur
	e.dirtyHL = true
	e.clampCursor()
}

func (e *Editor) undoOnce() { e.restore(&e.undo, &e.redo) }
func (e *Editor) redoOnce() { e.restore(&e.redo, &e.undo) }

// ---- text primitives ----

func (e *Editor) insertAt(p pos, s string) pos {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	s = strings.ReplaceAll(s, "\t", "    ")
	parts := strings.Split(s, "\n")
	line := e.lines[p.r]
	head := append([]rune(nil), line[:p.c]...)
	tail := append([]rune(nil), line[p.c:]...)
	if len(parts) == 1 {
		nl := append(head, []rune(parts[0])...)
		end := pos{p.r, len(nl)}
		e.lines[p.r] = append(nl, tail...)
		return end
	}
	newLines := make([][]rune, 0, len(parts))
	newLines = append(newLines, append(head, []rune(parts[0])...))
	for _, mid := range parts[1 : len(parts)-1] {
		newLines = append(newLines, []rune(mid))
	}
	last := []rune(parts[len(parts)-1])
	end := pos{p.r + len(parts) - 1, len(last)}
	newLines = append(newLines, append(last, tail...))
	rest := append([][]rune(nil), e.lines[p.r+1:]...)
	e.lines = append(append(e.lines[:p.r], newLines...), rest...)
	return end
}

func (e *Editor) textRange(a, b pos) string {
	if b.less(a) {
		a, b = b, a
	}
	if a.r == b.r {
		return string(e.lines[a.r][a.c:b.c])
	}
	var sb strings.Builder
	sb.WriteString(string(e.lines[a.r][a.c:]))
	for r := a.r + 1; r < b.r; r++ {
		sb.WriteByte('\n')
		sb.WriteString(string(e.lines[r]))
	}
	sb.WriteByte('\n')
	sb.WriteString(string(e.lines[b.r][:b.c]))
	return sb.String()
}

func (e *Editor) deleteRange(a, b pos) {
	if b.less(a) {
		a, b = b, a
	}
	head := append([]rune(nil), e.lines[a.r][:a.c]...)
	tail := e.lines[b.r][b.c:]
	e.lines[a.r] = append(head, tail...)
	if b.r > a.r {
		e.lines = append(e.lines[:a.r+1], e.lines[b.r+1:]...)
	}
	e.cur = a
}

func (e *Editor) deleteLines(from, to int) string {
	if to < from {
		from, to = to, from
	}
	parts := make([]string, 0, to-from+1)
	for r := from; r <= to; r++ {
		parts = append(parts, string(e.lines[r]))
	}
	e.lines = append(e.lines[:from], e.lines[to+1:]...)
	if len(e.lines) == 0 {
		e.lines = [][]rune{{}}
	}
	e.cur = pos{clampInt(from, 0, len(e.lines)-1), 0}
	return strings.Join(parts, "\n")
}

func (e *Editor) clampCursor() {
	e.cur.r = clampInt(e.cur.r, 0, len(e.lines)-1)
	max := len(e.lines[e.cur.r])
	if e.Vim && e.mode != ModeInsert && max > 0 {
		max--
	}
	e.cur.c = clampInt(e.cur.c, 0, max)
}

func (e *Editor) gutterWidth() int { return len(strconv.Itoa(len(e.lines))) + 1 }

func (e *Editor) textWidth() int {
	w := e.width - e.gutterWidth()
	if w < 1 {
		w = 1
	}
	return w
}

func (e *Editor) scroll() {
	if e.height <= 0 {
		return
	}
	if e.cur.r < e.top {
		e.top = e.cur.r
	}
	if e.cur.r >= e.top+e.height {
		e.top = e.cur.r - e.height + 1
	}
	dc := displayCol(e.lines[e.cur.r], e.cur.c)
	tw := e.textWidth()
	if dc < e.left {
		e.left = dc
	}
	if dc >= e.left+tw {
		e.left = dc - tw + 1
	}
}

func displayCol(line []rune, c int) int {
	w := 0
	for i := 0; i < c && i < len(line); i++ {
		w += runewidth.RuneWidth(line[i])
	}
	return w
}

// ---- insert mode ----

func (e *Editor) startInsert() {
	if !e.inInsert {
		e.pushUndo()
		e.inInsert = true
	}
}

func (e *Editor) insertKey(msg tea.KeyMsg) EditorAction {
	k := msg.String()
	if !e.Vim {
		if act, ok := e.plainShortcut(k); ok {
			return act
		}
	}
	switch k {
	case "esc":
		if e.Vim {
			e.mode = ModeNormal
			e.inInsert = false
			if e.cur.c > 0 {
				e.cur.c--
			}
			return ActNone
		}
		if e.selecting {
			e.selecting = false
			return ActNone
		}
		return ActEscape
	case "enter":
		e.replaceSelection()
		e.startInsert()
		indent := leadingSpace(e.lines[e.cur.r])
		e.cur = e.insertAt(e.cur, "\n"+indent)
		e.changed()
	case "backspace", "ctrl+h":
		e.startInsert()
		if e.replaceSelection() {
			break
		}
		if e.cur.c > 0 {
			e.deleteRange(pos{e.cur.r, e.cur.c - 1}, e.cur)
		} else if e.cur.r > 0 {
			pl := len(e.lines[e.cur.r-1])
			e.deleteRange(pos{e.cur.r - 1, pl}, e.cur)
		}
		e.changed()
		if e.complOn {
			return ActCompleteUpdate
		}
	case "delete":
		e.startInsert()
		if e.replaceSelection() {
			break
		}
		if e.cur.c < len(e.lines[e.cur.r]) {
			e.deleteRange(e.cur, pos{e.cur.r, e.cur.c + 1})
		} else if e.cur.r < len(e.lines)-1 {
			e.deleteRange(e.cur, pos{e.cur.r + 1, 0})
		}
		e.changed()
	case "alt+backspace":
		e.startInsert()
		start := e.wordStartBack(e.cur)
		e.deleteRange(start, e.cur)
		e.changed()
	case "ctrl+u":
		e.startInsert()
		e.deleteRange(pos{e.cur.r, 0}, e.cur)
		e.changed()
	case "tab":
		if e.cur.c > 0 && isWordRune(e.lines[e.cur.r][e.cur.c-1]) {
			return ActComplete
		}
		e.startInsert()
		e.cur = e.insertAt(e.cur, "  ")
		e.changed()
	case "shift+tab":
		e.startInsert()
		e.dedent(e.cur.r)
	case "ctrl+n", "ctrl+@", "ctrl+ ":
		return ActComplete
	case "ctrl+r":
		return ActHistory
	case "left", "right", "up", "down", "home", "end", "pgup", "pgdown", "ctrl+home", "ctrl+end", "ctrl+a", "ctrl+e",
		"shift+left", "shift+right", "shift+up", "shift+down", "shift+home", "shift+end", "alt+left", "alt+right", "ctrl+left", "ctrl+right":
		e.navKey(k)
		e.complOn = false
	default:
		if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
			e.startInsert()
			e.replaceSelection()
			text := string(msg.Runes)
			if msg.Type == tea.KeySpace {
				text = " "
			}
			e.cur = e.insertAt(e.cur, text)
			e.changed()
			if msg.Paste {
				return ActNone
			}
			r := msg.Runes
			if len(r) == 1 {
				if r[0] == '.' {
					return ActComplete
				}
				if isWordRune(r[0]) {
					if e.complOn {
						return ActCompleteUpdate
					}
					if e.wordLenBefore() == 3 {
						return ActComplete
					}
				} else {
					e.complOn = false
				}
			}
		}
	}
	return ActNone
}

// plainShortcut handles Ctrl shortcuts in non-vim mode.
func (e *Editor) plainShortcut(k string) (EditorAction, bool) {
	switch k {
	case "ctrl+z":
		e.undoOnce()
	case "ctrl+y":
		e.redoOnce()
	case "ctrl+a":
		e.anchor = pos{0, 0}
		e.cur = pos{len(e.lines) - 1, len(e.lines[len(e.lines)-1])}
		e.selecting = true
	case "ctrl+c":
		if !e.hasSelection() {
			return ActNone, false
		}
		e.register, e.regLinewise = e.selectedText(), false
		return ActYank, true
	case "ctrl+x":
		if !e.hasSelection() {
			return ActNone, true
		}
		e.register, e.regLinewise = e.selectedText(), false
		e.pushUndo()
		e.replaceSelection()
		e.changed()
		return ActYank, true
	case "ctrl+v":
		e.pushUndo()
		e.replaceSelection()
		e.cur = e.insertAt(e.cur, e.register)
		e.changed()
	default:
		return ActNone, false
	}
	return ActNone, true
}

// replaceSelection deletes the shift-selection, if any.
func (e *Editor) replaceSelection() bool {
	if !e.selecting {
		return false
	}
	e.selecting = false
	a, b := e.selRange()
	if a == b {
		return false
	}
	e.deleteRange(a, b)
	e.changed()
	return true
}

func (e *Editor) navKey(k string) {
	shift := strings.HasPrefix(k, "shift+")
	if !e.Vim {
		if shift && !e.selecting {
			e.selecting, e.anchor = true, e.cur
		} else if !shift {
			e.selecting = false
		}
	}
	k = strings.TrimPrefix(k, "shift+")
	switch k {
	case "left":
		e.moveLeft()
	case "right":
		if e.cur.c < len(e.lines[e.cur.r]) {
			e.cur.c++
		} else if e.cur.r < len(e.lines)-1 {
			e.cur = pos{e.cur.r + 1, 0}
		}
		e.wantCol = e.cur.c
	case "up":
		e.vertical(-1)
	case "down":
		e.vertical(1)
	case "home", "ctrl+a":
		e.cur.c = 0
		e.wantCol = 0
	case "end", "ctrl+e":
		e.cur.c = len(e.lines[e.cur.r])
		e.wantCol = 1 << 30
	case "pgup":
		e.vertical(-max(1, e.height-1))
	case "pgdown":
		e.vertical(max(1, e.height-1))
	case "ctrl+home":
		e.cur = pos{0, 0}
	case "ctrl+end":
		e.cur = pos{len(e.lines) - 1, len(e.lines[len(e.lines)-1])}
	case "alt+left", "ctrl+left":
		e.cur = e.wordStartBack(e.cur)
	case "alt+right", "ctrl+right":
		e.cur = e.nextWord(e.cur)
	}
}

func (e *Editor) moveLeft() {
	if e.cur.c > 0 {
		e.cur.c--
	} else if e.cur.r > 0 && e.Inserting() {
		e.cur = pos{e.cur.r - 1, len(e.lines[e.cur.r-1])}
	}
	e.wantCol = e.cur.c
}

func (e *Editor) vertical(n int) {
	if e.wantCol < e.cur.c {
		e.wantCol = e.cur.c
	}
	e.cur.r = clampInt(e.cur.r+n, 0, len(e.lines)-1)
	e.cur.c = min(e.wantCol, len(e.lines[e.cur.r]))
}

func (e *Editor) wordLenBefore() int {
	n := 0
	for c := e.cur.c - 1; c >= 0 && isWordRune(e.lines[e.cur.r][c]); c-- {
		n++
	}
	return n
}

func (e *Editor) dedent(r int) {
	l := e.lines[r]
	n := 0
	for n < 2 && n < len(l) && l[n] == ' ' {
		n++
	}
	if n > 0 {
		e.lines[r] = l[n:]
		if r == e.cur.r {
			e.cur.c = max(0, e.cur.c-n)
		}
		e.changed()
	}
}

// ---- normal / visual mode ----

func (e *Editor) normalKey(msg tea.KeyMsg) EditorAction {
	k := msg.String()
	if msg.Type == tea.KeySpace {
		k = " "
	}
	// r<char> replace
	if e.pending == "r" {
		e.pending = ""
		if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && e.cur.c < len(e.lines[e.cur.r]) {
			e.pushUndo()
			e.lines[e.cur.r][e.cur.c] = msg.Runes[0]
			e.changed()
		}
		return ActNone
	}
	// counts
	if len(k) == 1 && k[0] >= '0' && k[0] <= '9' && (k != "0" || e.count > 0) && e.pending != " " {
		e.count = e.count*10 + int(k[0]-'0')
		return ActNone
	}
	n := max(1, e.count)
	visual := e.mode == ModeVisual || e.mode == ModeVisualLine

	if e.pending == " " {
		e.pending, e.count = "", 0
		switch k {
		case "r", "enter":
			return ActRun
		case "a":
			return ActRunAll
		case "e":
			return ActExplain
		case "h":
			return ActHistory
		case "s":
			return ActSnippets
		}
		return ActNone
	}
	if e.pending == "g" {
		e.pending, e.count = "", 0
		op := e.gOp
		e.gOp = ""
		switch k {
		case "g":
			if op != "" {
				e.applyLinewise(op, 0, e.cur.r)
				e.cur.r = 0
				return e.opAction(op)
			}
			e.cur = pos{0, 0}
		}
		return ActNone
	}

	// operator-pending: d, c, y
	if op := e.opPending(); op != "" {
		return e.operatorMotion(op, k, n)
	}

	if e.motion(k, n) {
		e.count = 0
		return ActNone
	}
	e.count = 0
	switch k {
	case "esc":
		if visual {
			e.mode = ModeNormal
			return ActNone
		}
		return ActEscape
	case "i":
		e.enterInsert(e.cur)
	case "a":
		e.enterInsert(pos{e.cur.r, min(e.cur.c+1, len(e.lines[e.cur.r]))})
	case "I":
		e.enterInsert(pos{e.cur.r, len(leadingSpace(e.lines[e.cur.r]))})
	case "A":
		e.enterInsert(pos{e.cur.r, len(e.lines[e.cur.r])})
	case "o", "O":
		e.pushUndo()
		e.inInsert = true
		indent := leadingSpace(e.lines[e.cur.r])
		at := e.cur.r + 1
		if k == "O" {
			at = e.cur.r
		}
		e.lines = append(e.lines[:at], append([][]rune{[]rune(indent)}, e.lines[at:]...)...)
		e.cur = pos{at, len(indent)}
		e.mode = ModeInsert
		e.changed()
	case "v":
		if e.mode == ModeVisual {
			e.mode = ModeNormal
		} else {
			e.mode, e.anchor = ModeVisual, e.cur
		}
	case "V":
		if e.mode == ModeVisualLine {
			e.mode = ModeNormal
		} else {
			e.mode, e.anchor = ModeVisualLine, e.cur
		}
	case "x", "delete":
		if visual {
			return e.visualOp("d")
		}
		if len(e.lines[e.cur.r]) > 0 {
			e.pushUndo()
			end := min(e.cur.c+n, len(e.lines[e.cur.r]))
			e.register, e.regLinewise = e.textRange(e.cur, pos{e.cur.r, end}), false
			e.deleteRange(e.cur, pos{e.cur.r, end})
			e.changed()
		}
	case "d", "c", "y":
		if visual {
			return e.visualOp(k)
		}
		e.pending, e.count = k, n
	case "D", "C":
		e.pushUndo()
		end := pos{e.cur.r, len(e.lines[e.cur.r])}
		e.register, e.regLinewise = e.textRange(e.cur, end), false
		e.deleteRange(e.cur, end)
		e.changed()
		if k == "C" {
			e.mode, e.inInsert = ModeInsert, true
		}
	case "Y":
		e.register, e.regLinewise = string(e.lines[e.cur.r]), true
		return ActYank
	case "p", "P":
		e.paste(k == "P", n)
	case "u":
		for i := 0; i < n; i++ {
			e.undoOnce()
		}
	case "ctrl+r":
		for i := 0; i < n; i++ {
			e.redoOnce()
		}
	case "J":
		if e.cur.r < len(e.lines)-1 {
			e.pushUndo()
			l := e.lines[e.cur.r]
			next := []rune(strings.TrimLeft(string(e.lines[e.cur.r+1]), " "))
			at := len(l)
			e.lines[e.cur.r] = append(append(l, ' '), next...)
			e.lines = append(e.lines[:e.cur.r+1], e.lines[e.cur.r+2:]...)
			e.cur.c = at
			e.changed()
		}
	case ">", "<":
		if visual {
			a, b := e.selRange()
			e.pushUndo()
			e.indentLines(a.r, b.r, k == ">")
			e.mode = ModeNormal
			return ActNone
		}
		e.pending, e.count = k, n
	case "r":
		e.pending = "r"
	case "g":
		e.pending = "g"
	case " ":
		e.pending = " "
	case ":":
		return ActCommand
	case "enter":
		if visual {
			return ActRun
		}
		e.motion("j", 1)
	case "ctrl+n":
		return ActNone
	}
	return ActNone
}

func (e *Editor) enterInsert(at pos) {
	e.pushUndo()
	e.inInsert = true
	e.mode = ModeInsert
	e.cur = at
}

func (e *Editor) opPending() string {
	switch e.pending {
	case "d", "c", "y", ">", "<", "di", "ci", "yi":
		return e.pending
	}
	return ""
}

func (e *Editor) opAction(op string) EditorAction {
	if op == "y" {
		return ActYank
	}
	return ActNone
}

// operatorMotion applies op ("d","c","y",">","<", or "di" etc.) with motion key k.
func (e *Editor) operatorMotion(op, k string, n int) EditorAction {
	e.pending, e.count = "", 0
	if k == "esc" {
		return ActNone
	}
	if strings.HasSuffix(op, "i") && len(op) == 2 { // text object: iw
		if k == "w" {
			a, b := e.wordBounds(e.cur)
			return e.charOp(op[:1], a, b)
		}
		return ActNone
	}
	if k == "i" && (op == "d" || op == "c" || op == "y") {
		e.pending = op + "i"
		return ActNone
	}
	// doubled operator: dd, cc, yy, >>, <<
	if k == op {
		last := min(e.cur.r+n-1, len(e.lines)-1)
		e.applyLinewise(op, e.cur.r, last-e.cur.r)
		return e.opAction(op)
	}
	switch k {
	case "j", "down", "k", "up", "G":
		target := e.cur.r
		switch k {
		case "j", "down":
			target = min(e.cur.r+n, len(e.lines)-1)
		case "k", "up":
			target = max(e.cur.r-n, 0)
		case "G":
			target = len(e.lines) - 1
		}
		from, to := min(e.cur.r, target), max(e.cur.r, target)
		e.applyLinewise(op, from, to-from)
		return e.opAction(op)
	case "g":
		e.pending, e.gOp = "g", op
		return ActNone
	}
	if op == ">" || op == "<" {
		return ActNone
	}
	start := e.cur
	saved := e.cur
	if k == "w" && op == "c" {
		k = "e" // cw behaves like ce
		e.motion(k, n)
		end := pos{e.cur.r, min(e.cur.c+1, len(e.lines[e.cur.r]))}
		e.cur = saved
		return e.charOp(op, start, end)
	}
	if !e.motion(k, n) {
		return ActNone
	}
	end := e.cur
	e.cur = saved
	if end.less(start) {
		start, end = end, start
	}
	if k == "e" || k == "$" || k == "end" {
		end.c = min(end.c+1, len(e.lines[end.r]))
	}
	return e.charOp(op, start, end)
}

func (e *Editor) charOp(op string, a, b pos) EditorAction {
	text := e.textRange(a, b)
	e.register, e.regLinewise = text, false
	switch op {
	case "y":
		return ActYank
	case "d", "c":
		e.pushUndo()
		e.deleteRange(a, b)
		e.changed()
		if op == "c" {
			e.mode, e.inInsert = ModeInsert, true
		}
	}
	return ActNone
}

func (e *Editor) applyLinewise(op string, from, extra int) {
	to := min(from+extra, len(e.lines)-1)
	switch op {
	case "y":
		parts := make([]string, 0, to-from+1)
		for r := from; r <= to; r++ {
			parts = append(parts, string(e.lines[r]))
		}
		e.register, e.regLinewise = strings.Join(parts, "\n"), true
	case "d":
		e.pushUndo()
		e.register = e.deleteLines(from, to)
		e.regLinewise = true
		e.changed()
	case "c":
		e.pushUndo()
		parts := make([]string, 0, to-from+1)
		for r := from; r <= to; r++ {
			parts = append(parts, string(e.lines[r]))
		}
		e.register, e.regLinewise = strings.Join(parts, "\n"), true
		indent := leadingSpace(e.lines[from])
		e.lines = append(e.lines[:from+1], e.lines[to+1:]...)
		e.lines[from] = []rune(indent)
		e.cur = pos{from, len(indent)}
		e.mode, e.inInsert = ModeInsert, true
		e.changed()
	case ">", "<":
		e.pushUndo()
		e.indentLines(from, to, op == ">")
	}
}

func (e *Editor) indentLines(from, to int, in bool) {
	for r := from; r <= to; r++ {
		if in {
			e.lines[r] = append([]rune("  "), e.lines[r]...)
		} else {
			l := e.lines[r]
			n := 0
			for n < 2 && n < len(l) && l[n] == ' ' {
				n++
			}
			e.lines[r] = l[n:]
		}
	}
	e.changed()
}

func (e *Editor) visualOp(op string) EditorAction {
	a, b := e.selRange()
	linewise := e.mode == ModeVisualLine
	e.mode = ModeNormal
	if linewise {
		e.applyLinewise(op, a.r, b.r-a.r)
		if op == "y" {
			e.cur = pos{a.r, 0}
		}
		return e.opAction(op)
	}
	act := e.charOp(op, a, b)
	e.cur = a
	return act
}

func (e *Editor) paste(before bool, n int) {
	if e.register == "" {
		return
	}
	e.pushUndo()
	text := strings.Repeat(e.register+map[bool]string{true: "\n", false: ""}[e.regLinewise], n)
	if e.regLinewise {
		text = strings.TrimSuffix(text, "\n")
		at := e.cur.r + 1
		if before {
			at = e.cur.r
		}
		var newLines [][]rune
		for _, l := range strings.Split(text, "\n") {
			newLines = append(newLines, []rune(l))
		}
		e.lines = append(e.lines[:at], append(newLines, e.lines[at:]...)...)
		e.cur = pos{at, 0}
	} else {
		at := e.cur
		if !before && len(e.lines[at.r]) > 0 {
			at.c++
		}
		end := e.insertAt(at, text)
		e.cur = pos{end.r, max(0, end.c-1)}
	}
	e.changed()
}

// motion moves the cursor for motion keys and reports whether k was one.
func (e *Editor) motion(k string, n int) bool {
	switch k {
	case "h", "left", "backspace":
		for i := 0; i < n; i++ {
			e.moveLeft()
		}
	case "l", "right":
		e.cur.c = min(e.cur.c+n, len(e.lines[e.cur.r]))
		e.wantCol = e.cur.c
	case "j", "down":
		e.vertical(n)
	case "k", "up":
		e.vertical(-n)
	case "w":
		for i := 0; i < n; i++ {
			e.cur = e.nextWord(e.cur)
		}
		e.wantCol = e.cur.c
	case "b":
		for i := 0; i < n; i++ {
			e.cur = e.wordStartBack(e.cur)
		}
		e.wantCol = e.cur.c
	case "e":
		for i := 0; i < n; i++ {
			e.cur = e.wordEnd(e.cur)
		}
		e.wantCol = e.cur.c
	case "0", "home":
		e.cur.c, e.wantCol = 0, 0
	case "^":
		e.cur.c = len(leadingSpace(e.lines[e.cur.r]))
		e.wantCol = e.cur.c
	case "$", "end":
		e.cur.c = max(0, len(e.lines[e.cur.r])-1)
		e.wantCol = 1 << 30
	case "G":
		if e.count > 0 {
			e.cur.r = clampInt(e.count-1, 0, len(e.lines)-1)
		} else {
			e.cur.r = len(e.lines) - 1
		}
		e.cur.c = 0
	case "ctrl+d":
		e.vertical(max(1, e.height/2))
	case "ctrl+u":
		e.vertical(-max(1, e.height/2))
	case "ctrl+f", "pgdown":
		e.vertical(max(1, e.height-1))
	case "ctrl+b", "pgup":
		e.vertical(-max(1, e.height-1))
	case "{":
		r := e.cur.r - 1
		for r > 0 && strings.TrimSpace(string(e.lines[r])) != "" {
			r--
		}
		e.cur = pos{max(r, 0), 0}
	case "}":
		r := e.cur.r + 1
		for r < len(e.lines)-1 && strings.TrimSpace(string(e.lines[r])) != "" {
			r++
		}
		e.cur = pos{min(r, len(e.lines)-1), 0}
	default:
		return false
	}
	return true
}

func runeClass(r rune) int {
	switch {
	case unicode.IsSpace(r):
		return 0
	case isWordRune(r):
		return 1
	}
	return 2
}

func isWordRune(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

func (e *Editor) runeAt(p pos) (rune, bool) {
	if p.r >= len(e.lines) || p.c >= len(e.lines[p.r]) {
		return '\n', false
	}
	return e.lines[p.r][p.c], true
}

func (e *Editor) advance(p pos) (pos, bool) {
	if p.c < len(e.lines[p.r]) {
		p.c++
		if p.c < len(e.lines[p.r]) {
			return p, true
		}
	}
	if p.r < len(e.lines)-1 {
		return pos{p.r + 1, 0}, true
	}
	return p, false
}

func (e *Editor) retreat(p pos) (pos, bool) {
	if p.c > 0 {
		return pos{p.r, p.c - 1}, true
	}
	if p.r > 0 {
		return pos{p.r - 1, max(0, len(e.lines[p.r-1])-1)}, true
	}
	return p, false
}

func (e *Editor) classAt(p pos) int {
	r, ok := e.runeAt(p)
	if !ok {
		return 0
	}
	return runeClass(r)
}

func (e *Editor) nextWord(p pos) pos {
	start := e.classAt(p)
	startRow := p.r
	ok := true
	for ok && e.classAt(p) == start && start != 0 && p.r == startRow {
		p, ok = e.advance(p)
	}
	for ok && e.classAt(p) == 0 && len(e.lines[p.r]) > 0 {
		p, ok = e.advance(p)
	}
	return p
}

func (e *Editor) wordEnd(p pos) pos {
	p, ok := e.advance(p)
	for ok && e.classAt(p) == 0 {
		p, ok = e.advance(p)
	}
	c := e.classAt(p)
	for {
		n, ok := e.advance(p)
		if !ok || n.r != p.r || e.classAt(n) != c {
			return p
		}
		p = n
	}
}

func (e *Editor) wordStartBack(p pos) pos {
	if p.c == 0 && p.r > 0 && e.Inserting() {
		return pos{p.r - 1, len(e.lines[p.r-1])}
	}
	q, ok := e.retreat(p)
	for ok && e.classAt(q) == 0 && q.r == p.r {
		q, ok = e.retreat(q)
	}
	c := e.classAt(q)
	for {
		n, ok := e.retreat(q)
		if !ok || n.r != q.r || e.classAt(n) != c {
			return q
		}
		q = n
	}
}

func (e *Editor) wordBounds(p pos) (pos, pos) {
	l := e.lines[p.r]
	if len(l) == 0 {
		return p, p
	}
	c := runeClass(l[min(p.c, len(l)-1)])
	a, b := p.c, p.c
	for a > 0 && runeClass(l[a-1]) == c {
		a--
	}
	for b < len(l) && runeClass(l[b]) == c {
		b++
	}
	return pos{p.r, a}, pos{p.r, b}
}

func (e *Editor) hasSelection() bool {
	return e.mode == ModeVisual || e.mode == ModeVisualLine || (e.selecting && e.anchor != e.cur)
}

// selRange returns the selection as a half-open range.
func (e *Editor) selRange() (pos, pos) {
	a, b := e.anchor, e.cur
	if b.less(a) {
		a, b = b, a
	}
	switch e.mode {
	case ModeVisualLine:
		a.c = 0
		b.c = len(e.lines[b.r])
	case ModeVisual:
		b.c = min(b.c+1, len(e.lines[b.r]))
	}
	return a, b
}

func (e *Editor) selectedText() string {
	a, b := e.selRange()
	return e.textRange(a, b)
}

// Register returns the last yanked text.
func (e *Editor) Register() string { return e.register }

// ---- view ----

const (
	flagSel uint8 = 1 << iota
	flagCursor
	flagErr
)

// View renders the editor at its size.
func (e *Editor) View() string {
	if e.dirtyHL {
		e.hl = highlightLines(e.lines, e.lexer)
		e.dirtyHL = false
	}
	gw := e.gutterWidth()
	tw := e.textWidth()
	var selA, selB pos
	sel := e.hasSelection()
	if sel {
		selA, selB = e.selRange()
	}
	gutter := theme.Dim
	gutterCur := lipgloss.NewStyle().Foreground(theme.Accent)
	out := make([]string, 0, e.height)
	empty := len(e.lines) == 1 && len(e.lines[0]) == 0
	for vr := 0; vr < e.height; vr++ {
		r := e.top + vr
		if r >= len(e.lines) {
			out = append(out, theme.Dim.Render(Fit("~", gw)))
			continue
		}
		var sb strings.Builder
		num := Fit(strconv.Itoa(r+1), gw-1) + " "
		if r == e.cur.r {
			sb.WriteString(gutterCur.Render(num))
		} else {
			sb.WriteString(gutter.Render(num))
		}
		line := e.lines[r]
		if empty && e.Placeholder != "" && !e.focused {
			sb.WriteString(theme.Dim.Render(Fit(e.Placeholder, tw)))
			out = append(out, sb.String())
			continue
		}
		col := 0
		type run struct {
			syn, flags uint8
			text       []rune
		}
		var cur run
		var runs []run
		flush := func() {
			if len(cur.text) > 0 {
				runs = append(runs, cur)
			}
		}
		emit := func(rn rune, syn, flags uint8) {
			if len(cur.text) > 0 && (cur.syn != syn || cur.flags != flags) {
				flush()
				cur = run{}
			}
			cur.syn, cur.flags = syn, flags
			cur.text = append(cur.text, rn)
		}
		for c := 0; c <= len(line); c++ {
			var rn rune = ' '
			var syn uint8
			if c < len(line) {
				rn = line[c]
				if r < len(e.hl) && c < len(e.hl[r]) {
					syn = e.hl[r][c]
				}
			}
			w := runewidth.RuneWidth(rn)
			if col+w <= e.left {
				col += w
				continue
			}
			if col-e.left+w > tw {
				break
			}
			var flags uint8
			p := pos{r, c}
			if sel && !p.less(selA) && p.less(selB) {
				flags |= flagSel
			}
			if e.focused && p == e.cur {
				flags |= flagCursor
			}
			if e.errPos != nil && e.errPos.r == r && c >= e.errPos.c && c < e.errPos.c+1 {
				flags |= flagErr
			}
			if c == len(line) && flags&(flagCursor|flagSel) == 0 {
				break
			}
			emit(rn, syn, flags)
			col += w
		}
		flush()
		for _, rr := range runs {
			st := synStyles[rr.syn]
			if rr.flags&flagSel != 0 {
				st = st.Background(theme.SelBg)
			}
			if rr.flags&flagErr != 0 {
				st = st.Underline(true).Foreground(theme.Red)
			}
			if rr.flags&flagCursor != 0 {
				if e.Inserting() {
					st = st.Underline(true).Reverse(true)
				} else {
					st = theme.Cursor
				}
			}
			sb.WriteString(st.Render(string(rr.text)))
		}
		out = append(out, sb.String())
	}
	view := strings.Join(out, "\n")
	if e.complOn && e.focused {
		view = e.overlayCompletion(view, gw)
	}
	return view
}

func (e *Editor) overlayCompletion(view string, gw int) string {
	const maxItems = 8
	items := e.compl
	start := 0
	if e.complIx >= maxItems {
		start = e.complIx - maxItems + 1
	}
	end := min(len(items), start+maxItems)
	lw, dw := 0, 0
	for _, it := range items[start:end] {
		lw = max(lw, runewidth.StringWidth(it.Label))
		dw = max(dw, runewidth.StringWidth(it.Detail)+runewidth.StringWidth(it.Kind)+1)
	}
	lw = min(lw, 40)
	dw = min(dw, 30)
	w := lw + dw + 3
	var rows []string
	for i := start; i < end; i++ {
		it := items[i]
		txt := " " + Fit(it.Label, lw) + " " + Fit(strings.TrimSpace(it.Kind+" "+it.Detail), dw) + " "
		st := lipgloss.NewStyle().Background(theme.BarBg).Foreground(theme.Fg)
		if i == e.complIx {
			st = lipgloss.NewStyle().Background(theme.Accent).Foreground(theme.CursorFg)
		}
		rows = append(rows, st.Render(txt))
	}
	if len(items) > maxItems {
		rows = append(rows, lipgloss.NewStyle().Background(theme.BarBg).Foreground(theme.Muted).
			Render(Fit(" "+strconv.Itoa(e.complIx+1)+"/"+strconv.Itoa(len(items)), w)))
	}
	x := gw + displayCol(e.lines[e.complAt.r], e.complAt.c) - e.left
	y := e.cur.r - e.top + 1
	if y+len(rows) > e.height && e.cur.r-e.top-len(rows) >= 0 {
		y = e.cur.r - e.top - len(rows)
	}
	if x+w > e.width {
		x = max(0, e.width-w)
	}
	return Overlay(view, strings.Join(rows, "\n"), x, y)
}

func leadingSpace(l []rune) string {
	n := 0
	for n < len(l) && (l[n] == ' ' || l[n] == '\t') {
		n++
	}
	return string(l[:n])
}

func clampInt(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
