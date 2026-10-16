# 打包

`scripts/build.sh [goos/goarch …]` 为每个目标把 Rust 核心（`cargo build --release`）与引擎（`kyokai-svc`，
`CGO_ENABLED=0`）构建到 `resources/<goos>-<goarch>/bin`，把 `LICENSE` 与 `THIRD_PARTY_NOTICES.md` 复制到
`resources/licenses`，再运行 `go tool mygo build`。Windows 目标需要 Windows 主机（核心用 MSVC 链接；ARM64 机器也能构建
x64）；macOS 目标需要 Mac。`CORE_PREBUILT=1` 使用已放入 resources 的核心库（发布工作流），`MYGO_SIGN` 把签名身份传给
`mygo build -sign`。

## 产物

| 平台 | 文件 |
|---|---|
| Windows（x64、ARM64） | `Kyokai.exe` 与 `bin/kyokai-svc.exe`、`bin/kyokai_core.dll`、`licenses/`；按用户安装的 NSIS 安装包（`%LOCALAPPDATA%\Programs\Kyokai`，无需管理员权限）；便携 zip |
| macOS（Apple 芯片、Intel） | `Kyokai.app`（引擎与核心在 `Contents/Resources/bin`）与 `.dmg` |
| 两者 | 设置 `MYGO_UPDATER_PRIVATE_KEY` 时：签名的更新归档、增量包与 `update-<target>.json` |

`resources/<goos>-<goarch>/` 与 `resources/licenses/` 下的构建产物不提交。

## 签名

- **更新：** Ed25519（MyGo 更新器）；归档覆盖整个应用，包括核心库。
- **macOS 代码签名：** 发布版本使用自签名证书 “Kyokai Code Signing”，导入临时钥匙串并在 runner 上设为可信的代码签名证书。
  稳定的签名身份让屏幕录制、辅助功能与钥匙串授权跨更新保留；首次启动时 Gatekeeper 仍会要求“仍要打开”。真实身份会开启
  hardened runtime，它会拒绝没有 Team ID 的核心库，所以 `packaging/macos/app.entitlements` 与
  `packaging/macos/engine.entitlements`（`bin/kyokai-svc` 的 `macos.helperEntitlements`）关闭库验证。没有证书时应用为
  ad-hoc 签名。
- **Windows：** 没有代码签名；首次启动时 SmartScreen 会警告。

## 许可证

Kyokai 采用 AGPL-3.0-only。每个安装包都带有 `licenses/LICENSE` 与 `licenses/THIRD_PARTY_NOTICES.md`，设置页会显示它们；
“关于”页链接到源代码。
