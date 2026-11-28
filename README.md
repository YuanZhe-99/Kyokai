# Kyokai (鏡界)

**AI Computer Use across devices.**

Kyokai lets a person, or an AI agent, operate another computer. Devices find each other by a
Device ID over standard [EasyTier](https://github.com/EasyTier/EasyTier) networks, every connection
is end-to-end encrypted and authorized by the host, and AI tasks always run on the computer being
operated, with that computer's own provider key, policies and approvals. On desktops one app does
both sides (let others in, and reach your other machines); on Android it acts as a controller.

[Download](https://github.com/YuanZhe-99/Kyokai/releases/latest) ·
[User guide](doc/en-us/user-guide.md) · [Documentation](doc/en-us/README.md) ·
[中文文档](doc/zh-cn/README.md)

> **0.8.2** — Windows (x64, ARM64), macOS 14+ (Apple silicon, Intel), Linux (x64, ARM64; X11
> and Wayland) and Android 8.0+ (`arm64-v8a`, controller only; merged from the archived
> [Kyokai-Mobile](https://github.com/YuanZhe-99/Kyokai-Mobile) repository in `0.8.0`). Windows is
> the most complete and the most tested. On macOS a few checks remain; on Linux, GNOME and KDE
> Wayland desktops, the keyboard grab in a real session and parts of the privileged helper still
> wait for a run. On Android, `0.8.0` is verified on the Android 17 emulator and a Galaxy Z Fold 8
> (including in-place upgrade over `Kyokai-Mobile` `0.1.3`), while `0.8.1`'s Material 3 UI overhaul
> and foldable laptop deck await live on-device checks alongside remaining service and battery
> runs. What was verified where, and how, is in the [platform notes](doc/en-us/platform-notes.md);
> what is left is in [PLAN.md](PLAN.md).

## What it does

### AI Computer Use

Send a task in plain language to another computer. The host captures its own screen, asks its AI
provider for the next actions, checks each one against its own policy and performs the allowed
ones itself. The controller never sees the host's key and never runs anything on the host; both
desktop and Android controllers can start tasks, reply, approve or decline actions and review task
history.

- Claude, OpenAI and 20+ templates (OpenRouter, Gemini, Qwen, Ollama, LM Studio, …) or any
  OpenAI-compatible server that sees images and calls tools; the agent loop runs on
  [Pi](https://github.com/earendil-works/pi).
- AI sessions: conversations kept across replies and restarts, with fork, compact and export to
  Markdown, and a model and thinking level per task.
- Approvals: ask for every action, or **Auto**, where routine actions run and risky ones (system
  shortcuts, settings, terminals, deleting, sending, paying, installing, …) still ask.
- Scope: the host user grants apps, windows and folders; the rest of the screen is dimmed for the
  AI and actions there are refused.
- The host user always wins: their own keyboard or mouse takes over at once; pause, cancel and
  emergency stop are always there, and tasks pause when the computer sleeps.
- An element list beside each screenshot (UI Automation, AX, AT-SPI, with the system's OCR) and,
  on Windows, an AI that works in its window while the person uses another.

### Manual control

- The host's screen in H.264, HEVC, VP9, AV1 or VP8, in hardware where the system has it (Media
  Foundation, VideoToolbox, VA-API and Android `MediaCodec`), full colour 4:4:4, with adaptive
  quality that recovers on slow and lossy paths.
- Mouse and keyboard in RustDesk's modes: **Map** sends every key by its place, so the host's own
  input method types Chinese or Japanese and every shortcut reaches the host; **Translate** types
  with your layout; **Legacy**. **Swap Ctrl and Cmd** is enabled by default between macOS and
  Windows or Linux.
- Clipboard text, images and files; sound; the remote pointer; several displays, each in a pane of
  its own; waking a dark display, on the lock screen too.
- Session actions on Windows hosts: lock, Ctrl+Alt+Del, block the host's input, privacy mode, lock
  when the session ends.

### The device's own services

- SSH, Remote Desktop and VNC (Apple Screen Sharing too) through tunnels inside Kyokai's encrypted
  sessions, in built-in clients: nothing listens on the network, and each is a permission the host
  turns on.
- SSH signs in with your Kyokai keys, whose private parts never leave the engine; a files panel
  copies over SFTP, or SCP where there is none.
- A Sipeed NanoKVM USB wired to a host reaches the computer behind it, its firmware setup too,
  with nothing installed on that computer ([remote-services.md](doc/en-us/remote-services.md)).

### Android controller

- Controller-only Android app (`com.yuanzhe.kyokai`, Android 8.0+, `arm64-v8a`, 16 KB page-size
  aligned) built from the same Go/MyGo UI and embedded Rust core (`libkyokai_core.so`), replacing
  the archived [Kyokai-Mobile](https://github.com/YuanZhe-99/Kyokai-Mobile) Flutter repository and
  upgrading in place over `0.1.3` with your Device ID, networks and saved credentials preserved.
- Adaptive Material Design 3 interface with Android 12+ Material You dynamic colors (falling back
  to Kyokai's jade-teal theme): bottom navigation on phones, side rail with two-pane master-detail
  (or floating capsule dock) on tablets and unfolded foldables, and live host status telemetry
  cards and charts.
- Hardware H.264/HEVC decoding (`AMediaCodec`), 48 kHz Opus sound, foreground session service,
  multi-touch on-screen touchpad (tap, double-tap, two-finger right-click, three-finger
  middle-click, inertia scroll, tap-hold drag), and a computer keyboard with `Shift` and macOS
  `Option` layers, auto-open on generous portrait letterbox space and haptic feedback.
- **Foldable Laptop Mode**: half-folded horizontal hinge posture puts the remote display above the
  hinge and a full 5-row ANSI keyboard above a centered palmrest touchpad below it across Kyokai
  desktops, NanoKVM, RDP and VNC.
- Full controller support for host AI tasks, bidirectional clipboard (text, images and received
  files with Save and Share), and built-in SSH, SFTP/SCP, Remote Desktop, VNC and NanoKVM USB.

### Getting there

- A persistent Device ID such as `482 916 735`, derived from a device key that stays on the device,
  plus a friendly name and an encrypted identity backup.
- Up to 8 EasyTier networks at once, on standard, unmodified EasyTier servers, with no TUN device,
  no administrator rights and no Kyokai server; or by IP address or domain name. At every
  connection Kyokai picks the best route by latency and past throughput, and moves a healthy
  session to a clearly better one, AI task and tunnels included.
- Sign-in by a temporary password, a permanent password (both checked with OPAQUE, so neither is
  ever sent) or an SSH-style Ed25519 key, each chosen on its own; trusted devices, saved
  passwords and keys, and unattended access.
- Home shows your devices as cards with their live status: CPU, memory, GPU, disk, network and
  battery.

### Around it

- The desktop viewer: tabs that tear out into windows or split one window into up to four panes, a
  floating toolbar, full screen, codec, resolution, quality and frame rate, the route in use.
- The `kyokai` command line: status, devices, sessions, screenshots, AI tasks and tunnels on a
  local port (OpenSSH's `sftp` and `scp` go through them), with `--json` and exit codes.
- An optional privileged helper for UAC prompts, the lock and sign-in screens and unattended
  access; run modes switch both ways and keep everything.
- English, Simplified Chinese, Traditional Chinese and Japanese.

## Security and privacy

- **Network membership grants nothing.** Every connection is authenticated, and each permission
  stands alone: viewing does not imply control, control does not imply AI tasks or SSH.
- **Secrets never travel.** The device key, passwords, private keys, the network secret and API
  keys live encrypted in the system's credential store; they are never logged or sent.
- **The AI cannot grant itself anything.** Screen content is untrusted data; the host's executor
  validates every action; a model answer that arrives after a pause or cancel never acts.
- **Kyokai's own windows stay out of the picture**, and remote input never reaches them, unless
  the host user turns that off.
- **Nothing by default:** no autostart, no service, no elevation and no AI after exit. The
  privileged helper asks for administrator rights once, when you install it.
- **Logs hold no secrets**, and **No logs** (Security) keeps none at all: no log, no crash trace,
  the connection history in memory only.

A computer that sleeps leaves the network; waking it needs Wake-on-LAN or hardware.

## Install

Download from the [latest release](https://github.com/YuanZhe-99/Kyokai/releases/latest);
`SHA256SUMS.txt` lists every file's checksum.

| System | Download |
|---|---|
| Windows | `kyokai-setup-<version>-amd64.exe` or `-arm64.exe`: installs for your user, no administrator rights. `Kyokai-<version>-windows-amd64.zip` or `-arm64.zip` is portable. Not code signed, so SmartScreen warns on first launch |
| macOS | `kyokai-<version>-arm64.dmg` (Apple silicon) or `-amd64.dmg` (Intel). Signed but not notarized: if macOS calls the app damaged, run `xattr -dr com.apple.quarantine /Applications/Kyokai.app` once |
| Linux | `kyokai_<version>_amd64.deb` or `_arm64.deb`; `install.sh`, which installs for your user into `~/.local` and keeps Kyokai up to date; or `Kyokai-<version>-x86_64.AppImage` or `-aarch64.AppImage`. GNOME shows the tray icon only with its AppIndicator extension |
| Android | `Kyokai-<version>-android-arm64.apk` (Android 8.0+, `arm64-v8a`, 16 KB page-size aligned, controller only). Signed with the same release key and package ID (`com.yuanzhe.kyokai`) as the archived [Kyokai-Mobile](https://github.com/YuanZhe-99/Kyokai-Mobile): installs directly over `0.1.3` while keeping your Device ID, networks and saved sign-ins |

Desktop builds update themselves from **Settings › About & updates**, except the Debian package
and the AppImage; Android updates by installing the newer APK from Releases. Then follow the
[user guide](doc/en-us/user-guide.md).

## Build from source

Go 1.27.4 and Rust 1.100.0 (`rust-toolchain.toml`), plus Bun (`sidecar/package.json`) and
`cargo-about` for desktop packages:

- **Windows:** Visual Studio 2026 with the C++ tools for your architecture and the "C++ Clang
  Compiler for Windows" component.
- **macOS:** the Xcode command line tools.
- **Linux:** `libpipewire-0.3-dev`, `libva-dev`, `libgbm-dev`, `libclang-dev` and NASM.
- **Android:** Android SDK (platform 36, build-tools), Android NDK and Java 17
  (`scripts/build-android-core.sh` adds `aarch64-linux-android`).

```bash
# Desktop (this platform)
scripts/codecs.sh   # once: libvpx, libaom and Opus through vcpkg
scripts/build.sh    # the Rust core, the engine, the AI sidecar, then `mygo build`

# Android (arm64-v8a)
scripts/build-android-core.sh arm64-v8a
go tool mygo build -platform android/arm64 .
```

Details, the test gate and the dev loop: [development.md](doc/en-us/development.md). Continuous
integration and releases run on GitHub Actions: [ci-cd.md](doc/en-us/ci-cd.md).

## Architecture

A Go GUI on [MyGo](https://github.com/egoist/mygo), a headless engine (`kyokai-svc`), the `kyokai`
command line and a Rust core library that the GUI and the engine both load, which embeds EasyTier
and RustDesk's capture, codecs and input; AI tasks run in an agent sidecar (`kyokai-agent`). On
Android, the app runs `internal/engine` in-process (`host.serve = "none"`), loads
`libkyokai_core.so` directly and bridges Keystore, `AMediaCodec`, foreground sessions and foldable
posture through `internal/androidhost`. See [architecture.md](doc/en-us/architecture.md).

## Acknowledgements

- **[RustDesk](https://github.com/rustdesk/rustdesk)**: its mature open-source desktop capture,
  input control, video processing and cross-platform desktop technologies.
- **[EasyTier](https://github.com/EasyTier/EasyTier)**: its open-source peer-to-peer networking,
  NAT traversal and connectivity.
- **[MyGo](https://github.com/egoist/mygo)**: its cross-platform desktop and mobile application
  framework and application integration.
- **[Pi](https://github.com/earendil-works/pi)**: the agent loop that runs Kyokai's AI tasks.
- **[Arion](https://github.com/AstraFoundry/Arion)**: the design of Kyokai's first-run tour draws
  on Arion's onboarding; no code from Arion is used.

## License

Kyokai is licensed under [AGPL-3.0-only](LICENSE). Third-party components and their licenses are
listed in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) and
[third-party.md](doc/en-us/third-party.md).
