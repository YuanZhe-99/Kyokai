//! The controller's keyboard (doc/en-us/computer-use.md, "Keyboard
//! modes"): while a viewer window is in front, a low-level keyboard hook
//! takes the physical keys, by scan code, for that viewer's session —
//! RustDesk's Map and Translate modes. Legacy mode grabs nothing and leaves
//! keys to the GUI toolkit.
//!
//! The hook exists on Windows only so far (macOS comes with the Mac pass).
#![cfg_attr(not(windows), allow(dead_code))]

use std::collections::BTreeSet;

/// A keyboard mode that grabs keys (Legacy grabs none).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Mode {
    /// Every key by its position: the host's layout decides what it types.
    Map,
    /// Keys that type go through the controller's layout (and its input
    /// method) as text; every other key and shortcut goes by position.
    Translate,
}

impl Mode {
    /// The mode a name crosses the FFI as: `map`, `translate`, or `off`
    /// (no grab).
    /// Returns: None for an unknown name.
    pub fn from_name(name: &str) -> Option<Option<Mode>> {
        match name {
            "map" => Some(Some(Mode::Map)),
            "translate" => Some(Some(Mode::Translate)),
            "off" => Some(None),
            _ => None,
        }
    }
}

/// One key the hook took for a viewer, as `kyokai_keyboard_read` passes it
/// to the GUI.
/// Notes: `code` is the key's scan code as Windows numbers it (see
/// `scan_code`); `caps` and `num` are the controller's CapsLock and NumLock
/// after this event, which the host follows.
#[repr(C)]
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct KeyEvent {
    /// The viewer window the key was taken for (its native handle).
    pub window: usize,
    pub code: u32,
    pub down: u8,
    pub caps: u8,
    pub num: u8,
    pub _pad: u8,
}

/// CapsLock, NumLock and ScrollLock: they pass to the controller as well,
/// so its lock state and the host's change together.
pub const LOCKS: [u32; 3] = [0x3A, 0x45, 0x46];

/// Shift, Ctrl and Alt, left and right: in Translate mode they pass to the
/// controller as well, so its layout sees them when it makes characters.
const MODIFIERS: [u32; 6] = [0x2A, 0x36, 0x1D, 0xE01D, 0x38, 0xE038];

/// The scan code of the right Alt key, AltGr on layouts that have one.
const RIGHT_ALT: u32 = 0xE038;

/// The key's scan code as Windows numbers it (`MapVirtualKey`'s
/// `MAPVK_VK_TO_VSC_EX`): the PC set-1 code, 0xE0 above it for extended
/// keys, 0xE11D for Pause.
/// Inputs: the hook's virtual key, scan code and extended flag;
/// `vk_to_scan` looks a code up for keys the keyboard sent without one.
/// Returns: None for the keys Windows makes up (scan codes above 0xFF: the
/// Ctrl that comes with AltGr, the Shift around numpad keys) — the host's
/// layout makes its own.
/// Notes: the hook reports NumLock as extended 0x45 and Pause as plain
/// 0x45; Windows' own numbering is the other way round from the hook's.
pub fn scan_code(
    vk: u32,
    scan: u32,
    extended: bool,
    vk_to_scan: impl Fn(u32) -> u32,
) -> Option<u32> {
    if scan > 0xFF {
        return None;
    }
    match vk {
        0x13 => Some(0xE11D), // VK_PAUSE
        0x90 => Some(0x45),   // VK_NUMLOCK
        _ if scan == 0 => match vk_to_scan(vk) {
            0 => None,
            code => Some(code),
        },
        _ if extended => Some(0xE000 | scan),
        _ => Some(scan),
    }
}

/// What the hook does with one key event: what it sends to the session
/// (the code and whether it is a press), and whether the controller's own
/// windows still get it.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Verdict {
    pub send: Option<(u32, bool)>,
    pub swallow: bool,
}

const PASS: Verdict = Verdict {
    send: None,
    swallow: false,
};

/// One viewer window's grab: its mode, the keys it sent down (released
/// when the grab ends or the window leaves the front), and AltGr.
#[derive(Debug)]
pub struct Grab {
    pub mode: Mode,
    pub sent: BTreeSet<u32>,
    /// Windows made up AltGr's Ctrl: the right Alt that follows is AltGr.
    altgr_next: bool,
    /// AltGr is held (Translate: it stays the controller's).
    altgr: bool,
}

