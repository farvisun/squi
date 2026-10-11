package sshx

import (
	"bytes"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/farvisun/squi/internal/config"
)

// SecretKind tells the Prompter what kind of secret is requested.
type SecretKind string

// Secret kinds passed to Prompter.Secret.
const (
	KindPassword            SecretKind = "password"
	KindPassphrase          SecretKind = "passphrase"
	KindKeyboardInteractive SecretKind = "keyboard-interactive"
)

// Prompter is implemented by the UI. Calls block until the user answers and
// may come from any goroutine.
type Prompter interface {
	// ConfirmHostKey is called for hosts not present in known_hosts. Return
	// true to trust the key; sshx then appends it to known_hosts.
	ConfirmHostKey(host string, key ssh.PublicKey, fingerprint string) (bool, error)
	// Secret asks for a secret. prompt is human text naming the host or key,
	// e.g. "Password for squi@bastion:22".
	Secret(kind SecretKind, prompt string) (string, error)
}

// passwordTries bounds interactive password and keyboard-interactive retries.
const passwordTries = 3

var errNoPrompter = errors.New("no interactive prompt available")

// authStep is one authentication attempt offered to the server.
type authStep struct {
	method  string // RFC 4252 method name
	auth    ssh.AuthMethod
	secrets func() (password, passphrase string) // what the step used; may be nil
}

// authenticator drives authentication for one hop. It lives as long as the
// Tunnel so that secrets that worked are reused on reconnect.
type authenticator struct {
	hop      hop
	mode     string
	keyPaths []string // explicit keys tried before config IdentityFiles
	prompter Prompter

	password   string // tried before prompting
	passphrase string

	usedPassword, usedPassphrase string // secrets that last authenticated

	// Per-handshake state, reset by begin.
	wd        *watchdog
	steps     []authStep
	next      int
	last      *authStep
	partials  int
	keys      []*keyFile
	agentConn net.Conn
	typed     string // last password typed or auto-answered
	autoUsed  bool   // known password already offered via keyboard-interactive
}

func newAuthenticator(h hop, opts Options, final bool) *authenticator {
	a := &authenticator{
		hop:        h,
		mode:       opts.SSH.Auth,
		prompter:   opts.Prompter,
		passphrase: opts.Passphrase,
	}
	if a.mode == "" {
		a.mode = config.SSHAuthAuto
	}
	if opts.SSH.KeyPath != "" {
		a.keyPaths = []string{expandPath(opts.SSH.KeyPath, h)}
	}
	if final {
		a.password = opts.Password
	}
	return a
}

// begin prepares a fresh handshake; wd is paused while prompting.
func (a *authenticator) begin(wd *watchdog) {
	a.wd, a.next, a.last, a.partials, a.typed, a.autoUsed = wd, 0, nil, 0, "", false
	a.keys = nil
	for _, p := range a.identityPaths() {
		a.keys = append(a.keys, &keyFile{path: p, a: a})
	}
	a.steps = a.buildSteps()
}

// end finishes a handshake, remembering the secrets on success.
func (a *authenticator) end(ok bool) {
	if a.agentConn != nil {
		a.agentConn.Close()
		a.agentConn = nil
	}
	if ok {
		a.commit(a.last)
	}
	a.wd, a.steps, a.keys, a.last = nil, nil, nil, nil
}

func (a *authenticator) commit(s *authStep) {
	if s == nil || s.secrets == nil {
		return
	}
	pw, pp := s.secrets()
	if pw != "" {
		a.password, a.usedPassword = pw, pw
	}
	if pp != "" {
		a.passphrase, a.usedPassphrase = pp, pp
	}
}

// callback implements ssh.ClientAuthCallback: it offers the next step the
// server allows, in our preference order.
func (a *authenticator) callback(ctx *ssh.ClientAuthContext) (ssh.AuthMethod, error) {
	if n := len(ctx.PartialSuccessMethods); n > a.partials {
		a.partials = n
		a.commit(a.last)
	}
	for a.next < len(a.steps) {
		s := &a.steps[a.next]
		a.next++
		if slices.Contains(ctx.AllowedMethods, s.method) {
			a.last = s
			return s.auth, nil
		}
	}
	return nil, nil
}

