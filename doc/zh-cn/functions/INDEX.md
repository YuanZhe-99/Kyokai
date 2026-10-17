# 函数索引

每个 Go 包与 Rust crate 一行：职责，以及应该先读的声明。声明在文档注释中带有函数说明层（第一句写用途；非平凡的声明再写
输入、返回、副作用与注意事项）。

| 包 / crate | 职责 | 从这里读起 |
|---|---|---|
| `cmd/kyokai` | 界面入口 | `main` |
| `cmd/kyokai-svc` | 引擎入口 | `run`、`ctl`、`helperCmd`、`runHelper`、`asService`ã`agentCmd` |
| `internal/gui` | 界面状态、视图（含 AI 页面）、托盘、更新、引擎子进程 | `App`、`App.View`、`App.connectLoop`、`engineChild.ensure`、`App.aiView`、`describeAction`、`viewer.aiPanel`ã`deviceAvatar`ã`deviceLogoName`ã`App.runModeCard`ã`App.switchMode`ã`runPrivileged` |
| `internal/engine` | 引擎宿主 | `Run`、`Options`、`DefaultStateDir` |
| `internal/localapi` | 本地 API 协议、服务端、客户端、授权 | `ProtocolVersion`、`Request`、`Status`、`AISettings`、`AITask`、`NetworkSettings`、`NetworkStatus`、`SessionInfo`、`HostRequest`、`Client.Attach`、`Authorizer.Allows`ã`MoveStore` |
| `internal/core` | 通过 purego 绑定 Rust 核心 | `Load`、`Lib.Start`、`Lib.Call` |
| `internal/version` | 产品版本 | `Version`、`Older` |
| `internal/helper` | 安装、移除与查看特权助手（Windows 服务；macOS 稍后） | `Install`、`Uninstall`、`Inspect`、`ReadSettings`、`GroupSID`、`SocketPath`、`SelfUpdate`、`Retire`、`Settings.AppGone` |
| `internal/fsutil` | 原子写入与 JSON 文档 | `WriteFileAtomic`、`WriteJSON`、`ReadJSON` |
| `core/kyokai-core` | Rust 核心：C ABI、机密、身份、临时密码、EasyTier 节点 | `kyokai_start`、`kyokai_call`、`store::open`、`Identity::load_or_create`、`device_id`、`net::Network`、`session::serve`、`session::connect`、`session::HostAi`ã`about::Local`ã`about::About::clean`ã`agent::link`ã`agent::serve`ã`agent::start_helper`ã`agent::run_agent`、`access::Access`、`auth::Slots`、`backup::export`、`backup::import`、`media::Reassembler`、`media::Queue`、`kyokai_media_read`、`video::Source`、`mf::Encoder`、`mf::Decoder`、`mf::Codec`、`mf::decodable`、`decode::open`、`input::Event`、`input::Injector`、`input::LocalWatch`、`clipboard::share`、`clipboard::Watch`、`ai::Ai`、`ai::action::validate`、`ai::desktop::plan` |
| `core/third_party/rustdesk-dxgi` | 复用的 RustDesk Windows 屏幕采集 | `Displays`、`Capturer::frame` |
| `core/third_party/rustdesk-qos` | 复用的 RustDesk 视频 QoS | `VideoQoS`、`base_bitrate` |
| `core/third_party/thunk-noop` | EasyTier `thunk-rs` 的空操作替代 | `thunk` |
| `tools/deps` | 上游版本固定 | `check`、`update` |
| `tools/checkdocs` | 文档镜像检查 | `main` |
| `tools/sign` | 特权助手 bundle 签名密钥与 bundle 签名 | `keygen`、`bundle` |
| `internal/bundlesig` | 为助手的文件签名并验证（只读取一次） | `Sign`、`Read`、`PublicKey` |
| `tools/icon` | 绘制应用图标与托盘图形 | `app`、`tray`、`render` |
