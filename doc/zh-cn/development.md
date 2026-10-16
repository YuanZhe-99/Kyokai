# 开发

## 环境要求

- Go 1.27.2（`go.mod`；较旧的 Go 会自动下载它）。MyGo 是 Go 工具依赖（`go tool mygo …`）。
- Rust 1.99.1，带 `rustfmt` 与 `clippy`（`rust-toolchain.toml`；rustup 首次使用时安装）。
- Windows：Visual Studio 2026（或其 Build Tools），装好目标架构（ARM64 和/或 x64）的 C++ 工具、Windows SDK，以及
  “C++ Clang Compiler for Windows” 组件——`ring` 在 `aarch64-pc-windows-msvc` 上需要 clang；请把 VS 安装中的 `VC\Tools\Llvm\ARM64\bin`
  加入 `PATH`（该组件不会自动加入）。Git for Windows 提供 bash 与 OpenSSL。
- macOS：Xcode 命令行工具；Go 与 rustup（可用 Homebrew）；内嵌 EasyTier 后还需要 `protoc`（Homebrew 的 `protobuf`）。
- 依赖检查用 `cargo-deny`（`cargo install cargo-deny --locked`）；在 Windows ARM64 上要装好 clang 才能构建。

## 命令

```bash
go run ./tools/deps check          # pinned vs latest MyGo, EasyTier, RustDesk, Rust
gofmt -l cmd internal tools
go vet ./...
go test ./...
CGO_ENABLED=0 go test ./...
go tool mygo vet .
go tool mygo i18n check
go run ./tools/checkdocs           # doc/en-us and doc/zh-cn mirror each other
cargo fmt --manifest-path core/Cargo.toml --all --check
cargo clippy --manifest-path core/Cargo.toml --workspace --all-targets -- -D warnings
cargo test --manifest-path core/Cargo.toml --workspace
cargo deny --manifest-path core/Cargo.toml check
```

请在仓库根目录运行 cargo：`rust-toolchain.toml` 与 `.cargo/config.toml` 是从当前目录读取的，而不是从
`--manifest-path` 所在目录。

## 开发流程

```bash
scripts/build.sh windows/arm64           # or no target: this machine's platform
KYOKAI_PROFILE=dev build/windows-arm64/Kyokai.exe
KYOKAI_PROFILE=dev build/windows-arm64/bin/kyokai-svc.exe ctl status
scripts/smoke.sh user                    # the engine end to end with the real core
SCREENSHOT_DIR=$PWD/.dev/shots go test ./internal/gui/
KYOKAI_CORE_LIB=/abs/path/kyokai_core.dll go test ./internal/core/
```

`KYOKAI_PROFILE` 让开发实例与已安装的 Kyokai 分开：拥有自己的应用名、单实例锁、套接字与状态目录（`Kyokai-<profile>`）。
`KYOKAI_SVC` 让界面使用另一个引擎可执行文件，`KYOKAI_SOCKET` 指定另一个套接字。ABI 冒烟测试需要库的绝对路径。

## 可行性验证

`spikes/` 存放 Phase 0 的可行性验证程序，各自带有自己的 `go.mod` 或 Cargo 工作区，产品检查不会构建它们。结果写入
[upstream.md](upstream.md) 与 [platform-notes.md](platform-notes.md)；验证成功的代码在之后的阶段移入产品。

## 测试原则

测试保护产品契约和容易出错的逻辑，优先使用表驱动；每个修复的缺陷都要有回归测试。不为措辞、内部状态细节或收集证据写测试。
需要网络、官方 EasyTier 内核或服务商密钥的测试只在设置了相应环境变量时运行。`go test ./...` 保持在一分钟以内。
