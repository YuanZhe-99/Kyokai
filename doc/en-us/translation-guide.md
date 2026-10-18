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

## 6. Review checklist

- Same files, headings, tables and code blocks in both trees.
- Glossary terms used consistently.
- Links point to the same documents in each tree.
