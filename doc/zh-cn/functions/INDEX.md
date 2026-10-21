# 函数索引

每个 Go 包与 Rust crate 一行：职责，以及应该先读的声明。声明在文档注释中带有函数说明层（第一句写用途；非平凡的声明再写
输入、返回、副作用与注意事项）。

| 包 / crate | 职责 | 从这里读起 |
|---|---|---|
| `cmd/kyokai` | 界面入口 | `main` |
| `cmd/kyokai-svc` | 引擎入口 | `run`、`ctl`、`helperCmd`、`runHelper`、`asService`、`agentCmd`、`ctlKeys` |
| `internal/gui` | 界面状态、视图（主页、网络、AI、权限、安全、通用、关于）、连接标签页、托盘、更新、引擎子进程 | `App`、`App.View`、`App.connectLoop`、`App.statusPushed`、`App.watchMain`、`engineChild.ensure`、`App.aiView`、`describeAction`、`viewer.aiPanel`、`App.connectDevice`、`App.connectAddress`、`parseTarget`、`deviceRows`、`Prefs.named`、`App.setAlias`、`App.renameCard`、`App.removeDevice`、`App.byAddressSettings`、`App.setNoLogs`、`App.protected`、`App.protectedSetting`、`App.protectedCard`、`App.syncViewers`、`viewerWindow`、`App.moveTab`、`App.devicesCard`、`deviceAvatar`、`deviceLogoName`、`App.runModeCard`、`App.permissionsView`、`App.securityView`、`App.generalView`、`App.aboutView`、`App.connectionsCard`、`App.networksCard`、`App.networkEntry`、`App.networkForm`、`App.changeNetworks`、`viewer.actionBar`、`TitleBarOptions`、`viewerWindow.tabBar`、`App.syncFront`、`viewer.clipPanel`、`viewer.originalPicture`、`App.permissionsCard`、`App.hostBanner`、`App.relaunch`、`App.startOver`、`App.switchMode`、`runPrivileged`、`App.aiProviderCard`、`aiTemplates`、`friendlyModelName` |
| `internal/engine` | 引擎宿主 | `Run`、`Options`、`DefaultStateDir`、`Logger`、`CrashLog`、`SetLogsOff`、`LogsOff`、`protectedAllows`、`handler.services` |
| `internal/localapi` | 本地 API 协议、服务端、客户端、授权 | `ProtocolVersion`、`Request`、`Status`、`ByAddress`、`AISettings`、`AITask`、`NetworkSettings`、`NetworkProfiles`、`NetworkChange`、`NetworkStatus`、`NetworkState`、`SessionInfo`、`HostRequest`、`Client.Attach`、`Authorizer.Allows`、`Authorizer.Owner`、`IsOwner`、`MoveStore` |
| `internal/core` | 通过 purego 绑定 Rust 核心 | `Load`、`Lib.Start`、`Lib.Call` |
| `internal/version` | 产品版本与构建 | `Version`、`Older`、`Build` |
| `internal/helper` | 安装、移除与查看特权助手（Windows 服务；macOS 的 LaunchDaemon 与 LaunchAgent） | `Install`、`Uninstall`、`Installed`、`Inspect`、`ReadSettings`、`GroupSID`、`GroupGID`、`SocketPath`、`AgentSocket`、`SelfUpdate`、`Retire`、`Settings.AppGone` |
| `internal/fsutil` | 原子写入与 JSON 文档 | `WriteFileAtomic`、`WriteJSON`、`ReadJSON` |
| `core/kyokai-core` | Rust 核心：C ABI、机密、身份、临时密码、EasyTier 节点（每个已加入的网络一个） | `kyokai_start`、`kyokai_call`、`store::open`、`Identity::load_or_create`、`device_id`、`net::Network`、`net::Profile`、`Network::set_profile`、`Network::status`、`net::aggregate`、`net::order`、`net::patched`、`Network::connect`、`Network::accept_by_address`、`net::split_address`、`net::identify`、`session::serve`、`session::connect`、`session::identify`、`Sessions::remove_host`、`access::ByAddress`、`access::Source`、`Access::keep_history`、`Access::set_protected`、`Slots::check_permanent`、`session::HostAi`、`about::Local`、`about::About::clean`、`agent::link`、`agent::serve`、`agent::start_helper`、`agent::run_agent_socket`、`agent::run_agent`、`ai::Protocol`、`ai::Source`、`Ai::list_models`、`chat::Conversation`、`access::Access`、`Access::admits_by_password`、`access::PasswordMode`、`Ai::set_draft`、`video::Grab`、`mac::Capture`、`mac::Injector`、`mac::LocalWatch`、`mac::permissions`、`store::set_aside`、`auth::Slots`、`backup::export`、`backup::import`、`media::Reassembler`、`media::Queue`、`kyokai_media_read`、`video::Source`、`video::choose`、`video::prefer`、`video::offer`、`video::encodable`、`video::decodable`、`video::hevc_hardware`、`video::bgra_to_nv12_planes`、`mf::Encoder`、`mf::Decoder`、`mf::Codec`、`mf::decodable`、`mf::encodable`、`mf::hardware_free`、`vt::Encoder`、`vt::Decoder`、`vt::Codec`、`annexb::units`、`annexb::Syntax`、`soft::Encoder`、`soft::Decoder`、`soft::Codec`、`decode::open`、`audio::Source`、`audio::Pcm`、`audio::set_source`、`audio::devices`、`opus::Encoder`、`opus::Decoder`、`mac::AudioCapture`、`kyokai_audio_decode`、`decode::info`、`kyokai_decoder_info`、`media::last_chunk`、`input::Event`、`input::Injector`、`input::LocalWatch`、`privacy::hold`、`power::keep_awake`、`power::Suspend`、`clipboard::share`、`clipboard::Watch`、`session::ClipUse`、`files::Transfers::halt`、`ai::Ai`、`ai::action::validate`、`ai::desktop::plan`、`keyboard::grab`、`keyboard::Grab::route`、`keyboard::scan_code`、`mac::keycode_scan`、`input::Event::mods_held`、`input::scan_input`、`kyokai_keyboard_grab`、`files::list`、`files::check`、`files::Transfers`、`keys::proofs`、`keys::verify`、`keys::sign_ssh`、`keys::add_login`、`keys::add_authorized`、`Access::saved`、`Access::remember_key`、`Access::forget_saved`、`net::keep_resuming` |
| `core/third_party/rustdesk-codec` | 复用的 RustDesk libvpx（VP8、VP9）与 libaom（AV1）封装；链接静态库 | `VpxEncoder`、`VpxDecoder`、`AomEncoder`、`AomDecoder`、`EncodeYuvFormat` |
| `core/third_party/rustdesk-dxgi` | 复用的 RustDesk Windows 屏幕采集，加上 Kyokai 的 GPU 缩放 | `Displays`、`Capturer::frame`、`Capturer::scale_to`、`scale::Scaler` |
| `core/third_party/rustdesk-qos` | 复用的 RustDesk 视频 QoS | `VideoQoS`、`base_bitrate` |
| `core/third_party/thunk-noop` | EasyTier `thunk-rs` 的空操作替代 | `thunk` |
| `tools/deps` | 上游版本固定 | `check`、`update` |
| `tools/checkdocs` | 文档镜像检查 | `main` |
| `tools/sign` | 特权助手 bundle 签名密钥与 bundle 签名 | `keygen`、`bundle` |
| `internal/bundlesig` | 为助手的文件签名并验证（只读取一次） | `Sign`、`Read`、`PublicKey` |
| `tools/icon` | 绘制应用图标与托盘图形 | `app`、`tray`、`render` |
