# Third-party notices

Kyokai is licensed under AGPL-3.0-only (`LICENSE`). It includes or links the components below. The
inventory of reused code (repository, commit, files, modifications, obligations) is
[doc/en-us/third-party.md](doc/en-us/third-party.md); release builds regenerate the full list of
Rust and Go dependencies with their license texts.

## Go modules (GUI and engine)

| Component | License | Source |
|---|---|---|
| MyGo | MIT | https://github.com/egoist/mygo |
| purego (through MyGo) | Apache-2.0 | https://github.com/ebitengine/purego |
| go-text/typesetting (through MyGo) | Unlicense OR BSD-3-Clause | https://github.com/go-text/typesetting |
| golang.org/x/image, x/text, x/sys | BSD-3-Clause | https://go.googlesource.com |
| Kage shader compiler, inside MyGo | Apache-2.0 | https://github.com/hajimehoshi/ebiten |
| MingCute icon, inside MyGo | Apache-2.0 | https://github.com/Richard9394/MingCute |

## Rust crates (core library)

| Crate | License | Source |
|---|---|---|
| ed25519-dalek, curve25519-dalek | BSD-3-Clause | https://github.com/dalek-cryptography/curve25519-dalek |
| chacha20poly1305, sha2 and their RustCrypto dependencies | Apache-2.0 OR MIT | https://github.com/RustCrypto |
| getrandom | MIT OR Apache-2.0 | https://github.com/rust-random/getrandom |
| serde, serde_json | MIT OR Apache-2.0 | https://github.com/serde-rs |
| base64 | MIT OR Apache-2.0 | https://github.com/marshallpierce/rust-base64 |
| zeroize | Apache-2.0 OR MIT | https://github.com/RustCrypto/utils |
| windows-sys (Windows) | MIT OR Apache-2.0 | https://github.com/microsoft/windows-rs |
| security-framework (macOS) | MIT OR Apache-2.0 | https://github.com/kornelski/rust-security-framework |

`cargo deny` checks every crate in the graph against the allowed licenses (`core/deny.toml`).

## Planned components

| Component | License | Used for |
|---|---|---|
| EasyTier (embedded, v2.7 line) | LGPL-3.0 | P2P networking |
| RustDesk (vendored modules, 1.5.0) | AGPL-3.0 | Windows screen capture, input logic, privileged helper |
