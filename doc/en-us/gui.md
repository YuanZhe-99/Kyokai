# GUI

The interface is for people using Kyokai, not an engineering console (`internal/gui`).
`internal/gui/gui_test.go` renders every page in each language and theme and writes screenshots
when `SCREENSHOT_DIR` is set.

## Structure

- **Sidebar:** Home, Network, AI, Settings.
- **Home:** "This device" — the Device ID in groups of three with Copy; the temporary password,
  masked until Show, with Copy and New password; a note when secrets are not persisted. Below it,
  "Control another device" (Device ID, password, Connect; needs a network) and the sessions, newest
  first (to or from which device, state or why it failed, Disconnect; "Forget old identity" when a
  host's key changed). Recent devices sit under the form as rows (avatar, friendly name, Device ID,
last connection) that fill in the Device ID; a
  dropped session shows "reconnecting" while it resumes. A connected session that may view the
  screen has View, which opens the viewer: its own window with the picture scaled to fit on black,
  the host's pointer marked with a small ring, and a status line (Device ID, the display by number
  when the host has several, quality preset, frame-rate cap, size, codec, frame rate, bitrate,
  the host's measured delay) — with `control-input`, a bar above it with Lock screen,
  Ctrl+Alt+Del (helper modes), the Block their keyboard and mouse and Privacy mode switches and, on Windows, the keyboard mode (Map, Translate, Legacy; remembered for every viewer); closing it stops the stream, not
  the session. With `control-input`, the mouse, the wheel and the keyboard on the picture go to the
  host ([computer-use.md](computer-use.md)). With `run-ai-task` the viewer has an AI panel at its
  side: an instruction and Start, then the host's task as the AI page shows it (state, the model's
  text, its latest actions, Pause or Resume and Stop, a reply when it is done); its approvals have
  Allow and Deny with `approve-ai-action`, otherwise a note that the host's user approves. Such a
  session has View even without `view-screen`. Connecting asks for viewing, input, the
  clipboard and files by default. The connect form's switch "Also ask to run AI
  tasks there" asks for `run-ai-task` and `approve-ai-action` as well.
- **Confirmations:** a controller waiting for this host's user shows at the top of every page with
  its Device ID, whether it is known or a new key claiming a known ID, and a switch per requested
  permission, with Allow and Deny; a notification and the window coming forward announce it.
- **AI:** the task card — a new task's instruction and Start (with a note when the provider has no
  key), or the task in progress: its state in words (working, waiting for approval, paused and
  why, done, stopped), the model's latest text and progress note, its latest actions in words
  ("Click at 640, 425", "Press Ctrl+S") with what became of them, Pause or Resume and Stop; an
  action waiting for approval shows in a framed box with Allow and Deny and the provider's notes.
  A finished task can be continued with a reply. Below: the providers card — the model tasks use (every model of every
  source by its `Vendor: Model` name), the approval policy, the sources with their template, model
  count and key state (Edit, Remove), and Add provider from a template; the editor has the name,
  the address (a template's sites by name), the format for Custom, a write-only key, the models
  with Remove, an id to add and List models (a saved source) and the task history with
  Clear history. While the task waits for approval, every other page shows a banner with Review,
  and a notification announces it. A controller's task shows "Started from" its Device ID, and
  while it works every other page shows a banner with Review and Stop.
- **Network:** the node's live status (connected, connecting, or why it could not connect; the
  virtual address; the NAT type in plain words) and the Kyokai devices it routes to (Device ID,
  direct or relayed, latency), with a note that announced Device IDs are not proof; then the
  settings form: servers (one URL per line), network name, and a write-only secret field (empty
  keeps the stored secret; a weak secret is flagged), Try direct connections (off by default;
  on and fixed in the helper modes, [networking.md](networking.md)), Save and Leave network. Refused settings show
  the engine's reason.
- **Device names:** wherever a device appears (a confirmation, Recent, the sessions, trusted
  devices, the viewer's title) its friendly name goes beside the Device ID, never instead of it,
  with a round avatar (`deviceAvatar`): the vendor's monochrome logo when the device's maker,
  model, name or system (tried in that order; whole words only, so `hp` does not match `php-dev`)
  names a known maker or system (`deviceLogoName`), else the name's first letter. Vendor logos are
  trademarks of their owners, used only to identify a device's maker; their sources are listed in
  `internal/gui/assets/logos/SOURCES.md` (see [packaging.md](packaging.md), "Vendor logos").
- **Run mode** (Settings): No privileges / Follow Kyokai / Background service, a note on the one
  administrator prompt and what moves along; switching runs in the background with a toast
  ([run-modes.md](run-modes.md)). Diagnostics shows the mode in use.
- **Settings:** General (language, appearance, open at login — off by default, hide Kyokai from
  screen capture — on by default, applied to the main window and every viewer at once, close
  button),
  Device identity (Device ID, fingerprint; back up and restore with a passphrase through a file
  dialog; reset, after a warning), Access (permanent password: set, change or remove;
  trusted devices with their last connection and Remove), Storage (data folder with Open folder, how secrets are
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
  one-colour template glyph: white, or black (`tray-black.png`) while Windows draws the taskbar
  light (`SystemUsesLightTheme`), checked with each status update; macOS tints the template
  itself. `go run ./tools/icon resources/icon.png internal/gui/assets/tray.png` draws them all
  from their geometry.

## Tray

- Menu: the Device ID (disabled), Stop AI task while one is in progress (the emergency stop), Show
  Kyokai, Quit Kyokai. It never shows the password.
- Kyokai's windows are excluded from screen capture, so neither a viewer nor an AI task sees them.
- Closing the window hides it to the tray and says so the first time; without a tray, or with
  "Quit" chosen for the close button, closing quits.

## Languages

English, Simplified Chinese, Traditional Chinese and Japanese through MyGo's ARB catalogs
(`go tool mygo i18n check`). The app follows the system language and can be switched manually.
