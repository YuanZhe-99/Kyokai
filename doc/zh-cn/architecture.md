# 架构

## 进程

| 进程 | 可执行文件 | 运行身份 | 职责 |
|---|---|---|---|
| 界面 | `Kyokai`（MyGo） | 当前用户，从不提权 | 窗口、托盘、设置。自身不持有身份，只显示引擎推送的内容。 |
| 引擎 | `kyokai-svc run --host user` | 当前用户，界面的子进程 | 加载 Rust 核心（由它持有设备身份、密码、内嵌的 EasyTier 节点（[networking.md](networking.md)）以及会话两端（[protocol.md](protocol.md)））；提供本地 API。 |
| 控制命令行 | `kyokai-svc ctl …` | 当前用户 | 本地 API 的脚本客户端（开发、冒烟测试）。 |

引擎与界面同生同灭：macOS 上界面持有的生命线管道关闭时引擎停止，Windows 上界面进程消失时引擎停止（`--parent-pid`）；
从托盘退出时也会请求它关闭。同一套接字上的第二个引擎会在接触身份之前拒绝启动。`KYOKAI_PROFILE` 让开发实例拥有自己的
应用名（因而有自己的单实例锁）、套接字与状态目录。

## 运行方式

| 运行方式 | 前提 | 引擎 | 状态 |
|---|---|---|---|
| 免授权 | 无 | 界面的用户级子进程 | 已实现 |
| 随界面 | 安装一次特权助手 | 界面运行期间在助手中 | 助手的安装、服务与授权（Windows）；运行方式本身尚未实现 |
| 后台服务 | 同一个助手 | 开机即运行 | 未实现 |

界面启动时从不请求管理员权限。助手与各运行方式见 [run-modes.md](run-modes.md)。

## 包与 crate

| 包 / crate | 职责 |
|---|---|
| `cmd/kyokai` | 界面入口 |
| `cmd/kyokai-svc` | 引擎入口（`run`、`ctl`、`version`） |
| `internal/gui` | 界面状态、视图、托盘、应用更新、引擎子进程 |
| `internal/engine` | 引擎宿主：生命线、基于核心的本地 API 处理 |
| `internal/localapi` | 基于 AF_UNIX 的带版本 NDJSON 协议、对端凭据授权 |
| `internal/core` | 通过 purego 绑定 Rust 核心 |
| `internal/fsutil` | 原子写入与 JSON 文档 |
| `internal/version` | 唯一的产品版本 |
| `core/kyokai-core` | Rust 核心库：C ABI、机密存储、设备身份、密码与 OPAQUE（`auth`）、访问决定（`access`）、会话（`session`）、EasyTier 节点（`net`）、屏幕采集与编解码器（`video`；Media Foundation 为 `mf`，VideoToolbox 为 `vt`，libvpx 与 libaom 为 `soft`，H.264 与 HEVC 的封装格式为 `annexb`）、被控端的声音（`audio`、`opus`）、输入服务（`input`）、查看窗口的键盘抓取（`keyboard`）、AI 任务（`ai`：服务商、任务运行时及其驱动的桌面） |
| `core/third_party/rustdesk-codec` | 复用的 RustDesk libvpx 与 libaom 编解码器，以及 vcpkg 构建的静态库（[computer-use.md](computer-use.md)） |
| `core/third_party/rustdesk-dxgi` | 复用的 RustDesk DXGI 与 GDI 屏幕采集（[computer-use.md](computer-use.md)） |
| `core/third_party/rustdesk-qos` | 复用的 RustDesk 视频 QoS |
| `core/third_party/thunk-noop` | 替代 EasyTier 的 `thunk-rs` 构建步骤，使 Windows 构建保持静态 C 运行时 |

## FFI

核心是一个 C ABI 库（`kyokai_core.dll`、`libkyokai_core.dylib`），以 `crate-type = ["cdylib", "rlib"]` 构建，通过 purego
加载（`CGO_ENABLED=0`）。导出函数只收发整数与指针：

