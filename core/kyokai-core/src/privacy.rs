//! Blocking the host's own input and privacy mode (doc/en-us/computer-use.md,
//! "Session actions"), on Windows.
//!
//! Both are shared by every session that asks for them: the first hold
//! starts them, the last release ends them. The host user lifts both at
//! once with Ctrl+Alt+Shift+Esc (the emergency stop), and Ctrl+Alt+Del
//! always reaches Windows; a lifted hold is not held by anyone any more,
//! even when a new one starts (each start is a new generation).
//!
//! - The block: low-level keyboard and mouse hooks swallow input that
//!   Kyokai did not inject.
//! - The mask (RustDesk's privacy mode): a black, click-through, topmost
//!   window over the whole virtual desktop, left out of capture, so the
//!   people at the host see black while the controller sees the screen.

use crate::input::INJECTED_TAG;
use std::sync::Mutex;
use std::sync::atomic::{AtomicBool, AtomicU8, AtomicU32, AtomicU64, Ordering};
use windows::Win32::Foundation::{HINSTANCE, HWND, LPARAM, LRESULT, WPARAM};
use windows::Win32::Graphics::Gdi::{BLACK_BRUSH, GetStockObject, HBRUSH};
use windows::Win32::System::LibraryLoader::GetModuleHandleW;
use windows::Win32::System::Threading::GetCurrentThreadId;
use windows::Win32::UI::Input::KeyboardAndMouse::{
    VIRTUAL_KEY, VK_CONTROL, VK_ESCAPE, VK_LCONTROL, VK_LMENU, VK_LSHIFT, VK_MENU, VK_RCONTROL,
    VK_RMENU, VK_RSHIFT, VK_SHIFT,
};
use windows::Win32::UI::WindowsAndMessaging::*;
use windows::core::w;

/// What a hold is on.
#[derive(Clone, Copy)]
pub enum Kind {
    /// The host's keyboard and mouse.
    Block,
    /// The host's screen, as the people there see it.
    Mask,
}

/// One kind's thread (hooks or window) and its holders.
struct Shared {
    thread: u32,
    holders: usize,
    generation: u64,
}

/// One kind's state.
struct Slot {
    shared: Mutex<Option<Shared>>,
    /// The generation of the newest start.
    generation: AtomicU64,
    /// Whether it is in force (read by the hooks; cleared by the lift).
    live: AtomicBool,
    /// The thread, for the lift, which cannot take the lock.
    thread: AtomicU32,
}

impl Slot {
    const fn new() -> Slot {
        Slot {
            shared: Mutex::new(None),
            generation: AtomicU64::new(0),
            live: AtomicBool::new(false),
            thread: AtomicU32::new(0),
        }
    }
}

static BLOCK: Slot = Slot::new();
static MASK: Slot = Slot::new();

fn slot(kind: Kind) -> &'static Slot {
    match kind {
        Kind::Block => &BLOCK,
        Kind::Mask => &MASK,
    }
}

/// Takes a hold, starting the kind's thread when it is the first.
/// Returns: the generation held, or None when it could not start (no
/// hooks; a Windows without capture exclusion, where a mask would hide
/// the screen from the controller too).
pub fn hold(kind: Kind) -> Option<u64> {
    let s = slot(kind);
    let mut shared = s.shared.lock().expect("privacy");
    if shared.is_some() && !s.live.load(Ordering::Relaxed) {
        // The host user lifted it; its thread is ending.
        *shared = None;
    }
    if let Some(sh) = shared.as_mut() {
        sh.holders += 1;
        return Some(sh.generation);
    }
    let (tx, rx) = std::sync::mpsc::channel();
    std::thread::spawn(move || {
        // The agent's hooks and window belong on the desktop that takes
        // input.
        crate::agent::follow_input_desktop();
        match kind {
            Kind::Block => run_hooks(tx),
            Kind::Mask => run_mask(tx),
        }
    });
    let thread = rx.recv().ok().flatten()?;
    let generation = s.generation.fetch_add(1, Ordering::Relaxed) + 1;
    HELD.store(0, Ordering::Relaxed);
    s.thread.store(thread, Ordering::Relaxed);
    s.live.store(true, Ordering::Relaxed);
    *shared = Some(Shared {
        thread,
        holders: 1,
        generation,
    });
    Some(generation)
}

/// Gives up a hold; the last one ends the kind's thread.
pub fn release(kind: Kind, generation: u64) {
    let s = slot(kind);
    let mut shared = s.shared.lock().expect("privacy");
    let Some(sh) = shared.as_mut() else {
        return;
    };
    if sh.generation != generation {
        return;
    }
    sh.holders -= 1;
    if sh.holders == 0 || !s.live.load(Ordering::Relaxed) {
        s.live.store(false, Ordering::Relaxed);
        // SAFETY: posts WM_QUIT to the kind's thread started in `hold`.
        let _ = unsafe { PostThreadMessageW(sh.thread, WM_QUIT, WPARAM(0), LPARAM(0)) };
        *shared = None;
    }
}

