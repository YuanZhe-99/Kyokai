package ctl

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
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
	"watch-status":       ctlWatchStatus,
	"logs":               ctlLogs,
}

// ctlWatchStatus watches devices' status for a while, as Home does (a lease
// renewed every 10 seconds), and prints each sample as it arrives.
func ctlWatchStatus(args []string) error {
	fs, socket := newFlags(args[0])
	devices := fs.String("device", "", "the Device IDs to watch (comma-separated)")
	seconds := fs.Int("seconds", 20, "how long to watch")
	if err := parse(fs, args[1:]); err != nil {
		return err
	}
	client := localapi.NewClient(*socket)
	ctx := context.Background()
	lease := func(ids []string) error {
		_, err := client.Do(ctx, localapi.Request{Type: localapi.RequestWatchSet, Watch: &ids})
		return err
	}
	ids := list(*devices)
	if err := lease(ids); err != nil {
		return err
	}
	defer lease([]string{})
	renewed, end := time.Now(), time.Now().Add(time.Duration(*seconds)*time.Second)
	seen := map[string]int64{}
	for time.Now().Before(end) {
		if time.Since(renewed) >= 10*time.Second {
			if err := lease(ids); err != nil {
				return err
			}
			renewed = time.Now()
		}
		resp, err := client.Do(ctx, localapi.Request{Type: localapi.RequestStatus})
		if err != nil {
			return err
		}
		for _, w := range resp.Status.Watching {
			if w.Sample == nil || seen[w.DeviceID] == w.ReceivedAt {
				if w.Sample == nil {
					fmt.Printf("%s %s\n", w.DeviceID, w.State)
				}
				continue
			}
			seen[w.DeviceID] = w.ReceivedAt
			s := w.Sample
			gpu := "-"
			if s.GPU != nil {
				gpu = fmt.Sprintf("%.0f%%", s.GPU.Usage)
			}
			fmt.Printf("%s %s  cpu %.0f%% of %d  memory %d/%d MiB  gpu %s  disk %d/%d GiB  net ↓%d ↑%d B/s  battery %v  up %ds  services %v\n",
				w.DeviceID, w.State, s.CPU.Usage, s.CPU.Cores, s.Memory.Used>>20, s.Memory.Total>>20, gpu,
				s.Disk.Used>>30, s.Disk.Total>>30, s.Net.RxBps, s.Net.TxBps, s.Battery, s.UptimeS, s.Services)
		}
		time.Sleep(time.Second)
	}
	return nil
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
	fs, socket := newFlags(args[0])
	device := fs.String("device", "", "the Device ID")
	address := fs.String("address", "", "the device's IP address or domain name, instead of the network")
	service := fs.String("service", "ssh", "ssh, rdp or vnc")
	listen := fs.String("listen", "", "serve the tunnel on this port of this computer's loopback (PORT or 127.0.0.1:PORT), one tunnel per connection, instead of standard input and output")
	if err := parse(fs, args[1:]); err != nil {
		return err
	}
	want := localapi.TunnelRequest{DeviceID: *device, Address: *address, Service: *service}
	if *listen != "" {
		return serveTunnel(*socket, *listen, want)
	}
	conn, err := localapi.NewClient(*socket).Tunnel(context.Background(), want)
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
	fs, socket := newFlags(cmd)
	device := fs.String("device", "", "the Device ID")
	service := fs.String("service", "", "ssh, rdp or vnc (credentials-forget: also key, or none for everything)")
	user := fs.String("user", "", "credentials-set: the user name")
	domain := fs.String("domain", "", "credentials-set: the Remote Desktop domain")
	keyID := fs.String("key-id", "", "credentials-set: a kept sign-in key for SSH")
	if err := parse(fs, args[1:]); err != nil {
		return err
	}
	req := localapi.Request{DeviceID: *device, Service: *service}
	switch cmd {
	case "credentials-set":
		req.Type = localapi.RequestCredentialsSet
		req.Credentials = &localapi.Credentials{User: *user, Domain: *domain, KeyID: *keyID}
		if p, ok := secret(servicePasswordEnv); ok {
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
	if jsonOut {
		return printJSON(resp)
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
	fs, socket := newFlags(cmd)
	name := fs.String("name", "", "keys-generate, authorized-add: the key's name")
	public := fs.String("public", "", "authorized-add: the OpenSSH public key line")
	id := fs.String("id", "", "keys-remove, authorized-remove: the key's id")
	if err := parse(fs, args[1:]); err != nil {
		return err
	}
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
	if err != nil {
		return err
	}
	if jsonOut {
		return printJSON(resp)
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

// ctlLogs prints the engine's logs (the helper's and its agent's in the
// helper modes); only the owner or the helper's operator may read them.
func ctlLogs(args []string) error {
	fs, socket := newFlags(args[0])
	if err := parse(fs, args[1:]); err != nil {
		return err
	}
	resp, err := localapi.NewClient(*socket).Do(context.Background(), localapi.Request{Type: localapi.RequestLogsRead})
	if err != nil {
		return err
	}
	_, err = io.WriteString(os.Stdout, resp.Document)
	return err
}

// serveTunnel listens on a loopback port and gives each connection a
// tunnel of its own to the device's service, until it is stopped.
// Notes: only loopback: the host's service never becomes reachable from
// this computer's network.
func serveTunnel(socket, listen string, want localapi.TunnelRequest) error {
	addr, err := loopback(listen)
	if err != nil {
		return err
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer l.Close()
	fmt.Println("listening on", l.Addr())
	client := localapi.NewClient(socket)
	for {
		c, err := l.Accept()
		if err != nil {
			return err
		}
		go func() {
			defer c.Close()
			t, err := client.Tunnel(context.Background(), want)
			if err != nil {
				fmt.Fprintln(os.Stderr, "no tunnel:", err)
				return
			}
			defer t.Close()
			go func() {
				_, _ = io.Copy(t, c)
				time.Sleep(stdinGrace)
				_ = t.Close()
			}()
			_, _ = io.Copy(c, t)
		}()
	}
}

// loopback is a --listen value as an address on this computer's loopback:
// a port alone is 127.0.0.1's; any other host is refused.
func loopback(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		host, port = "127.0.0.1", listen
	}
	if n, err := strconv.Atoi(port); err != nil || n < 0 || n > 65535 {
		return "", fmt.Errorf("--listen wants a port, or 127.0.0.1:PORT")
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return "", fmt.Errorf("a tunnel listens on this computer's loopback only, not %s", host)
	}
	return net.JoinHostPort(host, port), nil
}
