# 翻译指南

## 1. 范围

`doc/en-us/` 为准。`doc/zh-cn/` 镜像它；界面字符串在 ARB 目录中（英文、简体中文、繁体中文、日文）。

## 2. 结构一致

两棵目录包含相同的文件，标题数、表格行数与代码块数都相同；代码块逐字节一致。`go run ./tools/checkdocs` 负责检查。

## 3. 永不翻译

代码、命令、路径、配置键、标识符、版本号、权限名（`VIEW_SCREEN` 等）、产品与公司名（Kyokai、EasyTier、MyGo、RustDesk、
OpenAI、Claude、Anthropic）以及协议名（Noise、OPAQUE）。

## 4. 风格

句子平实简短。中文使用全角标点，中英文之间只在有助阅读时加空格。

## 5. 术语表

| English | 简体中文 |
|---|---|
| Computer Use | 电脑操作（Computer Use） |
| controller | 控制端 |
| host | 被控端 |
| Device ID | 设备 ID |
| temporary password | 临时密码 |
| permanent password | 固定密码 |
| trusted device | 受信任设备 |
| permission | 权限 |
| approval | 批准 |
| take over | 接管 |
| emergency stop | 紧急停止 |
| AI task | AI 任务 |
| provider | 服务商 |
| unattended access | 无人值守访问 |
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
| pinned key | 固定的密钥 |
| rate limit | 速率限制 |
| local API | 本地 API |
| lifeline | 生命线 |
| state directory | 状态目录 |
| secrets store | 机密存储 |
| spike | 可行性验证（spike） |

## 6. 审阅清单

- 两棵目录的文件、标题、表格与代码块相同。
- 术语用法一致。
- 链接在两棵目录中指向同一份文档。
