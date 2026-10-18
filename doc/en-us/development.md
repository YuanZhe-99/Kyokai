# Development

## Requirements

- Go 1.27.2 (`go.mod`; an older Go downloads it automatically). MyGo is a Go tool dependency
  (`go tool mygo …`).
- Rust 1.99.1 with `rustfmt` and `clippy` (`rust-toolchain.toml`; rustup installs it on first use).
- Windows: Visual Studio 2026 (or its Build Tools) with the C++ tools for the target architecture
  (ARM64 and/or x64), the Windows SDK, and the component "C++ Clang Compiler for Windows" — `ring`
  needs clang on `aarch64-pc-windows-msvc`; put the installation's `VC\Tools\Llvm\ARM64\bin` on
  `PATH` (the component does not). Git for Windows provides bash and OpenSSL.
- macOS: the Xcode command line tools; Go and rustup (Homebrew works); `pkg-config`
  (`brew install pkgconf`), which vcpkg needs for aom.
- `protoc` for EasyTier's protocol files: `scripts/protoc.sh` downloads the pinned release
  (36.2) into `build/tools` once, checks its SHA-256 and prints its path. `scripts/build.sh` and
  `scripts/smoke.sh` use it on their own; for plain cargo commands run
  `export PROTOC=$(scripts/protoc.sh)` first. Without `PROTOC`, EasyTier's build would download
  an unverified release candidate on Windows.
- `cargo-deny` (`cargo install cargo-deny --locked`) for the dependency check; on Windows ARM64 it
  builds only once clang is installed.
- `cargo-about` (`cargo install cargo-about --locked --features cli`) for the license file that
  `scripts/build.sh` puts into every package.
- libvpx 1.16.0 and libaom 3.13.3, linked statically into the core: `scripts/codecs.sh` builds
  them with vcpkg in manifest mode (`core/vcpkg.json`, the registry pinned in
  `core/vcpkg-configuration.json`, overlay triplets in `core/vcpkg-triplets`) into
  `core/vcpkg_installed/<triplet>` (not committed); run it once per target before cargo, and
  again when the vcpkg pin moves. It uses `VCPKG_ROOT` when set, else the vcpkg that ships with
  Visual Studio 2026 (the "vcpkg package manager" component, read-only in its installation folder)
  on Windows, else `vcpkg` from `PATH`. vcpkg keeps its tools (CMake, a portable MSYS2 and Perl for
  libvpx's configure, NASM for x64), downloads and binary cache in its shared default places
  (`%LOCALAPPDATA%\vcpkg` on Windows); nothing is installed system-wide and `PATH` is not changed.
  macOS needs `brew install nasm` for the x64 slice. `KYOKAI_CODECS_DIR` points the core's build
  at another install tree.
- The libvpx and libaom bindings (`core/third_party/rustdesk-codec/generated`) are generated ahead
  of time, so builds need neither bindgen nor libclang. After moving the vcpkg pin, regenerate them
  with `bindgen-cli` (`cargo install bindgen-cli --locked`) and clang's `libclang` (Visual Studio's
  `VC\Tools\Llvm\ARM64\bin` as `LIBCLANG_PATH` on Windows):

  ```bash
  inc=core/vcpkg_installed/arm64-windows-static/arm64-windows-static/include
  out=core/third_party/rustdesk-codec
  bindgen $out/src/bindings/vpx_ffi.h -o $out/generated/vpx_ffi.rs --allowlist-type '^[vV].*' \
    --allowlist-var '^[vV].*' --allowlist-function '^[vV].*' --rustified-enum '^[vV].*' \
    --distrust-clang-mangling --no-layout-tests --no-doc-comments -- -I$inc
  bindgen $out/src/bindings/aom_ffi.h -o $out/generated/aom_ffi.rs \
    --allowlist-type '^(aom|AOM|OBU|AV1).*' --allowlist-var '^(aom|AOM|OBU|AV1).*' \
    --allowlist-function '^(aom|AOM|OBU|AV1).*' --rustified-enum '^(aom|AOM|OBU|AV1).*' \
    --distrust-clang-mangling --no-layout-tests --no-doc-comments -- -I$inc
  ```

  These are RustDesk's settings (`libs/scrap/build.rs`). The declarations are the same on every
  64-bit target Kyokai builds: C `long` stays `c_long`.

