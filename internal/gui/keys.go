package gui

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"golang.org/x/crypto/ssh"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// keysState is the Security page's keys and saved sign-ins (keys.go): the
// lists as last fetched, the form open, and a private key shown once.
type keysState struct {
	mine       []localapi.LoginKey
	authorized []localapi.AuthorizedKey
	loaded     bool
	busy       bool
	// form is the form open: "" none, "generate", "import", "authorize" or
	// "pair" (a pair whose public key may connect here).
	form keyForm
	// shown is a private key shown once, until Done.
	shown *shownKey
}

// keyForm is a key form's fields while it is open. The passphrase and a
// pasted private key are cleared as the form is sent.
type keyForm struct {
	kind       string
	name       string
	text       string
	passphrase string
	// locked: the key pasted or chosen is encrypted and needs its
	// passphrase.
	locked bool
	note   string
}

// shownKey is a private key shown once, to copy or save, never kept.
type shownKey struct {
	title string
	pem   string
	file  string // the file name Save suggests
	// pair: a pair whose public key may connect here, shown on the
	// Permissions page beside the keys that can connect.
	pair bool
}

// importedKey is a private key read for the engine: its type, OpenSSH
// public line and private part (an Ed25519 seed, else PKCS#8 DER).
type importedKey struct {
	Type    string
	Public  string
	Private []byte
}

// Why a private key cannot be imported.
var (
	errKeyLocked     = errors.New("the key needs its passphrase")
	errKeyPassphrase = errors.New("the passphrase is wrong")
	errKeyType       = errors.New("the key is of a type Kyokai does not keep")
)

// importKey reads an OpenSSH or PEM private key (passphrase "" for an
// unencrypted one): Ed25519, RSA of 2048 bits or more, ECDSA P-256 or P-384.
// Returns: errKeyLocked, errKeyPassphrase or errKeyType, or the key.
func importKey(data []byte, passphrase string) (importedKey, error) {
	var raw any
	var err error
	if passphrase == "" {
		raw, err = ssh.ParseRawPrivateKey(data)
	} else {
		raw, err = ssh.ParseRawPrivateKeyWithPassphrase(data, []byte(passphrase))
	}
	var missing *ssh.PassphraseMissingError
	switch {
	case errors.As(err, &missing):
		return importedKey{}, errKeyLocked
	case errors.Is(err, x509.IncorrectPasswordError):
		return importedKey{}, errKeyPassphrase
	case err != nil:
		return importedKey{}, err
	}
	var k importedKey
	var pub any
	switch key := raw.(type) {
	case ed25519.PrivateKey:
		k.Type, pub, k.Private = "ed25519", key.Public(), key.Seed()
	case *ed25519.PrivateKey:
		k.Type, pub, k.Private = "ed25519", key.Public(), key.Seed()
	case *rsa.PrivateKey:
		if key.N.BitLen() < 2048 {
			return importedKey{}, errKeyType
		}
		k.Type, pub = "rsa", key.Public()
	case *ecdsa.PrivateKey:
		switch key.Curve {
		case elliptic.P256():
			k.Type = "ecdsa-p256"
		case elliptic.P384():
			k.Type = "ecdsa-p384"
		default:
			return importedKey{}, errKeyType
		}
		pub = key.Public()
	default:
		return importedKey{}, errKeyType
	}
	if k.Private == nil {
		if k.Private, err = x509.MarshalPKCS8PrivateKey(raw); err != nil {
			return importedKey{}, err
		}
	}
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		return importedKey{}, err
	}
	k.Public = keyLine(sp)
	return k, nil
}

// newKeyPair makes an Ed25519 key pair: the key as the engine keeps it and
// the private key in OpenSSH form to take away, encrypted with passphrase
// when one is given.
func newKeyPair(comment, passphrase string) (importedKey, string, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return importedKey{}, "", err
	}
	var block *pem.Block
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(priv, comment)
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, comment, []byte(passphrase))
	}
	if err != nil {
		return importedKey{}, "", err
	}
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		return importedKey{}, "", err
	}
	return importedKey{Type: "ed25519", Public: keyLine(sp), Private: priv.Seed()}, string(pem.EncodeToMemory(block)), nil
}

