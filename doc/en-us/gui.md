# GUI

The interface is for people using Kyokai, not an engineering console (`internal/gui`).
`internal/gui/gui_test.go` renders every page in each language and theme and writes screenshots
when `SCREENSHOT_DIR` is set.

## Structure

- **Sidebar:** Home, Settings.
- **Home:** "This device" — the Device ID in groups of three with Copy; the temporary password,
  masked until Show, with Copy and New password; a note when secrets are not persisted.
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
- The app icon is original artwork: two screens mirrored across a boundary line (鏡界).

## Tray

- Menu: the Device ID (disabled), Show Kyokai, Quit Kyokai. It never shows the password.
- Closing the window hides it to the tray and says so the first time; without a tray, or with
  "Quit" chosen for the close button, closing quits.

## Languages

English, Simplified Chinese, Traditional Chinese and Japanese through MyGo's ARB catalogs
(`go tool mygo i18n check`). The app follows the system language and can be switched manually.
