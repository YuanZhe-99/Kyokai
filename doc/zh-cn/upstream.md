# 上游契约

Kyokai 依赖各上游的哪些行为。均于 2026-10-16 通过阅读固定版本的源码或官方文档核实。Phase 0 可行性验证运行后，
会把实测结果补充到对应小节；这里没有列出的内容一律不作假设。

## MyGo v0.5.5

- **渲染。** 自带 GPU 渲染器：Windows 上为 Direct3D 11，macOS 上为 Metal，CPU 光栅化作为回退。系统调用经由
  purego 与手写的 COM；全部可用 `CGO_ENABLED=0` 构建。界面为即时模式并按标识保留状态；`Window.Update(fn)`
  在主线程运行 `fn` 并重建视图。
- **实时画面。** `(*ui.Bitmap).Update(img)` 保留纹理、替换像素；步长为 4 × 宽度的 `*image.RGBA` 不复制直接使用，
  在下一次 `Update` 之前不得修改。必须在主线程调用。每帧都完整上传预乘 RGBA（没有 BGRA、YUV、脏矩形或共享纹理）；
  Metal 会等待在途帧。以一半或更小的尺寸绘制的位图每次更新后都会在 CPU 上做盒式降采样，经由 `ui/shader`
  绘制则不会。`ui.Image(c, bm).Fit(...)` 负责缩放；`Pixelated()` 为最近邻。
- **输入。** `Element.Focusable().HandleInput(func(ui.InputEvent) bool)` 提供按键按下/抬起、文本、输入法组字、
  命令、指针按下/抬起/移动、滚动（`DX`、`DY`、`Precise`），坐标为元素内的 DIP。缺口：没有扫描码；约 70 个
  `Key` 值（没有小键盘键、CapsLock、NumLock、PrintScreen、F13–F24、媒体键）；修饰键只有位掩码（不分左右）；
  Windows 丢弃未映射的键，并且总把 `WM_SYSKEYDOWN`（Alt、F10、Alt+F4）交给 `DefWindowProc`；X1/X2 在抬起时以
  `KeyBack`/`KeyForward` 到达；macOS 把 Control 加单击变成右键、忽略 CapsLock、菜单快捷键（Cmd+Q/W/H/C/V）优先；
  没有指针锁定，也没有底层钩子。
- **应用的窗口。** 多窗口、全屏、位置尺寸、置顶、`SetContentProtection`，以及 `NativeHandle()`（HWND / NSWindow）。
- **应用服务。** 托盘（菜单、图标）、通知（Windows 上为气泡）、剪贴板（文本、HTML、PNG 图片；没有变化事件）、
  对话框、外部打开、按应用名称区分的单实例锁、登录时启动、电源事件（锁定/解锁、空闲时间）、`App.Path`
  的各种目录（没有单独的 config 类）、`Relaunch`、可取消的退出钩子。
- **更新器。** `go tool mygo keygen` 把一对 Ed25519 密钥（64 字节私钥与 32 字节公钥的 base64）写到 `-o DIR`，
  默认是所有 MyGo 项目共用的按用户目录；不加 `-force` 时拒绝覆盖。设置 `MYGO_UPDATER_PRIVATE_KEY` 后，
  `mygo build` 为每个目标签名整个应用的归档和最多三个增量包；`update-<target>.json` 本身不签名。应用通过 HTTPS
  读取 GitHub 最新发布的清单，安装前校验大小与签名。Windows 把旧文件改名挪开（已加载的 DLL 可以改名）；
  macOS 替换整个 `.app`。更新需要对安装位置有写权限。
- **打包。** Windows：`.exe` 与按用户安装的 NSIS 安装包（`%LOCALAPPDATA%\Programs\<Name>`，不提权）。
  macOS：`.app` 与 `.dmg`，只能在 macOS 主机上签名。`resources/` 与 `resources/<goos>-<goarch>/` 下的文件随应用放在
  exe 旁或 `Contents/Resources` 中，运行时用 `App.Path(PathResources)` 找到。嵌套的 Mach-O 文件会用应用的身份重新签名；
  非 ad-hoc 身份会加上 `--options runtime --timestamp`（`.dmg` 也会加 `--timestamp`）。`macos.helperEntitlements`
  为嵌套代码单独指定 entitlements；`minimumSystemVersion` 默认为 12.0。
