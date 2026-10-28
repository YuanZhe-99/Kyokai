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
    /// Keys that type go through the controller's layout as text (its
    /// input method stays off in the picture); every other key and
    /// shortcut goes by position.
    Translate,
}

/// What the GUI asks of a viewer window's grab, by the name it crosses the
/// FFI as.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Ask {
    /// `map` or `translate`: grab in that mode.
    Grab(Mode),
    /// `off`: no grab.
    Off,
}

impl Ask {
    /// Returns: None for an unknown name.
    pub fn from_name(name: &str) -> Option<Ask> {
        match name {
            "map" => Some(Ask::Grab(Mode::Map)),
            "translate" => Some(Ask::Grab(Mode::Translate)),
            "off" => Some(Ask::Off),
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

/// Windows' virtual key of a typed character injected as Unicode text.
#[cfg_attr(not(windows), allow(dead_code))]
const VK_PACKET: u32 = 0xE7;

/// Whether the Windows hook leaves an injected key to the window in front
/// instead of taking it for a grabbing viewer: Kyokai's own (a host on this
/// computer; remote input never operates Kyokai's windows) and a character
/// injected as text (`VK_PACKET`, as RustDesk's Translate and Legacy modes
/// type), which reaches the viewer as text. Every other injected key — what
/// another remote-control program operating this computer types — is taken
/// like a typed one (regression: Kyokai inside a RustDesk session got none
/// of its keys, and its keyboard seemed dead).
#[cfg_attr(not(windows), allow(dead_code))]
pub fn leaves_injected(extra: usize, vk: u32) -> bool {
    extra == crate::input::INJECTED_TAG || vk == VK_PACKET
}

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
    /// sent key stays sent. Nothing composes here: the GUI keeps the
    /// controller's input method off in the picture (regression: while it
    /// was on, the editing keys waited up to a second after each letter on
    /// a Chinese, Japanese or Korean layout, and an Enter typed then was
    /// lost).
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

/// How a viewer window's grab stands, as `kyokai_keyboard_state` reports it
/// to the GUI (its status dots, its log): whether the keys typed into the
/// window reach the grab, or what lets them pass.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Standing {
    /// The grab takes the window's keys.
    Taking,
    /// The window holds no grab.
    None,
    /// No tap or hook runs (macOS turned the tap off, or it could not
    /// start).
    Off,
    /// macOS keeps keys from every event tap while Secure Event Input is
    /// on: the program holding it, when known.
    SecureInput(String),
    /// The keys go to another window's grab (on macOS, the window that
    /// grabbed last).
    OtherWindow,
    /// The GUI has read no key for longer than the core waits for it.
    NoReader,
    /// Another program's window is in front of Kyokai's: its name, when
    /// known.
    Behind(String),
}

impl Standing {
    /// The name the standing crosses the FFI as.
    pub fn name(&self) -> &'static str {
        match self {
            Standing::Taking => "taking",
            Standing::None => "none",
            Standing::Off => "off",
            Standing::SecureInput(_) => "secure-input",
            Standing::OtherWindow => "other-window",
            Standing::NoReader => "no-reader",
            Standing::Behind(_) => "behind",
        }
    }

    /// The program the standing names ("" for none).
    pub fn holder(&self) -> &str {
        match self {
            Standing::SecureInput(p) | Standing::Behind(p) => p,
            _ => "",
        }
    }
}

/// Decides how a window's grab stands, in the order a key meets what can
/// let it pass: the window's grab, the tap or hook, Secure Event Input, the
/// window the keys go to, the GUI's reader, the window in front.
/// Inputs: `secure` — the program holding Secure Event Input, when it is
/// on; `behind` — asked last (it reads the window list): the program in
/// front of Kyokai's window, None while Kyokai's is in front.
#[cfg_attr(not(any(windows, target_os = "macos")), allow(dead_code))]
fn standing(
    grabbed: bool,
    running: bool,
    secure: Option<String>,
    front: bool,
    reader: bool,
    behind: impl FnOnce() -> Option<String>,
) -> Standing {
    match () {
        _ if !grabbed => Standing::None,
        _ if !running => Standing::Off,
        _ if secure.is_some() => Standing::SecureInput(secure.unwrap_or_default()),
        _ if !front => Standing::OtherWindow,
        _ if !reader => Standing::NoReader,
        _ => match behind() {
            Some(program) => Standing::Behind(program),
            None => Standing::Taking,
        },
    }
}

#[cfg(target_os = "macos")]
pub use mac_impl::{grab, next, state};
#[cfg(windows)]
pub use windows_impl::{grab, next, state};

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

/// How a viewer window's grab stands. Not available here.
/// Returns: `Standing::None`.
#[cfg(not(any(windows, target_os = "macos")))]
pub fn state(_window: usize) -> Standing {
    Standing::None
}

/// The keyboard grab on macOS: an event tap that takes the keys going to
/// Kyokai while a viewer grabs, as RustDesk's grab does there.
#[cfg(target_os = "macos")]
mod mac_impl {
    use super::*;
    use crate::input::INJECTED_TAG;
    use objc2_core_foundation::{
        CFMachPort, CFRetained, CFRunLoop, CFRunLoopRunResult, kCFRunLoopCommonModes,
        kCFRunLoopDefaultMode,
    };
    use objc2_core_graphics::{
        CGEvent, CGEventField, CGEventFlags, CGEventMask, CGEventTapLocation, CGEventTapOptions,
        CGEventTapPlacement, CGEventTapProxy, CGEventType,
    };
    use std::collections::{HashMap, VecDeque};
    use std::ffi::c_void;
    use std::ptr::NonNull;
    use std::sync::atomic::{AtomicBool, AtomicPtr, Ordering};
    use std::sync::{Arc, Condvar, Mutex};
    use std::time::Duration;

