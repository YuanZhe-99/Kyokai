# CI/CD

Workflows live in `.github/workflows` and run on GitHub. The private remote has no runner, so a
push there is checked only by the local gate.

| Workflow | Trigger | Does |
|---|---|---|
| `ci.yml` → changes | push to `main`, pull requests, manual | whether anything outside `doc/` and Markdown files changed since the push's previous commit or the pull request's base (always yes when run by hand); a docs-only change runs the checks but not the test jobs |
| `ci.yml` → checks | push to `main`, pull requests, manual | gofmt, `go vet` for Windows and macOS, interface catalogs, doc mirror, `cargo fmt`, `cargo deny`, secret scan (gitleaks), upstream pins (informational) |
| `ci.yml` → test | same, when not docs-only | `windows-11-arm` and `macos-latest`: clippy, Rust tests, the Go tests with the real core (ABI smoke test), `mygo vet`, `scripts/smoke.sh user`, unsigned packages as artifacts; `windows-latest` and `macos-15-intel`: core build and Go tests, and `windows-latest` also uploads the unsigned x64 package (installed on the remote test VM). Every job uses the pinned `protoc` (`scripts/protoc.sh`) and builds libvpx and libaom through `.github/actions/codecs` (vcpkg cloned at the pinned commit and bootstrapped, its binary cache kept with `actions/cache` keyed on `core/vcpkg*.json` and the triplets, NASM from Homebrew on macOS, then `scripts/codecs.sh`); `windows-11-arm` puts LLVM's clang on `PATH` for `ring`; the macOS jobs run with their own unlocked default keychain. The core is built once in release for the runner's own target triple, where `scripts/smoke.sh` and `scripts/build.sh` find it, with LTO off and 16 code-generation units (`CARGO_PROFILE_RELEASE_*`): these packages are for testing, and releases keep `core/Cargo.toml`'s profile. Only `main` saves the Rust cache; other refs restore it |
| `release.yml` | tag `v*`, manual | Windows cores on `windows-latest` (x64) and `windows-11-arm`, each with its codec libraries (`.github/actions/codecs`), packaged on Ubuntu with NSIS; macOS built and packaged on `macos-latest`, the codec libraries for both slices built there (x64 cross-built); a tag publishes with `SHA256SUMS.txt` after checking that it matches `internal/version` |
| `upstream.yml` | weekly, manual | `go run ./tools/deps check -strict`; opens an issue when MyGo, EasyTier, RustDesk, vcpkg or Rust has moved |
| `dependabot.yml` | weekly | Actions, Go modules (MyGo excluded) and Cargo |

## Secrets

| Secret | Used by | Effect when unset |
|---|---|---|
| `MYGO_UPDATER_PRIVATE_KEY` | `release.yml`, `mygo build` | no signed updates are published; installed apps stay on their version |
| `KYOKAI_SIGNING_KEY` | `release.yml` (Windows), `scripts/build.sh` | the helper's files are unsigned; an installed helper updates only with an administrator prompt |
| `KYOKAI_MACOS_CERT_P12`, `KYOKAI_MACOS_CERT_PASSWORD` | `release.yml` (macOS) | the macOS build is ad hoc signed, so privacy grants do not survive updates; the release notes must say so |

The private halves live only in these secrets and in the maintainer's offline copy; the public
update key is `updates.publicKey` in `mygo.json`. Generate a MyGo key pair only with
`go tool mygo keygen -o <new directory>`: the default directory is shared by every MyGo project, and
`-force` would replace another app's key. The helper bundle key (`go run ./tools/sign keygen`) signs the
helper's files (`go run ./tools/sign bundle`); its public half is `internal/bundlesig/release.pub`.

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
4. vcpkg: move `third_party/upstream.json` (tag and commit) and the baseline in
   `core/vcpkg-configuration.json` together (`tools/deps` checks they match); run
   `scripts/codecs.sh`, regenerate the bindings ([development.md](development.md)), review the
   libvpx and libaom release notes and license files (`third_party/licenses`), and run the codec
   round-trip tests (`cargo test -p kyokai-core soft`).
5. Rust: change `rust-toolchain.toml` and the toolchain in the workflows.
6. Run the full gate and record "MyGo vX / EasyTier vY / RustDesk Z / vcpkg V / Rust W (checked YYYY-MM-DD)"
   in [version-history.md](version-history.md), also when nothing changed.
