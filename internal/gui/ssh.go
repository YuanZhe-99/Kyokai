package gui

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
	"golang.org/x/crypto/ssh"

	"github.com/YuanZhe-99/Kyokai/internal/core"
	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

func init() { serviceDials[serviceSSH] = dialSSH }

// sshTimeout bounds the SSH handshake and sign-in (tests shorten it).
var sshTimeout = 30 * time.Second

// sshTerm is an SSH shell as the terminal's Conn: what the shell writes is
// read, what is typed is written, and the terminal's size reaches the
// server as a window change.
type sshTerm struct {
	tunnel net.Conn
	client *ssh.Client
	sess   *ssh.Session
	in     io.WriteCloser
	out    io.Reader
	once   sync.Once
}

func (t *sshTerm) Read(p []byte) (int, error)  { return t.out.Read(p) }
func (t *sshTerm) Write(p []byte) (int, error) { return t.in.Write(p) }

// Resize tells the server the terminal's size (the terminal calls it as
// its view resizes).
func (t *sshTerm) Resize(cols, rows int) error { return t.sess.WindowChange(rows, cols) }

// Close ends the shell, the SSH connection and the tunnel.
func (t *sshTerm) Close() error {
	t.once.Do(func() {
		_ = t.sess.Close()
		_ = t.client.Close()
		_ = t.tunnel.Close()
	})
	return nil
}

// sshSigner is one of this device's sign-in keys as an ssh.Signer: the
// engine signs, and the private key never leaves it.
type sshSigner struct {
	ctx  context.Context
	link serviceLink
	id   string
	pub  ssh.PublicKey
}

func (k sshSigner) PublicKey() ssh.PublicKey { return k.pub }

func (k sshSigner) Sign(r io.Reader, data []byte) (*ssh.Signature, error) {
	return k.SignWithAlgorithm(r, data, "")
}

// SignWithAlgorithm has the engine sign the user-auth request data with
// the algorithm the server and the client agreed on.
func (k sshSigner) SignWithAlgorithm(_ io.Reader, data []byte, algorithm string) (*ssh.Signature, error) {
	if algorithm == "" {
		algorithm = k.pub.Type()
	}
	ctx, cancel := context.WithTimeout(k.ctx, 10*time.Second)
	defer cancel()
	blob, err := k.link.Sign(ctx, k.id, algorithm, data)
	if err != nil {
		return nil, err
	}
	var sig ssh.Signature
	if err := ssh.Unmarshal(blob, &sig); err != nil {
		return nil, err
	}
	return &sig, nil
}

// engineSigners are this device's sign-in keys as signers, RSA keys with
// SHA-2 signatures only.
func engineSigners(ctx context.Context, link serviceLink) []ssh.Signer {
	keys, err := link.Keys(ctx)
	if err != nil {
		return nil
	}
	var out []ssh.Signer
	for _, k := range keys {
		pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k.Public))
		if err != nil {
			continue
		}
		var s ssh.Signer = sshSigner{ctx: ctx, link: link, id: k.ID, pub: pub}
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
func keyLine(k ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
}

// sshDial signs in to a device's SSH server and opens a shell: with this
// device's keys (the engine signs), then the password typed or saved, also
// answering keyboard-interactive prompts with it. The server's key is
// pinned on first use; a different one is refused before any password is
// sent, unless it is trust, the key the person chose to trust.
// Inputs: typed is the form's sign-in, nil to use what is saved.
// Returns: the shell; errNeedSignIn without a user name; errSignInFailed;
// errHostKeyChanged with the server's key line.
// Side effects: saves the user name and pinned key (with the password when
// Remember is on) through link.
func sshDial(ctx context.Context, link serviceLink, device, address string, typed *credForm, saved *localapi.Credentials, trust string) (*sshTerm, string, error) {
	user, password, pinned := "", "", ""
	if saved != nil {
		user, pinned = saved.User, saved.HostKey
		if saved.Password != nil {
			password = *saved.Password
		}
	}
	if typed != nil {
		user, password = typed.user, typed.password
	}
	if user == "" {
		return nil, "", errNeedSignIn
	}
	tunnel, err := link.Open(ctx, device, address, serviceSSH)
	if err != nil {
		return nil, "", err
	}
	stop := context.AfterFunc(ctx, func() { _ = tunnel.Close() })
	defer stop()
	_ = tunnel.SetDeadline(time.Now().Add(sshTimeout))
	seen := ""
	auth := []ssh.AuthMethod{ssh.PublicKeysCallback(func() ([]ssh.Signer, error) { return engineSigners(ctx, link), nil })}
	if password != "" {
		// One password, tried once: a server that refused it by "password"
		// is not given it again by "keyboard-interactive", a second failure
		// that a Mac answers a minute late and counts against this computer.
		sent := false
		auth = append(auth, ssh.PasswordCallback(func() (string, error) {
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
		}))
	}
	cfg := &ssh.ClientConfig{User: user, Auth: auth, Timeout: sshTimeout,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			seen = keyLine(key)
			if pinned != "" && seen != pinned && seen != trust {
				return errHostKeyChanged
			}
			return nil
		}}
	conn, chans, reqs, err := ssh.NewClientConn(tunnel, "localhost:22", cfg)
	if err != nil {
		_ = tunnel.Close()
		if errors.Is(err, errHostKeyChanged) {
			return nil, seen, errHostKeyChanged
		}
		return nil, "", sshSignInError(err, seen != "")
	}
	_ = tunnel.SetDeadline(time.Time{})
	client := ssh.NewClient(conn, chans, reqs)
	t, err := sshShell(client, tunnel)
	if err != nil {
		_ = client.Close()
		_ = tunnel.Close()
		return nil, "", err
	}
	if seen != pinned || typed != nil {
		cr := localapi.Credentials{User: user, HostKey: seen}
		switch {
		case typed != nil && typed.remember:
			cr.Password = &password
		case typed == nil && saved != nil:
			cr.Password = saved.Password
		}
		_ = link.SaveCredential(ctx, device, serviceSSH, cr)
	}
	return t, "", nil
}

