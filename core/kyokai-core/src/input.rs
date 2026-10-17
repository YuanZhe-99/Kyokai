//! The host's input service (doc/en-us/computer-use.md, "Input"): events
//! from a controller allowed to control input, injected into the host's
//! desktop. Nothing reaches Kyokai's own windows; whatever is held is
//! released when the session ends.
//!
//! Injection exists on Windows only so far (macOS comes with the Mac pass).
#![cfg_attr(not(windows), allow(dead_code))]

use serde::{Deserialize, Serialize};
use std::time::{Duration, Instant};

/// One input event from the controller (`{"type": "input", "event": …}`).
/// Pointer positions are fractions of the shared picture (0–1).
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
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
    /// A key by its position (Map and Translate modes): its scan code as
    /// Windows numbers it (`crate::keyboard::scan_code`), with the
    /// controller's CapsLock and NumLock, which the host follows.
    Scan {
        code: u32,
        down: bool,
        #[serde(default)]
        caps: bool,
        #[serde(default)]
        num: bool,
    },
}

/// Whether a scan code names a key: a set-1 code, the same 0xE0-prefixed,
/// or Pause (0xE11D).
pub fn valid_scan(code: u32) -> bool {
    matches!(code, 0x01..=0xFF | 0xE001..=0xE0FF | 0xE11D)
}

/// `KEYBDINPUT` flags, as numbers so the mapping is testable everywhere.
const EXTENDED_KEY: u32 = 0x1; // KEYEVENTF_EXTENDEDKEY
const BY_SCAN_CODE: u32 = 0x8; // KEYEVENTF_SCANCODE

/// How `SendInput` presses a scan code: virtual key, scan code and flags.
/// Notes: by scan code, so the host's layout decides what the key types;
/// Pause has no scan code Windows takes and goes by virtual key.
pub fn scan_input(code: u32) -> (u16, u16, u32) {
    match code {
        0xE11D => (0x13, 0x45, 0), // VK_PAUSE
        c if c >> 8 == 0xE0 => (0, (c & 0xFF) as u16, BY_SCAN_CODE | EXTENDED_KEY),
        c => (0, c as u16, BY_SCAN_CODE),
    }
}

/// The lock key the host first brings in line with the controller before
/// a key goes down, as RustDesk's input service does: CapsLock before a
/// letter, NumLock before a numpad key whose meaning it changes.
/// Returns: the lock's scan code, and whether the controller has it on.
pub fn lock_for(code: u32, caps: bool, num: bool) -> Option<(u32, bool)> {
    match code {
        0x10..=0x19 | 0x1E..=0x26 | 0x2C..=0x32 => Some((0x3A, caps)),
        0x47..=0x49 | 0x4B..=0x4D | 0x4F..=0x53 => Some((0x45, num)),
        _ => None,
    }
}

/// The window an event is checked against before it is injected, so that
/// remote and AI input never reaches a Kyokai window.
/// Notes: Kyokai's windows are left out of capture, so a check by focus
/// alone would refuse clicks on windows the viewer does see while Kyokai's
/// (unseen) window is in front.
#[derive(Debug, Clone, Copy, PartialEq)]
#[cfg_attr(not(windows), allow(dead_code))]
enum Guard {
    /// Releases: they end what was pressed elsewhere and start nothing.
    None,
    /// Pointer presses and moves: the window under this point (fractions
    /// of the shared picture).
    At(f32, f32),
    /// The wheel: the window under the pointer.
    Pointer,
    /// Keys and text: the focused window.
    Focus,
}

