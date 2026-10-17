# Kyokai (鏡界)

**AI Computer Use Across Devices**

Kyokai is a cross-platform Computer Use application that enables people and AI agents to interact
with computers across devices. It combines secure peer-to-peer connectivity, direct human control,
and host-side AI execution in one unified experience.

> **Status:** early development, not released yet. On Windows the app, its engine, connections,
> manual control, AI tasks and the optional privileged helper work; keyboard modes, more software
> codecs and the macOS pass follow. See [PLAN.md](PLAN.md), the
> [platform notes](doc/en-us/platform-notes.md) (what was verified where) and the
> [user guide](doc/en-us/user-guide.md).

## Features

| Feature | Status |
|---|---|
| Persistent Device ID backed by a cryptographic device key, with a friendly name and backup | done (Windows) |
| Temporary and permanent host passwords, trusted devices, unattended access | done |
| Connect by Device ID + password over a standard EasyTier network, no IP addresses | done (Windows) |
| Manual Computer Use: the host's screen (H.264, HEVC, VP9; adaptive quality), mouse, keyboard, clipboard text, images and files, monitors, the remote pointer | done (Windows) |
| Session actions: lock the screen, Ctrl+Alt+Del, block the host's input, privacy mode | done (Windows): run on a test VM, Ctrl+Alt+Del in the helper mode |
| AI Computer Use with Claude, OpenAI or any of 20+ provider templates and custom OpenAI-compatible servers, executed on the host with its own key | done (Windows); Claude verified live |
| Human take-over, pause, cancel, emergency stop; tasks pause at sleep | done |
| Optional privileged helper for UAC prompts, the lock screen and unattended access | done (Windows); macOS follows |
| Keyboard modes, libvpx / libaom, 4:4:4 | planned |
| English, Simplified Chinese, Traditional Chinese, Japanese | done |

## AI Computer Use

You send a task in plain language to another computer. That computer — the host — captures its own
screen, asks its configured AI provider (Claude, OpenAI, or another provider or local model server
that sees images and calls tools) for the next actions, checks every action
against its own policies, and performs the allowed ones itself. The controller never sees the
host's API key. The host user can always take over or stop the task.

## Manual Computer Use

The same connection shows the host's screen and forwards your mouse, keyboard and clipboard, with
quality presets, monitor choice and RustDesk's session actions (lock, Ctrl+Alt+Del, blocking the
host's input, privacy mode). It works without any AI provider configured.

## Device ID & Password

Every installation has a persistent Device ID such as `482 916 735`, derived from a device key that
stays on the device. A host offers a temporary password (new on every start by default) and,
optionally, a permanent password. Both are checked with a password-authenticated key exchange, so
neither is ever sent.

## EasyTier Network Configuration

Kyokai embeds [EasyTier](https://github.com/EasyTier/EasyTier) and connects through standard,
unmodified EasyTier servers: you enter the server address(es), the network name and the network
secret. No TUN device, no administrator rights and no Kyokai server are needed; traffic goes
through the server unless you turn on direct connections. Membership of the
network grants nothing by itself: every connection still needs the host's password and permission.

## AI Provider Configuration

The host adds providers from templates (Claude, OpenAI, OpenRouter, Gemini, Qwen, Ollama, LM Studio
and more) or as a custom address, stores each key in its own credential store and picks the model
tasks use. Controllers only see the model name.

## Security & Privacy

- Secrets (device key, passwords, network secret, API keys) are kept encrypted with the operating
  system's credential store and never logged or sent.
- The host confirms who connects and what they may do; viewing does not imply control, control does
  not imply AI tasks.
- Screen content is treated as untrusted data; the AI cannot grant itself permissions.
- By default Kyokai starts nothing at boot, installs no service and never asks for administrator
  rights. The optional privileged helper asks once, when you install it.

## Getting Started

Builds are not published yet. See [Building from Source](#building-from-source).

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
