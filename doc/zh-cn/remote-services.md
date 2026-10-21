# 远程服务

控制端通过 Kyokai 会话中的隧道，用内置客户端访问被控端自己的 SSH、远程桌面与 VNC（Mac 上为屏幕共享）服务器；
不会为它们在网络上监听任何端口。每项服务都是单独的权限（`use-ssh`、`use-rdp`、`use-vnc`），默认关闭。

## 客户端

界面的客户端（`internal/gui`：`remote.go`、`servicetab.go`、`ssh.go`、`desktops.go`、`screen.go`）运行在控制端，
只经由隧道访问服务；服务标签页与桌面的标签页一样（[gui.md](gui.md)，“连接”）。

- **链接（`serviceLink`）。** `Open` 是隧道（经由引擎的 `tunnel.open`，一个承载该服务字节的 `net.Conn`）；
  `Credential` 与 `SaveCredential` 即 `credentials.get` 与 `credentials.set`；`Keys` 与 `Sign` 即 `keys.list` 与
  `keys.sign`。`credentials.get` 在客户端登录之前才调用；密码从应答直接交给客户端，绝不保存在界面的状态中。
  `credentials.set` 替换某项服务已保存的登录信息。开发时 `KYOKAI_DEV_SERVICES=1` 用一个替身代替引擎：它连接
  `127.0.0.1`（端口来自 `KYOKAI_DEV_SSH_PORT`、`KYOKAI_DEV_RDP_PORT`、`KYOKAI_DEV_VNC_PORT`），并把登录信息保存在内存中；
  不能开隧道的引擎会被拒绝，绝不改用本机自己的端口。
- **SSH**（`golang.org/x/crypto/ssh`）：先用本机的密钥——每个都是一个由引擎签名的 `ssh.Signer`（`keys.sign`，
  签的正是用户认证请求；RSA 使用 `rsa-sha2-512` 或 `rsa-sha2-256`），因此私钥从不到达界面——然后用密码，它也回答
  不回显的键盘交互提示。服务器的密钥在第一次使用时固定（`Credentials.HostKey`，一行 authorized_keys 格式）；
  密钥不同时，在发送任何密码之前就拒绝，只接受用户通过“信任新密钥”选中的那个密钥。外壳使用 `xterm-256color` 的
  PTY；MyGo 的终端（`plugins/terminal`，libghostty-vt）以隧道为其 `Conn` 显示它，其 `Resize` 发送窗口大小变化。
- **远程桌面**（复用代码，`internal/rdp`）：有用户名时使用网络级身份验证（CredSSP 与 NTLMv2），没有用户名时只用
  TLS，此时显示 Windows 的登录界面；图形管道除 macOS（VideoToolbox）外不使用 H.264。服务器的证书不固定即接受：
  隧道是在经过认证的 Kyokai 会话中到达设备自己的端口。按键以扫描码发送。
- **VNC**（复用代码，`internal/rfb`）：安全类型 None、VNC 密码（2），服务器提供时还有 Apple 的 Diffie-Hellman（30）、
  RSA（33）与 SRP（36）用户名加密码登录；按键以 keysym 发送。已由他人登录的 Mac 共享其屏幕（控制台会话）。
- **两种画面**（`screenClient`）：帧缓冲在协议的 goroutine 中转换为 RGBA，并在主线程显示，像查看窗口一样适应窗口
  或以原始大小显示；指针、按键与滚轮以对方桌面的像素发送。在映射与翻译模式下，键盘抓取像桌面一样获取按键
  （`App.takeKey`）；传统模式下按名称的键连同其修饰键发送，文字以文字发送。标签页位于前台期间剪贴板的文本互通：
  标签页来到前台时发送本机的剪贴板，对方桌面的剪贴板改变时传回。Ctrl+Alt+Del 在“操作”中。
- **保存的登录信息**：通过表单登录成功后，保存用户名（与域）；打开“记住密码”时连同密码一起保存。
