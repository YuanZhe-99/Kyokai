# Kyokai (鏡界)

**AI Computer Use Across Devices**

Kyokai is a cross-platform Computer Use application that enables people and AI agents to interact
with computers across devices. It combines secure peer-to-peer connectivity, direct human control,
and host-side AI execution in one unified experience.

> **Status:** 0.5.2: Windows (x64, ARM64) and macOS (Apple silicon, Intel). The app, its engine,
> connections, manual control, AI tasks, SSH / Remote Desktop / VNC through Kyokai and the optional
> privileged helper work on both; some macOS checks and viewer controls remain. See
> [PLAN.md](PLAN.md), the [platform notes](doc/en-us/platform-notes.md) (what was verified where)
> and the [user guide](doc/en-us/user-guide.md).

## Features

| Feature | Status |
|---|---|
| Persistent Device ID backed by a cryptographic device key, with a friendly name and backup | done |
| Sign-in by temporary password, permanent password or SSH-style Ed25519 key, each chosen on its own; connect without asking, saved passwords and keys, trusted devices, unattended access | done |
| Connect by Device ID over standard EasyTier networks (several joined at once, one Device ID in all), or by IP address or domain name; a device's routes (network and addresses) chosen by latency and past throughput at every connection | done |
| Home: a grid of device cards with the host's live status (CPU, memory, GPU, disk, network, battery) for devices with a saved password or key, and a details page per device | done |
| Manual Computer Use: the host's screen (H.264, HEVC, VP9 through Media Foundation; H.264 and HEVC through VideoToolbox; VP8, VP9 and AV1 in software; full colour 4:4:4; adaptive quality), mouse, RustDesk's keyboard modes (Chinese and Japanese input methods included), clipboard text, images and files, monitors, the remote pointer, sound | done (Windows); macOS: capture, VideoToolbox, input and keyboard |
| A picture that recovers at high latency, input that never jams on a stalled path, a dark display woken on request or by itself, locked or not | done |
| SSH, Remote Desktop and VNC (Apple Screen Sharing too) of a device through Kyokai's own sessions, with built-in clients; each a permission, off by default; ports set by the host | done |
| Session actions: lock the screen, Ctrl+Alt+Del, block the host's input, privacy mode | done (Windows) |
| AI Computer Use with Claude, OpenAI or any of 20+ provider templates and custom OpenAI-compatible servers, executed on the host with its own key; one AI record and a shared draft for every controller | done; Claude verified live on Windows |
| Human take-over, pause, cancel, emergency stop; tasks pause at sleep | done |
| Optional privileged helper for UAC prompts, the lock and sign-in screens and unattended access; run modes switch both ways keeping everything | done (Windows); macOS: install, update and removal |
| Viewer: tabs in the title bar that tear out into windows, a floating toolbar, full screen, original size, codec and quality, the route in use, per-session clipboard switch and history | done |
| Protected mode: only the AI settings and tasks until the permanent password turns it off | done |
| No logs: no logs or crash traces, connection history in memory only (off by default) | done |
| English, Simplified Chinese, Traditional Chinese, Japanese | done |

## AI Computer Use

You send a task in plain language to another computer. That computer — the host — captures its own
screen, asks its configured AI provider (Claude, OpenAI, or another provider or local model server
that sees images and calls tools) for the next actions, checks every action
against its own policies, and performs the allowed ones itself. The controller never sees the
host's API key. The host user can always take over or stop the task.

## Manual Computer Use

The same connection shows the host's screen and forwards your mouse, keyboard, clipboard and files,
with quality presets, monitor choice, the host's sound and RustDesk's session actions (lock,
Ctrl+Alt+Del, blocking the host's input, privacy mode). It works without any AI provider
configured. When the host's display is dark, Kyokai wakes it, on the lock screen too.

## SSH, Remote Desktop and VNC

A device's own SSH server, Remote Desktop and VNC (including a Mac's Screen Sharing) open in tabs
next to its desktop, through tunnels inside Kyokai's encrypted sessions: nothing listens on the
network, and the host's user decides, per permission, which a correct password or key reaches
without asking. SSH runs in a built-in terminal and can sign in with your Kyokai keys, whose private
parts never leave the engine; sign-ins can be remembered per device.

## Device ID, Passwords and Keys

