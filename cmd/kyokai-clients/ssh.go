package main

// SSH as the desktop's internal/gui/ssh.go does it, without its UI: sign in
// with this device's keys (the core signs; private keys never leave it) or
// a password, pin the server's key on first use, open a shell of
// xterm-256color.

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// sshTimeout bounds the SSH handshake and sign-in.
const sshTimeout = 30 * time.Second

var (
	errNeedSignIn      = errors.New("need-sign-in")
	errSignInFailed    = errors.New("sign-in-failed")
	errNoAnswer        = errors.New("no-answer")
	errHostKeyChanged  = errors.New("host-key-changed")
	errPasswordRefused = errors.New("sign-in-failed")
)

// sshTerm is an open shell.
type sshTerm struct {
	tunnel net.Conn
	client *ssh.Client
	sess   *ssh.Session
	in     io.WriteCloser
	out    io.Reader
	once   sync.Once
}

func (t *sshTerm) resize(cols, rows int) error { return t.sess.WindowChange(rows, cols) }

func (t *sshTerm) close() {
	t.once.Do(func() {
		_ = t.sess.Close()
		_ = t.client.Close()
		_ = t.tunnel.Close()
	})
}

// coreSigner is one of this device's sign-in keys: the core signs.
type coreSigner struct {
	id  string
	pub ssh.PublicKey
}

func (k coreSigner) PublicKey() ssh.PublicKey { return k.pub }

func (k coreSigner) Sign(r io.Reader, data []byte) (*ssh.Signature, error) {
	return k.SignWithAlgorithm(r, data, "")
}

// SignWithAlgorithm has the core sign the user-auth request data (core
// keys.sign accepts only an RFC 4252 user-auth blob).
func (k coreSigner) SignWithAlgorithm(_ io.Reader, data []byte, algorithm string) (*ssh.Signature, error) {
	if algorithm == "" {
		algorithm = k.pub.Type()
	}
	var r struct {
		Signature string `json:"signature"`
	}
	params := map[string]any{"key_id": k.id, "algorithm": algorithm, "data": base64.StdEncoding.EncodeToString(data)}
	if err := coreCall("keys.sign", params, &r); err != nil {
		return nil, err
	}
	blob, err := base64.StdEncoding.DecodeString(r.Signature)
	if err != nil {
		return nil, err
	}
	var sig ssh.Signature
	if err := ssh.Unmarshal(blob, &sig); err != nil {
		return nil, err
	}
	return &sig, nil
}

// coreSigners are this device's sign-in keys, RSA keys with SHA-2 only.
func coreSigners() []ssh.Signer {
	var keys []struct {
		ID     string `json:"id"`
		Public string `json:"public"`
	}
	if err := coreCall("keys.list", nil, &keys); err != nil {
		return nil
	}
	var out []ssh.Signer
	for _, k := range keys {
		pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k.Public))
		if err != nil {
			continue
		}
		var s ssh.Signer = coreSigner{id: k.ID, pub: pub}
		if pub.Type() == ssh.KeyAlgoRSA {
			if s, err = ssh.NewSignerWithAlgorithms(s.(ssh.AlgorithmSigner), []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}); err != nil {
				continue
			}
		}
		out = append(out, s)
	}
	return out
}

// keyLine is a public key in authorized_keys form, as host keys are pinned.
func keyLine(k ssh.PublicKey) string { return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k))) }

// sshConn is a signed-in SSH connection through a tunnel.
type sshConn struct {
	tunnel net.Conn
	client *ssh.Client
}

func (s *sshConn) close() {
	_ = s.client.Close()
	_ = s.tunnel.Close()
}

