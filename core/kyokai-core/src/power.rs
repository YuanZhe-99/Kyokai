//! Sleep and wake (doc/en-us/computer-use.md, "Sleep").
//!
//! While someone is connected to this host, the computer and its displays
//! stay awake, so a session is not cut by the idle timer and the captured
//! display does not go dark. When the computer goes to sleep anyway (the
//! user chose it, a lid closed), the AI task in progress pauses: it never
//! carries on from a screen it saw before the sleep.

use std::sync::{Arc, Mutex, Weak};

/// Keeps the computer and its displays awake while held; every holder
/// shares one power request.
pub struct Awake(());

/// The shared power request and how many hold it.
#[cfg(windows)]
static AWAKE: Mutex<(usize, Option<isize>)> = Mutex::new((0, None));

/// Keeps the computer awake until the guard is dropped.
/// Side effects: the first guard sets a system and a display power request
/// ("Kyokai: someone is connected", shown by `powercfg /requests`).
pub fn keep_awake() -> Awake {
    #[cfg(windows)]
    {
        let mut awake = AWAKE.lock().expect("power request");
        if awake.0 == 0 {
            awake.1 = windows_impl::request();
        }
        awake.0 += 1;
    }
    Awake(())
}

impl Drop for Awake {
    fn drop(&mut self) {
        #[cfg(windows)]
        {
            let mut awake = AWAKE.lock().expect("power request");
            awake.0 -= 1;
            if awake.0 == 0
                && let Some(h) = awake.1.take()
            {
                windows_impl::release(h);
            }
        }
    }
}

/// Whether a power request is held now (for tests).
#[cfg(all(test, windows))]
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

#[cfg(all(test, windows))]
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
