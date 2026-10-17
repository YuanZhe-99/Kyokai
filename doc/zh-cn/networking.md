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
| 监听 | 无：只有出站连接和打洞，因此不会出现防火墙或本地网络提示 |
| 数据面白名单 | 只有 TCP 与 UDP 端口 47310；网络中发往其他端口的流量一律丢弃 |
| 运行时 | 核心持有的双线程 Tokio 运行时 |

引擎运行且已配置网络时节点运行；设置变化时节点重启。

## 设置

用户配置一个网络：最多 8 个服务器 URL（`tcp`、`udp`、`ws`、`wss`、`quic`、`wg`；`faketcp` 等其他协议会被拒绝）、
网络名称（最多 64 个字符）和网络密钥。整个条目（含密钥）保存在机密存储中（[security.md](security.md)），因此没有可用
的系统存储时只在本次运行中有效。短于 16 个字符的密钥会被标为弱密钥。清空服务器和网络名称即关闭节点。

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