    /// Most keys waiting for the GUI; newer ones are dropped beyond it.
    const QUEUE: usize = 1024;
    /// Caps Lock's Mac key code.
    const CAPS_LOCK: u16 = 0x39;
    /// How long the tap's run loop runs before it looks at its stop flag.
    const SLICE: f64 = 0.5;

    /// A tap on a thread of its own: its run loop and port, and the flags
    /// its thread shares with the thread that ends the last grab.
    struct Tap {
        run_loop: CFRetained<CFRunLoop>,
        port: CFRetained<CFMachPort>,
        /// Asks the thread to end: its run loop runs in slices and looks at
        /// the flag between them, so a stop asked before it ran is not lost
        /// (CFRunLoopStop stops only a loop that runs; regression: such a
        /// stop left a tap running until Kyokai quit).
        stop: Arc<AtomicBool>,
        /// Set by the thread as it ends.
        done: Arc<AtomicBool>,
    }

    // SAFETY: CFRunLoopStop, CGEventTapEnable and CGEventTapIsEnabled may be
    // called from any thread.
    unsafe impl Send for Tap {}

    impl Tap {
        /// Whether the thread runs and macOS has the tap on.
        fn running(&self) -> bool {
            !self.done.load(Ordering::Acquire) && CGEvent::tap_is_enabled(&self.port)
        }

        /// Ends the thread, which switches the tap off and invalidates it.
        fn end(&self) {
            self.stop.store(true, Ordering::Release);
            self.run_loop.stop();
        }
    }

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
        tap: Option<Tap>,
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

