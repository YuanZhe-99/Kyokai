# 架构

## 进程

| 进程 | 可执行文件 | 运行身份 | 职责 |
|---|---|---|---|
| 界面 | `Kyokai`（MyGo） | 当前用户，从不提权 | 窗口、托盘、设置。自身不持有身份，只显示引擎推送的内容。 |
| 引擎 | `kyokai-svc run --host user` | 当前用户，界面的子进程 | 加载 Rust 核心（由它持有设备身份、密码、内嵌的 EasyTier 节点（[networking.md](networking.md)）以及会话两端（[protocol.md](protocol.md)））；提供本地 API。 |
| 命令行 | `kyokai …`（也可用 `kyokai-svc ctl …`） | 当前用户 | 面向人与脚本的本地 API 客户端（[cli.md](cli.md)）。 |

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
| `cmd/kyokai-svc` | 引擎入口（`run`、`ctl`、`helper`、`agent`、`version`） |
| `cmd/kyokai-cli` | 控制台程序 `kyokai` |
| `internal/ctl` | 命令行（[cli.md](cli.md)） |
| `internal/gui` | 界面状态、视图、托盘、应用更新、引擎子进程 |
| `internal/engine` | 引擎宿主：生命线、基于核心的本地 API 处理 |
| `internal/localapi` | 基于 AF_UNIX 的带版本 NDJSON 协议、对端凭据授权 |
| `internal/core` | 通过 purego 绑定 Rust 核心 |
| `internal/fsutil` | 原子写入与 JSON 文档 |
| `internal/version` | 唯一的产品版本 |
| `core/kyokai-core` | Rust 核心库：C ABI、机密存储、设备身份、密码与 OPAQUE（`auth`）、访问决定（`access`）、会话（`session`）、EasyTier 节点（`net`）、屏幕采集与编解码器（`video`；Media Foundation 为 `mf`，VideoToolbox 为 `vt`，VA-API 的 H.264 与 HEVC 为 `va`，系统的 FFmpeg 为 `ff`，libvpx 与 libaom 为 `soft`，H.264 与 HEVC 的封装格式为 `annexb`，查看窗口把解码后的画面转换进界面缓冲区为 `convert`）、被控端的声音（`audio`、`opus`）、输入服务（`input`）、查看窗口的键盘抓取（`keyboard`）、NanoKVM USB（`kvm`：它的采集卡、经串口的键盘和鼠标）、AI 任务（`ai`：服务商、任务运行时及其驱动的桌面）、登录密钥（`keys`）、通往被控端自身服务的隧道（`tunnel`）、被控端的状态（`system`）及其查看者（`watch`） |
| `core/third_party/rustdesk-codec` | 复用的 RustDesk libvpx 与 libaom 编解码器，以及 vcpkg 构建的静态库（[computer-use.md](computer-use.md)） |
| `core/third_party/rustdesk-dxgi` | 复用的 RustDesk DXGI 与 GDI 屏幕采集（[computer-use.md](computer-use.md)） |
| `core/third_party/rustdesk-qos` | 复用的 RustDesk 视频 QoS |
| `core/third_party/thunk-noop` | 替代 EasyTier 的 `thunk-rs` 构建步骤，使 Windows 构建保持静态 C 运行时 |
| `core/third_party/smoltcp` | 内置的 smoltcp 0.14.0（EasyTier 的进程内 TCP/IP 协议栈），重传超时的下限改为 200 毫秒（[networking.md](networking.md)） |
| `sidecar` | `kyokai-agent`，AI 任务在 Pi 上的循环（TypeScript，用 Bun 构建）：只追加的各服务商对话（`conversation.ts`）与引擎的协议（[agent sidecar](#agent-sidecar)） |

## FFI

核心是一个 C ABI 库（`kyokai_core.dll`、`libkyokai_core.dylib`），以 `crate-type = ["cdylib", "rlib"]` 构建，通过 purego
加载（`CGO_ENABLED=0`）。导出函数只收发整数与指针：

| 导出 | 作用 |
|---|---|
| `kyokai_abi_version() -> u32` | ABI 版本（8）；不一致时宿主拒绝加载 |
| `kyokai_start(json, len, *out, *out_len) -> i32` | 以 `{version, state_dir, profile}` 启动核心 |
| `kyokai_call(json, len, *out, *out_len) -> i32` | 应答 `{method, params}`；调用可并发进行（Go 绑定为每个调用提供自己的响应槽；`kyokai_stop` 会等待它们）。耗时的工作——各网络的视图（每 2 秒在后台刷新；超过 10 秒的视图显示为正在连接）、导出及其密钥派生、固定密码、`protect.set`、`keys.sign`、macOS 助手的权限、钥匙串提示——都不持有核心的锁，只在读取与替换时才取锁；重建核心仍持有它 |
| `kyokai_free(ptr, len)` | 释放响应缓冲区 |
| `kyokai_stop()` | 结束所有会话（已连接的会话各发送告别，最多等待一秒），然后丢弃核心的状态，包括内存中的机密 |
| `kyokai_media_read(session, buf, cap, timeout_ms) -> i64` | 等待控制端会话的下一个媒体包（它与 `kyokai_media_write` 查找会话时不取核心的锁，因此任何调用都不会拖慢视频、输入或隧道）（隧道会话为其字节，包 14），并复制到调用方的缓冲区；-1 超时，-2 已结束，-3 过大 |
| `kyokai_media_write(session, ptr, len) -> i32` | 把界面发来的控制包（输入、关键帧请求）交给会话：0，-1 格式错误，-2 已结束，-3 因落后而被拒绝；输入从不等待——会话无法接收输入时，移动、按下、点击与文字被拒绝，松开从不被拒绝（`session::InputGate`）——其他消息最多等待 200 毫秒；隧道的字节（包 14）总是等待空间 |
| `kyokai_decoder_open(json, len) -> u64` | 为视频流（`{codec, width, height}`）打开查看用的解码器；编解码器不可用时返回 0。不需要已启动的核心 |
| `kyokai_decoder_decode(id, frame, len, want, out, cap, *w, *h) -> i32` | 解码一帧；完成的画面按 `want` 直接转换进调用方的缓冲区（借用，从开头写入）：0 原尺寸 RGBA，1 一半尺寸 RGBA（每边向上取整），2 不转换（后面还有更新的帧在等待；不写入任何内容）。1 有画面，其完整尺寸写入 `*w`、`*h`；0 需要更多输入；-1 错误或未知的 `want`；-3 按所要的方式放不进 `cap` |
| `kyokai_decoder_info(id, out, cap) -> i32` | 把 `{decoder, hardware}` JSON 写入调用方的缓冲区：它是哪个解码器（`videotoolbox`、`media-foundation`、`libvpx`、`libaom`）以及是否用硬件解码，由解码器自己报告；返回长度，-1 未知句柄，-3 放不下 |
| `kyokai_decoder_close(id)` | 释放解码器 |
| `kyokai_audio_open() -> u64` | 打开被控端声音（Opus）的解码器；无法打开时为 0。不需要已启动的核心 |
| `kyokai_audio_decode(id, packet, len, out, cap) -> i32` | 把一个 Opus 数据包解码到调用方的浮点缓冲区，为 48 kHz 交错立体声：返回写入的帧数，-1 错误，-3 放不下 |
| `kyokai_audio_close(id)` | 释放声音解码器 |
| `kyokai_keyboard_grab(window, mode, len) -> i32` | 以 `map` 或 `translate` 模式为查看窗口（其原生句柄）抓取键盘，或以 `off` 结束抓取；再次请求抓取即重新持有它（macOS 的事件监听被重新打开或重新创建，Windows 的钩子被重新安装）：0，-1 未知模式或没有窗口，-2 此处不可用（没有钩子；在 macOS 上，事件监听需要应用的“辅助功能”权限）。不需要已启动的核心 |
| `kyokai_keyboard_read(*event, timeout_ms) -> i32` | 等待抓取到的下一个按键，并写入调用方的 `{window: usize, code: u32, down, caps, num: u8, pad: u8}`：1 有按键，0 超时，-2 不可用 |
| `kyokai_keyboard_state(window, *out, cap) -> i32` | 查看窗口的抓取状态，以 `{"state", "holder"}` JSON 写入调用方的缓冲区：`taking`、`none`、`off`、`secure-input`、`other-window`、`no-reader` 或 `behind`，已知时 `holder` 给出持有 Secure Event Input 或位于前台的程序；返回长度，-1 `out` 为空，-2 不可用，-3 缓冲区太小。不需要已启动的核心 |
| `kyokai_privacy_request(name, len) -> i32` | 在调用进程中向 macOS 请求 `screen`、`accessibility` 或 `input-monitoring`（由应用调用，因此提示属于应用本身）：1 已授权，0 尚未授权，-1 名称未知或本系统没有该权限。无需已启动的核心 |

响应是核心分配的缓冲区中的 `{"ok": true, "result": …}` 或 `{"ok": false, "error": "…"}`；panic 在边界处捕获并作为错误返回。
方法：`status`（从不含机密；包含网络状态）、`password.reveal`、`password.regenerate`、`network.get`（第一个网络不含密钥的设置，供旧版界面和 `ctl` 使用）、
`network.set`（`{servers, name, secret?, direct?}`：第一个网络；缺少密钥时保留已存的密钥，空密钥则清除，没有服务器也没有名称时移除它）、`network.profiles`（→ `{direct, profiles}`：保存的每个网络，
`{id, label, servers, name, has_secret, weak_secret, joined}`）、`network.profile.set`（`{id?, label?, servers?, name?, secret?, joined?}`：没有 id 时添加一个网络，
除非另有说明否则加入；有 id 时未给出的字段保留原值，密钥也一样；答复所有网络）、`network.profile.remove`（`{id}`）、`network.direct`
（`{direct}`：在每个网络中尝试直接路径，[networking.md](networking.md)）、`password.set_permanent`（`{password}`，null 表示移除）、
`session.connect`（`{device_id, password, want, remember, address?, route?, nanokvm?}`；空密码使用为该被控端保存的密码；`nanokvm` 表示请求经其 NanoKVM USB 有线连接的电脑，此时 `want` 只有 `use-nanokvm`——对本机自己的设备 ID 则是本机自己的 NanoKVM，在本引擎中打开，`take_over` 会结束控制端对它的会话，否则答复 `nanokvm-in-use`；`address` 表示只拨打该 IP 地址或域名，并把它加入该被控端的路径，`route`（`auto`、`network` 或它的某个地址）覆盖为它选定的路径（[networking.md](networking.md)，“路径”）；立即返回会话 id，进度见状态）、`session.identify`（`{address}` → `{device_id}` 或 `{failure}`：`unreachable`、`incompatible`、`broken` 或 `self`；拨号时不持有核心的锁）、`device.remove`（`{device_id}`：结束本机与该被控端的会话，并把它们移出列表，忘记其固定的密钥与为它保存的一切）、`protect.set`（`{on, password?}`：保护模式，设置了固定密码时才能打开，用它关闭，[identity-and-access.md](identity-and-access.md)）、`access.history`（`{keep}`：连接记录保存在机密存储中，或在完全无日志时只保存在内存中，[security.md](security.md)）、`password.forget_saved`（`{device_id}`：它的密码与为它保存的其他一切）、`session.close`、`request.decide`
（`{id, allow, permissions}`）、`trusted.list`、`trusted.remove`、`host.forget`、登录密钥（[identity-and-access.md](identity-and-access.md)，“密钥”）：`keys.list`（只有公开部分）、`keys.add`（`{name, private}`）、`keys.remove`（`{id}`）、`keys.sign`（`{key_id, algorithm, data}` → `{signature}`）、`authorized.list`、`authorized.add`（`{name, public}`）、`authorized.set`（`{id, off}`：关闭或打开一个密钥，并保留它）、`authorized.remove`（`{id}`）、隧道与保存的服务登录信息（[remote-services.md](remote-services.md)）：`tunnel.open`（`{device_id, address?, route?, service}` → `{id}`）、`tunnel.wait`（`{id, timeout_ms}` → `{state, failure}`，等待时不持有核心的锁）、`credentials.get`、`credentials.set`、`credentials.forget`、路径（[networking.md](networking.md)，“路径”）：`routes.set`（`{device_id, add?, remove?, prefer?}`）、`routes.probe`（`{devices}`：在后台探测它们的地址，最多 16 台）、`watch.set`（`{devices, slow?}`：在 30 秒内查看这些被控端的状态，`slow` 为其中以慢速查看的，[computer-use.md](computer-use.md)，“被控端状态”）、`identity.export`（`{passphrase}` → `{document}`）、`identity.import`
（`{document, passphrase}`）、`identity.reset`、`device.set_name`（`{name?, publish?}`：二者之一或都有；"" 表示使用电脑的名称，`publish` 让它在登录之前就显示在网络中；状态中带有 `name`、`publish_name` 与本机的 `about`）、`access.settings`（`{password_mode?: temporary|permanent|both|none, without_asking?, by_address?: {on, ranges}, service_ports?: {ssh?, rdp?, vnc?}, nanokvm?: {on, video, serial}}`；`by_address` 立即启动或停止监听；`service_ports` 是隧道到达的回环端口，`nanokvm` 是提供的 NanoKVM USB，[remote-services.md](remote-services.md)）、`nanokvm.devices`（→ `{video, serial, suggested}`：本机的采集卡与 USB 串口，以及看起来属于同一台 NanoKVM USB 的那一对）、`permission.request`（`{name}`：macOS 上的 `screen`、`accessibility`、`input-monitoring` 或 `keychain` → `{granted}`；`keychain` 会重新打开存储，打开后重建核心；在助手中由代理请求）、`store.start_over`（已保存的存储无法解锁：把它移到一旁，在新存储上重建核心 → `{kept}`）、`agent.restart`（助手的会话代理退出，由其监管者再次启动）、`store.export`（`{passphrase}` → `{document}`）、`store.import`（`{document, passphrase, replace?}`：整个存储，版本 2；带有历史记录的另一个身份只有在 `replace` 时才会被替换）、`store.clear`（`{to}`：切换运行方式时使用；留下 `moved.json`，[run-modes.md](run-modes.md)）、`sessions.close_controllers`（结束本引擎的控制端会话）、`host.serve`（`{serve}`：`all`、`unattended` 或 `none`，由引擎依据运行方式设定）、`capture.show`（`{show}`：采集中是否显示 Kyokai 的窗口，依“安全”中的“在屏幕采集中隐藏 Kyokai”；macOS 的采集即时跟随；助手会转告其代理；在界面说明之前保持隐藏）、`input.kyokai`（`{allow}`：人的远程输入能否到达 Kyokai 的窗口，依同一个开关，界面与 `capture.show` 一起发送；每个会话按事件判断，AI 永远不能，有确认或批准在等待时也不能，并随事件告知助手的代理；在界面说明之前拒绝；旧引擎会拒绝这个请求并继续保护）、`kyokai.shown`（`{shown}`：Kyokai 的某个窗口显示在 Wayland 桌面上，而 Wayland 既不能把它排除在画面之外，也不能让输入避开它，因此在它显示期间画面保持不动，除非关闭了“对采集隐藏 Kyokai”，否则不注入任何远程或 AI 输入；其他系统忽略）、`sound.source`（`{source}`：共享的声音，"" 为本机正在播放的声音，否则为输入设备的 id；正在进行的流随之切换；助手会转告其代理）、`sound.devices`（→ `[{id, name}]`，输入设备；助手模式下为代理的），以及 AI 任务的方法（[computer-use.md](computer-use.md)）：`ai.settings.get`、
`ai.settings.set`（`{sources?, model?: {source, id}, approval?}`；答复按来源附上 `keys`）、`ai.key.set`（`{source, key}`，null 表示移除；密钥从不返回）、
`ai.models.list`（`{source}` → `{models}`，向来源查询）、`ai.pane.open`（`{id, name, kind}`）、`ai.pane.next`（`{id}` → `{seq, request}`，或一秒后为 null，不持有核心的锁）、`ai.pane.answer`（`{id, seq, answer}`）、`ai.pane.local`（`{id, moved}`）、`ai.pane.close`（`{id}`）、`ai.scope.set`（`{apps}`，null 表示整个桌面；答复为带 `scope` 的设置）、`ai.folders.set`（`{folders}`，完整路径，null 表示所有文件夹；答复为带 `folders` 的设置）、`ai.apps`（→ 当前在屏幕上有窗口的程序）、
`ai.task.start`（`{key, instruction, choice?: {model?, thinking?}}` → `{id}`；key 使其幂等）、`ai.task.reply`（`{id, text, choice?}`）、`ai.task.pause`、`ai.task.resume`、
`ai.task.cancel`（`{id}`）、`ai.task.decide`（`{id, approve}`）、`ai.task.rename`（`{id, name}`）、`ai.task.fork`（`{id, run?}` → `{id}`）、
`ai.task.compact`（`{id}`）、`ai.task.export`（`{id}` → `{markdown}`）、`ai.tasks`（最新的在前）、`ai.draft.set`（`{text}`：下一条指令的共享草稿）、`ai.history.clear`、`ai.stop`（取消所有任务）。
状态中还包含 `permanent_password`、`password_mode` 与 `without_asking`（访问设置）、`service_ports`（实际使用的 `{ssh, rdp, vnc}`）、`nanokvm`（`{on, video, serial}`）、`by_address`（`{on, ranges, port, error}`：地址直接连接，[networking.md](networking.md)）、`keeps_history`、`protected`、`ai_draft`（共享草稿）、`permissions`（macOS 上为 `{screen, accessibility, input_monitoring, keychain}`，在助手中为代理的权限；其他系统为 null）、`password_grants`（正确密码无需确认即可授予的权限；用 `access.password_grants` `{grants}` 设置）、`requests`（待处理的被控端确认，各带 `password`：控制端是否给出了密码或密钥，以及 `key`：该密钥的名称）、`sessions`（隧道的带有其 `service`；控制端的带有其 `route`）、`recent`（连接过的被控端，最新的在前，各带其 `routes`（`{address, added, last_ok, rtt_ms, kbps, fails, probe}`）、`network_route`、`prefer`（手动选择的路径）、被控端声明的 `features`，以及 `saved`：为它保存的每种登录方式一个标志）、`watching` 与 `watchers`（本机发起的与针对本机的状态查看）以及
`ai`（进行中的任务及其状态与来源，或 null）。Go 绑定把传给核心的响应槽放在堆内存中，从不放在
goroutine 栈上，因为栈可能在取地址与调用之间移动。Windows 上以绝对路径、
`LoadLibraryEx(LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR | LOAD_LIBRARY_SEARCH_SYSTEM32)` 加载该库，并静态链接 C 运行时，
因此不会经由搜索路径解析任何文件。

## 本地 API

- 传输：AF_UNIX 套接字（Windows 上也是），位于 `%TEMP%\kyokai\engine.sock`、`$TMPDIR/kyokai-<uid>/engine.sock`（macOS）
  或 `$XDG_RUNTIME_DIR/kyokai/engine.sock`（Linux；未设置时为用户的缓存目录，绝不用共享的 `/tmp`）（开发配置为 `engine-<profile>.sock`；`KYOKAI_SOCKET` 可覆盖）；以换行分隔的 JSON，带 `version` 字段（2）。每个连接一个
  请求，`session.attach` 除外：它保持连接，状态变化时收到推送。
- 请求：`status`（含网络状态，以及 `build`：引擎构建所用的提交，用来区分同一版本的两次构建）、`password.reveal`、`password.regenerate`、`network.get`、`network.set`、`network.profiles`、`network.profile.set`、`network.profile.remove`、`network.direct`（被拒绝的设置
  以 `invalid` 和原因答复；状态中的 `network` 在所有网络合在一起的状态上增加 `networks`，即每个网络各自的状态，因此旧版界面照常读取）、`password.set_permanent`、`session.connect`、`session.identify`、`device.remove`、`protect.set`（保护模式打开期间，引擎只答复 `protectedAllows` 中的请求，其余以 `invalid` 拒绝；状态中为 `protected`）、`logs.set`（`{logs_off}`：完全无日志；引擎写入或删除其状态目录中的 `no-logs` 标记，关闭或重新打开它的日志，删除日志并调用 `access.history`；状态中为 `logs_off`；界面在连接引擎时以及设置改变时发送）、`password.forget_saved`、`session.close`、`request.decide`、`trusted.list`、
  `trusted.remove`、`host.forget`、`keys.list`、`keys.add`、`keys.remove`、`keys.sign`、`authorized.list`、`authorized.add`、`authorized.set`、`authorized.remove`、`credentials.get`（仅所有者或操作者）、`credentials.set`、`credentials.forget`、`watch.set`、`tunnel.open`（流式，见下文）、`identity.export`、`identity.import`、`identity.reset`、`device.set_name`、`access.settings`、`system.permission`（`{permission}`，macOS）、`capture.show`（`{show}`；界面在所连接的引擎报告核心已运行（未驻留）后发送，未送达则每秒重发，设置改变时也发送）、`kyokai.shown`（`{shown}`；Wayland 上的 Linux）、`input.kyokai`（`{kyokai_input}`；与 `capture.show` 一起发送；不认识它的引擎会继续保护 Kyokai 的窗口）、`sound.source`（`{sound_source}`，同样）、`sound.devices`（→ `sound_devices`）、`nanokvm.devices`（→ `nanokvm_devices`）、`clipboard.mine`、`secrets.start_over`、`helper.background`、`helper.agent_restart` 与 `helper.remove`（仅助手；在存储迁出之后）、`store.forget_move`（驻留的引擎重新开始）、`sessions.close_controllers`、`store.export`、`store.import`、`store.clear`（之后停止引擎）、`session.attach`、`session.media`、`shutdown`，以及与核心方法同名的 AI 请求（`ai.settings.get` … `ai.stop`、`ai.draft.set`）。`session.media`（某个进行中的控制端会话的 `{id}`）先答复 OK，
  之后双向传输带长度前缀的包：引擎发出视频、统计、被控端的编码清单、其 AI 任务、经过剪贴板的内容（包 13）以及输入是否暂停（包 15，`{paused}`），界面发出控制与 AI 消息，其中的 `clipboard-use` 由引擎为该会话保存，从不转发；界面首先请求关键帧。`tunnel.open`（`{tunnel: {device_id, address?, route?, service}}`）在被控端准入隧道之后（最多等待 90 秒）答复，或说明原因，之后双向以包 14 承载隧道的字节（[remote-services.md](remote-services.md)）。`ai.pane.serve`（`{pane: {id, name, kind}}`，仅限所有者）把界面的一个窗格借给 AI（[computer-use.md](computer-use.md#窗格中的-ai)）：它答复 OK，之后双向承载 JSON 值——引擎的请求 `{seq, request}`，以及界面的答复 `{seq, answer}` 或被控端用户在窗格上的输入 `{local}`——转交给核心的 `ai.pane.*`，直到任一方关闭连接。这些新增内容不改变任何请求的含义，因此本地 API 仍为版本 2：旧引擎会忽略 `clipboard-use`，旧界面会忽略包 13 与包 15。状态包含待处理的被控端确认与会话；机密只朝引擎方向传递
  （网络密钥、固定密码、用户输入的密码、服务商的 API 密钥）。会话在每个改变状态的命令之后、以及网络状态变化时收到状态：每个引擎由一个生产者生成状态，在变化后 100 毫秒、
  以及有会话连接时每 500 毫秒生成一次，把最新的交给每个会话，从不等待读得慢的一方。`clipboard.mine`（`{digest}`，界面即将复制的文字的 SHA-256，
  64 个小写十六进制字符）告诉核心不要把这段文字提供给控制端；所有级别与受保护模式都可以发送它。
- 授权来自操作系统的对端凭据（macOS 为 `LOCAL_PEERCRED`；Linux 为 `SO_PEERCRED` 与 `SO_PEERGROUPS`；Windows 为 `SIO_AF_UNIX_GETPEERPID` 与进程令牌的 SID），
  从不取自请求内容：只服务引擎自己的用户；读不出身份的连接一律拒绝。服务器在请求的上下文中标记每个连接的级别
  （`localapi.IsOwner`）：所有者或助手的操作者还可以用引擎的密钥签名（`keys.sign`）并读取保存的服务密码（`credentials.get`）；
  助手的 `kyokai` 组不可以。

## agent sidecar

从 0.7.0 起，AI 任务的循环运行在 `kyokai-agent` 中，这是一个基于 Pi 的 sidecar（PLAN.md，“Pi in the
product”），由引擎为每个任务启动。sidecar 负责循环与发给服务商的请求；引擎负责一切会产生作用的事：用自己的
解析器解析每个提议的操作（computer 工具集的成员，`ai::claude::parse_member`），检查它，运用批准策略与输入租约，
执行它并答复。服务商密钥每次只为一个请求交给 sidecar，从不写到任何地方。双方在 sidecar 的标准输入与输出上
以 JSON 行通信（`ai::sidecar`），协议 2；sidecar 先发 `hello`，引擎拒绝其他版本。

| 消息 | 方向 | 字段 |
|---|---|---|
| `hello` | sidecar → 引擎 | `v` |
| `start` | 引擎 → sidecar | `task`、`protocol`、`url`、`model`、`coordinates`、`system`、`instruction`、`shot`（`{b64, width, height, elements?}`，`elements` 为其旁的元素列表）、`resume?`（保留的对话，由 `instruction` 与 `shot` 接续；压缩过的对话 `{summary}` 则从总结重新开始）、`compact?`（只在关闭工具的情况下总结 `resume`：回答是带用量的 `turn`，然后是 `state` `{summary}`）、`thinking?`（`low`、`medium`、`high`、`max`） |
| `key?` / `key` | sidecar → 引擎 / 返回 | — / `key`（null：没有） |
| `act` | sidecar → 引擎 | `id`、`name`、`input`（一次 computer 工具集成员调用，或 `name: "openai"` 下的 OpenAI 操作对象）、`last`（本轮最后一个操作：服务商需要时，它的结果带上该批操作之后的屏幕；本轮第一个失败之后的操作，引擎答复“未执行”） |
| `result` | 引擎 → sidecar | `id`、`outcome`（`{kind: ok \| image \| text \| failed \| not-executed \| paused, …}`）、`shot?`、`acknowledged`（被控端用户批准了本轮的安全检查） |
| `turn` | sidecar → 引擎 | `text`、`progress`、`flagged`、`usage`（`{input, cached, output}`） |
| `steer`、`follow-up` | 引擎 → sidecar | `text`（`follow-up` 另有 `shot`） |
| `abort` | 引擎 → sidecar | — |
| `state` | sidecar → 引擎 | `state`：一次运行之后的对话，在 `done` 之前发送——服务商自己的历史，每张截图换成一条说明，并去掉 Claude 的思考内容（其签名把它绑定在产生它的历史上），或 OpenAI 的最后一个响应 id |
| `done`、`failed`、`aborted` | sidecar → 引擎 | `text` / `error` / — |

sidecar 发来的一行最长 4 MiB；无法解析、未知或过长的行，或管道关闭，都会以说明原因的方式结束任务。引擎的管道
关闭时 sidecar 随之结束，因此引擎不在后没有任何任务会自行恢复。

### AI 任务中各部分归谁

| 部分 | 归属 | 说明 |
|---|---|---|
| 操作的格式及其解析器 | 引擎（`ai::action`、`ai::claude::parse_member`、`ai::openai::parse_action`） | 无论 sidecar 发来什么，每个提议都在这里解析 |
| 执行器 | 引擎（`ai::task::act_one`、`ai::desktop`） | 唯一会产生作用的部分：在任务的租约下一次执行一个操作 |
| 策略 | 引擎（`ai::policy::decide`） | 根据操作、其目标的事实、范围与类别表决定运行、询问、拒绝或暂停（[computer-use.md](computer-use.md#批准)） |
| 观察 | 引擎（`ai::task::observe`、`desktop::dim_outside`） | 截屏、范围之外的变暗、截图的尺寸与网格 |
| 任务及其租约与批准 | 引擎（`ai::task::Ai`） | 各状态、暂停与接管、紧急停止 |
| 记录与保留的对话 | 引擎（`ai-tasks.json`、`ai-conversations/`） | 各次运行、步骤、token；不含截图的对话，“不保留日志”时只在内存中 |
| 循环与发给服务商的请求 | sidecar（`kyokai-agent`：Pi 与 `conversation.ts`） | 只追加的请求；可在上面带版本的协议之后替换 |
| 密钥 | 引擎的机密存储 | 每次只为一个请求交给 sidecar |

## 状态

| 路径 | 内容 |
|---|---|
| `%APPDATA%\Kyokai\` / `~/Library/Application Support/Kyokai/` | 状态目录（开发配置为 `Kyokai-<profile>`） |
| `secrets.bin` | 加密的机密存储（[security.md](security.md)） |
| `moved.json` | 切换运行方式时本引擎的存储去向：它存在期间引擎处于驻留状态，不生成身份（[run-modes.md](run-modes.md)） |
| `master.key`（Windows） | 存储密钥的 DPAPI 数据块；macOS 上密钥在登录钥匙串中，Linux 上在 Secret Service 中 |
| `gui.json` | 界面偏好设置 |
| `ai.json` | AI 设置：服务商、模型、批准方式（不含密钥） |
| `ai-tasks.json` | 已结束的 AI 任务，不含截图 |
| `ai-conversations/<id>.json` | 任务保留的对话（截图换成一条说明），回复从它继续；“不保留日志”时不写入 |
| `logs/engine.log` | 引擎日志，不含机密：引擎自己的记录、核心的记录（其 stderr）与崩溃跟踪 |
| `logs/gui-crash.log` | 界面的 stderr：它的崩溃跟踪、它所加载核心中的 panic、以错误结束的引擎 |

## 通用规则

- **机密从不外传**（[security.md](security.md)）。
- **绝不把陈旧数据显示为当前数据。** 只有引擎会话连接时界面才显示引擎状态，否则显示“正在启动”或引擎未运行的原因。
- **带版本的边界。** ABI 与本地 API 都带版本；枚举以名称传递。
- **主线程不做慢工作。** `gui.json` 由一个写入 goroutine 写入，保证最后的改动最后写入，退出时刷新（需要结果的调用方使用 `setPrefsNow`）；
  生成密钥、首次加载核心库与查询键盘抓取的状态都不在主线程上；长列表（SSH 文件树、传输）只构建可见的行。Kyokai 复制的文字在写入前用
  `clipboard.mine` 告知（`App.copyText`）。
- **持有应用锁时绝不等待主线程。** MyGo 的通知、剪贴板与窗口调用都要等待主线程，而主线程绘制时要取界面的锁：这些调用都在放开锁之后进行（`statusPushed`、`revealPassword`）；主线程 10 秒没有响应时，看门狗会把每个 goroutine 的调用栈写入 `gui-crash.log`（`watchMain`）。