impl Grab {
    pub fn new(mode: Mode) -> Grab {
        Grab {
            mode,
            sent: BTreeSet::new(),
            altgr_next: false,
            altgr: false,
        }
    }

    /// Decides one key event of the window in front.
    /// Inputs: `code` from `scan_code` (None for a made-up key); `types`
    /// tells whether the key makes a character on the controller's layout
    /// with the modifiers held (asked only in Translate mode).
    /// Notes: a release is sent exactly when its press was; a repeat of a
    /// sent key stays sent.
    pub fn route(
        &mut self,
        code: Option<u32>,
        down: bool,
        types: impl FnOnce() -> bool,
    ) -> Verdict {
        let Some(code) = code else {
            if down {
                self.altgr_next = true;
            }
            return Verdict {
                send: None,
                swallow: self.mode == Mode::Map,
            };
        };
        let altgr_next = std::mem::take(&mut self.altgr_next);
        if !down {
            if code == RIGHT_ALT {
                self.altgr = false;
            }
            return match self.sent.remove(&code) {
                true => Verdict {
                    send: Some((code, false)),
                    swallow: !self.passes(code),
                },
                false => PASS,
            };
        }
        if self.mode == Mode::Translate {
            if code == RIGHT_ALT && (altgr_next || self.altgr) {
                self.altgr = true;
                return PASS;
            }
            if !self.passes(code) && !self.sent.contains(&code) && types() {
                return PASS;
            }
        }
        self.sent.insert(code);
        Verdict {
            send: Some((code, true)),
            swallow: !self.passes(code),
        }
    }

    /// Whether a sent key also reaches the controller's windows.
    fn passes(&self, code: u32) -> bool {
        LOCKS.contains(&code) || (self.mode == Mode::Translate && MODIFIERS.contains(&code))
    }
}

#[cfg(windows)]
pub use windows_impl::{grab, next};

/// Grabs the keyboard for a viewer window. Not available yet off Windows.
/// Returns: false.
#[cfg(not(windows))]
pub fn grab(_window: usize, _mode: Option<Mode>) -> bool {
    false
}

/// The next key taken for a viewer. Not available yet off Windows.
/// Returns: None.
#[cfg(not(windows))]
pub fn next(_timeout: std::time::Duration) -> Option<KeyEvent> {
    None
}

#[cfg(windows)]
mod windows_impl {
    use super::*;
    use std::collections::{HashMap, VecDeque};
    use std::sync::{Condvar, Mutex};
    use std::time::Duration;
    use windows::Win32::Foundation::{HINSTANCE, HWND, LPARAM, LRESULT, WPARAM};
    use windows::Win32::System::LibraryLoader::GetModuleHandleW;
    use windows::Win32::System::Threading::GetCurrentThreadId;
    use windows::Win32::UI::Input::KeyboardAndMouse::*;
    use windows::Win32::UI::WindowsAndMessaging::{
        CallNextHookEx, GetForegroundWindow, GetMessageW, GetWindowThreadProcessId,
        KBDLLHOOKSTRUCT, LLKHF_EXTENDED, LLKHF_INJECTED, MSG, PostThreadMessageW,
        SetWindowsHookExW, UnhookWindowsHookEx, WH_KEYBOARD_LL, WM_KEYDOWN, WM_QUIT, WM_SYSKEYDOWN,
    };

    /// Most keys waiting for the GUI; newer ones are dropped beyond it.
    const QUEUE: usize = 1024;

    /// The hook's state, shared by the hook thread and the GUI's calls.
    #[derive(Default)]
    struct State {
        /// The viewer windows that grab, by native handle.
        windows: HashMap<usize, Grab>,
        /// The controller's CapsLock and NumLock, and the lock keys held
        /// (a held key repeats without toggling).
        caps: bool,
        num: bool,
        locks_held: BTreeSet<u32>,
        queue: VecDeque<KeyEvent>,
        /// The hook thread, while one runs.
        thread: Option<u32>,
    }

    impl State {
        fn push(&mut self, window: usize, code: u32, down: bool) {
            if self.queue.len() < QUEUE {
                self.queue.push_back(KeyEvent {
                    window,
                    code,
                    down: down as u8,
                    caps: self.caps as u8,
                    num: self.num as u8,
                    _pad: 0,
                });
                READY.notify_all();
            }
        }