// keyFingerprint is a public key line's SHA256 fingerprint as OpenSSH shows
// it, and its type in words; "" for a line that does not parse.
func keyFingerprint(line string) (fp, kind string) {
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return "", ""
	}
	kind = pub.Type()
	switch kind {
	case ssh.KeyAlgoED25519:
		kind = "Ed25519"
	case ssh.KeyAlgoRSA:
		kind = "RSA"
	case ssh.KeyAlgoECDSA256:
		kind = "ECDSA P-256"
	case ssh.KeyAlgoECDSA384:
		kind = "ECDSA P-384"
	}
	return ssh.FingerprintSHA256(pub), kind
}

// keysSettings are the Security page's cards of keys: this device's keys
// and what is saved for other devices (the keys that can connect to it are
// on the Permissions page, among the ways to sign in).
func (a *App) keysSettings(c *ui.Context) {
	a.mu.Lock()
	k := a.keys
	a.mu.Unlock()
	if !k.loaded && !k.busy {
		a.loadKeys()
	}
	if k.shown != nil && !k.shown.pair {
		a.shownKeyCard(c, *k.shown)
	}
	a.myKeysCard(c, k)
	a.savedCard(c)
}

// loadKeys fetches this device's keys and the keys that can connect to it.
func (a *App) loadKeys() {
	a.mu.Lock()
	a.keys.busy = true
	a.mu.Unlock()
	go func() {
		mine, err1 := a.requestWith(localapi.Request{Type: localapi.RequestKeysList})
		auth, err2 := a.requestWith(localapi.Request{Type: localapi.RequestAuthorizedList})
		a.update(func() {
			a.keys.busy, a.keys.loaded = false, true
			if err1 == nil {
				a.keys.mine = mine.Keys
			}
			if err2 == nil {
				a.keys.authorized = auth.Authorized
			}
		})
	}()
}

// keyRow is one key: its name, type and fingerprint, Copy public key and
// Remove.
func (a *App) keyRow(c *ui.Context, key, name, public string, busy bool, remove func()) {
	t := c.Theme()
	fp, kind := keyFingerprint(public)
	ui.Row(c.Key(key)).Gap(gap).AlignItems(ui.Center).FillWidth().Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, name).Bold().SingleLine()
			ui.Text(c, kind+" · "+fp).FontSize(sizeSmall).FontFeatures("tnum").TextColor(t.TextMuted).Selectable()
		})
		a.keyActions(c, key, name, public, busy, remove)
	})
}

// keyActions are a key's Copy public key and Remove (which asks first).
func (a *App) keyActions(c *ui.Context, key, name, public string, busy bool, remove func()) {
	if ui.Button(c.Key(key+"-copy"), L.KeysCopyPublic()).Clicked() {
		mygo.Clipboard.WriteText(public)
		a.update(func() { a.setToast(L.Copied(), false) })
	}
	if ui.Button(c.Key(key+"-rm"), L.ActionRemove()).Disabled(busy).Clicked() {
		go func() {
			if a.confirm(L.KeysRemoveConfirm(name), L.ActionRemove()) {
				remove()
			}
		}()
	}
}

