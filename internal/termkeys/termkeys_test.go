package termkeys

import (
	"bytes"
	"os"
	"testing"
)

func flat(c [][]byte) string { return string(bytes.Join(c, nil)) }

func TestTranslate(t *testing.T) {
	cases := []struct{ in, want string }{
		{"\x1b[13;5u", "\n"},           // ctrl+enter (kitty)
		{"\x1b[27;5;13~", "\n"},        // ctrl+enter (modifyOtherKeys)
		{"\x1b[13;3u", "\x1b\r"},       // alt+enter
		{"\x1b[13;2u", "\r"},           // shift+enter
		{"\x1b[27u", "\x1b"},           // esc
		{"\x1b[99;5u", "\x03"},         // ctrl+c
		{"\x1b[27;5;99~", "\x03"},      // ctrl+c (modifyOtherKeys)
		{"\x1b[119;5u", "\x17"},        // ctrl+w
		{"\x1b[49;3u", "\x1b1"},        // alt+1
		{"\x1b[93;3u", "\x1b]"},        // alt+]
		{"\x1b[97;6u", "\x01"},         // ctrl+shift+a
		{"\x1b[9;2u", "\x1b[Z"},        // shift+tab
		{"\x1b[127;5u", "\x08"},        // ctrl+backspace
		{"\x1b[32;5u", "\x00"},         // ctrl+space
		{"\x1b[P", "\x1bOP"},           // F1
		{"\x1b[97:65;6u", "\x01"},      // alternate keys reported
		{"\x1b[57399u", "\x1b[57399u"}, // kitty keypad key: untouched
		{"abc\x1b[A\x1b[15~", "abc\x1b[A\x1b[15~"},
		{"\x1b[<0;10;5M", "\x1b[<0;10;5M"}, // mouse
		{"\x1b[200~x\x1b[201~", "\x1b[200~x\x1b[201~"},
	}
	for _, c := range cases {
		if got := flat(Translate([]byte(c.in))); got != c.want {
			t.Errorf("Translate(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestEscapeIsSplit(t *testing.T) {
	got := Translate([]byte("\x1b[27uj"))
	if len(got) != 2 || string(got[0]) != "\x1b" || string(got[1]) != "j" {
		t.Fatalf("got %q", got)
	}
}

func TestInputHoldsPartialSequence(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	in := Wrap(r)
	go func() {
		w.Write([]byte("x\x1b[13;"))
		w.Write([]byte("5u"))
		w.Close()
	}()
	var all []byte
	buf := make([]byte, 64)
	for {
		n, err := in.Read(buf)
		all = append(all, buf[:n]...)
		if err != nil {
			break
		}
	}
	if string(all) != "x\n" {
		t.Fatalf("got %q", all)
	}
}
