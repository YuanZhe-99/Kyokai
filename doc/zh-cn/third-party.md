# 第三方代码

Kyokai 采用 AGPL-3.0-only 许可。本清单列出它复用或内嵌的每个上游、用到了什么以及由此产生的义务。
声明文本本身在 `THIRD_PARTY_NOTICES.md` 中。

## 清单

| 组件 | 仓库与版本 | 用途 | 许可证 | 修改 | 义务 |
|---|---|---|---|---|---|
| MyGo | `egoist/mygo` v0.5.5 | 界面框架、打包、更新器 | MIT | 无 | 保留版权声明；其内嵌的 Kage 编译器与 MingCute 图标为 Apache-2.0 |
| Easytier-MyGo 的文件 | `YuanZhe-99/Easytier-MyGo` | `tools/checkdocs`、`internal/fsutil`、`internal/version` 的写法、MyGo 维护技能、工作规范 | GPL-3.0，由其作者以 AGPL-3.0 重新授权 | 调整了名称与注释 | 只需遵守 Kyokai 自身的许可证 |
| EasyTier | `EasyTier/EasyTier` v2.7.0，提交 `8b7f1f0` | P2P 网络，内嵌于引擎的核心库 | LGPL-3.0 | 无；其构建依赖 `thunk-rs` 被一个空操作 crate 取代 | 随附 LGPL 与 GPL 文本及源码位置；Kyokai 自身源码公开，用户可以重新链接 |
| arboard | `1Password/arboard` 3.7.0 | 剪贴板（文本与图片，借助 `image` crate） | MIT OR Apache-2.0；其 Windows 依赖 `clipboard-win` 与 `error-code` 为 BSL-1.0 | 无 | 其声明随 `RUST_LICENSES.txt` 提供 |
| RustDesk | `rustdesk/rustdesk` 1.5.0，提交 `fada664` | Windows 屏幕采集：`core/third_party/rustdesk-dxgi` 中的 `libs/scrap/src/dxgi/{mod,gdi,hdr}.rs`（去掉 Magnification 模块，日志改用 `log`，scrap 的类型与 libyuv 的旋转放在一个 shim 中，停用一个上游测试）；视频 QoS：`core/third_party/rustdesk-qos` 中的 `src/server/video_qos.rs` 及其测试（它所需的 server 模块与 scrap 内容放在一个 shim 中，其余未改）；输入逻辑与特权助手代码随后加入 | AGPL-3.0 | 每个复用文件保留原文件头，并加上 “Modified by Kyokai” 说明 | 提供对应源码，远程用户也包括在内 |

## 绝不使用

`rustdesk/hbb_common` 与 `rustdesk-org/hwcodec` 没有许可授权，RustDesk 的 FFmpeg 构建使用了 `--enable-gpl`，
RustDesk 的会合（hbbs）与中继（hbbr）服务器也不在 Kyokai 的设计之内。