// myKeysCard is "Your keys": the keys this device signs in with, to other
// Kyokai devices (Ed25519) and to SSH servers, with Generate and Import.
func (a *App) myKeysCard(c *ui.Context, k keysState) {
	t := c.Theme()
	card(c.Key("my-keys")).Children(func() {
		ui.Text(c, L.KeysMineTitle()).FontSize(sizeHeading).Bold()
		ui.Text(c, L.KeysMineHint()).FontSize(sizeSmall).TextColor(t.TextMuted)
		if k.loaded && len(k.mine) == 0 {
			ui.Text(c, L.KeysNone()).FontSize(sizeSmall).TextColor(t.TextMuted)
		}
		for _, key := range k.mine {
			id := key.ID
			a.keyRow(c, "key-"+id, key.Name, key.Public, k.busy, func() {
				a.keysCommand(localapi.Request{Type: localapi.RequestKeysRemove, LoginKey: &localapi.LoginKey{ID: id}}, "")
			})
		}
		switch k.form.kind {
		case "generate", "import":
			a.keyForm(c, k)
		default:
			ui.Row(c).Gap(gap).Children(func() {
				if ui.Button(c.Key("key-gen"), L.KeysGenerate()).Disabled(k.busy).Clicked() {
					a.openKeyForm("generate")
				}
				if ui.Button(c.Key("key-import"), L.KeysImport()).Disabled(k.busy).Clicked() {
					a.openKeyForm("import")
				}
			})
		}
	})
}

// openKeyForm opens a key form, closing any other.
func (a *App) openKeyForm(kind string) {
	a.mu.Lock()
	a.keys.form = keyForm{kind: kind}
	a.mu.Unlock()
}

// keyForm is the key form open: a name, then for Import a private key
// (pasted or from a file) and its passphrase when it has one, for Add a
// public key line or .pub file, and for the generators an optional
// passphrase for the private key shown once.
func (a *App) keyForm(c *ui.Context, k keysState) {
	t := c.Theme()
	f := k.form
	edit := func(e ui.Element) {
		if e.Changed() {
			a.mu.Lock()
			a.keys.form = f
			a.mu.Unlock()
		}
	}
	ui.Column(c.Key("key-form")).Gap(8).FillWidth().Children(func() {
		edit(ui.TextInput(c.Key("key-name"), &f.name).Placeholder(L.KeysName()).Label(L.KeysName()).FillWidth())
		switch f.kind {
		case "import", "authorize":
			hint := L.KeysPastePrivate()
			if f.kind == "authorize" {
				hint = L.KeysPastePublic()
			}
			ui.Row(c).Gap(gap).AlignItems(ui.Center).FillWidth().Children(func() {
				ui.Text(c, hint).FontSize(sizeSmall).TextColor(t.TextMuted).Grow(1)
				if ui.Button(c.Key("key-file"), L.KeysChooseFile()).Clicked() {
					a.chooseKeyFile(f.kind == "authorize")
				}
			})
			edit(ui.TextArea(c.Key("key-text"), &f.text).FillWidth().MinHeight(inputAreaH).FontFeatures("tnum"))
		}
		if f.kind != "authorize" && (f.kind != "import" || f.locked) {
			label := L.KeysPassphraseOptional()
			if f.kind == "import" {
				label = L.KeysPassphrase()
			}
			edit(ui.TextInput(c.Key("key-pass"), &f.passphrase).Password().Placeholder(label).Label(label).FillWidth())
		}
		if f.note != "" {
			ui.Text(c, f.note).FontSize(sizeSmall).TextColor(t.Danger)
		}
		ui.Row(c).Gap(gap).Children(func() {
			ui.Row(c).Grow(1)
			if ui.Button(c.Key("key-cancel"), L.ActionCancel()).Clicked() {
				a.openKeyForm("")
			}
			label := map[string]string{"generate": L.KeysGenerate(), "import": L.KeysImport(), "authorize": L.KeysAuthorize(), "pair": L.KeysPair()}[f.kind]
			if ui.PrimaryButton(c.Key("key-go"), label).Disabled(k.busy || (f.kind == "import" || f.kind == "authorize") && strings.TrimSpace(f.text) == "").Clicked() {
				a.submitKeyForm(f)
			}
		})
	})
}

