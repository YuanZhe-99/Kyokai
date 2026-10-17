# Function index

One row per Go package and Rust crate: its role and the declarations to read first. Declarations
carry the Function Explanation Layer in their doc comments (first sentence: purpose; then inputs,
returns, side effects and notes where non-trivial).

| Package / crate | Role | Start with |
|---|---|---|
| `cmd/kyokai` | GUI entry point | `main` |
| `cmd/kyokai-svc` | Engine entry point | `run`, `ctl` |
| `internal/gui` | GUI state, views (Home, Network, sessions and confirmations, Settings), the viewer window, tray, updates, the engine child | `App`, `App.View`, `App.connectLoop`, `engineChild.ensure` |
| `internal/engine` | Engine host | `Run`, `Options`, `DefaultStateDir` |
| `internal/localapi` | Local API protocol, server, client, authorization | `ProtocolVersion`, `Request`, `Status`, `NetworkSettings`, `NetworkStatus`, `SessionInfo`, `HostRequest`, `Client.Attach`, `Authorizer.Allows` |
| `internal/core` | purego binding to the Rust core | `Load`, `Lib.Start`, `Lib.Call` |
| `internal/version` | Product version | `Version`, `Older` |
| `internal/fsutil` | Atomic writes and JSON documents | `WriteFileAtomic`, `WriteJSON`, `ReadJSON` |
| `core/kyokai-core` | Rust core: C ABI, secrets, identity, temporary password, EasyTier node | `kyokai_start`, `kyokai_call`, `store::open`, `Identity::load_or_create`, `device_id`, `net::Network`, `session::serve`, `session::connect`, `access::Access`, `auth::Slots`, `backup::export`, `backup::import`, `media::Reassembler`, `media::Queue`, `kyokai_media_read` |
| `core/third_party/thunk-noop` | No-op stand-in for EasyTier's `thunk-rs` | `thunk` |
| `tools/deps` | Upstream pins | `check`, `update` |
| `tools/checkdocs` | Documentation mirror check | `main` |
| `tools/sign` | Helper bundle signing key | `keygen` |
| `tools/icon` | Draws the app icon and the tray glyph | `app`, `tray`, `render` |