    /// Grabs the keyboard for a viewer window in a mode, or ends its grab
    /// (`Ask::Off`). A grab asked again renews the tap.
    /// Returns: whether the tap runs for it (false without the
    /// Accessibility permission, or for an ended grab).
    /// Side effects: starts the tap on a thread of its own with the first
    /// grab and stops it with the last; on a grab, switches the tap on
    /// again when macOS switched it off, and starts a new one when its
    /// thread ended; queues the release of the keys a window's grab held
    /// when its grab ends or changes mode.
    pub fn grab(window: usize, ask: Ask) -> bool {
        let Ok(mut guard) = STATE.lock() else {
            return false;
        };
        let st = guard.get_or_insert_with(State::default);
        st.caps = crate::mac::caps_lock();
        let keeps = |g: &Grab| matches!(ask, Ask::Grab(mode) if g.mode == mode);
        if st.windows.get(&window).is_some_and(|g| !keeps(g)) {
            st.release(window);
            st.windows.remove(&window);
        }
        match ask {
            Ask::Grab(mode) => {
                st.windows.entry(window).or_insert_with(|| Grab::new(mode));
                st.front = Some(window);
            }
            Ask::Off if st.front == Some(window) => st.front = None,
            Ask::Off => {}
        }
        if st.windows.is_empty() {
            if let Some(tap) = st.tap.take() {
                tap.end();
            }
            return false;
        }
        if ask == Ask::Off {
            return false;
        }
        match &st.tap {
            Some(tap) if !tap.done.load(Ordering::Acquire) => {
                if !CGEvent::tap_is_enabled(&tap.port) {
                    CGEvent::tap_enable(&tap.port, true);
                }
            }
            _ => match start() {
                Some(tap) => st.tap = Some(tap),
                None => {
                    st.tap = None;
                    st.windows.clear();
                    st.front = None;
                    return false;
                }
            },
        }
        true
    }