| 导出 | 作用 |
|---|---|
| `kyokai_abi_version() -> u32` | ABI 版本（5）；不一致时宿主拒绝加载 |
| `kyokai_start(json, len, *out, *out_len) -> i32` | 以 `{version, state_dir, profile}` 启动核心 |
| `kyokai_call(json, len, *out, *out_len) -> i32` | 应答 `{method, params}` |
| `kyokai_free(ptr, len)` | 释放响应缓冲区 |
| `kyokai_stop()` | 丢弃核心的状态，包括内存中的机密 |
| `kyokai_media_read(session, buf, cap, timeout_ms) -> i64` | 等待控制端会话的下一个媒体包，并复制到调用方的缓冲区；-1 超时，-2 已结束，-3 过大 |
| `kyokai_media_write(session, ptr, len) -> i32` | 把界面发来的控制包（输入、关键帧请求）交给会话：0，-1 格式错误，-2 已结束；会话落后时指针移动可能被丢弃（-3），其他包都会等到会话接收 |
| `kyokai_decoder_open(json, len) -> u64` | 为视频流（`{codec, width, height}`）打开查看用的解码器；编解码器不可用时返回 0。不需要已启动的核心 |
| `kyokai_decoder_decode(id, frame, len, out, cap, *w, *h) -> i32` | 把一帧解码到调用方的 RGBA 缓冲区：1 有画面，0 需要更多输入，-1 错误，-3 过大 |
| `kyokai_decoder_info(id, out, cap) -> i32` | 把 `{decoder, hardware}` JSON 写入调用方的缓冲区：它是哪个解码器（`videotoolbox`、`media-foundation`、`libvpx`、`libaom`）以及是否用硬件解码，由解码器自己报告；返回长度，-1 未知句柄，-3 放不下 |
| `kyokai_decoder_close(id)` | 释放解码器 |
| `kyokai_audio_open() -> u64` | 打开被控端声音（Opus）的解码器；无法打开时为 0。不需要已启动的核心 |
| `kyokai_audio_decode(id, packet, len, out, cap) -> i32` | 把一个 Opus 数据包解码到调用方的浮点缓冲区，为 48 kHz 交错立体声：返回写入的帧数，-1 错误，-3 放不下 |
| `kyokai_audio_close(id)` | 释放声音解码器 |
| `kyokai_keyboard_grab(window, mode, len) -> i32` | 以 `map` 或 `translate` 模式为查看窗口（其原生句柄）抓取键盘，或以 `off` 结束抓取：0，-1 未知模式或没有窗口，-2 此处不可用（没有钩子；在 macOS 上，事件监听需要应用的“辅助功能”权限）。不需要已启动的核心 |
| `kyokai_keyboard_read(*event, timeout_ms) -> i32` | 等待抓取到的下一个按键，并写入调用方的 `{window: usize, code: u32, down, caps, num: u8, pad: u8}`：1 有按键，0 超时，-2 不可用 |
| `kyokai_privacy_request(name, len) -> i32` | 在调用进程中向 macOS 请求 `screen`、`accessibility` 或 `input-monitoring`（由应用调用，因此提示属于应用本身）：1 已授权，0 尚未授权，-1 名称未知或本系统没有该权限。无需已启动的核心 |

响应是核心分配的缓冲区中的 `{"ok": true, "result": …}` 或 `{"ok": false, "error": "…"}`；panic 在边界处捕获并作为错误返回。
方法：`status`（从不含机密；包含网络状态）、`password.reveal`、`password.regenerate`、`network.get`（不含密钥的设置）、
`network.set`（`{servers, name, secret?}`；缺少密钥时保留已存的密钥）、`password.set_permanent`（`{password}`，null 表示移除）、
`session.connect`（`{device_id, password, want, remember}`；空密码使用为该被控端保存的密码；立即返回会话 id，进度见状态）、`password.forget_saved`（`{device_id}`）、`session.close`、`request.decide`
（`{id, allow, permissions}`）、`trusted.list`、`trusted.remove`、`host.forget`、`identity.export`（`{passphrase}` → `{document}`）、`identity.import`
（`{document, passphrase}`）、`identity.reset`、`device.set_name`（`{name}`；"" 表示使用电脑的名称；状态中带有 `name` 与本机的 `about`）、`access.settings`（`{password_mode?: temporary|permanent|both, without_asking?}`）、`permission.request`（`{name}`：macOS 上的 `screen`、`accessibility`、`input-monitoring` 或 `keychain` → `{granted}`；`keychain` 会重新打开存储，打开后重建核心；在助手中由代理请求）、`store.start_over`（已保存的存储无法解锁：把它移到一旁，在新存储上重建核心 → `{kept}`）、`agent.restart`（助手的会话代理退出，由其监管者再次启动）、`store.export`（`{passphrase}` → `{document}`）、`store.import`（`{document, passphrase}`）、`store.clear`（切换运行方式时使用，[run-modes.md](run-modes.md)）、`host.serve`（`{serve}`：`all`、`unattended` 或 `none`，由引擎依据运行方式设定）、`capture.show`（`{show}`：采集中是否显示 Kyokai 的窗口，依设置中的“在屏幕采集中隐藏 Kyokai”；macOS 的采集即时跟随；助手会转告其代理；在界面说明之前保持隐藏）、`sound.source`（`{source}`：共享的声音，"" 为本机正在播放的声音，否则为输入设备的 id；正在进行的流随之切换；助手会转告其代理）、`sound.devices`（→ `[{id, name}]`，输入设备；助手模式下为代理的），以及 AI 任务的方法（[computer-use.md](computer-use.md)）：`ai.settings.get`、
`ai.settings.set`（`{sources?, model?: {source, id}, approval?}`；答复按来源附上 `keys`）、`ai.key.set`（`{source, key}`，null 表示移除；密钥从不返回）、
`ai.models.list`（`{source}` → `{models}`，向来源查询）、
`ai.task.start`（`{key, instruction}` → `{id}`；key 使其幂等）、`ai.task.reply`（`{id, text}`）、`ai.task.pause`、`ai.task.resume`、
`ai.task.cancel`（`{id}`）、`ai.task.decide`（`{id, approve}`）、`ai.tasks`（最新的在前）、`ai.draft.set`（`{text}`：下一条指令的共享草稿）、`ai.history.clear`、`ai.stop`（取消所有任务）。
状态中还包含 `permanent_password`、`password_mode` 与 `without_asking`（访问设置）、`ai_draft`（共享草稿）、`permissions`（macOS 上为 `{screen, accessibility, input_monitoring, keychain}`，在助手中为代理的权限；其他系统为 null）、`password_grants`（正确密码无需确认即可授予的权限；用 `access.password_grants` `{grants}` 设置）、`requests`（待处理的被控端确认，各带 `password`：控制端是否给出了密码）、`sessions`、`recent`（连接过的被控端，最新的在前）以及
`ai`（进行中的任务及其状态与来源，或 null）。Go 绑定把传给核心的响应槽放在堆内存中，从不放在
goroutine 栈上，因为栈可能在取地址与调用之间移动。Windows 上以绝对路径、
`LoadLibraryEx(LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR | LOAD_LIBRARY_SEARCH_SYSTEM32)` 加载该库，并静态链接 C 运行时，
因此不会经由搜索路径解析任何文件。

