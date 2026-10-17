# 版本历史

## 0.1.0（2026-10-17）

首个发布版本，适用于 Windows（x64、ARM64）；macOS 随后提供。

- 连接：内嵌的 EasyTier 节点经标准服务器连接（未打开直接路径时经中继）、设备 ID 解析、带 OPAQUE 登录的 Noise XX（临时与固定密码）、
  被控端确认、受信任设备、无人值守授权、限速、会话恢复、身份备份、带厂商标志的友好名称。线路协议 4、FFI ABI 3、本地 API 2。
- 手动 Computer Use：DXGI 采集与 GDI 回退、多显示器、Media Foundation H.264 / HEVC / VP9（有硬件时使用硬件）、libvpx VP8 / VP9
  与 libaom AV1、全彩 4:4:4、RustDesk 的 QoS 与画质预设、带 RustDesk 键盘模式的输入、远程指针、剪贴板文本、图片与文件、
  会话操作（锁定、Ctrl+Alt+Del、输入阻止、隐私模式）。
- 在被控端用其自己的密钥运行的 AI Computer Use：从模板添加的 Claude、OpenAI 与 OpenAI 兼容服务商、批准、接管、紧急停止、
  睡眠时暂停、由控制端操控的任务。
- 运行方式：免授权、跟随 Kyokai 与后台服务，经由签名、可自我更新、可自行停用的特权助手及位于输入桌面的会话代理。

- 仓库脚手架：工作规范（`AGENTS.md`）、镜像文档、上游版本检查、Rust 工作区与 Go 模块；生成发布签名材料
  （应用更新、特权助手 bundle、macOS 代码签名）。
- 基础：Rust 核心（系统凭据存储保护的机密存储、持久的设备身份与设备 ID、临时密码）及带版本的 C ABI；作为界面用户级
  子进程运行并提供本地 API 的 `kyokai-svc` 引擎；界面（主页、设置、托盘、四种语言、签名的应用更新）；CI 与发布工作流；
  Windows 安装包。
- Phase 0：S1 EasyTier、S2 画面与 S3-lite 采集可行性验证在 Windows ARM64 上通过。
- MyGo v0.5.5 / Rust 1.99.1；为后续阶段固定 EasyTier v2.7.0（预发布版，提交 `8b7f1f0`）与 RustDesk 1.5.0
  （提交 `fada664`）（2026-10-16 检查）。
- 为软件编解码器固定 vcpkg 2026.07.29（提交 `9e593bb`：libvpx 1.16.0、aom 3.13.3）；MyGo v0.5.5、EasyTier v2.7.0、
  RustDesk 1.5.0 与 Rust 1.99.1 未变（2026-10-17 检查）。
- 发布检查（2026-10-17）：MyGo v0.5.5、EasyTier v2.7.0（提交 `8b7f1f0`）、RustDesk 1.5.0、vcpkg 2026.07.29 与 Rust 1.99.1
  均为最新；已检查，未变。
