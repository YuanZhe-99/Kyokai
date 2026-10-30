//! Sleep and wake (doc/en-us/computer-use.md, "Sleep").
//!
//! While someone is connected to this host, the computer and its displays
//! stay awake, so a session is not cut by the idle timer and the captured
//! display does not go dark. When the computer goes to sleep anyway (the
//! user chose it, a lid closed), the AI task in progress pauses: it never
//! carries on from a screen it saw before the sleep.
//!
//! The hold is a power request on Windows and a pair of IOKit power
//! assertions on macOS; in the helper modes the session agent, in the
//! user's session, holds one too while it streams video.
//!
//! A display that is dark gives the capture nothing to send: the capture
//! wakes it (`wake_display`) when it has had no picture for a second, or
//! Windows has reported the display off for a second (a dark display is
//! as quiet as a still screen), at most every five seconds; a viewer's
//! Wake it does the same at once.
#![cfg_attr(not(any(windows, target_os = "macos")), allow(dead_code))]

use std::sync::{Arc, Weak};
use std::time::{Duration, Instant};

/// How long a capture goes without a picture (none yet, or its display
/// lost) before it wakes the display.
const DARK_AFTER: Duration = Duration::from_secs(1);
/// How often a capture wakes the display at most.
const WAKE_EVERY: Duration = Duration::from_secs(5);

/// Whether a capture should wake the display now.
/// Inputs: since when it has had no picture (None: it has one), and when
/// it last woke the display.
pub fn wake_due(dark_since: Option<Instant>, woke: Option<Instant>, now: Instant) -> bool {
    dark_since.is_some_and(|t| now.saturating_duration_since(t) > DARK_AFTER)
        && woke.is_none_or(|w| now.saturating_duration_since(w) >= WAKE_EVERY)
}

/// Wakes the displays as a person's input would, so a capture that waits
/// on a dark display gets a picture.
/// Side effects: in the helper modes the session agent wakes them (a
/// service's input reaches no desktop). Windows: on the input desktop,
/// resets the display idle timer (`SetThreadExecutionState(ES_DISPLAY_REQUIRED)`)
/// and moves the pointer one pixel and back with absolute moves, tagged as
/// Kyokai's input (the local-input watch leaves it out); macOS: declares
/// user activity (`IOPMAssertionDeclareUserActivity`). Elsewhere nothing.
pub fn wake_display() {
    if let Some(link) = crate::agent::link() {
        link.wake_display();
    } else {
        #[cfg(any(windows, target_os = "macos"))]
        imp::wake();
    }
}

/// Whether the system reports the display off (Windows' console display
/// state; macOS: the main display asleep, or no display active while one is
/// connected; elsewhere never).
pub fn display_off() -> bool {
    #[cfg(windows)]
    {
        imp::display_off()
    }
    #[cfg(target_os = "macos")]
    {
        crate::mac::asleep()
    }
    #[cfg(not(any(windows, target_os = "macos")))]
    false
}

#[cfg(windows)]
use windows_impl as imp;

#[cfg(target_os = "macos")]
use mac_impl as imp;

/// Keeps the computer and its displays awake while held; every holder
/// shares one power request.
pub struct Awake(());

/// The shared power request and how many hold it.
#[cfg(any(windows, target_os = "macos"))]
static AWAKE: std::sync::Mutex<(usize, Option<imp::Request>)> = std::sync::Mutex::new((0, None));

/// Keeps the computer awake until the guard is dropped.
/// Side effects: the first guard sets a system and a display power request
/// ("Kyokai: someone is connected", shown by `powercfg /requests` on
/// Windows and `pmset -g assertions` on macOS); dropping the last one
/// releases it.
pub fn keep_awake() -> Awake {
    #[cfg(any(windows, target_os = "macos"))]
    {
        let mut awake = AWAKE.lock().expect("power request");
        if awake.0 == 0 {
            awake.1 = imp::request();
        }
        awake.0 += 1;
    }
    Awake(())
}