## Commands

```bash
go run ./tools/deps check          # pinned vs latest MyGo, EasyTier, RustDesk, vcpkg, Rust
scripts/codecs.sh                  # libvpx and libaom for this platform (once, before cargo)
gofmt -l cmd internal tools
go vet ./...
go test ./...
CGO_ENABLED=0 go test ./...
go tool mygo vet .
go tool mygo i18n check
go run ./tools/checkdocs           # doc/en-us and doc/zh-cn mirror each other
cargo fmt --manifest-path core/Cargo.toml --all --check
cargo clippy --manifest-path core/Cargo.toml --workspace --all-targets -- -D warnings
cargo test --manifest-path core/Cargo.toml --workspace
cargo deny --manifest-path core/Cargo.toml check
```

Run cargo from the repository root: `rust-toolchain.toml` and `.cargo/config.toml` are read from
the current directory, not from `--manifest-path`.

## Dev loop

```bash
scripts/build.sh windows/arm64           # or no target: this machine's platform
KYOKAI_PROFILE=dev build/windows-arm64/Kyokai.exe
KYOKAI_PROFILE=dev build/windows-arm64/bin/kyokai-svc.exe ctl status
KYOKAI_NETWORK_SECRET=… kyokai-svc ctl network-set --server tcp://host:11010 --name net
KYOKAI_PASSWORD=… kyokai-svc ctl connect --device 482916735   # then on the host: ctl allow --id N
KYOKAI_BACKUP_PASSPHRASE=… kyokai-svc ctl backup --file id.json   # restore --file, reset-identity --yes
scripts/smoke.sh user                    # the engine end to end with the real core
SCREENSHOT_DIR=$PWD/.dev/shots go test ./internal/gui/
KYOKAI_CORE_LIB=/abs/path/kyokai_core.dll go test ./internal/core/
```

`KYOKAI_PROFILE` keeps a development instance apart from an installed Kyokai: its own app name,
single-instance lock, socket and state directory (`Kyokai-<profile>`). `KYOKAI_SVC` points the GUI at
another engine binary, `KYOKAI_SOCKET` at another socket. The ABI smoke test needs an absolute
library path. `ctl network-set` takes the network secret from `KYOKAI_NETWORK_SECRET`, never from
the command line; `ctl network` shows the settings without it. Two engines with different profiles,
state directories and sockets on one machine see each other through the same network.

On macOS, sign local builds with the release identity (`MYGO_SIGN=<identity or its SHA-1>
scripts/build.sh`) once it is in the login keychain: ad hoc builds get a new identity each time,
which drops the privacy grants. Start the app with `open`, not from a shell, so that macOS counts
its engine as part of Kyokai; an engine started from a shell has no grants and sends the
placeholder picture. `ctl watch --type T --keys meta+a,enter` types into the host as the viewer
does.

## Spikes

`spikes/` holds the Phase 0 feasibility programs, each with its own `go.mod` or Cargo workspace so
that the product gate never builds them. Their results go into [upstream.md](upstream.md) and
[platform-notes.md](platform-notes.md); code that proves itself moves into the product in a later
phase.

## Testing principles

Tests protect product contracts and error-prone logic, preferably table-driven; every fixed bug
gets a regression test. No tests for wording, internal state details or evidence collection.
Tests that need the network, an official EasyTier core or a provider key run only when their
environment variables are set. `go test ./...` stays under a minute.

The AI runtime's tests drive a fake desktop and a local stand-in for the provider. With
`KYOKAI_LIVE_CLAUDE=1` and `ANTHROPIC_API_KEY` set, `cargo test ... live_claude` runs a real Claude
task on synthetic screens; `cargo test ... real_screen -- --ignored` checks the real capture and the
input hooks on a machine with a desktop (it reads the screen into memory only and injects nothing).
