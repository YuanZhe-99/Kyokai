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
//! A display that is dark already gives the capture nothing to send: the
//! capture wakes it (`wake_display`) when it has had no picture for a
//! second, at most every five seconds.

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
/// Side effects: Windows: resets the display idle timer
/// (`SetThreadExecutionState(ES_DISPLAY_REQUIRED)`) and moves the pointer
/// one pixel and back, tagged as Kyokai's input (the local-input watch
/// leaves it out); macOS: declares user activity
/// (`IOPMAssertionDeclareUserActivity`). Elsewhere nothing.
/// Notes: call it in the session that shows the display (the agent's, in
/// the helper modes): a service's input reaches no desktop.
pub fn wake_display() {
    #[cfg(any(windows, target_os = "macos"))]
    imp::wake();
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
        DEVICE_NOTIFY_SUBSCRIBE_PARAMETERS, HPOWERNOTIFY, PowerClearRequest, PowerCreateRequest,
        PowerRegisterSuspendResumeNotification, PowerRequestDisplayRequired,
        PowerRequestSystemRequired, PowerSetRequest, PowerUnregisterSuspendResumeNotification,
    };
    use windows::Win32::System::Threading::{
        POWER_REQUEST_CONTEXT_SIMPLE_STRING, REASON_CONTEXT, REASON_CONTEXT_0,
    };
    use windows::Win32::UI::WindowsAndMessaging::{DEVICE_NOTIFY_CALLBACK, PBT_APMSUSPEND};
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

    /// Wakes the displays (`super::wake_display`).
    pub fn wake() {
        use windows::Win32::System::Power::{ES_DISPLAY_REQUIRED, SetThreadExecutionState};
        use windows::Win32::UI::Input::KeyboardAndMouse::{
            INPUT, INPUT_0, INPUT_MOUSE, MOUSEEVENTF_MOVE, MOUSEINPUT, SendInput,
        };
        let step = |dx: i32| INPUT {
            r#type: INPUT_MOUSE,
            Anonymous: INPUT_0 {
                mi: MOUSEINPUT {
                    dx,
                    dy: 0,
                    mouseData: 0,
                    dwFlags: MOUSEEVENTF_MOVE,
                    time: 0,
                    dwExtraInfo: crate::input::INJECTED_TAG,
                },
            },
        };
        // SAFETY: a one-shot execution state and two relative moves from
        // a local array, whose size is passed.
        unsafe {
            SetThreadExecutionState(ES_DISPLAY_REQUIRED);
            SendInput(&[step(1), step(-1)], std::mem::size_of::<INPUT>() as i32);
        }
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