impl Drop for Awake {
    fn drop(&mut self) {
        #[cfg(any(windows, target_os = "macos"))]
        {
            let mut awake = AWAKE.lock().expect("power request");
            awake.0 -= 1;
            if awake.0 == 0
                && let Some(h) = awake.1.take()
            {
                imp::release(h);
            }
        }
    }
}

/// Whether a power request is held now (for tests).
#[cfg(all(test, any(windows, target_os = "macos")))]
fn awake() -> bool {
    AWAKE.lock().expect("power request").1.is_some()
}

/// Pauses the AI task in progress whenever the computer goes to sleep,
/// until dropped.
pub struct Suspend {
    #[cfg(windows)]
    _registration: Option<windows_impl::Registration>,
}

impl Suspend {
    /// Starts watching for sleep on behalf of `ai`.
    /// Notes: off Windows nothing is watched yet (the Mac pass).
    pub fn watch(ai: &Arc<crate::ai::Ai>) -> Suspend {
        #[cfg(windows)]
        {
            Suspend {
                _registration: windows_impl::Registration::new(Arc::downgrade(ai)),
            }
        }
        #[cfg(not(windows))]
        {
            let _ = ai;
            Suspend {}
        }
    }
}

/// What the sleep notification calls: the AI pauses, if it still exists.
#[cfg_attr(not(windows), allow(dead_code))]
pub(crate) fn going_to_sleep(ai: &Weak<crate::ai::Ai>) {
    if let Some(ai) = ai.upgrade() {
        ai.pause_current(crate::ai::Pause::Sleep);
    }
}

#[cfg(windows)]
mod windows_impl {
    use super::*;
    use std::ffi::c_void;
    use windows::Win32::Foundation::{CloseHandle, ERROR_SUCCESS, HANDLE};
    use windows::Win32::System::Power::{
        DEVICE_NOTIFY_SUBSCRIBE_PARAMETERS, HPOWERNOTIFY, POWERBROADCAST_SETTING,
        PowerClearRequest, PowerCreateRequest, PowerRegisterSuspendResumeNotification,
        PowerRequestDisplayRequired, PowerRequestSystemRequired, PowerSetRequest,
        PowerSettingRegisterNotification, PowerUnregisterSuspendResumeNotification,
    };
    use windows::Win32::System::Threading::{
        POWER_REQUEST_CONTEXT_SIMPLE_STRING, REASON_CONTEXT, REASON_CONTEXT_0,
    };
    use windows::Win32::UI::WindowsAndMessaging::{
        DEVICE_NOTIFY_CALLBACK, PBT_APMSUSPEND, PBT_POWERSETTINGCHANGE,
    };
    use windows::core::PWSTR;

    /// The power request's handle, as an integer so the static is Send.
    pub type Request = isize;

    /// Creates and sets the power request.
    /// Returns: its handle as an integer, or None when Windows refused.
    pub fn request() -> Option<isize> {
        let mut reason: Vec<u16> = "Kyokai: someone is connected\0".encode_utf16().collect();
        let context = REASON_CONTEXT {
            Version: 0, // POWER_REQUEST_CONTEXT_VERSION
            Flags: POWER_REQUEST_CONTEXT_SIMPLE_STRING,
            Reason: REASON_CONTEXT_0 {
                SimpleReasonString: PWSTR(reason.as_mut_ptr()),
            },
        };
        // SAFETY: `context` and its string live through the call, which
        // copies them; the handle is closed in `release`.
        unsafe {
            let h = PowerCreateRequest(&context).ok()?;
            let _ = PowerSetRequest(h, PowerRequestSystemRequired);
            let _ = PowerSetRequest(h, PowerRequestDisplayRequired);
            Some(h.0 as isize)
        }
    }

