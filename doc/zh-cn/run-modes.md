# 运行方式

Kyokai 沿用 Easytier-MyGo 的做法：管理员授权只在安装特权助手时进行**一次**，界面启动时从不请求。界面本身从不以提升的权限运行。

## 三种运行方式

| 运行方式 | 前提 | 引擎 | 增加的能力 |
|---|---|---|---|
| 免授权（默认） | 无 | 界面的用户级子进程 | 已登录用户的普通桌面 |
| 随界面 | 安装一次助手 | 在助手中，有界面连接时才服务会话 | Windows：提升权限的窗口、UAC 安全桌面、锁屏 |
| 后台服务 | 同一个助手 | 开机起就在助手中 | 登录前即可连接，仅限固定密码 |

助手把运行方式保存在 `state\mode.json` 中（`helper.background`）。有界面连接时（一个打开的状态流）它服务所有人；没有界面时，后台服务只服务固定密码（“无需确认即可连接”开启时），
随界面不服务任何人，并结束其被控会话（[identity-and-access.md](identity-and-access.md) 的“无人值守访问”）。

## 切换

“通用”中的“运行方式”在三种方式之间切换。离开免授权时，若尚未安装助手则先安装（那一次管理员提示），等待它启动；助手落后于应用的构建时，先用签名更新把它带到同一构建（[更新](#更新)），
然后**迁移存储**（`localapi.MoveStore`）：用户引擎用一次性口令封存整个存储——所有密钥项（身份、受信任设备与已知被控端、固定密码、网络及其密钥、访问设置、
已保存的密码与登录信息、密钥、AI 密钥）以及状态文件 `device.json`、`ai.json` 与 `ai-tasks.json`，外加“不留日志”时只在内存中保留的连接记录
（`store.export`，文档版本 2）；助手一次写入，用它替换自己的整个存储（`store.import`）；只有在助手报告相同的设备 ID 之后，用户引擎才忘记全部内容，
在状态文件夹中留下记录身份去向的 `moved.json` 并停止（`store.clear`）。因此同一身份既不会被两个引擎同时服务，也不会丢失，另一种运行方式也不会留下任何内容。
两个引擎必须是同一构建：0.4.0 的引擎会拒绝版本 2 的文档，且不写入任何内容。

迁移从不在未经询问的情况下替换另一个带有历史记录的身份（受信任设备、已知被控端、网络、固定密码、AI 密钥、已保存的登录信息或密钥）：界面会询问本人，
本人也可以选择让两种运行方式各自保留自己的身份。在状态文件夹中发现 `moved.json` 的引擎处于**驻留**状态：它不启动核心，也不生成自己的身份，只提供自身状态（`moved`）、
取回存储（`store.import`），以及在本人确认后以新身份重新开始（`store.forget_move`）；0.4.1 之前，这样的引擎会生成一个新身份，之后的切换可能把它迁移过去覆盖真正的身份。
`gui.json` 指明（`helper`，按配置文件保存）或自身引擎的标记表明身份已迁往助手时，界面使用助手的套接字；若助手已不在，主页提供重新安装助手或在此重新开始。
随界面与后台服务之间用 `helper.background` 切换，此时界面已经在使用助手。

切回时把存储反向迁移，然后助手无需提示即自行移除（`helper.remove`：在 Windows 上走与自行停用相同的路径，在 macOS 上与此前相同；在服务管理器重新启动交出存储的引擎期间会重试），
只有失败时才回退到管理员提示。`ctl move --to SOCKET` 可在命令行中完成同样的迁移。界面退出时只停止它自己的引擎；助手继续运行，并在最后一个界面断开 15 秒后结束该界面的控制端会话
（退出的界面会立即请求这样做，`sessions.close_controllers`）。

**更新后的清理。** 从 0.4.1 起，每个版本首次启动时（每个版本一次，`gui.json` 的 `stores_unified`）会移除未在使用的运行方式所留下的存储，那是此前的切换遗留的：
连接助手时，界面删除用户引擎的 `secrets.bin`、`master.key`、`device.json`、`ai.json` 与 `ai-tasks.json` 并留下标记（在 macOS 上钥匙串条目保留，它打不开任何东西）；
连接自身引擎且已安装助手时，先用签名更新把助手带到同一构建（从不弹出提示；未签名的构建会在下次启动时再试），再请求它自行移除。助手中有设备、网络或固定密码而当前运行方式没有时
（运行方式的选择丢失了），由本人决定保留哪一套。
助手属于已安装的应用：开发配置（`KYOKAI_PROFILE`）从不移除它。

## 助手（Windows）

`kyokai-svc helper install`（需要管理员）把应用的 `kyokai-svc.exe` 与核心库复制到只有管理员能修改的 `%ProgramFiles%\Kyokai\helper`，
因此用户无法替换服务运行的文件。它为 SYSTEM 与 Administrators 创建 `%ProgramData%\Kyokai`（用户只能打开该文件夹本身以访问套接字；
其下的 `state` 文件夹对用户关闭），把执行安装的用户作为**操作者**记录在 `state\helper.json` 中，为它自己的 `kyokai-svc.exe` 添加一条入站防火墙规则（`Kyokai helper`，
这样助手的直接连接无需提示；[networking.md](networking.md)），并注册自动启动的服务 `KyokaiHelper`：
它以 SYSTEM 身份运行 `kyokai-svc run --host helper`，失败后会重新启动。`helper uninstall` 停止并删除该服务、它的文件与套接字
（`--purge` 还会删除助手的身份与受信任设备）；`helper status` 不需要任何权限。界面只从服务管理器得知助手是否已安装，不为此启动任何进程（它的版本来自助手的状态）：引擎是控制台程序，界面为取其版本而运行它时会打开一个控制台窗口，开机自启时这个窗口空白地留在屏幕上（0.3.2）。

助手的引擎把身份与机密保存在 `%ProgramData%\Kyokai\state` 中（以 SYSTEM 身份使用 DPAPI）。它的套接字 `%ProgramData%\Kyokai\ctl.sock`
所有本地用户都能打开，但助手只服务 SYSTEM、操作者以及本地 `kyokai` 组的成员（若管理员创建了该组）；其他任何人，包括管理员，都会被拒绝
（[security.md](security.md)）。

## 助手（macOS）

应用在 `Contents/Resources/bin` 中、它自己的 `kyokai-svc` 旁边附带 `Kyokai Helper.app`：一个包（`com.yuanzhe.kyokai.helper`，没有程序坞图标），其中有核心库和助手自己的 `kyokai-svc`，因此 macOS 把代理的权限列为“Kyokai Helper”，而应用的引擎算作 Kyokai。`kyokai-svc helper install --from BIN --operator-uid N`（root，经由管理员提示 `osascript … with administrator privileges`，与 EasyTier MyGo 相同）用 `ditto` 把该包复制到 `/Library/Application Support/Kyokai`（属于 root，只有 root 可写），创建 `state`（仅 root），把操作者的 uid 记录在 `state/helper.json` 中，并加载两个 launchd 任务：守护进程 `com.yuanzhe.kyokai.helper`（以 root 身份从开机起运行 `run --host helper`，由 launchd 重新启动）和位于 `/Library/LaunchAgents` 的代理 `com.yuanzhe.kyokai.agent`（`agent --socket …`，在每个图形会话中以该用户身份运行，也在登录窗口运行）。`helper uninstall` 卸载这两个任务并删除包与套接字（`--purge`：还删除状态以及助手在系统钥匙串中的密钥）。

助手把身份保存在 `state` 中，存储的密钥在系统钥匙串里。它的套接字 `ctl.sock` 所有本地用户都能打开；助手只按 uid 与 gid 服务 root、操作者以及本地 `kyokai` 组的成员（[security.md](security.md)）。

## 助手（Linux）

`kyokai-svc helper install --from BIN --operator-uid N`（root 身份，经 `pkexec`，由桌面的 polkit 代理询问：唯一一次管理员提示）把应用的 `kyokai-svc` 与 `libkyokai_core.so` 复制到只有 root 可写的 `/usr/local/lib/kyokai/helper`，把操作者的 uid 与应用的文件夹记录在 `/var/lib/kyokai/helper.json`（仅 root），并写入两个 systemd 单元：系统服务 `kyokai-helper.service`（以 root 运行 `run --host helper`，开机即运行，结束后重新启动；systemd 为它的套接字创建所有本地用户都能进入的 `/run/kyokai`，以及仅 root 可用的 `/var/lib/kyokai`），以及 `/etc/systemd/user` 中的用户服务 `kyokai-agent.service`（`agent --socket /run/kyokai/agent.sock`，属于 `graphical-session.target`，以 `systemctl --global` 为所有用户启用，并立即在每个运行中的用户管理器里启动），它像 macOS 的代理一样在用户的图形会话中采集与注入。代理的链路与 macOS 相同：助手只接受由第一个座席上活动会话的用户（logind 的 `/run/systemd/seats/seat0`）运行的自己的 `kyokai-svc`（`SO_PEERCRED`、`/proc/<pid>/exe`）的连接，代理只连接由 root 持有的套接字。登录界面上没有代理：GDM 与 SDDM 不允许任何程序采集它。`helper uninstall` 停止并停用两个单元，删除文件与套接字（`--purge`：还删除状态）。

助手把身份保存在 `/var/lib/kyokai` 中，存储的密钥由 `systemd-creds` 封存（`master.cred`：主机的凭据密钥，电脑没有时由第一次封存创建，有 TPM 时同时使用 TPM），因为 root 没有 Secret Service。它的套接字 `/run/kyokai/ctl.sock` 所有本地用户都能打开；助手只为 root、操作者与本地 `kyokai` 组的成员服务，按 uid 与 gid 判断（[security.md](security.md)）。更新使用与 Windows 相同的签名包（应用的 `kyokai-svc` 旁边的 `bundle.json` 与 `bundle.sig`），自我停用会停止单元并删除一切，服务最后。AppImage 的文件夹是退出时就消失的挂载点，因此从它安装的助手不记录应用文件夹（`--app-dir=`），永远不会自行移除：切回“无特权”时移除它。

## 会话代理

助手以 SYSTEM 身份运行在没有桌面的会话 0 中。它在当前的控制台会话里以 SYSTEM 身份保持运行一个自身的副本（`kyokai-svc agent`，用移到该会话的
自身令牌在 `winsta0\default` 上启动），每 2 秒检查一次控制台会话，会话变化时替换代理。代理在每个请求时以及采集丢失显示器时都会跟随正在接收输入的桌面
——用户的桌面、UAC 安全桌面或锁屏——并在那里完成桌面上的工作：采集与编码、输入、指针、剪贴板以及 AI 的桌面调用。助手的视频、输入、指针、剪贴板与 AI 桌面
都把工作交给它（`agent::link`）；会话与 AI 的代码在各种运行方式下都相同。

无论采集到什么，采集都会每半秒把输入桌面与自己的桌面比较，并把变化当作显示器丢失来跟随（锁屏的细节见
[computer-use.md](computer-use.md) 的“视频”）。无法移到输入桌面的线程（拥有窗口或钩子的线程，
Windows 会拒绝它的 `SetThreadDesktop`）对每个桌面只记录一次；这时无法跟随的采集线程会结束，代理告诉助手（`{"ended": stream}`），
会话的帧随之结束，会话在一个新线程上重新打开视频（[computer-use.md](computer-use.md) 的“视频”）。助手把会话容纳帧的空位连同帧率与码率
一起告诉代理（`video.targets`），因此代理只采集会话能发送的画面；它来自代理的通道能容纳与被控端领先帧数一样多的帧。代理把它的日志——内核的
日志行与崩溃跟踪——写入助手状态文件夹中的 `logs\agent.log`，除非打开了“不保留日志”（[security.md](security.md) 的“日志”）；更改
“不保留日志”会重启代理，新的代理遵循这一选择（连接上来的界面会再次发送它的选择，这不会改变任何东西，也不会重启任何东西）。
macOS 的代理所用的用户无法写入助手的文件夹，因此它经链路发送日志行，由助手写入自己的 `helper.log`。助手的操作者无需管理员权限，
即可经 `logs.read` 读取这些日志（“诊断”中的**保存日志…**；`ctl logs`）。

两者通过两条单向命名管道通信（同一个管道句柄上的同步 I/O 会被串行化，一个等待中的读取会阻塞所有写入），每条管道都有随机名称，以第一个实例的方式创建，
并带有只允许 SYSTEM 的 DACL；助手检查客户端是否为它启动的代理进程，代理检查服务端是否为命令行上指明的助手。消息由 u32 长度、类型（JSON 请求或答复、
一帧视频、一张截图）与正文组成。人的输入发给代理后不等待答复，指针位置则在会话自己的一个线程上询问，因此两者都不会拖住网络的线程。只有编码后的视频、输入事件、剪贴板文本、本机复制的剪贴板图片（由代理编码为 PNG）、所复制文件的路径以及 AI 的截图会在两者之间传递。
两个进程都有特权，从不解码远程媒体，因此在助手模式下不接收来自控制端的剪贴板图片。剪贴板中的文件由助手在模拟控制台上已登录用户身份的情况下读写（[computer-use.md](computer-use.md) 的“文件”）。没有代理时，视频在会话重试期间显示占位画面（Kyokai Helper 正在准备）（[computer-use.md](computer-use.md) 的“视频”），输入被拒绝，AI 任务则视之为被控端用户接管。代理答复得太晚的打开会在那里再次关闭，没有代理连接时请求的唤醒会交给下一个代理，并且只有最新代理的连接结束才会断开链路。

在 macOS 上由 launchd 而不是助手启动代理：它连接助手文件夹中的 `agent.sock`（属于 root，因此只有 root 能占用；代理会确认对端是 root），助手只接受由屏幕前的用户或登录窗口的 root 运行的、它自己的 `kyokai-svc` 发来的连接（`LOCAL_PEERPID`、`proc_pidpath`）；较新的代理会替换较旧的，而较旧代理的连接之后结束时不会影响较新代理的视频流。链路上的消息相同。代理以它自己的权限注入；在那里不提供阻止用户输入、隐私模式与锁定。在助手模式下，状态中的 `permissions` 是代理的权限，在那里请求的权限也由代理请求；macOS 只在授权之后启动的代理中计入新授权，因此权限卡片提供**重启 Kyokai Helper**（`helper.agent_restart`：代理退出，launchd 再次启动它）。

## 更新

发布版本会为助手的文件签名：`scripts/build.sh` 在它们旁边写入 `bundle.json`（版本以及 `kyokai-svc.exe` 与 `kyokai_core.dll` 的 SHA-256）
与 `bundle.sig`（Ed25519，`KYOKAI_SIGNING_KEY`）。助手比应用旧时，“通用”中的“运行方式”会提供**更新助手**（`helper.update`）：助手**只读取一次**
该 bundle，用内置的密钥（`internal/bundlesig/release.pub`）检查签名、检查每个哈希、确认两个文件都在且版本不更旧，然后把这些已验证的字节原样写入
自己的文件夹（先把正在运行的文件改名移开）并退出；服务会重新启动它。它不会再次读取用户可写的应用文件夹，因此检查之后在那里被替换的文件不会被安装。
未签名的版本（开发版本，或没有密钥的发布版本）改为通过一次管理员提示安装。

应用更新只替换应用的文件，不会替换助手的文件，而版本不同的助手无法连接运行新版本的设备。因此 GUI 第一次连上落后于它的助手（版本更旧，或同一版本但构建不同：状态中的 `build`，因此测试版的助手也会跟上）且没有打开的会话时，
会自行请求这次签名更新，不弹出提示；它从不自行退回到管理员提示。因版本不一致而被拒绝的连接会说明哪一端不同：是这台设备较旧的助手，还是对方设备的版本。

在 macOS 上由代码签名完成这件事：助手先把应用的 `Kyokai Helper.app` 复制到属于 root 的文件夹，用 `codesign --verify --deep --strict` 按已安装包的指定要求（相同的标识符与证书）检查这份副本，并确认版本不更旧，然后把它放到位并退出；launchd 重新启动守护进程，助手重新启动代理。临时签名的版本永远不满足该要求，需要管理员提示。

## 自我停用

安装程序会记录应用所在的文件夹（`helper.json` 中的 `app_dir`）。当相隔一分钟的两次检查都发现那里没有应用的引擎时（应用更新替换文件不算移除），
助手会删除自己的服务、状态与套接字，安排在下次 Windows 启动时删除自己的文件，然后退出，因此移除应用后不会留下运行中的助手。在 macOS 上它卸载代理，删除自己的 plist、文件夹与密钥，最后卸载自己。
在助手模式下，身份只保存在助手中，因此移除应用也会把它一起移除。在 Windows 重启之前再次安装的助手会先撤销此前的移除在其文件夹下安排的删除操作，
否则这些操作会在那次重启时删掉它的新文件。

## 当前实现

Windows 上已实现助手的安装、卸载与状态命令、服务本身、它的授权以及会话代理（链路、在控制台会话中启动与跟随桌面）；链路已在进程内测试，服务与代理尚未在机器上测试。
在 macOS 上，助手已在一台 macOS 27 的 MacBook Pro 上运行（[platform-notes.md](platform-notes.md) 的“macOS”）：一次提示完成安装、双向的存储迁移、代理传来的视频与一次点击、签名自更新（临时签名的包被拒绝）以及无需提示的移除；后台服务、登录窗口与自我停用尚未在那里运行。助手已在 Windows ARM64 上以服务方式运行（[platform-notes.md](platform-notes.md) 的“助手”）：安装、授权、代理及其对安全桌面的采集、
双向的存储迁移、无人值守访问与卸载。界面中的运行方式切换与签名更新尚未在那里运行。签名自更新与自我停用已实现，其规则已经测试（检查之后被替换的文件不会被安装）；这些都尚未以服务方式运行过。只用 `kyokai-svc helper install`
安装的助手会运行一个自己的引擎，拥有单独的设备 ID，直到有存储迁入为止。Linux 上的助手、它的单元、代理链路与封存的密钥已实现，链路的对端检查已测试；尚未在机器上安装过。
