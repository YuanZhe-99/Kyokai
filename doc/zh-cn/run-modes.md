# 运行方式

Kyokai 沿用 Easytier-MyGo 的做法：管理员授权只在安装特权助手时进行**一次**，界面启动时从不请求。界面本身从不以提升的权限运行。

## 三种运行方式

| 运行方式 | 前提 | 引擎 | 增加的能力 |
|---|---|---|---|
| 免授权（默认） | 无 | 界面的用户级子进程 | 已登录用户的普通桌面 |
| 随界面 | 安装一次助手 | 在助手中，有界面连接时才服务会话 | Windows：提升权限的窗口、UAC 安全桌面、锁屏 |
| 后台服务 | 同一个助手 | 开机起就在助手中 | 登录前即可连接，仅限无人值守授权 |

## 助手（Windows）

`kyokai-svc helper install`（需要管理员）把应用的 `kyokai-svc.exe` 与核心库复制到只有管理员能修改的 `%ProgramFiles%\Kyokai\helper`，
因此用户无法替换服务运行的文件。它为 SYSTEM 与 Administrators 创建 `%ProgramData%\Kyokai`（用户只能打开该文件夹本身以访问套接字；
其下的 `state` 文件夹对用户关闭），把执行安装的用户作为**操作者**记录在 `state\helper.json` 中，并注册自动启动的服务 `KyokaiHelper`：
它以 SYSTEM 身份运行 `kyokai-svc run --host helper`，失败后会重新启动。`helper uninstall` 停止并删除该服务、它的文件与套接字
（`--purge` 还会删除助手的身份与受信任设备）；`helper status` 不需要任何权限。

助手的引擎把身份与机密保存在 `%ProgramData%\Kyokai\state` 中（以 SYSTEM 身份使用 DPAPI）。它的套接字 `%ProgramData%\Kyokai\ctl.sock`
所有本地用户都能打开，但助手只服务 SYSTEM、操作者以及本地 `kyokai` 组的成员（若管理员创建了该组）；其他任何人，包括管理员，都会被拒绝
（[security.md](security.md)）。

## 当前实现

Windows 上已实现助手的安装、卸载与状态命令、服务本身及其授权；macOS 的助手（root LaunchDaemon）随 Mac 测试一同实现，在此之前报告
“尚不可用”。设置中的运行方式选择、在控制台会话桌面上采集与注入的会话代理、把身份移入助手、助手的签名自更新与自我停用将随后实现
（`PLAN.md`，第 6 阶段）。在此之前，已安装的助手会运行一个自己的引擎，拥有单独的设备 ID，仅供开发使用。
