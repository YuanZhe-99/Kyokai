# 函数索引

每个 Go 包与 Rust crate 一行：职责，以及应该先读的声明。声明在文档注释中带有函数说明层（第一句写用途；非平凡的声明再写
输入、返回、副作用与注意事项）。

| 包 / crate | 职责 | 从这里读起 |
|---|---|---|
| `cmd/kyokai` | 界面入口 | `main` |
| `cmd/kyokai-svc` | 引擎入口 | `run`、`ctl` |
| `internal/gui` | 界面状态、视图、托盘、更新、引擎子进程 | `App`、`App.View`、`App.connectLoop`、`engineChild.ensure` |
| `internal/engine` | 引擎宿主 | `Run`、`Options`、`DefaultStateDir` |
| `internal/localapi` | 本地 API 协议、服务端、客户端、授权 | `ProtocolVersion`、`Request`、`Status`、`NetworkSettings`、`NetworkStatus`、`Client.Attach`、`Authorizer.Allows` |
| `internal/core` | 通过 purego 绑定 Rust 核心 | `Load`、`Lib.Start`、`Lib.Call` |
| `internal/version` | 产品版本 | `Version`、`Older` |
| `internal/fsutil` | 原子写入与 JSON 文档 | `WriteFileAtomic`、`WriteJSON`、`ReadJSON` |
| `core/kyokai-core` | Rust 核心：C ABI、机密、身份、临时密码、EasyTier 节点 | `kyokai_start`、`kyokai_call`、`store::open`、`Identity::load_or_create`、`device_id`、`net::Network` |
| `core/third_party/thunk-noop` | EasyTier `thunk-rs` 的空操作替代 | `thunk` |
| `tools/deps` | 上游版本固定 | `check`、`update` |
| `tools/checkdocs` | 文档镜像检查 | `main` |
| `tools/sign` | 特权助手 bundle 签名密钥 | `keygen` |
| `tools/icon` | 绘制应用图标与托盘图形 | `app`、`tray`、`render` |
