# 开发

## 环境要求

- Go 1.27.2（`go.mod`；较旧的 Go 会自动下载它）。MyGo 是 Go 工具依赖（`go tool mygo …`）。
- Rust 1.99.1，带 `rustfmt` 与 `clippy`（`rust-toolchain.toml`；rustup 首次使用时安装）。
- Windows：Visual Studio 2026（或其 Build Tools），装好目标架构（ARM64 和/或 x64）的 C++ 工具、Windows SDK，以及
  “C++ Clang Compiler for Windows” 组件——`ring` 在 `aarch64-pc-windows-msvc` 上需要 clang；请把 VS 安装中的 `VC\Tools\Llvm\ARM64\bin`
  加入 `PATH`（该组件不会自动加入）。Git for Windows 提供 bash 与 OpenSSL。
- macOS：Xcode 命令行工具；Go 与 rustup（可用 Homebrew）；vcpkg 构建 aom 需要的 `pkg-config`
  （`brew install pkgconf`）。
- EasyTier 协议文件需要的 `protoc`：`scripts/protoc.sh` 把固定版本（36.2）下载到 `build/tools`（只下载一次），校验
  其 SHA-256 并输出路径。`scripts/build.sh` 与 `scripts/smoke.sh` 会自行使用它；直接运行 cargo 命令前先执行
  `export PROTOC=$(scripts/protoc.sh)`。没有 `PROTOC` 时，EasyTier 的构建会在 Windows 上下载未经校验的候选版本。
- 依赖检查用 `cargo-deny`（`cargo install cargo-deny --locked`）；在 Windows ARM64 上要装好 clang 才能构建。
- `scripts/build.sh` 放入每个安装包的许可证文件需要 `cargo-about`（`cargo install cargo-about --locked --features cli`）。
- libvpx 1.16.0 与 libaom 3.13.3 静态链接进核心：`scripts/codecs.sh` 以清单模式用 vcpkg 构建它们（`core/vcpkg.json`，
  注册表固定在 `core/vcpkg-configuration.json`，覆盖三元组在 `core/vcpkg-triplets`），装到
  `core/vcpkg_installed/<triplet>`（不提交）；每个目标在 cargo 之前运行一次，vcpkg 固定版本变动后再运行。它优先用
  `VCPKG_ROOT`，否则在 Windows 上用 Visual Studio 2026 自带的 vcpkg（“vcpkg package manager” 组件，在其安装目录中只读），
  再否则用 `PATH` 中的 `vcpkg`。vcpkg 把它的工具（CMake、供 libvpx 的 configure 使用的便携 MSYS2 与 Perl、x64 用的
  NASM）、下载与二进制缓存放在它共用的默认位置（Windows 上为 `%LOCALAPPDATA%\vcpkg`）；不做系统级安装，也不修改
  `PATH`。macOS 的 x64 部分需要 `brew install nasm`。`KYOKAI_CODECS_DIR` 让核心的构建改用另一个安装目录。
- libvpx 与 libaom 的绑定（`core/third_party/rustdesk-codec/generated`）预先生成，构建既不需要 bindgen 也不需要
  libclang。移动 vcpkg 固定版本后，用 `bindgen-cli`（`cargo install bindgen-cli --locked`）与 clang 的 `libclang`
  （Windows 上把 Visual Studio 的 `VC\Tools\Llvm\ARM64\bin` 设为 `LIBCLANG_PATH`）重新生成：

  ```bash
  inc=core/vcpkg_installed/arm64-windows-static/arm64-windows-static/include
  out=core/third_party/rustdesk-codec
  bindgen $out/src/bindings/vpx_ffi.h -o $out/generated/vpx_ffi.rs --allowlist-type '^[vV].*' \
    --allowlist-var '^[vV].*' --allowlist-function '^[vV].*' --rustified-enum '^[vV].*' \
    --distrust-clang-mangling --no-layout-tests --no-doc-comments -- -I$inc
  bindgen $out/src/bindings/aom_ffi.h -o $out/generated/aom_ffi.rs \
    --allowlist-type '^(aom|AOM|OBU|AV1).*' --allowlist-var '^(aom|AOM|OBU|AV1).*' \
    --allowlist-function '^(aom|AOM|OBU|AV1).*' --rustified-enum '^(aom|AOM|OBU|AV1).*' \
    --distrust-clang-mangling --no-layout-tests --no-doc-comments -- -I$inc
  ```

  这是 RustDesk 的设置（`libs/scrap/build.rs`）。在 Kyokai 构建的每个 64 位目标上，声明都相同：C 的 `long` 保持为
  `c_long`。

