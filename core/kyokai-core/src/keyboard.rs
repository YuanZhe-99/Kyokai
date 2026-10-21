//! The controller's keyboard (doc/en-us/computer-use.md, "Keyboard
//! modes"): while a viewer window is in front, a low-level keyboard hook
//! takes the physical keys, by scan code, for that viewer's session —
//! RustDesk's Map and Translate modes. Legacy mode grabs nothing and leaves
//! keys to the GUI toolkit.
//!
//! Windows takes the keys with a low-level keyboard hook, macOS with an
//! event tap (it needs Kyokai's Accessibility permission); both route them
//! through `Grab`.
#![cfg_attr(not(any(windows, target_os = "macos")), allow(dead_code))]

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

/// What the GUI asks of a viewer window's grab, by the name it crosses the
/// FFI as.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Ask {
    /// `map` or `translate`: grab in that mode. Asked again in the same
    /// mode, it also says the controller's input method composes nothing.
    Grab(Mode),
    /// `pass`: the controller's input method composes (the GUI got a
    /// composition): every key passes to it until the GUI asks to grab
    /// again; keys sent down are still released through the grab.
    Pass,
    /// `off`: no grab.
    Off,
}

impl Ask {
    /// Returns: None for an unknown name.
    pub fn from_name(name: &str) -> Option<Ask> {
        match name {
            "map" => Some(Ask::Grab(Mode::Map)),
            "translate" => Some(Ask::Grab(Mode::Translate)),
            "pass" => Some(Ask::Pass),
            "off" => Some(Ask::Off),
            _ => None,
        }
    }
}

/// The keys an input method edits its composition with: Enter, Esc,
/// Backspace, Tab, the arrows, Space and the digits (its candidates).
/// After a key that typed on a layout with an input method, these pass to
/// it until the GUI says no composition is open (IME_WAIT at most): its
/// composition event may come after the next key.
const EDITING: [u32; 10] = [
    0x1C, 0xE01C, 0x01, 0x0E, 0x0F, 0xE048, 0xE050, 0xE04B, 0xE04D, 0x39,
];

/// Whether a key edits an input method's composition (`EDITING`, the
/// digit row 1–0 too).
fn edits(code: u32) -> bool {
    EDITING.contains(&code) || (0x02..=0x0B).contains(&code)
}

/// How long after a key that typed on an input-method layout the editing
/// keys pass, unless the GUI says sooner that nothing is composed.
const IME_WAIT: std::time::Duration = std::time::Duration::from_secs(1);

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
#[cfg_attr(not(windows), allow(dead_code))]
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
/// when the grab ends or the window leaves the front), AltGr, and the
/// controller's input method.
#[derive(Debug)]
pub struct Grab {
    pub mode: Mode,
    pub sent: BTreeSet<u32>,
    /// Windows made up AltGr's Ctrl: the right Alt that follows is AltGr.
    altgr_next: bool,
    /// AltGr is held (Translate: it stays the controller's).
    altgr: bool,
    /// The controller's input method composes (`Ask::Pass`): every press
    /// passes to it.
    pub passing: bool,
    /// When a key that typed on an input-method layout passed (Translate):
    /// the editing keys pass for IME_WAIT unless the GUI says first that
    /// nothing is composed.
    pub ime_typed: Option<std::time::Instant>,
}

impl Grab {
    pub fn new(mode: Mode) -> Grab {
        Grab {
            mode,
            sent: BTreeSet::new(),
            altgr_next: false,
            altgr: false,
            passing: false,
            ime_typed: None,
        }
    }

    /// Takes the GUI's word on the input method: composing (`Ask::Pass`)
    /// or not (asked to grab again in the same mode).
    pub fn composing(&mut self, on: bool) {
        self.passing = on;
        self.ime_typed = None;
    }

