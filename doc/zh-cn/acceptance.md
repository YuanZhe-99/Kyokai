# 验收

规格中的必测场景（§26）与双机验收场景（§27），逐项列出现状与证据。**已验证**表示真实运行过，记录在
[platform-notes.md](platform-notes.md)；**已测试**表示只有自动化测试；**已实现**表示已实现但尚未运行；**未完成**表示尚未做。
OpenAI 是规格中的服务商；Kyokai 对任何已配置的服务商运行相同流程，目前的实测使用的是 Claude。

## 设备身份

| 场景 | 状态 | 证据 |
|---|---|---|
| 设备 ID 在应用重启后保持不变 | 已验证（Windows） | 平台说明：机密、应用（ARM64 安装包） |
| 设备 ID 在 EasyTier 重连后保持不变 | 已验证（Windows） | 平台说明：远程 x64 虚拟机（节点重启后会话恢复） |
| 网络地址变化时设备 ID 不变 | 已验证（Windows） | 平台说明：监听重新绑定；ID 只由设备密钥得出 |
| 可从备份安全恢复身份 | 已验证（Windows） | 平台说明：身份备份；`a_backup_restores_the_same_identity_and_only_with_its_passphrase` |
| 拒绝加密身份不匹配 | 已测试 | `a_changed_host_key_is_refused` |

## 认证

| 场景 | 状态 | 证据 |
|---|---|---|
| 临时密码可用 | 已验证（Windows） | 平台说明：会话 |
| 刷新临时密码后旧密码失效 | 已验证（Windows） | 平台说明：引擎（`smoke.sh user` 会重新生成）；旧记录在内存中被替换 |
| 固定密码可用 | 已验证（Windows） | 平台说明：会话 |
| 临时密码与固定密码可分别启用 | 已测试 | 两个 OPAQUE 槽位；`unattended_grants_need_the_permanent_password_and_follow_whom_the_host_serves` |
| 拒绝错误密码 | 已验证（Windows） | 平台说明：会话；`refusals_are_named` |
| 多次失败会被限速 | 已测试 | `an_address_is_blocked_after_five_failures_and_pinned_keys_skip_the_backoff` |
| 可撤销受信任设备 | 已测试 | `revoking_a_device_ends_its_sessions_and_its_resumes` |

## EasyTier 网络

| 场景 | 状态 | 证据 |
|---|---|---|
| 两台设备加入标准 EasyTier 网络 | 已验证（Windows ↔ Windows） | 平台说明：引擎中的 EasyTier、远程 x64 虚拟机 |
| 只需要标准 EasyTier 设置 | 已验证 | 服务器地址、网络名称与密钥（[networking.md](networking.md)） |
| 无需 IP 地址即可解析设备 ID | 已验证 | 平台说明：会话、远程 x64 虚拟机 |
| 无 TUN 连接 | 已验证（Windows） | 平台说明：S1 EasyTier 试验、防火墙提示 |
| 动态地址不影响身份 | 已验证（Windows） | 平台说明：监听重新绑定 |
| 网络中断得到妥善处理 | 已验证（Windows） | 平台说明：远程 x64 虚拟机；`a_dropped_session_resumes_without_a_new_login` |

## 手动 Computer Use

| 场景 | 状态 | 证据 |
|---|---|---|
| 查看远程屏幕 | 已验证（Windows） | 平台说明：虚拟机的真实桌面、屏幕视频 |
| 鼠标控制 | 已验证（Windows） | 平台说明：助手（在 UAC 安全桌面上的一次点击） |
| 键盘控制 | 已验证（Windows） | 平台说明：在 Windows 11 记事本中输入 |
| 多显示器选择 | 已验证（Windows 虚拟机） | 平台说明：显示器 |
| DPI 坐标映射 | 已测试 | 虚拟桌面上的输入映射测试；尚未试过混合 DPI |
| 断开时释放资源 | 已测试 | 按住的键与按钮被松开，采集与编码器随会话释放 |

## AI Computer Use（规格中的“OpenAI Computer Use”）

| 场景 | 状态 | 证据 |
|---|---|---|
| A 向 B 发送任务 | 已验证（Windows ↔ Windows 虚拟机） | 平台说明：虚拟机的真实桌面 |
| B 用自己的凭据调用服务商 | 已验证（Claude）；OpenAI 等待密钥 | 平台说明：AI 运行时 |
| B 采集自己的桌面 | 已验证（Windows） | 平台说明：AI 运行时 |
| B 执行经过校验的操作 | 在合成屏幕上已验证；已测试 | `claude_task_clicks_and_finishes` |
| A 收到进度 | 已验证（Windows） | 平台说明：来自控制端的 AI |
| 拒绝无效的工具调用 | 已测试 | `openai_rejects_unknown_action_and_key`、`claude_rejects_bad_members` |
| 拒绝越界坐标 | 已测试 | `stale_and_out_of_range_actions_are_rejected` |
| 拒绝过时的观察 | 已测试 | `stale_and_out_of_range_actions_are_rejected` |
| API 超时不会造成死锁 | 已测试 | `one_task_at_a_time_and_cancel_stops_a_request` |
| 任务完成得到确认 | 在合成屏幕上已验证 | 模型在结束前截图并确认了结果 |