## 命令

```bash
go run ./tools/deps check          # pinned vs latest MyGo, EasyTier, RustDesk, vcpkg, Rust
scripts/codecs.sh                  # libvpx and libaom for this platform (once, before cargo)
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
KYOKAI_NETWORK_SECRET=… kyokai-svc ctl network-set --server tcp://host:11010 --name net
KYOKAI_PASSWORD=… kyokai-svc ctl connect --device 482916735   # then on the host: ctl allow --id N
KYOKAI_BACKUP_PASSPHRASE=… kyokai-svc ctl backup --file id.json   # restore --file, reset-identity --yes
scripts/smoke.sh user                    # the engine end to end with the real core
SCREENSHOT_DIR=$PWD/.dev/shots go test ./internal/gui/
KYOKAI_CORE_LIB=/abs/path/kyokai_core.dll go test ./internal/core/
```

`KYOKAI_PROFILE` 让开发实例与已安装的 Kyokai 分开：拥有自己的应用名、单实例锁、套接字与状态目录（`Kyokai-<profile>`）。
`KYOKAI_SVC` 让界面使用另一个引擎可执行文件，`KYOKAI_SOCKET` 指定另一个套接字。ABI 冒烟测试需要库的绝对路径。`ctl network-set` 从
`KYOKAI_NETWORK_SECRET` 读取网络密钥，从不从命令行读取；`ctl network` 显示不含密钥的设置。同一台机器上使用不同
配置、状态目录和套接字的两个引擎，可以通过同一个网络互相看到。

在 macOS 上，发布用的签名身份进入登录钥匙串后，请用它为本地构建签名（`MYGO_SIGN=<身份或其 SHA-1> scripts/build.sh`）：临时签名的构建每次都是新身份，会丢失隐私授权。请用 `open` 而不是从 shell 启动应用，这样 macOS 才会把它的引擎算作 Kyokai 的一部分；从 shell 启动的引擎没有授权，只会发送占位画面。`ctl watch --type T --keys meta+a,enter` 会像查看窗口那样向被控端输入。

## 可行性验证

`spikes/` 存放 Phase 0 的可行性验证程序，各自带有自己的 `go.mod` 或 Cargo 工作区，产品检查不会构建它们。结果写入
[upstream.md](upstream.md) 与 [platform-notes.md](platform-notes.md)；验证成功的代码在之后的阶段移入产品。

## 测试原则

测试保护产品契约和容易出错的逻辑，优先使用表驱动；每个修复的缺陷都要有回归测试。不为措辞、内部状态细节或收集证据写测试。
需要网络、官方 EasyTier 内核或服务商密钥的测试只在设置了相应环境变量时运行。`go test ./...` 保持在一分钟以内。

AI 运行时的测试驱动一个模拟桌面和一个本地的服务商替身。设置 `KYOKAI_LIVE_CLAUDE=1` 与 `ANTHROPIC_API_KEY` 后，
`cargo test ... live_claude` 会在合成屏幕上运行一次真实的 Claude 任务；`cargo test ... real_screen -- --ignored` 在有桌面的电脑上检查真实采集与输入钩子
（只把屏幕读入内存，不注入任何输入）。
