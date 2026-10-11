package ui

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/termenv"
	"golang.org/x/crypto/ssh"

	"github.com/farvisun/squi/internal/sshx"
)

// errCancelled is returned when the user dismisses a prompt.
var errCancelled = errors.New("cancelled by user")

// Bridge lets background goroutines (connecting, SSH auth) ask the user
// something and block until the UI answers.
type Bridge struct {
	Send func(tea.Msg)
}

type promptKind int

const (
	askSecret promptKind = iota
	askConfirm
)

type promptMsg struct {
	kind   promptKind
	title  string
	text   string
	danger bool
	reply  chan promptReply
}

type promptReply struct {
	value string
	ok    bool
}

func (b *Bridge) ask(ctx context.Context, p promptMsg) (promptReply, error) {
	if b == nil || b.Send == nil {
		return promptReply{}, errors.New("no interactive UI available")
	}
	p.reply = make(chan promptReply, 1)
	b.Send(p)
	select {
	case r := <-p.reply:
		if !r.ok {
			return r, errCancelled
		}
		return r, nil
	case <-ctx.Done():
		return promptReply{}, ctx.Err()
	}
}

// Secret asks for a password or passphrase.
func (b *Bridge) Secret(ctx context.Context, title, text string) (string, error) {
	r, err := b.ask(ctx, promptMsg{kind: askSecret, title: title, text: text})
	return r.value, err
}

// sshPrompter adapts the bridge to sshx.Prompter.
type sshPrompter struct {
	ctx context.Context
	b   *Bridge
}

func (p sshPrompter) ConfirmHostKey(host string, key ssh.PublicKey, fingerprint string) (bool, error) {
	text := fmt.Sprintf("The authenticity of host %s can't be established.\n%s key fingerprint is\n  %s\n\nTrust this host and add it to known_hosts?",
		host, key.Type(), fingerprint)
	_, err := p.b.ask(p.ctx, promptMsg{kind: askConfirm, title: "Unknown SSH host", text: text, danger: true})
	if errors.Is(err, errCancelled) {
		return false, nil
	}
	return err == nil, err
}

func (p sshPrompter) Secret(kind sshx.SecretKind, prompt string) (string, error) {
	title := "SSH " + string(kind)
	return p.b.Secret(p.ctx, title, prompt)
}

// handlePrompt shows a modal for a prompt request from a goroutine.
func (m *Model) handlePrompt(p promptMsg) {
	reply := func(v string, ok bool) tea.Cmd {
		return func() tea.Msg { p.reply <- promptReply{v, ok}; return nil }
	}
	switch p.kind {
	case askConfirm:
		m.push(&confirmModal{title: p.title, body: p.text, danger: p.danger,
			onYes: func() tea.Cmd { return reply("", true) },
			onNo:  func() tea.Cmd { return reply("", false) }})
	default:
		pm := newPrompt(p.title, p.text, "", true, func(v string) tea.Cmd { return reply(v, true) })
		pm.onCancel = func() tea.Cmd { return reply("", false) }
		m.push(pm)
	}
}

// copy puts text on the system clipboard (OSC 52 over SSH).
func (m *Model) copy(text, what string) tea.Cmd {
	remote := os.Getenv("SSH_TTY") != "" || os.Getenv("SSH_CONNECTION") != ""
	if remote || clipboard.WriteAll(text) != nil {
		termenv.DefaultOutput().Copy(text)
	}
	return m.toast("Copied "+what, toastInfo)
}
