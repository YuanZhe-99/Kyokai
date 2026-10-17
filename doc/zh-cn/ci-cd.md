# CI/CD

工作流位于 `.github/workflows`，在 GitHub 上运行。私有远端没有 runner，推送到那里只由本地检查把关。

| 工作流 | 触发 | 作用 |
|---|---|---|
| `ci.yml` → checks | 推送到 `main`、拉取请求、手动 | gofmt、Windows 与 macOS 的 `go vet`、界面目录、文档镜像、`cargo fmt`、`cargo deny`、机密扫描（gitleaks）、上游版本（仅提示） |
| `ci.yml` → test | 同上 | `windows-11-arm` 与 `macos-latest`：clippy、Rust 测试、使用真实核心的 Go 测试（ABI 冒烟测试）、`mygo vet`、`scripts/smoke.sh user`、未签名安装包作为产物；`windows-latest` 与 `macos-15-intel`：构建核心与 Go 测试，`windows-latest` 还会上传未签名的 x64 安装包（安装在远程测试虚拟机上）。所有任务都使用固定版本的 `protoc`（`scripts/protoc.sh`），并通过 `.github/actions/codecs` 构建 libvpx 与 libaom（在固定提交克隆并引导 vcpkg，用 `actions/cache` 按 `core/vcpkg*.json` 与三元组保留其二进制缓存，macOS 上从 Homebrew 安装 NASM，然后运行 `scripts/codecs.sh`）；`windows-11-arm` 把 LLVM 的 clang 加入 `PATH` 供 `ring` 使用；macOS 任务使用各自已解锁的默认钥匙串 |
| `release.yml` | 标签 `v*`、手动 | Windows 核心在 `windows-latest`（x64）与 `windows-11-arm` 上构建，各自带上编解码库（`.github/actions/codecs`），在 Ubuntu 上用 NSIS 打包；macOS 在 `macos-latest` 上构建打包，两种架构的编解码库都在那里构建（x64 交叉构建）；标签会在核对与 `internal/version` 一致后附带 `SHA256SUMS.txt` 发布 |
| `upstream.yml` | 每周、手动 | `go run ./tools/deps check -strict`；MyGo、EasyTier、RustDesk、vcpkg 或 Rust 有更新时开 issue |
| `dependabot.yml` | 每周 | Actions、Go 模块（不含 MyGo）与 Cargo |

## Secrets

| Secret | 使用方 | 未设置时 |
|---|---|---|
| `MYGO_UPDATER_PRIVATE_KEY` | `release.yml`、`mygo build` | 不发布签名更新；已安装的应用停留在原版本 |
| `KYOKAI_SIGNING_KEY` | `release.yml`（Windows）、`scripts/build.sh` | 助手的文件未签名；已安装的助手只能通过管理员提示更新 |
| `KYOKAI_MACOS_CERT_P12`、`KYOKAI_MACOS_CERT_PASSWORD` | `release.yml`（macOS） | macOS 版本为 ad-hoc 签名，隐私授权无法跨更新保留；发布说明必须写明 |

私钥只存在于这些 secrets 和维护者的离线副本中；更新公钥是 `mygo.json` 的 `updates.publicKey`。只用
`go tool mygo keygen -o <新目录>` 生成 MyGo 密钥对：默认目录为所有 MyGo 项目共用，`-force` 会替换其他应用的密钥。
特权助手的 bundle 密钥（`go run ./tools/sign keygen`）为助手的文件签名（`go run ./tools/sign bundle`）；其公钥是 `internal/bundlesig/release.pub`。

## 上游更新流程

每次变更都从 `go run ./tools/deps check` 开始。上游有更新时：

1. MyGo：`go run ./tools/deps update`，然后 `go tool mygo migrate-ui .`（预览）、`-write`，并手工检查标识与绑定值的变化。
2. EasyTier：一起更新 `third_party/upstream.json`（标签与提交）和 `core/kyokai-core/Cargo.toml` 中的两个 Cargo 固定版本；
   检查 `thunk-noop` 是否仍覆盖 EasyTier 所需的 `thunk-rs` 特性；删除更新后不再需要的 `core/deny.toml` 安全公告忽略项；
   然后让两个引擎经标准服务器运行（`ctl network-set`，`ctl status` 显示两者都在线并互相可见）。
3. RustDesk：移动固定版本之前，审阅每个复用文件的上游差异。
4. vcpkg：一起移动 `third_party/upstream.json`（标签与提交）和 `core/vcpkg-configuration.json` 中的 baseline
   （`tools/deps` 会检查两者一致）；运行 `scripts/codecs.sh`，重新生成绑定（[development.md](development.md)），审阅
   libvpx 与 libaom 的发布说明和许可证文件（`third_party/licenses`），并运行编解码往返测试（`cargo test -p kyokai-core soft`）。
5. Rust：修改 `rust-toolchain.toml` 与工作流中的工具链。
6. 运行完整检查，并在 [version-history.md](version-history.md) 记录“MyGo vX / EasyTier vY / RustDesk Z / vcpkg V / Rust W
   （YYYY-MM-DD 检查）”，没有变化时也要记录。
