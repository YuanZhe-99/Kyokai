package gui

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
)

// fakeLink is a serviceLink over one connection, whose engine keeps one
// key (signing as the engine would) and remembers what is saved.
type fakeLink struct {
	conn  net.Conn
	key   ssh.Signer
	mu    sync.Mutex
	saved *localapi.Credentials
	signs int
}

func (f *fakeLink) Open(context.Context, string, string, string) (net.Conn, error) {
	return f.conn, nil
}
func (f *fakeLink) Credential(context.Context, string, string) (*localapi.Credentials, error) {
	return nil, nil
}
func (f *fakeLink) SaveCredential(_ context.Context, _, _ string, cr localapi.Credentials) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saved = &cr
	return nil
}
func (f *fakeLink) Keys(context.Context) ([]localapi.LoginKey, error) {
	if f.key == nil {
		return nil, nil
	}
	return []localapi.LoginKey{{ID: "k1", Name: "laptop", Type: "ed25519", Public: keyLine(f.key.PublicKey())}}, nil
}
func (f *fakeLink) Sign(_ context.Context, id, algorithm string, data []byte) ([]byte, error) {
	f.mu.Lock()
	f.signs++
	f.mu.Unlock()
	sig, err := f.key.(ssh.AlgorithmSigner).SignWithAlgorithm(rand.Reader, data, algorithm)
	if err != nil {
		return nil, err
	}
	return ssh.Marshal(sig), nil
}

// connPair is a connected pair of loopback TCP connections, a tunnel's
// stand-in (net.Pipe would deadlock: both SSH ends write their version
// first).
func connPair(t *testing.T) (net.Conn, net.Conn) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, _ := l.Accept()
		accepted <- c
	}()
	client, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	server := <-accepted
	t.Cleanup(func() { client.Close(); server.Close() })
	return client, server
}

// sshServer is an SSH server on one end of a pipe that admits key (if not
// nil) or password, answers a shell, says hello and reports window changes.
type sshServer struct {
	host      ssh.Signer
	passwords chan string
	sizes     chan [2]uint32
}

func newSSHServer(t *testing.T) *sshServer {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	host, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return &sshServer{host: host, passwords: make(chan string, 4), sizes: make(chan [2]uint32, 4)}
}

func (s *sshServer) serve(conn net.Conn, key ssh.PublicKey, password string) {
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if key != nil && bytes.Equal(k.Marshal(), key.Marshal()) {
				return nil, nil
			}
			return nil, errors.New("unknown key")
		},
		PasswordCallback: func(_ ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			s.passwords <- string(pw)
			if password != "" && string(pw) == password {
				return nil, nil
			}
			return nil, errors.New("wrong password")
		},
	}
	cfg.AddHostKey(s.host)
	_, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		ch, reqs, err := nc.Accept()
		if err != nil {
			return
		}
		go func() {
			for r := range reqs {
				switch r.Type {
				case "pty-req", "shell":
					_ = r.Reply(true, nil)
					if r.Type == "shell" {
						_, _ = ch.Write([]byte("hello"))
					}
				case "window-change":
					s.sizes <- [2]uint32{binary.BigEndian.Uint32(r.Payload), binary.BigEndian.Uint32(r.Payload[4:])}
				default:
					_ = r.Reply(false, nil)
				}
			}
		}()
	}
}

// An SSH sign-in with this device's key: the engine signs, no password is
// asked, the server's key is pinned, the shell answers, and the terminal's
// size reaches the server as a window change.
func TestSSHSignsInWithEngineKeys(t *testing.T) {
	client, server := connPair(t)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	key, _ := ssh.NewSignerFromKey(priv)
	link := &fakeLink{conn: client, key: key}
	srv := newSSHServer(t)
	go srv.serve(server, key.PublicKey(), "")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	term, _, err := sshDial(ctx, link, "657365333", "", &credForm{user: "yuan"}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	buf := make([]byte, 5)
	if _, err := io.ReadFull(term, buf); err != nil || string(buf) != "hello" {
		t.Fatalf("shell said %q, %v", buf, err)
	}
	if link.signs == 0 {
		t.Fatal("the engine signed nothing")
	}
	if link.saved == nil || link.saved.HostKey != keyLine(srv.host.PublicKey()) || link.saved.User != "yuan" || link.saved.Password != nil {
		t.Fatalf("saved %+v, want the user and the pinned host key without a password", link.saved)
	}
	if err := term.Resize(132, 43); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-srv.sizes:
		if got != [2]uint32{132, 43} {
			t.Fatalf("window change %v, want 132×43", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no window change reached the server")
	}
}

// A server whose key changed is refused before any password is sent; a
// wrong password says so; a remembered password is saved with the key.
func TestSSHHostKeyAndPassword(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv := newSSHServer(t)
	pw := "hunter2"

	client, server := connPair(t)
	go srv.serve(server, nil, pw)
	saved := &localapi.Credentials{User: "yuan", Password: &pw, HostKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"}
	_, seen, err := sshDial(ctx, &fakeLink{conn: client}, "657365333", "", nil, saved, "")
	if !errors.Is(err, errHostKeyChanged) || seen != keyLine(srv.host.PublicKey()) {
		t.Fatalf("changed key: err %v, seen %q", err, seen)
	}
	select {
	case got := <-srv.passwords:
		t.Fatalf("the password %q was sent to a server whose key changed", got)
	default:
	}

	client, server = connPair(t)
	go srv.serve(server, nil, pw)
	if _, _, err := sshDial(ctx, &fakeLink{conn: client}, "657365333", "", &credForm{user: "yuan", password: "wrong"}, nil, ""); !errors.Is(err, errSignInFailed) {
		t.Fatalf("wrong password: err %v", err)
	}

	client, server = connPair(t)
	go srv.serve(server, nil, pw)
	link := &fakeLink{conn: client}
	trust := keyLine(srv.host.PublicKey())
	term, _, err := sshDial(ctx, link, "657365333", "", &credForm{user: "yuan", password: pw, remember: true}, saved, trust)
	if err != nil {
		t.Fatal(err)
	}
	term.Close()
	if link.saved == nil || link.saved.Password == nil || *link.saved.Password != pw || link.saved.HostKey != trust {
		t.Fatalf("saved %+v, want the password and the trusted key", link.saved)
	}
}
