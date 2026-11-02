//! The host's desktop as an AI task sees it: screenshots scaled for the
//! model, and actions turned into input events
//! (doc/en-us/computer-use.md, "AI tasks").
#![cfg_attr(
    not(any(windows, target_os = "macos", target_os = "linux")),
    allow(dead_code)
)]

use base64::Engine;

use super::Shot;
use super::action::{Action, Button, Key, Point};
use crate::input::{Event, LocalInput, Mods, PointerAction};

/// Largest screenshot the model gets; bigger screens are scaled down
/// (Claude recommends about 1280×720 to 1366×768).
pub const SHOT_MAX: (u32, u32) = (1366, 768);

/// A captured screen: top-down BGRA at full resolution.
pub struct Frame {
    pub bgra: Vec<u8>,
    pub width: u32,
    pub height: u32,
}

/// What a task needs from the desktop it runs on.
pub trait Desktop: Send {
    /// The screen's size in pixels, cheap enough to check before every action.
    fn size(&mut self) -> Result<(u32, u32), String>;
    /// Captures the screen.
    fn capture(&mut self) -> Result<Frame, String>;
    /// Injects one event. Returns: false when it was refused (a Kyokai
    /// window has the focus).
    fn inject(&mut self, e: &Event) -> bool;
    /// Releases every key and button the task holds.
    fn release(&mut self);
    /// The cursor as fractions of the screen.
    fn cursor(&mut self) -> Option<(f32, f32)>;
    /// When the host user last used the keyboard or mouse.
    fn local_input(&mut self) -> LocalInput;
    /// Whether the emergency-stop shortcut was pressed since the last call
    /// (the call clears it).
    fn stop_requested(&mut self) -> bool;
    /// What input at `at` (fractions of the screen; None: the focused
    /// window, for keys) would reach, as the system reports it
    /// (`policy::Target`). The default knows nothing, so the policy asks.
    fn target(&mut self, at: Option<(f32, f32)>) -> super::policy::Target {
        let _ = at;
        super::policy::Target::default()
    }
    /// The windows on the screen, topmost first: each one's program (as
    /// `target` names it) and its rectangle in the capture's pixels. The
    /// default knows none, so a scoped screenshot is dimmed all over.
    fn windows(&mut self) -> Vec<Shown> {
        Vec::new()
    }
}

/// A window on the screen as AI scope reads it (`Desktop::windows`).
#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize, serde::Deserialize)]
pub struct Shown {
    pub program: Option<String>,
    /// `[x0, y0, x1, y1]` in the capture's pixels (may reach past it).
    pub rect: [i32; 4],
}

/// Dims what the granted programs' windows do not show: covered parts of
/// a granted window, other windows and the desktop become a dark grey, so
/// the model sees only the granted apps (AI scope).
/// Inputs: `windows` topmost first; `apps` the granted programs.
pub fn dim_outside(frame: &mut Frame, windows: &[Shown], apps: &[String]) {
    let (w, h) = (frame.width as i32, frame.height as i32);
    let mut keep = vec![false; (w * h) as usize];
    // Bottom to top, each window paints its own answer over the ones below.
    for win in windows.iter().rev() {
        let granted = win.program.as_ref().is_some_and(|p| apps.contains(p));
        let [x0, y0, x1, y1] = win.rect;
        let (x0, x1) = (x0.clamp(0, w), x1.clamp(0, w));
        for y in y0.clamp(0, h)..y1.clamp(0, h) {
            keep[(y * w + x0) as usize..(y * w + x1) as usize].fill(granted);
        }
    }
    for (px, keep) in frame.bgra.as_chunks_mut::<4>().0.iter_mut().zip(keep) {
        if !keep {
            let grey = ((px[0] as u32 + px[1] as u32 + px[2] as u32) / 12) as u8;
            px[..3].fill(grey);
        }
    }
}

/// One step of an action.
#[derive(Debug, Clone, PartialEq)]
pub enum Step {
    Inject(Event),
    Sleep(u32),
    /// Release everything held (the end of every action).
    Release,
}

