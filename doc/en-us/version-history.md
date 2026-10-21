# Version history

## 0.4.1 (2026-10-21, test build)

- A run-mode switch moves everything: the move carried only the setting name networks had before
  0.3.2, so networks, saved passwords and access settings stayed behind and Helper mode looked like
  another device. The whole store and its files now move, another identity with its own settings
  is never replaced unasked, an engine whose store moved out stays parked instead of making a new
  identity, and the Windows helper removes itself without a prompt. The first start of 0.4.1 removes
  what earlier switches left in the run mode not in use.
- The picture never waits for one lost message: a key frame dropped at high latency (the helper
  path at about 230 ms froze the picture while the delay looked normal) is asked for again at every
  layer, a still screen still answers, a capture or encoder that fails reopens, unacknowledged
  frames are released after 3 s, and capture waits for room. A dark display wakes; the viewer
  offers Wake it and says when it is recovering the picture. In Helper mode the session agent
  follows the input desktop and keeps a log; the lock and sign-in screens show (a capture checks the
  input desktop whatever it captured, never keeps copying the old desktop, and gets a still screen's
  first picture from GDI).
- Input never jams: it never waits for a stalled session, releases always arrive, presses typed
  during a stall or a reconnect are dropped instead of arriving late, the keyboard grab steps aside
  while a session is stalled, and Map mode keeps the controller's input method out of the way while
  Translate mode lets it finish composing (Chinese and Japanese input could leave the controller's
  keyboard dead). Kyokai running inside a RustDesk session takes the keys RustDesk types
  (on Windows they were left to the window and lost).
- Sign in with SSH-style Ed25519 keys: keys that can connect to this device, your own keys
  (generated or imported; a private key is shown once), a proof bound to the session.
- SSH, Remote Desktop and VNC (Apple Screen Sharing too) of a device through tunnels inside Kyokai
  sessions, with built-in clients (MyGo's terminal; RDP and VNC from open-screen-sharing,
  GPL-3.0-or-later); each is a permission, off by default; their passwords and keys are saved with
  the device's Kyokai password and forgotten together, and Security lists what is saved. A VNC
  server on Windows that refuses its own computer or offers no supported sign-in says which setting
  to change.
- Home is a grid of device cards with the host's live status for devices with a saved password or
  key, watched quietly: Pulse-style tiles (a large figure and a dot history of two minutes) for CPU
  (apps and system), memory, GPU, disk (with reads and writes), network (with totals) and battery,
  and a details page per device. A card offers Remote Desktop, SSH, VNC and AI only when they work
  without the other device's user (the host says what the saved password grants and whether its AI
  is ready), the most used first.
- Closing a window with connections asks first when it holds two or more, or one while Home is
  shown, and disconnects them; closing the main window no longer leaves its connections running.
- Network: every node refuses EasyTier's QUIC and KCP proxy input and foreign networks; Try direct
  connections moves into Advanced with a warning; latency-first routing. Loading indicators from
  MyGo's loading plugin everywhere.
- Protocol 10 with feature announcements (`key-login`, `tunnel`, `status`, `input-sync`): 0.4.1 and
  0.4.0 connect both ways with what both have (checked); local API 2; ABI 5.
- Upstream versions: MyGo v0.6.2 (from v0.5.5); EasyTier v2.7.0, RustDesk 1.5.0 (vendored code),
  Rust 1.99.1 checked, unchanged; vcpkg 2026.10.16 (from 2026.07.29: aom 3.15.2, Opus 1.6.1, libvpx
  1.16.0 unchanged).

## 0.4.0 (2026-10-19)

- A Windows host is faster: a still screen no longer counts as a lost display (capture was torn
  down and rebuilt after every quiet moment); a stream that restarts, changes codec or resumes after
  a drop gets the hardware encoder back instead of falling to software; the GPU scales a large
  desktop before it is copied; capture that fell back to GDI tries desktop duplication again every
  2 s; the pointer and a person's input stay off the network's threads.