/// Whether the hold of `generation` is still in force.
pub fn live(kind: Kind, generation: u64) -> bool {
    let s = slot(kind);
    s.live.load(Ordering::Relaxed) && s.generation.load(Ordering::Relaxed) == generation
}

/// Lifts the block and the mask for everyone: the host user's emergency
/// stop. Called on the hook thread, so it takes no lock.
fn lift() {
    for s in [&BLOCK, &MASK] {
        if s.live.swap(false, Ordering::Relaxed) {
            // SAFETY: posts WM_QUIT to a thread started in `hold`; a thread
            // that already ended ignores it.
            let _ = unsafe {
                PostThreadMessageW(
                    s.thread.load(Ordering::Relaxed),
                    WM_QUIT,
                    WPARAM(0),
                    LPARAM(0),
                )
            };
        }
    }
    crate::input::emergency_stop();
}

/// The modifiers the host's keyboard holds, seen by the block's hook (which
/// swallows them, so the key state does not show them).
static HELD: AtomicU8 = AtomicU8::new(0);
const HELD_CTRL: u8 = 1;
const HELD_ALT: u8 = 2;
const HELD_SHIFT: u8 = 4;

/// Installs the block's hooks and pumps messages until WM_QUIT.
fn run_hooks(ready: std::sync::mpsc::Sender<Option<u32>>) {
    // SAFETY: the hooks are installed and removed on this thread, which
    // pumps messages for them until WM_QUIT.
    unsafe {
        let module = GetModuleHandleW(None).ok().map(|m| HINSTANCE(m.0));
        let mouse = SetWindowsHookExW(WH_MOUSE_LL, Some(mouse_hook), module, 0);
        let keys = SetWindowsHookExW(WH_KEYBOARD_LL, Some(key_hook), module, 0);
        let (mouse, keys) = match (mouse, keys) {
            (Ok(m), Ok(k)) => (m, k),
            (m, k) => {
                for h in [m, k].into_iter().flatten() {
                    let _ = UnhookWindowsHookEx(h);
                }
                let _ = ready.send(None);
                return;
            }
        };
        let _ = ready.send(Some(GetCurrentThreadId()));
        let mut msg = MSG::default();
        while GetMessageW(&mut msg, None, 0, 0).as_bool() {}
        let _ = UnhookWindowsHookEx(mouse);
        let _ = UnhookWindowsHookEx(keys);
    }
}

/// Swallows the host's own mouse input while blocked.
unsafe extern "system" fn mouse_hook(code: i32, w: WPARAM, l: LPARAM) -> LRESULT {
    if code >= 0 && BLOCK.live.load(Ordering::Relaxed) {
        // SAFETY: for HC_ACTION, `l` points to the event's MSLLHOOKSTRUCT.
        let info = unsafe { &*(l.0 as *const MSLLHOOKSTRUCT) };
        if info.flags & LLMHF_INJECTED == 0 || info.dwExtraInfo != INJECTED_TAG {
            return LRESULT(1);
        }
    }
    // SAFETY: passes the event on unchanged.
    unsafe { CallNextHookEx(None, code, w, l) }
}

/// Swallows the host's own keys while blocked, except that the emergency
/// stop lifts the block and the mask (and stops the AI, as everywhere).
unsafe extern "system" fn key_hook(code: i32, w: WPARAM, l: LPARAM) -> LRESULT {
    if code >= 0 && BLOCK.live.load(Ordering::Relaxed) {
        // SAFETY: for HC_ACTION, `l` points to the event's KBDLLHOOKSTRUCT.
        let info = unsafe { &*(l.0 as *const KBDLLHOOKSTRUCT) };
        if info.flags.0 & LLKHF_INJECTED.0 == 0 || info.dwExtraInfo != INJECTED_TAG {
            let down = matches!(w.0 as u32, WM_KEYDOWN | WM_SYSKEYDOWN);
            let bit = match VIRTUAL_KEY(info.vkCode as u16) {
                VK_LCONTROL | VK_RCONTROL | VK_CONTROL => HELD_CTRL,
                VK_LMENU | VK_RMENU | VK_MENU => HELD_ALT,
                VK_LSHIFT | VK_RSHIFT | VK_SHIFT => HELD_SHIFT,
                _ => 0,
            };
            if down {
                HELD.fetch_or(bit, Ordering::Relaxed);
            } else {
                HELD.fetch_and(!bit, Ordering::Relaxed);
            }
            let all = HELD_CTRL | HELD_ALT | HELD_SHIFT;
            if !(down
                && info.vkCode == VK_ESCAPE.0 as u32
                && HELD.load(Ordering::Relaxed) & all == all)
            {
                return LRESULT(1);
            }
            lift();
        }
    }
    // SAFETY: passes the event on unchanged.
    unsafe { CallNextHookEx(None, code, w, l) }
}

/// The mask's timer: it stays on top of windows that came later.
const ON_TOP_TIMER: usize = 1;

