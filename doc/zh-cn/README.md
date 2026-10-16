# Kyokai 文档

**这些文档是代码的权威描述。** `doc/en-us/` 是源；`doc/zh-cn/` 逐文件镜像它。计划与未完成的工作不写在这里；
多步骤计划进行期间只存在于仓库的 `PLAN.md` 中。

Kyokai 是一款 AI 电脑操作（Computer Use）应用：人或 AI 代理通过内嵌的 EasyTier P2P 网络操作另一台电脑，
AI 任务在被操作的那台电脑上运行。支持 Windows（x64、ARM64）与 macOS（Apple 芯片、Intel）。

| 文档 | 内容 |
|---|---|
| [architecture.md](architecture.md) | 进程、运行方式、包与 crate、FFI、本地 API、状态 |
| [security.md](security.md) | 机密存储、设备身份、临时密码、日志 |
| [gui.md](gui.md) | 界面结构、措辞、托盘、语言 |
| [upstream.md](upstream.md) | 已核实的上游契约与特殊行为：MyGo、EasyTier、RustDesk、OpenAI、Claude |
| [third-party.md](third-party.md) | 复用的代码与依赖、许可证与义务 |
| [platform-notes.md](platform-notes.md) | 各平台已实现与已验证的内容 |
| [development.md](development.md) | 环境、命令、开发流程、可行性验证 |
| [ci-cd.md](ci-cd.md) | 工作流、secrets、上游更新流程 |
| [packaging.md](packaging.md) | 构建脚本、产物、签名、许可证 |
| [translation-guide.md](translation-guide.md) | 镜像规则与术语表 |
| [version-history.md](version-history.md) | 各版本及其固定的上游版本 |
| [functions/INDEX.md](functions/INDEX.md) | 各包与各 crate 的职责和关键声明 |

## 固定版本

| 组件 | 版本 | 依据 |
|---|---|---|
| Go | 1.27.2 | `go.mod` |
| MyGo | v0.5.5 | `go.mod` |
| Rust | 1.99.1 | `rust-toolchain.toml` |
| EasyTier（待内嵌） | v2.7.0，预发布版，提交 `8b7f1f0` | `third_party/upstream.json` |
| RustDesk（所复用代码的来源） | 1.5.0，提交 `fada664` | `third_party/upstream.json` |