- **purego。** Windows 上用 `syscall.LoadLibrary` 加载库（purego 的 `Dlopen` 在那里不编译）；macOS 上用
  `purego.Dlopen`。回调永不释放（共享上限约 1000–2000 个），不能带浮点参数；在 Windows ARM64 上调用完全不能传浮点。
- **实测（S2，Windows ARM64，`spikes/s2-viewer`）。** 来自 Rust 库的 1920×1080 RGBA 帧经 `Bitmap.Update` 显示在 500 行列表旁：
  每秒显示 59–60 帧，MyGo 的帧统计显示受垂直同步约束的约 16 毫秒帧，构建与布局约 1 毫秒；画面显示约占一个核心的
  21–25 %（进程 31–37 % 减去测试图案的绘制）。

## EasyTier v2.7.0

- **来源。** 2026-10-06 的预发布版（最新稳定版：v2.6.4）。根目录许可证为 LGPL-3.0（没有“或更新版本”条款）。
  crates.io 上停在 2.0.3，因此按提交固定为 git 依赖（`8b7f1f0196edb5b580829ed5f135050fb5853478`）；它自己的若干依赖也是
  git 依赖。Edition 2024，rust-version 1.95。会移动的 `nightly` 标签不算发布。
- **构建。** 需要 `protoc`（构建脚本只在 Windows 上下载一个 x64 版本）；在 `aarch64-pc-windows-msvc` 上 `ring` 需要 clang；
  在 x86/x64 Windows 上 `thunk-rs` 会下载 VC-LTL/YY-Thunks，WinDivert 从 C 源码编译。`kcp` 功能需要 libclang。
  v2.7.0 只有启用 `faketcp` 时才链接 `Packet`。
- **功能开关。** TUN 是一个功能开关，可以不启用（上游没有在 Windows 与 macOS 上测试过不带 TUN 的构建）。
  不启用 `wireguard`、`aes-gcm` 或 `openssl-crypto` 时，网络加密会静默退化为 XOR。
- **嵌入 API。** `easytier::instance::factory::create_native_instance(TomlConfig)` →
  `start()` / `stop()`；`route_snapshots()`、`peer_snapshots()`、`node_snapshot()`、`add_connector()`。
  数据面：`data_plane_tcp_connect`、`data_plane_tcp_bind`、`data_plane_udp_bind` 返回虚拟网络上的 tokio 流
  （仅 IPv4，128 KiB 缓冲，smoltcp 不带拥塞控制）；`accept()` 给出对端的虚拟地址；节点的 IPv4 变化时流会关闭。
- **`no_tun` 入站。** 发往本节点虚拟 IPv4 的包在 smoltcp 中终止（16 KiB 窗口），再转发到 `127.0.0.1:<同一端口>`，
  因此除非用 `tcp_whitelist` / `udp_whitelist` 限制，本机所有回环服务都能被网络访问；应用看到的来源是 `127.0.0.1`。
  入站需要 IPv4（静态或 DHCP；存在对端路由后 DHCP 才分配）。
- **节点与路由。** 路由条目带有 `inst_id`（配置的 `instance_id`）、`hostname`（去掉控制字符，最多 32 个字符）、
  IPv4、NAT 类型、版本与功能标志；`peer_id` 每次运行都会变化。只列出可达的节点；不可达条目 90 秒后清除。
  没有可自由填写的元数据字段。
- **网络密钥。** 传统模式用名称 + 密钥的非密码学哈希检查成员身份（在首个握手中发送，也会发给公共服务器，
  因此弱密钥可被离线猜测），并且整个网络使用同一把静态 AES-128-GCM 密钥加密，没有前向保密。中继只转发密文。
  安全模式（每条链路 Noise）必须整个网络一致；普通公共服务器很可能拒绝它。
- **服务器 URL。**

