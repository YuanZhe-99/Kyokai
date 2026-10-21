package gui

import (
	"context"
	"encoding/base64"
	"errors"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
)

// The services a device's tunnels reach (doc/en-us/remote-services.md).
const (
	serviceSSH = "ssh"
	serviceRDP = "rdp"
	serviceVNC = "vnc"
)

// serviceLink is how a service tab reaches a device's own service and the
// sign-in saved for it: the engine (engineLink), or, in development before
// the engine serves tunnels, the stub (devLink).
type serviceLink interface {
	// Open is a byte stream to a device's service: a tunnel inside a Kyokai
	// session (address dials the device there instead of the network).
	Open(ctx context.Context, device, address, service string) (net.Conn, error)
	// Credential is the sign-in saved for a device's service, nil when none
	// is; asked right before a client signs in, never kept.
	Credential(ctx context.Context, device, service string) (*localapi.Credentials, error)
	// SaveCredential keeps a device's sign-in for a service, replacing what
	// was kept.
	SaveCredential(ctx context.Context, device, service string, cr localapi.Credentials) error
	// Keys are this device's sign-in keys (public parts only).
	Keys(ctx context.Context) ([]localapi.LoginKey, error)
	// Sign signs an SSH user-auth request with a kept key (algorithm as SSH
	// names it); the private key never leaves the engine.
	// Returns: the SSH signature encoding (format and blob).
	Sign(ctx context.Context, keyID, algorithm string, data []byte) ([]byte, error)
}

// errNoTunnel says the engine in use opens no tunnels yet.
var errNoTunnel = errors.New("the engine opens no tunnels")

// openTunnel opens a tunnel through the engine on socket.
// Notes: until the engine serves tunnel.open (localapi.Client.Tunnel, WP6)
// it refuses, never reaching this computer's own services in a device's
// place: WP11 replaces the body with
// `return localapi.NewClient(socket).Tunnel(ctx, req)`.
var openTunnel = func(ctx context.Context, socket string, req localapi.TunnelRequest) (net.Conn, error) {
	return nil, errNoTunnel
}

// engineLink is the engine as a serviceLink: tunnels and credentials over
// the local API.
type engineLink struct{ a *App }

func (l engineLink) Open(ctx context.Context, device, address, service string) (net.Conn, error) {
	return openTunnel(ctx, l.a.sock(), localapi.TunnelRequest{DeviceID: device, Address: address, Service: service})
}

func (l engineLink) Credential(ctx context.Context, device, service string) (*localapi.Credentials, error) {
	resp, err := localapi.NewClient(l.a.sock()).Do(ctx, localapi.Request{Type: localapi.RequestCredentialsGet, DeviceID: device, Service: service})
	if err != nil {
		return nil, err
	}
	return resp.Credentials, nil
}

func (l engineLink) SaveCredential(ctx context.Context, device, service string, cr localapi.Credentials) error {
	_, err := localapi.NewClient(l.a.sock()).Do(ctx, localapi.Request{Type: localapi.RequestCredentialsSet, DeviceID: device, Service: service, Credentials: &cr})
	return err
}

func (l engineLink) Keys(ctx context.Context) ([]localapi.LoginKey, error) {
	resp, err := localapi.NewClient(l.a.sock()).Do(ctx, localapi.Request{Type: localapi.RequestKeysList})
	if err != nil {
		return nil, err
	}
	return resp.Keys, nil
}

func (l engineLink) Sign(ctx context.Context, keyID, algorithm string, data []byte) ([]byte, error) {
	resp, err := localapi.NewClient(l.a.sock()).Do(ctx, localapi.Request{Type: localapi.RequestKeysSign,
		Sign: &localapi.SignRequest{KeyID: keyID, Algorithm: algorithm, Data: base64.StdEncoding.EncodeToString(data)}})
	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(resp.Signature)
}

// devLink is the development stub of serviceLink (KYOKAI_DEV_SERVICES=1):
// it dials the service on this computer's loopback (ports from
// KYOKAI_DEV_SSH_PORT, KYOKAI_DEV_RDP_PORT, KYOKAI_DEV_VNC_PORT, else the
// services' own), keeps credentials in memory for the run and has no keys.
// Notes: it never reaches another computer; the engine's tunnels replace it
// (openTunnel).
type devLink struct{ creds *devCreds }

// devCreds are devLink's credentials, by device and service.
type devCreds struct {
	mu sync.Mutex
	m  map[string]localapi.Credentials
}

// devPorts are the services' own ports.
var devPorts = map[string]int{serviceSSH: 22, serviceRDP: 3389, serviceVNC: 5900}

func (devLink) Open(ctx context.Context, _, _, service string) (net.Conn, error) {
	port := devPorts[service]
	if p, err := strconv.Atoi(os.Getenv("KYOKAI_DEV_" + map[string]string{serviceSSH: "SSH", serviceRDP: "RDP", serviceVNC: "VNC"}[service] + "_PORT")); err == nil {
		port = p
	}
	if port == 0 {
		return nil, errors.New("unknown service " + service)
	}
	d := net.Dialer{Timeout: 3 * time.Second}
	return d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
}

func (l devLink) Credential(_ context.Context, device, service string) (*localapi.Credentials, error) {
	if l.creds == nil {
		return nil, nil
	}
	l.creds.mu.Lock()
	defer l.creds.mu.Unlock()
	cr, ok := l.creds.m[device+"/"+service]
	if !ok {
		return nil, nil
	}
	return &cr, nil
}

func (l devLink) SaveCredential(_ context.Context, device, service string, cr localapi.Credentials) error {
	if l.creds == nil {
		return nil
	}
	l.creds.mu.Lock()
	defer l.creds.mu.Unlock()
	if l.creds.m == nil {
		l.creds.m = map[string]localapi.Credentials{}
	}
	l.creds.m[device+"/"+service] = cr
	return nil
}

func (devLink) Keys(context.Context) ([]localapi.LoginKey, error) { return nil, nil }

func (devLink) Sign(context.Context, string, string, []byte) ([]byte, error) {
	return nil, errors.New("no keys in development")
}

// newServiceLink is the link the app's service tabs use: the engine, or the
// stub while KYOKAI_DEV_SERVICES is set.
func newServiceLink(a *App) serviceLink {
	if os.Getenv("KYOKAI_DEV_SERVICES") != "" {
		return devLink{creds: &devCreds{}}
	}
	return engineLink{a}
}
