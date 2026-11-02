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
    /// The foreground window's visible controls on the captured display,
    /// from the system's accessibility tree (`ai::elements`): none for
    /// Kyokai's own windows, a password field's value left out. The default
    /// (and a system without the tree) lists none.
    fn elements(&mut self) -> Option<super::elements::Listed> {
        None
    }
    /// The window the AI works in beside the host user (computer-use.md,
    /// "Beside the host user"): the topmost visible window of a granted
    /// program. None where the system cannot do it.
    fn beside(&mut self, apps: &[String]) -> Option<Beside> {
        let _ = apps;
        None
    }
    /// That window as it looks, also where others cover it.
    fn capture_beside(&mut self, b: &Beside) -> Result<Frame, String> {
        let _ = b;
        Err(BESIDE_NOT_HERE.into())
    }
    /// That window's elements, their bounds in its own pixels.
    fn elements_beside(&mut self, b: &Beside) -> Option<super::elements::Listed> {
        let _ = b;
        None
    }
    /// Acts on the window's element `index` without the pointer or the
    /// keyboard, if it is still `expect`.
    /// Returns: why it cannot, for the task to fall back.
    fn act_beside(
        &mut self,
        b: &Beside,
        index: usize,
        expect: &super::elements::Element,
        act: &BesideAct,
    ) -> Result<(), String> {
        let _ = (b, index, expect, act);
        Err(BESIDE_NOT_HERE.into())
    }
    /// The foreground window, as `Beside::id` names windows.
    fn front(&mut self) -> Option<u64> {
        None
    }
    /// Brings the window to the front for a step with the pointer and
    /// keyboard. Returns: whether it is in front now.
    fn raise(&mut self, b: &Beside) -> bool {
        let _ = b;
        false
    }
    /// Whether this is another computer seen through a pane (`ai::pane`),
    /// where this computer's AI scope (its apps and folders) does not
    /// apply: the pane itself is the grant.
    fn remote(&self) -> bool {
        false
    }
}

/// Why a step cannot run beside the host user here.
pub const BESIDE_NOT_HERE: &str = "this system cannot do it in the background";
/// Why a step beside the host user did not run: the element is no longer
/// what the model saw (not a reason to ask for the screen).
pub const BESIDE_CHANGED: &str = "the window changed; take a new screenshot";

/// The window the AI works in beside the host user (`Desktop::beside`).
#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize, serde::Deserialize)]
pub struct Beside {
    /// The system's handle for it.
    pub id: u64,
    /// Its program, as `policy::Target::program` names it.
    pub program: String,
    /// Where it is on the screen: `[x0, y0, x1, y1]`.
    pub rect: [i32; 4],
    /// Whether it is a file chooser, and the folder it (or a file
    /// manager) shows, as `policy::Target` has them.
    #[serde(default)]
    pub file_dialog: bool,
    #[serde(default)]
    pub folder: Option<String>,
}

/// What the AI does to an element beside the host user.
#[derive(Debug, Clone, PartialEq)]
pub enum BesideAct {
    /// Its default action: invoke, toggle, select, expand or collapse.
    Click,
    /// Text added to its value.
    Type(String),
    /// Scrolled by notches, positive down.
    Scroll(i32),
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
        elements: None,
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

        fn elements(&mut self) -> Option<crate::ai::elements::Listed> {
            facts::elements()
        }

        fn beside(&mut self, apps: &[String]) -> Option<Beside> {
            facts::beside(apps)
        }

        fn capture_beside(&mut self, b: &Beside) -> Result<Frame, String> {
            facts::capture_beside(b)
        }

        fn elements_beside(&mut self, b: &Beside) -> Option<crate::ai::elements::Listed> {
            facts::elements_beside(b)
        }

        fn act_beside(
            &mut self,
            b: &Beside,
            index: usize,
            expect: &crate::ai::elements::Element,
            act: &BesideAct,
        ) -> Result<(), String> {
            facts::act_beside(b, index, expect, act)
        }

        fn front(&mut self) -> Option<u64> {
            facts::front()
        }

