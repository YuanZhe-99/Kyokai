# User guide

Kyokai lets you see and operate another computer, and lets an AI operate a computer for its user —
always on the computer being operated, with that computer's own AI provider key. This guide is
for the people using it; how it works is in the other pages of this folder.

## Install and start

On Windows, run the installer: it installs for your user only and asks for no administrator rights.
Kyokai starts with its window and an icon in the notification area; closing the window keeps it
running there (General can make the close button quit instead). Kyokai does not start
with Windows unless you switch on "Open at login"; started at login, it opens no window and waits
in the notification area (the menu bar on a Mac), unless you switch off "Start in the tray when opened
at login".

On macOS, open the disk image and drag Kyokai to Applications. Kyokai is signed but not notarized
by Apple, so macOS may call the downloaded app damaged: run
`xattr -dr com.apple.quarantine /Applications/Kyokai.app` in Terminal once, then open it.
Kyokai asks on its first start for **Screen Recording** (so others can see this screen),
**Accessibility** (so they can use the mouse and keyboard) and **Input Monitoring** (so your own
keyboard and mouse pause an AI task, and Control+Option+Shift+Esc stops it). Allow them in System
Settings, Privacy & Security; **Privacy permissions** on Home and in Permissions shows what is still
missing and opens the right pane with **Allow…**. A permission you allow counts only after Kyokai
starts again: click **Reopen Kyokai** on its row. Allow Kyokai to use its
Keychain item too (**Always Allow**); if you denied it, **Allow…** on the Keychain row asks again.

Kyokai keeps its secrets — this device's identity, passwords, keys — encrypted with your Windows
account. If that is not possible, Home says so: the Device ID then changes on every start.

## This device

Home shows **This device**: the Device ID, nine digits that others use to reach this computer, and
the **Temporary password**, hidden until you choose Show. The temporary password changes every
time Kyokai starts and whenever you choose **New password**; give it only to someone you want to let
in. Permissions can also set a permanent password, for devices you trust over time.
Security, Device identity, sets this computer's **Friendly name**, which others see beside the
Device ID once they have logged in; left empty, it is the computer's name.

## Join a network

Devices find each other through EasyTier networks. On the **Network** page choose **Add a
network** and enter the **Servers** (one address per line), the **Network name** and the **Network
secret** that whoever runs your network gave you, optionally a name to show for it, and save: the
device joins it at once. You can keep up to 8 networks and join several at the same time (each
has its **Join** switch); this device has the same Device ID in all of them, so a device in any of
your networks reaches it. Each network shows its state, this device's address in it and the
Kyokai devices it can reach, directly or through a relay. A device's announced ID is only a hint:
who a device is gets proven when you connect. Connecting by Device ID tries every joined network
that lists the device, direct paths first. **Edit** changes a network; **Remove** leaves it and
forgets its settings.

Connections go through the server unless you switch on **Try direct connections** (under Advanced on the Network page; it applies
at once), which is faster where both networks allow it; Windows may then ask to let Kyokai through
its firewall, and a Mac to let it find devices on the local network. On one network it is enough on
one of the two computers; turn it on on both for computers on different networks. Each device in
the list has Copy and Connect. Without a network you can still connect by address (below).

## Control another device

Under **Control another device** on Home, enter the other device's Device ID and choose
**Connect**, or click a device under **Recent** (a green dot means it is on your network). By
default you ask to see the screen, use the mouse and keyboard, share the clipboard and copy files;
**Options** › **Also ask to run AI tasks there**, or **Connect with AI** in a device's ⋯ menu, asks
for the AI permissions as well. The connection opens as a tab next to Home. While the other
computer's user is asked, the tab offers to enter its password instead; with **Remember password**
it is kept (encrypted) for next time, and Security, **Saved passwords and keys**, forgets it. With
the right password you are in at once with what the other computer lets a password grant; for
anything more its user is asked. The other computer's user chooses what to allow:

| Permission | Lets you |
|---|---|
| See the screen | watch the other computer's screen |
| Use the mouse and keyboard | operate it from the picture |
| Share the clipboard | copy text on one side and paste it on the other |
| Copy files | copy files or folders on one side and paste them on the other (up to 1000 items and 1 GiB at a time) |
| Run AI tasks | give the other computer's AI a task and steer it |
| Approve AI actions | answer the approvals of those tasks |