/// Scales `region` (`[x0, y0, x1, y1]` in frame pixels) of a frame into a
/// screenshot that fits `max`, its sides rounded down to `multiple`s (1:
/// as they come; a smart-resize model's 28 or 32, `Coordinates::multiple`).
/// Returns: the PNG as base64 and its size.
pub fn shot(frame: &Frame, region: [u32; 4], max: (u32, u32), multiple: u32) -> Shot {
    let [x0, y0, x1, y1] = region;
    let (rw, rh) = (x1 - x0, y1 - y0);
    let scale = (max.0 as f64 / rw as f64)
        .min(max.1 as f64 / rh as f64)
        .min(1.0);
    let w = ((rw as f64 * scale).round() as u32).max(1);
    let h = ((rh as f64 * scale).round() as u32).max(1);
    let (w, h) = (
        (w / multiple * multiple).max(multiple),
        (h / multiple * multiple).max(multiple),
    );
    let mut rgb = Vec::with_capacity((w * h * 3) as usize);
    let stride = frame.width as usize * 4;
    // Each output pixel averages the source pixels it covers.
    for oy in 0..h {
        let sy0 = y0 + oy * rh / h;
        let sy1 = (y0 + (oy + 1) * rh / h).max(sy0 + 1);
        for ox in 0..w {
            let sx0 = x0 + ox * rw / w;
            let sx1 = (x0 + (ox + 1) * rw / w).max(sx0 + 1);
            let mut sum = [0u32; 3];
            for sy in sy0..sy1 {
                let row = &frame.bgra[sy as usize * stride..];
                for sx in sx0..sx1 {
                    let p = &row[sx as usize * 4..sx as usize * 4 + 3];
                    sum[0] += p[2] as u32;
                    sum[1] += p[1] as u32;
                    sum[2] += p[0] as u32;
                }
            }
            let n = (sy1 - sy0) * (sx1 - sx0);
            rgb.extend(sum.map(|s| ((s + n / 2) / n) as u8));
        }
    }
    let mut png_bytes = Vec::new();
    let mut enc = png::Encoder::new(&mut png_bytes, w, h);
    enc.set_color(png::ColorType::Rgb);
    enc.set_depth(png::BitDepth::Eight);
    enc.set_compression(png::Compression::Fast);
    let mut writer = enc.write_header().expect("PNG header into memory");
    writer.write_image_data(&rgb).expect("PNG data into memory");
    drop(writer);
    Shot {
        b64: base64::engine::general_purpose::STANDARD.encode(&png_bytes),
        width: w,
        height: h,
    }
}

/// Maps a region of the model's screenshot onto the frame it came from.
/// Inputs: `region` — validated against `seen` (the screenshot's size).
pub fn frame_region(region: [i32; 4], seen: (u32, u32), frame: (u32, u32)) -> [u32; 4] {
    let map =
        |v: i32, from: u32, to: u32| ((v.max(0) as u64 * to as u64 / from as u64) as u32).min(to);
    let [x0, y0, x1, y1] = region;
    let (fx0, fy0) = (map(x0, seen.0, frame.0), map(y0, seen.1, frame.1));
    let (fx1, fy1) = (map(x1, seen.0, frame.0), map(y1, seen.1, frame.1));
    [fx0, fy0, fx1.max(fx0 + 1), fy1.max(fy0 + 1)]
}