        /// Queues the release of every key a window's grab sent down.
        fn release(&mut self, window: usize, grab: &mut Grab) {
            for code in std::mem::take(&mut grab.sent) {
                self.push(window, code, false);
            }
        }
    }

    static STATE: Mutex<Option<State>> = Mutex::new(None);
    static READY: Condvar = Condvar::new();

    /// Grabs the keyboard for a viewer window in a mode, or ends its grab
    /// (`mode` None). Keys are taken only while that window is in front.
    /// Returns: whether the hook runs for it (false when it cannot be
    /// installed, or for an ended grab).
    /// Side effects: installs the hook on a thread of its own with the
    /// first grab and removes it with the last; reads the lock keys'
    /// state on the calling thread (the GUI's); queues the release of the
    /// keys the window's grab held when its grab ends or changes mode.
    pub fn grab(window: usize, mode: Option<Mode>) -> bool {
        let Ok(mut guard) = STATE.lock() else {
            return false;
        };
        let st = guard.get_or_insert_with(State::default);
        // SAFETY: plain queries of the calling thread's keyboard state.
        unsafe {
            st.caps = GetKeyState(VK_CAPITAL.0 as i32) & 1 != 0;
            st.num = GetKeyState(VK_NUMLOCK.0 as i32) & 1 != 0;
        }
        let old = st.windows.remove(&window);
        if let Some(mut old) = old {
            if Some(old.mode) == mode {
                st.windows.insert(window, old);
            } else {
                st.release(window, &mut old);
            }
        }
        if let Some(mode) = mode {
            st.windows.entry(window).or_insert_with(|| Grab::new(mode));
        }
        match (st.windows.is_empty(), st.thread) {
            (true, Some(thread)) => {
                // SAFETY: posts WM_QUIT to the hook thread started below.
                let _ = unsafe { PostThreadMessageW(thread, WM_QUIT, WPARAM(0), LPARAM(0)) };
                st.thread = None;
            }
            (false, None) => match start() {
                Some(thread) => st.thread = Some(thread),
                None => {
                    st.windows.clear();
                    return false;
                }
            },
            _ => {}
        }
        mode.is_some()
    }

    /// Waits up to `timeout` for the next key taken for a viewer.
    pub fn next(timeout: Duration) -> Option<KeyEvent> {
        let guard = STATE.lock().ok()?;
        let (mut guard, _) = READY
            .wait_timeout_while(guard, timeout, |st| {
                st.as_ref().is_none_or(|st| st.queue.is_empty())
            })
            .ok()?;
        guard.as_mut()?.queue.pop_front()
    }

    /// Starts the hook thread.
    /// Returns: its id, or None when the hook could not be installed.
    fn start() -> Option<u32> {
        let (tx, rx) = std::sync::mpsc::channel();
        std::thread::spawn(move || {
            // SAFETY: the hook is installed and removed on this thread, which
            // pumps messages for it until WM_QUIT.
            unsafe {
                let module = GetModuleHandleW(None).ok().map(|m| HINSTANCE(m.0));
                let Ok(hook) = SetWindowsHookExW(WH_KEYBOARD_LL, Some(key_hook), module, 0) else {
                    let _ = tx.send(None);
                    return;
                };
                let _ = tx.send(Some(GetCurrentThreadId()));
                let mut msg = MSG::default();
                while GetMessageW(&mut msg, None, 0, 0).as_bool() {}
                let _ = UnhookWindowsHookEx(hook);
            }
        });
        rx.recv().ok().flatten()
    }

    /// Whether a key makes a character on the layout of the window in
    /// front with the modifiers held now (a dead key counts), without
    /// changing the layout's dead-key state. Win, or Ctrl or Alt alone,
    /// make a shortcut; Ctrl and Alt together are AltGr.
    fn types(window: HWND, vk: u32, scan: u32, caps: bool) -> bool {
        // SAFETY: plain queries of the keyboard state and the window's
        // layout; ToUnicodeEx with flag 4 leaves the layout's state alone.
        unsafe {
            let held = |vk: VIRTUAL_KEY| GetAsyncKeyState(vk.0 as i32) < 0;
            if held(VK_LWIN) || held(VK_RWIN) || held(VK_CONTROL) != held(VK_MENU) {
                return false;
            }
            let mut state = [0u8; 256];
            for vk in [
                VK_SHIFT,
                VK_LSHIFT,
                VK_RSHIFT,
                VK_CONTROL,
                VK_LCONTROL,
                VK_RCONTROL,
                VK_MENU,
                VK_LMENU,
                VK_RMENU,
            ] {
                if held(vk) {
                    state[vk.0 as usize] = 0x80;
                }
            }
            if caps {
                state[VK_CAPITAL.0 as usize] = 1;
            }
            let layout = GetKeyboardLayout(GetWindowThreadProcessId(window, None));
            let mut out = [0u16; 8];
            let n = ToUnicodeEx(vk, scan, &state, &mut out, 4, Some(layout));
            n < 0 || (n > 0 && out[0] >= 0x20 && out[0] != 0x7F)
        }
    }