Every installation has a persistent Device ID such as `482 916 735`, derived from a device key that
stays on the device. A host offers a temporary password (shown on Home) and, optionally, a permanent
password, both checked with a password-authenticated key exchange, so neither is ever sent. Devices
can also sign in with SSH-style Ed25519 keys. The host chooses which of these let others in, in any
combination; with none, every connection asks.

## Home

Home lists your devices as cards. For a device with a saved password or key, its card shows live
status — CPU, memory, GPU, disk, network and battery — watched quietly without opening its screen,
and offers the connections that work there without the other side's user.

## EasyTier Network Configuration

Kyokai embeds [EasyTier](https://github.com/EasyTier/EasyTier) and connects through standard,
unmodified EasyTier servers: for each network you enter the server address(es), the network
name and the network secret. Kyokai keeps up to 8 networks and can join several at once, with the
same Device ID in each. No TUN device, no administrator rights and no Kyokai server are needed;
traffic goes through the server unless you turn on direct connections (Advanced). A device can also
be reached by IP address or domain name (off on the host by default), and Kyokai picks the best way
to reach it at every connection. Membership of a network grants nothing by itself: every connection
still needs the host's password, key or confirmation.

## AI Provider Configuration

The host adds providers from templates (Claude, OpenAI, OpenRouter, Gemini, Qwen, Ollama, LM Studio
and more) or as a custom address, stores each key in its own credential store and picks the model
tasks use. Controllers only see the model name.

## Security & Privacy

- Secrets (device key, passwords, private keys, network secret, API keys) are kept encrypted with
  the operating system's credential store and never logged or sent.
- The host confirms who connects and what they may do; each permission stands alone: viewing does
  not imply control, control does not imply AI tasks or SSH.
- Screen content is treated as untrusted data; the AI cannot grant itself permissions.
- Kyokai's own windows stay out of the picture and out of reach of remote input by default.
- By default Kyokai starts nothing at boot, installs no service and never asks for administrator
  rights. The optional privileged helper asks once, when you install it.
- A computer that sleeps or hibernates leaves the network and cannot be reached; waking it needs
  Wake-on-LAN or hardware.

## Getting Started

Download the installer for your computer from the
[latest release](https://github.com/YuanZhe-99/Kyokai/releases/latest): `Kyokai.Setup.<version>.amd64.exe`
or `.arm64.exe` for Windows, `Kyokai.<version>.arm64.dmg` (Apple silicon) or `.amd64.dmg` (Intel)
for macOS; the `.zip` files are portable Windows builds. `SHA256SUMS.txt` lists every file's
checksum. Then follow the [user guide](doc/en-us/user-guide.md). To build it yourself, see
[Building from Source](#building-from-source).

## Building from Source

Requirements: Go 1.27.2, Rust 1.99.1 (`rust-toolchain.toml`), and on Windows Visual Studio
2026 with the C++ tools for your architecture plus the "C++ Clang Compiler for Windows" component;
on macOS Xcode command line tools. Details: [doc/en-us/development.md](doc/en-us/development.md).

```bash
scripts/build.sh               # this platform: Rust core, engine, then `mygo build`
```

## GitHub Actions

Continuous integration and releases run on GitHub Actions; see
[doc/en-us/ci-cd.md](doc/en-us/ci-cd.md).

## Architecture

A Go + [MyGo](https://github.com/egoist/mygo) GUI, a headless engine (`kyokai-svc`) and a Rust core
library loaded by both; the core embeds EasyTier. See
[doc/en-us/architecture.md](doc/en-us/architecture.md).

## Documentation

- [English documentation](doc/en-us/README.md) · [中文文档](doc/zh-cn/README.md)

## Acknowledgements

- **[RustDesk](https://github.com/rustdesk/rustdesk)** — for its mature open-source desktop
  capture, input control, video processing, and cross-platform desktop technologies.
- **[EasyTier](https://github.com/EasyTier/EasyTier)** — for its open-source peer-to-peer
  networking, NAT traversal, and connectivity technologies.
- **[MyGo](https://github.com/egoist/mygo)** — for its cross-platform desktop application
  framework and application integration capabilities.

## Open Source Licenses

Kyokai is licensed under [AGPL-3.0-only](LICENSE). Third-party components and their licenses are
listed in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) and
[doc/en-us/third-party.md](doc/en-us/third-party.md).
