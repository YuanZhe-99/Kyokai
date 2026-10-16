# CI/CD

工作流位于 `.github/workflows`，在 GitHub 上运行。私有远端没有 runner，推送到那里只由本地检查把关。

| 工作流 | 触发 | 作用 |
|---|---|---|
| `ci.yml` → checks | 推送到 `main`、拉取请求、手动 | gofmt、Windows 与 macOS 的 `go vet`、界面目录、文档镜像、`cargo fmt`、`cargo deny`、机密扫描（gitleaks）、上游版本（仅提示） |
| `ci.yml` → test | 同上 | `windows-11-arm` 与 `macos-latest`：clippy、Rust 测试、使用真实核心的 Go 测试（ABI 冒烟测试）、`mygo vet`、`scripts/smoke.sh user`、未签名安装包作为产物；`windows-latest` 与 `macos-15-intel`：构建核心与 Go 测试 |
| `release.yml` | 标签 `v*`、手动 | Windows 核心在 `windows-latest`（x64）与 `windows-11-arm` 上构建，在 Ubuntu 上用 NSIS 打包；macOS 在 `macos-latest` 上构建打包；标签会在核对与 `internal/version` 一致后附带 `SHA256SUMS.txt` 发布 |
| `upstream.yml` | 每周、手动 | `go run ./tools/deps check -strict`；MyGo、EasyTier、RustDesk 或 Rust 有更新时开 issue |
| `dependabot.yml` | 每周 | Actions、Go 模块（不含 MyGo）与 Cargo |

## Secrets

| Secret | 使用方 | 未设置时 |
|---|---|---|
| `MYGO_UPDATER_PRIVATE_KEY` | `release.yml`、`mygo build` | 不发布签名更新；已安装的应用停留在原版本 |
| `KYOKAI_MACOS_CERT_P12`、`KYOKAI_MACOS_CERT_PASSWORD` | `release.yml`（macOS） | macOS 版本为 ad-hoc 签名，隐私授权无法跨更新保留；发布说明必须写明 |

私钥只存在于这些 secrets 和维护者的离线副本中；更新公钥是 `mygo.json` 的 `updates.publicKey`。只用
`go tool mygo keygen -o <新目录>` 生成 MyGo 密钥对：默认目录为所有 MyGo 项目共用，`-force` 会替换其他应用的密钥。
特权助手的 bundle 密钥（`go run ./tools/sign keygen`）从特权助手阶段开始使用。

## 上游更新流程

每次变更都从 `go run ./tools/deps check` 开始。上游有更新时：

1. MyGo：`go run ./tools/deps update`，然后 `go tool mygo migrate-ui .`（预览）、`-write`，并手工检查标识与绑定值的变化。
2. EasyTier：一起更新 `third_party/upstream.json`（标签与提交）和 Cargo 固定版本，再运行 EasyTier 集成测试（引擎内嵌
   EasyTier 之后）。
3. RustDesk：移动固定版本之前，审阅每个复用文件的上游差异。
4. Rust：修改 `rust-toolchain.toml` 与工作流中的工具链。
5. 运行完整检查，并在 [version-history.md](version-history.md) 记录“MyGo vX / EasyTier vY / RustDesk Z / Rust W
   （YYYY-MM-DD 检查）”，没有变化时也要记录。
