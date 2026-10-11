package ui

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/farvisun/squi/internal/sqlfmt"
	"github.com/farvisun/squi/internal/sqltext"
	"github.com/farvisun/squi/internal/termkeys"
)

// externalEditMsg is delivered when the external editor exits.
type externalEditMsg struct {
	tab  *tab
	path string
	orig string
	err  error
}

// editorCommand returns the user's editor: $VISUAL, $EDITOR, else vi
// (notepad on Windows). The value may carry arguments, e.g. "code -w".
func editorCommand() []string {
	for _, v := range []string{"VISUAL", "EDITOR"} {
		if f := strings.Fields(os.Getenv(v)); len(f) > 0 {
			return f
		}
	}
	if runtime.GOOS == "windows" {
		return []string{"notepad"}
	}
	return []string{"vi"}
}

// ttyExec runs an editor on the real terminal. Bubble Tea would hand it the
// program's input, which may be the termkeys filter rather than a TTY;
// extended key reporting is paused so the editor sees plain keys.
type ttyExec struct {
	cmd       *exec.Cmd
	termWrite func(string)
}

func (e *ttyExec) SetStdin(io.Reader)  {}
func (e *ttyExec) SetStdout(io.Writer) {}
func (e *ttyExec) SetStderr(io.Writer) {}

func (e *ttyExec) Run() error {
	e.cmd.Stdin, e.cmd.Stdout, e.cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if e.termWrite != nil {
		e.termWrite(termkeys.Disable)
		defer e.termWrite(termkeys.Enable)
	}
	return e.cmd.Run()
}

// openExternalEditor edits the tab's SQL in $VISUAL / $EDITOR.
func (m *Model) openExternalEditor(t *tab) tea.Cmd {
	f, err := os.CreateTemp("", "squi-*.sql")
	if err != nil {
		return m.toast("Temp file: "+err.Error(), toastError)
	}
	orig := t.editor.Value()
	_, werr := f.WriteString(orig)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(f.Name())
		return m.toast("Temp file: "+werr.Error(), toastError)
	}
	argv := append(editorCommand(), f.Name())
	path := f.Name()
	run := &ttyExec{cmd: exec.Command(argv[0], argv[1:]...), termWrite: m.termWrite}
	return tea.Exec(run, func(err error) tea.Msg { return externalEditMsg{tab: t, path: path, orig: orig, err: err} })
}

// handleExternalEdit loads the edited file back into the tab.
func (m *Model) handleExternalEdit(msg externalEditMsg) tea.Cmd {
	defer os.Remove(msg.path)
	if msg.err != nil {
		var ee *exec.Error
		if errors.As(msg.err, &ee) {
			return m.toast("Cannot start editor: "+ee.Error()+" — set $VISUAL or $EDITOR", toastError)
		}
		return m.toast("Editor: "+msg.err.Error(), toastError)
	}
	b, err := os.ReadFile(msg.path)
	if err != nil {
		return m.toast("Read edited SQL: "+err.Error(), toastError)
	}
	text := string(b)
	if !strings.HasSuffix(msg.orig, "\n") {
		text = strings.TrimSuffix(strings.TrimSuffix(text, "\n"), "\r") // editors add a final newline
	}
	if !m.hasTab(msg.tab) {
		return nil
	}
	t := msg.tab
	if text == msg.orig {
		return m.toast("No changes from the editor", toastInfo)
	}
	if t.editor.Value() != msg.orig {
		return m.toast("The query changed meanwhile; edited text not applied", toastWarn)
	}
	off := t.editor.CursorOffset()
	t.editor.SetValue(text)
	t.editor.SetCursorOffset(min(off, len(text)))
	m.focus = focusEditor
	return m.toast("Query updated from the editor", toastInfo)
}

// formatSQL pretty-prints the selected statements, or the one under the
// cursor (undoable).
func (m *Model) formatSQL(t *tab) tea.Cmd {
	src := t.editor.Value()
	d := t.ac.driver()
	from, to, ok := t.editor.Selection()
	if !ok {
		st, found := sqltext.StatementAt(src, t.editor.CursorOffset(), d)
		if !found {
			return m.toast("Nothing to format", toastInfo)
		}
		from, to = st.Start, st.End
	}
	out := sqlfmt.FormatScript(src, from, to, d)
	if out == src {
		return m.toast("Already formatted (only queries and DML are reflowed)", toastInfo)
	}
	t.editor.SetValue(out)
	t.editor.SetCursorOffset(from)
	return nil
}