        fn raise(&mut self, b: &Beside) -> bool {
            facts::raise(b)
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
                let program = program(root);
                let file_dialog = file_dialog(root);
                let folder = if file_dialog {
                    address_bar(root)
                } else if program.as_deref() == Some("explorer.exe") {
                    explorer_folder(root).or_else(|| address_bar(root))
                } else {
                    None
                };
                let mut target = Target {
                    program,
                    file_dialog,
                    folder,
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

        /// The executable name (lowercase) of the process owning a window;
        /// for a Store app's frame (ApplicationFrameHost), the app's own,
        /// from the core window inside it (Settings is systemsettings.exe).
        fn program(root: HWND) -> Option<String> {
            let name = |hwnd: HWND| {
                let mut pid = 0u32;
                // SAFETY: a plain query; a null window leaves `pid` zero.
                unsafe { GetWindowThreadProcessId(hwnd, Some(&mut pid)) };
                let path = (pid != 0).then(|| process_image(pid)).flatten()?;
                std::path::Path::new(&path)
                    .file_name()
                    .map(|n| n.to_string_lossy().to_lowercase())
            };
            let own = name(root)?;
            if own != FRAME_HOST {
                return Some(own);
            }
            // SAFETY: a plain search among the window's children.
            let core =
                unsafe { FindWindowExW(Some(root), None, w!("Windows.UI.Core.CoreWindow"), None) };
            Some(core.ok().and_then(name).unwrap_or(own))
        }

        /// The process that draws Store apps' window frames.
        const FRAME_HOST: &str = "applicationframehost.exe";

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

        /// The folder a File Explorer window shows, from the shell's list of
        /// its windows: the entries whose frame is `root`, one per tab; with
        /// several tabs, those whose name starts the window's title (the
        /// tab in front names it), when they agree on one folder.
        /// Returns: its path, or None for a folder not on disk (This PC,
        /// Libraries), a window the list does not have, or tabs it cannot
        /// tell apart.
        /// Notes: only calls that cross processes (the view's own window is
        /// an input-synchronous call, refused from here).
        fn explorer_folder(root: HWND) -> Option<String> {
            use windows::Win32::System::Com::{
                CLSCTX_LOCAL_SERVER, CoTaskMemFree, IServiceProvider,
            };
            use windows::Win32::System::Variant::{VARIANT, VT_I4};
            use windows::Win32::UI::Shell::{
                IFolderView, IPersistFolder2, IShellBrowser, IShellWindows, IWebBrowser2,
                SHGetNameFromIDList, SID_STopLevelBrowser, SIGDN_FILESYSPATH, ShellWindows,
            };
            use windows::Win32::UI::WindowsAndMessaging::GetWindowTextW;
            use windows::core::Interface;
            let mut title = [0u16; 512];
            // SAFETY: fills `title` up to its length.
            let n = unsafe { GetWindowTextW(root, &mut title) } as usize;
            let title = String::from_utf16_lossy(&title[..n]);
            // (name, path) of each of the frame's tabs.
            let mut tabs: Vec<(String, Option<String>)> = Vec::new();
            // SAFETY: COM on this thread (as `element`); every object is
            // released when dropped, the ID lists freed after their names
            // are read.
            unsafe {
                let _ = CoInitializeEx(None, COINIT_MULTITHREADED);
                let list: IShellWindows =
                    CoCreateInstance(&ShellWindows, None, CLSCTX_LOCAL_SERVER).ok()?;
                for i in 0..list.Count().ok()? {
                    let mut index = VARIANT::default();
                    (*index.Anonymous.Anonymous).vt = VT_I4;
                    (*index.Anonymous.Anonymous).Anonymous.lVal = i;
                    let Ok(item) = list.Item(&index) else {
                        continue;
                    };
                    let Ok(web) = item.cast::<IWebBrowser2>() else {
                        continue;
                    };
                    if web.HWND().ok().map(|h| h.0) != Some(root.0 as isize) {
                        continue;
                    }
                    let name = web
                        .LocationName()
                        .map(|n| n.to_string())
                        .unwrap_or_default();
                    let path = (|| {
                        let browser = item
                            .cast::<IServiceProvider>()
                            .ok()?
                            .QueryService::<IShellBrowser>(&SID_STopLevelBrowser)
                            .ok()?;
                        let view = browser.QueryActiveShellView().ok()?;
                        let folder: IPersistFolder2 =
                            view.cast::<IFolderView>().ok()?.GetFolder().ok()?;
                        let pidl = folder.GetCurFolder().ok()?;
                        let name = SHGetNameFromIDList(pidl, SIGDN_FILESYSPATH);
                        CoTaskMemFree(Some(pidl as *const _));
                        let name = name.ok()?;
                        let path = name.to_string().ok();
                        CoTaskMemFree(Some(name.0 as *const _));
                        path
                    })();
                    tabs.push((name, path));
                }
            }
            if tabs.len() > 1 {
                tabs.retain(|(name, _)| !name.is_empty() && title.starts_with(name.as_str()));
            }
            let (_, first) = tabs.first()?;
            tabs.iter()
                .all(|(_, p)| p == first)
                .then(|| first.clone())
                .flatten()
        }

        /// The folder a file chooser (or an Explorer window) shows, read
        /// from its address bar: the breadcrumb toolbar's text ("Address:
        /// C:\…", in the system's language) from its drive or share on.
        /// Returns: None when the bar names no folder on disk ("Desktop").
        fn address_bar(root: HWND) -> Option<String> {
            use windows::Win32::Foundation::LPARAM;
            use windows::Win32::UI::WindowsAndMessaging::{
                EnumChildWindows, GetParent, GetWindowTextW,
            };
            use windows::core::BOOL;

            /// Takes the text of the breadcrumb toolbar, if `hwnd` is it.
            unsafe extern "system" fn each(hwnd: HWND, l: LPARAM) -> BOOL {
                // SAFETY: `l` is the string `address_bar` passed, alive for
                // the whole EnumChildWindows call; the queries only read.
                unsafe {
                    let class = |h: HWND| {
                        let mut buf = [0u16; 64];
                        let n = GetClassNameW(h, &mut buf) as usize;
                        String::from_utf16_lossy(&buf[..n])
                    };
                    if class(hwnd) != "ToolbarWindow32"
                        || GetParent(hwnd).map(class).as_deref() != Ok("Breadcrumb Parent")
                    {
                        return true.into();
                    }
                    let mut buf = [0u16; 1024];
                    let n = GetWindowTextW(hwnd, &mut buf) as usize;
                    *(l.0 as *mut String) = String::from_utf16_lossy(&buf[..n]);
                    false.into()
                }
            }

            let mut text = String::new();
            // SAFETY: the callback gets the string's address, which outlives
            // the call.
            let _ = unsafe {
                EnumChildWindows(Some(root), Some(each), LPARAM(&mut text as *mut _ as isize))
            };
            path_in(&text)
        }

        /// The foreground window's controls (`Desktop::elements`): one
        /// cached UI Automation query of its control view, kept when on the
        /// primary display, not off screen, and either a control people act
        /// on or a named text or image; at most `MAX_ELEMENTS`, in the
        /// tree's order.
        /// Returns: None for no window, Kyokai's own, or no tree.
        pub fn elements() -> Option<crate::ai::elements::Listed> {
            // SAFETY: plain queries; a null window gives a null root.
            let root = physical(|| unsafe { GetAncestor(GetForegroundWindow(), GA_ROOT) });
            elements_of(root)
        }

        /// The controls of one top-level window, as `elements` lists them.
        fn elements_of(root: HWND) -> Option<crate::ai::elements::Listed> {
            // SAFETY: plain queries of the screen's metrics.
            let screen = unsafe {
                [
                    0,
                    0,
                    GetSystemMetrics(SM_CXSCREEN),
                    GetSystemMetrics(SM_CYSCREEN),
                ]
            };
            physical(|| {
                walk(root, screen, None).map(|(title, found)| crate::ai::elements::Listed {
                    window: title,
                    program: program(root),
                    elements: found.into_iter().map(|(_, e)| e).collect(),
                })
            })
        }

        /// What `walk` does to the element at an index, as it reaches it.
        type Visit<'a> = (
            usize,
            &'a mut dyn FnMut(&IUIAutomationElement) -> Result<(), String>,
        );

        /// Walks a window's control view once (a cached UI Automation query)
        /// and keeps what the element list keeps: the controls people act on
        /// and named text and images, not off screen, inside `area` (screen
        /// pixels, whose top left the bounds are taken from), at most
        /// `MAX_ELEMENTS`, in the tree's order; a password field's value is
        /// never read. With `visit`, it calls it on the element of that
        /// index, while it is alive.
        /// Returns: the window's title and the elements kept, or None for no
        /// window, Kyokai's own, or no tree; the visit's error as an element
        /// list of none.
        /// Notes: call under `physical` (bounds in physical pixels).
        fn walk(
            root: HWND,
            area: [i32; 4],
            mut visit: Option<Visit>,
        ) -> Option<(
            String,
            Vec<(IUIAutomationElement, crate::ai::elements::Element)>,
        )> {
            use crate::ai::elements::{Element, MAX_ELEMENTS};
            use crate::input::kyokai_process;
            use windows::Win32::System::Variant::{VT_BSTR, VariantClear};
            use windows::Win32::UI::Accessibility::{
                TreeScope_Descendants, UIA_BoundingRectanglePropertyId, UIA_ControlTypePropertyId,
                UIA_IsOffscreenPropertyId, UIA_IsPasswordPropertyId, UIA_NamePropertyId,
                UIA_ValueValuePropertyId,
            };
            use windows::Win32::UI::WindowsAndMessaging::GetWindowTextW;
            // SAFETY: plain queries of the window and its process; COM on
            // this thread (as `element`), every object released when dropped,
            // each VARIANT cleared after reading.
            unsafe {
                if root.is_invalid() {
                    return None;
                }
                let mut pid = 0u32;
                GetWindowThreadProcessId(root, Some(&mut pid));
                if pid == 0 || kyokai_process(pid) {
                    return None;
                }
                let mut title = [0u16; 512];
                let n = GetWindowTextW(root, &mut title) as usize;
                let _ = CoInitializeEx(None, COINIT_MULTITHREADED);
                let uia: IUIAutomation =
                    CoCreateInstance(&CUIAutomation, None, CLSCTX_INPROC_SERVER).ok()?;
                let window = uia.ElementFromHandle(root).ok()?;
                let cache = uia.CreateCacheRequest().ok()?;
                for p in [
                    UIA_ControlTypePropertyId,
                    UIA_NamePropertyId,
                    UIA_BoundingRectanglePropertyId,
                    UIA_IsOffscreenPropertyId,
                    UIA_IsPasswordPropertyId,
                    UIA_ValueValuePropertyId,
                ] {
                    cache.AddProperty(p).ok()?;
                }
                let found = window
                    .FindAllBuildCache(
                        TreeScope_Descendants,
                        &uia.ControlViewCondition().ok()?,
                        &cache,
                    )
                    .ok()?;
                let mut elements = Vec::new();
                for i in 0..found.Length().unwrap_or(0) {
                    if elements.len() == MAX_ELEMENTS {
                        break;
                    }
                    let Ok(e) = found.GetElement(i) else { continue };
                    let Some(role) = e.CachedControlType().ok().and_then(|t| role(t.0)) else {
                        continue;
                    };
                    if e.CachedIsOffscreen().is_ok_and(|o| o.as_bool()) {
                        continue;
                    }
                    let Ok(r) = e.CachedBoundingRectangle() else {
                        continue;
                    };
                    let rect = [
                        r.left.max(area[0]) - area[0],
                        r.top.max(area[1]) - area[1],
                        r.right.min(area[2]) - area[0],
                        r.bottom.min(area[3]) - area[1],
                    ];
                    if rect[2] <= rect[0] || rect[3] <= rect[1] {
                        continue;
                    }
                    let name = e.CachedName().map(|n| n.to_string()).unwrap_or_default();
                    let password = e.CachedIsPassword().is_ok_and(|p| p.as_bool());
                    let mut text = String::new();
                    if !password
                        && let Ok(mut v) = e.GetCachedPropertyValue(UIA_ValueValuePropertyId)
                    {
                        if v.Anonymous.Anonymous.vt == VT_BSTR {
                            text = v.Anonymous.Anonymous.Anonymous.bstrVal.to_string();
                        }
                        let _ = VariantClear(&mut v);
                    }
                    // Text and images only when they say something.
                    if matches!(role, "text" | "image") && name.trim().is_empty() {
                        continue;
                    }
                    if let Some((at, f)) = visit.as_mut()
                        && *at == elements.len()
                    {
                        // The visit is all a visiting walk is for.
                        let _ = f(&e);
                        return None;
                    }
                    elements.push((
                        e,
                        Element {
                            role: role.into(),
                            name,
                            text,
                            rect,
                        },
                    ));
                }
                Some((String::from_utf16_lossy(&title[..n]), elements))
            }
        }

        /// The window the AI works in beside the host user: the topmost
        /// visible window of a granted program (`Desktop::beside`).
        pub fn beside(apps: &[String]) -> Option<super::Beside> {
            use windows::Win32::Foundation::{LPARAM, RECT};
            use windows::Win32::Graphics::Dwm::{
                DWMWA_CLOAKED, DWMWA_EXTENDED_FRAME_BOUNDS, DwmGetWindowAttribute,
            };
            use windows::Win32::UI::WindowsAndMessaging::{EnumWindows, IsIconic, IsWindowVisible};
            use windows::core::BOOL;

            /// Adds one shown window to the list `l` points at.
            unsafe extern "system" fn each(hwnd: HWND, l: LPARAM) -> BOOL {
                // SAFETY: `l` is the list `beside` passed, alive for the
                // whole EnumWindows call; the queries only read the window.
                unsafe {
                    let out = &mut *(l.0 as *mut Vec<(HWND, [i32; 4])>);
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
                        && r.right - r.left > 8
                        && r.bottom - r.top > 8
                    {
                        out.push((hwnd, [r.left, r.top, r.right, r.bottom]));
                    }
                    true.into()
                }
            }

            let mut shown: Vec<(HWND, [i32; 4])> = Vec::new();
            physical(|| {
                // SAFETY: the callback gets the list's address, which
                // outlives the call.
                let _ = unsafe { EnumWindows(Some(each), LPARAM(&mut shown as *mut _ as isize)) };
            });
            // The shell's own windows (the taskbars, the desktop) are explorer.exe's
            // too, but no window to work in.
            const SHELL: [&str; 4] = [
                "Shell_TrayWnd",
                "Shell_SecondaryTrayWnd",
                "Progman",
                "WorkerW",
            ];
            shown.into_iter().find_map(|(hwnd, rect)| {
                let mut class = [0u16; 64];
                // SAFETY: fills `class` up to its length.
                let n = unsafe { GetClassNameW(hwnd, &mut class) } as usize;
                if SHELL.contains(&String::from_utf16_lossy(&class[..n]).as_str()) {
                    return None;
                }
                let p = program(hwnd)?;
                if !apps.contains(&p) || crate::input::is_kyokai(&p) {
                    return None;
                }
                let dialog = file_dialog(hwnd);
                let folder = if dialog {
                    address_bar(hwnd)
                } else if p == "explorer.exe" {
                    explorer_folder(hwnd).or_else(|| address_bar(hwnd))
                } else {
                    None
                };
                Some(super::Beside {
                    id: hwnd.0 as u64,
                    program: p,
                    rect,
                    file_dialog: dialog,
                    folder,
                })
            })
        }

        /// The window as it draws itself (PrintWindow, its full content),
        /// cut to the frame the screen shows, also where others cover it.
        pub fn capture_beside(b: &super::Beside) -> Result<super::Frame, String> {
            use windows::Win32::Foundation::RECT;
            use windows::Win32::Graphics::Gdi::{
                BI_RGB, BITMAPINFO, BITMAPINFOHEADER, CreateCompatibleBitmap, CreateCompatibleDC,
                DIB_RGB_COLORS, DeleteDC, DeleteObject, GetDC, GetDIBits, ReleaseDC, SelectObject,
            };
            use windows::Win32::Storage::Xps::{PRINT_WINDOW_FLAGS, PrintWindow};
            use windows::Win32::UI::WindowsAndMessaging::{GetWindowRect, PW_RENDERFULLCONTENT};
            let hwnd = HWND(b.id as usize as *mut _);
            physical(|| {
                // SAFETY: GDI objects made here are selected out and deleted
                // below; GetDIBits fills `px`, sized for the whole bitmap.
                unsafe {
                    let mut wr = RECT::default();
                    GetWindowRect(hwnd, &mut wr).map_err(|e| format!("the window: {e}"))?;
                    let (w, h) = (wr.right - wr.left, wr.bottom - wr.top);
                    if w <= 0 || h <= 0 {
                        return Err("the window has no size".into());
                    }
                    let screen = GetDC(None);
                    let dc = CreateCompatibleDC(Some(screen));
                    let bmp = CreateCompatibleBitmap(screen, w, h);
                    let old = SelectObject(dc, bmp.into());
                    let drawn =
                        PrintWindow(hwnd, dc, PRINT_WINDOW_FLAGS(PW_RENDERFULLCONTENT)).as_bool();
                    SelectObject(dc, old);
                    let mut info = BITMAPINFO {
                        bmiHeader: BITMAPINFOHEADER {
                            biSize: std::mem::size_of::<BITMAPINFOHEADER>() as u32,
                            biWidth: w,
                            biHeight: -h,
                            biPlanes: 1,
                            biBitCount: 32,
                            biCompression: BI_RGB.0,
                            ..Default::default()
                        },
                        ..Default::default()
                    };
                    let mut px = vec![0u8; (w * h * 4) as usize];
                    let rows = GetDIBits(
                        dc,
                        bmp,
                        0,
                        h as u32,
                        Some(px.as_mut_ptr().cast()),
                        &mut info,
                        DIB_RGB_COLORS,
                    );
                    let _ = DeleteObject(bmp.into());
                    let _ = DeleteDC(dc);
                    ReleaseDC(None, screen);
                    if !drawn || rows != h {
                        return Err("the window did not draw itself".into());
                    }
                    // The frame the screen shows, without the invisible
                    // resize borders.
                    let [x0, y0, x1, y1] = b.rect;
                    let (ox, oy) = ((x0 - wr.left).clamp(0, w), (y0 - wr.top).clamp(0, h));
                    let (fw, fh) = ((x1 - x0).min(w - ox), (y1 - y0).min(h - oy));
                    let mut bgra = Vec::with_capacity((fw * fh * 4) as usize);
                    for y in oy..oy + fh {
                        let row = ((y * w + ox) * 4) as usize;
                        bgra.extend_from_slice(&px[row..row + (fw * 4) as usize]);
                    }
                    for p in bgra.as_chunks_mut::<4>().0 {
                        p[3] = 255;
                    }
                    Ok(super::Frame {
                        bgra,
                        width: fw as u32,
                        height: fh as u32,
                    })
                }
            })
        }

        /// The window's elements, their bounds in its own pixels.
        pub fn elements_beside(b: &super::Beside) -> Option<crate::ai::elements::Listed> {
            let hwnd = HWND(b.id as usize as *mut _);
            physical(|| {
                walk(hwnd, b.rect, None).map(|(title, found)| crate::ai::elements::Listed {
                    window: title,
                    program: Some(b.program.clone()),
                    elements: found.into_iter().map(|(_, e)| e).collect(),
                })
            })
        }

        /// Acts on element `index` of the window through its UI Automation
        /// patterns, if it is still `expect` (its role and name).
        /// Returns: why it cannot (another element there now, no pattern for
        /// the act, a read-only value), for the task to fall back.
        pub fn act_beside(
            b: &super::Beside,
            index: usize,
            expect: &crate::ai::elements::Element,
            act: &super::BesideAct,
        ) -> Result<(), String> {
            use windows::Win32::UI::Accessibility::{
                ExpandCollapseState_Collapsed, IUIAutomationExpandCollapsePattern,
                IUIAutomationInvokePattern, IUIAutomationLegacyIAccessiblePattern,
                IUIAutomationScrollPattern, IUIAutomationSelectionItemPattern,
                IUIAutomationTogglePattern, IUIAutomationValuePattern, ScrollAmount_NoAmount,
                ScrollAmount_SmallDecrement, ScrollAmount_SmallIncrement,
                UIA_ExpandCollapsePatternId, UIA_InvokePatternId, UIA_LegacyIAccessiblePatternId,
                UIA_ScrollPatternId, UIA_SelectionItemPatternId, UIA_TogglePatternId,
                UIA_ValuePatternId,
            };
            let hwnd = HWND(b.id as usize as *mut _);
            let mut done: Option<Result<(), String>> = None;
            let mut act_on = |e: &IUIAutomationElement| -> Result<(), String> {
                // SAFETY: COM calls on an element this thread holds, within
                // the walk that found it.
                let r = unsafe {
                    let same = e.CachedName().map(|n| n.to_string()).unwrap_or_default()
                        == expect.name
                        && e.CachedControlType().ok().and_then(|t| role(t.0))
                            == Some(expect.role.as_str());
                    if !same {
                        Err(super::BESIDE_CHANGED.to_string())
                    } else {
                        match act {
                            super::BesideAct::Click => {
                                if let Ok(p) = e.GetCurrentPatternAs::<IUIAutomationInvokePattern>(
                                    UIA_InvokePatternId,
                                ) {
                                    p.Invoke().map_err(|e| e.to_string())
                                } else if let Ok(p) = e
                                    .GetCurrentPatternAs::<IUIAutomationTogglePattern>(
                                        UIA_TogglePatternId,
                                    )
                                {
                                    p.Toggle().map_err(|e| e.to_string())
                                } else if let Ok(p) = e
                                    .GetCurrentPatternAs::<IUIAutomationSelectionItemPattern>(
                                        UIA_SelectionItemPatternId,
                                    )
                                {
                                    p.Select().map_err(|e| e.to_string())
                                } else if let Ok(p) = e
                                    .GetCurrentPatternAs::<IUIAutomationExpandCollapsePattern>(
                                        UIA_ExpandCollapsePatternId,
                                    )
                                {
                                    if p.CurrentExpandCollapseState()
                                        .is_ok_and(|s| s == ExpandCollapseState_Collapsed)
                                    {
                                        p.Expand().map_err(|e| e.to_string())
                                    } else {
                                        p.Collapse().map_err(|e| e.to_string())
                                    }
                                } else if let Ok(p) = e
                                    .GetCurrentPatternAs::<IUIAutomationLegacyIAccessiblePattern>(
                                        UIA_LegacyIAccessiblePatternId,
                                    )
                                {
                                    p.DoDefaultAction().map_err(|e| e.to_string())
                                } else {
                                    Err("this element has no action to take in the background"
                                        .into())
                                }
                            }
                            super::BesideAct::Type(text) => {
                                let p = e
                                    .GetCurrentPatternAs::<IUIAutomationValuePattern>(
                                        UIA_ValuePatternId,
                                    )
                                    .map_err(|_| {
                                        "this element takes no text in the background".to_string()
                                    })?;
                                if p.CurrentIsReadOnly().is_ok_and(|r| r.as_bool()) {
                                    return Err("this element is read-only".into());
                                }
                                let now =
                                    p.CurrentValue().map(|v| v.to_string()).unwrap_or_default();
                                p.SetValue(&windows::core::BSTR::from(now + text))
                                    .map_err(|e| e.to_string())
                            }
                            super::BesideAct::Scroll(notches) => {
                                let p = e
                                    .GetCurrentPatternAs::<IUIAutomationScrollPattern>(
                                        UIA_ScrollPatternId,
                                    )
                                    .map_err(|_| {
                                        "this element does not scroll in the background".to_string()
                                    })?;
                                let step = if *notches > 0 {
                                    ScrollAmount_SmallIncrement
                                } else {
                                    ScrollAmount_SmallDecrement
                                };
                                for _ in 0..notches.unsigned_abs().min(20) {
                                    p.Scroll(ScrollAmount_NoAmount, step)
                                        .map_err(|e| e.to_string())?;
                                }
                                Ok(())
                            }
                        }
                    }
                };
                done = Some(r.clone());
                r
            };
            physical(|| walk(hwnd, b.rect, Some((index, &mut act_on))));
            done.unwrap_or_else(|| Err(super::BESIDE_CHANGED.into()))
        }

        /// The foreground window, as `Beside::id` names windows.
        pub fn front() -> Option<u64> {
            // SAFETY: plain queries; a null window gives a null root.
            let root = physical(|| unsafe { GetAncestor(GetForegroundWindow(), GA_ROOT) });
            (!root.is_invalid()).then_some(root.0 as u64)
        }

        /// Brings the window to the front (restored if minimized).
        /// Returns: whether it is in front now.
        pub fn raise(b: &super::Beside) -> bool {
            use windows::Win32::UI::WindowsAndMessaging::{
                IsIconic, SW_RESTORE, SetForegroundWindow, ShowWindow,
            };
            let hwnd = HWND(b.id as usize as *mut _);
            // SAFETY: plain window calls on a handle that may be gone, which
            // they refuse.
            unsafe {
                if IsIconic(hwnd).as_bool() {
                    let _ = ShowWindow(hwnd, SW_RESTORE);
                }
                let _ = SetForegroundWindow(hwnd);
            }
            front() == Some(b.id)
        }

        /// A UI Automation control type as the element list names it, or
        /// None for containers and parts that are not acted on (panes,
        /// groups, bars, scroll bars, separators, the window itself).
        fn role(control_type: i32) -> Option<&'static str> {
            Some(match control_type {
                50000 => "button",
                50002 => "checkbox",
                50003 => "combobox",
                50004 => "edit",
                50005 => "link",
                50006 => "image",
                50007 => "listitem",
                50011 => "menuitem",
                50013 => "radiobutton",
                50015 => "slider",
                50016 => "spinner",
                50019 => "tabitem",
                50020 => "text",
                50024 => "treeitem",
                50029 => "dataitem",
                50030 => "document",
                50031 => "splitbutton",
                50035 => "headeritem",
                _ => return None,
            })
        }