    /// Wakes the displays (`super::wake_display`): resets the display idle
    /// timer, which turns a dark display on (measured on the ARM64 test
    /// computer: on within 5 s, the Kernel-Power reason
    /// SetThreadExecutionState), and moves the pointer a pixel and back on
    /// the desktop that receives input, with absolute moves, which mouse
    /// acceleration cannot shrink to nothing (regression: a one-pixel
    /// relative move woke no display).
    /// Notes: the move runs on a thread of its own, which follows the input
    /// desktop (the agent's, to the lock screen): the caller — often a
    /// capture thread — stays on its desktop, with the capturer it opened
    /// there (regression: a wake moved the capture's thread behind its
    /// capturer, and its check for a moved desktop saw nothing).
    pub fn wake() {
        use windows::Win32::System::Power::{ES_DISPLAY_REQUIRED, SetThreadExecutionState};
        // SAFETY: a one-shot execution state, no pointers.
        unsafe { SetThreadExecutionState(ES_DISPLAY_REQUIRED) };
        let _ = std::thread::Builder::new().name("wake".into()).spawn(|| {
            crate::agent::follow_input_desktop();
            crate::input::nudge_pointer();
        });
    }

    /// GUID_CONSOLE_DISPLAY_STATE: the console's display turned off, on or
    /// dimmed.
    const CONSOLE_DISPLAY_STATE: windows::core::GUID =
        windows::core::GUID::from_u128(0x6fe69556_704a_47a0_8f24_c28d936fda47);
    /// The console display's state as Windows last reported it: 0 off,
    /// 1 on, 2 dimmed; UNREPORTED before the first report.
    static DISPLAY: std::sync::atomic::AtomicU32 = std::sync::atomic::AtomicU32::new(UNREPORTED);
    /// DISPLAY before Windows' first report, which comes on another thread
    /// just after registering.
    const UNREPORTED: u32 = u32::MAX;
    /// How long the first call waits for that report.
    const FIRST_REPORT: std::time::Duration = std::time::Duration::from_millis(500);
    static WATCHING: std::sync::Once = std::sync::Once::new();

    /// Whether Windows reports the console's display off.
    /// Side effects: the first call registers for the display state, for
    /// the process's life, and waits up to FIRST_REPORT for Windows' first
    /// report (unreported reads as on).
    pub fn display_off() -> bool {
        WATCHING.call_once(|| {
            // Windows keeps a pointer to the parameters while registered:
            // they live as long as the process.
            let params = Box::leak(Box::new(DEVICE_NOTIFY_SUBSCRIBE_PARAMETERS {
                Callback: Some(display_changed),
                Context: std::ptr::null_mut(),
            }));
            let mut handle = std::ptr::null_mut();
            // SAFETY: `params` is never freed; with DEVICE_NOTIFY_CALLBACK
            // the recipient is the parameters.
            let r = unsafe {
                PowerSettingRegisterNotification(
                    &CONSOLE_DISPLAY_STATE,
                    DEVICE_NOTIFY_CALLBACK,
                    HANDLE(params as *mut DEVICE_NOTIFY_SUBSCRIBE_PARAMETERS as *mut c_void),
                    &mut handle,
                )
            };
            if r != ERROR_SUCCESS {
                log::info!("power: the display state is not reported ({})", r.0);
            } else {
                // Regression: the first stream of a session took the GPU's
                // encoder in the dark, the display read as on until Windows
                // reported it.
                let until = std::time::Instant::now() + FIRST_REPORT;
                while DISPLAY.load(std::sync::atomic::Ordering::Relaxed) == UNREPORTED
                    && std::time::Instant::now() < until
                {
                    std::thread::sleep(std::time::Duration::from_millis(5));
                }
            }
        });
        DISPLAY.load(std::sync::atomic::Ordering::Relaxed) == 0
    }