## 本地 API

- 传输：AF_UNIX 套接字（Windows 上也是），位于 `%TEMP%\kyokai\engine.sock` 或 `$TMPDIR/kyokai-<uid>/engine.sock`
  （开发配置为 `engine-<profile>.sock`；`KYOKAI_SOCKET` 可覆盖）；以换行分隔的 JSON，带 `version` 字段（2）。每个连接一个
  请求，`session.attach` 除外：它保持连接，状态变化时收到推送。
- 请求：`status`（含网络状态，以及 `build`：引擎构建所用的提交，用来区分同一版本的两次构建）、`password.reveal`、`password.regenerate`、`network.get`、`network.set`（被拒绝的设置
  以 `invalid` 和原因答复）、`password.set_permanent`、`session.connect`、`password.forget_saved`、`session.close`、`request.decide`、`trusted.list`、
  `trusted.remove`、`host.forget`、`identity.export`、`identity.import`、`identity.reset`、`device.set_name`、`access.settings`、`system.permission`（`{permission}`，macOS）、`capture.show`（`{show}`；界面在连接引擎时以及设置改变时发送）、`sound.source`（`{sound_source}`，同样）、`sound.devices`（→ `sound_devices`）、`secrets.start_over`、`helper.background`、`helper.agent_restart` 与 `helper.remove`（仅助手；`helper.remove` 用于 macOS，在存储迁出之后）、`store.export`、`store.import`、`store.clear`（之后停止引擎）、`session.attach`、`session.media`、`shutdown`，以及与核心方法同名的 AI 请求（`ai.settings.get` … `ai.stop`、`ai.draft.set`）。`session.media`（某个进行中的控制端会话的 `{id}`）先答复 OK，
  之后双向传输带长度前缀的包：引擎发出视频、统计、被控端的编码清单与其 AI 任务，界面发出控制与 AI 消息；界面首先请求关键帧。状态包含待处理的被控端确认与会话；机密只朝引擎方向传递
  （网络密钥、固定密码、用户输入的密码、服务商的 API 密钥）。会话在每个改变状态的命令之后、以及网络状态变化时收到状态。
- 授权来自操作系统的对端凭据（macOS 为 `LOCAL_PEERCRED`；Windows 为 `SIO_AF_UNIX_GETPEERPID` 与进程令牌的 SID），
  从不取自请求内容：只服务引擎自己的用户；读不出身份的连接一律拒绝。

## 状态

| 路径 | 内容 |
|---|---|
| `%APPDATA%\Kyokai\` / `~/Library/Application Support/Kyokai/` | 状态目录（开发配置为 `Kyokai-<profile>`） |
| `secrets.bin` | 加密的机密存储（[security.md](security.md)） |
| `master.key`（Windows） | 存储密钥的 DPAPI 数据块；macOS 上密钥在登录钥匙串中 |
| `gui.json` | 界面偏好设置 |
| `ai.json` | AI 设置：服务商、模型、批准方式（不含密钥） |
| `ai-tasks.json` | 已结束的 AI 任务，不含截图 |
| `logs/engine.log` | 引擎日志，不含机密，以及引擎的崩溃跟踪 |
| `logs/gui-crash.log` | 界面的崩溃跟踪 |

## 通用规则

- **机密从不外传**（[security.md](security.md)）。
- **绝不把陈旧数据显示为当前数据。** 只有引擎会话连接时界面才显示引擎状态，否则显示“正在启动”或引擎未运行的原因。
- **带版本的边界。** ABI 与本地 API 都带版本；枚举以名称传递。
