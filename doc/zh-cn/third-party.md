# 第三方代码

Kyokai 采用 AGPL-3.0-only 许可。本清单列出它复用或内嵌的每个上游、用到了什么以及由此产生的义务。
声明文本本身在 `THIRD_PARTY_NOTICES.md` 中。

## 清单

| 组件 | 仓库与版本 | 用途 | 许可证 | 修改 | 义务 |
|---|---|---|---|---|---|
| MyGo | `egoist/mygo` v0.5.5 | 界面框架、打包、更新器 | MIT | 无 | 保留版权声明；其内嵌的 Kage 编译器与 MingCute 图标为 Apache-2.0 |
| Easytier-MyGo 的文件 | `YuanZhe-99/Easytier-MyGo` | `tools/checkdocs`、`internal/fsutil`、`internal/version` 的写法、MyGo 维护技能、工作规范、厂商标志匹配（`internal/gui/vendorlogo.go`）、助手（`internal/helper`）与 bundle 签名（`internal/bundlesig`） | GPL-3.0，由其作者以 AGPL-3.0 重新授权 | 调整了名称与注释；匹配改为读取设备的说明而不是主机名 | 只需遵守 Kyokai 自身的许可证 |
| MyApps-AI | `YuanZhe-99/MyApps-AI`（同一作者） | 模型命名规则（`internal/gui/modelname.go`，由 `model_naming.dart` 及其测试用例移植）与服务商模板的地址（`internal/gui/aisources.go`） | GPL-3.0，由其作者以 AGPL-3.0 重新授权 | 移植为 Go；不支持图片与工具的模板未采用 | 只需遵守 Kyokai 自身的许可证 |
| 厂商标志 | Simple Icons 与 Wikimedia Commons，经由 EasyTier MyGo | 设备头像（`internal/gui/assets/logos/`） | CC0 1.0（18 个文件）、公有领域（1 个）；均为其所有者的商标 | 无 | 保留 `SOURCES.md`；只作指名性使用，所有者要求时移除 |
| EasyTier | `EasyTier/EasyTier` v2.7.0，提交 `8b7f1f0` | P2P 网络，内嵌于引擎的核心库 | LGPL-3.0 | 无；其构建依赖 `thunk-rs` 被一个空操作 crate 取代 | 随附 LGPL 与 GPL 文本及源码位置；Kyokai 自身源码公开，用户可以重新链接 |
| arboard | `1Password/arboard` 3.7.0 | 剪贴板（文本与图片，借助 `image` crate） | MIT OR Apache-2.0；其 Windows 依赖 `clipboard-win` 与 `error-code` 为 BSL-1.0 | 无 | 其声明随 `RUST_LICENSES.txt` 提供 |
| libvpx | `webmproject/libvpx` 1.16.0，由 vcpkg 构建（`microsoft/vcpkg` 2026.07.29，提交 `9e593bb`，启用其 `realtime` 特性） | 软件 VP8 与 VP9，静态链接进核心 | BSD-3-Clause，附 Google 的额外专利授权 | 无 | 随附 `libvpx-LICENSE.txt` 与 `libvpx-PATENTS.txt`（`third_party/licenses`） |
| Opus | `xiph/opus` 1.5.2，由 vcpkg 构建（同一提交） | 被控端的声音（48 kHz 立体声，20 毫秒一包），静态链接进核心 | BSD-3-Clause | 无（vcpkg 移植） | 随附 `opus-LICENSE.txt`（`third_party/licenses`）；Opus 另有其贡献者提供的免版税专利许可 |
| libaom | `aomedia/aom` 3.13.3，由 vcpkg 构建（同一提交） | 软件 AV1，静态链接进核心 | BSD-2-Clause，附 Alliance for Open Media Patent License 1.0 | 无（vcpkg 移植自带的补丁） | 随附 `libaom-LICENSE.txt` 与 `libaom-PATENTS.txt`（`third_party/licenses`）；对就 AV1 提出专利主张的被许可人，专利许可终止 |
| RustDesk | `rustdesk/rustdesk` 1.5.0，提交 `fada664` | Windows 屏幕采集：`core/third_party/rustdesk-dxgi` 中的 `libs/scrap/src/dxgi/{mod,gdi,hdr}.rs`（去掉 Magnification 模块，日志改用 `log`，scrap 的类型与 libyuv 的旋转放在一个 shim 中，停用一个上游测试）；视频 QoS：`core/third_party/rustdesk-qos` 中的 `src/server/video_qos.rs` 及其测试（它所需的 server 模块与 scrap 内容放在一个 shim 中，其余未改）。软件编解码器：`core/third_party/rustdesk-codec` 中的 `libs/scrap/src/common/{vpxcodec,vpx,aom}.rs` 及绑定头文件 `libs/scrap/src/bindings/{vpx,aom}_ffi.h`（与 `hbb_common` 和 RustDesk 的 protobuf 消息解耦，绑定预先生成，增加强制关键帧与码率设置，libvpx 不做前瞻，去掉 libyuv 转换；`common/mod.rs` 与 `codec.rs` 中的错误类型、调用宏与画面类型放在 crate 根中）。只参照设计、未复制代码的部分：由 SYSTEM 服务在控制台会话中启动的会话代理、用 `SendSAS` 发送 Ctrl+Alt+Del、以不参与采集的窗口实现隐私模式、GDI 回退的用法以及会话操作；键盘模式随后加入 | AGPL-3.0 | 每个复用文件保留原文件头，并加上 “Modified by Kyokai” 说明 | 提供对应源码，远程用户也包括在内 |

## 绝不使用

`rustdesk/hbb_common` 与 `rustdesk-org/hwcodec` 没有许可授权，RustDesk 的 FFmpeg 构建使用了 `--enable-gpl`，
RustDesk 的会合（hbbs）与中继（hbbr）服务器也不在 Kyokai 的设计之内。
