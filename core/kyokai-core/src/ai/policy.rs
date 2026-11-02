//! The approval policy (doc/en-us/computer-use.md, "Approvals"): whether an
//! action the model proposes runs, waits for a person's approval, or is
//! refused, decided from the action and facts the system gives about its
//! target — never from what the model says about it. The model cannot
//! change it, and nothing an approval grants outlives the one action shown.

use serde::{Deserialize, Serialize};

use super::action::{Action, Key};

/// When a task asks before acting.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum Approval {
    /// Asks for what the policy's classes name, and whenever it cannot
    /// tell what an action would reach.
    #[default]
    #[serde(alias = "flagged")]
    Auto,
    /// Before every action.
    EveryAction,
}

/// What the screen shows where input would go, as the input desktop and
/// the session's lock state tell.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum Surface {
    /// The user's own desktop.
    #[default]
    Desktop,
    /// A UAC consent prompt (the secure desktop, helper modes).
    Consent,
    /// A prompt for a password or PIN (UAC's credential prompt, a system
    /// sign-in sheet).
    Credentials,
    /// The lock screen or the sign-in screen.
    Locked,
    /// A secure surface this build cannot look into (No privileges: the
    /// input desktop is not readable).
    Unknown,
}

/// A control's role, as UI Automation or Accessibility report it.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum Role {
    Password,
    Text,
    Button,
    Other,
}

/// What an action would reach, as the system reports it. `None` fields are
/// not known (the platform cannot tell, or nothing is there).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Target {
    pub surface: Surface,
    /// The program owning the window: its executable's file name on Windows
    /// and Linux, its app's name on macOS (`Terminal.app` is "terminal");
    /// lowercase.
    pub program: Option<String>,
    /// The control under the point or with the focus.
    pub role: Option<Role>,
    /// The control's name (its label), lowercase.
    pub name: Option<String>,
    /// Whether the window is a file chooser (open or save).
    pub file_dialog: bool,
}

/// Why an action asks or is refused: a class of the policy's table.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum Class {
    /// Class 0: a UAC consent, a credential prompt, the lock or sign-in
    /// screen.
    SecureSurface,
    /// Typing where a password goes.
    Credentials,
    /// The provider flagged the turn.
    Flagged,
    /// Every action asks in this mode.
    EveryAction,
    /// What the action would reach is not known.
    Unknown,
    /// Shortcuts with the Windows or Command key.
    SystemShortcut,
    /// Shutting down, restarting, signing out, locking.
    PowerSession,
    /// System and security settings, the registry, the network's settings.
    Settings,
    /// Deleting in a file manager.
    Deleting,
    /// Choosing a file to upload or save (file dialogs).
    Uploading,
    /// A password manager.
    PasswordVault,
    /// Installing software.
    Installing,
    /// Typing into a terminal.
    Terminal,
    /// Ending programs (Task Manager, Activity Monitor).
    ProcessControl,
    /// Sending a message or mail.
    Sending,
    /// Paying or buying.
    Payments,
    /// Agreeing, allowing or granting.
    Consent,
    /// A Remote Desktop or VNC window (no element tree, pixels only).
    RemoteDesktop,
}

/// What the policy decided for one action.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Verdict {
    Run,
    Ask(Class),
    Refuse(Class),
    /// The session is locked: the task pauses.
    Pause,
}

/// Refusals in one task after which it asks before every action.
pub const REFUSALS_BEFORE_EVERY_ACTION: u32 = 3;