| 协议 | 默认端口 | 说明 |
|---|---|---|
| `tcp`、`udp` | 11010 | |
| `wg` | 11011 | |
| `quic` | 11012 | |
| `faketcp` | 11013 | 需要 `faketcp` 功能 |
| `ws`、`wss` | 80、443 | |
| `http`、`https` | — | 仅作连接器：跟随重定向或读取 URL 列表 |
| `txt` | — | 仅作连接器：DNS TXT 记录中的 URL |
| `srv` | — | 仅作连接器：`_easytier._<proto>.<domain>` |

- **公共服务器。** 不属于网络 N 的公共服务器以 `PublicServer_…` 节点身份加入 N，始终中继 RPC，除非另行配置也会中继数据；
  N 中的节点会对所有可达但未直连的节点尝试 UDP 打洞。

## RustDesk 1.5.0

| 模块 | 许可状态 | Kyokai 的用法 |
|---|---|---|
| 仓库 | AGPL-3.0 | 复用的文件保留其声明 |
| `libs/scrap` | 上游 scrap 为 MIT；RustDesk 新增部分为 AGPL | Windows DXGI 采集器，解除耦合后复用 |
| `libs/enigo` | 来自上游 enigo 的 MIT 文件，含 RustDesk 的修改 | 虚拟桌面移动与扫描码逻辑 |
| `libs/clipboard` | AGPL，另含来自 FreeRDP 的 Apache-2.0 C 代码 | 不使用（基于 RDP 的文件复制） |
| `libs/hbb_common` | 没有许可授权 | 绝不复制 |
| `rustdesk-org/hwcodec` | 没有许可授权 | 绝不复制 |

- `libs/scrap` 引用了 `hbb_common` 与 `libs/base`，并读取 RustDesk 的配置存储，不能直接当作 crate 使用。
  Windows 采集为 DXGI 桌面复制，带 HDR 色调映射与 GDI 回退；旋转的屏幕经过 libyuv。macOS 采集为 CGDisplayStream，
  已在 macOS 15 SDK 中废弃，macOS 15 与 26 上有黑帧报告。
- 编解码：通过 vcpkg 的 libvpx（VP8/VP9）、aom（AV1）与 libyuv，另有基于 FFmpeg 的硬件编解码；Windows ARM64
  版本（自 1.4.8 起提供）只做软件编码。
- 上游 `enigo` 0.6.1（MIT）只把绝对移动换算到主显示器；RustDesk 的分支用 `MOUSEEVENTF_VIRTUALDESK` 覆盖整个虚拟桌面。
- macOS 权限：`CGPreflightScreenCaptureAccess` / `CGRequestScreenCaptureAccess`、`AXIsProcessTrustedWithOptions`、
  `IOHIDCheckAccess` / `IOHIDRequestAccess`。
- 没有 SYSTEM 服务时，RustDesk 无法采集 UAC 安全桌面或锁屏，向提权窗口发送的 `SendInput` 会被丢弃（UIPI）。

## OpenAI 电脑操作

2026-10-16 依据 computer use 指南与 openai-python 的类型定义核实。

- Responses API，`tools: [{"type": "computer"}]`（没有其他字段）。指南中点名的模型：computer 工具用 `gpt-6.1-sol`；
  `gpt-6-astra` 更推荐代码执行，Kyokai 不使用。`computer-use-preview` 仍供旧集成使用。
- 输出项 `computer_call`：`id`、`call_id`、`status`、`pending_safety_checks`（`id`、`code`、`message`），以及有序的
  `actions` 列表（另有旧式的单个 `action`）。
- 动作：`click`（`button`：left、right、wheel、back、forward；`x`、`y`；可选 `keys`）、`double_click`（`x`、`y`、`keys`）、
  `drag`（点组成的 `path`、`keys`）、`keypress`（`keys`）、`move`（`x`、`y`、`keys`）、`screenshot`、`scroll`
  （`x`、`y`、`scroll_x`、`scroll_y`、`keys`）、`type`（`text`）、`wait`。