    /// Takes the keys of a grabbing viewer window in front for its session
    /// and passes everything else on.
    unsafe extern "system" fn key_hook(code: i32, w: WPARAM, l: LPARAM) -> LRESULT {
        if code >= 0 && hook_event(w, l) {
            return LRESULT(1);
        }
        // SAFETY: passes the event on unchanged.
        unsafe { CallNextHookEx(None, code, w, l) }
    }

    /// Handles one key event in the hook.
    /// Returns: whether to swallow it.
    fn hook_event(w: WPARAM, l: LPARAM) -> bool {
        // SAFETY: for HC_ACTION, `l` points to the event's KBDLLHOOKSTRUCT.
        let info = unsafe { &*(l.0 as *const KBDLLHOOKSTRUCT) };
        let down = matches!(w.0 as u32, WM_KEYDOWN | WM_SYSKEYDOWN);
        let extended = info.flags.0 & LLKHF_EXTENDED.0 != 0;
        // SAFETY: a plain lookup in the keyboard layout's tables.
        let code = scan_code(info.vkCode, info.scanCode, extended, |vk| unsafe {
            MapVirtualKeyW(vk, MAPVK_VK_TO_VSC_EX)
        });
        let Ok(mut guard) = STATE.lock() else {
            return false;
        };
        let Some(st) = guard.as_mut() else {
            return false;
        };
        // Lock keys toggle on a press, not on its repeats.
        if let Some(c) = code.filter(|c| LOCKS.contains(c)) {
            if !down {
                st.locks_held.remove(&c);
            } else if st.locks_held.insert(c) {
                match c {
                    0x3A => st.caps = !st.caps,
                    0x45 => st.num = !st.num,
                    _ => {}
                }
            }
        }
        // SAFETY: a plain query of the window in front.
        let front = unsafe { GetForegroundWindow() };
        let window = front.0 as usize;
        // A window that left the front releases what it held there.
        let others: Vec<usize> = st
            .windows
            .iter()
            .filter(|(w, g)| **w != window && !g.sent.is_empty())
            .map(|(w, _)| *w)
            .collect();
        for w in others {
            if let Some(mut g) = st.windows.remove(&w) {
                st.release(w, &mut g);
                st.windows.insert(w, g);
            }
        }
        // Another program's injected keys stay the controller's.
        if info.flags.0 & LLKHF_INJECTED.0 != 0 {
            return false;
        }
        let caps = st.caps;
        let Some(grab) = st.windows.get_mut(&window) else {
            return false;
        };
        let verdict = grab.route(code, down, || {
            types(front, info.vkCode, info.scanCode, caps)
        });
        if let Some((code, down)) = verdict.send {
            st.push(window, code, down);
        }
        verdict.swallow
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn hook_events_get_windows_scan_codes() {
        // (virtual key, hook scan code, extended) → the code sent.
        let cases = [
            (0x41, 0x1E, false, Some(0x1E)),   // A
            (0x0D, 0x1C, false, Some(0x1C)),   // Enter
            (0x0D, 0x1C, true, Some(0xE01C)),  // numpad Enter
            (0x24, 0x47, true, Some(0xE047)),  // Home
            (0x67, 0x47, false, Some(0x47)),   // numpad 7
            (0x24, 0x47, false, Some(0x47)),   // numpad 7 with NumLock off
            (0xA2, 0x1D, false, Some(0x1D)),   // left Ctrl
            (0xA3, 0x1D, true, Some(0xE01D)),  // right Ctrl
            (0xA5, 0x38, true, Some(0xE038)),  // right Alt
            (0x5B, 0x5B, true, Some(0xE05B)),  // left Win
            (0x2C, 0x37, true, Some(0xE037)),  // Print Screen
            (0x90, 0x45, true, Some(0x45)),    // NumLock
            (0x13, 0x45, false, Some(0xE11D)), // Pause
            (0xA2, 0x21D, false, None),        // AltGr's made-up Ctrl
            (0xA0, 0x22A, true, None),         // a numpad key's made-up Shift
            (0xAD, 0, true, Some(0xE020)),     // a media key sent without a code
            (0xFF, 0, false, None),            // nothing to look up
        ];
        let lookup = |vk| if vk == 0xAD { 0xE020 } else { 0 };
        for (vk, scan, ext, want) in cases {
            assert_eq!(
                scan_code(vk, scan, ext, lookup),
                want,
                "vk {vk:#x} scan {scan:#x}"
            );
        }
    }

    #[test]
    fn modes_cross_by_name() {
        assert_eq!(Mode::from_name("map"), Some(Some(Mode::Map)));
        assert_eq!(Mode::from_name("translate"), Some(Some(Mode::Translate)));
        assert_eq!(Mode::from_name("off"), Some(None));
        assert_eq!(Mode::from_name("legacy"), None);
    }

    /// One step of a key sequence: the code (None: made up), press, whether
    /// it types on the controller's layout, and the verdict wanted.
    type Step = (Option<u32>, bool, bool, Option<(u32, bool)>, bool);

    #[test]
    fn grabs_route_keys_per_mode() {
        let sent = |c, d| Some((c, d));
        let cases: [(Mode, &[Step]); 6] = [
            // Map: every key by position, swallowed; locks pass too.
            (
                Mode::Map,
                &[
                    (Some(0x1E), true, true, sent(0x1E, true), true),
                    (Some(0x1E), true, true, sent(0x1E, true), true),
                    (Some(0x1E), false, true, sent(0x1E, false), true),
                    (Some(0x3A), true, false, sent(0x3A, true), false),
                    (Some(0x3A), false, false, sent(0x3A, false), false),
                    // A release whose press went to the controller stays there.
                    (Some(0x30), false, true, None, false),
                ],
            ),
            // Map: AltGr's made-up Ctrl is swallowed and not sent.
            (
                Mode::Map,
                &[
                    (None, true, false, None, true),
                    (Some(0xE038), true, false, sent(0xE038, true), true),
                    (Some(0xE038), false, false, sent(0xE038, false), true),
                ],
            ),
            // Translate: a typing key stays the controller's (its text is
            // sent); Shift goes both ways.
            (
                Mode::Translate,
                &[
                    (Some(0x2A), true, false, sent(0x2A, true), false),
                    (Some(0x1E), true, true, None, false),
                    (Some(0x1E), false, true, None, false),
                    (Some(0x2A), false, false, sent(0x2A, false), false),
                ],
            ),
            // Translate: a shortcut goes by position; Ctrl both ways.
            (
                Mode::Translate,
                &[
                    (Some(0x1D), true, false, sent(0x1D, true), false),
                    (Some(0x2E), true, false, sent(0x2E, true), true),
                    (Some(0x1D), false, false, sent(0x1D, false), false),
                    (Some(0x2E), false, false, sent(0x2E, false), true),
                ],
            ),
            // Translate: AltGr stays the controller's, held or repeating.
            (
                Mode::Translate,
                &[
                    (None, true, false, None, false),
                    (Some(0xE038), true, false, None, false),
                    (Some(0xE038), true, false, None, false),
                    (Some(0x10), true, true, None, false),
                    (Some(0x10), false, true, None, false),
                    (None, false, false, None, false),
                    (Some(0xE038), false, false, None, false),
                    // A plain right Alt afterwards is Alt.
                    (Some(0xE038), true, false, sent(0xE038, true), false),
                ],
            ),
            // Translate: Enter and Win make no text; a repeat of a sent key
            // stays sent even if it would type.
            (
                Mode::Translate,
                &[
                    (Some(0x1C), true, false, sent(0x1C, true), true),
                    (Some(0xE05B), true, false, sent(0xE05B, true), true),
                    (Some(0xE05B), true, true, sent(0xE05B, true), true),
                    (Some(0xE05B), false, false, sent(0xE05B, false), true),
                ],
            ),
        ];
        for (mode, steps) in cases {
            let mut g = Grab::new(mode);
            for (i, &(code, down, types, send, swallow)) in steps.iter().enumerate() {
                let v = g.route(code, down, || types);
                assert_eq!(v, Verdict { send, swallow }, "{mode:?} step {i}");
            }
        }
    }
}
