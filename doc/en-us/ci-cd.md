# CI/CD

Workflows live in `.github/workflows` and run on GitHub. The private remote has no runner, so a
push there is checked only by the local gate.

| Workflow | Trigger | Does |
|---|---|---|
| `ci.yml` → changes | push to `main`, pull requests, manual | whether anything outside `doc/` and Markdown files changed since the push's previous commit or the pull request's base (always yes when run by hand); a docs-only change runs the checks but not the test jobs |
| `ci.yml` → checks | push to `main`, pull requests, manual | gofmt, `go vet` for Windows, macOS and Linux, interface catalogs, doc mirror, `cargo fmt`, `cargo deny`, secret scan (gitleaks), upstream pins (informational) |
| `ci.yml` → rust | same, when not docs-only | `windows-11-arm`, `macos-latest` and `ubuntu-24.04` (with `libpipewire-0.3-dev`, `libclang-dev` and NASM): clippy and the Rust tests, beside the test jobs instead of before them |
| `ci.yml` → test | same, when not docs-only | `windows-11-arm` and `macos-latest`: the Go tests with the real core (ABI smoke test), `mygo vet`, `scripts/smoke.sh user`, unsigned packages as artifacts; `windows-latest` and `macos-15-intel`: core build and Go tests, and `windows-latest` also uploads the unsigned x64 package (installed on the remote test VM). Every job, the rust jobs too, uses the pinned `protoc` (`scripts/protoc.sh`) and gets libvpx and libaom through `.github/actions/codecs`: the install tree `core/vcpkg_installed` for the runner's system and architecture, the targets and `core/vcpkg*.json` and the triplets comes from `main`'s cache, and only a miss runs vcpkg (cloned at the pinned commit and bootstrapped, its binary cache restored from the newest saved for the runner's system and architecture and the same files — main's too for a release tag — and saved again only when the run built something, since vcpkg's own key includes the compiler and a runner image with a newer one misses the binaries; NASM from Homebrew on macOS; then `scripts/codecs.sh`), after which `main` saves the tree; `windows-11-arm` puts LLVM's clang on `PATH` for `ring`; the macOS jobs run with their own unlocked default keychain. The core is built once in release for the runner's own target triple, where `scripts/smoke.sh` finds it and the package takes it as built (`CORE_PREBUILT`), with LTO off and 16 code-generation units (`CARGO_PROFILE_RELEASE_*`), at optimization level 1 on `macos-15-intel`, which packages nothing: these packages are for testing, and releases keep `core/Cargo.toml`'s profile. Before the Rust cache each job uninstalls every toolchain but the pinned one, so a runner image's own Rust does not change the cache's key; only `main` saves the Rust cache (the test jobs under a key of their own: they hold the release build only), other refs restore it; `ubuntu-24.04`: the full gate with the smoke test on a session bus of its own with gnome-keyring and an unencrypted default keyring (a runner has no Secret Service, and an empty-password unlock makes no default collection) and the unsigned `.deb`; `ubuntu-24.04-arm`: core build, Go tests and the `.deb` |
| `release.yml` | tag `v*`, manual | Windows cores on `windows-latest` (x64) and `windows-11-arm`, each with its codec libraries (`.github/actions/codecs`), packaged on Ubuntu with NSIS; macOS built and packaged on `macos-latest`: the codec libraries for both slices (x64 cross-built), both cores in one cargo run, packaged as built (`CORE_PREBUILT`); a run by hand on `main` saves the codec trees a tag then restores; each disk image verified (`hdiutil verify`, a strict `codesign --verify` of the app in it, Gatekeeper's verdict printed); Linux built and packaged on `ubuntu-24.04` (x64) and `ubuntu-24.04-arm`: the `.deb`, the archive with `install.sh` (published once) and the AppImage (`scripts/appimage.sh`); a tag publishes with `SHA256SUMS.txt` after checking that it matches `internal/version`; spaces in file names become dots first, as GitHub stores them |
| `upstream.yml` | weekly, manual | `go run ./tools/deps check -strict`; opens an issue when MyGo, EasyTier, RustDesk, vcpkg or Rust has moved |
| `dependabot.yml` | weekly | Actions, Go modules (MyGo excluded) and Cargo |

## Secrets

| Secret | Used by | Effect when unset |
|---|---|---|
| `MYGO_UPDATER_PRIVATE_KEY` | `release.yml`, `mygo build` | no signed updates are published; installed apps stay on their version |
| `KYOKAI_SIGNING_KEY` | `release.yml` (Windows), `scripts/build.sh` | the helper's files are unsigned; an installed helper updates only with an administrator prompt |
| `KYOKAI_MACOS_CERT_P12`, `KYOKAI_MACOS_CERT_PASSWORD` | `release.yml` (macOS): the Apple Development certificate (`.p12`, base64) and its password | the macOS build is ad hoc signed, so privacy grants and Keychain access do not survive updates, and the release notes must say so |

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