- Windows: started at login with the helper installed, Kyokai no longer leaves a blank window (it
  ran a console program to read the helper's version).
- Kyokai's windows follow Hide Kyokai from capture (Security): with it off (it asks first), a
  person controlling this device can also use Kyokai's own windows; the host's AI never can, and
  nobody can while a connection request or an AI approval waits. On macOS, Kyokai's windows are now
  guarded in the helper modes too.
- macOS: the first connection request could freeze the app (it announced the request under its
  lock while macOS asked about notifications); a watchdog writes the stacks of a frozen GUI to
  `gui-crash.log`. Capture and sound give up at once without Screen Recording, a program without a
  bundle identifier no longer panics the capture filter, streams are stopped before they are
  released, and every framework callback catches its own panic. A Mac host stays awake while
  someone is connected.
- The engine's stderr, its panics and what each session's capture, encoder and sound opened go to
  `engine.log` while logs are on.
- The viewer's two bars are one toolbar floating over the picture, as RustDesk's: Display, Quality,
  Keyboard and Actions menus, toggles for Sound, Pointer, Clipboard, AI and Full screen, and the
  path to the device. Its grip drags it along the top edge (the place is kept for every viewer);
  it folds to a small handle (kept for each device).
- Protocol 10, unchanged; local API 2 with the new request `input.kyokai` (an older engine refuses
  it and keeps Kyokai's windows guarded).
- Upstream versions checked, unchanged: MyGo v0.5.5, EasyTier v2.7.0, RustDesk 1.5.0 (vendored
  code), vcpkg 2026.07.29, Rust 1.99.1.

## 0.3.2 (2026-10-19)

- Several networks at once: the Network page keeps up to 8 EasyTier networks, each with its
  servers, network name, secret and a name shown here, and joins any number of them together (a
  Join switch each), with the same Device ID in every one. Connecting by Device ID tries every
  joined network that lists the device, direct paths first, and a session that drops resumes
  through whichever network still reaches it; a host serves sessions from all of them. Try direct
  connections is one choice for every network, each node listening on a port of its own. The one
  network of earlier versions becomes the first, joined. Login limits count each network's
  addresses separately (their address ranges repeat).
- Found in the Windows ↔ Mac test: the ring marking a Windows host's pointer showed only while
  dragging (Windows hides its cursor with no mouse attached, and the host followed it); a host now
  reports its pointer as a Mac host does. The viewer's top bar showed only for Windows hosts, so a
  Mac host's viewer lacked Keyboard mode and Swap Ctrl and Cmd; it now shows for every session that
  controls the host, and folds away like the status bar (kept for each device). On a Mac host,
  turning Hide Kyokai from capture off during a stream could leave Kyokai hidden: the stream now
  opens again with the new setting; a session agent that starts again also gets the sound to share.
- Windows brought in line with the Mac's video path: the encoder converts each picture straight
  into reused Media Foundation samples and the viewer converts decoded pictures in place (no
  per-frame buffers or copies beyond the conversion), and the viewer decodes H.264 on the GPU
  where it has a decoder, as a Mac does through VideoToolbox.
- Started at login, Kyokai opens no window and waits in the tray (on a Mac it asked too early whether it was a login item, read no, and showed its window).
- Protocol 10 and local API 2, unchanged: the networks come with new requests and a `networks`
  list in the status, beside the old ones, so 0.3.2 and 0.3.1 connect to each other and an older
  GUI works with a newer engine.
- Upstream versions checked, unchanged: MyGo v0.5.5, EasyTier v2.7.0, RustDesk 1.5.0 (vendored
  code), vcpkg 2026.07.29, Rust 1.99.1.

## 0.3.1 (2026-10-19)

- The tab strip is the window's title bar: the traffic lights (macOS) or minimize, maximize and close
  (Windows) sit in it beside the Kyokai tab and the connections' tabs, and its empty part moves the
  window (MyGo's hidden title bar), instead of a native title bar above a row of tabs. The strip now
  shows in the main window even with no connection.
- No Settings page any more: the sidebar is Home, Network, AI, Permissions, Security, General and
  About. Try direct connections and Accept connections by address moved to the Network page;
  passwords, what a password grants, trusted devices, Sound to share and macOS's privacy
  permissions to Permissions; the device identity, protected mode, Hide Kyokai from capture, No
  logs and how secrets are kept to Security; language, appearance, open at login, the close button
  and the run mode to General; version, updates, diagnostics and licenses to About.
- The viewer: Fit or Original size (one point per pixel of the other computer, scrolled both ways
  when larger than the window), kept for each device; a Clipboard panel with a switch that shares
  the clipboard with the device or not (kept for each device) and what crossed while its tab was in
  front, from that device or from this computer, kept in memory until the tab closes. With several
  connections only the tab in front exchanges the clipboard; a copy made while its tab was behind
  goes when it comes to the front.
- Protocol 10, unchanged: the clipboard choice stays between the GUI and its engine (local API 2,
  additions only), so 0.3.1 and 0.3.0 connect to each other.
- Upstream versions checked, unchanged: MyGo v0.5.5, EasyTier v2.7.0, RustDesk 1.5.0 (vendored
  code), vcpkg 2026.07.29, Rust 1.99.1.

## 0.3.0 (2026-10-18)

- Connect by address: "Control another device" takes an IP address or domain name as well as a
  Device ID and connects over plain TCP, with no network; the host accepts it only with Accept
  connections by address on (Settings, Access; off by default) and only from the listed address
  ranges (by default local networks and Tailscale's 100.64.0.0/10). Recent remembers the address.
  The Network page's direct addresses, which joined EasyTier nodes by address, are gone.
- Recent: Remove now takes a device off for good (its finished connections brought it back), and
  Rename… gives a device a name shown only on this computer.
- The viewer: its bars wrap onto more lines in a narrow window (the controls ran off the edge), and
  the status line folds away entirely (⌄), leaving a small ⌃ tab to pull it out again, kept for
  each device.
- No logs (Settings, Storage; off by default, turned on after a warning): no logs or crash traces
  are written and those written are deleted, and trusted devices and Recent are kept in memory only.
- Protected mode (Settings, Access; needs a permanent password): Kyokai offers only the AI settings
  and AI tasks, the engine refuses everything else, and only the permanent password turns it off.
- Protocol 10, unchanged: 0.3.0 and 0.2.0 connect to each other; a 0.2.0 host does not accept
  connections by address.
- Upstream versions checked, unchanged: MyGo v0.5.5, EasyTier v2.7.0, RustDesk 1.5.0 (vendored
  code), vcpkg 2026.07.29, Rust 1.99.1.

## 0.2.0 (2026-10-18)

- The viewer: the other computer's pointer follows the mouse (moves without a button held were
  dropped, so the pointer moved only on clicks), reported 60 times a second; a Codec choice (Auto
  and what both computers do); Direct or Relayed; and a tooltip on the numbers with where the
  time goes (the host's encoding, the network, this computer's decoding and drawing).
- macOS: H.264 and HEVC in hardware through VideoToolbox, on a Mac host and in a Mac viewer, so
  sessions with a Mac no longer fall back to software VP9; Hide Kyokai from capture now decides
  what Kyokai's own capture and the AI's screenshots see, live.
- Networking: with Try direct connections on, the node listens (TCP and UDP, port 47311–47320),
  so devices on the same network connect directly instead of through the server's relay; with it
  off, no direct connection is made either way.
- Lower delay: pixel conversion four to five times faster; a picture is taken only when the session
  can send it; a viewer that falls behind goes on from the next key frame instead of decoding
  broken pictures; decoding no longer waits for drawing; the stream's first picture is no longer
  lost when the viewer opens. Clicks and keys are never dropped behind pointer moves.
- Found with the test builds: an older helper is updated by the app itself, without a prompt,
  from a signed build (an app update left it behind, so its device could not reach updated ones),
  and a version mismatch names the side that differs; the host stays at most 3 frames ahead of the
  viewer's acknowledgements (on a slow relay seconds of video queued in the transport, 9.9 s at
  1 fps); a device with direct connections off still dials a LAN peer that listens, and the switch
  moved to Settings, Run mode, applied at once; the Network page's devices have their name, Copy
  and Connect; keys reach the host from a Mac's viewer (macOS gave the picture no text); the Codec
  choice names codecs only, and the viewer's details say what encodes and decodes, in hardware or
  software; a Mac host asks for the hardware encoder first. Then: a helper of the same version
  from another build is updated too; the keyboard in every direction: a shortcut's modifiers no
  longer stay down on the host, a Mac viewer grabs the keyboard (Map and Translate, with
  Accessibility) and in Legacy sends the menu's Copy, Paste and the like as Cmd shortcuts, and
  Swap Ctrl and Cmd is on by default between a Mac and Windows; each device's viewer settings are
  remembered. The Mac grab no longer freezes other programs' keyboards. Sound: the viewer plays
  what the other computer plays (Opus 1.5.2, ScreenCaptureKit and WASAPI loopback), with a Sound
  switch; 10 ms low-delay packets, none while the host is silent, a jitter buffer of 40–400 ms that gathers its target before playing,
  its own Hear sound permission (on by default) and the sound to share chosen in Settings (what the
  computer plays, or an input such as a microphone).
- Found in the next test: sessions flushed after every message, and EasyTier's flush waits for
  the peer's acknowledgement, so a session carried about one message per round trip for video,
  sound and input together (the frame rate stayed low, sound choked, and a burst of sound could
  stall the session): writes no longer flush, and on this Mac the frame rate went from 0–13.5 to
  26.6 fps with sound arriving at most 32 ms apart. The viewer reads, decodes video and plays
  sound on separate goroutines, as RustDesk's client does. For 2 seconds after a person's input the
  host sends at the viewer's frame-rate cap while the delay is low (RustDesk's QoS climbs from 15
  only with its probes). A device whose Kyokai quits says goodbye, so the other side ends the
  session at once instead of after a minute. The Network page has direct addresses (other devices'
  IP addresses or domain names, a network without a server) and Clear network settings.
- Wire protocol 10 (a 0.2.0 device connects only to 0.2.0), FFI ABI 5, local API 2.
- Checked (2026-10-18): MyGo v0.5.5, EasyTier v2.7.0 (commit `8b7f1f0`), RustDesk 1.5.0, vcpkg
  2026.07.29 and Rust 1.99.1 are the latest; checked, unchanged.

## 0.1.2 (2026-10-18)

- macOS: the Dock icon is shown only while a Kyokai window is open; with only the menu bar icon
  left, Kyokai leaves the Dock and the app switcher.
- Checked (2026-10-18): MyGo v0.5.5, EasyTier v2.7.0 (commit `8b7f1f0`), RustDesk 1.5.0, vcpkg
  2026.07.29 and Rust 1.99.1 are the latest; checked, unchanged.

## 0.1.1 (2026-10-17)

The macOS release (Apple silicon, Intel), and connections reworked after RustDesk.

- macOS: ScreenCaptureKit capture, CGEvent input with the keyboard modes, the privacy permissions
  (Screen Recording, Accessibility, Input Monitoring, Keychain) asked for and shown on Home; the
  run modes through a LaunchDaemon helper and a LaunchAgent (Kyokai Helper), installed with one
  administrator prompt, updated by code signature, removed without one; signed with an Apple
  Development certificate so grants and Keychain access survive updates.
- Connections: devices only, no sessions in the interface. Home shows This device, Control another
  device and Recent devices as cards (menu: Connect, Connect with AI, Forget password, Remove).
  Each connection is a tab beside Home that can be dragged into its own window and back, with
  full screen, a password field with Remember password while the host is asked, and Reconnect.
- Access: Accepted passwords (temporary, permanent or both) and a global Connect without asking,
  on by default, replace per-device unattended grants; with nobody at the computer only the
  permanent password admits.
- AI: the host keeps one AI record for every controller, and the draft of the next instruction is
  shared live between the host and every controller. Wire protocol 6, FFI ABI 4, local API 2.
- Fixes: a stopping engine no longer removes its successor's socket; quitting at logout works;
  Reopen no longer starts a second copy; a store that cannot be unlocked offers Start over; crash
  traces are written to the logs folder.
- Release check (2026-10-17): MyGo v0.5.5, EasyTier v2.7.0 (commit `8b7f1f0`), RustDesk 1.5.0,
  vcpkg 2026.07.29 and Rust 1.99.1 are the latest; checked, unchanged.

## 0.1.0 (2026-10-17)

The first release, for Windows (x64, ARM64); macOS follows.

- Connections: the embedded EasyTier node through standard servers (relayed unless direct paths
  are turned on), Device ID resolution, Noise XX with OPAQUE logins (temporary and permanent
  passwords), the host's confirmation, trusted devices, unattended grants, rate limits, resumed
  sessions, identity backup, friendly names with vendor logos. Wire protocol 4, FFI ABI 3, local
  API 2.
- Manual Computer Use: DXGI capture with a GDI fallback, monitors, Media Foundation H.264 / HEVC /
  VP9 (hardware where present), libvpx VP8 / VP9 and libaom AV1, full colour 4:4:4, RustDesk's
  QoS and quality presets, input with RustDesk's keyboard modes, the remote pointer, clipboard
  text, images and files, session actions (lock, Ctrl+Alt+Del, input block, privacy mode).
- AI Computer Use on the host with its own keys: Claude, OpenAI and OpenAI-compatible providers
  from templates, approvals, take-over, emergency stop, pause at sleep, tasks steered from a
  controller.
- Run modes: No privileges, Follow Kyokai and Background service through a signed, self-updating,
  self-disabling privileged helper with a session agent on the input desktop.

- Repository scaffold: working conventions (`AGENTS.md`), mirrored documentation, the upstream pin
  check, the Rust workspace and the Go module; release signing material generated (app updates,
  helper bundles, macOS code signing).
- Foundation: the Rust core (secrets store under the OS credential store, persistent device
  identity and Device ID, temporary password) behind a versioned C ABI; the `kyokai-svc` engine as
  a user-level child of the GUI with the local API; the GUI (Home, Settings, tray, four languages,
  signed app updates); CI and release workflows; Windows packages.
- Phase 0: the S1 EasyTier, S2 viewer and S3-lite capture spikes pass on Windows ARM64.
- MyGo v0.5.5 / Rust 1.99.1; EasyTier v2.7.0 (prerelease, commit `8b7f1f0`) and RustDesk 1.5.0
  (commit `fada664`) pinned for the coming phases (checked 2026-10-16).
- vcpkg 2026.07.29 (commit `9e593bb`: libvpx 1.16.0, aom 3.13.3) pinned for the software codecs;
  MyGo v0.5.5, EasyTier v2.7.0, RustDesk 1.5.0 and Rust 1.99.1 unchanged (checked 2026-10-17).
- Release check (2026-10-17): MyGo v0.5.5, EasyTier v2.7.0 (commit `8b7f1f0`), RustDesk 1.5.0,
  vcpkg 2026.07.29 and Rust 1.99.1 are the latest; checked, unchanged.
