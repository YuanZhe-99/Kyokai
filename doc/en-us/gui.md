# GUI

The interface is for people using Kyokai, not an engineering console (`internal/gui`).
`internal/gui/gui_test.go` renders every page in each language and theme and writes screenshots
when `SCREENSHOT_DIR` is set.

## Structure

- **Main window:** the tab strip is the window's title bar (MyGo's hidden title bar,
  `TitleBarOptions`): the window's own controls sit in it — the traffic lights on macOS, minimize,
  maximize and close on Windows, as tall as the strip — then the Kyokai tab (Home) and one tab per
  connected device ([Connections](#connections)); its empty part moves the window, and a double
  click zooms or maximizes it as a title bar does. The strip hides in full screen.
- **Sidebar:** Kyokai's mark and name, then Home, Network, AI, Permissions, Security, General and
  About, each with its icon (original line drawings in `internal/gui/assets/icons`), left-aligned.
  There is no separate Settings page: every page but Home is an area of settings.
- **Home**, laid out as RustDesk's: on the left "This device" — the Device ID in groups of three
  with Copy; the temporary password, masked until Show, with Copy and New password (when only
  the permanent password is accepted, a line saying so instead, or a warning that none is set; when
  no password is accepted, a line saying other devices sign in with a key or ask); a note when
  secrets are not persisted, and when the saved identity cannot be unlocked, Start over (the old
  store moved aside, a new identity kept from then on; `secrets.start_over`). On the right
  "Control another device": a Device ID or an address (an IP address or domain name, for a
  device that accepts connections by address; "Looking for the device…" while it is asked who it
  is), Options (also ask to run AI tasks there) and Connect (a Device ID needs a network). On macOS, while Screen Recording, Accessibility, Input Monitoring or the
  Keychain is missing, "Privacy permissions" with Allow for each (Permissions always shows it there);
  a permission asked for in this run also offers "Reopen Kyokai", since macOS counts a new grant
  only in an engine started after it. In the helper modes the card says the permissions are
  Kyokai Helper's, and a missing one offers Restart Kyokai Helper. Home is wider than the other
  pages (`homeMaxW`, 1120). Below, **Recent**: a grid of device cards — one per device, never one
  per session — each at least 300 wide, one to three side by side (`homeColumnsFor`). A card has a
  header (avatar, its name — the one given here, else its friendly name — and Device ID, its state
  as a chip: connected, connecting, online or offline, and ⋯); its route, a menu (`cardPath`): a dot
  and the route in use while connected, else the route chosen by hand ("… · chosen") or the one
  Auto would take now — "Direct · 12 ms", "Relayed · 231 ms" in the warning colour, an address
  "192.168.1.20 · 2 ms" — why the last attempt failed, or when it was last connected; the menu has
  Auto, the Kyokai network and each address with its latency now or why it does not answer, then
  Addresses… ([networking.md](networking.md), "Routes"); its live status; chips for what is saved for it
  (Kyokai password, key, SSH, Remote Desktop, VNC; flags only); and its actions: Connect (Show
  while connected or connecting), then only what will work without the other device's user
  (`cardActions`): Remote Desktop, SSH and VNC for the services its status reports answering, and
  AI (Connect with AI) while its AI is ready, each only when its status says the saved password or
  key grants it (`offers`); the most used for that device first (`Prefs.Uses`, counted on this
  device only), else in that order; the buttons share the row. Everything else stays in the ⋯
  menu, which has Connect, Connect with AI, Open Remote Desktop / SSH / VNC,
  Addresses… (an editor card above the devices, like Rename: every route with its latency, when it
  was last used and the bitrate it reached, Remove for an address, a field to add one, Route: the
  same choice as the route menu, and Done; Home looks at the shown devices' addresses every 60 s
  while visible), Rename… (a name shown only on this device, edited in a card above the devices; empty goes back to
  the device's own), Saved passwords and keys… (the Security page), Forget old identity (after a
  key change) and Remove (disconnects it, takes it and its finished connections off Recent, and
  forgets its pinned key, saved sign-ins and the name given here).
- **Live status on Home:** while Home shows (the main window visible, on its Kyokai tab and the
  Home page), the GUI leases a watch of the recent devices (`watch.set`, eight at most, renewed
  every 10 s and dropped as Home hides). A watched device's card shows compact tiles in two
  columns, after the system monitor Pulse: each metric's icon and name in its own colour (CPU
  blue, its system share red; memory green; GPU red; disk orange, writes blue; network teal, sent
  blue; battery green, orange when low), a large figure with its unit (CPU now, memory in use, GPU
  now, disk free, download rate, battery), and the last two minutes as a dot history — columns of
  dots, newest on the right, each lit from the bottom as far as its value reaches, the parts of a
  stacked metric in their colours (`dotHistory`). GPU and battery are hidden when the host has
  none to report; uptime follows the tiles. A tile opens the device's **status details**
  (`hostStatusView`, Home's own page until Back to Home or another page): every tile in full, up
  to three side by side, with its caption ("Now", "In use of 16 GB", "Free of 512 GB",
  "Downloading", "Charging"), the figure, three smaller ones (CPU: apps, system, cores; memory:
  used, available, total; GPU: average and peak of the history; disk: reading, writing, used;
  network: uploading, received, sent) and a taller history. Never stale data as current: a sample
  up to 6 s old shows as it is; from 6 to 60 s the tiles are dimmed with "Updated N s ago"; older,
  no values show, only "Status from HH:MM", and the histories are cleared. Without a sample the card says how the watch goes:
  "Getting its status…" with the loading indicator, no saved password or key, needs Kyokai 0.4.1
  there, or refused.

- **Confirmations:** a controller waiting for this host's user shows at the top of every page with
  its Device ID, whether it is known or a new key claiming a known ID, a warning when it gave no
  password, and a switch per requested
  permission, with Allow (the primary button) and Deny; on macOS a warning when Screen Recording or
  Accessibility is missing for what it asks; a notification and the window coming forward announce
  it.
- **Connected here:** while another device is connected to this one (or resuming), a card at the
  top of every page names it, says what it may do, and has Disconnect.
- **AI:** the task card — a new task's instruction and Start (with a note when the provider has no
  key; the instruction being written is shared live with every connected controller's AI panel),
  or the task in progress: its state in words (working, waiting for approval, paused and
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
- **Network:** the networks kept (up to 8), with a note that this device has the same Device ID in
  each. Each shows its name here (or its network name) with the network name under it, a Join
  switch (applied at once; refused with the engine's reason, after which the page reads the
  networks again), Edit and Remove (after a confirmation: Kyokai leaves it and forgets its servers,
  name and secret), its live state (connected, connecting, could not connect with the reason, or
  not joined; the virtual address; the NAT type in plain words; the port for direct connections
  while it listens) and the Kyokai devices it routes to (the name a device gave in its last session
  here, if any, its Device ID, direct or relayed, latency, Copy and Connect). Add a network and
  Edit open the same form in place: the name shown here (optional), servers (one URL per line),
  network name, and a write-only secret field (empty keeps the stored secret; a weak secret is
  flagged), Save (once there is a server and a name) and Cancel; refused settings show the
  engine's reason. A note says announced Device IDs are not proof. Then how devices reach this one: Accept connections by address, off by default, and while on
  the accepted ranges, one per line, with Save and Cancel, and a warning when it cannot listen.
  Last, **Advanced**: Try direct connections (off by default; on and fixed in the helper modes;
  applied as it is switched, [networking.md](networking.md)), with a warning that it makes this
  device listen and easier to find, which a protocol choice does not; and Latency-first routing.
- **Device names:** wherever a device appears (a confirmation, Recent, the connection tabs, trusted
  devices, the viewer's title, the Network page) its name — the one this device's user gave it
  (`gui.json` `aliases`, never sent anywhere), else the friendly name it gives itself — goes beside
  the Device ID, never instead of it,
  with a round avatar (`deviceAvatar`): the vendor's monochrome logo when the device's maker,
  model, name or system (tried in that order; whole words only, so `hp` does not match `php-dev`)
  names a known maker or system (`deviceLogoName`), else the name's first letter. Vendor logos are
  trademarks of their owners, used only to identify a device's maker; their sources are listed in
  `internal/gui/assets/logos/SOURCES.md` (see [packaging.md](packaging.md), "Vendor logos").
- **Permissions:** on macOS "Privacy permissions" first; Passwords and keys (permanent password:
  set, change or remove, its hint only Not set or Set; Ways to sign in: a check box for the
  temporary password, the one shown on Home, one for the permanent password, with a warning under
  it while none is set, and one per key that can connect — its name, with its type and `SHA256:`
  fingerprint under it, Copy public key and Remove after a confirmation — in any combination; with
  none checked every connection asks, and with nobody at the computer no one gets in; Add a key
  from an OpenSSH line or a `.pub` file, its comment naming it; Generate a pair — the public key
  may connect here, the private part shown once on this page and never stored; Connect without
  asking, on by default, which admits a checked way to sign in without asking — with nobody at the
  computer only the permanent password or a key —, and below it "With a correct password": a switch per permission a password grants
  without asking, among them See this computer's status, on by default, and Connect to its SSH,
  Remote Desktop, and VNC or Screen Sharing, off by default; then "Ports of this computer's
  services": a port each for SSH, Remote Desktop and VNC, empty for the standard one, with Save and
  Cancel while edited); the devices watching this computer's
  status, with since when; trusted devices with their last connection and Remove; Sound to share — what
  this computer plays unless an input device is chosen, a device that is gone shown as not
  connected, an input asking macOS for the Microphone permission.
- **Security:** Device identity (Device ID, friendly name, fingerprint; back up and restore with a
  passphrase through a file dialog; reset, after a warning); Your keys (name, type and `SHA256:` fingerprint, Copy public key, Remove
  after a confirmation; Generate a key — Ed25519, its private part shown once in OpenSSH form with
  Copy and Save to a file…, encrypted with the passphrase given, then kept by the engine; Import a
  key — pasted or from a file, OpenSSH or PEM, the passphrase asked for an encrypted one; Ed25519,
  RSA of 2048 bits or more, ECDSA P-256 or P-384) and Saved passwords and keys (each device with
  something kept: a chip per sign-in — the Kyokai password, the admitted key, each service's
  sign-in — with × to forget that one, and Forget all); Protected mode (only with a permanent
  password, after a confirmation); Privacy: hide Kyokai from screen capture — on by default,
  applied to the main window and every viewer at once and told to the engine, whose macOS capture
  follows it; turned off (after a warning), Kyokai's windows also take the input of the people
  controlling this device, never the AI's and nobody's while a request or an approval waits —
  how secrets are kept, and No logs — off by default, turning it on after a warning
  that nobody can tell any more who connected or whom this device connected to, with a note when
  the engine in use cannot do it yet.
- **General:** language, appearance, open at login — off by default; started at login, Kyokai shows no window, only its tray icon (on a Mac, as a login item, asked once the app is ready: asked earlier, macOS answered no), unless Start in the tray when opened at login (on by default) is off — and the close button; Run
  mode: No privileges / Follow Kyokai / Background service, a note on the one administrator prompt
  and what moves along; switching runs in the background with a loading indicator and a toast
  ([run-modes.md](run-modes.md)); Windows and macOS. While this profile's own engine is parked
  (its identity moved to Kyokai Helper, which is gone), every page shows a card to install the helper
  again or start over here.
- **About:** version, license, source code and app updates; Diagnostics (version and ABI, engine
  start time, run mode, data folder with Open folder, logs folder with Open folder and **Save logs…** — the engine's logs through `logs.read`, the helper's and its agent's in the helper modes, which only administrators open, and this GUI's own, into one text file — or "Off" with No logs); Open
  source licenses (the shipped notices).
- **Protected mode:** the sidebar shows only AI and every page is the AI page, topped by a card
  with the permanent password field and Turn off (a wrong password says so); confirmations and
  the devices connected here still show.
- Until the engine's session is attached, pages that need it show "Starting…" or why the engine is
  not running.
- **App updates (About):** a daily check (switchable) and "Check for updates" read the signed
  update of the latest GitHub release; "Install and restart" stops the engine, installs in place
  and relaunches. Development builds say they cannot update themselves.

## Connections

- One connection per device, shown as a tab from the start, as RustDesk does. Connecting opens the
  tab in the main window: Connecting…; while the device's user is asked, "Waiting for the other
  device to allow it…" with "Or enter its password", Remember password and Connect (the waiting
  request is given up, so the device has one at a time); then the picture. When the connection
  ends or fails, the tab says why (a wrong password among them), with the password field and
  Reconnect. An empty password uses the one saved for the device, else asks its user. Closing a
  tab (×) disconnects, also while it connects or waits for the device's user: that request ends
  at once and never comes back as a tab of its own. A tab dragged onto another Kyokai window's tabs moves there; dropped
  outside Kyokai, or with its ⧉, it gets a window of its own, which closes with its last tab;
  closing such a window disconnects its tabs. A tab dragged over a window's picture area (from
  its own strip or another window's) shows where it may go and splits the area there, as Claude
  Desktop splits sessions: dropped on an edge of a tab's pane it splits that pane, beside or
  above or below that tab, so one side may hold one tab at full height and the other two; dropped
  in a pane's middle it takes that tab's place (`split`, `split.place`, a tree of panes; at most
  four tabs shown, a further one takes a place). The dividers move; while split each place has a thin header with its title and
  Show only this, and the place in focus an accent edge: the focus follows the keyboard into a
  place, and choosing a tab in the strip brings its place into focus or puts it in the one in
  focus. A closed tab gives its room to its neighbour; Home shows over the whole
  window. Every tab shown keeps its connection running; only the one in focus takes the keyboard
  and the clipboard; in Map and Translate the keys go to the tab whose picture is under the
  pointer, which takes the focus with them, and in every mode a picture (a Remote Desktop or VNC
  screen's too) keeps the controller's input method off ([computer-use.md](computer-use.md),
  "Keyboard modes"). A tab
  is a desktop or one of a device's services (SSH, Remote Desktop, VNC; `tab`, `serviceTab`), one
  per device and kind, and every kind drags, moves and closes alike.
- A service tab ([remote-services.md](remote-services.md), "Clients") is titled "SSH · device"
  and the like. It shows "Connecting to SSH…" with the loading indicator, then, when nothing saved
  is remembered, a sign-in form: the way to sign in (a segmented choice — SSH: Key, Password; VNC:
  Password only, User name and password, No sign-in; Remote Desktop: User name and password, On the
  remote screen), the fields that way needs (user name; domain for Remote Desktop; password), a
  hint for Key and the no-sign-in ways, Remember, and Cancel and Sign in (until the fields are
  filled, Sign in is off), then the client: MyGo's terminal for SSH,
  in the app's light or dark theme, or the other desktop's picture for Remote Desktop and VNC, with
  a toolbar of Display (Fit or Original size), Keyboard (the keyboard mode), Actions
  (Ctrl+Alt+Del) and Full screen. When it ends it says why, with Reconnect, and after an SSH
  server's key changed, Trust the new key.
- The picture is scaled to fit on black, or with Original size at one point per pixel of the
  host, scrolled both ways when larger than the window (its scroll bars' thumbs move it; the wheel
  goes to the host while the session controls it) and centred when smaller; the host's pointer is
  marked with a small ring that follows the mouse. A toolbar floats over the picture's top edge,
  as RustDesk's: its grip (or any gap between its controls) drags it along that edge, and where it
  is let go is kept for every viewer (`toolbar_at` in `gui.json`); ⌃ folds it to a small handle
  (the grip and ⌄) at the same place, kept for each device. Its icons, each named by its tooltip:
  Display (the display by number when the host has several; Fit or Original size), Quality (the
  preset, Frame rate with the cap such as "30 fps", Codec with Auto and what both computers do, by
  codec name only, when there is a choice, and Full colour), Keyboard (with `control-input`, on
  Windows and macOS: the keyboard mode Map, Translate or Legacy, remembered for every viewer, and
  Swap Ctrl and Cmd where a Mac is on either side; on a Mac without Kyokai's Accessibility
  permission the icon takes the warning colour and its tooltip says to allow it), Actions (with
  `control-input`, when the host offers them, a Windows host: Lock screen, Ctrl+Alt+Del in the
  helper modes, Block their keyboard and mouse and Privacy mode, checked as the host reports),
  then toggles for Sound (plays the host's sound; on unless chosen, kept for each device), Pointer
  (shows or hides the ring, remembered for every viewer), Clipboard (pulls out the clipboard panel,
  with `clipboard` or `files`), AI (collapses the AI panel) and Full screen; what does not fit a
  narrow window goes into the toolbar's » menu. Beside them the path: a dot and the address the session takes
  (`sessions[].route`), else Direct or Relayed (Relayed in the warning colour, its tooltip with a
  hint to turn on direct connections on both devices), with the host's measured delay, the tooltip also showing size, codec, frame rate and
  bitrate, what encodes and what decodes, each in hardware or software as it reports, and where
  the time goes (the host's encoding, the network, this computer's decoding and drawing, the frame
  rate against the host's target) — or, for the test pattern of a host that may not record its
  screen, a warning sign saying "this is a placeholder picture". Before the first picture the
  picture area says "Waiting for the picture…"; when no picture came for 2 seconds although the
  host sends frames, a strip near the picture's bottom edge says "Recovering the picture…" over
  the last one. A host showing its placeholder gets a status over the dark picture area instead of
  the colour bars, saying what the host says: its screen asleep or off and being woken, no screen
  to show, Kyokai Helper getting ready, the picture starting again, no codec in common, or a screen
  it cannot share. A session with `control-input` offers **Wake it** when the host says its
  display is dark or asleep — an older host, which does not say, after 3 seconds of waiting or
  recovering ([computer-use.md](computer-use.md), "Sleep"). While the
  engine refuses input as behind, the path is led by "Input paused — the connection is behind" in
  the warning colour, and the keyboard grab steps aside after half a second
  ([computer-use.md](computer-use.md), "Input"). After a choice in the toolbar the
  picture takes the keyboard again; the mouse, the wheel and the keyboard on the picture go to the
  host ([computer-use.md](computer-use.md)), a press on the toolbar never does. The quality preset,
  frame-rate cap, Fit or Original size, Full colour, Codec, Swap Ctrl and Cmd, the clipboard switch
  and the folded toolbar are kept for each device (`viewers` in `gui.json`): a device's viewer
  opens with the settings chosen for it last.
- The clipboard panel: "Share the clipboard with this device" (on unless switched off, kept for
  each device) and what crossed the clipboard with the device while its tab was in front, newest
  first (at most 30, in memory only, gone when the tab closes): each entry's source — from the
  device (the host) or from this computer (the controller) — and time, then the text with Copy,
  the image as a thumbnail with its size, or the files' names, count and size; Clear empties it.
  Only the tab in front — shown in the Kyokai window used last; none while that window shows the
  Kyokai tab — exchanges the clipboard ([computer-use.md](computer-use.md), "Clipboard"); the panel
  says so when its tab is behind. Both panels may show, the clipboard's left of the AI's.
- With `run-ai-task` the tab has an AI panel, "AI on the other computer", which the AI switch
  collapses (a step waiting for approval shows it anyway). It shows the host's one AI record,
  the same for every controller whoever started the task (state, the model's text, its latest
  actions, Pause or Resume and Stop, a reply when it is done), and the instruction being written,
  which is shared live with the host's AI page and every other controller (typing here wins for
  a second over a draft typed elsewhere). Approvals have Allow and Deny with
  `approve-ai-action`, otherwise a note that the host's user approves. A connection asks for
  viewing, input, the clipboard and files, and with Connect with AI (or the option) for
  `run-ai-task` and `approve-ai-action` as well.

## Wording rules

- Copy is written for the person using the app.
- Never shown: reason codes, keys, socket paths, internal identifiers other than the Device ID (the
  fingerprint appears only on the Security page, sign-in keys and their fingerprints only there and
  in Permissions' Passwords and keys; saved
  sign-ins show as flags, never their passwords).

## Visual language

- MyGo light/dark themes plus the desktop accent; layout numbers are named constants in
  `style.go`.
- Body text 14, titles 20–24, nothing below 12; the Device ID is 34 with tabular digits.
- Whatever is under way shows one loading indicator, loading-dev's Arc from MyGo's loading plugin in the
  accent colour (white on the viewer's black stage), named for assistive technology by what is loading.
- The app icon is original artwork: a ring split by a boundary line (鏡界), solid on one side and
  an outline on the other, with a pointer reaching across. The tray shows the same mark as a
  one-colour template glyph: white, or black (`tray-black.png`) while Windows draws the taskbar
  light (`SystemUsesLightTheme`), checked with each status update; macOS tints the template
  itself. `go run ./tools/icon resources/icon.png internal/gui/assets/tray.png` draws them all
  from their geometry.

## Tray

- Menu: the Device ID (disabled), Stop AI task while one is in progress (the emergency stop), Show
  Kyokai, Quit Kyokai. It never shows the password.
- Kyokai's windows are excluded from screen capture, so neither a viewer nor an AI task sees them,
  and remote input never reaches them, unless Security's Hide Kyokai from capture is off (the AI
  never reaches them).
- The main window exists only while it is shown: started in the tray Kyokai makes none, and
  closing it to the tray ends it (its place and size are kept for the next one); Show Kyokai, a
  second launch, a connection request, a connection's tab or the Dock make it anew. A hidden
  window kept for later came up at Windows sign-in as a white frame Kyokai took for hidden:
  Windows' rearranging of windows when the displays changed (lock, a dark display, a KVM's
  monitor coming back) made it visible behind MyGo's back. On Windows the GUI log notes every
  window of Kyokai's that turns visible.
- Closing the window puts Kyokai in the tray and says so the first time; without a tray, or with
  "Quit" chosen for the close button, closing quits. Quitting the app itself (its menu, the Dock,
  logging out or shutting down) always quits, never asks, and has the engine end this GUI's
  connections (`sessions.close_controllers`, waiting a second at most).
- Closing a window decides by `decideClose`: the main window with Quit chosen asks while any
  connection is live anywhere, then quits; otherwise a window asks when it holds two live
  connections or more (connecting, waiting, connected or reconnecting; desktops and services
  alike), or one it does not show (the main window on Home), then disconnects its tabs and closes
  (the main window into the tray). The question is a dialog naming the connections, with "Disconnect
  and close" (or "Disconnect and quit") and Cancel.
- On macOS the Dock icon is shown only while a Kyokai window is open (the main window or a
  connection's own window); with only the menu bar icon left, Kyokai leaves the Dock and the app
  switcher, and opening the window brings the icon back.

## Languages

English, Simplified Chinese, Traditional Chinese and Japanese through MyGo's ARB catalogs
(`go tool mygo i18n check`). The app follows the system language and can be switched manually.