    /// Decides one key event of the window in front.
    /// Inputs: `code` from `scan_code` (None for a made-up key); `types`
    /// tells whether the key makes a character on the controller's layout
    /// with the modifiers held (asked only in Translate mode); `ime`
    /// whether that layout has an input method (asked only for a key that
    /// types).
    /// Notes: a release is sent exactly when its press was; a repeat of a
    /// sent key stays sent; while the controller's input method composes,
    /// or may have begun to after a key it typed, its keys stay with it
    /// (regression: Enter, Esc and Backspace went to the host, so a
    /// composition never ended and swallowed every later key).
    pub fn route(
        &mut self,
        code: Option<u32>,
        down: bool,
        types: impl FnOnce() -> bool,
        ime: impl FnOnce() -> bool,
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
        if self.passing && !self.sent.contains(&code) {
            return PASS;
        }
        if self.mode == Mode::Translate {
            if code == RIGHT_ALT && (altgr_next || self.altgr) {
                self.altgr = true;
                return PASS;
            }
            let composing = self.ime_typed.is_some_and(|t| t.elapsed() < IME_WAIT);
            if composing && edits(code) && !self.sent.contains(&code) {
                return PASS;
            }
            if !self.passes(code) && !self.sent.contains(&code) && types() {
                if ime() {
                    self.ime_typed = Some(std::time::Instant::now());
                }
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

#[cfg(target_os = "macos")]
pub use mac_impl::{grab, next};
#[cfg(windows)]
pub use windows_impl::{grab, next};

/// Grabs the keyboard for a viewer window. Not available here.
/// Returns: false.
#[cfg(not(any(windows, target_os = "macos")))]
pub fn grab(_window: usize, _ask: Ask) -> bool {
    false
}

/// The next key taken for a viewer. Not available here.
/// Returns: None.
#[cfg(not(any(windows, target_os = "macos")))]
pub fn next(_timeout: std::time::Duration) -> Option<KeyEvent> {
    None
}

/// The keyboard grab on macOS: an event tap that takes the keys going to
/// Kyokai while a viewer grabs, as RustDesk's grab does there.
#[cfg(target_os = "macos")]
mod mac_impl {
    use super::*;
    use crate::input::INJECTED_TAG;
    use objc2_core_foundation::{CFMachPort, CFRetained, CFRunLoop, kCFRunLoopCommonModes};
    use objc2_core_graphics::{
        CGEvent, CGEventField, CGEventFlags, CGEventMask, CGEventTapLocation, CGEventTapOptions,
        CGEventTapPlacement, CGEventTapProxy, CGEventType,
    };
    use std::collections::{HashMap, VecDeque};
    use std::ffi::c_void;
    use std::ptr::NonNull;
    use std::sync::atomic::{AtomicPtr, Ordering};
    use std::sync::{Condvar, Mutex};
    use std::time::Duration;

    /// Most keys waiting for the GUI; newer ones are dropped beyond it.
    const QUEUE: usize = 1024;
    /// Caps Lock's Mac key code.
    const CAPS_LOCK: u16 = 0x39;

    /// The tap's run loop, stopped from the thread that ends the last grab.
    struct Loop(CFRetained<CFRunLoop>);

    // SAFETY: CFRunLoopStop may be called from any thread.
    unsafe impl Send for Loop {}

    /// The grab's state, shared by the tap's thread and the GUI's calls.
    #[derive(Default)]
    struct State {
        /// The viewer windows that grab, by native handle, and the one
        /// that grabbed last: the window in front (a viewer grabs only
        /// while its picture has the focus in the key window).
        windows: HashMap<usize, Grab>,
        front: Option<usize>,
        /// The controller's Caps Lock (a Mac has no NumLock).
        caps: bool,
        queue: VecDeque<KeyEvent>,
        run_loop: Option<Loop>,
        /// When the GUI last asked for a key: keys are taken only while it
        /// reads them, so none are lost when its reader is gone.
        read: Option<std::time::Instant>,
    }

    impl State {
        fn push(&mut self, window: usize, code: u32, down: bool) {
            if self.queue.len() < QUEUE {
                self.queue.push_back(KeyEvent {
                    window,
                    code,
                    down: down as u8,
                    caps: self.caps as u8,
                    num: 0,
                    _pad: 0,
                });
                READY.notify_all();
            }
        }

        /// Queues the release of every key a window's grab sent down.
        fn release(&mut self, window: usize) {
            if let Some(g) = self.windows.get_mut(&window) {
                let sent = std::mem::take(&mut g.sent);
                for code in sent {
                    self.push(window, code, false);
                }
            }
        }
    }

    static STATE: Mutex<Option<State>> = Mutex::new(None);
    static READY: Condvar = Condvar::new();
    /// The tap, to switch it on again when macOS turned it off.
    static TAP: AtomicPtr<CFMachPort> = AtomicPtr::new(std::ptr::null_mut());

    /// Grabs the keyboard for a viewer window in a mode, passes its keys to
    /// the controller's input method while it composes (`Ask::Pass`), or
    /// ends its grab (`Ask::Off`).
    /// Returns: whether the tap runs for it (false without the
    /// Accessibility permission, or for an ended grab).
    /// Side effects: starts the tap on a thread of its own with the first
    /// grab and stops it with the last; queues the release of the keys a
    /// window's grab held when its grab ends or changes mode.
    pub fn grab(window: usize, ask: Ask) -> bool {
        let Ok(mut guard) = STATE.lock() else {
            return false;
        };
        let st = guard.get_or_insert_with(State::default);
        st.caps = crate::mac::caps_lock();
        let keeps = |g: &Grab| match ask {
            Ask::Grab(mode) => g.mode == mode,
            Ask::Pass => true,
            Ask::Off => false,
        };
        if st.windows.get(&window).is_some_and(|g| !keeps(g)) {
            st.release(window);
            st.windows.remove(&window);
        }
        match ask {
            Ask::Grab(mode) => {
                st.windows
                    .entry(window)
                    .or_insert_with(|| Grab::new(mode))
                    .composing(false);
                st.front = Some(window);
            }
            Ask::Pass => {
                st.windows
                    .entry(window)
                    .or_insert_with(|| Grab::new(Mode::Translate))
                    .composing(true);
                st.front = Some(window);
            }
            Ask::Off if st.front == Some(window) => st.front = None,
            Ask::Off => {}
        }
        match (st.windows.is_empty(), st.run_loop.is_some()) {
            (true, true) => {
                if let Some(l) = st.run_loop.take() {
                    l.0.stop();
                }
            }
            (false, false) => match start() {
                Some(l) => st.run_loop = Some(l),
                None => {
                    st.windows.clear();
                    st.front = None;
                    return false;
                }
            },
            _ => {}
        }
        ask != Ask::Off
    }

    /// How long after the GUI last asked for a key the tap still takes keys:
    /// a GUI that stopped reading never traps the keyboard for longer.
    const READER_GONE: Duration = Duration::from_secs(1);

    /// Waits up to `timeout` for the next key taken for a viewer.
    pub fn next(timeout: Duration) -> Option<KeyEvent> {
        let mut guard = STATE.lock().ok()?;
        if let Some(st) = guard.as_mut() {
            st.read = Some(std::time::Instant::now());
        }
        let (mut guard, _) = READY
            .wait_timeout_while(guard, timeout, |st| {
                st.as_ref().is_none_or(|st| st.queue.is_empty())
            })
            .ok()?;
        guard.as_mut()?.queue.pop_front()
    }

    /// Starts the tap on a thread of its own.
    /// Returns: its run loop, or None when macOS refused the tap (no
    /// Accessibility permission).
    fn start() -> Option<Loop> {
        let (tx, rx) = std::sync::mpsc::sync_channel(1);
        std::thread::spawn(move || {
            let mask: CGEventMask = [
                CGEventType::KeyDown,
                CGEventType::KeyUp,
                CGEventType::FlagsChanged,
            ]
            .iter()
            .fold(0, |m, t| m | 1 << t.0);
            // SAFETY: `tapped` matches the callback type and keeps no
            // pointer from `info`.
            let tap = unsafe {
                CGEvent::tap_create(
                    CGEventTapLocation::SessionEventTap,
                    CGEventTapPlacement::HeadInsertEventTap,
                    CGEventTapOptions::Default,
                    mask,
                    Some(tapped),
                    std::ptr::null_mut(),
                )
            };
            let Some(tap) = tap else {
                let _ = tx.send(None);
                return;
            };
            let (Some(source), Some(run_loop)) = (
                CFMachPort::new_run_loop_source(None, Some(&tap), 0),
                CFRunLoop::current(),
            ) else {
                let _ = tx.send(None);
                return;
            };
            TAP.store(CFRetained::as_ptr(&tap).as_ptr(), Ordering::Release);
            // SAFETY: an immutable constant of the framework.
            run_loop.add_source(Some(&source), unsafe { kCFRunLoopCommonModes });
            CGEvent::tap_enable(&tap, true);
            let _ = tx.send(Some(Loop(run_loop)));
            CFRunLoop::run();
            // The tap goes with its run loop: left installed, macOS would
            // wait on it for every key of every program (regression: the
            // keyboard froze everywhere until Kyokai quit).
            TAP.store(std::ptr::null_mut(), Ordering::Release);
            CGEvent::tap_enable(&tap, false);
            source.invalidate();
            tap.invalidate();
        });
        rx.recv().ok().flatten()
    }

    /// Whether a key event types a character on the controller's layout:
    /// text comes with it, and neither Command nor Control is held (they
    /// make shortcuts).
    fn types(e: &CGEvent) -> bool {
        if CGEvent::flags(Some(e)).intersects(CGEventFlags::MaskCommand | CGEventFlags::MaskControl)
        {
            return false;
        }
        let mut text = [0u16; 4];
        let mut n = 0;
        // SAFETY: `text` holds 4 units and `n` receives the count.
        unsafe { CGEvent::keyboard_get_unicode_string(Some(e), 4, &mut n, text.as_mut_ptr()) };
        n > 0 && text[0] >= 0x20 && text[0] != 0x7F
    }

    /// The tap's callback: takes the keys of the viewer in front.
    unsafe extern "C-unwind" fn tapped(
        _proxy: CGEventTapProxy,
        kind: CGEventType,
        event: NonNull<CGEvent>,
        _info: *mut c_void,
    ) -> *mut CGEvent {
        if kind == CGEventType::TapDisabledByTimeout || kind == CGEventType::TapDisabledByUserInput
        {
            // SAFETY: the tap lives as long as its thread's run loop runs.
            if let Some(tap) = unsafe { TAP.load(Ordering::Acquire).as_ref() } {
                CGEvent::tap_enable(tap, true);
            }
            return event.as_ptr();
        }
        // SAFETY: the event is valid for the callback.
        let e = unsafe { event.as_ref() };
        match route(kind, e) {
            true => std::ptr::null_mut(),
            false => event.as_ptr(),
        }
    }

    /// Whether Kyokai owns the frontmost window. Tests route events as if
    /// it did.
    fn front_is_ours(ours: i64) -> bool {
        cfg!(test) || crate::mac::window_owner(None).is_some_and(|pid| pid as i64 == ours)
    }

    /// Handles one key event in the tap.
    /// Returns: whether to swallow it.
    fn route(kind: CGEventType, e: &CGEvent) -> bool {
        // Kyokai's own injected keys (a host on this Mac) stay as they are.
        if CGEvent::integer_value_field(Some(e), CGEventField::EventSourceUserData)
            == INJECTED_TAG as i64
        {
            return false;
        }
        let Ok(mut guard) = STATE.try_lock() else {
            return false;
        };
        let Some(st) = guard.as_mut() else {
            return false;
        };
        let Some(window) = st.front else {
            return false;
        };
        // Nobody reads the keys (the GUI's reader ended): take none.
        if st.read.is_none_or(|t| t.elapsed() > READER_GONE) {
            st.release(window);
            return false;
        }
        // Keys for another program (Kyokai left the front before its
        // viewer said so) are its own; what the viewer held is let go. The
        // event's target may not be known yet, so Kyokai must also own the
        // frontmost window.
        let ours = std::process::id() as i64;
        let target = CGEvent::integer_value_field(Some(e), CGEventField::EventTargetUnixProcessID);
        if target != 0 && target != ours || !front_is_ours(ours) {
            st.release(window);
            return false;
        }
        let keycode =
            CGEvent::integer_value_field(Some(e), CGEventField::KeyboardEventKeycode) as u16;
        // Keys no scan code names (Fn, the media keys) stay this Mac's.
        let Some(code) = crate::mac::keycode_scan(keycode) else {
            return false;
        };
        let flags = CGEvent::flags(Some(e));
        // Caps Lock reports only its toggles: it is sent as a press and a
        // release, and passes to this Mac too.
        if kind == CGEventType::FlagsChanged && keycode == CAPS_LOCK {
            st.caps = flags.contains(CGEventFlags::MaskAlphaShift);
            for down in [true, false] {
                st.push(window, 0x3A, down);
            }
            return false;
        }
        let down = match kind {
            CGEventType::KeyDown => true,
            CGEventType::KeyUp => false,
            CGEventType::FlagsChanged => match crate::mac::modifier_flag(keycode) {
                Some(flag) => flags.contains(flag),
                None => return false,
            },
            _ => return false,
        };
        let Some(grab) = st.windows.get_mut(&window) else {
            return false;
        };
        // The tap does not tell a Mac's input method apart: the GUI's
        // composition (Ask::Pass) covers it.
        let verdict = grab.route(Some(code), down, || types(e), || false);
        if let Some((code, down)) = verdict.send {
            st.push(window, code, down);
        }
        verdict.swallow
    }

    #[cfg(test)]
    mod tests {
        use super::*;
        use objc2_core_foundation::CFRetained;

        /// A key event as the tap would see it, never posted.
        fn key(
            kind: CGEventType,
            code: u16,
            flags: CGEventFlags,
            text: &str,
        ) -> CFRetained<CGEvent> {
            let e = CGEvent::new_keyboard_event(None, code, kind != CGEventType::KeyUp).unwrap();
            CGEvent::set_type(Some(&e), kind);
            CGEvent::set_flags(Some(&e), flags);
            let units: Vec<u16> = text.encode_utf16().collect();
            // SAFETY: `units` holds `units.len()` UTF-16 units.
            unsafe {
                CGEvent::keyboard_set_unicode_string(Some(&e), units.len() as _, units.as_ptr())
            };
            e
        }

        /// The tap's routing of Mac key events: in Map mode Cmd and the
        /// shortcut's key go to the host by scan code and stay off this
        /// Mac; in Translate mode a key that types stays this Mac's (its
        /// text follows), while Cmd shortcuts still go; keys no scan code
        /// names pass.
        #[test]
        fn mac_keys_reach_the_viewer_by_scan_code() {
            let route_in = |mode: Mode, events: &[(CGEventType, u16, CGEventFlags, &str)]| {
                *STATE.lock().unwrap() = Some(State {
                    windows: HashMap::from([(7, Grab::new(mode))]),
                    front: Some(7),
                    read: Some(std::time::Instant::now()),
                    ..State::default()
                });
                let swallowed: Vec<bool> = events
                    .iter()
                    .map(|(kind, code, flags, text)| route(*kind, &key(*kind, *code, *flags, text)))
                    .collect();
                let sent: Vec<(u32, bool)> = STATE
                    .lock()
                    .unwrap()
                    .take()
                    .unwrap()
                    .queue
                    .iter()
                    .map(|k| (k.code, k.down != 0))
                    .collect();
                (swallowed, sent)
            };
            let cmd = CGEventFlags::MaskCommand;
            let none = CGEventFlags::empty();
            let copy = [
                (CGEventType::FlagsChanged, 0x37, cmd, ""),
                (CGEventType::KeyDown, 0x08, cmd, "c"),
                (CGEventType::KeyUp, 0x08, cmd, "c"),
                (CGEventType::FlagsChanged, 0x37, none, ""),
            ];
            let (swallowed, sent) = route_in(Mode::Map, &copy);
            assert_eq!(swallowed, [true; 4]);
            assert_eq!(
                sent,
                [(0xE05B, true), (0x2E, true), (0x2E, false), (0xE05B, false)]
            );
            let (swallowed, sent) = route_in(Mode::Translate, &copy);
            assert_eq!(swallowed, [true; 4], "Translate sends shortcuts too");
            assert_eq!(sent.len(), 4);
            let typed = [
                (CGEventType::KeyDown, 0x00, none, "a"),
                (CGEventType::KeyUp, 0x00, none, "a"),
            ];
            let (swallowed, sent) = route_in(Mode::Translate, &typed);
            assert_eq!((swallowed, sent), (vec![false, false], vec![]));
            let (swallowed, sent) = route_in(Mode::Map, &typed);
            assert_eq!(
                (swallowed, sent),
                (vec![true, true], vec![(0x1E, true), (0x1E, false)])
            );
            // Without a reader (the GUI's ended) nothing is taken.
            *STATE.lock().unwrap() = Some(State {
                windows: HashMap::from([(7, Grab::new(Mode::Map))]),
                front: Some(7),
                ..State::default()
            });
            let e = key(CGEventType::KeyDown, 0x00, none, "a");
            assert!(!route(CGEventType::KeyDown, &e), "no reader, no grab");
            assert!(STATE.lock().unwrap().take().unwrap().queue.is_empty());
            // Fn has no scan code: it stays this Mac's.
            let (swallowed, sent) =
                route_in(Mode::Map, &[(CGEventType::FlagsChanged, 0x3F, none, "")]);
            assert_eq!((swallowed, sent), (vec![false], vec![]));
        }
    }
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
        /// When the GUI last asked for a key: keys are taken only while it
        /// reads them, so none are lost when its reader is gone.
        read: Option<std::time::Instant>,
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

    /// Grabs the keyboard for a viewer window in a mode, passes its keys to
    /// the controller's input method while it composes (`Ask::Pass`), or
    /// ends its grab (`Ask::Off`). Keys are taken only while that window is
    /// in front.
    /// Returns: whether the hook runs for it (false when it cannot be
    /// installed, or for an ended grab).
    /// Side effects: installs the hook on a thread of its own with the
    /// first grab and removes it with the last; reads the lock keys'
    /// state on the calling thread (the GUI's); queues the release of the
    /// keys the window's grab held when its grab ends or changes mode.
    pub fn grab(window: usize, ask: Ask) -> bool {
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
            match ask {
                Ask::Grab(mode) if old.mode == mode => {
                    old.composing(false);
                    st.windows.insert(window, old);
                }
                Ask::Pass => {
                    old.composing(true);
                    st.windows.insert(window, old);
                }
                _ => st.release(window, &mut old),
            }
        }
        match ask {
            Ask::Grab(mode) => {
                st.windows.entry(window).or_insert_with(|| Grab::new(mode));
            }
            Ask::Pass => {
                st.windows.entry(window).or_insert_with(|| {
                    let mut g = Grab::new(Mode::Translate);
                    g.composing(true);
                    g
                });
            }
            Ask::Off => {}
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
        ask != Ask::Off
    }

    /// How long after the GUI last asked for a key the hook still takes keys:
    /// a GUI that stopped reading (stuck, or gone) never traps the keyboard
    /// for longer.
    const READER_GONE: Duration = Duration::from_secs(1);

    /// Whether the keyboard layout of a window has an input method: the
    /// Chinese, Japanese and Korean layouts (by their primary language).
    fn ime_layout(window: HWND) -> bool {
        // SAFETY: plain queries of the window's thread and its layout.
        unsafe {
            let layout = GetKeyboardLayout(GetWindowThreadProcessId(window, None));
            let lang = layout.0 as usize & 0x3FF;
            matches!(lang, 0x04 | 0x11 | 0x12)
        }
    }

    /// Waits up to `timeout` for the next key taken for a viewer.
    pub fn next(timeout: Duration) -> Option<KeyEvent> {
        let mut guard = STATE.lock().ok()?;
        if let Some(st) = guard.as_mut() {
            st.read = Some(std::time::Instant::now());
        }
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
        // The hook never waits: a key that comes while the GUI's call holds
        // the state passes on (Windows ends a hook that takes too long, and
        // a waiting hook stalls every program's keyboard).
        let Ok(mut guard) = STATE.try_lock() else {
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
        // Nobody reads the keys (the GUI's reader ended or is stuck): take
        // none, and let go of what was taken.
        if st.read.is_none_or(|t| t.elapsed() > READER_GONE) {
            if let Some(mut g) = st.windows.remove(&window) {
                st.release(window, &mut g);
                st.windows.insert(window, g);
            }
            return false;
        }
        let caps = st.caps;
        let Some(grab) = st.windows.get_mut(&window) else {
            return false;
        };
        let verdict = grab.route(
            code,
            down,
            || types(front, info.vkCode, info.scanCode, caps),
            || ime_layout(front),
        );
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
        assert_eq!(Ask::from_name("map"), Some(Ask::Grab(Mode::Map)));
        assert_eq!(
            Ask::from_name("translate"),
            Some(Ask::Grab(Mode::Translate))
        );
        assert_eq!(Ask::from_name("pass"), Some(Ask::Pass));
        assert_eq!(Ask::from_name("off"), Some(Ask::Off));
        assert_eq!(Ask::from_name("legacy"), None);
    }

    /// Translate with an input method: a key that types starts a
    /// composition, after which the editing keys pass to it until the GUI
    /// says nothing is composed; while it composes (`pass`) every key does;
    /// a key sent down before is still released through the grab
    /// (regression: Enter, Esc and Backspace went to the host, and the
    /// composition swallowed every later key).
    #[test]
    fn an_input_method_keeps_its_keys_in_translate() {
        let sent = |c, d| Some((c, d));
        // (code, down, types, ime layout, send, swallow)
        type Ime = (u32, bool, bool, bool, Option<(u32, bool)>, bool);
        let steps: &[Ime] = &[
            // Shift goes both ways, as without an input method.
            (0x2A, true, false, true, sent(0x2A, true), false),
            // "n" types on a Pinyin layout: the input method composes.
            (0x31, true, true, true, None, false),
            (0x31, false, true, true, None, false),
            // Enter, Backspace, Esc and the arrows edit the composition.
            (0x1C, true, false, true, None, false),
            (0x1C, false, false, true, None, false),
            (0x0E, true, false, true, None, false),
            (0x01, true, false, true, None, false),
            (0xE04B, true, false, true, None, false),
            // A shortcut key that does not edit goes to the host.
            (0xE053, true, false, true, sent(0xE053, true), true),
            (0xE053, false, false, true, sent(0xE053, false), true),
            // Shift's release, sent down before, still reaches the host.
            (0x2A, false, false, true, sent(0x2A, false), false),
        ];
        let mut g = Grab::new(Mode::Translate);
        for (i, &(code, down, types, ime, send, swallow)) in steps.iter().enumerate() {
            let v = g.route(Some(code), down, || types, || ime);
            assert_eq!(v, Verdict { send, swallow }, "step {i}");
        }
        // The GUI says nothing is composed: Enter goes to the host again.
        g.composing(false);
        let v = g.route(Some(0x1C), true, || false, || true);
        assert_eq!(v.send, Some((0x1C, true)));
        g.route(Some(0x1C), false, || false, || true);
        // The race window ends by itself after IME_WAIT.
        g.route(Some(0x31), true, || true, || true);
        g.ime_typed = Some(std::time::Instant::now() - IME_WAIT);
        assert_eq!(
            g.route(Some(0x1C), true, || false, || true).send,
            Some((0x1C, true))
        );
        g.route(Some(0x1C), false, || false, || true);
        // While the GUI sees a composition (`pass`), every key passes, a
        // shortcut too; a key down from before is released through the grab.
        g.route(Some(0x1D), true, || false, || false);
        g.composing(true);
        for code in [0x1C, 0x01, 0x2E, 0xE05B] {
            assert_eq!(g.route(Some(code), true, || false, || false), PASS);
        }
        assert_eq!(
            g.route(Some(0x1D), false, || false, || false),
            Verdict {
                send: Some((0x1D, false)),
                swallow: false
            }
        );
        // A layout without an input method opens no race window.
        let mut g = Grab::new(Mode::Translate);
        g.route(Some(0x1E), true, || true, || false);
        assert_eq!(
            g.route(Some(0x1C), true, || false, || false).send,
            Some((0x1C, true))
        );
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
                let v = g.route(code, down, || types, || false);
                assert_eq!(v, Verdict { send, swallow }, "{mode:?} step {i}");
            }
        }
    }
}