    /// Notes the console display's state from a setting change.
    unsafe extern "system" fn display_changed(
        _: *const c_void,
        kind: u32,
        setting: *const c_void,
    ) -> u32 {
        if kind == PBT_POWERSETTINGCHANGE && !setting.is_null() {
            // SAFETY: for PBT_POWERSETTINGCHANGE `setting` is a
            // POWERBROADCAST_SETTING whose data (a DWORD for this setting)
            // follows its header; read unaligned.
            let s = unsafe { &*(setting as *const POWERBROADCAST_SETTING) };
            if s.PowerSetting == CONSOLE_DISPLAY_STATE && s.DataLength >= 4 {
                // SAFETY: DataLength says the DWORD is there (see above).
                let v = unsafe { std::ptr::read_unaligned(s.Data.as_ptr() as *const u32) };
                DISPLAY.store(v, std::sync::atomic::Ordering::Relaxed);
            }
        }
        0
    }

    /// Clears and closes the power request.
    pub fn release(h: isize) {
        let h = HANDLE(h as *mut c_void);
        // SAFETY: `h` came from `request` and is closed once.
        unsafe {
            let _ = PowerClearRequest(h, PowerRequestSystemRequired);
            let _ = PowerClearRequest(h, PowerRequestDisplayRequired);
            let _ = CloseHandle(h);
        }
    }

    /// A registration for sleep notifications; its context is a boxed
    /// `Weak<Ai>` freed after unregistering.
    pub struct Registration {
        handle: *mut c_void,
        context: *mut Weak<crate::ai::Ai>,
        // Windows keeps a pointer to the parameters while registered.
        params: Box<DEVICE_NOTIFY_SUBSCRIBE_PARAMETERS>,
    }

    // SAFETY: the handle and the context are used only to unregister and
    // free them, once, in Drop; the callback only reads the Weak.
    unsafe impl Send for Registration {}
    // SAFETY: as above; shared references never touch the pointers.
    unsafe impl Sync for Registration {}

    impl Registration {
        /// Registers for sleep notifications.
        /// Returns: None when Windows refused (the AI is then not paused
        /// at sleep; a stale screen is still rejected).
        pub fn new(ai: Weak<crate::ai::Ai>) -> Option<Registration> {
            let context = Box::into_raw(Box::new(ai));
            let mut params = Box::new(DEVICE_NOTIFY_SUBSCRIBE_PARAMETERS {
                Callback: Some(notified),
                Context: context as *mut c_void,
            });
            let mut handle = std::ptr::null_mut();
            // SAFETY: `params` and `context` stay alive until unregistered
            // in Drop; with DEVICE_NOTIFY_CALLBACK the recipient is the
            // parameters.
            let r = unsafe {
                PowerRegisterSuspendResumeNotification(
                    DEVICE_NOTIFY_CALLBACK,
                    HANDLE(&mut *params as *mut DEVICE_NOTIFY_SUBSCRIBE_PARAMETERS as *mut c_void),
                    &mut handle,
                )
            };
            if r != ERROR_SUCCESS {
                // SAFETY: not registered, so nothing else holds the context.
                drop(unsafe { Box::from_raw(context) });
                return None;
            }
            Some(Registration {
                handle,
                context,
                params,
            })
        }
    }

    impl Drop for Registration {
        fn drop(&mut self) {
            // SAFETY: unregisters the handle from `new`; after it returns no
            // callback runs, so the context can be freed.
            unsafe {
                let _ = PowerUnregisterSuspendResumeNotification(HPOWERNOTIFY(self.handle as _));
                drop(Box::from_raw(self.context));
            }
            let _ = &self.params;
        }
    }

    /// The sleep notification.
    unsafe extern "system" fn notified(context: *const c_void, kind: u32, _: *const c_void) -> u32 {
        if kind == PBT_APMSUSPEND {
            // SAFETY: the context is the registration's live Weak<Ai>.
            let ai = unsafe { &*(context as *const Weak<crate::ai::Ai>) };
            going_to_sleep(ai);
        }
        ERROR_SUCCESS.0
    }
}

#[cfg(target_os = "macos")]
mod mac_impl {
    use objc2_core_foundation::CFString;

