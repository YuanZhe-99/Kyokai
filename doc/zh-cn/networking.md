# 网络

Kyokai 设备之间通过引擎内嵌的 EasyTier 节点互相连接（核心中的 `net.rs`）。节点通过标准 EasyTier 服务器加入一个
EasyTier 网络；没有 TUN 设备，不需要管理员权限，也没有自定义的协调服务器。

## 节点

| 设置 | 值 |
|---|---|
| 实例 ID | 对 9 位设备 ID 取 UUIDv5（命名空间 `6b796f6b-6169-4e00-8000-000000005331`） |
| 主机名 | `Kyokai-<设备 ID>` |
| TUN | 无（`no_tun`）；流量走 EasyTier 的进程内数据面（smoltcp） |
| 地址 | 由网络的 DHCP 分配的 IPv4 |
| 监听 | 无；打开直接连接时，在所有地址上以 47311–47320 中第一个 TCP 与 UDP 都空闲的端口监听（全部被占用时不监听，例如被本机的其他引擎占用） |
| 直接连接 | 默认关闭：不做 UDP、TCP 或对称型打洞，不用 UPnP，不做 STUN，也不监听，因此没有任何东西等待主动发来的数据包，Windows 防火墙不会询问（此时 NAT 类型显示为“未知”）；节点仍会连接正在监听的对端（先用 TCP，属于出站连接），否则流量经由服务器中转。用“尝试直接连接”（`direct`，在“设置”的“运行方式”中）打开，助手中始终打开（见下文）：打洞、UPnP、监听与直接连接，优先 UDP（`default_protocol`） |
| 数据面白名单 | 只有 TCP 与 UDP 端口 47310；网络中发往其他端口的流量一律丢弃 |
| 运行时 | 核心持有的双线程 Tokio 运行时 |

引擎运行且已配置网络时节点运行；设置变化时节点重启。

## 设置

用户配置一个网络：最多 8 个服务器 URL（`tcp`、`udp`、`ws`、`wss`、`quic`、`wg`；`faketcp` 等其他协议会被拒绝）、
网络名称（最多 64 个字符）和网络密钥。整个条目（含密钥）保存在机密存储中（[security.md](security.md)），因此没有可用
的系统存储时只在本次运行中有效。短于 16 个字符的密钥会被标为弱密钥。清空服务器和网络名称即关闭节点。

## 直接连接

打洞会在所有地址上绑定一个 UDP 套接字（`0.0.0.0:port`，并做 UPnP 映射）等待对方的数据包；Windows 防火墙把这视为接受入站流量，
并询问是否允许该程序——标准用户无法批准这个提示（在测试虚拟机上出现过；本机因以前的回答已有规则）。用于为打洞判断 NAT 类型的 STUN 在启动时也会这样做（在所有地址上的两个短暂 UDP 套接字，在本机上观察到）。因此在免授权模式下，
节点启动时带有 `disable_udp_hole_punching`、`disable_tcp_hole_punching`、`disable_sym_hole_punching`、`disable_upnp` 以及空的
`stun_servers` 与 `stun_servers_v6`；之后它只保持到服务器的 TCP 连接，并连接正在监听的对端（先用 TCP，`default_protocol`；这是防火墙不会询问的出站连接），否则会话经由
服务器中转。它不设置 `disable_p2p`，因为那样也会拒绝正在监听的对端提出的直接连接：保持关闭的设备曾因此让监听中的局域网对端一直走
中转（2026-10-18）。“设置”中“运行方式”里的**尝试直接连接**（网络设置中的 `direct`，切换后立即生效，放在运行方式旁边是因为它让
这台电脑向网络开放）会打开打洞并让节点监听，并接受 Windows 可能询问。EasyTier 的直连器只能连上正在监听的对端：它会依次尝试对端的各个网卡地址和公网地址。只靠打洞时，需要共用的路由器把设备的
数据包转回它自己的网络，而很多路由器不会，所以同一局域网中的两台设备在开始监听之前一直经由服务器中转（2026-10-18 所见）。同一局域网中
只需一台设备打开直接连接：另一台会连接它的监听端口。在 macOS 上，本地网络中的第一次直接连接会询问 Kyokai 的“本地网络”权限
（助手是 root 守护进程，不会询问）。助手始终打洞并监听：它的安装程序为它自己的 `kyokai-svc.exe` 添加一条入站允许规则
（`Kyokai helper`），卸载时一并删除（[run-modes.md](run-modes.md)）。

## 状态

引擎在被询问时读取节点状态，从不使用缓存：

| 字段 | 含义 |
|---|---|
| `state` | `off`、`connecting`、`online`（与服务器或对端的连接已建立）或 `error` |
| `error` | 节点无法启动的原因 |
| `address` | 分配到之后的虚拟 IPv4 地址 |
| `nat` | 通过 STUN 得到的 NAT 类型：`unknown`、`open`、`no-pat`、`full-cone`、`restricted`、`port-restricted`、`symmetric`、`symmetric-easy`、`symmetric-firewall` |
| `devices` | 网络可路由到的其他 Kyokai 设备：其宣告的设备 ID、`direct` 或 `relay` 路径、以毫秒计的延迟 |

连接到本地 API 的会话在状态变化时收到新状态（每 2 秒重新读取一次）。

## 信任

EasyTier 是不可信的通道。网络中的任何成员都可以宣告任意实例 ID 和主机名，两个节点也可以宣告同一个（两者都保持可
路由），所以 `devices` 中的设备 ID 只是寻找设备的线索，从不证明对方身份。每个 Kyokai 会话都在数据面之上做端到端
认证。EasyTier 自身的加密由网络中所有成员共享同一把密钥。

## 构建

核心只启用 EasyTier 的 `smoltcp`、`aes-gcm`、`dhcp-ipv4`、`dns-resolver`、`websocket`、`quic` 和 `wireguard`
特性。`dns-resolver` 用于解析 STUN 服务器，NAT 探测和打洞都需要它。EasyTier 的 `thunk-rs` 构建步骤（支持 Windows 7、
未经校验的下载、改为链接 `msvcrt.dll`）被一个空操作 crate 取代，因此所有 Windows 构建都保持静态 C 运行时；构建使用
固定版本并校验过的 `protoc`，而不是 EasyTier 自行下载的版本（[development.md](development.md)）。
