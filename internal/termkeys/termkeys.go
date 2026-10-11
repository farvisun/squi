// Package termkeys lets squi see modified keys such as ctrl+enter, which
// legacy terminal input cannot tell apart from plain enter.
//
// squi asks the terminal for the kitty keyboard protocol ("disambiguate"
// flag) and xterm's modifyOtherKeys. Terminals that support either then
// report such keys as CSI sequences. Bubble Tea v1 only understands legacy
// input, so Input rewrites those sequences back to the legacy bytes Bubble
// Tea knows; ctrl+enter becomes "\n" (ctrl+j), which squi binds to "run".
// Terminals that support neither keep sending legacy bytes, which pass
// through unchanged.
package termkeys

import (
	"bytes"
	"os"
	"regexp"
	"strconv"
	"unicode"
	"unicode/utf8"
)

const (
	// Enable pushes kitty "disambiguate" mode and sets modifyOtherKeys=2.
	// Kitty keeps separate flag stacks per screen, so send it after the
	// alternate screen is entered.
	Enable = "\x1b[>1u\x1b[>4;2m"
	// Disable pops the kitty mode and resets modifyOtherKeys. Send it before
	// leaving the alternate screen.
	Disable = "\x1b[<u\x1b[>4;0m"
	// ResetOtherKeys resets modifyOtherKeys only (safe on the main screen).
	ResetOtherKeys = "\x1b[>4;0m"
)

// Input wraps a terminal file and translates extended key reports.
// It implements Bubble Tea's term.File so the program still switches the
// terminal to raw mode and polls the underlying descriptor.
type Input struct {
	f       *os.File
	buf     []byte
	partial []byte   // incomplete CSI held until the next read
	ready   [][]byte // translated chunks not yet returned
}

// Wrap returns an Input reading from f.
func Wrap(f *os.File) *Input { return &Input{f: f, buf: make([]byte, 4096)} }

func (in *Input) Write(p []byte) (int, error) { return in.f.Write(p) }
func (in *Input) Close() error                { return in.f.Close() }
func (in *Input) Fd() uintptr                 { return in.f.Fd() }
func (in *Input) Name() string                { return in.f.Name() }

// Read returns translated input. Chunks are returned one per call so a
// translated escape key is never glued to the next key (which Bubble Tea
// would read as alt+key).
func (in *Input) Read(p []byte) (int, error) {
	for len(in.ready) == 0 {
		n, err := in.f.Read(in.buf)
		if n > 0 {
			data := append(in.partial, in.buf[:n]...)
			in.partial = nil
			if cut := incompleteCSI(data); cut >= 0 {
				in.partial = append([]byte(nil), data[cut:]...)
				data = data[:cut]
			}
			in.ready = Translate(data)
		}
		if err != nil {
			if len(in.ready) == 0 && len(in.partial) > 0 {
				in.ready, in.partial = [][]byte{in.partial}, nil
			}
			if len(in.ready) == 0 {
				return 0, err
			}
			break
		}
	}
	n := copy(p, in.ready[0])
	if n < len(in.ready[0]) {
		in.ready[0] = in.ready[0][n:]
	} else {
		in.ready = in.ready[1:]
	}
	return n, nil
}

// incompleteCSI returns where a trailing, unfinished "ESC [ digits" sequence
// starts, or -1.
func incompleteCSI(b []byte) int {
	i := bytes.LastIndexByte(b, 0x1b)
	if i < 0 || len(b)-i < 3 || b[i+1] != '[' {
		return -1
	}
	for _, c := range b[i+2:] {
		if (c < '0' || c > '9') && c != ';' && c != ':' {
			return -1
		}
	}
	return i
}

var (
	// kitty: CSI code[:alternates] [; mods[:event] [; text]] u
	csiU = regexp.MustCompile(`\x1b\[(\d+)(?::[\d:]*)?(?:;(\d*)(?::\d+)?)?(?:;[\d:]*)?u`)
	// xterm modifyOtherKeys: CSI 27 ; mods ; code ~
	otherKeys = regexp.MustCompile(`\x1b\[27;(\d+);(\d+)~`)
	// kitty sends unmodified F1, F2 and F4 as CSI P/Q/S
	csiF = regexp.MustCompile(`\x1b\[([PQS])`)
)

// Translate rewrites extended key reports in b to legacy input. The result
// is split after each translated escape key.
func Translate(b []byte) [][]byte {
	var out [][]byte
	cur := []byte{}
	for len(b) > 0 {
		loc, kind := nextMatch(b)
		if loc == nil {
			cur = append(cur, b...)
			break
		}
		cur = append(cur, b[:loc[0]]...)
		seq := b[loc[0]:loc[1]]
		b = b[loc[1]:]
		var rep []byte
		switch kind {
		case 'u':
			m := csiU.FindSubmatch(seq)
			rep = key(atoi(m[1]), atoi(string(m[2])), seq)
		case '~':
			m := otherKeys.FindSubmatch(seq)
			rep = key(atoi(m[2]), atoi(m[1]), seq)
		case 'F':
			rep = []byte{0x1b, 'O', seq[2]}
		}
		cur = append(cur, rep...)
		if len(rep) == 1 && rep[0] == 0x1b {
			out = append(out, cur)
			cur = []byte{}
		}
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

func nextMatch(b []byte) ([]int, byte) {
	var best []int
	var kind byte
	for _, c := range []struct {
		re *regexp.Regexp
		k  byte
	}{{csiU, 'u'}, {otherKeys, '~'}, {csiF, 'F'}} {
		if loc := c.re.FindIndex(b); loc != nil && (best == nil || loc[0] < best[0]) {
			best, kind = loc, c.k
		}
	}
	return best, kind
}

func atoi[T string | []byte](s T) int {
	n, _ := strconv.Atoi(string(s))
	return n
}

const (
	modShift = 1
	modAlt   = 2
	modCtrl  = 4
)

// key encodes code with modifiers (1-based, as sent by the terminal) the way
// a legacy terminal would. Unknown keys are returned as seq unchanged.
func key(code, mods int, seq []byte) []byte {
	if mods > 0 {
		mods--
	}
	shift, alt, ctrl := mods&modShift != 0, mods&modAlt != 0, mods&modCtrl != 0
	var b []byte
	switch {
	case code == 13:
		b = []byte{'\r'}
		if ctrl {
			b = []byte{'\n'}
		}
	case code == 9:
		b = []byte{'\t'}
		if shift {
			return []byte("\x1b[Z")
		}
	case code == 127 || code == 8:
		b = []byte{0x7f}
		if ctrl {
			b = []byte{0x08}
		}
	case code == 27:
		b = []byte{0x1b}
	case code == 32:
		b = []byte{' '}
		if ctrl {
			b = []byte{0}
		}
	case code > 32 && code < 0xE000 || code > 0xF8FF && code <= unicode.MaxRune:
		r := rune(code)
		if shift && !ctrl {
			r = unicode.ToUpper(r)
		}
		if ctrl {
			if c, ok := ctrlByte(r); ok {
				b = []byte{c}
				break
			}
		}
		b = utf8.AppendRune(nil, r)
	default:
		return seq
	}
	if alt {
		b = append([]byte{0x1b}, b...)
	}
	return b
}

func ctrlByte(r rune) (byte, bool) {
	switch {
	case r >= 'a' && r <= 'z':
		return byte(r - 'a' + 1), true
	case r >= '@' && r <= '_':
		return byte(r - '@'), true
	case r == '/':
		return 0x1f, true
	case r == '?':
		return 0x7f, true
	}
	return 0, false
}