Once connected, the tab shows the other screen. Drag the tab out of the window (or click its ⧉)
for a window of its own, and onto another Kyokai window to move it there. To see several at once,
drag a tab onto a picture: drop it at an edge to show it beside, above or below that one (up to
four, in any arrangement); the one you click in takes the keyboard, and its ▢ shows it alone again. The toolbar floating
over the top of the picture holds the controls; rest the pointer on an icon to see its name. Drag
it by its grip at the left end to wherever along the top edge suits you, and fold it with ⌃ to a small handle;
Kyokai remembers both. **Display** chooses the screen when the other computer has several, and
**Fit** (the whole screen in the window) or **Original size** (one point per pixel of the other
computer, with scroll bars to drag when it is larger than the window). **Quality** sets
**Faster**, **Balanced** or **Sharper**, a frame-rate cap and the **Codec** (**Auto** lets the
other computer choose; the list shows what both computers support). Beside the icons the toolbar
says whether the connection is **Direct** or **Relayed**; its tooltip shows where the time goes.
**Full screen** fills the display.
Click the picture to use the mouse and keyboard there; Kyokai's own windows on the other computer take your input only
where its user turned off **Hide Kyokai from screen capture**, and not while a connection request or an AI approval waits
there. **Actions** can **Lock screen**, send **Ctrl+Alt+Del** (when the other computer runs
Kyokai as a helper) and **Block their keyboard and mouse** while you work, or turn on **Privacy mode**, which also
blacks out their screen while you still see it; its user can always lift both with
Ctrl+Alt+Shift+Esc or Ctrl+Alt+Del. **Keyboard** chooses how your keyboard reaches the other
computer: **Map** (the default) sends each key by its place, so the other computer's layout
decides what it types and shortcuts such as the Windows key, Alt+Tab or Cmd+Tab go there too;
**Translate** types with your own layout and input method; **Legacy** sends only the keys the
viewer window receives. On a Mac, Map and Translate need **Accessibility** for Kyokai in System
Settings, Privacy & Security. To type Chinese or Japanese, either use **Map** and the other
computer's own input method (switch it there, for example with Win+Space; your own input method
stays out of the way while the picture has the keyboard), or use **Translate** with your own input
method: compose, choose and commit as usual — Enter, Esc and the arrows stay with your input method
while it composes — and the committed text is typed there. If the connection stalls, typing pauses
("Input paused" in the toolbar) instead of arriving late, and your own keyboard keeps working. Between a Mac and Windows, **Swap Ctrl and Cmd** (on by default) makes
your usual shortcuts work there: Cmd+C copies on a Windows PC, Ctrl+C on a Mac. Keys go there only
while the picture has the focus: click it first. You hear what the other computer plays (or the input
its user chose in Permissions, **Sound to share**, such as a microphone; a Mac asks for its
Microphone permission then); **Sound** in the toolbar turns it off. **Clipboard** pulls out the
clipboard panel: **Share the clipboard with this device** turns sharing off or on for that device,
and below it is what crossed the clipboard while its tab was in front — from that device or from
this computer, newest first, with **Copy** for text — kept only until the tab closes. With several
devices connected, only the one whose tab is in front (in the Kyokai window you used last) shares
the clipboard; switching to a tab sends what you copied meanwhile. Quality, frame rate, Fit or
Original size, codec, sound, the swap and the clipboard switch are remembered for each device. While you are connected the other computer does not go to
sleep by itself, and a screen that was dark already is woken. If the picture does not come, the
other computer keeps trying to bring it back by itself, and the picture area says what is happening
there (its screen asleep, Kyokai Helper getting ready, the picture starting again); when it says
the screen is asleep or off, **Wake it** wakes it for you. Closing the tab (×) disconnects; when a connection ends, its tab says why and offers
**Reconnect**. A connection that drops reconnects by itself for a minute without asking again.
Closing a window disconnects its tabs: Kyokai asks first when that ends two connections or more, or
one you cannot see because Home is in front; with **Quit** chosen for the close button, closing the
main window asks while any connection is open, then quits.
**Remove** in a device's menu takes it off Recent and forgets its saved password. **Rename…** gives
it a name of your own, shown only on this device; leave it empty to use the device's own name
again.

### Connect by address

On the same local network, over Tailscale, or wherever the other computer can be reached, you can
connect without joining a network: enter its IP address or domain name (add `:port` when it is not
47310) instead of a Device ID and choose **Connect**. Kyokai first asks who answers there, then
connects to that device; its card then keeps the address among its routes. The other computer must allow it:
the Network page, **Accept connections by address** (off by default). It accepts them only from the
listed address ranges — by default local networks and Tailscale — and still needs a password or
its user's approval. Windows may ask to let Kyokai through its firewall.

### Several ways to reach one device