// chooseKeyFile reads a key file into the form open (a dialog: off the main
// thread).
func (a *App) chooseKeyFile(public bool) {
	go func() {
		var filters []mygo.FileFilter // a private key's file has no extension
		if public {
			filters = []mygo.FileFilter{{Name: L.KeysFileFilter(), Extensions: []string{"pub"}}}
		}
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: L.KeysChooseFile(), Filters: filters, ShowHiddenFiles: true})
		if err != nil || len(paths) == 0 {
			return
		}
		data, err := os.ReadFile(paths[0])
		a.update(func() {
			if err != nil || len(data) > 64<<10 {
				a.keys.form.note = L.KeysUnreadable()
				return
			}
			a.keys.form.text = string(data)
			if a.keys.form.name == "" {
				a.keys.form.name = strings.TrimSuffix(baseName(paths[0]), ".pub")
			}
		})
	}()
}

// baseName is a path's last element, by either separator.
func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// submitKeyForm runs the form: generates or imports a key for this device,
// or authorizes a public key (or a new pair's) to connect here.
// Side effects: the engine keeps the key; a generated private key shows
// once; the form's secrets are cleared.
func (a *App) submitKeyForm(f keyForm) {
	name := strings.TrimSpace(f.name)
	if name == "" {
		name = L.KeysDefaultName()
	}
	a.mu.Lock()
	a.keys.form.passphrase, a.keys.form.note = "", ""
	if f.kind == "import" {
		a.keys.form.text = ""
	}
	a.mu.Unlock()
	fail := func(note string) {
		a.update(func() {
			a.keys.form = f
			a.keys.form.passphrase, a.keys.form.note = "", note
			if f.kind == "import" {
				a.keys.form.text = f.text
			}
		})
	}
	switch f.kind {
	case "generate", "pair":
		key, privatePEM, err := newKeyPair(name, f.passphrase)
		if err != nil {
			fail(L.KeysFailed())
			return
		}
		shown := &shownKey{title: L.KeysShownOnce(name), pem: privatePEM, file: "id_ed25519"}
		if f.kind == "pair" {
			shown.pair = true
			a.keysCommand(localapi.Request{Type: localapi.RequestAuthorizedAdd, Authorized: &localapi.AuthorizedKey{Name: name, Public: key.Public}}, "", shown)
			return
		}
		seed := base64.StdEncoding.EncodeToString(key.Private)
		a.keysCommand(localapi.Request{Type: localapi.RequestKeysAdd, LoginKey: &localapi.LoginKey{Name: name, Type: key.Type, Public: key.Public, Private: &seed}}, "", shown)
	case "import":
		key, err := importKey([]byte(f.text), f.passphrase)
		switch {
		case errors.Is(err, errKeyLocked):
			f.locked = true
			fail("")
			return
		case errors.Is(err, errKeyPassphrase):
			fail(L.KeysWrongPassphrase())
			return
		case errors.Is(err, errKeyType):
			fail(L.KeysUnsupported())
			return
		case err != nil:
			fail(L.KeysUnreadable())
			return
		}
		private := base64.StdEncoding.EncodeToString(key.Private)
		a.keysCommand(localapi.Request{Type: localapi.RequestKeysAdd, LoginKey: &localapi.LoginKey{Name: name, Type: key.Type, Public: key.Public, Private: &private}}, L.KeysImported())
	case "authorize":
		line := strings.TrimSpace(f.text)
		if fp, _ := keyFingerprint(line); fp == "" {
			fail(L.KeysUnreadable())
			return
		}
		if _, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(line)); err == nil && strings.TrimSpace(f.name) == "" && comment != "" {
			name = comment
		}
		a.keysCommand(localapi.Request{Type: localapi.RequestAuthorizedAdd, Authorized: &localapi.AuthorizedKey{Name: name, Public: line}}, L.KeysAuthorized())
	}
}

