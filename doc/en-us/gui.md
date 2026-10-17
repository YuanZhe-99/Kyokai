# GUI

The interface is for people using Kyokai, not an engineering console (`internal/gui`).
`internal/gui/gui_test.go` renders every page in each language and theme and writes screenshots
when `SCREENSHOT_DIR` is set.

## Structure

- **Sidebar:** Home, Network, Settings.
- **Home:** "This device" — the Device ID in groups of three with Copy; the temporary password,
  masked until Show, with Copy and New password; a note when secrets are not persisted.
- **Network:** the node's live status (connected, connecting, or why it could not connect; the
  virtual address; the NAT type in plain words) and the Kyokai devices it routes to (Device ID,
  direct or relayed, latency), with a note that announced Device IDs are not proof; then the
  settings form: servers (one URL per line), network name, and a write-only secret field (empty
  keeps the stored secret; a weak secret is flagged), Save and Leave network. Refused settings show
  the engine's reason.
- **Settings:** General (language, appearance, open at login — off by default, close button),
  Device identity (Device ID, fingerprint), Storage (data folder with Open folder, how secrets are
  kept), Diagnostics (version and ABI, engine start time, run mode, logs folder), About (version,
  license, source code, app updates), Open source licenses (the shipped notices).
- Until the engine's session is attached, pages that need it show "Starting…" or why the engine is
  not running.
- **App updates (About):** a daily check (switchable) and "Check for updates" read the signed
  update of the latest GitHub release; "Install and restart" stops the engine, installs in place
  and relaunches. Development builds say they cannot update themselves.

## Wording rules

- Copy is written for the person using the app.
- Never shown: reason codes, keys, socket paths, internal identifiers other than the Device ID (the
  fingerprint appears only on the identity card).

## Visual language

- MyGo light/dark themes plus the desktop accent; layout numbers are named constants in
  `style.go`.
- Body text 14, titles 20–24, nothing below 12; the Device ID is 34 with tabular digits.
- The app icon is original artwork: a ring split by a boundary line (鏡界), solid on one side and
  an outline on the other, with a pointer reaching across. The tray shows the same mark as a
  one-colour template glyph. `go run ./tools/icon resources/icon.png internal/gui/assets/tray.png`
  draws both from their geometry.

## Tray

- Menu: the Device ID (disabled), Show Kyokai, Quit Kyokai. It never shows the password.
- Closing the window hides it to the tray and says so the first time; without a tray, or with
  "Quit" chosen for the close button, closing quits.

## Languages

English, Simplified Chinese, Traditional Chinese and Japanese through MyGo's ARB catalogs
(`go tool mygo i18n check`). The app follows the system language and can be switched manually.