/// Turns an input action into steps.
/// Inputs: `seen` — the size of the screenshot the coordinates refer to;
/// `cursor` — the pointer now, as fractions (for clicks without a point).
/// Returns: the steps, ending with a release; or why the action cannot run
/// here. Screenshots, zooms and the cursor position are not input; the
/// task answers them itself.
pub fn plan(
    action: &Action,
    seen: (u32, u32),
    cursor: Option<(f32, f32)>,
) -> Result<Vec<Step>, String> {
    let at = |p: &Point| {
        (
            (p.x as f32 + 0.5) / seen.0 as f32,
            (p.y as f32 + 0.5) / seen.1 as f32,
        )
    };
    let here = |p: Option<&Point>| -> Result<(f32, f32), String> {
        match p {
            Some(p) => Ok(at(p)),
            None => cursor.ok_or_else(|| "the cursor position is not known".to_string()),
        }
    };
    let pointer = |action, button, (x, y): (f32, f32)| {
        Step::Inject(Event::Pointer {
            action,
            button,
            x,
            y,
        })
    };
    let mut steps = Vec::new();
    match action {
        Action::Screenshot | Action::Zoom { .. } | Action::CursorPosition => {}
        Action::Move { to, held } => {
            press_modifiers(held, &mut steps)?;
            steps.push(pointer(PointerAction::Move, 0, at(to)));
        }
        Action::Click {
            at: p,
            button,
            count,
            modifiers,
        } => {
            let b = button_index(*button)?;
            let xy = here(p.as_ref())?;
            press_modifiers(modifiers, &mut steps)?;
            steps.push(pointer(PointerAction::Move, 0, xy));
            for i in 0..*count {
                if i > 0 {
                    steps.push(Step::Sleep(40));
                }
                steps.push(pointer(PointerAction::Down, b, xy));
                steps.push(pointer(PointerAction::Up, b, xy));
            }
        }
        Action::MouseDown { button } => steps.push(pointer(
            PointerAction::Down,
            button_index(*button)?,
            here(None)?,
        )),
        Action::MouseUp { button } => steps.push(pointer(
            PointerAction::Up,
            button_index(*button)?,
            here(None)?,
        )),
        Action::Drag { path, modifiers } => {
            press_modifiers(modifiers, &mut steps)?;
            let first = at(&path[0]);
            steps.push(pointer(PointerAction::Move, 0, first));
            steps.push(pointer(PointerAction::Down, 0, first));
            let mut from = first;
            for p in &path[1..] {
                let to = at(p);
                // Apps see a drag only when the pointer moves in steps.
                for i in 1..=DRAG_STEPS {
                    let t = i as f32 / DRAG_STEPS as f32;
                    steps.push(Step::Sleep(10));
                    steps.push(pointer(
                        PointerAction::Move,
                        0,
                        (from.0 + (to.0 - from.0) * t, from.1 + (to.1 - from.1) * t),
                    ));
                }
                from = to;
            }
            steps.push(pointer(PointerAction::Up, 0, from));
        }
        Action::Scroll {
            at: p,
            dx,
            dy,
            modifiers,
        } => {
            press_modifiers(modifiers, &mut steps)?;
            if let Some(p) = p {
                steps.push(pointer(PointerAction::Move, 0, at(p)));
            }
            steps.push(Step::Inject(Event::Scroll {
                dx: *dx as f32 / 120.0,
                dy: *dy as f32 / 120.0,
            }));
        }
        Action::Type { text } => {
            // One character at a time, paced: apps that fall behind a burst
            // (Windows 11 Notepad) type some characters as the next one or
            // drop them.
            for (i, c) in text.chars().filter(|c| *c != '\r').enumerate() {
                if i > 0 {
                    steps.push(Step::Sleep(TYPE_PACE));
                }
                if c == '\n' {
                    tap("enter", Mods::default(), &mut steps);
                } else {
                    steps.push(Step::Inject(Event::Text { text: c.into() }));
                }
            }
        }
        Action::KeyCombo { keys, repeat } => {
            let (mods, rest) = split_combo(keys)?;
            if rest.is_empty() {
                press_modifiers(keys, &mut steps)?;
            } else {
                for i in 0..*repeat {
                    if i > 0 {
                        steps.push(Step::Sleep(30));
                    }
                    for (name, extra) in &rest {
                        tap(name, merge(mods, *extra), &mut steps);
                    }
                }
            }
        }
        Action::HoldKey { keys, duration_ms } => {
            let (mods, rest) = split_combo(keys)?;
            let held: Vec<Key> = keys.iter().filter(|k| k.is_modifier()).cloned().collect();
            press_modifiers(&held, &mut steps)?;
            for (name, extra) in &rest {
                steps.push(key(name, true, merge(mods, *extra)));
            }
            steps.push(Step::Sleep(*duration_ms));
            for (name, _) in rest.iter().rev() {
                steps.push(key(name, false, Mods::default()));
            }
        }
        Action::Wait { duration_ms } => steps.push(Step::Sleep(*duration_ms)),
    }
    if !steps.is_empty() {
        steps.push(Step::Release);
    }
    Ok(steps)
}

