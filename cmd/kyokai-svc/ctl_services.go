package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
)

// ctlServices are the `ctl` commands for sign-in keys, tunnels and saved
// service sign-ins, each parsing its own flags (doc/en-us/development.md).
var ctlServices = map[string]func(args []string) error{
	"keys":               ctlKeys,
	"keys-generate":      ctlKeys,
	"keys-remove":        ctlKeys,
	"authorized":         ctlKeys,
	"authorized-add":     ctlKeys,
	"authorized-remove":  ctlKeys,
	"tunnel":             ctlTunnel,
	"credentials-set":    ctlCredentials,
	"credentials-get":    ctlCredentials,
	"credentials-forget": ctlCredentials,
}

// stdinGrace is how long `ctl tunnel` still prints the service's answer
// after its standard input ended.
const stdinGrace = 2 * time.Second

// servicePasswordEnv carries a service's password to `ctl credentials-set`.
const servicePasswordEnv = "KYOKAI_SERVICE_PASSWORD"

// ctlTunnel opens a tunnel to a device's own service and pipes it to
// standard input and output, as OpenSSH's ProxyCommand expects:
//
//	ssh -o ProxyCommand="kyokai-svc ctl tunnel --device 482916735 --service ssh" user@host
func ctlTunnel(args []string) error {
	fs := flag.NewFlagSet("ctl tunnel", flag.ExitOnError)
	socket := fs.String("socket", localapi.UserSocketPath(os.Getenv("KYOKAI_PROFILE")), "local API socket path")
	device := fs.String("device", "", "the Device ID")
	address := fs.String("address", "", "the device's IP address or domain name, instead of the network")
	service := fs.String("service", "ssh", "ssh, rdp or vnc")
	fs.Parse(args[1:])
	conn, err := localapi.NewClient(*socket).Tunnel(context.Background(), localapi.TunnelRequest{DeviceID: *device, Address: *address, Service: *service})
	if err != nil {
		return fmt.Errorf("no tunnel: %w", err)
	}
	defer conn.Close()
	go func() {
		_, _ = io.Copy(conn, os.Stdin)
		// Standard input ended: a tunnel has no half-close, so the answer
		// to what was just sent gets a moment, then the tunnel ends.
		time.Sleep(stdinGrace)
		_ = conn.Close()
	}()
	_, err = io.Copy(os.Stdout, conn)
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// ctlCredentials saves, shows (never the password itself) and forgets a
// device's service sign-ins; the password comes from the environment.
func ctlCredentials(args []string) error {
	cmd := args[0]
	fs := flag.NewFlagSet("ctl "+cmd, flag.ExitOnError)
	socket := fs.String("socket", localapi.UserSocketPath(os.Getenv("KYOKAI_PROFILE")), "local API socket path")
	device := fs.String("device", "", "the Device ID")
	service := fs.String("service", "", "ssh, rdp or vnc (credentials-forget: also key, or none for everything)")
	user := fs.String("user", "", "credentials-set: the user name")
	domain := fs.String("domain", "", "credentials-set: the Remote Desktop domain")
	keyID := fs.String("key-id", "", "credentials-set: a kept sign-in key for SSH")
	fs.Parse(args[1:])
	req := localapi.Request{DeviceID: *device, Service: *service}
	switch cmd {
	case "credentials-set":
		req.Type = localapi.RequestCredentialsSet
		req.Credentials = &localapi.Credentials{User: *user, Domain: *domain, KeyID: *keyID}
		if p, ok := os.LookupEnv(servicePasswordEnv); ok {
			req.Credentials.Password = &p
		}
	case "credentials-get":
		req.Type = localapi.RequestCredentialsGet
	case "credentials-forget":
		req.Type = localapi.RequestCredentialsForget
	}
	resp, err := localapi.NewClient(*socket).Do(context.Background(), req)
	if err != nil {
		return err
	}
	if c := resp.Credentials; c != nil {
		fmt.Printf("user %q domain %q key %q password set: %v\nserver key %s\n", c.User, c.Domain, c.KeyID, c.Password != nil, c.HostKey)
		return nil
	}
	fmt.Println("ok")
	return nil
}

// ctlKeys lists, generates and removes this device's sign-in keys and the
// keys that can connect to it. A generated key's private part goes to the
// engine only; its public line is printed for the other device.
func ctlKeys(args []string) error {
	cmd := args[0]
	fs := flag.NewFlagSet("ctl "+cmd, flag.ExitOnError)
	socket := fs.String("socket", localapi.UserSocketPath(os.Getenv("KYOKAI_PROFILE")), "local API socket path")
	name := fs.String("name", "", "keys-generate, authorized-add: the key's name")
	public := fs.String("public", "", "authorized-add: the OpenSSH public key line")
	id := fs.String("id", "", "keys-remove, authorized-remove: the key's id")
	fs.Parse(args[1:])
	req := localapi.Request{}
	switch cmd {
	case "keys":
		req.Type = localapi.RequestKeysList
	case "keys-generate":
		_, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		seed := base64.StdEncoding.EncodeToString(private.Seed())
		req.Type, req.LoginKey = localapi.RequestKeysAdd, &localapi.LoginKey{Name: *name, Private: &seed}
	case "keys-remove":
		req.Type, req.LoginKey = localapi.RequestKeysRemove, &localapi.LoginKey{ID: *id}
	case "authorized":
		req.Type = localapi.RequestAuthorizedList
	case "authorized-add":
		req.Type, req.Authorized = localapi.RequestAuthorizedAdd, &localapi.AuthorizedKey{Name: *name, Public: *public}
	case "authorized-remove":
		req.Type, req.Authorized = localapi.RequestAuthorizedRemove, &localapi.AuthorizedKey{ID: *id}
	}
	resp, err := localapi.NewClient(*socket).Do(context.Background(), req)
	if errors.Is(err, localapi.ErrNotRunning) {
		return errors.New("the engine is not running")
	}
	if err != nil {
		return err
	}
	for _, k := range resp.Keys {
		fmt.Printf("key %s %q %s\n  %s\n", k.ID, k.Name, k.Type, k.Public)
	}
	for _, k := range resp.Authorized {
		fmt.Printf("authorized %s %q last %d\n  %s\n", k.ID, k.Name, k.Last, k.Public)
	}
	if len(resp.Keys) == 0 && len(resp.Authorized) == 0 {
		fmt.Println("ok")
	}
	return nil
}