## 授权与安全

| 场景 | 状态 | 证据 |
|---|---|---|
| 未授权设备无法启动 AI 任务 | 已测试 | `ai_requests_need_their_permissions` |
| VIEW_SCREEN 不意味着 RUN_AI_TASK | 已测试 | `confirmations_grant_only_what_was_asked_and_flag_new_keys`、`ai_requests_need_their_permissions` |
| A 无法取得 B 的 API 密钥 | 已测试 | `settings_and_keys_never_expose_the_key`；没有任何消息携带它 |
| 手动接管会停止 AI 操作 | 已测试 | `the_stop_shortcut_cancels_and_a_take_over_pauses` |
| 紧急停止阻止后续输入 | 已测试 | `the_stop_shortcut_cancels_and_a_take_over_pauses`、`a_response_after_a_pause_never_acts` |
| 断线会暂停远程 AI | 已测试 | `a_controllers_task_pauses_when_its_session_ends` |
| 重连不会重复执行操作 | 已测试 | 命令从不重发；任务创建是幂等的 |
| 敏感操作需要批准 | 已测试 | `every_action_policy_asks_and_a_decline_runs_nothing`、`openai_safety_check_waits_for_approval_and_is_acknowledged` |
| 撤销后停止未授权的执行 | 已测试 | `revoking_a_device_ends_its_sessions_and_its_resumes` |

## 应用生命周期

| 场景 | 状态 | 证据 |
|---|---|---|
| 默认不注册自动启动 | 已验证（Windows） | 平台说明：应用（ARM64 安装包） |
| 默认不安装特权服务 | 已验证（Windows） | 助手只在要求时安装，并弹出一次管理员提示 |
| 退出时停止网络 | 已验证（Windows） | 平台说明：应用（结束图形界面即结束引擎） |
| 退出时停止 AI 执行 | 已验证（Windows） | 运行 AI 任务的引擎随应用结束 |
| 不会残留常驻网络守护进程 | 已验证（Windows） | 平台说明：助手（卸载后不留任何东西） |
| 显示配置路径 | 已验证（Windows） | 设置 → 存储与诊断 |

## 构建与发布

| 场景 | 状态 | 证据 |
|---|---|---|
| 每个平台都能构建 | 已验证 | 本地 Windows ARM64 与 x64；CI 上的 macOS Apple 芯片与 Intel |
| Rust 原生库可以加载 | 已验证 | Windows 与 macOS CI 上的 ABI 冒烟测试 |
| 图形界面与打包的核心库配合工作 | 已验证（Windows） | 平台说明：应用（ARM64 与 x64 安装包） |
| CI 产物是真实的安装包 | 已验证（Windows x64） | 平台说明：远程 x64 虚拟机（CI 构建的安装程序） |
| 发布校验和一致 | 已验证（0.1.0） | 下载的 `Kyokai.Setup.0.1.0.arm64.exe` 与 `SHA256SUMS.txt` 一致；0.1.0 中安装程序以带空格的文件名列出，之后的版本使用 GitHub 提供的文件名 |
| 报告缺少签名 | 已验证 | 未签名的构建会被标注；助手在没有提示的情况下拒绝未签名的更新 |
| README 致谢 | 已完成 | README 的致谢部分 |
| 第三方义务 | 已完成 | [third-party.md](third-party.md)、`THIRD_PARTY_NOTICES.md`、`RUST_LICENSES.txt` |

## 双机验收（§27）

| 步骤 | 状态 | 证据 |
|---|---|---|
| 1 在标准服务器上设置网络 | 已验证（Windows ↔ Windows 虚拟机） | 平台说明：远程 x64 虚拟机 |
| 2 B 显示设备 ID 与密码 | 已验证 | 主页 |
| 3 A 用设备 ID 与密码连接，无需 IP 地址 | 已验证 | 平台说明：远程 x64 虚拟机 |
| 4 手动模式 | 已验证 | 平台说明：虚拟机的真实桌面 |
| 5 在 B 的真实桌面上运行 AI 模式，由 A 操控 | 用 Claude 已验证（Windows ↔ Windows 虚拟机）；OpenAI 等待密钥 | 平台说明：虚拟机的真实桌面（任务打开记事本并输入文字，标记的快捷键请求了批准） |
| 6 退出时结束会话、网络与 AI，并释放资源 | 已验证（Windows） | 平台说明：应用 |

macOS：采集、输入与助手已在一台 Mac 上运行（[platform-notes.md](platform-notes.md) 的“macOS”）；清单的其余部分等待 Mac 测试的剩余部分。
