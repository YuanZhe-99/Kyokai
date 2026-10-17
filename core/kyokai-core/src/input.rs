//! The host's input service (doc/en-us/computer-use.md, "Input"): events
//! from a controller allowed to control input, injected into the host's
//! desktop. Nothing reaches Kyokai's own windows; whatever is held is
//! released when the session ends.
//!
//! Injection exists on Windows only so far (macOS comes with the Mac pass).
#![cfg_attr(not(windows), allow(dead_code))]

use serde::Deserialize;

/// One input event from the controller (`{"type": "input", "event": …}`).
/// Pointer positions are fractions of the shared picture (0–1).
#[derive(Debug, Clone, PartialEq, Deserialize)]
#[serde(tag = "kind", rename_all = "kebab-case")]
pub enum Event {
    Pointer {
        action: PointerAction,
        /// 0 primary, 1 secondary, 2 middle.
        #[serde(default)]
        button: u8,
        x: f32,
        y: f32,
    },
    /// Scroll by `dx`/`dy` lines; positive `dy` moves the content down.
    Scroll { dx: f32, dy: f32 },
    /// A key by name (see `vk`), with the modifiers it is pressed with.
    Key {
        key: String,
        down: bool,
        #[serde(default)]
        mods: Mods,
    },
    /// Text to type as it is (characters, not keys).
    Text { text: String },
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum PointerAction {
    Move,
    Down,
    Up,
}

/// Modifier keys held with a key.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Deserialize)]
pub struct Mods {
    #[serde(default)]
    pub shift: bool,
    #[serde(default)]
    pub ctrl: bool,
    #[serde(default)]
    pub alt: bool,
    #[serde(default)]
    pub meta: bool,
}

/// Longest text one event may type.
const MAX_TEXT: usize = 4096;

impl Event {
    /// Checks an event at the protocol boundary.
    pub fn valid(&self) -> bool {
        match self {
            Event::Pointer { button, x, y, .. } => {
                *button <= 2 && (0.0..=1.0).contains(x) && (0.0..=1.0).contains(y)
            }
            Event::Scroll { dx, dy } => {
                dx.is_finite() && dy.is_finite() && dx.abs() <= 100.0 && dy.abs() <= 100.0
            }
            Event::Key { key, .. } => vk(key).is_some(),
            Event::Text { text } => !text.is_empty() && text.len() <= MAX_TEXT,
        }
    }
}

/// The Windows virtual-key code for a key name.
pub fn vk(name: &str) -> Option<u16> {
    let b = name.as_bytes();
    if b.len() == 1 && (b[0].is_ascii_lowercase() || b[0].is_ascii_digit()) {
        return Some(b[0].to_ascii_uppercase() as u16);
    }
    if let Some(n) = name.strip_prefix('f').and_then(|n| n.parse::<u16>().ok())
        && (1..=24).contains(&n)
    {
        return Some(0x6F + n); // VK_F1 = 0x70
    }
    Some(match name {
        "enter" => 0x0D,
        "escape" => 0x1B,
        "backspace" => 0x08,
        "tab" => 0x09,
        "space" => 0x20,
        "delete" => 0x2E,
        "insert" => 0x2D,
        "home" => 0x24,
        "end" => 0x23,
        "page-up" => 0x21,
        "page-down" => 0x22,
        "left" => 0x25,
        "up" => 0x26,
        "right" => 0x27,
        "down" => 0x28,
        "minus" => 0xBD,
        "equal" => 0xBB,
        "comma" => 0xBC,
        "period" => 0xBE,
        "slash" => 0xBF,
        "semicolon" => 0xBA,
        "quote" => 0xDE,
        "bracket-left" => 0xDB,
        "bracket-right" => 0xDD,
        "backslash" => 0xDC,
        "backquote" => 0xC0,
        "context-menu" => 0x5D,
        _ => return None,
    })
}

/// Marks Kyokai's injected events (`dwExtraInfo`), so local input can be
/// told apart from remote input.
pub const INJECTED_TAG: usize = 0x4B59_4F4B; // "KYOK"

#[cfg(windows)]
pub use windows_impl::Injector;

#[cfg(windows)]
mod windows_impl {
    use super::*;
    use std::collections::BTreeSet;
    use std::time::{Duration, Instant};
    use windows::Win32::Foundation::{CloseHandle, HWND};
    use windows::Win32::System::Threading::{
        OpenProcess, PROCESS_NAME_WIN32, PROCESS_QUERY_LIMITED_INFORMATION,
        QueryFullProcessImageNameW,
    };
    use windows::Win32::UI::Input::KeyboardAndMouse::*;
    use windows::Win32::UI::WindowsAndMessaging::{GetForegroundWindow, GetWindowThreadProcessId};
    use windows::core::PWSTR;

    /// A modifier's virtual key and whether `Mods` holds it.
    type Modifier = (u16, fn(&Mods) -> bool);

