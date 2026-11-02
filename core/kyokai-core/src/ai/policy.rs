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
    /// The folder a file manager's window or a file chooser shows, as an
    /// absolute path; None when it is not a file view, or the folder is
    /// not one on disk (Desktop, Libraries, This PC) or cannot be read.
    pub folder: Option<String>,
}

/// What the host user granted the AI in this session (AI scope); a None
/// part is not limited. Neither part implies the other.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
pub struct Scope {
    /// The programs, as `Target::program` names them.
    pub apps: Option<Vec<String>>,
    /// The folders, absolute paths: checked, not enforced (the file views'
    /// folders, typed paths, and changes seen while the AI acts).
    pub folders: Option<Vec<String>>,
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
    /// A program outside the apps the host user granted (AI scope).
    OutsideScope,
    /// A folder outside the folders the host user granted (AI scope).
    OutsideFolders,
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
/// `scope` — what the host user granted (AI scope).
/// Notes: the order is fixed: refusals and secure surfaces, then what never
/// loosens (consent, credentials, flags), then the mode; risk signals add
/// up to the worst one, and an unknown target asks.
pub fn decide(
    mode: Approval,
    flagged: bool,
    action: &Action,
    target: &Target,
    scope: &Scope,
) -> Verdict {
    let reaches = reaches(action);
    if reaches {
        match target.surface {
            Surface::Locked | Surface::Unknown => return Verdict::Pause,
            Surface::Credentials => return Verdict::Refuse(Class::SecureSurface),
            Surface::Consent => return Verdict::Ask(Class::SecureSurface),
            Surface::Desktop => {}
        }
        // AI scope: only the granted programs' windows, no switching away;
        // a system picker or notification asks, anything else is refused
        // (an unknown program too).
        if let Some(apps) = &scope.apps
            && acts(action)
        {
            if switches_app(action) {
                return Verdict::Refuse(Class::OutsideScope);
            }
            match target.program.as_deref() {
                Some(p) if apps.iter().any(|a| a == p) => {}
                Some(p) if SYSTEM_SURFACES.contains(&p) => {
                    return Verdict::Ask(Class::OutsideScope);
                }
                _ => return Verdict::Refuse(Class::OutsideScope),
            }
        }
        if let Some(folders) = &scope.folders
            && acts(action)
            && let Some(v) = folder_check(action, target, folders)
        {
            return v;
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

/// AI scope's folders: a typed path outside them is refused; in a file
/// view whose folder is outside them (or not known), an action asks unless
/// it only scrolls, clicks into a text field (the address bar, a name box),
/// or types paths that are all inside them — the way back in.
/// Returns: the verdict, or None when the folders have nothing to say.
fn folder_check(action: &Action, target: &Target, folders: &[String]) -> Option<Verdict> {
    let mut typed_in = false;
    if let Action::Type { text } = action {
        let paths = typed_paths(text, target.folder.as_deref(), &|v| std::env::var(v).ok());
        if paths
            .iter()
            .any(|p| !p.as_deref().is_some_and(|p| within(p, folders)))
        {
            return Some(Verdict::Refuse(Class::OutsideFolders));
        }
        typed_in = !paths.is_empty();
    }
    let file_view = target.file_dialog
        || target
            .program
            .as_deref()
            .is_some_and(|p| FILE_MANAGERS.contains(&p));
    if !file_view || typed_in || target.folder.as_deref().is_some_and(|f| within(f, folders)) {
        return None;
    }
    let harmless = match action {
        Action::Scroll { .. } => true,
        Action::Click { .. } => target.role == Some(Role::Text),
        _ => false,
    };
    (!harmless).then_some(Verdict::Ask(Class::OutsideFolders))
}

/// The paths typed text names: absolute ones (`C:\…`, `\\server\…`, and
/// `/…` at the start or after a space or quote), the home folder's and
/// other variables' (`~/…`, `%USERPROFILE%\…`, `$HOME/…`), and relative
/// ones that climb (`../…`), taken against `folder` (the file view's).
/// Each runs to the end of its line or a quote, since names hold spaces.
/// Inputs: `var` reads an environment variable.
/// Returns: each path made absolute, or None where it cannot be (an unknown
/// variable, a climbing path without a folder).
pub fn typed_paths(
    text: &str,
    folder: Option<&str>,
    var: &dyn Fn(&str) -> Option<String>,
) -> Vec<Option<String>> {
    let mut out = Vec::new();
    for line in text.split(['\n', '\r']) {
        let mut i = 0;
        let mut prev: Option<char> = None;
        while let Some(c) = line[i..].chars().next() {
            let s = &line[i..];
            let gap = prev.is_none_or(|p| p.is_whitespace() || matches!(p, '"' | '\'' | '=' | '('));
            let b = s.as_bytes();
            let sep = |n: usize| matches!(b.get(n), Some(b'\\' | b'/'));
            // Where the path starts, and the absolute path its start stands for.
            let start: Option<(usize, Option<String>)> = if c.is_ascii_alphabetic()
                && b.get(1) == Some(&b':')
                && sep(2)
                && !prev.is_some_and(|p| p.is_alphanumeric())
            {
                Some((0, Some(String::new())))
            } else if !gap {
                None
            } else if s.starts_with("\\\\") {
                Some((0, Some(String::new())))
            } else if c == '~' && sep(1) {
                Some((1, var("USERPROFILE").or_else(|| var("HOME"))))
            } else if c == '%' {
                s[1..].find('%').map(|end| (end + 2, var(&s[1..end + 1])))
            } else if c == '$' {
                let name = s[1..].trim_start_matches('{');
                let len = name
                    .find(|c: char| !(c.is_ascii_alphanumeric() || c == '_'))
                    .unwrap_or(name.len());
                let skip = s.len() - name.len() + len;
                let skip = skip + usize::from(s[skip..].starts_with('}'));
                (len > 0).then(|| (skip, var(&name[..len])))
            } else if s.starts_with("..") && sep(2) {
                Some((0, folder.map(|f| format!("{f}/"))))
            } else if c == '/'
                && b.get(1)
                    .is_some_and(|n| !n.is_ascii_whitespace() && *n != b'/')
            {
                Some((0, Some(String::new())))
            } else {
                None
            };
            let Some((skip, base)) = start else {
                prev = Some(c);
                i += c.len_utf8();
                continue;
            };
            let len = s
                .find(['"', '\'', '<', '>', '|'])
                .unwrap_or(s.len())
                .max(skip);
            out.push(base.map(|base| format!("{base}{}", &s[skip..len])));
            prev = s[..len].chars().last();
            i += len.max(1);
        }
    }
    out
}

/// Whether a path is a full one: from a drive (`C:\`), a share (`\\server\`)
/// or the root (`/`).
pub fn absolute(path: &str) -> bool {
    let b = path.as_bytes();
    (b.len() >= 3 && b[0].is_ascii_alphabetic() && b[1] == b':' && matches!(b[2], b'\\' | b'/'))
        || path.starts_with("\\\\")
        || path.starts_with('/')
}

/// Whether `path` is one of `folders` or inside one, compared after
/// resolving `.` and `..`, with either separator; case-insensitive for
/// Windows paths and on macOS.
pub fn within(path: &str, folders: &[String]) -> bool {
    let p = normal(path);
    folders.iter().any(|f| {
        let f = normal(f);
        p == f || p.starts_with(&format!("{}/", f.trim_end_matches('/')))
    })
}

/// A path for comparing: `/` separators, `.` and `..` resolved (never above
/// its root), no trailing separator, lowercase where names ignore case.
fn normal(path: &str) -> String {
    let path = path.trim().replace('\\', "/");
    let b = path.as_bytes();
    let drive = b.len() >= 2 && b[0].is_ascii_alphabetic() && b[1] == b':';
    let share = path.starts_with("//");
    // The root's parts (a drive, or a share's server and name) stay.
    let fixed = if share { 2 } else { usize::from(drive) };
    let mut parts: Vec<&str> = Vec::new();
    for part in path.split('/') {
        match part {
            "" | "." => {}
            ".." => {
                if parts.len() > fixed {
                    parts.pop();
                }
            }
            p => parts.push(p),
        }
    }
    let joined = if share {
        format!("//{}", parts.join("/"))
    } else if drive {
        parts.join("/")
    } else {
        format!("/{}", parts.join("/"))
    };
    if drive || share || cfg!(target_os = "macos") {
        joined.to_lowercase()
    } else {
        joined
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

/// Whether keys switch to another app or window: Alt+Tab, Alt+Esc, the
/// Windows or Command key with Tab or a digit (the taskbar's apps).
fn switches_app(a: &Action) -> bool {
    let keys = keys(a);
    let tab = presses(keys, "tab");
    (keys.contains(&Key::Alt) && (tab || presses(keys, "escape")))
        || (keys.contains(&Key::Meta)
            && (tab
                || keys
                    .iter()
                    .any(|k| matches!(k, Key::Char(c) if c.is_ascii_digit()))))
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
/// System surfaces that come up over a granted app on its behalf: pickers,
/// notifications, the Start and search panels.
const SYSTEM_SURFACES: &[&str] = &[
    "pickerhost.exe",
    "shellexperiencehost.exe",
    "searchhost.exe",
    "startmenuexperiencehost.exe",
    "notificationcenter",
    "usernotificationcenter",
    "xdg-desktop-portal-gnome",
    "xdg-desktop-portal-kde",
    "xdg-desktop-portal-gtk",
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
            assert_eq!(
                decide(mode, flagged, &action, &target, &Scope::default()),
                want,
                "{why}"
            );
        }
    }

    /// AI scope: granted programs only, no switching away; system pickers
    /// ask; moves and screenshots are free; class 0 still comes first.
    #[test]
    fn scope_holds_the_granted_programs() {
        use Verdict::*;
        let scope = Scope {
            apps: Some(vec!["code.exe".into()]),
            folders: None,
        };
        let alt_tab = combo(&[Key::Alt, Key::Named("tab")]);
        let win_digit = combo(&[Key::Meta, Key::Char('2')]);
        let locked = Target {
            surface: Surface::Locked,
            ..on("code.exe")
        };
        let cases: Vec<(Action, Target, Verdict, &str)> = vec![
            (click(), on("code.exe"), Run, "the granted app"),
            (typing(), on("code.exe"), Run, "typing into it"),
            (
                click(),
                on("notepad.exe"),
                Refuse(Class::OutsideScope),
                "another app",
            ),
            (
                click(),
                Target::default(),
                Refuse(Class::OutsideScope),
                "an unknown app",
            ),
            (
                click(),
                on("pickerhost.exe"),
                Ask(Class::OutsideScope),
                "a system picker",
            ),
            (
                alt_tab,
                on("code.exe"),
                Refuse(Class::OutsideScope),
                "Alt+Tab",
            ),
            (
                win_digit,
                on("code.exe"),
                Refuse(Class::OutsideScope),
                "Win+2",
            ),
            (
                Action::Move {
                    to: Point { x: 1, y: 1 },
                    held: vec![],
                },
                on("notepad.exe"),
                Run,
                "a move",
            ),
            (Action::Screenshot, on("notepad.exe"), Run, "a screenshot"),
            (click(), locked, Pause, "the lock screen first"),
        ];
        for (action, target, want, why) in cases {
            assert_eq!(
                decide(Approval::Auto, false, &action, &target, &scope),
                want,
                "{why}"
            );
        }
    }

    /// AI scope's folders: the file view's folder, typed paths, and the
    /// ways back in; the apps grant is not implied.
    #[test]
    fn scope_checks_the_granted_folders() {
        use Verdict::*;
        let scope = Scope {
            apps: None,
            folders: Some(vec![r"C:\Work\Report".into()]),
        };
        let explorer = |folder: Option<&str>, role: Option<Role>| Target {
            folder: folder.map(Into::into),
            role,
            ..on("explorer.exe")
        };
        let dialog = |folder: &str| Target {
            folder: Some(folder.into()),
            file_dialog: true,
            ..on("notepad.exe")
        };
        let typed = |text: &str| Action::Type { text: text.into() };
        let scroll = Action::Scroll {
            at: Some(Point { x: 1, y: 1 }),
            dx: 0,
            dy: 3,
            modifiers: vec![],
        };
        let inside = Some(r"C:\Work\Report\2026");
        let outside = Some(r"C:\Users\me\Documents");
        let cases: Vec<(Action, Target, Verdict, &str)> = vec![
            (
                click(),
                explorer(inside, None),
                Run,
                "a click in a granted folder",
            ),
            (
                click(),
                explorer(Some(r"c:/work/report"), None),
                Run,
                "the folder itself, any case and separator",
            ),
            (
                click(),
                explorer(outside, None),
                Ask(Class::OutsideFolders),
                "a click in another folder",
            ),
            (
                click(),
                explorer(None, None),
                Ask(Class::OutsideFolders),
                "a folder not on disk (This PC)",
            ),
            (
                click(),
                explorer(Some(r"C:\Work\ReportOld"), None),
                Ask(Class::OutsideFolders),
                "a sibling sharing the name's start",
            ),
            (scroll, explorer(outside, None), Run, "scrolling"),
            (
                click(),
                explorer(outside, Some(Role::Text)),
                Run,
                "a click into the address bar",
            ),
            (
                typed(r"C:\Work\Report\q3"),
                explorer(outside, Some(Role::Text)),
                Run,
                "typing the way back in",
            ),
            (
                typed(r"C:\Work\Report\..\Secrets"),
                explorer(inside, Some(Role::Text)),
                Refuse(Class::OutsideFolders),
                "a path climbing out",
            ),
            (
                typed(r"..\..\Secrets"),
                explorer(inside, Some(Role::Text)),
                Refuse(Class::OutsideFolders),
                "a relative path climbing out",
            ),
            (
                typed("notes.txt"),
                explorer(outside, Some(Role::Text)),
                Ask(Class::OutsideFolders),
                "a name typed into another folder",
            ),
            (
                combo(&[Key::Named("enter")]),
                dialog(r"C:\Users\me\Documents"),
                Ask(Class::OutsideFolders),
                "saving in another folder",
            ),
            (
                typed(r"copy D:\elsewhere\a.txt"),
                on("code.exe"),
                Refuse(Class::OutsideFolders),
                "a path typed anywhere",
            ),
            (
                typed("hello world"),
                on("code.exe"),
                Run,
                "plain text elsewhere",
            ),
            (
                click(),
                on("notepad.exe"),
                Run,
                "the folders grant no apps limit",
            ),
        ];
        for (action, target, want, why) in cases {
            assert_eq!(
                decide(Approval::Auto, false, &action, &target, &scope),
                want,
                "{why}"
            );
        }
    }

    /// Typed paths: each kind of start, where it ends, and what cannot be
    /// made absolute.
    #[test]
    fn typed_paths_are_found() {
        let var = |v: &str| match v {
            "USERPROFILE" => Some(r"C:\Users\me".to_string()),
            "HOME" => Some("/home/me".to_string()),
            _ => None,
        };
        let some = |s: &str| Some(s.to_string());
        type Case<'a> = (&'a str, Option<&'a str>, Vec<Option<String>>);
        let cases: Vec<Case> = vec![
            (r"C:\a b\c", None, vec![some(r"C:\a b\c")]),
            (r#"open "D:\x" now"#, None, vec![some(r"D:\x")]),
            (r"\\server\share\f", None, vec![some(r"\\server\share\f")]),
            ("cat /etc/passwd", None, vec![some("/etc/passwd")]),
            ("~/notes", None, vec![some(r"C:\Users\me/notes")]),
            (
                r"%USERPROFILE%\Desktop",
                None,
                vec![some(r"C:\Users\me\Desktop")],
            ),
            ("${HOME}/x", None, vec![some("/home/me/x")]),
            (r"%NOPE%\x", None, vec![None]),
            (r"..\up", Some(r"C:\w"), vec![some(r"C:\w/..\up")]),
            ("../up", None, vec![None]),
            ("https://example.com/a and/or 1/2", None, vec![]),
            ("line one\n/tmp/x", None, vec![some("/tmp/x")]),
        ];
        for (text, folder, want) in cases {
            assert_eq!(typed_paths(text, folder, &var), want, "{text}");
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
