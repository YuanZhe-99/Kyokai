package gui

import (
	"errors"
	"os"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// identityBackup is the identity card's backup, restore and reset part.
// The passphrase field is write-only and cleared after each use.
func (a *App) identityBackup(c *ui.Context) {
	t := c.Theme()
	a.mu.Lock()
	pass, busy, id := a.passphrase, a.busy["identity"], a.status.DeviceID
	a.mu.Unlock()
	ui.Divider(c)
	ui.Text(c, L.BackupTitle())
	ui.Text(c, L.BackupHint()).FontSize(sizeSmall).TextColor(t.TextMuted)
	ui.Row(c).Gap(gap).AlignItems(ui.Center).Children(func() {
		in := ui.TextInput(c.Key("backup-pass"), &pass).Password().Placeholder(L.BackupPassphrase()).Grow(1).Disabled(busy)
		if in.Changed() {
			a.mu.Lock()
			a.passphrase = pass
			a.mu.Unlock()
		}
		if ui.Button(c.Key("backup-save"), L.ActionBackUp()).Disabled(busy || pass == "").Clicked() {
			go a.backUp(pass, id)
		}
		if ui.Button(c.Key("backup-restore"), L.ActionRestore()).Disabled(busy || pass == "").Clicked() {
			go a.restore(pass)
		}
	})
	settingRow(c, "reset-id", L.ResetTitle(), L.ResetHint(), func() {
		if ui.Button(c.Key("reset-go"), L.ActionReset()).Disabled(busy).Clicked() {
			go a.resetIdentity()
		}
	})
}

// backUp asks where to save, then writes the encrypted backup there.
// Side effects: writes a file readable only by this user.
func (a *App) backUp(passphrase, deviceID string) {
	path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
		Parent: a.window, Title: L.ActionBackUp(), DefaultPath: "kyokai-identity-" + deviceID + ".json",
		Filters: []mygo.FileFilter{{Name: L.BackupFileKind(), Extensions: []string{"json"}}},
	})
	if err != nil || path == "" {
		return
	}
	a.identityRequest(localapi.Request{Type: localapi.RequestIdentityExport, Backup: &localapi.IdentityBackup{Passphrase: passphrase}},
		func(resp *localapi.Response) {
			if err := os.WriteFile(path, []byte(resp.Document), 0o600); err != nil {
				a.setToast(err.Error(), true)
				return
			}
			a.setToast(L.BackupSaved(), false)
		})
}

// restore asks for a backup file and replaces this device's identity.
func (a *App) restore(passphrase string) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent: a.window, Title: L.ActionRestore(),
		Filters: []mygo.FileFilter{{Name: L.BackupFileKind(), Extensions: []string{"json"}}},
	})
	if err != nil || len(paths) == 0 {
		return
	}
	doc, err := os.ReadFile(paths[0])
	if err != nil {
		a.update(func() { a.setToast(err.Error(), true) })
		return
	}
	if !a.confirm(L.RestoreConfirm(), L.ActionRestore()) {
		return
	}
	a.identityRequest(localapi.Request{Type: localapi.RequestIdentityImport,
		Backup: &localapi.IdentityBackup{Document: string(doc), Passphrase: passphrase}},
		func(*localapi.Response) { a.setToast(L.IdentityReplaced(), false) })
}

// resetIdentity gives this device a new identity after the user confirms.
func (a *App) resetIdentity() {
	if !a.confirm(L.ResetConfirm(), L.ActionReset()) {
		return
	}
	a.identityRequest(localapi.Request{Type: localapi.RequestIdentityReset},
		func(*localapi.Response) { a.setToast(L.IdentityReplaced(), false) })
}

// confirm asks a yes/no question about a step that cannot be undone.
func (a *App) confirm(message, action string) bool {
	res, err := mygo.Dialog.Message(mygo.MessageOptions{
		Parent: a.window, Type: mygo.MessageWarning, Title: action, Message: message,
		Buttons: []string{action, L.ActionCancel()}, DefaultButton: 1, CancelButton: 1,
	})
	return err == nil && res.Button == 0
}

// identityRequest sends one identity request; then runs on success (on the
// main thread). The passphrase field is cleared either way.
func (a *App) identityRequest(req localapi.Request, then func(*localapi.Response)) {
	a.update(func() { a.busy["identity"] = true })
	resp, err := a.requestWith(req)
	a.update(func() {
		delete(a.busy, "identity")
		a.passphrase = ""
		var refused *localapi.RequestError
		switch {
		case errors.As(err, &refused):
			a.setToast(refused.Message, true)
		case err != nil:
			a.setToast(L.EngineDown(), true)
		default:
			then(resp)
		}
	})
}