// sshConnect signs in with plan's method alone.
// Inputs: pinned, the server key saved; trust, the key the person chose
// to trust after a change.
// Returns: the connection and the server key it showed; errHostKeyChanged
// with the key it offered instead.
func sshConnect(ctx context.Context, device, address string, plan signIn, pinned, trust string) (*sshConn, string, error) {
	if plan.User == "" {
		return nil, "", errNeedSignIn
	}
	tunnel, err := openTunnel(ctx, device, address, "ssh")
	if err != nil {
		return nil, "", err
	}
	stop := context.AfterFunc(ctx, func() { _ = tunnel.Close() })
	defer stop()
	_ = tunnel.SetDeadline(time.Now().Add(sshTimeout))
	password := plan.Password
	defer func() { password = "" }()
	var auth []ssh.AuthMethod
	if plan.Method == "key" {
		auth = []ssh.AuthMethod{ssh.PublicKeysCallback(func() ([]ssh.Signer, error) { return coreSigners(), nil })}
	} else {
		// One password, tried once (a Mac counts a second failure).
		sent := false
		auth = []ssh.AuthMethod{ssh.PasswordCallback(func() (string, error) {
			sent = true
			return password, nil
		}), ssh.KeyboardInteractive(func(_, _ string, questions []string, echos []bool) ([]string, error) {
			if sent && slices.Contains(echos, false) {
				return nil, errPasswordRefused
			}
			answers := make([]string, len(questions))
			for i := range questions {
				if !echos[i] {
					answers[i] = password
				}
			}
			return answers, nil
		})}
	}
	keys := &hostKeys{pinned: pinned, trust: trust}
	cfg := &ssh.ClientConfig{User: plan.User, Auth: auth, Timeout: sshTimeout, HostKeyCallback: keys.check}
	conn, chans, reqs, err := ssh.NewClientConn(tunnel, "localhost:22", cfg)
	seen, refused := keys.shown()
	if err != nil {
		_ = tunnel.Close()
		if errors.Is(err, errHostKeyChanged) {
			return nil, refused, errHostKeyChanged
		}
		var ne net.Error
		switch {
		case errors.Is(err, os.ErrDeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
			return nil, "", errNoAnswer
		case seen != "":
			return nil, "", errSignInFailed
		}
		return nil, "", err
	}
	_ = tunnel.SetDeadline(time.Time{})
	return &sshConn{tunnel: tunnel, client: ssh.NewClient(conn, chans, reqs)}, seen, nil
}

// shell opens a shell of xterm-256color on the connection.
// Inputs: lang, the shell's LANG; cols, rows, the terminal's size.
// Side effects: on failure the connection is closed.
func (s *sshConn) shell(lang string, cols, rows int) (*sshTerm, error) {
	sess, err := s.client.NewSession()
	if err == nil {
		_ = sess.Setenv("LANG", lang)
		_ = sess.Setenv("COLORTERM", "truecolor")
		modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 115200, ssh.TTY_OP_OSPEED: 115200}
		err = sess.RequestPty("xterm-256color", rows, cols, modes)
	}
	var in io.WriteCloser
	var out io.Reader
	if err == nil {
		in, err = sess.StdinPipe()
	}
	if err == nil {
		out, err = sess.StdoutPipe()
	}
	if err == nil {
		err = sess.Shell()
	}
	if err != nil {
		s.close()
		return nil, err
	}
	return &sshTerm{tunnel: s.tunnel, client: s.client, sess: sess, in: in, out: out}, nil
}

// hostKeys checks the server's key at each key exchange: the first against
// the pinned key unless it is the one the person chose to trust, every
// re-key against the first.
type hostKeys struct {
	pinned, trust string
	mu            sync.Mutex
	seen, refused string
}

func (h *hostKeys) check(_ string, _ net.Addr, key ssh.PublicKey) error {
	line := keyLine(key)
	h.mu.Lock()
	defer h.mu.Unlock()
	want := h.seen
	if want == "" && line != h.trust {
		want = h.pinned
	}
	if want != "" && line != want {
		h.refused = line
		return errHostKeyChanged
	}
	h.seen = line
	return nil
}

func (h *hostKeys) shown() (seen, refused string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.seen, h.refused
}

// sshLang is the shell's LANG for the app's language tag.
func sshLang(locale string) string {
	switch {
	case strings.HasPrefix(locale, "zh_TW"), strings.HasPrefix(locale, "zh_HK"), strings.Contains(locale, "Hant"):
		return "zh_TW.UTF-8"
	case strings.HasPrefix(locale, "zh"):
		return "zh_CN.UTF-8"
	case strings.HasPrefix(locale, "ja"):
		return "ja_JP.UTF-8"
	}
	return "en_US.UTF-8"
}

// sshSignIn signs client c in to a device's SSH server for ssh.open and
// sftp.open, both with the SSH service's sign-in and pinned key.
// Returns: the connection; nil when it failed, c ended with why.
// Side effects: a successful sign-in from the form is saved as asked, and
// the server's key pinned.
func sshSignIn(ctx context.Context, c *client, p openParams) *sshConn {
	saved := savedCredential(p.DeviceID, "ssh")
	plan := p.plan(saved)
	conn, seen, err := sshConnect(ctx, p.DeviceID, p.Address, plan, saved.HostKey, p.Trust)
	if err != nil {
		fields := map[string]any{"state": "failed", "error": reason(err)}
		if errors.Is(err, errHostKeyChanged) {
			fields["host_key"] = seen
		}
		c.end(fields)
		return nil
	}
	cr := kept(plan, p.Remember, p.typed())
	if cr == nil && seen != saved.HostKey {
		cr = &credentials{User: plan.User, Method: plan.Method}
	}
	if cr != nil {
		cr.HostKey = seen
		saveCredential(p.DeviceID, "ssh", *cr)
	}
	return conn
}

// runSSH connects client c to a device's SSH server and relays the shell.
func runSSH(ctx context.Context, c *client, p openParams) {
	conn := sshSignIn(ctx, c, p)
	if conn == nil {
		return
	}
	t, err := conn.shell(sshLang(p.Locale), max(p.Cols, 20), max(p.Rows, 5))
	if err != nil {
		c.end(map[string]any{"state": "failed", "error": reason(err)})
		return
	}
	c.mu.Lock()
	c.term = t
	c.closer = t.close
	c.mu.Unlock()
	c.state(map[string]any{"state": "connected"})
	buf := make([]byte, 32<<10)
	for {
		n, err := t.out.Read(buf)
		if n > 0 {
			c.pushData(buf[:n])
		}
		if err != nil {
			break
		}
	}
	t.close()
	c.end(map[string]any{"state": "closed"})
}