/// Decides one action.
/// Inputs: `mode` — the task's approval mode; `flagged` — the provider
/// flagged this turn and the host user has not acknowledged it; `target` —
/// what the action would reach (only looked at for actions that reach
/// something).
/// Notes: the order is fixed: refusals and secure surfaces, then what never
/// loosens (consent, credentials, flags), then the mode; risk signals add
/// up to the worst one, and an unknown target asks.
pub fn decide(mode: Approval, flagged: bool, action: &Action, target: &Target) -> Verdict {
    let reaches = reaches(action);
    if reaches {
        match target.surface {
            Surface::Locked | Surface::Unknown => return Verdict::Pause,
            Surface::Credentials => return Verdict::Refuse(Class::SecureSurface),
            Surface::Consent => return Verdict::Ask(Class::SecureSurface),
            Surface::Desktop => {}
        }
        if types(action) && target.role == Some(Role::Password) {
            return Verdict::Ask(Class::Credentials);
        }
    }
    if flagged {
        return Verdict::Ask(Class::Flagged);
    }
    if mode == Approval::EveryAction {
        return Verdict::Ask(Class::EveryAction);
    }
    if let Some(class) = shortcut(action) {
        return Verdict::Ask(class);
    }
    if !reaches || !acts(action) {
        return Verdict::Run;
    }
    let Some(program) = target.program.as_deref() else {
        return Verdict::Ask(Class::Unknown);
    };
    match classify(program, action, target) {
        Some(class) => Verdict::Ask(class),
        None => Verdict::Run,
    }
}

/// Whether an action reaches a window (screenshots, waits and the cursor's
/// position do not).
fn reaches(a: &Action) -> bool {
    !matches!(
        a,
        Action::Screenshot | Action::Zoom { .. } | Action::Wait { .. } | Action::CursorPosition
    )
}

/// Whether an action can change something: a pointer move alone does not.
fn acts(a: &Action) -> bool {
    !matches!(a, Action::Move { .. })
}

/// Whether an action types (text or keys).
fn types(a: &Action) -> bool {
    matches!(
        a,
        Action::Type { .. } | Action::KeyCombo { .. } | Action::HoldKey { .. }
    )
}

/// The keys an action presses, if it presses any.
fn keys(a: &Action) -> &[Key] {
    match a {
        Action::KeyCombo { keys, .. } | Action::HoldKey { keys, .. } => keys,
        _ => &[],
    }
}

/// Key combinations that are a class on their own, wherever they go.
fn shortcut(a: &Action) -> Option<Class> {
    let keys = keys(a);
    let has = |k: &Key| keys.contains(k);
    let named = |n: &str| presses(keys, n);
    if has(&Key::Ctrl) && has(&Key::Alt) && named("delete") {
        return Some(Class::PowerSession);
    }
    if has(&Key::Ctrl) && has(&Key::Shift) && named("escape") {
        return Some(Class::ProcessControl);
    }
    if has(&Key::Meta) {
        if keys.contains(&Key::Char('l')) {
            return Some(Class::PowerSession);
        }
        return Some(Class::SystemShortcut);
    }
    None
}

/// Whether `keys` has the named key (`normalize_key`'s names).
fn presses(keys: &[Key], name: &str) -> bool {
    keys.iter()
        .any(|k| matches!(k, Key::Named(n) if *n == name))
}

/// The class of an action reaching `program`, by the policy's table.
fn classify(program: &str, action: &Action, target: &Target) -> Option<Class> {
    let in_list = |list: &[&str]| list.contains(&program);
    if in_list(REMOTE_DESKTOP) {
        return Some(Class::RemoteDesktop);
    }
    if in_list(VAULTS) {
        return Some(Class::PasswordVault);
    }
    if in_list(INSTALLERS) {
        return Some(Class::Installing);
    }
    if in_list(SETTINGS) {
        return Some(Class::Settings);
    }
    if in_list(PROCESS_CONTROL) {
        return Some(Class::ProcessControl);
    }
    if in_list(TERMINALS) && types(action) {
        return Some(Class::Terminal);
    }
    if target.file_dialog {
        return Some(Class::Uploading);
    }
    let presses = |n: &str| presses(keys(action), n);
    if in_list(FILE_MANAGERS)
        && (presses("delete") || (keys(action).contains(&Key::Meta) && presses("backspace")))
    {
        return Some(Class::Deleting);
    }
    let named = |words: &[&str]| {
        target
            .name
            .as_deref()
            .is_some_and(|n| words.iter().any(|w| n.contains(w)))
    };
    let clicks = matches!(action, Action::Click { .. });
    if in_list(MESSAGING) && (presses("enter") || clicks && named(SEND_WORDS)) {
        return Some(Class::Sending);
    }
    if in_list(BROWSERS) && clicks && named(PAY_WORDS) {
        return Some(Class::Payments);
    }
    if (in_list(BROWSERS) || in_list(SYSTEM_DIALOGS)) && clicks && named(CONSENT_WORDS) {
        return Some(Class::Consent);
    }
    None
}

