# 平台说明

| 平台 | 状态 |
|---|---|
| Windows（x64、ARM64） | 0.1.0 基础已实现，并在 Windows 11 ARM64 上验证，x64 在模拟下验证（见下）。 |
| macOS（Apple 芯片、Intel） | 已实现并交叉检查（`go vet`、`cargo check`）；尚未在 Mac 上构建或运行——首次推送后由 CI 与真机验证。 |
| Linux | 暂不支持。 |

## Windows

于 Windows 11 ARM64 验证（Snapdragon 开发套件，2026-10-16；Go 1.27.2、Rust 1.99.1、未安装 Clang 组件的 Visual
Studio 2026）：

| 检查 | 结果 |
|---|---|
| 本地检查 | gofmt、`go vet`（Windows 与 macOS）、`go test`（含 `CGO_ENABLED=0`）、`mygo vet`、`mygo i18n check`、`checkdocs`、`cargo fmt`、`cargo clippy -D warnings`、`cargo test` 通过 |
| ABI 冒烟测试 | `internal/core` 加载 release 版 `kyokai_core.dll`，启动、调用并停止它 |
| 核心库 | 只引用系统 DLL（`KERNEL32`、`ntdll`、`api-ms-win-core-synch`）：C 运行时为静态链接 |
| 机密 | 主密钥受 DPAPI 保护；引擎与应用重启后设备 ID 不变 |
| 引擎 | `scripts/smoke.sh user` 通过：状态、密码、换密码、拒绝第二个引擎、关闭、重启 |
| 应用（ARM64 安装包） | 不弹任何提示就以子进程启动引擎；每次启动临时密码都不同；强制结束界面后引擎随之结束；`Run` 下没有注册任何项 |
| 应用（x64 安装包） | 在模拟下以同一身份运行；窗口按系统深色主题渲染 |
| 安装包 | `scripts/build.sh windows/arm64 windows/amd64` 为两者构建 `Kyokai.exe` 与按用户安装的安装包 |
| S2 画面可行性验证 | 来自 Rust 的 1920×1080 RGBA 帧经 `ui.Bitmap` 显示在 500 行列表旁，59–60 fps；画面显示约占一个核心的 21–25 % |
| `cargo-deny` | 暂时无法在本机构建：ARM64 上的 `ring` 需要 clang（由 CI 运行） |

## 已知限制

- 在开发机上，`AppData\Local` 或 `AppData\Roaming` 下文件夹中创建的 AF_UNIX 套接字文件在监听关闭后会残留为无法访问的条目，
  导致再次绑定失败；`%TEMP%` 与用户文件夹表现正常。因此引擎的套接字放在 `%TEMP%\kyokai`。残留条目
  （`AppData\Local\kyokai` 与测试文件夹）无法在创建它们的会话中删除。
- 安装 Clang 组件之前，依赖 `ring` 的内容（EasyTier、TLS 客户端、`cargo-deny`）都无法在 Windows ARM64 上构建。
- Windows 安装包没有代码签名；首次启动时 SmartScreen 会警告。
