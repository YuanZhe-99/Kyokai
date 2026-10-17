# CI/CD

Workflows live in `.github/workflows` and run on GitHub. The private remote has no runner, so a
push there is checked only by the local gate.

| Workflow | Trigger | Does |
|---|---|---|
| `ci.yml` → checks | push to `main`, pull requests, manual | gofmt, `go vet` for Windows and macOS, interface catalogs, doc mirror, `cargo fmt`, `cargo deny`, secret scan (gitleaks), upstream pins (informational) |
| `ci.yml` → test | same | `windows-11-arm` and `macos-latest`: clippy, Rust tests, the Go tests with the real core (ABI smoke test), `mygo vet`, `scripts/smoke.sh user`, unsigned packages as artifacts; `windows-latest` and `macos-15-intel`: core build and Go tests. Every job uses the pinned `protoc` (`scripts/protoc.sh`); `windows-11-arm` puts LLVM's clang on `PATH` for `ring`; the macOS jobs run with their own unlocked default keychain |
| `release.yml` | tag `v*`, manual | Windows cores on `windows-latest` (x64) and `windows-11-arm`, packaged on Ubuntu with NSIS; macOS built and packaged on `macos-latest`; a tag publishes with `SHA256SUMS.txt` after checking that it matches `internal/version` |
| `upstream.yml` | weekly, manual | `go run ./tools/deps check -strict`; opens an issue when MyGo, EasyTier, RustDesk or Rust has moved |
| `dependabot.yml` | weekly | Actions, Go modules (MyGo excluded) and Cargo |

## Secrets

| Secret | Used by | Effect when unset |
|---|---|---|
| `MYGO_UPDATER_PRIVATE_KEY` | `release.yml`, `mygo build` | no signed updates are published; installed apps stay on their version |
| `KYOKAI_MACOS_CERT_P12`, `KYOKAI_MACOS_CERT_PASSWORD` | `release.yml` (macOS) | the macOS build is ad hoc signed, so privacy grants do not survive updates; the release notes must say so |

The private halves live only in these secrets and in the maintainer's offline copy; the public
update key is `updates.publicKey` in `mygo.json`. Generate a MyGo key pair only with
`go tool mygo keygen -o <new directory>`: the default directory is shared by every MyGo project, and
`-force` would replace another app's key. The helper bundle key (`go run ./tools/sign keygen`) is
used from the privileged helper's phase on.

## Upstream update procedure

Every change set starts with `go run ./tools/deps check`. When an upstream has moved:

1. MyGo: `go run ./tools/deps update`, then `go tool mygo migrate-ui .` (preview), `-write`, and
   review identity and bound-value changes by hand.
2. EasyTier: move `third_party/upstream.json` (tag and commit) and the two Cargo pins in
   `core/kyokai-core/Cargo.toml` together; check that `thunk-noop` still matches the `thunk-rs`
   features EasyTier asks for; drop every `core/deny.toml` advisory ignore that the update makes
   unnecessary; then run two engines against a standard server (`ctl network-set`, `ctl status`
   shows both online and each other).
3. RustDesk: review the upstream diff of every vendored file before moving the pin.
4. Rust: change `rust-toolchain.toml` and the toolchain in the workflows.
5. Run the full gate and record "MyGo vX / EasyTier vY / RustDesk Z / Rust W (checked YYYY-MM-DD)"
   in [version-history.md](version-history.md), also when nothing changed.
