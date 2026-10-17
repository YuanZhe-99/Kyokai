# User guide

Kyokai lets you see and operate another computer, and lets an AI operate a computer for its user —
always on the computer being operated, with that computer's own AI provider key. This guide is
for the people using it; how it works is in the other pages of this folder.

## Install and start

On Windows, run the installer: it installs for your user only and asks for no administrator rights.
Kyokai starts with its window and an icon in the notification area; closing the window keeps it
running there (Settings, General, can make the close button quit instead). Kyokai does not start
with Windows unless you switch on "Open at login".

On macOS, Kyokai asks on its first start for **Screen Recording** (so others can see this screen)
and **Accessibility** (so they can use the mouse and keyboard). Allow both in System Settings,
Privacy & Security, then quit and reopen Kyokai; **Privacy permissions** on Home and in Settings
shows what is still missing and opens the right pane with **Allow…**.

Kyokai keeps its secrets — this device's identity, passwords, keys — encrypted with your Windows
account. If that is not possible, Home says so: the Device ID then changes on every start.

## This device

Home shows **This device**: the Device ID, nine digits that others use to reach this computer, and
the **Temporary password**, hidden until you choose Show. The temporary password changes every
time Kyokai starts and whenever you choose **New password**; give it only to someone you want to let
in. Settings, Access, can also set a permanent password, for devices you trust over time.
Settings, Device identity, sets this computer's **Friendly name**, which others see beside the
Device ID once they have logged in; left empty, it is the computer's name.

## Join a network

Devices find each other through an EasyTier network. On the **Network** page enter the
**Servers** (one address per line), the **Network name** and the **Network secret** that whoever runs
your network gave you, and save. The page then shows the network's state, this device's address
and the Kyokai devices it can reach, directly or through a relay. A device's announced ID is only a
hint: who a device is gets proven when you connect. **Leave network** disconnects. Connections go
through the server unless you switch on **Try direct connections**, which is faster where both
networks allow it; Windows may then ask to let Kyokai through its firewall.

## Control another device

Under **Control another device** on Home, enter the other device's Device ID and its password and
choose **Connect**. By default you ask to see the screen, use the mouse and keyboard, share the
clipboard and copy files; switch on **Also ask to run AI tasks there** to ask for the AI permissions as well. With
the right password you are in at once with what the other computer lets a password grant; for
anything more its user is asked. Leave the password empty and choose **Ask to connect** to ask its
user without one. The other computer's user chooses what to allow:

| Permission | Lets you |
|---|---|
| See the screen | watch the other computer's screen |
| Use the mouse and keyboard | operate it from the picture |
| Share the clipboard | copy text on one side and paste it on the other |
| Copy files | copy files or folders on one side and paste them on the other (up to 1000 items and 1 GiB at a time) |
| Run AI tasks | give the other computer's AI a task and steer it |
| Approve AI actions | answer the approvals of those tasks |

Once connected, **View** opens the other screen in its own window. The bar below the picture sets
the quality (**Faster**, **Balanced**, **Sharper**) and a frame-rate cap. Click the picture to use
the mouse and keyboard there; Kyokai's own windows on the other computer never receive your
input. The bar above it can **Lock screen**, send **Ctrl+Alt+Del** (when the other computer runs
Kyokai as a helper) and **Block their keyboard and mouse** while you work, or turn on **Privacy mode**, which also
blacks out their screen while you still see it; its user can always lift both with
Ctrl+Alt+Shift+Esc or Ctrl+Alt+Del. On Windows the bar also chooses how your keyboard reaches the
other computer: **Map** (the default) sends each key by its place, so the other computer's layout
decides what it types and shortcuts such as the Windows key or Alt+Tab go there too; **Translate**
types with your own layout and input method; **Legacy** sends only the keys the viewer window
receives. Keys go there only while the picture has the focus: click it first. While you are connected the other computer does not go to
sleep by itself. **Disconnect** ends the session; closing the window only stops the picture. A session that
drops reconnects by itself for a minute without asking again.

## Let someone control this device

Someone with your password gets in at once with what Settings, Access, **With a correct password**
allows (by default seeing the screen, the mouse and keyboard, the clipboard and files, but no AI
tasks). Otherwise — they ask for more, ask without a password, or a known Device ID arrives with a
new key — a request appears at the top of every page with their Device ID, a warning in the last
two cases, and a switch for each permission they asked for. Choose what to allow, then **Allow**, or **Deny**. Devices you allowed are listed in Settings,
Access; **Remove** revokes one at once and ends its sessions. **Connect without asking** lets that
device in for 30 days with the permanent password alone, for a computer nobody sits at. Your own use
of the keyboard and mouse always wins over theirs and over any AI.

## Run Kyokai as a helper

Settings, **Run mode**, chooses where this device runs. **No privileges** (the default) needs
nothing. **Follow Kyokai** and **Background service** install a helper once, after one
administrator prompt, so the other side can also see and use sign-in screens and administrator
prompts on this computer; Background service also lets devices you allowed to connect without
asking in before anyone signs in. Your Device ID, trusted devices and settings move along.
Choosing No privileges again removes the helper.

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
off **Hide Kyokai from screen capture** in Settings; the AI then sees Kyokai's windows as well.

## AI on another computer

In a session with **Run AI tasks**, the viewer has an AI panel: describe the task and start it. It
runs on the other computer with that computer's provider and key, and the panel follows it. With
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

## Back up your identity

Settings, **Device identity**, can **Back up…** this device's identity into a file protected by a
passphrase you choose, and **Restore…** it on this or another computer, which then has this Device
ID. **Reset…** gives this computer a new identity and Device ID; devices that knew the old one see a
warning and can choose **Forget old identity**.

## When something is wrong

- "The Kyokai engine is not running.": its background part stopped; the reason is shown, and Kyokai
  starts it again. Logs are in the folder that Settings, Diagnostics, opens.
- The network shows an error: check the server addresses, the name and the secret.
- The other device is reached through a relay: both networks block direct paths; it works, a
  little slower.
- An AI task stops with an error: the reason (an unaccepted key, a provider that did not answer)
  is shown with the task.