// keysCommand sends a key request, then fetches the lists again; done is a
// toast for success, and shown a private key to show once after it.
func (a *App) keysCommand(req localapi.Request, done string, shown ...*shownKey) {
	a.mu.Lock()
	a.keys.busy = true
	a.mu.Unlock()
	go func() {
		_, err := a.requestWith(req)
		a.update(func() {
			a.keys.busy, a.keys.loaded = false, false
			var refused *localapi.RequestError
			switch {
			case errors.As(err, &refused):
				a.setToast(refused.Message, true)
			case err != nil:
				a.setToast(L.EngineDown(), true)
			default:
				a.keys.form = keyForm{}
				if len(shown) > 0 {
					a.keys.shown = shown[0]
				}
				if done != "" {
					a.setToast(done, false)
				}
			}
		})
	}()
}

// shownKeyCard shows a private key once, to copy or save to a file, with
// Done, after which it is gone.
func (a *App) shownKeyCard(c *ui.Context, k shownKey) {
	t := c.Theme()
	card(c.Key("shown-key")).Border(2, t.Accent).Children(func() {
		ui.Text(c, k.title).FontSize(sizeHeading).Bold()
		ui.Text(c, L.KeysShownHint()).FontSize(sizeSmall).TextColor(t.Warning)
		ui.Text(c, k.pem).FontSize(sizeSmall).FontFeatures("tnum").Selectable()
		ui.Row(c).Gap(gap).Children(func() {
			if ui.Button(c.Key("shown-copy"), L.ActionCopy()).Clicked() {
				mygo.Clipboard.WriteText(k.pem)
				a.update(func() { a.setToast(L.Copied(), false) })
			}
			if ui.Button(c.Key("shown-save"), L.KeysSave()).Clicked() {
				go a.saveKeyFile(k)
			}
			ui.Row(c).Grow(1)
			if ui.PrimaryButton(c.Key("shown-done"), L.KeysDone()).Clicked() {
				a.mu.Lock()
				a.keys.shown = nil
				a.mu.Unlock()
			}
		})
	})
}

// saveKeyFile writes a private key shown once to a file the person chooses,
// readable by them alone. Notes: a dialog: call off the main thread.
func (a *App) saveKeyFile(k shownKey) {
	path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Title: L.KeysSave(), DefaultPath: k.file})
	if err != nil || path == "" {
		return
	}
	err = os.WriteFile(path, []byte(k.pem), 0o600)
	a.update(func() {
		if err != nil {
			a.setToast(L.KeysSaveFailed(), true)
		} else {
			a.setToast(L.KeysSaved(), false)
		}
	})
}

// savedFlags are the sign-ins a device keeps, in the order the chips show:
// the name the forget request takes and the chip's words.
func savedFlags(s localapi.Saved) [][2]string {
	var out [][2]string
	for _, f := range []struct {
		on         bool
		name, text string
	}{
		{s.Password, "password", L.SavedPassword()}, {s.Key, "key", L.SavedKey()},
		{s.SSH, serviceSSH, L.ServiceSSH()}, {s.RDP, serviceRDP, L.ServiceRDP()}, {s.VNC, serviceVNC, L.ServiceVNC()},
	} {
		if f.on {
			out = append(out, [2]string{f.name, f.text})
		}
	}
	return out
}

