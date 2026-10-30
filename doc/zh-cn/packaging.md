# 打包

`scripts/build.sh [goos/goarch …]` 为每个目标构建 libvpx、libaom 与 Opus（`scripts/codecs.sh`，静态链接进核心），
再把 Rust 核心（`cargo build --release`）与引擎（`kyokai-svc`，`CGO_ENABLED=0`）构建到
`resources/<goos>-<goarch>/bin`，把 `LICENSE`、`THIRD_PARTY_NOTICES.md` 与 `third_party/licenses` 复制到
`resources/licenses`，再运行 `go tool mygo build`。Windows 目标需要 Windows 主机（核心用 MSVC 链接；ARM64 机器也能构建
x64）；macOS 目标需要 Mac。`CORE_PREBUILT=1` 使用已放入 resources 的核心库（发布工作流），`MYGO_SIGN` 把签名身份传给
`mygo build -sign`。

## 产物

| 平台 | 文件 |
|---|---|
| Windows（x64、ARM64） | `Kyokai.exe` 与 `bin/kyokai-svc.exe`、`bin/kyokai_core.dll`、`licenses/`；按用户安装的 NSIS 安装包（`%LOCALAPPDATA%\Programs\Kyokai`，无需管理员权限）；便携 zip |
| macOS（Apple 芯片、Intel） | `Kyokai.app`，其中有 `Contents/Resources/bin/kyokai-svc`（应用的引擎）与 `bin/Kyokai Helper.app`（核心库与助手自己的引擎，[run-modes.md](run-modes.md)），以及 `.dmg` |
| 两者 | 设置 `MYGO_UPDATER_PRIVATE_KEY` 时：签名的更新归档、增量包与 `update-<target>.json` |

`resources/<goos>-<goarch>/` 与 `resources/licenses/` 下的构建产物不提交。

## 签名

- **更新：** Ed25519（MyGo 更新器）；归档覆盖整个应用，包括核心库。
- **macOS 代码签名：** 发布版本使用维护者的 Apple Development 证书（免费的 Personal Team，因此带 Team ID），在 runner 上导入临时钥匙串；工作流按该身份的 SHA-1 签名（在本地由 `MYGO_SIGN` 指定它）。有 Team ID 时，屏幕录制、辅助功能与输入监控的授权以及钥匙串的“始终允许”都会跨构建与更新保留（已在 macOS 27 上确认）。应用未经公证，因此首次启动时 Gatekeeper 仍会要求“仍要打开”。证书有效期一年：在 Xcode 中续期，导出 `.p12` 并替换两个密钥。`packaging/macos/app.entitlements` 与
  `packaging/macos/engine.entitlements`（`bin/kyokai-svc` 与 `bin/Kyokai Helper.app` 的 `macos.helperEntitlements`）在 hardened runtime 下关闭库验证。没有证书时应用为
  ad-hoc 签名。
- **Windows：** 没有代码签名；首次启动时 SmartScreen 会警告。设置 `KYOKAI_SIGNING_KEY` 时，`bin` 文件夹中还有 `bundle.json` 与 `bundle.sig`，
  已安装的助手在更新自身之前会检查它们（[run-modes.md](run-modes.md) 的“更新”）。

## 许可证

Kyokai 采用 AGPL-3.0-only。每个安装包都带有 `licenses/LICENSE` 与 `licenses/THIRD_PARTY_NOTICES.md`，“关于”页面会显示它们；
“关于”页链接到源代码。安装包还带有 `licenses/RUST_LICENSES.txt`——核心中每个 Rust crate 及其许可证文本，由
`cargo about` 根据 `core/about.toml` 与 `core/about.hbs` 生成——以及 EasyTier 的 LGPL-3.0 要求随附的
`licenses/GPL-3.0.txt`，以及来自 `third_party/licenses` 的原生编解码库文本：`libvpx-LICENSE.txt`（BSD-3-Clause）与
`libvpx-PATENTS.txt`（Google 的专利授权），`libaom-LICENSE.txt`（BSD-2-Clause）与 `libaom-PATENTS.txt`（Alliance for
Open Media Patent License 1.0），以及 `opus-LICENSE.txt`（BSD-3-Clause），还有 `open-screen-sharing-LICENSE.txt`（复用的远程桌面与 VNC 客户端，GPL-3.0-or-later 及其名称条款）。`mygo build` 还会把 SSH 终端的模拟器 libghostty-vt（MyGo 的终端插件在
`mygo-plugin.json` 中列出其各平台构建及 SHA-256）放进应用的资源中。缺少 `cargo-about` 时构建会停止。

## 厂商标志

设备头像中的厂商标志（[gui.md](gui.md) 的“设备名称”）是 `internal/gui/assets/logos/` 中的 SVG 文件，在构建时嵌入界面；应用从不下载它们。
它们取自 EasyTier MyGo 的标志集，另加 Linux 的：19 个 Simple Icons 文件（CC0 1.0）和 1 个公有领域文件，每个文件的来源、许可证与商标所有者都列在
`SOURCES.md` 中；没有记录来源的文件没有采用。添加标志时，取一个 CC0 或公有领域的 SVG，保存为 `<slug>.svg`，在 `SOURCES.md` 中加一行，
在 `internal/gui/vendorlogo.go` 的 `vendorKeywords` 中加入它的单词，并在 `TestDeviceLogoName` 中加一个用例；文件无法解析或某个关键词的标志缺失时，
`TestVendorLogosParse` 会失败。