// sshShell opens a shell in a terminal of xterm-256color, 80 by 24 until
// the view tells its size.
func sshShell(client *ssh.Client, tunnel net.Conn) (*sshTerm, error) {
	sess, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 115200, ssh.TTY_OP_OSPEED: 115200}
	if err := sess.RequestPty("xterm-256color", 24, 80, modes); err != nil {
		return nil, err
	}
	in, err := sess.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := sess.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := sess.Shell(); err != nil {
		return nil, err
	}
	return &sshTerm{tunnel: tunnel, client: client, sess: sess, in: in, out: out}, nil
}

// sshClient is a running SSH tab: MyGo's terminal on the shell, in the
// app's theme.
type sshClient struct {
	term *terminal.Terminal
	conn *sshTerm
}

// newTerminal starts a terminal on a connection; it ran when the shell
// ended. Tests replace it (the terminal loads libghostty-vt).
var newTerminal = func(conn io.ReadWriteCloser, ended func()) (*terminal.Terminal, error) {
	return terminal.New(terminal.Options{Conn: conn, OnExit: func(int) { ended() }})
}

// errPasswordRefused ends a sign-in whose password the server already
// refused once in this connection.
var errPasswordRefused = errors.New("the password was refused")

// sshSignInError is what an SSH handshake's error means to its tab.
// Inputs: keyed, the key exchange finished (the server's key was seen), so
// what failed was the sign-in.
// Returns: errNoAnswer when the server went quiet (OpenSSH holds a failed
// or penalized sign-in, up to minutes); errSignInFailed when, after the
// key exchange, it refused the sign-in or closed the connection; else err.
func sshSignInError(err error, keyed bool) error {
	var ne net.Error
	switch {
	case errors.Is(err, os.ErrDeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return errNoAnswer
	case keyed:
		return errSignInFailed
	}
	return err
}

// dialSSH is the SSH tab's serviceDial.
func dialSSH(ctx context.Context, s *serviceTab, typed *credForm) (serviceClient, error) {
	saved := s.savedCredential(ctx)
	s.mu.Lock()
	trust := s.hostKey
	if saved != nil && typed == nil {
		s.form.user = saved.User
	}
	s.mu.Unlock()
	conn, seen, err := sshDial(ctx, s.app.link, s.device, s.address, typed, saved, trust)
	if errors.Is(err, errHostKeyChanged) {
		s.mu.Lock()
		s.changed = seen
		s.mu.Unlock()
	}
	if err != nil {
		return nil, err
	}
	c := &sshClient{conn: conn}
	term, err := newTerminal(conn, func() { s.ended(c, L.ServiceEnded()) })
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	c.term = term
	return c, nil
}

func (c *sshClient) view(ctx *ui.Context, _ *serviceTab) {
	terminal.View(ctx, c.term).Fill().AutoFocus()
}

func (c *sshClient) close() {
	if c.term != nil {
		_ = c.term.Close()
	}
	_ = c.conn.Close()
}

func (c *sshClient) grabs() bool        { return false }
func (c *sshClient) scan(core.KeyEvent) {}
func (c *sshClient) setFront(bool)      {}
