# 命令行

`kyokai` 是 Kyokai 面向人与脚本的命令行（包 `internal/ctl`）。每条命令都是经本地 API（[architecture.md](architecture.md#本地-api)）
向用户自己的引擎发出的一个请求，并打印它的回答；它与界面一样是一个客户端，没有自己的信任，界面不能做的事它也不能做。

## 运行

| 平台 | 路径 |
|---|---|
| Windows | 应用文件夹中的 `bin\kyokai.exe`（安装后为 `%LOCALAPPDATA%\Programs\Kyokai\bin\kyokai.exe`） |
| macOS | `Kyokai.app/Contents/Resources/bin/kyokai` |
| Linux | 应用文件夹中的 `bin/kyokai`（Debian 包为 `/opt/kyokai/bin/kyokai`） |

它不会加入 `PATH`；需要的人自行加入该文件夹或建立链接。`kyokai-svc ctl COMMAND …` 是同一组命令的另一个名字。
命令与界面启动的引擎通信（`--socket` 指定另一个；`KYOKAI_PROFILE` 选择开发实例的引擎），只有特权助手在其套接字上监听时才会与它通信。

`kyokai help` 列出所有命令；`kyokai help COMMAND` 与 `kyokai COMMAND --help` 说明一条命令及其参数。参数写在命令之后。

## 输出与退出码

默认输出供人阅读。`--json` 改为以本地 API 的 JSON 回答打印引擎的回答（`screenshot`：文件及其尺寸；`move`：设备与套接字）；
边到达边打印的命令（`watch`、`watch-status`、`tunnel`、`logs`）不接受它。`backup --json` 仍只把身份写入它的文件。

| 退出码 | 含义 |
|---|---|
| 0 | 完成（也包括显示了帮助） |
| 1 | 其他任何错误 |
| 2 | 未知命令或参数错误 |
| 3 | 套接字上没有引擎在监听 |
| 4 | 引擎拒绝了请求；消息说明原因 |

## 机密

机密只来自环境变量或标准输入，绝不来自参数，因为其他进程可以读取参数：`--secret-stdin` 从标准输入的第一行读取命令的机密，代替它的环境变量。

| 变量 | 读取它的命令 |
|---|---|
| `KYOKAI_PASSWORD` | `connect`：被控端的密码（为空则不带密码请求） |
| `KYOKAI_PERMANENT_PASSWORD` | `permanent`；关闭受保护模式时的 `protect` |
| `KYOKAI_NETWORK_SECRET` | `network-set`、`network-profile`（未设置则保留已存的） |
| `KYOKAI_BACKUP_PASSPHRASE` | `backup`、`restore` |
| `KYOKAI_AI_KEY` | `ai-key` |
| `KYOKAI_SERVICE_PASSWORD` | `credentials-set` |

## 命令

| 分组 | 命令 |
|---|---|
| 这台设备 | `status`、`password`、`regenerate`、`permanent`、`name-set`、`access`、`password-grants`、`trusted`、`untrust`、`backup`、`restore`、`reset-identity` |
| 网络 | `network`、`network-set`、`networks`、`network-profile`、`network-remove`、`network-direct`、`network-backend`、`cores`、`core-install` |
| 会话与设备 | `connect`、`identify`、`close`、`allow`、`deny`、`forget`、`remove`、`devices`、`route-add`、`route-remove`、`route-prefer`、`screenshot`、`watch`、`watch-status` |
| AI | `ai`、`ai-set`、`ai-key`、`ai-models`、`ai-start`、`ai-reply`、`ai-tasks`、`ai-follow`、`ai-rename`、`ai-fork`、`ai-compact`、`ai-export`、`ai-pause`、`ai-resume`、`ai-cancel`、`ai-approve`、`ai-decline`、`ai-apps`、`ai-scope`、`ai-folders`、`ai-clear`、`ai-stop` |
| 密钥、隧道与登录信息 | `keys`、`keys-generate`、`keys-remove`、`authorized`、`authorized-add`、`authorized-remove`、`tunnel`、`credentials-set`、`credentials-get`、`credentials-forget` |
| 引擎 | `background`、`helper-update`、`move`、`capture-show`、`no-logs`、`protect`、`clipboard-mine`、`logs`、`shutdown` |
| 命令行本身 | `help`、`version` |

`devices` 列出本机连接过的设备及其保存的地址；界面为设备起的名称属于界面自己，留在那里。`tunnel --listen PORT` 在这台电脑的回环地址上（绝不是其他地址）提供被控端的 SSH、远程桌面或 VNC，
每个连接一条隧道；不带它时隧道是标准输入与输出，正如 OpenSSH 的 `ProxyCommand` 所要求，也可以用来传输文件：`sftp` 与 `scp` 加上
`-o ProxyCommand="kyokai tunnel --device ID --service ssh"`。`ai-follow --id N` 随任务进行打印它的步骤与状态，直到结束。

`watch` 用于测试画面流（每秒帧数、间隔、被控端的报告、注入的输入）；其余命令做的都是界面所做的事，每条一个请求。

## 截图

`kyokai screenshot --id N --out FILE.png [--display D]` 把控制端会话 N（`connect` 打印的编号）的第一幅完整画面保存为 PNG 文件。
这是命令行显示被控端屏幕的唯一方式：除非命令要求，不跟随也不写入任何画面，并且只写到命令指定的路径。它与查看器一样需要会话的
`view-screen` 授权；它向被控端请求一个关键帧，并用查看器的解码器解码，因此静止的屏幕也能得到画面；十秒内没有画面即为失败。

## AI 范围

`kyokai ai-apps` 按 AI 范围使用的名字列出这台设备屏幕上有窗口的程序；`kyokai ai-scope --apps A,B` 在本会话中只把这些程序的窗口
授予 AI，不带程序的 `kyokai ai-scope` 则重新给它整个桌面（[computer-use.md](computer-use.md#ai-范围)）；
`kyokai ai-folders --folders "C:\Work;D:\Shared"`（与 `PATH` 相同的分隔方式：Windows 上为 `;`，其他系统为 `:`）
只授予这些文件夹，检查而非强制，不带文件夹则重新允许所有文件夹。`ai` 会显示两者。
范围只能由被控端用户设置，所以这些命令只与这台设备自己的引擎通信。
