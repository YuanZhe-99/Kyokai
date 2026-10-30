# 远程服务

控制端通过 Kyokai 会话中的隧道，到达被控端自身的 SSH 服务器、远程桌面或 VNC（macOS 上为屏幕共享），隧道通往被控端的回环地址。
这些服务不在网络上监听任何东西：它们保持原样，只有 Kyokai 准入的人才能到达。被控端也可以提供经 NanoKVM USB 与它有线连接的电脑（[NanoKVM USB](#nanokvm-usb)）。

## 服务

| 服务 | 权限 | 被控端上的标准端口 |
|---|---|---|
| `ssh` | `use-ssh` | 22 |
| `rdp` | `use-rdp` | 3389 |
| `vnc` | `use-vnc` | 5900 |

被控端连接 127.0.0.1，然后是 ::1，使用该服务的端口：标准端口，或者被控端用户在“访问”的“本机服务的端口”中设置的端口
（`access.settings` 的 `{service_ports: {ssh?, rdp?, vnc?}}`，端口为 0 或未给出即标准端口；状态以 `service_ports`
给出实际使用的端口）。控制端只指定服务，因此到达不了被控端的其他端口。主页卡片提供在这些端口上有应答的服务。由被控端用户打开服务（Windows 可选功能中的 SSH 或 macOS 的“远程登录”、
远程桌面、某个 VNC 服务器或屏幕共享）；Kyokai 从不更改它们。

## 准入

每个服务都与其他权限一样是一项权限（[identity-and-access.md](identity-and-access.md)）：任何权限都不隐含另一个，隧道只请求它自己的那一项。
它们列在“使用正确密码时”之下，默认关闭。在那里打开后，正确的 Kyokai 密码或一把可连接的密钥无需确认即可到达该服务，无人值守的被控端
也是如此；关闭时会询问被控端用户，用户在确认中看到该服务，而无人在电脑前时隧道会被拒绝（`absent`）。既没有保存的密码也没有密钥的控制端
会不带密码请求。

只有在被控端于 3 秒内到达其服务之后，隧道才会被准入；否则以 `service-down` 拒绝。被控端在 4 个桌面会话之外最多服务 8 条隧道（更多的以
`busy` 拒绝）；每条隧道都像桌面会话一样让被控端保持唤醒，并在“已连接到本机”中连同服务一起显示。隧道从不续接：断开的隧道就此结束，由客户端重新连接。

## 隧道

一个会话承载一条隧道（[protocol.md](protocol.md)，“隧道”）：控制端在问候消息中指明服务，只发给声明了 `tunnel` 的被控端，之后字节以带
`0x05` 标记的明文传输。途中的每个缓冲区都有上限——在服务与会话之间、在会话与界面之间，每个方向 16 块、每块最多 64 KiB——因此快速的服务
会等待缓慢的读取方，而不会占满内存，两个方向也从不互相等待。60 秒内未收到对端任何帧的隧道会结束。在助手模式下，助手只转发字节，不解码任何内容。

在控制端，界面通过本地 API 的 `tunnel.open`（`{device_id, address?, service}`）打开隧道：引擎像桌面会话一样拨打被控端，有保存的密码时使用它，
否则使用登录密钥，再否则不带密码请求；最多等待被控端 90 秒（可能会询问其用户）；然后答复 OK，此后连接双向以类型为 14 的包承载字节，直到
任一方关闭。拒绝时会说明原因：`unsupported`（被控端没有声明隧道：没有向它请求任何东西）、`service-down`、`denied`、`timeout`、`absent`、
`busy`、`password`、`rate-limited`、`unreachable`、`key-changed`、`wrong-device`、`incompatible`、
`broken` 或 `closed`（在连接之前就已结束）。`localapi.Client.Tunnel` 以 `net.Conn` 返回这些字节。`kyokai-svc ctl tunnel
--device ID --service ssh` 把隧道接到它的标准输入与输出，供 OpenSSH 的 `ProxyCommand` 使用：

```bash
ssh -o ProxyCommand="kyokai-svc ctl tunnel --device 482916735 --service ssh" me@kyokai
```

## 保存的登录信息

控制端把每项服务在某个被控端上的登录信息保存在机密存储的 `saved.<设备 ID>` 下，与 Kyokai 密码以及被控端接受过的密钥放在一起
（[identity-and-access.md](identity-and-access.md)，“保存的密码”）：SSH 为用户、密码或登录密钥，以及首次使用时固定的服务器密钥；远程桌面为
用户、域与密码；VNC 为用户（Apple 的屏幕共享需要）与密码。

| 请求 | 作用 |
|---|---|
| `credentials.set`（`{device_id, service, credentials}`） | 替换该服务的登录信息——包括它的 `method`（`key`、`password`、`account` 或 `none`，随“记住”保存）与用户名——但未给出的密码或服务器密钥保持不变，空密码表示移除 |
| `credentials.get`（`{device_id, service}`） | 回答它，包括密码；只为引擎的所有者或助手的操作者提供，供客户端在当时登录使用 |
| `credentials.forget`（`{device_id, service?}`） | 忘记该服务的登录信息、被接受的密钥（`key`）、Kyokai 密码（`password`），或为该被控端保存的一切（不带服务） |

状态只说明保存了哪些（`recent[].saved`：`password`、`key`、`ssh`、`rdp`、`vnc`），从不包含机密。私钥从不离开引擎：SSH 客户端通过
`keys.sign` 签名（[identity-and-access.md](identity-and-access.md)，“密钥”）。

## 安全

- 隧道到达的服务把它的回环地址当作被控端自己：对本机连接另眼相待的服务（来自 localhost 时不要密码）会把这份待遇给每个为它准入的控制端。
  因此它的权限默认关闭，并会询问被控端用户。
- 在远程桌面或 VNC 会话中，控制端通过被控端自己的服务工作，而不是 Kyokai 的采集与输入：“在屏幕采集中隐藏 Kyokai”以及对 Kyokai 窗口的保护
  在那里不适用，控制端能做什么由该服务自己的登录决定。
- 保存的服务密码留在引擎的存储中，只通过 `credentials.get`、在客户端登录时、为所有者或操作者到达界面的客户端；从不出现在状态或日志中
  （[security.md](security.md)）。
- NanoKVM USB 把有线连接的电脑的键盘和鼠标交给被准入的人：那台电脑允许自己的键盘做的任何事（它的登录界面、它的固件设置）。
  因此它的权限默认关闭，并会询问被控端用户。

## 客户端

界面的客户端（`internal/gui`：`remote.go`、`servicetab.go`、`ssh.go`、`desktops.go`、`screen.go`）运行在控制端，
只经由隧道访问服务；服务小窗与桌面的小窗一样（[gui.md](gui.md)，“连接”）。

- **小窗**（`openService`）。每打开一次 SSH 就是一个单独的小窗和连接，从第二个起编号（SSH 2、SSH 3），
  受被控端最多 8 条隧道的限制；设备的远程桌面或 VNC 是它屏幕的一个会话，只有一个小窗，再次打开时显示它，已结束则重新连接。
- **链接（`serviceLink`）。** `Open` 是隧道（经由引擎的 `tunnel.open`，一个承载该服务字节的 `net.Conn`）；
  `Credential` 与 `SaveCredential` 即 `credentials.get` 与 `credentials.set`；`Keys` 与 `Sign` 即 `keys.list` 与
  `keys.sign`。`credentials.get` 在客户端登录之前才调用；密码从应答直接交给客户端，绝不保存在界面的状态中。
  `credentials.set` 替换某项服务已保存的登录信息。开发时 `KYOKAI_DEV_SERVICES=1` 用一个替身代替引擎：它连接
  `127.0.0.1`（端口来自 `KYOKAI_DEV_SSH_PORT`、`KYOKAI_DEV_RDP_PORT`、`KYOKAI_DEV_VNC_PORT`），并把登录信息保存在内存中；
  不能开隧道的引擎会被拒绝，绝不改用本机自己的端口。
- **登录方式**（`signInMethods`、`signInPlan`）：小窗不会尝试用户没有选择的任何方式。该设备的这项服务没有记住的方式时
  （第一次连接、关闭了“记住”，或 0.5.1 之前保存的登录信息），在打开任何隧道之前先显示表单：SSH 提供“密钥”（用户名加本设备的密钥）
  或“密码”（用户名加密码）；VNC 提供“仅密码”、“用户名和密码”（Mac 的）或“无需登录”；远程桌面提供“用户名和密码”或“在远程屏幕上”。
  通过表单登录成功且打开了“记住”时，连同方式一起保存（有密码的方式也保存密码），下次连接用同样的方式登录而不再询问；
  关闭“记住”时只保留用户名与域，并忘记之前记住的方式。记住的方式失败，或“无需登录”遇到需要登录的服务器时，
  再次显示表单并选中该方式。
- **SSH**（`golang.org/x/crypto/ssh`），只用所选方式：“密钥”提供本设备的密钥——每个都是一个由引擎签名的
  `ssh.Signer`（`keys.sign`，签的正是用户认证请求；RSA 使用 `rsa-sha2-512` 或 `rsa-sha2-256`），因此私钥从不到达界面——
  且从不发送密码；“密码”从不提供密钥：密码也回答不回显的键盘交互提示，但服务器拒绝过一次后，同一连接中不会再发送第二次（Mac 对每次失败都约一分钟后才回应，
  并记在来源地址上，而每条隧道的来源都是它自己的回环地址）。密钥交换之后，拒绝或连接被关闭都视为登录被拒绝，
  直到 30 秒时限仍无回应则显示服务器没有回应。服务器的密钥在第一次使用时固定（`Credentials.HostKey`，一行 authorized_keys 格式）；
  密钥不同时，在发送任何密码之前就拒绝，只接受用户通过“信任新密钥”选中的那个密钥。此后同一连接中的每次密钥交换（重新协商密钥）
  都必须出示第一次接受的那个密钥，否则连接结束。外壳使用 `xterm-256color` 的
  PTY；MyGo 的终端（`plugins/terminal`，libghostty-vt）以隧道为其 `Conn` 显示它，其 `Resize` 发送窗口大小变化。
- **远程桌面**（复用代码，`internal/rdp`）：“用户名和密码”使用网络级身份验证（CredSSP 与 NTLMv2），“在远程屏幕上”只用
  TLS，此时显示 Windows 的登录界面（只允许网络级身份验证的服务器会要求换一种方式）；图形管道除 macOS（VideoToolbox）外不使用 H.264。服务器的证书不固定即接受：
  隧道是在经过认证的 Kyokai 会话中到达设备自己的端口。按键以扫描码发送。
- **VNC**（复用代码，`internal/rfb`）：安全类型 None、VNC 密码（2），服务器提供时还有 Apple 的 Diffie-Hellman（30）、
  RSA（33）与 SRP（36）用户名加密码登录；按键以 keysym 发送。已由他人登录的 Mac 共享其屏幕（控制台会话）。在 Windows 上，
  TigerVNC 安装后即可使用；TightVNC 与 UltraVNC 会拒绝来自本机的连接，直到打开“Allow loopback connections”（TightVNC）
  或 `AllowLoopback`（UltraVNC），并且需要 VNC 密码（UltraVNC：关闭 MS-Logon 与加密插件）；不支持 RealVNC Server 自己的登录方式（RA2），
  只支持允许关闭加密时使用其 VNC 密码。小窗会说明是哪种情况：在问候之前就关闭的服务器（`rfb.ErrClosedBeforeGreeting`）
  会提示回环设置，拒绝时显示服务器自己的原因（`rfb.RefusedError`），不提供任何受支持登录方式的服务器
  （`rfb.ErrUnsupportedServer`，RFB 3.3 中超出一个字节、按整体命名的类型）会要求使用 VNC 密码认证。“仅密码”只向服务器请求 VNC 密码（2），“用户名和密码”优先请求 Apple 的登录方式，
  “无需登录”只请求无安全验证；需要登录而没得到的服务器会要求换一种方式，绝不先以空白信息尝试而被 Mac 记为一次失败；在登录过程中关闭连接的
  服务器（`rfb.ErrSignInDropped`，Mac 对连续失败的做法）视为登录被拒绝。
- **两种画面**（`screenClient`）：帧缓冲在协议的 goroutine 中转换为 RGBA，并在主线程显示，像查看窗口一样适应窗口
  或以原始大小显示；指针、按键与滚轮以对方桌面的像素发送。在映射与翻译模式下，键盘抓取像桌面一样获取按键
  （`App.takeKey`）；传统模式下按名称的键连同其修饰键发送，文字以文字发送；每种模式下，控制端的输入法在画面中都是关闭的，
  与桌面的画面相同。小窗位于前台期间剪贴板的文本互通：
  小窗来到前台时发送本机的剪贴板，对方桌面的剪贴板改变时传回。“操作”中有“强制粘贴”（把本机剪贴板中的文本像按键一样键入：远程桌面经扫描码与 Unicode 按键事件，VNC 经 keysym）与 Ctrl+Alt+Del。
- **保存的登录信息**：见上面的登录方式；SSH 还保存第一次使用时固定的服务器密钥，密钥对记住的登录而言是新的时，
  连同记住的方式与用户名一起保存。

## NanoKVM USB

被控端可以提供经 Sipeed 的 NanoKVM USB 与它有线连接的电脑：一块 UVC 采集卡采集那台电脑的 HDMI 输出，一颗 CH340 串口芯片
通过其 USB 键盘和鼠标为它输入（CH9329 的命令帧，57,600 波特）。控制端在单独的小窗中像桌面一样查看并操作那台电脑；被控端自己的
屏幕、输入、声音、剪贴板或 AI 都不会到达它。

- **选择。** 在“访问”的“NanoKVM USB”中，被控端用户选择采集卡与串口，并打开“提供给其他设备”（`access.settings`
  `{nanokvm: {on, video, serial}}`；两者未选齐时打开会被拒绝；状态以 `nanokvm` 给出）。
  `nanokvm.devices` 列出本机的采集卡与 USB 串口（`{video, serial, suggested}`，每个端口为 `{id, name, nanokvm}`），并建议
  看起来属于同一台 NanoKVM 的那一对：同一集线器上的 MacroSilicon 采集芯片（USB 厂商 345F）与 CH340（1A86:7523），或者各只有
  一个时的那两个。Linux 从 sysfs 列出 V4L2 采集节点与 `ttyUSB`/`ttyACM` 串口；Windows 列出 Media Foundation 的视频采集设备与
  “端口”类设备，按它们在设备树中所在的集线器配对；macOS 列出 AVFoundation 的外部设备与 `/dev/cu.usbserial*`、
  `/dev/cu.wchusbserial*`、`/dev/cu.usbmodem*`，只在各有一个时配对。被控端用户打开之前，什么都不提供。
- **在本机上。** 选好设备后（无论是否提供给其他设备），本机用户自己也能打开它，不需要网络会话、密码或权限：向本机自己的设备 ID
  发送带 `nanokvm` 的 `session.connect`（`session::local_kvm`），会列出一个控制端会话，其被控端一侧运行在同一个引擎中，两者在内存中相连。
  它的“自动”就是原始（被控端能发送时）：既不编码，也不经网络发送。被控端用户永远优先：控制端的 NanoKVM 会话已连接时，请求答复
  `nanokvm-in-use`；带 `take_over` 时先结束那个会话，等串口空出后再打开（最多 3 秒，否则为 `service-down`）；本机打开或正在打开期间，
  请求它的控制端得到 `busy`。主页“本机”卡片（打开）、“访问”（打开）、+ 选择器的“这台电脑”一行与搜索都能打开它，小窗名为“这台电脑”。
- **准入。** 它的权限是 `use-nanokvm`，像服务一样单独请求。提供它就是让正确的密码或已授权的登录密钥使用它：打开“提供给其他设备”会把
  `use-nanokvm` 加入“使用正确密码时”（其图块仍可单独更改），关闭则将其移除（`Access::set_nanokvm`）；没有密码或密钥时会询问被控端用户。没有提供它的被控端在询问任何人之前
  以 `service-down` 拒绝，串口打不开（被其他程序占用、没有权限）时也是如此；同一时间只有一个会话（`busy`）。它的裁决不带续接
  令牌：断开的会话就此结束，由查看窗口重新连接。控制端只向声明了 `nanokvm` 的被控端在问候消息中指明它（[protocol.md](protocol.md)，
  “NanoKVM 会话”）；被控端的状态采样带有 `nanokvm` 且保存的登录信息授予它时，主页卡片与 + 选择器提供它。
- **画面。** 被控端以 1920×1080 或采集卡给出的尺寸、以它最快的帧率读取采集卡的 MJPEG（Linux 上为 V4L2，Windows 上为 Media
  Foundation；macOS 上为 AVFoundation，由它解码）；不是一个完整 JPEG 的画面（刚打开后的最初几帧）会被丢弃。默认解码它们
  （zune-jpeg），并像屏幕一样以查看窗口选择的编码器编码，使用同样的 QoS 与确认，未变化的画面不再编码。**原始**——编码器
  `mjpeg`，由 Linux 或 Windows 被控端提供给能解码它的控制端，“自动”从不选它——按采集卡给出的样子发送 JPEG，每一帧都是关键帧，
  帧率不超过查看窗口的上限：被控端不编码，也没有二次压缩，但 1080p 时约需 50 到 120 Mbit/s，且没有码率控制，适合本地网络。
  打不开的采集卡显示占位图 `nanokvm`，并像屏幕一样重试。
- **输入。** 按位置的按键以 USB 用法码发给有线连接的电脑（美式布局的位置，由它自己的布局解读），按名称的按键连同其修饰键，文字
  按美式键盘的打法输入（在那里没有按键的字符会被跳过），指针在画面上用绝对坐标（0–4095），带三个按键与滚轮。串口繁忙时
  （它每秒约传 400 帧）指针移动会被合并；按下、松开与按键从不合并。被控端每秒询问一次设备那台电脑的状态（GET_INFO）：在字母或
  小键盘按键之前，让它的 Caps Lock 与 Num Lock 与控制端一致，与桌面相同。“操作”中有“强制粘贴”（作为文字，见上）、Ctrl+Alt+Del 与“唤醒”（按一下 Shift）；会话
  结束时松开所有按住的键。
- **平台。** 在 Linux 上，引擎的用户需要能访问采集卡（`video` 组，或所在座席）与串口（`dialout`；Arch 上为 `uucp`）。macOS 在第一次
  打开采集卡时请求“摄像头”权限（`NSCameraUsageDescription` 与 `com.apple.security.device.camera`）。在助手模式下由引擎自己打开这些设备。
