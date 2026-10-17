# AGENTS.md

Operating guide for agents working on **Kyokai (鏡界)**. This file holds **only** rules about how to
work here. Everything describing what the code *is* or *does* lives in `doc/en-us/` — see
[Where to read what](#where-to-read-what).

Kyokai is an AI Computer Use application: a person or an AI agent operates another computer over an
embedded EasyTier P2P network, and AI tasks always run on the computer being operated. It is a Go +
MyGo GUI, a headless engine (`kyokai-svc`) and a Rust core library that both load; Windows and macOS
only for now. Treat the user's message as the change request: plan, implement, verify, report.

## Reading order

When you need to understand code, read in this order and stop as soon as you have what you need:

1. **`doc/en-us/`** — start here, always. `architecture.md` for shape and rules;
   `functions/INDEX.md` to find the right package or crate; the concept docs for behavior.
2. **Comments in the source** — the Function Explanation Layer on each declaration.
3. **The implementation** — only when the docs and comments are insufficient, or when you must
   confirm actual behavior before changing it.

Do not jump straight to reading source bodies. Where docs and code disagree on something you are
about to change, verify against the code, then fix the docs in the same commit.

## Where to read what

| Question | Read |
|---|---|
| Current plan, milestones and open decisions (only while a plan is active) | `PLAN.md` |
| Processes, run modes, packages, crates, FFI, local API, state | `doc/en-us/architecture.md` |
| Secrets, identity, passwords, logging | `doc/en-us/security.md` |
| The embedded EasyTier node: settings, status, trust, build | `doc/en-us/networking.md` |
| The session wire protocol: framing, handshake, login, messages | `doc/en-us/protocol.md` |
| Passwords, permissions, confirmation, pinned keys, rate limits | `doc/en-us/identity-and-access.md` |
| Seeing and operating a host: video, input, AI | `doc/en-us/computer-use.md` |
| How people use Kyokai, step by step | `doc/en-us/user-guide.md` |
| Interface structure, wording, tray, languages | `doc/en-us/gui.md` |
| Verified upstream contracts (MyGo, EasyTier, RustDesk, OpenAI, Claude) and quirks | `doc/en-us/upstream.md` |
| Reused third-party code, licenses and obligations | `doc/en-us/third-party.md` |
| What is implemented and verified per platform | `doc/en-us/platform-notes.md` |
| Environment, commands, dev loop, spikes | `doc/en-us/development.md` |
| Workflows, upstream update procedure, releases, packages | `doc/en-us/ci-cd.md`, `doc/en-us/packaging.md` |
| English→Chinese terminology | `doc/en-us/translation-guide.md` |
| Past releases and pinned upstream versions | `doc/en-us/version-history.md` |
| Per-package and per-crate roles | `doc/en-us/functions/INDEX.md` |

New concept docs are added with the code they describe; add their rows here in the same commit.

## Required workflow

1. Treat the user's message as the modification request.
2. Before editing, fetch the remote(s) and check whether the local branch is behind. Resolve any
   divergence before starting.
3. Run `go run ./tools/deps check`. If MyGo, EasyTier, RustDesk or the Rust toolchain has moved,
   follow [Upstream versions](#upstream-versions) first.
4. Read per [Reading order](#reading-order). Plan when the work is non-trivial; multi-step plans go
   into `PLAN.md`.
5. Keep changes scoped. Do not revert unrelated work in the tree.
6. Update documentation in the same change set — see
   [Documentation maintenance](#documentation-maintenance).
7. Verify — see [Verification](#verification).
8. Report briefly, in English and Chinese: what changed, what was verified (with evidence), the
   current version, the configured remotes, and anything that could not be done.
9. Ask before pushing. The user decides when to release.

## Documentation maintenance

**Docs are the primary artifact. Update them first, and never let them drift.**

Any change to a behavior, a data format, the FFI, the local API, the wire protocol, a run mode or a
user-visible feature updates, in the same commit, the affected concept docs and the
`functions/INDEX.md` row.

Every language directory under `doc/` (currently `en-us` and `zh-cn`) mirrors the others exactly —
same files, headings, tables, and code blocks. `doc/en-us/` is authoritative: any documentation
change updates **all** language directories in the same commit, translated per
`translation-guide.md`; new terms go into its glossary. `go run ./tools/checkdocs` must pass.

**Put explanation in the docs, not here.** This file is for agent instructions only.

**Plans live only in `PLAN.md`** — never in `doc/`. It holds milestones, done-when criteria and open
decisions; delete items as soon as they are done; long-lived contracts move into the docs. When the
plan is complete, delete `PLAN.md`.

Add a `doc/en-us/version-history.md` entry for each release. Documentation-only commits do not
bump versions or create tags.

## Authoring rules

**Function Explanation Layer.** Every function, method and significant type carries a doc comment
(Go doc comments; Rust `///`) whose first sentence states its purpose ("Observe records …").
Non-trivial declarations add short lines as needed:

- `Inputs:` important parameters only
- `Returns:` what the caller receives
- `Side effects:` state, file, network, process or UI effects
- `Notes:` assumptions, edge cases, invariants

Keep comments accurate in the same change as the code: they are the second layer of the reading
order, and a stale comment leads to a wrong change. Every Rust `unsafe` block carries a
`// SAFETY:` comment (clippy `undocumented_unsafe_blocks` is denied), and every FFI export states
who owns each pointer it takes or returns.

**Code guidelines** (shared by the MyApps projects):

- Do not over-scope: make only the changes that are requested or clearly necessary; no speculative
  features, flags or configurability, and no empty crates or packages waiting for later work.
- Do not over-abstract: no interface or trait with a single implementation, no builders for simple
  objects, no helpers for one-time operations; fewer cohesive files beat many thin wrappers; keep
  implementation details private.
- Do not over-defend: validate at true boundaries — FFI entry points, the local API, the network
  protocol, provider responses, files read from disk — and trust internal callers.
- Use the pinned language versions idiomatically (Go 1.27, Rust 2024 edition): exhaustive matches,
  enums with data, generics where they remove duplication.
- Name concisely; reuse existing helpers and conventions; when removing code, remove it completely.
- Choose data structures for their read/write ratio; nothing allocates per frame on the video and
  input paths.

**Tests.** The product requires tests (the scenarios in `PLAN.md`), which overrides the MyApps
default of writing tests only on request. Tests protect product contracts and error-prone logic,
preferably table-driven; every fixed bug gets a regression test. No tests for wording, internal
state details or evidence collection.

Other conventions:

- MyGo UI code follows `.agents/skills/mygo-maintenance`: views are functions of state, elements
  are never stored, background results reach the view through `Window.Update`, controls get stable
  keys where state applies.
- Interface copy is written for the person using the app. Never surface reason codes, keys,
  hashes, fingerprints (outside the identity page), socket paths or internal IDs in the main
  interface; the Device ID is the only identifier people see.
- Interface strings go through the ARB catalogs (en, zh-Hans, zh-Hant, ja);
  `go tool mygo i18n check` must pass.
- Layout numbers and colors are named tokens, not literals scattered through views.

## Behavior contract

Do not change these without the user explicitly deciding to:

- **AI runs only on the host.** A controller never needs or receives the host's provider keys.
  The model's proposals are validated and executed by the host's own executor; nothing runs shell
  commands or code on the user's desktop.
- **Secrets never travel.** Provider keys, the network secret, passwords, private keys and session
  keys are never logged, sent, cached into snapshots or stored in plaintext; without a working OS
  credential store they are not persisted at all.
- **Network membership grants nothing.** Every session is authenticated and authorized per
  permission; permissions never imply each other; the model cannot grant itself anything.
- **The host user always wins.** Local input, take-over and emergency stop revoke the AI input
  lease at once; a model response that arrives after a pause or cancel never acts.
- **Remote input never reaches Kyokai's own windows**, and Kyokai's windows are excluded from
  capture unless the host user turns that off in Settings (on by default).
- **No replay.** Input events and AI actions are never re-sent after a reconnect; task creation is
  idempotent; stale observations are rejected.
- **Default posture.** No autostart, no service, no elevation, no TUN, no unattended access, no AI
  after exit. Administrator prompts appear only when installing or uninstalling the privileged
  helper or updating it from an unsigned build — never when the GUI starts. The GUI never runs
  elevated.
- **The privileged helper** authorizes only the operator and the `kyokai` group, runs only its
  protected copies, verifies the bytes it installs, and never decodes remote media.
- **Standard EasyTier servers only.** No custom coordination server; nothing from RustDesk's
  hbbs/hbbr infrastructure.
- **Versioned boundaries.** The FFI (`kyokai_abi_version`), the local API and the wire protocol are
  versioned; enums cross as names; changing a field's meaning bumps the version.
- **Never show stale data as current.**

## Upstream versions

Every change set keeps the upstreams current: the latest MyGo tag; EasyTier at the latest tag of
the line Kyokai embeds (the v2.7 prerelease line, pinned by commit, until a v2.7.x stable ships;
the moving `nightly` tag is never used); RustDesk's vendored code reviewed against its latest
release; the Rust toolchain in `rust-toolchain.toml`. When `go run ./tools/deps check` reports
an update, move it following `doc/en-us/ci-cd.md`, re-run the EasyTier integration test when
EasyTier moves, and add a `version-history.md` line naming the versions. Record "checked,
unchanged" too.

## Verification

```bash
gofmt -l cmd internal tools
go vet ./...
go test ./...
CGO_ENABLED=0 go test ./...
go tool mygo vet .
go tool mygo i18n check
go run ./tools/checkdocs
cargo fmt --manifest-path core/Cargo.toml --all --check
cargo clippy --manifest-path core/Cargo.toml --workspace --all-targets -- -D warnings
cargo test --manifest-path core/Cargo.toml --workspace
cargo deny --manifest-path core/Cargo.toml check
```

- Gated live tests (EasyTier against an official core, OpenAI, Claude) run only when their
  environment variables are set; keys are set by the user in their own terminal, never committed.
- Run a real session when a change touches the engine, the local API or a run mode. Inspect the
  native app (screenshots) when a change touches layout.
- Input and capture tests need two machines: one desktop feeds injected input back to itself.
- **Never claim "verified" without an actual run;** record what was verified where
  (`doc/en-us/platform-notes.md`).
- Installing the privileged helper needs administrator rights: ask the user first, and uninstall it
  after testing unless told otherwise.

## Release, version, commit, tag, push

The version lives in `internal/version` (`Version`); `mygo.json` must match (a test enforces it).
The Rust core receives the version from the binary that starts it.

For ordinary work, do not bump versions or tag until the user confirms the release version and
confirms pushing. When the user confirms:

1. Set the version in `internal/version/version.go` and `mygo.json`.
2. Bring the upstreams to their latest versions and re-run verification.
3. Commit, including the `doc/en-us/version-history.md` entry (and its mirror).
4. Create an annotated tag `vX.Y.Z`.
5. Push **the commit first**, then the tag, to `origin` and `github`.

GitHub Actions release builds trigger on tag pushes to `github`. Signing keys and certificates live
only in GitHub secrets and the maintainer's offline copy.

## Agent co-author attribution

An agent that made a real, material contribution to a commit may add its own accurate
`Co-authored-by:` trailer. Attribution is per commit: do not add an agent merely because it
reviewed, observed, or continued another agent's work, and never copy a trailer automatically from
an earlier commit. When multiple agents materially contributed, include one accurate trailer each.
Use the agent's actual documented identity; never invent a provider, model, name, or email.
Approved examples are `Co-authored-by: Codex <noreply@openai.com>` and, for Claude Code,
`Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>` with the model name replaced by the actual
model that did the work.

## Remotes and secrets

- `origin` → `<local_gitea_address>` (private Gitea)
- `github` → `git@github.com:YuanZhe-99/Kyokai.git` (public)

Determine the repository path from the runtime workspace; do not hardcode a machine-specific
absolute path here.

**Masking rule:** keep the `origin` URL written as `<local_gitea_address>` in every committed file.
Never write the underlying host or port anywhere in the repo. The GitHub repository is public:
committed files never name private repositories, machines or where keys are kept.

**Never commit:** secrets, signing keys or certificates, API keys, network secrets, `.dev/`,
`build/`, `core/target/`, built files under `resources/<goos>-<goarch>/`, or machine-specific
state.

## MyApps milestone releases

For each completed milestone, increment the patch version by 0.0.1; the user decides when to
release. Documentation-only changes do not bump the version.

License: AGPL-3.0-only (`LICENSE`). Code taken from Easytier-MyGo (GPL-3.0, same author) is
relicensed under AGPL-3.0 here. Vendored RustDesk code keeps its notices; never copy code that
carries no license grant (RustDesk's `hbb_common` and `hwcodec`). EasyTier is LGPL-3.0 and ships
with its license texts and source location.