    /// How a viewer window's grab stands now (`Standing`).
    /// Side effects: reads the session's Secure Event Input holder and, last,
    /// the frontmost application.
    pub fn state(window: usize) -> Standing {
        let secure = crate::mac::secure_input_holder();
        let (grabbed, running, front, reader) = {
            let Ok(guard) = STATE.lock() else {
                return Standing::Off;
            };
            let Some(st) = guard.as_ref() else {
                return Standing::None;
            };
            (
                st.windows.contains_key(&window),
                st.tap.as_ref().is_some_and(Tap::running),
                st.front == Some(window),
                st.read.is_some_and(|t| t.elapsed() <= READER_GONE),
            )
        };
        standing(
            grabbed,
            running,
            secure,
            front,
            reader,
            || match crate::mac::front_app() {
                Some(pid) if pid as i64 == std::process::id() as i64 => None,
                Some(pid) => Some(crate::mac::process_name(pid)),
                None => Some(String::new()),
            },
        )
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
    /// Returns: the running tap, or None when macOS refused it (no
    /// Accessibility permission).
    fn start() -> Option<Tap> {
        let (tx, rx) = std::sync::mpsc::sync_channel(1);
        std::thread::spawn(move || {
            let mask: CGEventMask = [
                CGEventType::KeyDown,
                CGEventType::KeyUp,
                CGEventType::FlagsChanged,
            ]
            .iter()
            .fold(0, |m, t| m | 1 << t.0);
            // The callback switches its own tap on again through `info`
            // (regression: a shared pointer, which a tap stopping just as a
            // new one started could clear, left the new one off for good
            // once macOS switched it off). The thread keeps the cell until
            // the tap is invalidated.
            let cell = Box::new(AtomicPtr::<CFMachPort>::new(std::ptr::null_mut()));
            let info = std::ptr::from_ref::<AtomicPtr<CFMachPort>>(&*cell)
                .cast_mut()
                .cast::<c_void>();
            // SAFETY: `tapped` matches the callback type; `info` points to
            // the cell, which outlives the tap.
            let tap = unsafe {
                CGEvent::tap_create(
                    CGEventTapLocation::SessionEventTap,
                    CGEventTapPlacement::HeadInsertEventTap,
                    CGEventTapOptions::Default,
                    mask,
                    Some(tapped),
                    info,
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
            cell.store(CFRetained::as_ptr(&tap).as_ptr(), Ordering::Release);
            // SAFETY: an immutable constant of the framework.
            run_loop.add_source(Some(&source), unsafe { kCFRunLoopCommonModes });
            CGEvent::tap_enable(&tap, true);
            let (stop, done) = (
                Arc::new(AtomicBool::new(false)),
                Arc::new(AtomicBool::new(false)),
            );
            let _ = tx.send(Some(Tap {
                run_loop: run_loop.clone(),
                port: tap.clone(),
                stop: stop.clone(),
                done: done.clone(),
            }));
            while !stop.load(Ordering::Acquire) {
                // SAFETY: an immutable constant of the framework.
                let mode = unsafe { kCFRunLoopDefaultMode };
                // Finished: the tap's source went (macOS invalidated it).
                if CFRunLoop::run_in_mode(mode, SLICE, false) == CFRunLoopRunResult::Finished {
                    break;
                }
            }
            // The tap goes with its run loop: left installed, macOS would
            // wait on it for every key of every program (regression: the
            // keyboard froze everywhere until Kyokai quit).
            CGEvent::tap_enable(&tap, false);
            source.invalidate();
            tap.invalidate();
            done.store(true, Ordering::Release);
            drop(cell);
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
        info: *mut c_void,
    ) -> *mut CGEvent {
        if kind == CGEventType::TapDisabledByTimeout || kind == CGEventType::TapDisabledByUserInput
        {
            // SAFETY: `info` is the tap's cell (`start`), which its thread
            // keeps until the tap is invalidated; the tap it points to lives
            // as long.
            let cell = unsafe { &*info.cast::<AtomicPtr<CFMachPort>>() };
            // SAFETY: as above.
            if let Some(tap) = unsafe { cell.load(Ordering::Acquire).as_ref() } {
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

    /// Whether Kyokai is the frontmost application (`mac::front_app`).
    /// Tests route events as if it were.
    /// Notes: which viewer gets the keys is the GUI's to say (it grabs while
    /// the pointer is over the picture). Regressions: a check here that the
    /// window at the pointer was Kyokai's read the whole window list, whose
    /// invisible windows of other programs (and the system's) cover
    /// Kyokai's; the tap let every key pass while the viewer took itself for
    /// grabbing, and a Mac sent no Enter, Tab, Delete, Shift or Command. Up to
    /// 0.6.5 the frontmost normal window of that list stood for the front;
    /// with Kyokai's window tiled to half the screen the second dot turned
    /// amber and keys passed.
    fn front_is_ours(ours: i64) -> bool {
        cfg!(test) || crate::mac::front_app().is_some_and(|pid| pid as i64 == ours)
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
        let verdict = grab.route(Some(code), down, || types(e));
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
    use std::sync::atomic::{AtomicU32, Ordering};
    use std::sync::{Condvar, Mutex};
    use std::time::Duration;
    use windows::Win32::Foundation::{HINSTANCE, HWND, LPARAM, LRESULT, POINT, WPARAM};
    use windows::Win32::System::LibraryLoader::GetModuleHandleW;
    use windows::Win32::System::Threading::GetCurrentThreadId;
    use windows::Win32::UI::Input::KeyboardAndMouse::*;
    use windows::Win32::UI::WindowsAndMessaging::{
        CallNextHookEx, GA_ROOT, GetAncestor, GetCursorPos, GetForegroundWindow, GetMessageW,
        GetWindowThreadProcessId, HHOOK, KBDLLHOOKSTRUCT, LLKHF_EXTENDED, LLKHF_INJECTED, MSG,
        PostThreadMessageW, SetWindowsHookExW, UnhookWindowsHookEx, WH_KEYBOARD_LL, WM_APP,
        WM_KEYDOWN, WM_QUIT, WM_SYSKEYDOWN, WindowFromPoint,
    };

    /// Most keys waiting for the GUI; newer ones are dropped beyond it.
    const QUEUE: usize = 1024;
    /// Asks the hook thread to install its hook again.
    const REHOOK: u32 = WM_APP + 1;
    /// The hook thread whose hook is installed (0: none). Windows removes a
    /// low-level hook that kept it waiting too long, without notice: a grab
    /// asked again installs it again.
    static HOOKED: AtomicU32 = AtomicU32::new(0);

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

    /// Grabs the keyboard for a viewer window in a mode, or ends its grab
    /// (`Ask::Off`). Keys are taken only while that window is in front. A
    /// grab asked again renews the hook.
    /// Returns: whether the hook runs for it (false when it cannot be
    /// installed, or for an ended grab).
    /// Side effects: installs the hook on a thread of its own with the
    /// first grab and removes it with the last, and installs it again on
    /// later grabs; reads the lock keys' state on the calling thread (the
    /// GUI's); queues the release of the keys the window's grab held when
    /// its grab ends or changes mode.
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
                    st.windows.insert(window, old);
                }
                _ => st.release(window, &mut old),
            }
        }
        if let Ask::Grab(mode) = ask {
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
            (false, Some(thread)) if ask != Ask::Off => {
                // SAFETY: posts REHOOK to the hook thread started below.
                let _ = unsafe { PostThreadMessageW(thread, REHOOK, WPARAM(0), LPARAM(0)) };
            }
            _ => {}
        }
        ask != Ask::Off
    }

    /// How a viewer window's grab stands now (`Standing`).
    pub fn state(window: usize) -> Standing {
        let (grabbed, running, reader) = {
            let Ok(guard) = STATE.lock() else {
                return Standing::Off;
            };
            let Some(st) = guard.as_ref() else {
                return Standing::None;
            };
            (
                st.windows.contains_key(&window),
                st.thread
                    .is_some_and(|t| HOOKED.load(Ordering::Acquire) == t),
                st.read.is_some_and(|t| t.elapsed() <= READER_GONE),
            )
        };
        // The hook takes keys for the window in front while the pointer is
        // over it (`hook_event`).
        standing(grabbed, running, None, true, reader, || {
            // SAFETY: a plain query of the window in front.
            let front = unsafe { GetForegroundWindow() };
            (front.0 as usize != window || !pointer_over(front)).then(String::new)
        })
    }

    /// How long after the GUI last asked for a key the hook still takes keys:
    /// a GUI that stopped reading (stuck, or gone) never traps the keyboard
    /// for longer.
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

    /// Starts the hook thread.
    /// Returns: its id, or None when the hook could not be installed.
    fn start() -> Option<u32> {
        let (tx, rx) = std::sync::mpsc::channel();
        std::thread::spawn(move || {
            // SAFETY: the hook is installed and removed on this thread, which
            // pumps messages for it until WM_QUIT.
            unsafe {
                let module = GetModuleHandleW(None).ok().map(|m| HINSTANCE(m.0));
                let install = || SetWindowsHookExW(WH_KEYBOARD_LL, Some(key_hook), module, 0).ok();
                let Some(first) = install() else {
                    let _ = tx.send(None);
                    return;
                };
                let id = GetCurrentThreadId();
                let mut hook: Option<HHOOK> = Some(first);
                HOOKED.store(id, Ordering::Release);
                let _ = tx.send(Some(id));
                let mut msg = MSG::default();
                while GetMessageW(&mut msg, None, 0, 0).as_bool() {
                    // Only the thread in `State::thread` is sent REHOOK.
                    if msg.message == REHOOK {
                        if let Some(h) = hook.take() {
                            let _ = UnhookWindowsHookEx(h);
                        }
                        hook = install();
                        HOOKED.store(if hook.is_some() { id } else { 0 }, Ordering::Release);
                    }
                }
                if let Some(h) = hook {
                    let _ = UnhookWindowsHookEx(h);
                }
                // Only this thread's: a newer one may have installed its own.
                let _ = HOOKED.compare_exchange(id, 0, Ordering::AcqRel, Ordering::Acquire);
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

    /// Whether the pointer is over a part of `window` that shows: the
    /// topmost window at the pointer belongs to it.
    fn pointer_over(window: HWND) -> bool {
        let mut at = POINT::default();
        // SAFETY: plain queries of the pointer and the windows under it.
        unsafe {
            if GetCursorPos(&mut at).is_err() {
                return false;
            }
            let under = WindowFromPoint(at);
            !under.is_invalid() && GetAncestor(under, GA_ROOT) == window
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
        // The keys belong to the viewer under the pointer: the window in
        // front counts only while the pointer is over a part of it that
        // shows (another program's window over it takes them back).
        let window = if pointer_over(front) {
            front.0 as usize
        } else {
            0
        };
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
        // Injected keys are taken like typed ones (another remote-control
        // program operating this computer types through injection), but for
        // those `leaves_injected` names.
        if info.flags.0 & LLKHF_INJECTED.0 != 0 && leaves_injected(info.dwExtraInfo, info.vkCode) {
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

    /// Keys another remote-control program injects (RustDesk operating
    /// this computer) are taken for a grabbing viewer; Kyokai's own and
    /// characters injected as text are left alone (regression: Kyokai
    /// inside a RustDesk session got none of its keys).
    #[test]
    fn foreign_injected_keys_are_taken() {
        use crate::input::INJECTED_TAG;
        for (extra, vk, leave) in [
            (0, 0x41, false),                // RustDesk's A, by scan code
            (0x1234, 0x0D, false),           // another tool's Enter
            (INJECTED_TAG, 0x41, true),      // Kyokai's own, as a host
            (0, VK_PACKET, true),            // a character as Unicode text
            (INJECTED_TAG, VK_PACKET, true), // both
        ] {
            assert_eq!(leaves_injected(extra, vk), leave, "{extra:#x} {vk:#x}");
        }
    }

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
        assert_eq!(Ask::from_name("pass"), None);
        assert_eq!(Ask::from_name("off"), Some(Ask::Off));
        assert_eq!(Ask::from_name("legacy"), None);
    }

    /// Translate never holds a key back for the controller's input method,
    /// which stays off in the picture: after a letter that types (as text),
    /// Enter goes to the host at once (regression: on a Chinese, Japanese or
    /// Korean layout the editing keys waited up to a second after each
    /// letter, and an Enter typed then was lost).
    #[test]
    fn translate_sends_enter_right_after_a_letter() {
        let mut g = Grab::new(Mode::Translate);
        assert_eq!(g.route(Some(0x31), true, || true), PASS);
        assert_eq!(g.route(Some(0x31), false, || true), PASS);
        for (down, send) in [(true, Some((0x1C, true))), (false, Some((0x1C, false)))] {
            let v = g.route(Some(0x1C), down, || false);
            assert_eq!(
                v,
                Verdict {
                    send,
                    swallow: true
                }
            );
        }
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

    /// A grab's standing names the first thing a key meets that lets it
    /// pass, and reads the window list only when nothing before it does;
    /// the names are what crosses the FFI.
    #[test]
    fn standing_names_what_lets_keys_pass() {
        struct Case {
            grabbed: bool,
            running: bool,
            secure: bool,
            front: bool,
            reader: bool,
            behind: Option<&'static str>,
            want: Standing,
            name: &'static str,
        }
        let case = |grabbed, running, secure, front, reader, behind, want, name| Case {
            grabbed,
            running,
            secure,
            front,
            reader,
            behind,
            want,
            name,
        };
        #[rustfmt::skip]
        let cases = [
            case(false, false, true, false, false, None, Standing::None, "none"),
            case(true, false, true, false, false, None, Standing::Off, "off"),
            case(true, true, true, false, false, None, Standing::SecureInput("Terminal".into()), "secure-input"),
            case(true, true, false, false, false, None, Standing::OtherWindow, "other-window"),
            case(true, true, false, true, false, None, Standing::NoReader, "no-reader"),
            case(true, true, false, true, true, Some("Finder"), Standing::Behind("Finder".into()), "behind"),
            case(true, true, false, true, true, Some(""), Standing::Behind(String::new()), "behind"),
            case(true, true, false, true, true, None, Standing::Taking, "taking"),
        ];
        for c in cases {
            let asked = std::cell::Cell::new(false);
            let secure = c.secure.then(|| "Terminal".to_string());
            let got = standing(c.grabbed, c.running, secure, c.front, c.reader, || {
                asked.set(true);
                c.behind.map(str::to_string)
            });
            assert_eq!(got, c.want);
            assert_eq!(got.name(), c.name);
            let listed = c.name == "behind" || c.name == "taking";
            assert_eq!(asked.get(), listed, "{}: window list read", c.name);
        }
        assert_eq!(
            Standing::SecureInput("Terminal".into()).holder(),
            "Terminal"
        );
        assert_eq!(Standing::OtherWindow.holder(), "");
    }
}