func (a *authenticator) buildSteps() []authStep {
	var steps []authStep
	use := func(modes ...string) bool { return a.mode == config.SSHAuthAuto || slices.Contains(modes, a.mode) }
	if use(config.SSHAuthAgent) {
		steps = append(steps, authStep{method: "publickey", auth: ssh.PublicKeysCallback(a.agentSigners)})
	}
	if use(config.SSHAuthKey) {
		for _, k := range a.keys {
			secrets := func() (string, string) { return "", k.passphrase }
			if _, err := os.Stat(k.path + "-cert.pub"); err == nil {
				steps = append(steps, authStep{"publickey", ssh.PublicKeysCallback(k.certSigners), secrets})
			}
			steps = append(steps, authStep{"publickey", ssh.PublicKeysCallback(k.signers), secrets})
		}
	}
	if use(config.SSHAuthPassword) {
		if a.password != "" {
			pw := a.password
			steps = append(steps, authStep{"password", ssh.Password(pw), func() (string, string) { return pw, "" }})
		}
		typed := func() (string, string) { return a.typed, "" }
		if a.prompter != nil {
			steps = append(steps, authStep{"password",
				ssh.RetryableAuthMethod(ssh.PasswordCallback(a.askPassword), passwordTries), typed})
		}
		steps = append(steps, authStep{"keyboard-interactive",
			ssh.RetryableAuthMethod(ssh.KeyboardInteractive(a.challenge), passwordTries), typed})
	}
	return steps
}

// identityPaths lists existing key files: explicit key, config IdentityFiles,
// then (only if neither is set, like OpenSSH) the default ~/.ssh keys.
func (a *authenticator) identityPaths() []string {
	paths := slices.Concat(a.keyPaths, a.hop.identityFiles)
	if len(paths) == 0 {
		dir := filepath.Join(homeDir(), ".ssh")
		for _, n := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
			paths = append(paths, filepath.Join(dir, n))
		}
	}
	var out []string
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

func (a *authenticator) ask(kind SecretKind, prompt string) (string, error) {
	if a.prompter == nil {
		return "", errNoPrompter
	}
	a.wd.pause()
	defer a.wd.resume()
	return a.prompter.Secret(kind, prompt)
}

func (a *authenticator) askPassword() (string, error) {
	pw, err := a.ask(KindPassword, "Password for "+a.hop.String())
	if err == nil {
		a.typed = pw
	}
	return pw, err
}

// challenge answers keyboard-interactive questions. A password-like question
// is answered with the known password once before asking the user.
func (a *authenticator) challenge(name, instruction string, questions []string, echos []bool) ([]string, error) {
	answers := make([]string, len(questions))
	for i, q := range questions {
		pwLike := !echos[i] && strings.Contains(strings.ToLower(q), "password")
		if pwLike && !a.autoUsed && a.password != "" {
			a.autoUsed = true
			answers[i], a.typed = a.password, a.password
			continue
		}
		prompt := strings.TrimSpace(strings.Join([]string{name, instruction, strings.TrimSpace(q)}, " "))
		ans, err := a.ask(KindKeyboardInteractive, fmt.Sprintf("%s (%s)", prompt, a.hop))
		if err != nil {
			return nil, err
		}
		if pwLike {
			a.typed = ans
		}
		answers[i] = ans
	}
	return answers, nil
}

func (a *authenticator) agentSigners() ([]ssh.Signer, error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, errors.New("ssh-agent: SSH_AUTH_SOCK is not set")
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("ssh-agent: %w", err)
	}
	a.agentConn = conn
	signers, err := agent.NewClient(conn).Signers()
	if err != nil || !a.hop.identitiesOnly {
		return signers, err
	}
	// IdentitiesOnly: only agent keys that match a configured identity.
	return slices.DeleteFunc(signers, func(s ssh.Signer) bool {
		return !slices.ContainsFunc(a.keys, func(k *keyFile) bool {
			pub := k.publicKey()
			return pub != nil && bytes.Equal(pub.Marshal(), s.PublicKey().Marshal())
		})
	}), nil
}

// keyFile is a private key on disk, parsed lazily and decrypted only when
// the server accepts its public key.
type keyFile struct {
	path string
	a    *authenticator

	loaded     bool
	raw        []byte
	pub        ssh.PublicKey // nil if unknown until decrypted
	signer     ssh.Signer    // nil while encrypted
	unlockErr  error
	passphrase string // passphrase that decrypted the key
}