impl Event {
    /// Which window this event is checked against.
    #[cfg_attr(not(windows), allow(dead_code))]
    fn guard(&self) -> Guard {
        match self {
            Event::Pointer {
                action: PointerAction::Up,
                ..
            }
            | Event::Key { down: false, .. }
            | Event::Scan { down: false, .. } => Guard::None,
            Event::Pointer { x, y, .. } => Guard::At(*x, *y),
            Event::Scroll { .. } => Guard::Pointer,
            Event::Key { .. } | Event::Text { .. } | Event::Scan { .. } => Guard::Focus,
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum PointerAction {
    Move,
    Down,
    Up,
}

/// Modifier keys held with a key.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Serialize, Deserialize)]
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
            Event::Scan { code, .. } => valid_scan(*code),
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
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
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

#[cfg(all(windows, test))]
pub use windows_impl::take_sent;
#[cfg(windows)]
pub use windows_impl::{
    Injector, LocalWatch, emergency_stop, kyokai_process, physical, pointer, pointer_in,
};

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
    use crate::privacy::{self, Kind};
    use std::collections::BTreeSet;
    use std::sync::OnceLock;
    use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
    use windows::Win32::Foundation::{
        CloseHandle, HINSTANCE, HWND, LPARAM, LRESULT, POINT, WPARAM,
    };
    use windows::Win32::System::LibraryLoader::GetModuleHandleW;
    use windows::Win32::System::Shutdown::LockWorkStation;
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
        CURSOR_SHOWING, CURSORINFO, CallNextHookEx, GetCursorInfo, GetCursorPos,
        GetForegroundWindow, GetMessageW, GetSystemMetrics, GetWindowThreadProcessId,
        KBDLLHOOKSTRUCT, LLKHF_INJECTED, LLMHF_INJECTED, MSG, MSLLHOOKSTRUCT, PostThreadMessageW,
        SM_CXSCREEN, SM_CXVIRTUALSCREEN, SM_CYSCREEN, SM_CYVIRTUALSCREEN, SM_XVIRTUALSCREEN,
        SM_YVIRTUALSCREEN, SetWindowsHookExW, UnhookWindowsHookEx, WH_KEYBOARD_LL, WH_MOUSE_LL,
        WM_KEYDOWN, WM_MOUSEMOVE, WM_QUIT, WM_SYSKEYDOWN, WindowFromPoint,
    };
    use windows::core::PWSTR;

    /// How long the answer for one process is reused.
    const OWNER_TTL: Duration = Duration::from_secs(1);

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
        /// Keys held by scan code (Map and Translate modes).
        scans: BTreeSet<u32>,
        buttons: BTreeSet<u8>,
        /// The last process asked about: its id, whether it is Kyokai's,
        /// and when that was checked.
        owner: (u32, bool, Instant),
        /// The agent's injector that does the work in the helper modes.
        remote: Option<u64>,
        /// The display pointer positions are fractions of; None for the
        /// primary.
        area: Option<Area>,
        /// The block of the host's own input this session holds: its
        /// generation (see `crate::privacy`).
        block: Option<u64>,
        /// The privacy mode this session holds: the mask's generation and
        /// the block's that comes with it.
        privacy: Option<(u64, u64)>,
    }

    impl Default for Injector {
        fn default() -> Self {
            Injector {
                keys: BTreeSet::new(),
                scans: BTreeSet::new(),
                buttons: BTreeSet::new(),
                owner: (0, false, Instant::now() - OWNER_TTL),
                remote: crate::agent::link().map(|l| l.injector()),
                area: None,
                block: None,
                privacy: None,
            }
        }
    }

    impl Injector {
        /// Locks the host's screen (the session goes on; the lock screen
        /// shows where the helper serves it).
        /// Returns: whether Windows locked it.
        pub fn lock_screen(&self) -> bool {
            if let (Some(_), Some(link)) = (self.remote, crate::agent::link()) {
                return link.lock_screen();
            }
            // SAFETY: no arguments; locks the calling session's workstation.
            unsafe { LockWorkStation() }.is_ok()
        }

        /// Sends Ctrl+Alt+Del, which only a service can do: the helper.
        /// Returns: whether it was sent.
        pub fn ctrl_alt_del(&self) -> bool {
            self.remote.is_some() && crate::agent::link().is_some() && crate::agent::send_sas()
        }

