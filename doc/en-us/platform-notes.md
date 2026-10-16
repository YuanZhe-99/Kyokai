# Platform notes

| Platform | Status |
|---|---|
| Windows (x64, ARM64) | Scaffold only: the Go and Rust gates pass on Windows 11 ARM64 (below). No app yet. |
| macOS (Apple silicon, Intel) | Scaffold only; nothing built or run on macOS yet. |
| Linux | Not supported yet. |

## Windows

Verified on Windows 11 ARM64 (Snapdragon dev kit, 2026-10-16; Go 1.27.2, Rust 1.99.1, Visual
Studio 2026 without its Clang component):

| Check | Result |
|---|---|
| Go module | `go vet ./...` and `go test ./...` pass |
| Rust core | `cargo fmt`, `cargo clippy -D warnings` and `cargo test` pass for `kyokai-core` |
| `cargo-deny` | does not build yet: `ring` needs clang on ARM64 |

## Known limitations

- Until the Clang component is installed, nothing that depends on `ring` (EasyTier, TLS clients,
  `cargo-deny`) builds on Windows ARM64.
