# 平台说明

| 平台 | 状态 |
|---|---|
| Windows（x64、ARM64） | 0.1.0 基础已实现，并在 Windows 11 ARM64 上验证，x64 在模拟下验证（见下）。 |
| macOS（Apple 芯片、Intel） | 已在 GitHub 的 macOS 运行器上构建与测试（Apple 芯片：完整检查，包括 `scripts/smoke.sh user` 与未签名安装包；Intel：构建与 Go 测试）；尚未在真实 Mac 上运行。 |
| Linux | 暂不支持。 |

## Windows

于 Windows 11 ARM64 验证（Snapdragon 开发套件，2026-10-16；Go 1.27.2、Rust 1.99.1、未安装 Clang 组件的 Visual
Studio 2026）：

| 检查 | 结果 |
|---|---|
| 本地检查 | gofmt、`go vet`（Windows 与 macOS）、`go test`（含 `CGO_ENABLED=0`）、`mygo vet`、`mygo i18n check`、`checkdocs`、`cargo fmt`、`cargo clippy -D warnings`、`cargo test` 通过 |
| ABI 冒烟测试 | `internal/core` 加载 release 版 `kyokai_core.dll`，启动、调用并停止它 |
| 核心库 | 只引用系统 DLL（`KERNEL32`、`ntdll`、`api-ms-win-core-synch`，内嵌 EasyTier 后还有 `ws2_32`、`iphlpapi`、`bcrypt`、`crypt32` 等）：ARM64 与 x64 的 C 运行时均为静态链接——没有 `msvcrt.dll` |
| 机密 | 主密钥受 DPAPI 保护；引擎与应用重启后设备 ID 不变 |
| 引擎 | `scripts/smoke.sh user` 通过：状态、密码、换密码、拒绝第二个引擎、关闭、重启 |
| 应用（ARM64 安装包） | 不弹任何提示就以子进程启动引擎；每次启动临时密码都不同；强制结束界面后引擎随之结束；`Run` 下没有注册任何项 |
| 应用（x64 安装包） | 在模拟下以同一身份运行；窗口按系统深色主题渲染 |
| 安装包 | `scripts/build.sh windows/arm64 windows/amd64` 为两者构建 `Kyokai.exe` 与按用户安装的安装包 |
| S2 画面可行性验证 | 来自 Rust 的 1920×1080 RGBA 帧经 `ui.Bitmap` 显示在 500 行列表旁，59–60 fps；画面显示约占一个核心的 21–25 % |
| S3-lite 采集可行性验证 | 复用并解除 `hbb_common` 耦合的 RustDesk DXGI 采集器以约 60 fps 采集 4K 显示器；转换为 RGBA 并缩放到 1080p 约占一个核心的 15 % |
| S1 EasyTier 可行性验证 | v2.7.0 进程内内嵌、无 TUN、无需管理员：经原版服务器双向数据面可用，白名单有效，可干净重启；UDP 打洞未测（对称型 NAT） |
| 引擎内的 EasyTier（2026-10-17） | 经标准公共服务器：10 秒内上线并获得 DHCP 地址；通过 STUN 得到 NAT 类型（此网络为对称型）；使用各自配置的两个引擎互相列出对方的设备 ID，经中继，约 36 ms |
| 会话（2026-10-17） | 两个引擎经标准服务器：错误密码在两端都记为密码错误而被拒绝；正确的临时密码会触发被控端确认，确认只授予查看屏幕，会话即以此权限连接；随后控制端成为受信任设备（“已知”）；固定密码同样可用；从控制端关闭会话会同时结束两端 |
| 身份备份（2026-10-17） | 备份 → 重置（新设备 ID）→ 用错误口令恢复被拒绝，用正确口令恢复后旧设备 ID、固定密码与受信任设备都回来了，网络重新连接 |
| 远程 x64 虚拟机（2026-10-17） | CI 构建的 x64 安装包按用户安装、无任何提示；其引擎经标准服务器加入网络（两端都是对称型 NAT，因此经中继，约 99 ms）；从本机到虚拟机的会话在虚拟机上触发确认，并以授予的子集权限连接；虚拟机节点重启后，会话在 7 秒内恢复，权限不变且无需再次确认 |
| 视频通道（2026-10-17） | 测试图案（320×180 原始 RGBA）经两个引擎与公共服务器的中继：4 fps、约 7.6 Mbit/s，即这条路径上中继的上限；视频流描述能到达，并会在关键帧请求时重发 |
| 屏幕视频（2026-10-17） | 本机 1920×1080 屏幕经 DXGI、Media Foundation H.264（软件编码器；第一帧之后即有输出）、两个引擎与公共中继，再由查看窗口的解码器解码：屏幕大多静止时 15.5 fps、约 1 Mbit/s，画面正确 |
| `cargo deny` | 把 VS 的 clang 加入 `PATH` 后通过（advisories、bans、licenses、sources） |

## macOS

| 检查 | 结果 |
|---|---|
| CI（`macos-latest`、`macos-15-intel`） | 检查、ABI 冒烟测试、引擎冒烟测试与未签名安装包均通过 |
| 钥匙串 | 测试程序读取另一个（签名不同的）程序创建的钥匙串项目时会弹出访问提示，无人值守运行会因此卡住；因此测试每次使用新的配置，而发布的应用始终使用同一证书签名 |

## 已知限制

- 在开发机上，`AppData\Local` 或 `AppData\Roaming` 下文件夹中创建的 AF_UNIX 套接字文件在监听关闭后会残留为无法访问的条目，
  导致再次绑定失败；`%TEMP%` 与用户文件夹表现正常。因此引擎的套接字放在 `%TEMP%\kyokai`。残留条目
  （`AppData\Local\kyokai` 与测试文件夹）无法在创建它们的会话中删除。
- `ring`（EasyTier、TLS 客户端、`cargo-deny`）在 Windows ARM64 上需要 clang：VS 组件把它装在 `VC\Tools\Llvm\ARM64\bin`，
  但不会加入 `PATH`。
- Windows 安装包没有代码签名；首次启动时 SmartScreen 会警告。
