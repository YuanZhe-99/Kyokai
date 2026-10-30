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
/// screenshot that fits `max`.
/// Returns: the PNG as base64 and its size.
pub fn shot(frame: &Frame, region: [u32; 4], max: (u32, u32)) -> Shot {
    let [x0, y0, x1, y1] = region;
    let (rw, rh) = (x1 - x0, y1 - y0);
    let scale = (max.0 as f64 / rw as f64)
        .min(max.1 as f64 / rh as f64)
        .min(1.0);
    let w = ((rw as f64 * scale).round() as u32).max(1);
    let h = ((rh as f64 * scale).round() as u32).max(1);
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
    }
}