        /// Whether Ctrl+Alt+Del can be sent from here (the helper modes).
        pub fn can_ctrl_alt_del(&self) -> bool {
            self.remote.is_some()
        }

        /// Blocks or unblocks the host's own keyboard and mouse for this
        /// session. Kyokai's injected input still passes; the emergency stop
        /// (Ctrl+Alt+Shift+Esc) lifts the block for every session, and so
        /// does Ctrl+Alt+Del, which Windows never lets a hook see.
        /// Returns: whether the host's input is blocked for this session now.
        pub fn block_input(&mut self, on: bool) -> bool {
            if let (Some(id), Some(link)) = (self.remote, crate::agent::link()) {
                return link.block_input(id, on);
            }
            match (on, self.block) {
                (true, None) => self.block = privacy::hold(Kind::Block),
                (false, Some(generation)) => {
                    privacy::release(Kind::Block, generation);
                    self.block = None;
                }
                _ => {}
            }
            self.input_blocked()
        }

        /// Turns privacy mode on or off for this session: the host's screen
        /// goes black for the people there (not in the picture) and their
        /// keyboard and mouse are blocked; lifted like the block.
        /// Returns: whether privacy mode is on for this session now.
        pub fn privacy(&mut self, on: bool) -> bool {
            if let (Some(id), Some(link)) = (self.remote, crate::agent::link()) {
                return link.privacy(id, on);
            }
            match (on, self.privacy) {
                (true, None) => {
                    if let Some(block) = privacy::hold(Kind::Block) {
                        match privacy::hold(Kind::Mask) {
                            Some(mask) => self.privacy = Some((mask, block)),
                            None => privacy::release(Kind::Block, block),
                        }
                    }
                }
                (false, Some((mask, block))) => {
                    privacy::release(Kind::Mask, mask);
                    privacy::release(Kind::Block, block);
                    self.privacy = None;
                }
                _ => {}
            }
            self.privacy_on()
        }

        /// Whether privacy mode is still on for this session.
        pub fn privacy_on(&self) -> bool {
            if let (Some(id), Some(link)) = (self.remote, crate::agent::link()) {
                return link.privacy_on(id);
            }
            self.privacy
                .is_some_and(|(mask, _)| privacy::live(Kind::Mask, mask))
        }

        /// Whether the host's input is still blocked for this session (the
        /// host user may have lifted it).
        pub fn input_blocked(&self) -> bool {
            if let (Some(id), Some(link)) = (self.remote, crate::agent::link()) {
                return link.input_blocked(id);
            }
            self.block.is_some_and(|g| privacy::live(Kind::Block, g))
                || self
                    .privacy
                    .is_some_and(|(_, g)| privacy::live(Kind::Block, g))
        }

        /// Sets the display pointer positions refer to (None: the primary).
        pub fn set_area(&mut self, area: Option<Area>) {
            self.area = area;
        }

