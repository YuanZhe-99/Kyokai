# Translation guide

## 1. Scope

`doc/en-us/` is authoritative. `doc/zh-cn/` mirrors it; interface strings live in the ARB
catalogs (English, Simplified Chinese, Traditional Chinese, Japanese).

## 2. Structural parity

Both trees contain the same files with the same number of headings, table rows and code blocks;
code blocks are byte-identical. `go run ./tools/checkdocs` enforces this.

## 3. Never translated

Code, commands, paths, config keys, identifiers, version numbers, permission names
(`VIEW_SCREEN`, …), product and company names (Kyokai, EasyTier, MyGo, RustDesk, OpenAI, Claude,
Anthropic) and protocol names (Noise, OPAQUE).

## 4. Style

Plain, short sentences. Chinese text uses full-width punctuation, with a space between Chinese
and Latin words only where it helps readability.

## 5. Glossary

| English | 简体中文 |
|---|---|
| Computer Use | 电脑操作（Computer Use） |
| controller | 控制端 |
| host | 被控端 |
| Device ID | 设备 ID |
| temporary password | 临时密码 |
| permanent password | 固定密码 |
| trusted device | 受信任设备 |
| friendly name | 别名 |
| No privileges / Follow Kyokai / Background service (run modes) | 免授权 / 随界面 / 后台服务 |
| vendor logo | 厂商标志 |
| permission | 权限 |
| approval | 批准 |
| take over | 接管 |
| Screen Recording / Accessibility / Input Monitoring (macOS) | 屏幕录制 / 辅助功能 / 输入监控 |
| emergency stop | 紧急停止 |
| input lease | 输入租约 |
| AI task | AI 任务 |
| (AI) source | 来源 |
| OpenAI-compatible | OpenAI 兼容 |
| provider | 服务商 |
| unattended access | 无人值守访问 |
| Accepted passwords / Connect without asking | 接受的密码 / 无需确认即可连接 |
| connection (to a device) | 连接 |
| engine | 引擎 |
| privileged helper | 特权助手 |
| run mode | 运行方式 |
| No privileges | 免授权 |
| Follow GUI | 随界面 |
| Background service | 后台服务 |
| operator | 操作者（operator） |
| network name / network secret | 网络名称 / 网络密钥 |
| server address | 服务器地址 |
| EasyTier node | EasyTier 节点 |
| data plane | 数据面 |
| hole punching | 打洞 |
| relay | 中继 |
| session | 会话 |
| host confirmation | 被控端确认 |
| admission | 准入 |
| password grants | 密码授予的权限 |
| pinned key | 固定的密钥 |
| rate limit | 速率限制 |
| Copy files (the `files` permission) | 复制文件 |
| temporary folder | 临时文件夹 |
| impersonate (a user) | 模拟（用户身份） |
| local API | 本地 API |
| lifeline | 生命线 |
| state directory | 状态目录 |
| secrets store | 机密存储 |
| keyboard mode (Map / Translate / Legacy) | 键盘模式（映射 / 翻译 / 传统） |
| scan code | 扫描码 |
| spike | 可行性验证（spike） |
| software codec | 软件编解码器 |
| full colour (4:4:4) | 全彩（4:4:4） |
| key frame | 关键帧 |
| tile (HEVC picture partition) | 图块 |
| patent grant | 专利授权 |
| codec choice / codec offer | 编码选择 / 编码清单 |
| frame acknowledgement | 帧确认 |
| Swap Ctrl and Cmd | 交换 Ctrl 与 Cmd |
| Sound (the host's, in the viewer) | 声音 |
| Hear sound (permission) / Sound to share | 听到声音 / 共享的声音 |
| jitter buffer | 抖动缓冲 |
| Connect by address / Accept connections by address / Accepted from | 按地址连接 / 接受地址直接连接 / 接受来自 |
| Networks / Add a network / Join (a network) / Not joined | 网络 / 添加网络 / 加入 / 未加入 |
| Name shown here (a network's) | 在此显示的名称 |
| No logs | 完全无日志 |
| Protected mode | 保护模式 |
| Rename… (a name shown only on this device) | 重命名…（只在本机显示的名称） |
| event tap (macOS) | 事件监听 |
| in hardware / in software (an encoder, a decoder) | 硬件 / 软件 |
| direct (path) | 直连 |
| listener | 监听 |
| parameter set (H.264, HEVC) | 参数集 |
| media engine (Apple silicon) | 媒体引擎 |
| General / Permissions / Security / About (pages) | 通用 / 权限 / 安全 / 关于 |
| Fit / Original size (the viewer) | 适应窗口 / 原始大小 |
| clipboard history / Share the clipboard with this device | 剪贴板历史 / 与这台设备共享剪贴板 |
| (the tab) in front | （标签页）位于前台 |
| tab strip (the title bar) | 标签栏（即标题栏） |
| toolbar (the viewer's) / Hide controls / Show controls | 控制栏 / 收起控制栏 / 展开控制栏 |
| grip / handle (the folded toolbar) | 拖动柄 / 把手 |
| feature token (protocol) | 标记 |
| sign-in key / keys that can connect | 登录密钥 / 可连接的密钥 |
| saved passwords and keys | 已保存的密码与密钥 |
| tunnel (to a service) | 隧道 |
| status watcher / see this computer's status | 状态查看者 / 查看本机状态 |
| Remote Desktop (RDP) / Screen Sharing (VNC) | 远程桌面（RDP） / 屏幕共享（VNC） |
| input paused | 输入已暂停 |
| input method / composition (IME) | 输入法 / 输入组合 |
| keyboard grab / the grab steps aside | 键盘抓取 / 抓取让开 |
| wake (a display) | 唤醒（显示器） |
| loading indicator | 加载指示器 |
| proof (with a sign-in key) | 证明 |
| sample (of a host's status) / lease (on watching it) | 采样 / 租约 |
| saved sign-in (of a service) | 保存的登录信息 |
| Your keys / Generate a key / Import a key / Generate a pair | 你的密钥 / 生成密钥 / 导入密钥 / 生成密钥对 |
| passphrase (of a key) | 口令 |
| service tab / Sign in / Trust the new key | 服务标签页 / 登录 / 信任新密钥 |
| live status / sparkline / Getting its status… | 实时状态 / 走势线 / 正在获取状态… |
| watch lease (Home's status watch) | 查看租约 |
| dot history / status details | 点阵历史 / 状态详情 |
| Disconnect and close / Disconnect and quit | 断开并关闭 / 断开并退出 |
| route (to a device) / Addresses… | 路径 / 地址… |
| Auto (route choice) | 自动 |
| happy eyeballs | 快速回退并行连接（happy eyeballs） |

## 6. Review checklist

- Same files, headings, tables and code blocks in both trees.
- Glossary terms used consistently.
- Links point to the same documents in each tree.