// The table's programs: Windows and Linux executables, macOS apps' names;
// lowercase.

const REMOTE_DESKTOP: &[&str] = &[
    "mstsc.exe",
    "msrdc.exe",
    "vncviewer.exe",
    "tvnviewer.exe",
    "rustdesk.exe",
    "anydesk.exe",
    "teamviewer.exe",
    "screen sharing",
    "microsoft remote desktop",
    "windows app",
    "vnc viewer",
    "rustdesk",
    "anydesk",
    "teamviewer",
    "remmina",
    "vncviewer",
];
const VAULTS: &[&str] = &[
    "1password.exe",
    "keepass.exe",
    "keepassxc.exe",
    "bitwarden.exe",
    "1password",
    "1password 7",
    "bitwarden",
    "keepassxc",
    "keychain access",
    "passwords",
    "seahorse",
];
const INSTALLERS: &[&str] = &[
    "msiexec.exe",
    "appinstaller.exe",
    "installer",
    "pkexec",
    "gnome-software",
    "plasma-discover",
];
const SETTINGS: &[&str] = &[
    "systemsettings.exe",
    "control.exe",
    "mmc.exe",
    "regedit.exe",
    "sechealthui.exe",
    "netsh.exe",
    "gpedit.exe",
    "system settings",
    "system preferences",
    "gnome-control-center",
    "systemsettings",
];
const PROCESS_CONTROL: &[&str] = &[
    "taskmgr.exe",
    "procexp.exe",
    "procexp64.exe",
    "activity monitor",
    "gnome-system-monitor",
    "plasma-systemmonitor",
];
const TERMINALS: &[&str] = &[
    "cmd.exe",
    "powershell.exe",
    "pwsh.exe",
    "windowsterminal.exe",
    "wt.exe",
    "conhost.exe",
    "openconsole.exe",
    "mintty.exe",
    "wsl.exe",
    "alacritty.exe",
    "wezterm-gui.exe",
    "terminal",
    "iterm",
    "iterm2",
    "wezterm",
    "alacritty",
    "kitty",
    "ghostty",
    "warp",
    "gnome-terminal-server",
    "konsole",
    "xterm",
    "wezterm-gui",
    "tilix",
    "xfce4-terminal",
];
const FILE_MANAGERS: &[&str] = &[
    "explorer.exe",
    "finder",
    "nautilus",
    "dolphin",
    "nemo",
    "thunar",
];
const MESSAGING: &[&str] = &[
    "outlook.exe",
    "olk.exe",
    "thunderbird.exe",
    "slack.exe",
    "ms-teams.exe",
    "teams.exe",
    "discord.exe",
    "telegram.exe",
    "weixin.exe",
    "wechat.exe",
    "qq.exe",
    "whatsapp.exe",
    "signal.exe",
    "lark.exe",
    "feishu.exe",
    "dingtalk.exe",
    "mail",
    "messages",
    "microsoft outlook",
    "microsoft teams",
    "slack",
    "discord",
    "telegram",
    "wechat",
    "qq",
    "whatsapp",
    "signal",
    "thunderbird",
    "lark",
    "feishu",
    "dingtalk",
    "telegram-desktop",
    "signal-desktop",
];
const BROWSERS: &[&str] = &[
    "chrome.exe",
    "msedge.exe",
    "firefox.exe",
    "brave.exe",
    "opera.exe",
    "vivaldi.exe",
    "safari",
    "google chrome",
    "microsoft edge",
    "firefox",
    "brave browser",
    "arc",
    "chrome",
    "chromium",
    "brave",
    "msedge",
];
const SYSTEM_DIALOGS: &[&str] = &[
    "usernotificationcenter",
    "universalaccessauthwarn",
    "coreservicesuiagent",
];
const SEND_WORDS: &[&str] = &["send", "发送", "傳送", "送信", "送出"];
const PAY_WORDS: &[&str] = &[
    "pay",
    "buy",
    "place order",
    "purchase",
    "checkout",
    "支付",
    "付款",
    "购买",
    "購買",
    "下单",
    "購入",
    "支払",
];
const CONSENT_WORDS: &[&str] = &[
    "allow",
    "accept",
    "agree",
    "grant",
    "authorize",
    "允许",
    "同意",
    "接受",
    "授权",
    "允許",
    "授權",
    "許可",
    "承認",
];