        /// A File Explorer window opened on a folder reads as that folder,
        /// from the shell's list and from its address bar. Opens and closes
        /// a window on this desktop: run by hand (`--ignored`).
        #[test]
        #[ignore]
        fn explorer_shows_its_folder() {
            use windows::Win32::Foundation::{LPARAM, WPARAM};
            use windows::Win32::UI::WindowsAndMessaging::{PostMessageW, WM_CLOSE};
            let dir = std::env::temp_dir().join("kyokai-explorer-folder");
            std::fs::create_dir_all(&dir).unwrap();
            // explorer.exe hands the folder to the running shell and returns.
            let _ = std::process::Command::new("explorer.exe")
                .arg(&dir)
                .status();
            let mut root = HWND::default();
            for _ in 0..50 {
                std::thread::sleep(std::time::Duration::from_millis(200));
                let mut h = None;
                // SAFETY: a plain walk over the top-level Explorer windows.
                while let Ok(next) = unsafe { FindWindowExW(None, h, w!("CabinetWClass"), None) } {
                    eprintln!(
                        "{next:?}: {:?} / {:?}",
                        explorer_folder(next),
                        address_bar(next)
                    );
                    if explorer_folder(next).is_some_and(|f| f.ends_with("kyokai-explorer-folder"))
                    {
                        root = next;
                    }
                    h = Some(next);
                }
                if !root.is_invalid() {
                    break;
                }
            }
            assert!(!root.is_invalid(), "no Explorer window on the folder");
            let want = dir.to_string_lossy().to_lowercase();
            assert_eq!(
                explorer_folder(root).map(|f| f.to_lowercase()),
                Some(want.clone())
            );
            eprintln!("address bar: {:?}", address_bar(root));
            let t0 = std::time::Instant::now();
            let listed = elements_of(root).expect("Explorer's elements");
            let mut roles: Vec<&str> = listed.elements.iter().map(|e| e.role.as_str()).collect();
            roles.sort();
            roles.dedup();
            eprintln!(
                "elements: {} in {:?}, roles {roles:?}",
                listed.elements.len(),
                t0.elapsed()
            );
            assert!(!listed.elements.is_empty());
            // SAFETY: asks the window to close, as its close button does.
            unsafe {
                let _ = PostMessageW(Some(root), WM_CLOSE, WPARAM(0), LPARAM(0));
            }
            let _ = std::fs::remove_dir(&dir);
        }