/// Pointer moves per drag segment.
const DRAG_STEPS: u32 = 8;
/// Milliseconds between typed characters.
const TYPE_PACE: u32 = 30;

fn button_index(b: Button) -> Result<u8, String> {
    match b {
        Button::Left => Ok(0),
        Button::Right => Ok(1),
        Button::Middle => Ok(2),
        Button::Back | Button::Forward => {
            Err("the back and forward buttons are not supported".into())
        }
    }
}

fn key(name: &str, down: bool, mods: Mods) -> Step {
    Step::Inject(Event::Key {
        key: name.into(),
        down,
        mods,
    })
}

fn tap(name: &str, mods: Mods, steps: &mut Vec<Step>) {
    steps.push(key(name, true, mods));
    steps.push(key(name, false, mods));
}

fn merge(a: Mods, b: Mods) -> Mods {
    Mods {
        shift: a.shift || b.shift,
        ctrl: a.ctrl || b.ctrl,
        alt: a.alt || b.alt,
        meta: a.meta || b.meta,
    }
}

/// Presses modifier keys one after another, each with the ones before it
/// held, so the input service keeps them all down.
fn press_modifiers(keys: &[Key], steps: &mut Vec<Step>) -> Result<(), String> {
    let mut held = Mods::default();
    for k in keys {
        let name = match k {
            Key::Ctrl => "ctrl",
            Key::Shift => "shift",
            Key::Alt => "alt",
            Key::Meta => "meta",
            other => return Err(format!("{other} is not a modifier")),
        };
        steps.push(key(name, true, held));
        set_mod(&mut held, k);
    }
    Ok(())
}

fn set_mod(m: &mut Mods, k: &Key) {
    match k {
        Key::Ctrl => m.ctrl = true,
        Key::Shift => m.shift = true,
        Key::Alt => m.alt = true,
        Key::Meta => m.meta = true,
        _ => {}
    }
}