A device can be reached through the Kyokai network and any number of addresses — its LAN address
at home, a public address or domain name, a Tailscale address. Open its card's ⋯ menu →
**Addresses…** to add or remove them; each shows its delay now, when it was last used and the
speed it reached. The line under the device's name shows the route a connection would take and
opens a menu: **Auto** (the default) picks, at every connection and reconnection, the route that
answers fastest, passing over one that was slow or failed lately; or choose the Kyokai network or
one address to use only that. A connection that drops reconnects over the next-best route under
Auto; a working one is never moved. The viewer's path badge shows the route in use.

### SSH, Remote Desktop and VNC

A device running Kyokai 0.4.1 or later lets you reach its own SSH, Remote Desktop or VNC (Screen
Sharing on a Mac) through Kyokai, with no port opened to the network. Its card on Home shows
**SSH**, **Remote Desktop** or **VNC** for the services it has switched on, and its ⋯ menu always
offers them. Each opens as a tab like a desktop's. Its user allows each one like any other
permission (Permissions, **With a correct password**, has a switch for each, off by default).
The first time, Kyokai asks how to sign in before it connects: for SSH with **Key** (your keys, see
below) or **Password**; for VNC with **Password only**, **User name and password** (a Mac's) or
**No sign-in**; for Remote Desktop with **User name and password** or **On the remote screen** (the
Windows sign-in screen). With **Remember**, the next connection signs in the same way without
asking, and asks again only if that fails. The first time, Kyokai also remembers the SSH server's
key, and if it ever changes, it sends no password and asks whether to trust the new one. If the
other computer runs a service on another port, its user enters that port under Permissions, **Ports
of this computer's services**; you only pick the service. The
picture of Remote Desktop and VNC fits the window or shows at its original size (**Display**),
takes the keyboard like a desktop's (**Keyboard**), sends **Ctrl+Alt+Del** from **Actions**, and
shares the clipboard's text while its tab is in front.

### Status on Home

Home shows each device as a card. A device with a saved password or key that runs Kyokai 0.4.1 or
later shows its processor, memory, graphics, disk and network use, its battery and how long it has
been up, updated every two seconds while Home is in front, with a line of the last two minutes.
Values older than six seconds are dimmed with their age, and after a minute they are no longer
shown. The other computer sees who is watching on its Permissions page and can turn
**See this computer's status** off; watching keeps nothing awake there.

### Sign in with keys

Security, **Your keys**: **Generate a key** makes an Ed25519 key and shows its private part once, to
copy or save (with an optional passphrase) for other SSH clients; Kyokai keeps it and never lets it
out. **Import a key** takes an existing OpenSSH or PEM key. To let a key sign in to this device, go
to Permissions, **Passwords and keys**, and **Add a key** with its public key, or **Generate a
pair** there and take the private part away. A key that can connect counts as a correct password;
unchecking it keeps it but stops it signing in. **Saved passwords and keys** (Security) lists what
is kept for each device you connect to, with × to forget one — the Kyokai password, the key, or a
service's sign-in — or **Forget all**.

## Let someone control this device

Someone with your password gets in at once with what Permissions, **With a correct password**
allows (by default seeing the screen, the mouse and keyboard, the clipboard and files, but no AI
tasks). Otherwise — they ask for more, ask without a password, or a known Device ID arrives with a
new key — a request appears at the top of every page with their Device ID, a warning in the last
two cases, and a switch for each permission they asked for. Choose what to allow, then **Allow**, or **Deny**. Devices you allowed are listed in
Permissions; **Remove** revokes one at once and ends its connections. There, **Ways to sign in**
chooses what lets a device in: the temporary password, the permanent password and each key that can
connect, each with its own check box, in any combination; with none checked, every connection asks
you. **Connect without asking** (on by default) lets a checked way in without asking you; switched
off, you are always asked. With nobody at the computer (Background service), only the permanent
password or a key gets in. Your own use
of the keyboard and mouse always wins over theirs and over any AI.

## Run Kyokai as a helper

General, **Run mode**, chooses where this device runs. **No privileges** (the default) needs
nothing. **Follow Kyokai** and **Background service** install a helper once, after one
administrator prompt, so the other side can also see and use sign-in screens and administrator
prompts on this computer; Background service also lets devices you allowed to connect without
asking in before anyone signs in. Everything moves along — your Device ID, trusted devices,
networks, passwords saved for other devices, keys and settings — and nothing stays behind in the
other mode. Choosing No privileges again moves it all back and removes the helper, without another
prompt. If the other mode already holds a different identity with its own settings, Kyokai asks
which one to keep. The first start of 0.5.0 removes what earlier switches left behind in the mode
not in use.

On a Mac the helper sees and controls the screen as **Kyokai Helper**, which needs its own Screen
Recording and Accessibility (Device Control and Data Access) permissions: after switching, open
**Privacy permissions** in Permissions, click **Allow…**, switch Kyokai Helper on in the pane that
opens, then click **Restart Kyokai Helper**. Switching back to No privileges on a Mac needs no
password.

## AI on this computer

On the **AI** page, under **AI providers**, add a provider from its template (Claude and OpenAI
are there already; OpenRouter, Gemini, Qwen, Ollama and many more are a click away, or **Custom**
for any other), enter your API key, add models by id or with **List models**, and choose the
**Model** tasks use. Other providers need a model that accepts images and calls tools. Choose also and when to be asked: **Flagged steps and system shortcuts** (the default) or **Every
action**. The key stays in this computer's secrets store and is never shown again or sent anywhere
but to the provider. Then describe a task and choose **Start**.

While the task works you see what the model says and each action it takes. An action that needs
your approval waits in a framed box with **Allow** and **Deny**; a banner on the other pages and a
notification tell you. **Pause**, **Resume** and **Stop** are always at hand. When the task is done
you can reply to continue it. **Task history** keeps finished tasks (never screenshots) until you
clear it.

The AI sends screenshots of this computer's screen to the provider you chose. Kyokai's own windows
are left out of them, and out of screen sharing and other remote-desktop tools too. To see and
change Kyokai through another remote-desktop tool, for example on a computer nobody sits at, turn
off **Hide Kyokai from screen capture** in Security (Kyokai asks first): the AI then sees Kyokai's
windows as well, and people controlling this computer through Kyokai can use them too. The AI never
can, and nobody can while a connection request or an AI approval waits for you.

## AI on another computer

With **Run AI tasks**, the connection's tab has an AI panel (hide it with the **AI** switch): describe
the task and start it. It runs on the other computer with that computer's provider and key, and
the panel follows it. Everyone connected to that computer sees the same task and the same draft as
it is typed, and so does its own AI page. With
**Approve AI actions** you can answer its approvals; the other computer's user can always answer
them too, and sees a banner while your task works there, with **Stop** at hand.

## Safety

- Using the keyboard or mouse on a computer pauses its AI task at once; moving the mouse just after
  clicking Resume or Allow does not.
- **Ctrl+Alt+Shift+Esc** stops a running AI task, as does **Stop AI task** in the tray menu. It also
  gives you your keyboard and mouse back when a controlling person blocked them.
- A person at the controlling device who uses the mouse or keyboard in the viewer takes over: the
  AI task pauses first.
- When the controlling device disconnects, its AI task pauses.
- Network membership alone grants nothing; every session needs your password or your confirmation.
- **Protected mode** (Security; needs a permanent password) leaves only the AI settings and
  AI tasks in Kyokai on this computer, for someone who should use the AI here but change nothing
  else. To turn it off, enter the permanent password on the AI page.
- **No logs** (Security) keeps Kyokai from writing logs or crash reports and from keeping a
  record of connections: trusted devices and Recent last only until Kyokai quits, and the logs
  already written are deleted. It is a trade-off: afterwards you can't find out who connected to
  this computer or which devices it connected to, so Kyokai asks before turning it on.

## Back up your identity

Security, **Device identity**, can **Back up…** this device's identity into a file protected by a
passphrase you choose, and **Restore…** it on this or another computer, which then has this Device
ID. **Reset…** gives this computer a new identity and Device ID; devices that knew the old one see a
warning and can choose **Forget old identity**.

## When something is wrong

- "The Kyokai engine is not running.": its background part stopped; the reason is shown, and Kyokai
  starts it again. Logs are in the folder that About, Diagnostics, opens; **Save logs…** there puts
  them, Kyokai Helper's included (whose folder needs administrator rights), into one file to send.
- The network shows an error: check the server addresses, the name and the secret.
- The other device is reached through a relay: turn on **Try direct connections** (Advanced on the Network
  page) on both devices; if it stays relayed, the networks block direct paths: it works, a little slower.
- An AI task stops with an error: the reason (an unaccepted key, a provider that did not answer)
  is shown with the task.
- Kyokai stops answering or closes: with logs on, `gui-crash.log` and `engine.log` in the logs
  folder (About, Diagnostics) say where it was — a frozen window writes its state there after 10
  seconds. On a Mac, `kill -QUIT` and the Kyokai process number write it at once, the system keeps a
  crashed engine's report in `~/Library/Logs/DiagnosticReports` (`kyokai-svc-…`), and
  `log show --last 1h --predicate 'process == "Kyokai" OR process == "kyokai-svc"'` shows what
  macOS noted; send those with a description of what happened.