        /// Injects an event, unless it would reach a Kyokai window (see
        /// `Guard`).
        /// Returns: whether it was injected.
        pub fn inject(&mut self, e: &Event) -> bool {
            if let (Some(id), Some(link)) = (self.remote, crate::agent::link()) {
                return e.valid() && link.inject(id, e, self.area);
            }
            if !e.valid() || self.blocked(e.guard()) {
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
                Event::Scan {
                    code,
                    down,
                    caps,
                    num,
                } => {
                    let mut batch = Vec::new();
                    if *down {
                        // The host's CapsLock or NumLock follows the
                        // controller's before a key it changes.
                        if let Some((lock, on)) = lock_for(*code, *caps, *num)
                            && host_lock(lock) != on
                        {
                            batch.push(by_scan(lock, false));
                            batch.push(by_scan(lock, true));
                        }
                        self.scans.insert(*code);
                    } else {
                        self.scans.remove(code);
                    }
                    batch.push(by_scan(*code, !*down));
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
            if let (Some(id), Some(link)) = (self.remote, crate::agent::link()) {
                link.release(id);
                return;
            }
            let mut batch: Vec<INPUT> = self.keys.iter().map(|k| keyboard(*k, true)).collect();
            batch.extend(self.scans.iter().map(|c| by_scan(*c, true)));
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
            self.scans.clear();
            self.buttons.clear();
        }

        /// Whether the window `guard` names is one of Kyokai's (or cannot
        /// be found).
        fn blocked(&mut self, guard: Guard) -> bool {
            let hwnd = match guard {
                Guard::None => return false,
                Guard::At(x, y) => {
                    let area = self.area.unwrap_or_else(|| physical(desktop_areas).1);
                    let at = POINT {
                        x: area.x + (x.clamp(0.0, 1.0) * (area.width.max(1) - 1) as f32) as i32,
                        y: area.y + (y.clamp(0.0, 1.0) * (area.height.max(1) - 1) as f32) as i32,
                    };
                    // SAFETY: a plain query of the window at a point.
                    physical(|| unsafe { WindowFromPoint(at) })
                }
                Guard::Pointer => {
                    let mut at = POINT::default();
                    // SAFETY: plain queries of the pointer and the window
                    // under it.
                    match physical(|| unsafe { GetCursorPos(&mut at).map(|_| WindowFromPoint(at)) })
                    {
                        Ok(h) => h,
                        Err(_) => return true,
                    }
                }
                // SAFETY: a plain query of the focused window.
                Guard::Focus => unsafe { GetForegroundWindow() },
            };
            self.kyokai_window(hwnd)
        }

        /// Whether a window belongs to one of Kyokai's processes (the
        /// answer for the last process is reused for OWNER_TTL, so moves
        /// do not query the process each time).
        fn kyokai_window(&mut self, hwnd: HWND) -> bool {
            let mut pid = 0u32;
            // SAFETY: a plain query of the window's process.
            unsafe { GetWindowThreadProcessId(hwnd, Some(&mut pid)) };
            let (last, kyokai, at) = self.owner;
            if pid == last && at.elapsed() < OWNER_TTL {
                return kyokai;
            }
            let kyokai = kyokai_process(pid);
            self.owner = (pid, kyokai, Instant::now());
            kyokai
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
        if let Some(link) = crate::agent::link() {
            return link.pointer(None);
        }
        let (_, primary) = physical(desktop_areas);
        pointer_in(primary)
    }

    /// Where the pointer is, as fractions of `area` (outside 0–1 elsewhere),
    /// and whether it is shown.
    pub fn pointer_in(area: Area) -> Option<(f32, f32, bool)> {
        if let Some(link) = crate::agent::link() {
            return link.pointer(Some(area));
        }
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
            match (self.remote, crate::agent::link()) {
                (Some(id), Some(link)) => link.drop_injector(id),
                _ => {
                    self.release_all();
                    if let Some(generation) = self.block.take() {
                        privacy::release(Kind::Block, generation);
                    }
                    if let Some((mask, block)) = self.privacy.take() {
                        privacy::release(Kind::Mask, mask);
                        privacy::release(Kind::Block, block);
                    }
                }
            }
        }
    }

    /// The watch's clock: local input is stored as nanoseconds since it,
    /// plus one (0 means none).
    static EPOCH: OnceLock<Instant> = OnceLock::new();
    static PRESSED: AtomicU64 = AtomicU64::new(0);
    static MOVED: AtomicU64 = AtomicU64::new(0);
    /// Set by Ctrl+Alt+Shift+Esc, the emergency stop.
    static STOP: AtomicBool = AtomicBool::new(false);

    /// Records the emergency stop (seen by the block's hook, which
    /// swallows it before the watch's hook could).
    pub fn emergency_stop() {
        STOP.store(true, Ordering::Relaxed);
    }

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

    #[cfg(not(test))]
    fn send(batch: &[INPUT]) {
        if !batch.is_empty() {
            // SAFETY: a slice of fully initialized INPUT records.
            unsafe { SendInput(batch, std::mem::size_of::<INPUT>() as i32) };
        }
    }

    /// The keyboard input tests would have injected: virtual key, scan
    /// code, flags and tag. Tests never inject into the desktop they run on.
    #[cfg(test)]
    static SENT: std::sync::Mutex<Vec<(u16, u16, u32, usize)>> = std::sync::Mutex::new(Vec::new());

    #[cfg(test)]
    fn send(batch: &[INPUT]) {
        let mut sent = SENT.lock().unwrap();
        for i in batch.iter().filter(|i| i.r#type == INPUT_KEYBOARD) {
            // SAFETY: keyboard records carry `ki`.
            let ki = unsafe { i.Anonymous.ki };
            sent.push((ki.wVk.0, ki.wScan, ki.dwFlags.0, ki.dwExtraInfo));
        }
    }

    /// Takes the recorded keyboard input that `ours` picks (tests run in
    /// parallel; each takes only its own keys).
    #[cfg(test)]
    pub fn take_sent(
        ours: impl Fn(&(u16, u16, u32, usize)) -> bool,
    ) -> Vec<(u16, u16, u32, usize)> {
        let mut sent = SENT.lock().unwrap();
        let (mine, rest) = sent.drain(..).partition(|s| ours(s));
        *sent = rest;
        mine
    }

    /// Whether the host's CapsLock (0x3A) or NumLock (0x45) is on.
    pub(super) fn host_lock(lock: u32) -> bool {
        let vk = if lock == 0x3A { VK_CAPITAL } else { VK_NUMLOCK };
        // SAFETY: a plain query of the keyboard state.
        unsafe { GetKeyState(vk.0 as i32) & 1 != 0 }
    }

    /// A press or release of a key by scan code (`scan_input`).
    fn by_scan(code: u32, up: bool) -> INPUT {
        let (vk, scan, flags) = scan_input(code);
        let mut flags = KEYBD_EVENT_FLAGS(flags);
        if up {
            flags |= KEYEVENTF_KEYUP;
        }
        INPUT {
            r#type: INPUT_KEYBOARD,
            Anonymous: INPUT_0 {
                ki: KEYBDINPUT {
                    wVk: VIRTUAL_KEY(vk),
                    wScan: scan,
                    dwFlags: flags,
                    time: 0,
                    dwExtraInfo: INJECTED_TAG,
                },
            },
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
            (r#"{"kind":"scan","code":30,"down":true}"#, true),
            (
                r#"{"kind":"scan","code":57435,"down":false,"caps":true,"num":true}"#,
                true,
            ),
            (r#"{"kind":"scan","code":57629,"down":true}"#, true),
            // As the GUI sends it, with the fields other kinds use.
            (
                r#"{"kind":"scan","button":0,"x":0,"y":0,"dx":0,"dy":0,"code":57373,"down":true,"caps":true}"#,
                true,
            ),
            (r#"{"kind":"scan","code":0,"down":true}"#, false),
            (r#"{"kind":"scan","code":512,"down":true}"#, false),
            (r#"{"kind":"scan","code":57344,"down":true}"#, false),
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
    fn each_event_is_checked_against_the_window_it_would_reach() {
        let pointer = |action| Event::Pointer {
            action,
            button: 0,
            x: 0.25,
            y: 0.75,
        };
        let key = |down| Event::Key {
            key: "a".into(),
            down,
            mods: Mods::default(),
        };
        let cases = [
            // A click lands on the window under it, whatever has the focus.
            (pointer(PointerAction::Down), Guard::At(0.25, 0.75)),
            (pointer(PointerAction::Move), Guard::At(0.25, 0.75)),
            (pointer(PointerAction::Up), Guard::None),
            (Event::Scroll { dx: 0.0, dy: 1.0 }, Guard::Pointer),
            (key(true), Guard::Focus),
            (key(false), Guard::None),
            (Event::Text { text: "x".into() }, Guard::Focus),
            (scan(0x1E, true), Guard::Focus),
            (scan(0x1E, false), Guard::None),
        ];
        for (event, want) in cases {
            assert_eq!(event.guard(), want, "{event:?}");
        }
    }

    fn scan(code: u32, down: bool) -> Event {
        Event::Scan {
            code,
            down,
            caps: false,
            num: false,
        }
    }

    #[test]
    fn scan_codes_are_pressed_by_position() {
        // code → (virtual key, scan code, flags) for SendInput.
        let cases = [
            (0x1E, (0, 0x1E, BY_SCAN_CODE)),                  // A
            (0x1C, (0, 0x1C, BY_SCAN_CODE)),                  // Enter
            (0xE01C, (0, 0x1C, BY_SCAN_CODE | EXTENDED_KEY)), // numpad Enter
            (0x47, (0, 0x47, BY_SCAN_CODE)),                  // numpad 7
            (0xE047, (0, 0x47, BY_SCAN_CODE | EXTENDED_KEY)), // Home
            (0xE01D, (0, 0x1D, BY_SCAN_CODE | EXTENDED_KEY)), // right Ctrl
            (0xE05B, (0, 0x5B, BY_SCAN_CODE | EXTENDED_KEY)), // left Win
            (0x45, (0, 0x45, BY_SCAN_CODE)),                  // NumLock
            (0xE11D, (0x13, 0x45, 0)),                        // Pause
            (0xF2, (0, 0xF2, BY_SCAN_CODE)),                  // Hangul
        ];
        for (code, want) in cases {
            assert_eq!(scan_input(code), want, "{code:#x}");
        }
    }

    #[test]
    fn locks_follow_the_controller_before_the_keys_they_change() {
        let cases = [
            (0x1E, Some((0x3A, true))),  // A: CapsLock
            (0x32, Some((0x3A, true))),  // M
            (0x02, None),                // 1: no lock
            (0x47, Some((0x45, false))), // numpad 7: NumLock
            (0x52, Some((0x45, false))), // numpad 0
            (0x4A, None),                // numpad minus: the same either way
            (0xE047, None),              // Home
            (0x3A, None),                // CapsLock itself
        ];
        for (code, want) in cases {
            assert_eq!(lock_for(code, true, false), want, "{code:#x}");
        }
    }

    /// A Map-mode key reaches SendInput by scan code, tagged as Kyokai's,
    /// with CapsLock brought in line first; ending the session releases it.
    /// (The test sink records instead of injecting.)
    #[cfg(windows)]
    #[test]
    fn map_keys_are_injected_by_scan_code() {
        use windows_impl::{host_lock, take_sent};
        let ours = |s: &(u16, u16, u32, usize)| matches!(s.1, 0x3A | 0x1F | 0x5B);
        let caps = host_lock(0x3A);
        let mut inj = Injector::default();
        let down = Event::Scan {
            code: 0x1F,
            down: true,
            caps: !caps,
            num: false,
        };
        if !inj.inject(&down) {
            return; // a Kyokai window is in front
        }
        let tag = INJECTED_TAG;
        assert_eq!(
            take_sent(ours),
            [
                (0, 0x3A, BY_SCAN_CODE, tag),
                (0, 0x3A, BY_SCAN_CODE | 2, tag),
                (0, 0x1F, BY_SCAN_CODE, tag),
            ]
        );
        let win = Event::Scan {
            code: 0xE05B,
            down: true,
            caps,
            num: false,
        };
        assert!(inj.inject(&win));
        assert_eq!(
            take_sent(ours),
            [(0, 0x5B, BY_SCAN_CODE | EXTENDED_KEY, tag)]
        );
        inj.release_all();
        let mut released = take_sent(ours);
        released.sort();
        assert_eq!(
            released,
            [
                (0, 0x1F, BY_SCAN_CODE | 2, tag),
                (0, 0x5B, BY_SCAN_CODE | EXTENDED_KEY | 2, tag),
            ]
        );
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
