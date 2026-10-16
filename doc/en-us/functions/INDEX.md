# Function index

One row per Go package and Rust crate: its role and the declarations to read first. Declarations
carry the Function Explanation Layer in their doc comments (first sentence: purpose; then inputs,
returns, side effects and notes where non-trivial).

| Package / crate | Role | Start with |
|---|---|---|
| `core/kyokai-core` | The native core library and its C ABI | `kyokai_abi_version`, `ABI_VERSION` |
| `internal/version` | Product version | `Version`, `Older` |
| `internal/fsutil` | Atomic writes and JSON documents | `WriteFileAtomic`, `WriteJSON`, `ReadJSON` |
| `tools/deps` | Upstream pins | `check`, `update` |
| `tools/checkdocs` | Documentation mirror check | `main` |
| `tools/sign` | Helper bundle signing key | `keygen` |
