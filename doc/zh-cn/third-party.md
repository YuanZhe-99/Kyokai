# 第三方代码

Kyokai 采用 AGPL-3.0-only 许可。本清单列出它复用或内嵌的每个上游、用到了什么以及由此产生的义务。
声明文本本身在 `THIRD_PARTY_NOTICES.md` 中。

## 清单

| 组件 | 仓库与版本 | 用途 | 许可证 | 修改 | 义务 |
|---|---|---|---|---|---|
| MyGo | `egoist/mygo` v0.5.5 | 界面框架、打包、更新器 | MIT | 无 | 保留版权声明；其内嵌的 Kage 编译器与 MingCute 图标为 Apache-2.0 |
| Easytier-MyGo 的文件 | `YuanZhe-99/Easytier-MyGo` | `tools/checkdocs`、`internal/fsutil`、`internal/version` 的写法、MyGo 维护技能、工作规范 | GPL-3.0，由其作者以 AGPL-3.0 重新授权 | 调整了名称与注释 | 只需遵守 Kyokai 自身的许可证 |
| EasyTier | `EasyTier/EasyTier` v2.7.0，提交 `8b7f1f0` | P2P 网络，内嵌于引擎（自 Phase 2 起） | LGPL-3.0 | 不打算修改 | 随附 LGPL 与 GPL 文本及源码位置；Kyokai 自身源码公开，用户可以重新链接 |
| RustDesk | `rustdesk/rustdesk` 1.5.0，提交 `fada664` | 复用的 Windows 采集、输入逻辑与特权助手代码（自 Phase 3 起） | AGPL-3.0 | 每个复用文件保留原文件头，并加上 “Modified by Kyokai” 说明 | 提供对应源码，远程用户也包括在内 |

## 绝不使用

`rustdesk/hbb_common` 与 `rustdesk-org/hwcodec` 没有许可授权，RustDesk 的 FFmpeg 构建使用了 `--enable-gpl`，
RustDesk 的会合（hbbs）与中继（hbbr）服务器也不在 Kyokai 的设计之内。