    /// The idle system sleep and idle display sleep assertions' IDs; 0
    /// (kIOPMNullAssertionID) for one macOS refused.
    pub type Request = [u32; 2];

    /// kIOPMAssertionLevelOn.
    const LEVEL_ON: u32 = 255;

    #[link(name = "IOKit", kind = "framework")]
    unsafe extern "C" {
        fn IOPMAssertionCreateWithName(
            kind: &CFString,
            level: u32,
            name: &CFString,
            id: *mut u32,
        ) -> i32;
        fn IOPMAssertionRelease(id: u32) -> i32;
        fn IOPMAssertionDeclareUserActivity(name: &CFString, kind: u32, id: *mut u32) -> i32;
    }

    /// The user-activity assertion, passed again so that each wake extends
    /// it rather than making another (macOS lets it lapse by itself).
    static ACTIVITY: std::sync::atomic::AtomicU32 = std::sync::atomic::AtomicU32::new(0);

    /// Wakes the displays (`super::wake_display`): local user activity
    /// (kIOPMUserActiveLocal), as a key press would.
    pub fn wake() {
        use std::sync::atomic::Ordering;
        let name = CFString::from_static_str("Kyokai: waking the display for a connection");
        let mut id = ACTIVITY.load(Ordering::Relaxed);
        // SAFETY: the name lives through the call; `id` is a valid in/out
        // pointer to the assertion this function made before, or 0.
        if unsafe { IOPMAssertionDeclareUserActivity(&name, 0, &mut id) } == 0 {
            ACTIVITY.store(id, Ordering::Relaxed);
        }
    }

    /// Takes the idle system sleep and idle display sleep assertions.
    /// Returns: their IDs, or None when macOS refused both.
    pub fn request() -> Option<Request> {
        let name = CFString::from_static_str("Kyokai: someone is connected");
        let ids = [
            "PreventUserIdleSystemSleep", // kIOPMAssertionTypePreventUserIdleSystemSleep
            "PreventUserIdleDisplaySleep", // kIOPMAssertionTypePreventUserIdleDisplaySleep
        ]
        .map(|kind| {
            let kind = CFString::from_static_str(kind);
            let mut id = 0;
            // SAFETY: both strings live through the call, which retains what
            // it keeps; `id` is a valid out pointer; the assertion is
            // released in `release`.
            let r = unsafe { IOPMAssertionCreateWithName(&kind, LEVEL_ON, &name, &mut id) };
            if r == 0 { id } else { 0 }
        });
        (ids != [0, 0]).then_some(ids)
    }

    /// Releases the assertions.
    pub fn release(ids: Request) {
        for id in ids.into_iter().filter(|&id| id != 0) {
            // SAFETY: `id` came from `request` and is released once.
            unsafe {
                IOPMAssertionRelease(id);
            }
        }
    }
}

#[cfg(test)]
mod wake_tests {
    use super::*;

    /// A capture wakes the display after a second without a picture, then
    /// at most every five seconds; never while it has a picture.
    #[test]
    fn a_dark_display_is_woken_at_a_bounded_rate() {
        let now = Instant::now() + Duration::from_secs(60);
        let ago = |ms: u64| Some(now - Duration::from_millis(ms));
        let cases = [
            (None, None, false),
            (None, ago(10_000), false),
            (ago(500), None, false),
            (ago(1001), None, true),
            (ago(30_000), ago(4_900), false),
            (ago(30_000), ago(5_000), true),
        ];
        for (dark, woke, want) in cases {
            assert_eq!(wake_due(dark, woke, now), want, "{dark:?} {woke:?}");
        }
    }
}

#[cfg(all(test, any(windows, target_os = "macos")))]
mod tests {
    use super::*;

    /// (Sessions in other tests may hold guards too, so only holding is
    /// checked.)
    #[test]
    fn one_power_request_lasts_while_any_guard_does() {
        let a = keep_awake();
        let b = keep_awake();
        assert!(awake());
        drop(a);
        assert!(awake());
        drop(b);
    }
}