/// Shows the mask and pumps messages until WM_QUIT.
fn run_mask(ready: std::sync::mpsc::Sender<Option<u32>>) {
    // SAFETY: the class, window and timer are made and destroyed on this
    // thread, which pumps messages for them until WM_QUIT.
    unsafe {
        let module = GetModuleHandleW(None).ok().map(|m| HINSTANCE(m.0));
        let class = WNDCLASSW {
            lpfnWndProc: Some(mask_proc),
            hInstance: module.unwrap_or_default(),
            hbrBackground: HBRUSH(GetStockObject(BLACK_BRUSH).0),
            lpszClassName: w!("KyokaiPrivacyMask"),
            ..Default::default()
        };
        // A second registration fails harmlessly: the class exists.
        RegisterClassW(&class);
        let (x, y, cx, cy) = virtual_desktop();
        let Ok(hwnd) = CreateWindowExW(
            WS_EX_TOPMOST | WS_EX_TOOLWINDOW | WS_EX_NOACTIVATE | WS_EX_LAYERED | WS_EX_TRANSPARENT,
            w!("KyokaiPrivacyMask"),
            w!(""),
            WS_POPUP,
            x,
            y,
            cx,
            cy,
            None,
            None,
            module,
            None,
        ) else {
            let _ = ready.send(None);
            return;
        };
        // Opaque, but clicks and the pointer pass through to the windows
        // below; left out of capture, or not shown at all.
        let shown = SetLayeredWindowAttributes(hwnd, Default::default(), 255, LWA_ALPHA).is_ok()
            && SetWindowDisplayAffinity(hwnd, WDA_EXCLUDEFROMCAPTURE).is_ok();
        if !shown {
            let _ = DestroyWindow(hwnd);
            let _ = ready.send(None);
            return;
        }
        let _ = ShowWindow(hwnd, SW_SHOWNOACTIVATE);
        SetTimer(Some(hwnd), ON_TOP_TIMER, 500, None);
        let _ = ready.send(Some(GetCurrentThreadId()));
        let mut msg = MSG::default();
        while GetMessageW(&mut msg, None, 0, 0).as_bool() {
            DispatchMessageW(&msg);
        }
        let _ = DestroyWindow(hwnd);
    }
}

/// The virtual desktop: x, y, width, height in pixels.
fn virtual_desktop() -> (i32, i32, i32, i32) {
    crate::input::physical(|| {
        // SAFETY: plain metric queries.
        unsafe {
            (
                GetSystemMetrics(SM_XVIRTUALSCREEN),
                GetSystemMetrics(SM_YVIRTUALSCREEN),
                GetSystemMetrics(SM_CXVIRTUALSCREEN),
                GetSystemMetrics(SM_CYVIRTUALSCREEN),
            )
        }
    })
}

/// The mask's window procedure: back on top every half second, resized
/// with the displays.
unsafe extern "system" fn mask_proc(hwnd: HWND, msg: u32, w: WPARAM, l: LPARAM) -> LRESULT {
    match msg {
        WM_TIMER | WM_DISPLAYCHANGE => {
            let (x, y, cx, cy) = virtual_desktop();
            // SAFETY: repositions this thread's own window.
            let _ = unsafe { SetWindowPos(hwnd, Some(HWND_TOPMOST), x, y, cx, cy, SWP_NOACTIVATE) };
            LRESULT(0)
        }
        // SAFETY: the default handling of every other message.
        _ => unsafe { DefWindowProcW(hwnd, msg, w, l) },
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Shows the mask for a moment: it must not stop clicks from reaching
    /// the window below (Kyokai's input guard asks WindowFromPoint), and a
    /// second hold shares it. Ignored by default: it blacks out this
    /// machine's screen for half a second.
    #[test]
    #[ignore]
    fn the_mask_lets_clicks_through_and_is_shared() {
        let a = hold(Kind::Mask).expect("mask");
        let b = hold(Kind::Mask).expect("second hold");
        assert_eq!(a, b);
        std::thread::sleep(std::time::Duration::from_millis(300));
        let (x, y, cx, cy) = virtual_desktop();
        let at = windows::Win32::Foundation::POINT {
            x: x + cx / 2,
            y: y + cy / 2,
        };
        // SAFETY: plain queries.
        let pid = unsafe {
            let hwnd = WindowFromPoint(at);
            let mut pid = 0;
            GetWindowThreadProcessId(hwnd, Some(&mut pid));
            pid
        };
        assert_ne!(pid, std::process::id(), "the mask took the click");
        // The picture still shows the screen, not the black mask.
        if let Some(display) = rustdesk_dxgi::Displays::get_from_gdi().into_iter().next()
            && let Ok(mut cap) = rustdesk_dxgi::Capturer::new(display)
            && let Ok(rustdesk_dxgi::Frame::PixelBuffer(pb)) = cap.frame(0)
        {
            let lit = pb.data.chunks(4).filter(|p| p[..3] != [0, 0, 0]).count();
            assert!(lit > pb.data.len() / 4 / 10, "the capture is black");
        }
        release(Kind::Mask, a);
        assert!(live(Kind::Mask, b));
        release(Kind::Mask, b);
        assert!(!live(Kind::Mask, b));
    }
}
