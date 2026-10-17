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
| 随界面 | 安装一次特权助手 | 界面运行期间在助手中 | 未实现 |
| 后台服务 | 同一个助手 | 开机即运行 | 未实现 |

界面启动时从不请求管理员权限。

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
| `core/kyokai-core` | Rust 核心库：C ABI、机密存储、设备身份、密码与 OPAQUE（`auth`）、访问决定（`access`）、会话（`session`）、EasyTier 节点（`net`） |
| `core/third_party/thunk-noop` | 替代 EasyTier 的 `thunk-rs` 构建步骤，使 Windows 构建保持静态 C 运行时 |

## FFI

核心是一个 C ABI 库（`kyokai_core.dll`、`libkyokai_core.dylib`），以 `crate-type = ["cdylib", "rlib"]` 构建，通过 purego
加载（`CGO_ENABLED=0`）。导出函数只收发整数与指针：

| 导出 | 作用 |
|---|---|
| `kyokai_abi_version() -> u32` | ABI 版本（1）；不一致时宿主拒绝加载 |
| `kyokai_start(json, len, *out, *out_len) -> i32` | 以 `{version, state_dir, profile}` 启动核心 |
| `kyokai_call(json, len, *out, *out_len) -> i32` | 应答 `{method, params}` |
| `kyokai_free(ptr, len)` | 释放响应缓冲区 |
| `kyokai_stop()` | 丢弃核心的状态，包括内存中的机密 |

响应是核心分配的缓冲区中的 `{"ok": true, "result": …}` 或 `{"ok": false, "error": "…"}`；panic 在边界处捕获并作为错误返回。
方法：`status`（从不含机密；包含网络状态）、`password.reveal`、`password.regenerate`、`network.get`（不含密钥的设置）、
`network.set`（`{servers, name, secret?}`；缺少密钥时保留已存的密钥）、`password.set_permanent`（`{password}`，null 表示移除）、
`session.connect`（`{device_id, password, want}`；立即返回会话 id，进度见状态）、`session.close`、`request.decide`
（`{id, allow, permissions}`）、`trusted.list`、`trusted.remove`、`host.forget`。状态中还包含 `permanent_password`、`requests`
（待处理的被控端确认）和 `sessions`。Go 绑定把传给核心的响应槽放在堆内存中，从不放在
goroutine 栈上，因为栈可能在取地址与调用之间移动。Windows 上以绝对路径、
`LoadLibraryEx(LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR | LOAD_LIBRARY_SEARCH_SYSTEM32)` 加载该库，并静态链接 C 运行时，
因此不会经由搜索路径解析任何文件。

## 本地 API

- 传输：AF_UNIX 套接字（Windows 上也是），位于 `%TEMP%\kyokai\engine.sock` 或 `$TMPDIR/kyokai-<uid>/engine.sock`
  （开发配置为 `engine-<profile>.sock`；`KYOKAI_SOCKET` 可覆盖）；以换行分隔的 JSON，带 `version` 字段（1）。每个连接一个
  请求，`session.attach` 除外：它保持连接，状态变化时收到推送。
- 请求：`status`（含网络状态）、`password.reveal`、`password.regenerate`、`network.get`、`network.set`（被拒绝的设置
  以 `invalid` 和原因答复）、`password.set_permanent`、`session.connect`、`session.close`、`request.decide`、`trusted.list`、
  `trusted.remove`、`host.forget`、`session.attach`、`shutdown`。状态包含待处理的被控端确认与会话；机密只朝引擎方向传递
  （网络密钥、固定密码、用户输入的密码）。会话在每个改变状态的命令之后、以及网络状态变化时收到状态。
- 授权来自操作系统的对端凭据（macOS 为 `LOCAL_PEERCRED`；Windows 为 `SIO_AF_UNIX_GETPEERPID` 与进程令牌的 SID），
  从不取自请求内容：只服务引擎自己的用户；读不出身份的连接一律拒绝。

## 状态

| 路径 | 内容 |
|---|---|
| `%APPDATA%\Kyokai\` / `~/Library/Application Support/Kyokai/` | 状态目录（开发配置为 `Kyokai-<profile>`） |
| `secrets.bin` | 加密的机密存储（[security.md](security.md)） |
| `master.key`（Windows） | 存储密钥的 DPAPI 数据块；macOS 上密钥在登录钥匙串中 |
| `gui.json` | 界面偏好设置 |
| `logs/engine.log` | 引擎日志，不含机密 |

## 通用规则

- **机密从不外传**（[security.md](security.md)）。
- **绝不把陈旧数据显示为当前数据。** 只有引擎会话连接时界面才显示引擎状态，否则显示“正在启动”或引擎未运行的原因。
- **带版本的边界。** ABI 与本地 API 都带版本；枚举以名称传递。