// savedCard is "Saved passwords and keys": per device, a chip for each
// sign-in kept for it (flags only, never a secret), each with Forget, and
// Forget all.
func (a *App) savedCard(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	var recent []localapi.RecentDevice
	if a.status != nil {
		recent = a.status.Recent
	}
	prefs := a.prefs
	a.mu.Unlock()
	card(c.Key("saved")).Children(func() {
		ui.Text(c, L.SavedTitle()).FontSize(sizeHeading).Bold()
		ui.Text(c, L.SavedHint()).FontSize(sizeSmall).TextColor(t.TextMuted)
		listed := false
		for _, r := range recent {
			flags := savedFlags(r.Saved)
			if r.SavedPassword && !r.Saved.Password {
				flags = append([][2]string{{"password", L.SavedPassword()}}, flags...)
			}
			if len(flags) == 0 {
				continue
			}
			listed = true
			device := r.DeviceID
			ui.Row(c.Key("saved-" + device)).Gap(gap).AlignItems(ui.Center).FillWidth().Children(func() {
				deviceAvatar(c, prefs.named(device, r.About))
				ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
					ui.Text(c, deviceTitle(prefs.named(device, r.About), device)).SingleLine()
					ui.Row(c).Wrap().Gap(6).Children(func() {
						for _, f := range flags {
							savedChip(c, "saved-"+device+"-"+f[0], f[1], func() { a.forgetSaved(device, f[0]) })
						}
					})
				})
				if ui.Button(c.Key("saved-all-"+device), L.SavedForgetAll()).Clicked() {
					a.forgetSaved(device, "")
				}
			})
		}
		if !listed {
			ui.Text(c, L.SavedNone()).FontSize(sizeSmall).TextColor(t.TextMuted)
		}
	})
}

// savedChip is a saved sign-in's chip: its name and × to forget it.
func savedChip(c *ui.Context, key, text string, forget func()) {
	t := c.Theme()
	ui.Row(c.Key(key)).Gap(2).AlignItems(ui.Center).Padding(2, 4, 2, 10).Radius(chipRadius).Background(t.Accent.Alpha(0.12)).Children(func() {
		ui.Text(c, text).FontSize(sizeSmall)
		if forget == nil {
			return
		}
		label := L.SavedForget(text)
		x := ui.ButtonBase(c.Key(key+"-x")).Padding(1, 6).Radius(4).Label(label).Tooltip(label)
		x.Children(func() { ui.Text(c, "×").FontSize(sizeSmall).TextColor(t.TextMuted) })
		if x.Clicked() {
			forget()
		}
	})
}

// forgetSaved forgets one sign-in kept for a device ("password", "key",
// "ssh", "rdp", "vnc"), or all of them ("").
func (a *App) forgetSaved(device, what string) {
	req := localapi.Request{Type: localapi.RequestCredentialsForget, DeviceID: device, Service: what}
	if what == "" {
		req = localapi.Request{Type: localapi.RequestForgetPassword, DeviceID: device}
	}
	go func() {
		_, err := a.requestWith(req)
		a.update(func() {
			var refused *localapi.RequestError
			switch {
			case errors.As(err, &refused):
				a.setToast(refused.Message, true)
			case err != nil:
				a.setToast(L.EngineDown(), true)
			default:
				a.setToast(L.SavedForgotten(), false)
			}
		})
	}()
}

// watchersCard is the Permissions page's list of the devices watching this
// computer's status, with when they started.
func (a *App) watchersCard(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	var watchers []localapi.Watcher
	if a.status != nil {
		watchers = a.status.Watchers
	}
	prefs := a.prefs
	a.mu.Unlock()
	card(c.Key("watchers")).Children(func() {
		ui.Text(c, L.WatchersTitle()).FontSize(sizeHeading).Bold()
		ui.Text(c, L.WatchersHint()).FontSize(sizeSmall).TextColor(t.TextMuted)
		if len(watchers) == 0 {
			ui.Text(c, L.WatchersNone()).FontSize(sizeSmall).TextColor(t.TextMuted)
		}
		for _, w := range watchers {
			about := prefs.named(w.DeviceID, w.About)
			ui.Row(c.Key("watcher-" + w.DeviceID)).Gap(gap).AlignItems(ui.Center).Children(func() {
				deviceAvatar(c, about)
				ui.Column(c).Grow(1).Gap(2).Children(func() {
					ui.Text(c, deviceTitle(about, w.DeviceID))
					ui.Text(c, L.WatchersSince(time.Unix(w.Since, 0).Local().Format("15:04"))).FontSize(sizeSmall).TextColor(t.TextMuted)
				})
			})
		}
	})
}