#[cfg(test)]
mod tests {
    use super::*;
    use crate::ai::action::{Button, Point};

    fn on(program: &str) -> Target {
        Target {
            program: Some(program.into()),
            ..Default::default()
        }
    }

    fn click() -> Action {
        Action::Click {
            at: Some(Point { x: 10, y: 10 }),
            button: Button::Left,
            count: 1,
            modifiers: vec![],
        }
    }

    fn typing() -> Action {
        Action::Type { text: "dir".into() }
    }

    fn combo(keys: &[Key]) -> Action {
        Action::KeyCombo {
            keys: keys.to_vec(),
            repeat: 1,
        }
    }

    /// The classes and the decision order, in Auto unless a row says.
    #[test]
    fn the_policy_decides_by_class_and_order() {
        use Approval::*;
        use Verdict::*;
        let named = |program: &str, name: &str| Target {
            name: Some(name.into()),
            ..on(program)
        };
        let surface = |s: Surface| Target {
            surface: s,
            ..on("notepad.exe")
        };
        let password = Target {
            role: Some(Role::Password),
            ..on("chrome.exe")
        };
        let dialog = Target {
            file_dialog: true,
            ..on("chrome.exe")
        };
        let enter = Key::Named("enter");
        let delete = Key::Named("delete");
        let escape = Key::Named("escape");
        // (mode, flagged, action, target, verdict, why)
        let cases: Vec<(Approval, bool, Action, Target, Verdict, &str)> = vec![
            (
                Auto,
                false,
                click(),
                on("notepad.exe"),
                Run,
                "an ordinary app",
            ),
            (
                Auto,
                false,
                typing(),
                on("notepad.exe"),
                Run,
                "typing into an editor",
            ),
            (
                EveryAction,
                false,
                click(),
                on("notepad.exe"),
                Ask(Class::EveryAction),
                "every action",
            ),
            (
                Auto,
                true,
                click(),
                on("notepad.exe"),
                Ask(Class::Flagged),
                "a flagged turn",
            ),
            (
                Auto,
                false,
                click(),
                Target::default(),
                Ask(Class::Unknown),
                "an unknown target",
            ),
            (
                Auto,
                false,
                Action::Screenshot,
                Target::default(),
                Run,
                "looking reaches nothing",
            ),
            (
                Auto,
                false,
                Action::Move {
                    to: Point { x: 1, y: 1 },
                    held: vec![],
                },
                Target::default(),
                Run,
                "a move changes nothing",
            ),
            // Class 0, whatever the mode.
            (
                Auto,
                false,
                click(),
                surface(Surface::Locked),
                Pause,
                "the lock screen pauses",
            ),
            (
                Auto,
                false,
                click(),
                surface(Surface::Unknown),
                Pause,
                "an unreadable secure desktop pauses",
            ),
            (
                Auto,
                false,
                typing(),
                surface(Surface::Credentials),
                Refuse(Class::SecureSurface),
                "credential prompts are refused",
            ),
            (
                EveryAction,
                false,
                click(),
                surface(Surface::Credentials),
                Refuse(Class::SecureSurface),
                "refused in every mode",
            ),
            (
                Auto,
                false,
                click(),
                surface(Surface::Consent),
                Ask(Class::SecureSurface),
                "UAC consent asks",
            ),
            (
                Auto,
                false,
                Action::Screenshot,
                surface(Surface::Locked),
                Run,
                "the AI may see the lock screen",
            ),
            (
                Auto,
                false,
                typing(),
                password.clone(),
                Ask(Class::Credentials),
                "typing a password asks",
            ),
            (
                EveryAction,
                true,
                typing(),
                password,
                Ask(Class::Credentials),
                "credentials before flags and modes",
            ),
            // Shortcuts.
            (
                Auto,
                false,
                combo(&[Key::Meta, Key::Char('r')]),
                on("explorer.exe"),
                Ask(Class::SystemShortcut),
                "Win+R",
            ),
            (
                Auto,
                false,
                combo(&[Key::Meta, Key::Char('l')]),
                on("explorer.exe"),
                Ask(Class::PowerSession),
                "Win+L locks",
            ),
            (
                Auto,
                false,
                combo(&[Key::Ctrl, Key::Alt, delete.clone()]),
                on("notepad.exe"),
                Ask(Class::PowerSession),
                "Ctrl+Alt+Del",
            ),
            (
                Auto,
                false,
                combo(&[Key::Ctrl, Key::Shift, escape]),
                on("notepad.exe"),
                Ask(Class::ProcessControl),
                "Ctrl+Shift+Esc",
            ),
            (
                Auto,
                false,
                combo(&[Key::Meta, Key::Char('c')]),
                Target::default(),
                Ask(Class::SystemShortcut),
                "Command+C on an unknown target",
            ),
            // Programs.
            (
                Auto,
                false,
                typing(),
                on("powershell.exe"),
                Ask(Class::Terminal),
                "typing into a terminal",
            ),
            (
                Auto,
                false,
                typing(),
                on("terminal"),
                Ask(Class::Terminal),
                "typing into Terminal.app",
            ),
            (
                Auto,
                false,
                click(),
                on("terminal"),
                Run,
                "clicking a terminal",
            ),
            (
                Auto,
                false,
                click(),
                on("1password.exe"),
                Ask(Class::PasswordVault),
                "a vault",
            ),
            (
                Auto,
                false,
                click(),
                on("mstsc.exe"),
                Ask(Class::RemoteDesktop),
                "Remote Desktop",
            ),
            (
                Auto,
                false,
                click(),
                on("msiexec.exe"),
                Ask(Class::Installing),
                "an installer",
            ),
            (
                Auto,
                false,
                click(),
                on("system settings"),
                Ask(Class::Settings),
                "Settings",
            ),
            (
                Auto,
                false,
                click(),
                on("taskmgr.exe"),
                Ask(Class::ProcessControl),
                "Task Manager",
            ),
            (
                Auto,
                false,
                click(),
                dialog,
                Ask(Class::Uploading),
                "a file dialog",
            ),
            (
                Auto,
                false,
                combo(&[delete]),
                on("explorer.exe"),
                Ask(Class::Deleting),
                "Delete in Explorer",
            ),
            (
                Auto,
                false,
                combo(&[Key::Meta, Key::Named("backspace")]),
                on("finder"),
                Ask(Class::SystemShortcut),
                "Command+Backspace is a shortcut first",
            ),
            (
                Auto,
                false,
                combo(std::slice::from_ref(&enter)),
                on("slack.exe"),
                Ask(Class::Sending),
                "Enter in a chat",
            ),
            (
                Auto,
                false,
                combo(&[enter]),
                on("notepad.exe"),
                Run,
                "Enter in an editor",
            ),
            (
                Auto,
                false,
                click(),
                named("outlook.exe", "send"),
                Ask(Class::Sending),
                "Send in mail",
            ),
            (
                Auto,
                false,
                click(),
                named("weixin.exe", "发送(s)"),
                Ask(Class::Sending),
                "Send in WeChat",
            ),
            (
                Auto,
                false,
                click(),
                named("chrome.exe", "place order"),
                Ask(Class::Payments),
                "paying",
            ),
            (
                Auto,
                false,
                click(),
                named("chrome.exe", "accept all"),
                Ask(Class::Consent),
                "consent in a browser",
            ),
            (
                Auto,
                false,
                click(),
                named("notepad.exe", "send"),
                Run,
                "words count only in known apps",
            ),
            (
                Auto,
                false,
                click(),
                named("chrome.exe", "search"),
                Run,
                "an ordinary page",
            ),
        ];
        for (mode, flagged, action, target, want, why) in cases {
            assert_eq!(decide(mode, flagged, &action, &target), want, "{why}");
        }
    }

    /// Settings from before 0.7.0 said "flagged": that is Auto now.
    #[test]
    fn flagged_settings_read_as_auto() {
        let a: Approval = serde_json::from_str("\"flagged\"").unwrap();
        assert_eq!(a, Approval::Auto);
        assert_eq!(serde_json::to_string(&Approval::Auto).unwrap(), "\"auto\"");
    }
}
