package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// openParams opens a client (ssh.open, sftp.open, rdp.open, vnc.open).
//
//	device_id  the device; address, where to dial it ("" through the network)
//	method     the sign-in the person chose in the form: ssh key|password;
//	           rdp account|none; vnc password|account|none. "" uses the one
//	           saved for the device and service (credentials.get), and
//	           fails with need-sign-in when there is none.
//	user, domain, password  the form's; remember keeps them (kept)
//	trust      ssh: the server key line the person chose to trust
//	cols, rows ssh: the terminal's size; locale, the app's language tag
//	           sftp.open signs in as ssh.open does, with its saved sign-in
//	width, height  rdp: the desktop size asked for (default 1600 × 900)
type openParams struct {
	DeviceID string `json:"device_id"`
	Address  string `json:"address"`
	Method   string `json:"method"`
	User     string `json:"user"`
	Domain   string `json:"domain"`
	Password string `json:"password"`
	Remember bool   `json:"remember"`
	Trust    string `json:"trust"`
	Cols     int    `json:"cols"`
	Rows     int    `json:"rows"`
	Locale   string `json:"locale"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

// typed is whether the person filled the sign-in form.
func (p openParams) typed() bool { return p.Method != "" }

// signIn is how to sign in to a service.
type signIn struct{ Method, User, Domain, Password string }

// plan is the form's sign-in, else the one saved.
func (p openParams) plan(saved credentials) signIn {
	if p.typed() {
		return signIn{p.Method, p.User, p.Domain, p.Password}
	}
	password := ""
	if saved.Password != nil {
		password = *saved.Password
	}
	return signIn{saved.Method, saved.User, saved.Domain, password}
}

// credentials is a saved service sign-in (core credentials.get/set).
// Password: absent keeps the saved one, "" removes it.
type credentials struct {
	Method   string  `json:"method,omitempty"`
	User     string  `json:"user,omitempty"`
	Domain   string  `json:"domain,omitempty"`
	Password *string `json:"password,omitempty"`
	KeyID    string  `json:"key_id,omitempty"`
	HostKey  string  `json:"host_key,omitempty"`
}

func savedCredential(device, service string) credentials {
	var c credentials
	_ = coreCall("credentials.get", map[string]any{"device_id": device, "service": service}, &c)
	return c
}

func saveCredential(device, service string, c credentials) {
	_ = coreCall("credentials.set", map[string]any{"device_id": device, "service": service, "credentials": c}, nil)
}

// kept is what a successful sign-in from the form leaves saved
// (internal/gui/servicetab.go kept): remembered, its method, user, domain
// and password (none for key and none); not, the user and domain only, the
// password and method forgotten. Nothing for a saved sign-in.
func kept(plan signIn, remember, typed bool) *credentials {
	if !typed {
		return nil
	}
	empty := ""
	if !remember {
		return &credentials{User: plan.User, Domain: plan.Domain, Password: &empty}
	}
	password := plan.Password
	if plan.Method == "key" || plan.Method == "none" {
		password = ""
	}
	return &credentials{Method: plan.Method, User: plan.User, Domain: plan.Domain, Password: &password}
}

// reason is an error as a state packet names it: a tunnel's failure or the
// host's refusal, need-sign-in, sign-in-required, sign-in-failed,
// no-answer, no-secure-sign-in, host-key-changed, no-sftp (sftp.open: the
// server offers no SFTP), closed; else the error's text.
func reason(err error) string {
	var te tunnelError
	switch {
	case errors.As(err, &te):
		return te.reason
	case errors.Is(err, context.Canceled):
		return "closed"
	}
	return err.Error()
}

// run runs one call.
func run(method string, raw json.RawMessage) (any, error) {
	switch method {
	case "key.import":
		return importKey(raw)
	case "ssh.open", "sftp.open", "rdp.open", "vnc.open":
		var p openParams
		if err := json.Unmarshal(raw, &p); err != nil || p.DeviceID == "" {
			return nil, fmt.Errorf("bad open")
		}
		c := clients.add()
		ctx, cancel := context.WithCancel(context.Background())
		c.stop = cancel
		c.state(map[string]any{"state": "connecting"})
		switch method {
		case "ssh.open":
			go runSSH(ctx, c, p)
		case "sftp.open":
			go runSFTP(ctx, c, p)
		case "rdp.open":
			go runRDP(ctx, c, p)
		default:
			go runVNC(ctx, c, p)
		}
		return map[string]any{"id": c.id}, nil
	}
	if strings.HasPrefix(method, "sftp.") {
		return filesCall(method, raw)
	}
	var p struct {
		ID      uint64  `json:"id"`
		Data    string  `json:"data"`
		Cols    int     `json:"cols"`
		Rows    int     `json:"rows"`
		X       int     `json:"x"`
		Y       int     `json:"y"`
		Buttons uint8   `json:"buttons"`
		DX      float64 `json:"dx"`
		DY      float64 `json:"dy"`
		Code    int     `json:"code"`
		Keysym  uint32  `json:"keysym"`
		Down    bool    `json:"down"`
		Text    string  `json:"text"`
		Width   int     `json:"width"`
		Height  int     `json:"height"`
		Scale   int     `json:"scale"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("bad params")
	}
	c := clients.get(p.ID)
	if c == nil {
		return nil, fmt.Errorf("no such client")
	}
	c.mu.Lock()
	d, t := c.desk, c.term
	c.mu.Unlock()
	if method == "close" {
		c.close()
		return map[string]any{}, nil
	}
	var err error
	switch {
	case method == "write" && t != nil:
		var b []byte
		if b, err = base64.StdEncoding.DecodeString(p.Data); err == nil {
			_, err = t.in.Write(b)
		}
	case method == "resize" && t != nil:
		err = t.resize(max(p.Cols, 1), max(p.Rows, 1))
	case d == nil:
		return map[string]any{}, nil // not connected yet: dropped, as input before a session is
	case method == "pointer":
		err = d.pointer(p.X, p.Y, p.Buttons)
	case method == "scroll":
		// Notches, positive dy up; both clients scroll 20 pixels a notch.
		err = d.scroll(p.X, p.Y, p.DX*20, p.DY*20)
	case method == "scancode":
		err = d.scancode(p.Code, p.Down)
	case method == "key":
		err = d.key(p.Keysym, p.Down)
	case method == "text":
		err = d.text(p.Text)
	case method == "clipboard":
		err = d.clipboard(p.Text)
	case method == "display":
		err = d.display(p.Width, p.Height, p.Scale)
	default:
		return nil, fmt.Errorf("unknown method %q", method)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{}, nil
}

// close ends the client: its dial, or its connection.
func (c *client) close() {
	c.mu.Lock()
	stop, closer := c.stop, c.closer
	c.mu.Unlock()
	if stop != nil {
		stop()
	}
	if closer != nil {
		closer()
	}
}
