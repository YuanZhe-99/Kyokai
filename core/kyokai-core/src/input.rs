//! The host's input service (doc/en-us/computer-use.md, "Input"): events
//! from a controller allowed to control input, injected into the host's
//! desktop. Nothing reaches Kyokai's own windows; whatever is held is
//! released when the session ends.
//!
//! Injection exists on Windows only so far (macOS comes with the Mac pass).
#![cfg_attr(not(windows), allow(dead_code))]

use serde::Deserialize;
use std::time::{Duration, Instant};

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
        "caps-lock" => 0x14,
        "print-screen" => 0x2C,
        "shift" => 0x10,
        "ctrl" => 0x11,
        "alt" => 0x12,
        "meta" => 0x5B,
        _ => return None,
    })
}

/// Marks Kyokai's injected events (`dwExtraInfo`), so local input can be
/// told apart from remote input.
pub const INJECTED_TAG: usize = 0x4B59_4F4B; // "KYOK"

/// A rectangle of the virtual desktop, in pixels.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Area {
    pub x: i32,
    pub y: i32,
    pub width: u32,
    pub height: u32,
}

/// Maps a point given as fractions of `area` to `SendInput`'s absolute
/// coordinates over the virtual desktop `desk` (0–65535 across it).
pub fn absolute(fx: f32, fy: f32, area: Area, desk: Area) -> (i32, i32) {
    let axis = |f: f32, start: i32, len: u32, desk_start: i32, desk_len: u32| {
        let px = start as f32 + f.clamp(0.0, 1.0) * (len.max(1) - 1) as f32;
        ((px - desk_start as f32) * 65535.0 / (desk_len.max(2) - 1) as f32).round() as i32
    };
    (
        axis(fx, area.x, area.width, desk.x, desk.width),
        axis(fy, area.y, area.height, desk.y, desk.height),
    )
}

/// How long pointer moves are forgiven after the host user handed an AI
/// task the desktop (a hand leaving the mouse after clicking Resume or
/// Allow in Kyokai's window); presses never are.
pub const MOVE_GRACE: Duration = Duration::from_millis(1500);

/// When the host's user last used the keyboard or mouse, as the local-input
/// watch saw it.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct LocalInput {
    /// A key, a mouse button or the wheel.
    pub press: Option<Instant>,
    /// The pointer moving.
    pub moved: Option<Instant>,
}

impl LocalInput {
    /// Whether the host user used the computer after `since`, the moment an
    /// AI task was given the desktop.
    pub fn after(&self, since: Instant) -> bool {
        self.press.is_some_and(|t| t > since) || self.moved.is_some_and(|t| t > since + MOVE_GRACE)
    }
}

#[cfg(windows)]
pub use windows_impl::{Injector, LocalWatch, kyokai_process, physical, pointer, pointer_in};

/// Where the pointer is, as fractions of the primary display (outside 0–1
/// on another display), and whether it is shown. Not available yet off
/// Windows.
#[cfg(not(windows))]
pub fn pointer() -> Option<(f32, f32, bool)> {
    None
}

/// Where the pointer is, as fractions of `area`, and whether it is shown.
/// Not available yet off Windows.
#[cfg(not(windows))]
pub fn pointer_in(_area: Area) -> Option<(f32, f32, bool)> {
    None
}

