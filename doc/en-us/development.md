# Development

## Requirements

- Go 1.27.2 (`go.mod`; an older Go downloads it automatically). MyGo is a Go tool dependency
  (`go tool mygo …`).
- Rust 1.99.1 with `rustfmt` and `clippy` (`rust-toolchain.toml`; rustup installs it on first use).
- Windows: Visual Studio 2026 (or its Build Tools) with the C++ tools for the target architecture
  (ARM64 and/or x64), the Windows SDK, and the component "C++ Clang Compiler for Windows" — `ring`
  needs clang on `aarch64-pc-windows-msvc`. Git for Windows provides bash and OpenSSL.
- macOS: the Xcode command line tools; Go and rustup (Homebrew works); `protoc` (Homebrew
  `protobuf`) once EasyTier is embedded.
- `cargo-deny` (`cargo install cargo-deny --locked`) for the dependency check; on Windows ARM64 it
  builds only once clang is installed.

## Commands

```bash
go run ./tools/deps check          # pinned vs latest MyGo, EasyTier, RustDesk, Rust
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
scripts/smoke.sh user                    # the engine end to end with the real core
SCREENSHOT_DIR=$PWD/.dev/shots go test ./internal/gui/
KYOKAI_CORE_LIB=/abs/path/kyokai_core.dll go test ./internal/core/
```

`KYOKAI_PROFILE` keeps a development instance apart from an installed Kyokai: its own app name,
single-instance lock, socket and state directory (`Kyokai-<profile>`). `KYOKAI_SVC` points the GUI at
another engine binary, `KYOKAI_SOCKET` at another socket. The ABI smoke test needs an absolute
library path.

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
