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
“关于”页链接到源代码。安装包还带有 `licenses/RUST_LICENSES.txt`——核心中每个 Rust crate 及其许可证文本，由
`cargo about` 根据 `core/about.toml` 与 `core/about.hbs` 生成——以及 EasyTier 的 LGPL-3.0 要求随附的
`licenses/GPL-3.0.txt`。缺少 `cargo-about` 时构建会停止。

## 厂商标志

设备头像中的厂商标志（[gui.md](gui.md) 的“设备名称”）是 `internal/gui/assets/logos/` 中的 SVG 文件，在构建时嵌入界面；应用从不下载它们。
它们取自 EasyTier MyGo 的标志集：18 个 Simple Icons 文件（CC0 1.0）和 1 个公有领域文件，每个文件的来源、许可证与商标所有者都列在
`SOURCES.md` 中；没有记录来源的文件没有采用。添加标志时，取一个 CC0 或公有领域的 SVG，保存为 `<slug>.svg`，在 `SOURCES.md` 中加一行，
在 `internal/gui/vendorlogo.go` 的 `vendorKeywords` 中加入它的单词，并在 `TestDeviceLogoName` 中加一个用例；文件无法解析或某个关键词的标志缺失时，
`TestVendorLogosParse` 会失败。