#[cfg(windows)]
mod windows_impl {
    use super::*;
    use std::collections::BTreeSet;
    use std::sync::OnceLock;
    use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
    use windows::Win32::Foundation::{CloseHandle, HINSTANCE, HWND, LPARAM, LRESULT, WPARAM};
    use windows::Win32::System::LibraryLoader::GetModuleHandleW;
    use windows::Win32::System::Threading::{
        GetCurrentThreadId, OpenProcess, PROCESS_NAME_WIN32, PROCESS_QUERY_LIMITED_INFORMATION,
        QueryFullProcessImageNameW,
    };
    use windows::Win32::UI::HiDpi::{
        DPI_AWARENESS_CONTEXT, DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2,
        SetThreadDpiAwarenessContext,
    };
    use windows::Win32::UI::Input::KeyboardAndMouse::*;
    use windows::Win32::UI::WindowsAndMessaging::{
        CURSOR_SHOWING, CURSORINFO, CallNextHookEx, GetCursorInfo, GetForegroundWindow,
        GetMessageW, GetSystemMetrics, GetWindowThreadProcessId, KBDLLHOOKSTRUCT, LLKHF_INJECTED,
        LLMHF_INJECTED, MSG, MSLLHOOKSTRUCT, PostThreadMessageW, SM_CXSCREEN, SM_CXVIRTUALSCREEN,
        SM_CYSCREEN, SM_CYVIRTUALSCREEN, SM_XVIRTUALSCREEN, SM_YVIRTUALSCREEN, SetWindowsHookExW,
        UnhookWindowsHookEx, WH_KEYBOARD_LL, WH_MOUSE_LL, WM_KEYDOWN, WM_MOUSEMOVE, WM_QUIT,
        WM_SYSKEYDOWN,
    };
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
        /// The display pointer positions are fractions of; None for the
        /// primary.
        area: Option<Area>,
    }

    impl Default for Injector {
        fn default() -> Self {
            Injector {
                keys: BTreeSet::new(),
                buttons: BTreeSet::new(),
                guard: (Instant::now() - Duration::from_secs(1), false),
                area: None,
            }
        }
    }

    impl Injector {
        /// Sets the display pointer positions refer to (None: the primary).
        pub fn set_area(&mut self, area: Option<Area>) {
            self.area = area;
        }

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
                    let (desk, primary) = physical(desktop_areas);
                    let (ax, ay) = absolute(*x, *y, self.area.unwrap_or(primary), desk);
                    let mut flags =
                        MOUSEEVENTF_MOVE | MOUSEEVENTF_ABSOLUTE | MOUSEEVENTF_VIRTUALDESK;
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
            // SAFETY: plain queries of the focused window's process.
            let front = unsafe {
                let hwnd: HWND = GetForegroundWindow();
                let mut pid = 0u32;
                GetWindowThreadProcessId(hwnd, Some(&mut pid));
                kyokai_process(pid)
            };
            self.guard = (Instant::now(), front);
            front
        }
    }

    /// Runs `f` with this thread aware of the real pixel size of every
    /// monitor, then restores the thread's previous awareness.
    pub fn physical<T>(f: impl FnOnce() -> T) -> T {
        // SAFETY: changes only this thread's DPI awareness; restored below.
        let before: DPI_AWARENESS_CONTEXT =
            unsafe { SetThreadDpiAwarenessContext(DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2) };
        let out = f();
        // SAFETY: restores the value returned above.
        unsafe { SetThreadDpiAwarenessContext(before) };
        out
    }

    /// The virtual desktop and the primary display, in pixels (call it
    /// inside `physical`).
    fn desktop_areas() -> (Area, Area) {
        // SAFETY: plain metric queries.
        unsafe {
            (
                Area {
                    x: GetSystemMetrics(SM_XVIRTUALSCREEN),
                    y: GetSystemMetrics(SM_YVIRTUALSCREEN),
                    width: GetSystemMetrics(SM_CXVIRTUALSCREEN).max(1) as u32,
                    height: GetSystemMetrics(SM_CYVIRTUALSCREEN).max(1) as u32,
                },
                Area {
                    x: 0,
                    y: 0,
                    width: GetSystemMetrics(SM_CXSCREEN).max(1) as u32,
                    height: GetSystemMetrics(SM_CYSCREEN).max(1) as u32,
                },
            )
        }
    }

    /// Where the pointer is, as fractions of the primary display (outside
    /// 0–1 on another display), and whether it is shown.
    pub fn pointer() -> Option<(f32, f32, bool)> {
        let (_, primary) = physical(desktop_areas);
        pointer_in(primary)
    }

    /// Where the pointer is, as fractions of `area` (outside 0–1 elsewhere),
    /// and whether it is shown.
    pub fn pointer_in(area: Area) -> Option<(f32, f32, bool)> {
        physical(|| {
            let mut info = CURSORINFO {
                cbSize: std::mem::size_of::<CURSORINFO>() as u32,
                ..Default::default()
            };
            // SAFETY: fills `info`, whose size is set.
            unsafe { GetCursorInfo(&mut info) }.ok()?;
            Some((
                (info.ptScreenPos.x - area.x) as f32 / area.width.max(1) as f32,
                (info.ptScreenPos.y - area.y) as f32 / area.height.max(1) as f32,
                info.flags.0 & CURSOR_SHOWING.0 != 0,
            ))
        })
    }

    /// Whether a process is one of Kyokai's (by its executable's name).
    pub fn kyokai_process(pid: u32) -> bool {
        // SAFETY: plain queries; the process handle is closed below.
        unsafe {
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
        }
    }

    impl Drop for Injector {
        fn drop(&mut self) {
            self.release_all();
        }
    }

    /// The watch's clock: local input is stored as nanoseconds since it,
    /// plus one (0 means none).
    static EPOCH: OnceLock<Instant> = OnceLock::new();
    static PRESSED: AtomicU64 = AtomicU64::new(0);
    static MOVED: AtomicU64 = AtomicU64::new(0);
    /// Set by Ctrl+Alt+Shift+Esc, the emergency stop.
    static STOP: AtomicBool = AtomicBool::new(false);

    fn stamp(at: &AtomicU64) {
        let epoch = *EPOCH.get_or_init(Instant::now);
        at.store(epoch.elapsed().as_nanos() as u64 + 1, Ordering::Relaxed);
    }

    fn read(at: &AtomicU64) -> Option<Instant> {
        let epoch = *EPOCH.get_or_init(Instant::now);
        match at.load(Ordering::Relaxed) {
            0 => None,
            n => Some(epoch + Duration::from_nanos(n - 1)),
        }
    }

    /// Watches for local input with low-level keyboard and mouse hooks on a
    /// thread of its own, until dropped. One runs at a time (an AI task's).
    pub struct LocalWatch {
        thread: u32,
    }

    impl LocalWatch {
        /// Installs the hooks.
        /// Returns: the watch, or why the hooks could not be installed.
        pub fn start() -> Result<LocalWatch, String> {
            PRESSED.store(0, Ordering::Relaxed);
            MOVED.store(0, Ordering::Relaxed);
            STOP.store(false, Ordering::Relaxed);
            let (tx, rx) = std::sync::mpsc::channel();
            std::thread::spawn(move || {
                // SAFETY: the hooks are installed and removed on this thread,
                // which pumps messages for them until WM_QUIT.
                unsafe {
                    let module = GetModuleHandleW(None).ok().map(|m| HINSTANCE(m.0));
                    let mouse = SetWindowsHookExW(WH_MOUSE_LL, Some(mouse_hook), module, 0);
                    let keys = SetWindowsHookExW(WH_KEYBOARD_LL, Some(key_hook), module, 0);
                    let (Ok(mouse), Ok(keys)) = (mouse, keys) else {
                        let _ = tx.send(Err("input hooks could not be installed".to_string()));
                        return;
                    };
                    let _ = tx.send(Ok(GetCurrentThreadId()));
                    let mut msg = MSG::default();
                    while GetMessageW(&mut msg, None, 0, 0).as_bool() {}
                    let _ = UnhookWindowsHookEx(mouse);
                    let _ = UnhookWindowsHookEx(keys);
                }
            });
            let thread = rx
                .recv()
                .map_err(|_| "input hooks could not be installed".to_string())??;
            Ok(LocalWatch { thread })
        }

        /// When the host's user last used the keyboard or mouse.
        pub fn input(&self) -> LocalInput {
            LocalInput {
                press: read(&PRESSED),
                moved: read(&MOVED),
            }
        }

        /// Whether the emergency stop was pressed since the last call
        /// (clears it).
        pub fn stop_requested(&self) -> bool {
            STOP.swap(false, Ordering::Relaxed)
        }
    }

    impl Drop for LocalWatch {
        fn drop(&mut self) {
            // SAFETY: posts WM_QUIT to the hook thread started above.
            let _ = unsafe { PostThreadMessageW(self.thread, WM_QUIT, WPARAM(0), LPARAM(0)) };
        }
    }

    /// Notes local mouse input: events not injected by Kyokai.
    unsafe extern "system" fn mouse_hook(code: i32, w: WPARAM, l: LPARAM) -> LRESULT {
        if code >= 0 {
            // SAFETY: for HC_ACTION, `l` points to the event's MSLLHOOKSTRUCT.
            let info = unsafe { &*(l.0 as *const MSLLHOOKSTRUCT) };
            if info.flags & LLMHF_INJECTED == 0 || info.dwExtraInfo != INJECTED_TAG {
                stamp(if w.0 as u32 == WM_MOUSEMOVE {
                    &MOVED
                } else {
                    &PRESSED
                });
            }
        }
        // SAFETY: passes the event on unchanged.
        unsafe { CallNextHookEx(None, code, w, l) }
    }

    /// Notes local keyboard input (events not injected by Kyokai) and the
    /// emergency stop.
    unsafe extern "system" fn key_hook(code: i32, w: WPARAM, l: LPARAM) -> LRESULT {
        if code >= 0 {
            // SAFETY: for HC_ACTION, `l` points to the event's KBDLLHOOKSTRUCT.
            let info = unsafe { &*(l.0 as *const KBDLLHOOKSTRUCT) };
            if info.flags.0 & LLKHF_INJECTED.0 == 0 || info.dwExtraInfo != INJECTED_TAG {
                stamp(&PRESSED);
                let down = matches!(w.0 as u32, WM_KEYDOWN | WM_SYSKEYDOWN);
                // SAFETY: plain queries of the keyboard state.
                let held = |vk: VIRTUAL_KEY| unsafe { GetAsyncKeyState(vk.0 as i32) } < 0;
                if down
                    && info.vkCode == VK_ESCAPE.0 as u32
                    && held(VK_CONTROL)
                    && held(VK_MENU)
                    && held(VK_SHIFT)
                {
                    STOP.store(true, Ordering::Relaxed);
                }
            }
        }
        // SAFETY: passes the event on unchanged.
        unsafe { CallNextHookEx(None, code, w, l) }
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
    fn pointer_positions_land_on_the_shown_display() {
        let primary = Area {
            x: 0,
            y: 0,
            width: 1920,
            height: 1080,
        };
        let right = Area {
            x: 1920,
            y: 0,
            width: 2560,
            height: 1440,
        };
        let left = Area {
            x: -1280,
            y: 0,
            width: 1280,
            height: 1024,
        };
        // One display: the same as the plain 0..65535 mapping.
        assert_eq!(absolute(0.0, 0.0, primary, primary), (0, 0));
        assert_eq!(absolute(1.0, 1.0, primary, primary), (65535, 65535));
        assert_eq!(absolute(0.5, 0.5, primary, primary), (32768, 32768));
        // Two side by side: the right one starts after the primary.
        let desk = Area {
            x: 0,
            y: 0,
            width: 4480,
            height: 1440,
        };
        let (x, y) = absolute(0.0, 0.0, right, desk);
        assert_eq!((x, y), ((1920.0 * 65535.0 / 4479.0_f32).round() as i32, 0));
        assert_eq!(absolute(1.0, 1.0, right, desk), (65535, 65535));
        // A display left of the primary has negative coordinates.
        let desk = Area {
            x: -1280,
            y: 0,
            width: 3200,
            height: 1080,
        };
        assert_eq!(absolute(0.0, 0.0, left, desk), (0, 0));
        let (x, _) = absolute(0.0, 0.0, primary, desk);
        assert_eq!(x, (1280.0 * 65535.0 / 3199.0_f32).round() as i32);
        // Out-of-range fractions stay on the display.
        assert_eq!(absolute(-1.0, 2.0, primary, primary), (0, 65535));
    }

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