    const MODIFIERS: [Modifier; 4] = [
        (0x10, |m| m.shift), // VK_SHIFT
        (0x11, |m| m.ctrl),  // VK_CONTROL
        (0x12, |m| m.alt),   // VK_MENU
        (0x5B, |m| m.meta),  // VK_LWIN
    ];

    /// Injects one session's events and remembers what it holds down.
    pub struct Injector {
        keys: BTreeSet<u16>,
        buttons: BTreeSet<u8>,
        guard: (Instant, bool),
    }

    impl Default for Injector {
        fn default() -> Self {
            Injector {
                keys: BTreeSet::new(),
                buttons: BTreeSet::new(),
                guard: (Instant::now() - Duration::from_secs(1), false),
            }
        }
    }

    impl Injector {
        /// Injects an event, unless a Kyokai window has the focus.
        /// Returns: whether it was injected.
        pub fn inject(&mut self, e: &Event) -> bool {
            if !e.valid() || self.kyokai_in_front() {
                return false;
            }
            match e {
                Event::Pointer {
                    action,
                    button,
                    x,
                    y,
                } => {
                    let (ax, ay) = ((x * 65535.0) as i32, (y * 65535.0) as i32);
                    let mut flags = MOUSEEVENTF_MOVE | MOUSEEVENTF_ABSOLUTE;
                    match action {
                        PointerAction::Move => {}
                        PointerAction::Down => {
                            flags |= [
                                MOUSEEVENTF_LEFTDOWN,
                                MOUSEEVENTF_RIGHTDOWN,
                                MOUSEEVENTF_MIDDLEDOWN,
                            ][*button as usize];
                            self.buttons.insert(*button);
                        }
                        PointerAction::Up => {
                            flags |= [
                                MOUSEEVENTF_LEFTUP,
                                MOUSEEVENTF_RIGHTUP,
                                MOUSEEVENTF_MIDDLEUP,
                            ][*button as usize];
                            self.buttons.remove(button);
                        }
                    }
                    send(&[mouse(ax, ay, 0, flags)]);
                }
                Event::Scroll { dx, dy } => {
                    let mut batch = Vec::new();
                    if *dy != 0.0 {
                        batch.push(mouse(0, 0, (-dy * 120.0) as i32, MOUSEEVENTF_WHEEL));
                    }
                    if *dx != 0.0 {
                        batch.push(mouse(0, 0, (dx * 120.0) as i32, MOUSEEVENTF_HWHEEL));
                    }
                    send(&batch);
                }
                Event::Key { key, down, mods } => {
                    let code = vk(key).expect("valid");
                    let mut batch = Vec::new();
                    if *down {
                        self.sync_mods(mods, &mut batch);
                        batch.push(keyboard(code, false));
                        self.keys.insert(code);
                    } else {
                        batch.push(keyboard(code, true));
                        self.keys.remove(&code);
                    }
                    send(&batch);
                }
                Event::Text { text } => {
                    let mut batch = Vec::new();
                    for unit in text.encode_utf16() {
                        batch.push(unicode(unit, false));
                        batch.push(unicode(unit, true));
                    }
                    send(&batch);
                }
            }
            true
        }

        /// Presses or releases modifiers so they match `mods`.
        fn sync_mods(&mut self, mods: &Mods, batch: &mut Vec<INPUT>) {
            for (code, wanted) in MODIFIERS {
                let held = self.keys.contains(&code);
                if wanted(mods) && !held {
                    batch.push(keyboard(code, false));
                    self.keys.insert(code);
                } else if !wanted(mods) && held {
                    batch.push(keyboard(code, true));
                    self.keys.remove(&code);
                }
            }
        }

        /// Releases every key and button this session holds.
        pub fn release_all(&mut self) {
            let mut batch: Vec<INPUT> = self.keys.iter().map(|k| keyboard(*k, true)).collect();
            for b in &self.buttons {
                batch.push(mouse(
                    0,
                    0,
                    0,
                    [
                        MOUSEEVENTF_LEFTUP,
                        MOUSEEVENTF_RIGHTUP,
                        MOUSEEVENTF_MIDDLEUP,
                    ][*b as usize],
                ));
            }
            send(&batch);
            self.keys.clear();
            self.buttons.clear();
        }

        /// Whether the focused window is one of Kyokai's (checked at most
        /// every 200 ms).
        fn kyokai_in_front(&mut self) -> bool {
            if self.guard.0.elapsed() < Duration::from_millis(200) {
                return self.guard.1;
            }
            // SAFETY: plain queries; the process handle is closed below.
            let front = unsafe {
                let hwnd: HWND = GetForegroundWindow();
                let mut pid = 0u32;
                GetWindowThreadProcessId(hwnd, Some(&mut pid));
                match OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, false, pid) {
                    Ok(h) => {
                        let mut name = [0u16; 1024];
                        let mut len = name.len() as u32;
                        let ok = QueryFullProcessImageNameW(
                            h,
                            PROCESS_NAME_WIN32,
                            PWSTR(name.as_mut_ptr()),
                            &mut len,
                        )
                        .is_ok();
                        let _ = CloseHandle(h);
                        ok && is_kyokai(&String::from_utf16_lossy(&name[..len as usize]))
                    }
                    Err(_) => false,
                }
            };
            self.guard = (Instant::now(), front);
            front
        }
    }