// load reads the key and its public half without prompting.
func (k *keyFile) load() error {
	if k.loaded {
		return nil
	}
	raw, err := os.ReadFile(k.path)
	if err != nil {
		return err
	}
	k.loaded, k.raw = true, raw
	signer, err := ssh.ParsePrivateKey(raw)
	var missing *ssh.PassphraseMissingError
	switch {
	case err == nil:
		k.signer, k.pub = signer, signer.PublicKey()
	case errors.As(err, &missing):
		k.pub = missing.PublicKey
		if k.pub == nil {
			k.pub, _ = readPublicKey(k.path + ".pub")
		}
	default:
		k.unlockErr = fmt.Errorf("key %s: %w", k.path, err)
	}
	return nil
}

func (k *keyFile) publicKey() ssh.PublicKey {
	if k.load() != nil {
		return nil
	}
	return k.pub
}

// unlock returns the decrypted signer, asking for a passphrase if needed.
func (k *keyFile) unlock() (ssh.Signer, error) {
	if err := k.load(); err != nil {
		return nil, err
	}
	if k.signer != nil || k.unlockErr != nil {
		return k.signer, k.unlockErr
	}
	k.signer, k.unlockErr = k.decrypt()
	if k.signer != nil && k.pub == nil {
		k.pub = k.signer.PublicKey()
	}
	return k.signer, k.unlockErr
}

func (k *keyFile) decrypt() (ssh.Signer, error) {
	if pp := k.a.passphrase; pp != "" {
		if s, err := ssh.ParsePrivateKeyWithPassphrase(k.raw, []byte(pp)); err == nil {
			k.passphrase = pp
			return s, nil
		}
	}
	for range passwordTries {
		pp, err := k.a.ask(KindPassphrase, fmt.Sprintf("Passphrase for key %s (%s)", k.path, k.a.hop))
		if err != nil {
			return nil, fmt.Errorf("key %s: %w", k.path, err)
		}
		s, err := ssh.ParsePrivateKeyWithPassphrase(k.raw, []byte(pp))
		if errors.Is(err, x509.IncorrectPasswordError) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("key %s: %w", k.path, err)
		}
		k.passphrase = pp
		return s, nil
	}
	return nil, fmt.Errorf("key %s: incorrect passphrase", k.path)
}

// signers returns a lazily-unlocked signer for the key itself.
func (k *keyFile) signers() ([]ssh.Signer, error) {
	pub := k.publicKey()
	if pub == nil {
		// Public half unknown (e.g. encrypted PEM without .pub): unlock now.
		if _, err := k.unlock(); err != nil {
			return nil, err
		}
		pub = k.pub
	}
	return []ssh.Signer{&keySigner{pub: pub, key: k}}, nil
}

// certSigners returns a signer presenting "<key>-cert.pub".
func (k *keyFile) certSigners() ([]ssh.Signer, error) {
	pub, err := readPublicKey(k.path + "-cert.pub")
	if err != nil {
		return nil, err
	}
	cert, ok := pub.(*ssh.Certificate)
	if !ok {
		return nil, fmt.Errorf("%s-cert.pub: not a certificate", k.path)
	}
	if kp := k.publicKey(); kp != nil && !bytes.Equal(kp.Marshal(), cert.Key.Marshal()) {
		return nil, fmt.Errorf("%s-cert.pub: does not match key", k.path)
	}
	return []ssh.Signer{&keySigner{pub: cert, key: k}}, nil
}

// keySigner signs with a keyFile, unlocking it on first use.
type keySigner struct {
	pub ssh.PublicKey // the key or its certificate
	key *keyFile
}

func (s *keySigner) PublicKey() ssh.PublicKey { return s.pub }

func (s *keySigner) Sign(rand io.Reader, data []byte) (*ssh.Signature, error) {
	return s.SignWithAlgorithm(rand, data, "")
}

func (s *keySigner) SignWithAlgorithm(rand io.Reader, data []byte, algo string) (*ssh.Signature, error) {
	signer, err := s.key.unlock()
	if err != nil {
		return nil, err
	}
	if as, ok := signer.(ssh.AlgorithmSigner); ok {
		return as.SignWithAlgorithm(rand, data, algo)
	}
	if algo != "" && algo != signer.PublicKey().Type() {
		return nil, fmt.Errorf("key %s: unsupported signature algorithm %s", s.key.path, algo)
	}
	return signer.Sign(rand, data)
}

func readPublicKey(path string) (ssh.PublicKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey(b)
	return pub, err
}
