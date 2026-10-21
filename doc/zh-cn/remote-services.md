# 远程服务

控制端通过 Kyokai 会话中的隧道，到达被控端自身的 SSH 服务器、远程桌面或 VNC（macOS 上为屏幕共享），隧道通往被控端的回环地址。
这些服务不在网络上监听任何东西：它们保持原样，只有 Kyokai 准入的人才能到达。

## 服务

| 服务 | 权限 | 被控端上的端口 |
|---|---|---|
| `ssh` | `use-ssh` | 22 |
| `rdp` | `use-rdp` | 3389 |
| `vnc` | `use-vnc` | 5900 |

被控端连接 127.0.0.1，然后是 ::1，使用该服务的端口。由被控端用户打开服务（Windows 可选功能中的 SSH 或 macOS 的“远程登录”、
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
| `credentials.set`（`{device_id, service, credentials}`） | 替换该服务的登录信息，但未给出的密码或服务器密钥保持不变，空密码表示移除 |
| `credentials.get`（`{device_id, service}`） | 回答它，包括密码；只为引擎的所有者或助手的操作者提供，供客户端在当时登录使用 |
| `credentials.forget`（`{device_id, service?}`） | 忘记该服务的登录信息、被接受的密钥（`key`），或为该被控端保存的一切（不带服务） |

状态只说明保存了哪些（`recent[].saved`：`password`、`key`、`ssh`、`rdp`、`vnc`），从不包含机密。私钥从不离开引擎：SSH 客户端通过
`keys.sign` 签名（[identity-and-access.md](identity-and-access.md)，“密钥”）。

## 安全

- 隧道到达的服务把它的回环地址当作被控端自己：对本机连接另眼相待的服务（来自 localhost 时不要密码）会把这份待遇给每个为它准入的控制端。
  因此它的权限默认关闭，并会询问被控端用户。
- 在远程桌面或 VNC 会话中，控制端通过被控端自己的服务工作，而不是 Kyokai 的采集与输入：“在屏幕采集中隐藏 Kyokai”以及对 Kyokai 窗口的保护
  在那里不适用，控制端能做什么由该服务自己的登录决定。
- 保存的服务密码留在引擎的存储中，只通过 `credentials.get`、在客户端登录时、为所有者或操作者到达界面的客户端；从不出现在状态或日志中
  （[security.md](security.md)）。