        /// The foreground window's elements read from this desktop, with
        /// how long the read took; prints counts and roles only, no names.
        /// Run by hand (`--ignored`).
        #[test]
        #[ignore]
        fn elements_of_the_foreground_window() {
            let t0 = std::time::Instant::now();
            let listed = elements();
            let took = t0.elapsed();
            let Some(l) = listed else {
                eprintln!("no list (no window, Kyokai's own, or no tree) in {took:?}");
                return;
            };
            let mut roles: Vec<&str> = l.elements.iter().map(|e| e.role.as_str()).collect();
            roles.sort();
            roles.dedup();
            eprintln!(
                "{:?}: {} elements in {took:?}; roles {roles:?}; with text {}",
                l.program,
                l.elements.len(),
                l.elements.iter().filter(|e| !e.text.is_empty()).count()
            );
            assert!(l.elements.len() <= crate::ai::elements::MAX_ELEMENTS);
        }

        /// Beside the host user: an Explorer window, covered or not, is
        /// found, captured on its own, listed in its own pixels, and one of
        /// its tree items selected without the pointer. Opens and closes a
        /// window on this desktop: run by hand (`--ignored`).
        #[test]
        #[ignore]
        fn an_explorer_window_works_beside() {
            use windows::Win32::Foundation::{LPARAM, WPARAM};
            use windows::Win32::UI::WindowsAndMessaging::{PostMessageW, WM_CLOSE};
            let dir = std::env::temp_dir().join("kyokai-beside");
            std::fs::create_dir_all(&dir).unwrap();
            // explorer.exe hands the folder to the running shell and returns.
            let _ = std::process::Command::new("explorer.exe")
                .arg(&dir)
                .status();
            std::thread::sleep(std::time::Duration::from_secs(2));
            let b = beside(&["explorer.exe".into()]).expect("an Explorer window");
            let frame = capture_beside(&b).unwrap();
            assert_eq!(
                (frame.width as i32, frame.height as i32),
                (b.rect[2] - b.rect[0], b.rect[3] - b.rect[1])
            );
            assert!(
                frame.bgra.chunks(4).any(|p| p[0] != p[1]),
                "a picture, not a blank"
            );
            let listed = elements_beside(&b).unwrap();
            eprintln!(
                "window {:?}: roles {:?}",
                listed.window,
                listed
                    .elements
                    .iter()
                    .map(|e| e.role.as_str())
                    .collect::<std::collections::BTreeSet<_>>()
            );
            assert!(
                listed
                    .elements
                    .iter()
                    .all(|e| e.rect[2] <= frame.width as i32)
            );
            let (i, item) = listed
                .elements
                .iter()
                .enumerate()
                .find(|(_, e)| e.role == "treeitem")
                .expect("a tree item");
            let t0 = std::time::Instant::now();
            let acted = act_beside(&b, i, item, &super::BesideAct::Click);
            eprintln!(
                "{} elements; clicked {:?} in {:?}: {acted:?}",
                listed.elements.len(),
                item.role,
                t0.elapsed()
            );
            assert!(acted.is_ok());
            let wrong = crate::ai::elements::Element {
                name: "not this".into(),
                ..item.clone()
            };
            assert!(act_beside(&b, i, &wrong, &super::BesideAct::Click).is_err());
            // SAFETY: asks the window to close, as its close button does.
            unsafe {
                let _ = PostMessageW(
                    Some(HWND(b.id as usize as *mut _)),
                    WM_CLOSE,
                    WPARAM(0),
                    LPARAM(0),
                );
            }
            let _ = std::fs::remove_dir(&dir);
        }

        /// Address bar texts in any language name their folder from its
        /// drive or share on; none for a place not on disk.
        #[test]
        fn address_bars_name_their_folder() {
            for (text, want) in [
                (
                    r"Address: C:\Users\me\Documents",
                    Some(r"C:\Users\me\Documents"),
                ),
                (r"地址: D:\工作", Some(r"D:\工作")),
                (r"Address: \\nas\share\x", Some(r"\\nas\share\x")),
                ("Address: Desktop", None),
                ("", None),
            ] {
                assert_eq!(path_in(text).as_deref(), want, "{text}");
            }
        }

        /// The path in an address bar's text: from its first drive (`C:\`)
        /// or share (`\\`) on.
        fn path_in(text: &str) -> Option<String> {
            let b = text.as_bytes();
            let (start, _) = text.char_indices().find(|&(i, c)| {
                (c.is_ascii_alphabetic()
                    && b.get(i + 1) == Some(&b':')
                    && b.get(i + 2) == Some(&b'\\'))
                    || text[i..].starts_with("\\\\")
            })?;
            Some(text[start..].trim_end().to_owned())
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