- 回复为 `computer_call_output`，带 `call_id`、`output: {type: "computer_screenshot", image_url: "data:image/png;base64,…"}`，
  批准后还有 `acknowledged_safety_checks`；请求带上 `previous_response_id`。坐标位于截图的像素空间；截图缩小后，
  坐标需要换算回去。

## Claude 电脑操作

2026-10-16 依据 Anthropic 的 computer use 文档与 API 参考核实。

- 工具项 `{"type": "computer_toolset_20260801"}`：在 Claude API 上正式可用，无需 beta 头，没有 `name` 也没有显示尺寸；
  可选的 `configs` 用来关闭成员（17 个默认全开）；不能与 `computer_20251124` 出现在同一请求中。Claude Opus 5.5 与
  Sonnet 5.5 在 Claude API 上只接受该 toolset。支持的模型：Fable 5.1、Fable 5、Opus 5.5、Opus 5、Sonnet 5.5、
  Sonnet 5、Opus 4.8、Haiku 5.5。
- 调用是 `tool_use` 块，其 `name` 即成员名，带 `"toolset_name": "computer"`；一轮可以有多个，按顺序执行。每个块在下一条
  用户消息中对应一个 `tool_result`，并都回显 `"toolset_name": "computer"`；第一次失败之后的其余块以 `is_error`
  （“Not executed …”）作答。`screenshot` 与 `zoom` 返回图片，`cursor_position` 返回文本，其余返回简短的 `OK`。

| 成员 | 输入 |
|---|---|
| `screenshot`、`cursor_position`、`left_mouse_down`、`left_mouse_up` | 无 |
| `zoom` | `region` `[x0, y0, x1, y1]` |
| `left_click`、`right_click`、`middle_click`、`double_click`、`triple_click` | 可选 `coordinate` `[x, y]`，可选修饰键 `text`（`shift`、`ctrl`、`alt`、`super`，以 `+` 连接） |
| `left_click_drag` | `start_coordinate`、`coordinate`，可选 `text` |
| `mouse_move` | `coordinate` |
| `scroll` | `scroll_direction`（up、down、left、right）、`scroll_amount`，可选 `coordinate`、`text` |
| `type` | `text` |
| `key` | `text`（`Return`、`ctrl+s`、`alt+Tab`），可选 `repeat` 1–100 |
| `hold_key` | `text`、`duration` 秒，最多 300 |
| `wait` | `duration` 秒，最多 300 |

- 坐标位于所返回完整截图的像素空间，`zoom` 之后也是如此。图片：长边最多 2576 像素、约 3.75 MP，API 不会替你缩小；
  一个请求中超过 20 张图片后，每边最多 2000 像素。建议约 1280×720 到 1366×768；macOS Retina 截图要缩小，或把坐标减半。
- 历史只能追加：修改或删除较早的轮次（例如裁掉旧截图）会使之后的思考块失效，新账号上会被拒绝；用服务端清除工具结果
  （`context-management-2025-06-27`）或压缩来控制上下文。
- Claude Opus 5.5：自适应思考无法关闭，effort 默认为 `medium`，强制 `tool_choice` 返回 400，拒绝以
  `stop_reason: "refusal"` 和 `stop_details` 返回；服务端回退（`server-side-fallback-2026-07-01`、`fallbacks: "default"`）
  会换模型重试。工具调用之间的进度说明以 `thinking` 块返回，用 `thinking.display: "updates"`
  （`thinking-display-updates-2026-08-18`）即可读取。
- 分类器会扫描返回的截图以发现提示注入，并引导模型向用户确认；Anthropic 仍要求对有后果的操作进行人工确认。

## 操作系统

- **macOS：** 自 Sequoia 起，不经系统选择器采集屏幕的应用每月需要重新确认；可以免除此事的
  “Persistent Content Capture” entitlement 需要 Apple 批准。隐私授权跟随代码签名：ad-hoc 签名每次构建都会变化。
- **Windows：** 普通用户进程只能采集和控制普通桌面；提权窗口（UIPI）、UAC 安全桌面与锁屏需要 SYSTEM 进程。