    impl Drop for Injector {
        fn drop(&mut self) {
            self.release_all();
        }
    }

    fn send(batch: &[INPUT]) {
        if !batch.is_empty() {
            // SAFETY: a slice of fully initialized INPUT records.
            unsafe { SendInput(batch, std::mem::size_of::<INPUT>() as i32) };
        }
    }

    fn mouse(dx: i32, dy: i32, data: i32, flags: MOUSE_EVENT_FLAGS) -> INPUT {
        INPUT {
            r#type: INPUT_MOUSE,
            Anonymous: INPUT_0 {
                mi: MOUSEINPUT {
                    dx,
                    dy,
                    mouseData: data as u32,
                    dwFlags: flags,
                    time: 0,
                    dwExtraInfo: INJECTED_TAG,
                },
            },
        }
    }

    fn keyboard(code: u16, up: bool) -> INPUT {
        // Extended keys (arrows, Insert/Delete, Home/End, Page Up/Down) need
        // the flag, or the numpad's twins are pressed instead.
        let extended = matches!(code, 0x21..=0x28 | 0x2D | 0x2E | 0x5B | 0x5D);
        let mut flags = KEYBD_EVENT_FLAGS(0);
        if up {
            flags |= KEYEVENTF_KEYUP;
        }
        if extended {
            flags |= KEYEVENTF_EXTENDEDKEY;
        }
        INPUT {
            r#type: INPUT_KEYBOARD,
            Anonymous: INPUT_0 {
                ki: KEYBDINPUT {
                    wVk: VIRTUAL_KEY(code),
                    wScan: 0,
                    dwFlags: flags,
                    time: 0,
                    dwExtraInfo: INJECTED_TAG,
                },
            },
        }
    }

    fn unicode(unit: u16, up: bool) -> INPUT {
        let mut flags = KEYEVENTF_UNICODE;
        if up {
            flags |= KEYEVENTF_KEYUP;
        }
        INPUT {
            r#type: INPUT_KEYBOARD,
            Anonymous: INPUT_0 {
                ki: KEYBDINPUT {
                    wVk: VIRTUAL_KEY(0),
                    wScan: unit,
                    dwFlags: flags,
                    time: 0,
                    dwExtraInfo: INJECTED_TAG,
                },
            },
        }
    }
}

/// Whether an executable path is Kyokai's GUI or engine.
pub fn is_kyokai(path: &str) -> bool {
    let name = path
        .rsplit(['\\', '/'])
        .next()
        .unwrap_or(path)
        .to_ascii_lowercase();
    name.starts_with("kyokai")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn events_parse_and_are_checked() {
        let cases = [
            (
                r#"{"kind":"pointer","action":"down","button":0,"x":0.5,"y":0.25}"#,
                true,
            ),
            (
                r#"{"kind":"pointer","action":"move","x":1.5,"y":0.25}"#,
                false,
            ),
            (
                r#"{"kind":"pointer","action":"up","button":7,"x":0.1,"y":0.1}"#,
                false,
            ),
            (r#"{"kind":"scroll","dx":0,"dy":-3}"#, true),
            (r#"{"kind":"key","key":"enter","down":true}"#, true),
            (
                r#"{"kind":"key","key":"c","down":true,"mods":{"ctrl":true}}"#,
                true,
            ),
            (r#"{"kind":"key","key":"f13","down":false}"#, true),
            (
                r#"{"kind":"key","key":"launch-rockets","down":true}"#,
                false,
            ),
            (r#"{"kind":"text","text":"héllo 世界"}"#, true),
            (r#"{"kind":"text","text":""}"#, false),
        ];
        for (json, ok) in cases {
            let e: Event = serde_json::from_str(json).unwrap();
            assert_eq!(e.valid(), ok, "{json}");
        }
        assert!(serde_json::from_str::<Event>(r#"{"kind":"teleport"}"#).is_err());
    }

    #[test]
    fn key_names_map_to_virtual_keys() {
        assert_eq!(vk("a"), Some(0x41));
        assert_eq!(vk("7"), Some(0x37));
        assert_eq!(vk("f1"), Some(0x70));
        assert_eq!(vk("f24"), Some(0x87));
        assert_eq!(vk("f25"), None);
        assert_eq!(vk("page-down"), Some(0x22));
    }

    #[test]
    fn kyokai_processes_are_recognized() {
        assert!(is_kyokai(
            r"C:\Users\x\AppData\Local\Programs\Kyokai\Kyokai.exe"
        ));
        assert!(is_kyokai(r"C:\Kyokai\bin\kyokai-svc.exe"));
        assert!(is_kyokai("/Applications/Kyokai.app/Contents/MacOS/Kyokai"));
        assert!(!is_kyokai(r"C:\Windows\notepad.exe"));
    }
}
