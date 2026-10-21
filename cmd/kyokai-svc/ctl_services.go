package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
)

// ctlServices are the `ctl` commands for sign-in keys, each parsing its
// own flags (doc/en-us/development.md).
var ctlServices = map[string]func(args []string) error{
	"keys":              ctlKeys,
	"keys-generate":     ctlKeys,
	"keys-remove":       ctlKeys,
	"authorized":        ctlKeys,
	"authorized-add":    ctlKeys,
	"authorized-remove": ctlKeys,
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
