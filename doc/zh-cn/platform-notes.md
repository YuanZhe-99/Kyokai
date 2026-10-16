# 平台说明

| 平台 | 状态 |
|---|---|
| Windows（x64、ARM64） | 仅有脚手架：Go 与 Rust 检查在 Windows 11 ARM64 上通过（见下）。尚无应用。 |
| macOS（Apple 芯片、Intel） | 仅有脚手架；尚未在 macOS 上构建或运行。 |
| Linux | 暂不支持。 |

## Windows

于 Windows 11 ARM64 验证（Snapdragon 开发套件，2026-10-16；Go 1.27.2、Rust 1.99.1、未安装 Clang 组件的 Visual
Studio 2026）：

| 检查 | 结果 |
|---|---|
| Go 模块 | `go vet ./...` 与 `go test ./...` 通过 |
| Rust 核心 | `kyokai-core` 的 `cargo fmt`、`cargo clippy -D warnings` 与 `cargo test` 通过 |
| `cargo-deny` | 暂时无法构建：ARM64 上的 `ring` 需要 clang |

## 已知限制

- 安装 Clang 组件之前，依赖 `ring` 的内容（EasyTier、TLS 客户端、`cargo-deny`）都无法在 Windows ARM64 上构建。