/// A key as the input service names it, with the modifiers its character
/// needs.
type Pressed = (&'static str, Mods);

/// Splits a combination into its modifiers and the other keys, as input
/// key names with any modifier their character needs (`+` is shift and
/// `=`).
fn split_combo(keys: &[Key]) -> Result<(Mods, Vec<Pressed>), String> {
    let mut mods = Mods::default();
    let mut rest = Vec::new();
    for k in keys {
        if k.is_modifier() {
            set_mod(&mut mods, k);
            continue;
        }
        rest.push(key_name(k)?);
    }
    Ok((mods, rest))
}

/// The input service's name for a key.
fn key_name(k: &Key) -> Result<Pressed, String> {
    let plain = |n: &'static str| Ok((n, Mods::default()));
    match k {
        Key::Named(n) => plain(n),
        Key::Char(c) => match c {
            'a'..='z' | '0'..='9' => {
                const CHARS: &str = "abcdefghijklmnopqrstuvwxyz0123456789";
                let i = CHARS.find(*c).expect("listed");
                plain(&CHARS[i..i + 1])
            }
            '-' => plain("minus"),
            '=' => plain("equal"),
            ',' => plain("comma"),
            '.' => plain("period"),
            '/' => plain("slash"),
            ';' => plain("semicolon"),
            '\'' => plain("quote"),
            '[' => plain("bracket-left"),
            ']' => plain("bracket-right"),
            '\\' => plain("backslash"),
            '`' => plain("backquote"),
            '+' => Ok((
                "equal",
                Mods {
                    shift: true,
                    ..Mods::default()
                },
            )),
            ' ' => plain("space"),
            other => Err(format!(
                "the key {other:?} cannot be pressed; type it instead"
            )),
        },
        modifier => Err(format!("{modifier} is a modifier")),
    }
}

#[cfg(target_os = "linux")]
pub use crate::linux::Screen;
#[cfg(target_os = "macos")]
pub use crate::mac::Screen;
#[cfg(windows)]
pub use windows_impl::Screen;

#[cfg(windows)]
mod windows_impl {
    //! The primary display of the interactive desktop.
    use super::*;
    use crate::input::physical;
    use crate::input::{Injector, LocalWatch};
    use rustdesk_dxgi::{CapturerGDI, Displays};

    /// The real desktop: GDI capture, `SendInput` injection, and a watch on
    /// local input.
    pub struct Screen {
        injector: Injector,
        watch: LocalWatch,
    }

    impl Screen {
        /// Opens the desktop and starts watching for local input.
        pub fn open() -> Result<Screen, String> {
            Ok(Screen {
                injector: Injector::default(),
                watch: LocalWatch::start()?,
            })
        }
    }

    /// The primary display (the one at the desktop's origin): its name and
    /// size in pixels.
    fn primary() -> Result<(Vec<u16>, u32, u32), String> {
        let d = Displays::new()
            .map_err(|e| format!("displays: {e}"))?
            .find(|d| d.origin() == (0, 0))
            .ok_or("no primary display")?;
        Ok((d.name().to_vec(), d.width() as u32, d.height() as u32))
    }

    impl Desktop for Screen {
        fn size(&mut self) -> Result<(u32, u32), String> {
            primary().map(|(_, w, h)| (w, h))
        }

        fn capture(&mut self) -> Result<Frame, String> {
            physical(|| {
                let (mut name, w, h) = primary()?;
                name.push(0);
                let capturer = CapturerGDI::new(&name, w as i32, h as i32)
                    .map_err(|e| format!("capture: {e}"))?;
                let mut bgra = Vec::new();
                capturer
                    .frame(&mut bgra)
                    .map_err(|e| format!("capture: {e}"))?;
                Ok(Frame {
                    bgra,
                    width: w,
                    height: h,
                })
            })
        }

        fn inject(&mut self, e: &Event) -> bool {
            // The AI never reaches Kyokai's windows, whatever the host user lets.
            self.injector.inject(e, false)
        }

        fn release(&mut self) {
            self.injector.release_all();
        }

        fn cursor(&mut self) -> Option<(f32, f32)> {
            let (x, y) = crate::input::pointer()?;
            Some((x.clamp(0.0, 1.0), y.clamp(0.0, 1.0)))
        }

        fn local_input(&mut self) -> LocalInput {
            self.watch.input()
        }

        fn stop_requested(&mut self) -> bool {
            self.watch.stop_requested()
        }

        fn target(&mut self, at: Option<(f32, f32)>) -> crate::ai::policy::Target {
            facts::target(at)
        }

        fn windows(&mut self) -> Vec<Shown> {
            facts::windows()
        }
    }

    mod facts {
        //! What input would reach on this desktop (`Desktop::target`): the
        //! input desktop and the session's lock state, the window's
        //! process, and the UI Automation element there.
        use crate::ai::policy::{Role, Surface, Target};
        use crate::input::{physical, process_image};
        use windows::Win32::Foundation::{HWND, POINT};
        use windows::Win32::System::Com::{
            CLSCTX_INPROC_SERVER, COINIT_MULTITHREADED, CoCreateInstance, CoInitializeEx,
        };
        use windows::Win32::System::RemoteDesktop::ProcessIdToSessionId;
        use windows::Win32::System::RemoteDesktop::{
            WTS_CURRENT_SERVER_HANDLE, WTS_CURRENT_SESSION, WTS_PROCESS_INFOW,
            WTSEnumerateProcessesW, WTSFreeMemory, WTSINFOEXW, WTSQuerySessionInformationW,
            WTSSessionInfoEx,
        };
        use windows::Win32::System::Threading::GetCurrentProcessId;
        use windows::Win32::UI::Accessibility::{
            CUIAutomation, IUIAutomation, IUIAutomationElement, UIA_ButtonControlTypeId,
            UIA_DocumentControlTypeId, UIA_EditControlTypeId,
        };
        use windows::Win32::UI::WindowsAndMessaging::{
            FindWindowExW, GA_ROOT, GetAncestor, GetClassNameW, GetForegroundWindow,
            GetSystemMetrics, GetWindowThreadProcessId, SM_CXSCREEN, SM_CYSCREEN, WindowFromPoint,
        };
        use windows::core::w;

        /// WTSINFOEX_LEVEL1's `SessionFlags` for a locked session.
        const WTS_SESSIONSTATE_LOCK: i32 = 0;

        /// Reads the facts for input at `at` (fractions of the primary
        /// display), or at the focus.
        pub fn target(at: Option<(f32, f32)>) -> Target {
            let surface = surface();
            if surface != Surface::Desktop {
                return Target {
                    surface,
                    ..Default::default()
                };
            }
            physical(|| {
                // SAFETY: plain queries of the screen's metrics and windows.
                let (hwnd, point) = unsafe {
                    match at {
                        Some((x, y)) => {
                            let p = POINT {
                                x: (x * GetSystemMetrics(SM_CXSCREEN) as f32) as i32,
                                y: (y * GetSystemMetrics(SM_CYSCREEN) as f32) as i32,
                            };
                            (WindowFromPoint(p), Some(p))
                        }
                        None => (GetForegroundWindow(), None),
                    }
                };
                // SAFETY: a plain query; a null window gives a null root.
                let root = unsafe { GetAncestor(hwnd, GA_ROOT) };
                let mut target = Target {
                    program: program(root),
                    file_dialog: file_dialog(root),
                    ..Default::default()
                };
                if let Some(e) = element(point) {
                    // SAFETY: COM calls on an element this thread holds.
                    unsafe {
                        target.role = Some(match e.CurrentIsPassword().map(|b| b.as_bool()) {
                            Ok(true) => Role::Password,
                            _ => match e.CurrentControlType() {
                                Ok(t)
                                    if t == UIA_EditControlTypeId
                                        || t == UIA_DocumentControlTypeId =>
                                {
                                    Role::Text
                                }
                                Ok(t) if t == UIA_ButtonControlTypeId => Role::Button,
                                _ => Role::Other,
                            },
                        });
                        target.name = e
                            .CurrentName()
                            .ok()
                            .map(|n| n.to_string().to_lowercase())
                            .filter(|n| !n.is_empty());
                    }
                }
                target
            })
        }

        /// What the input desktop shows: the user's desktop, the lock or
        /// sign-in screen, a UAC prompt, or a secure desktop this process
        /// cannot open.
        fn surface() -> Surface {
            if locked() {
                return Surface::Locked;
            }
            match crate::agent::input_desktop_name() {
                Some(n) if n.eq_ignore_ascii_case("default") => Surface::Desktop,
                None => Surface::Unknown,
                Some(_) if running("logonui.exe") => Surface::Locked,
                Some(_) if running("consent.exe") => Surface::Consent,
                Some(_) => Surface::Unknown,
            }
        }

        /// Whether this session is locked (its lock screen shows).
        fn locked() -> bool {
            let mut buf = windows::core::PWSTR::null();
            let mut len = 0u32;
            // SAFETY: the system allocates `buf`, freed below; it is read
            // only when the call succeeded and filled a whole WTSINFOEXW.
            unsafe {
                if WTSQuerySessionInformationW(
                    Some(WTS_CURRENT_SERVER_HANDLE),
                    WTS_CURRENT_SESSION,
                    WTSSessionInfoEx,
                    &mut buf,
                    &mut len,
                )
                .is_err()
                {
                    return false;
                }
                let info = buf.0 as *const WTSINFOEXW;
                let locked = len as usize >= std::mem::size_of::<WTSINFOEXW>()
                    && (*info).Level == 1
                    && (*info).Data.WTSInfoExLevel1.SessionFlags == WTS_SESSIONSTATE_LOCK;
                WTSFreeMemory(buf.0.cast());
                locked
            }
        }

        /// Whether a program by this executable name runs in this session.
        fn running(exe: &str) -> bool {
            let mut session = 0u32;
            let mut list: *mut WTS_PROCESS_INFOW = std::ptr::null_mut();
            let mut count = 0u32;
            // SAFETY: the system allocates `list` with `count` entries, read
            // only within that count and freed below.
            unsafe {
                if ProcessIdToSessionId(GetCurrentProcessId(), &mut session).is_err()
                    || WTSEnumerateProcessesW(
                        Some(WTS_CURRENT_SERVER_HANDLE),
                        0,
                        1,
                        &mut list,
                        &mut count,
                    )
                    .is_err()
                {
                    return false;
                }
                let found = std::slice::from_raw_parts(list, count as usize)
                    .iter()
                    .any(|p| {
                        p.SessionId == session
                            && p.pProcessName
                                .to_string()
                                .is_ok_and(|n| n.eq_ignore_ascii_case(exe))
                    });
                WTSFreeMemory(list.cast());
                found
            }
        }

        /// The executable name (lowercase) of the process owning a window.
        fn program(root: HWND) -> Option<String> {
            let mut pid = 0u32;
            // SAFETY: a plain query; a null window leaves `pid` zero.
            unsafe { GetWindowThreadProcessId(root, Some(&mut pid)) };
            if pid == 0 {
                return None;
            }
            let path = process_image(pid)?;
            std::path::Path::new(&path)
                .file_name()
                .map(|n| n.to_string_lossy().to_lowercase())
        }

        /// The visible top-level windows, topmost first (`EnumWindows`'
        /// order), each with its program and its frame as the screen shows
        /// it (DWM's bounds, without the invisible resize borders); minimized
        /// and cloaked windows (other virtual desktops, suspended apps) left
        /// out.
        pub fn windows() -> Vec<super::Shown> {
            use windows::Win32::Foundation::{LPARAM, RECT};
            use windows::Win32::Graphics::Dwm::{
                DWMWA_CLOAKED, DWMWA_EXTENDED_FRAME_BOUNDS, DwmGetWindowAttribute,
            };
            use windows::Win32::UI::WindowsAndMessaging::{EnumWindows, IsIconic, IsWindowVisible};
            use windows::core::BOOL;

            /// Adds one window to the list `l` points at.
            unsafe extern "system" fn each(hwnd: HWND, l: LPARAM) -> BOOL {
                // SAFETY: `l` is the list `windows` passed, alive for the
                // whole EnumWindows call; the queries only read the window.
                unsafe {
                    let out = &mut *(l.0 as *mut Vec<super::Shown>);
                    let mut cloaked = 0u32;
                    let _ = DwmGetWindowAttribute(
                        hwnd,
                        DWMWA_CLOAKED,
                        (&mut cloaked as *mut u32).cast(),
                        4,
                    );
                    let mut r = RECT::default();
                    if IsWindowVisible(hwnd).as_bool()
                        && !IsIconic(hwnd).as_bool()
                        && cloaked == 0
                        && DwmGetWindowAttribute(
                            hwnd,
                            DWMWA_EXTENDED_FRAME_BOUNDS,
                            (&mut r as *mut RECT).cast(),
                            std::mem::size_of::<RECT>() as u32,
                        )
                        .is_ok()
                        && r.right > r.left
                        && r.bottom > r.top
                    {
                        out.push(super::Shown {
                            program: program(hwnd),
                            rect: [r.left, r.top, r.right, r.bottom],
                        });
                    }
                    true.into()
                }
            }

            let mut out: Vec<super::Shown> = Vec::new();
            physical(|| {
                // SAFETY: the callback gets the list's address, which
                // outlives the call.
                let _ = unsafe { EnumWindows(Some(each), LPARAM(&mut out as *mut _ as isize)) };
            });
            out
        }

        /// Whether a top-level window is the common file dialog (a dialog
        /// holding the shell's item view).
        fn file_dialog(root: HWND) -> bool {
            let mut class = [0u16; 32];
            // SAFETY: fills `class` up to its length.
            let n = unsafe { GetClassNameW(root, &mut class) } as usize;
            if String::from_utf16_lossy(&class[..n]) != "#32770" {
                return false;
            }
            // SAFETY: a plain search among the window's children.
            unsafe { FindWindowExW(Some(root), None, w!("DUIViewWndClassName"), None) }.is_ok()
        }

        /// The UI Automation element at a point, or the focused one.
        fn element(at: Option<POINT>) -> Option<IUIAutomationElement> {
            // SAFETY: COM on this thread (multithreaded, as the crate uses
            // it elsewhere); the objects are released when dropped.
            unsafe {
                let _ = CoInitializeEx(None, COINIT_MULTITHREADED);
                let uia: IUIAutomation =
                    CoCreateInstance(&CUIAutomation, None, CLSCTX_INPROC_SERVER).ok()?;
                match at {
                    Some(p) => uia.ElementFromPoint(p).ok(),
                    None => uia.GetFocusedElement().ok(),
                }
            }
        }
    }
}
