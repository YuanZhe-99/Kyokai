# Kyokai documentation

**These docs are the authoritative description of the code.** `doc/en-us/` is the source;
`doc/zh-cn/` mirrors it file for file. Plans and open work are not documented here; while a
multi-step plan is active it lives only in the repository's `PLAN.md`.

Kyokai is an AI Computer Use application: a person or an AI agent operates another computer over an
embedded EasyTier P2P network, and AI tasks run on the computer being operated. It runs on Windows
(x64, ARM64) and macOS (Apple silicon, Intel).

| Document | Covers |
|---|---|
| [upstream.md](upstream.md) | Verified upstream contracts and quirks: MyGo, EasyTier, RustDesk, OpenAI, Claude |
| [third-party.md](third-party.md) | Reused code and dependencies, licenses and obligations |
| [platform-notes.md](platform-notes.md) | What is implemented and verified on each platform |
| [development.md](development.md) | Environment, commands, the dev loop, spikes |
| [translation-guide.md](translation-guide.md) | Mirror rules and the glossary |
| [version-history.md](version-history.md) | Releases and the upstream versions they pin |
| [functions/INDEX.md](functions/INDEX.md) | Per-package and per-crate roles and key declarations |

## Pinned versions

| Component | Version | Source of truth |
|---|---|---|
| Go | 1.27.2 | `go.mod` |
| MyGo | v0.5.5 | `go.mod` |
| Rust | 1.99.1 | `rust-toolchain.toml` |
| EasyTier (to be embedded) | v2.7.0, prerelease, commit `8b7f1f0` | `third_party/upstream.json` |
| RustDesk (source of vendored code) | 1.5.0, commit `fada664` | `third_party/upstream.json` |
