//! NanoKVM USB (doc/en-us/remote-services.md, "NanoKVM USB"): a computer
//! wired to this host through Sipeed's NanoKVM USB, offered to controllers
//! as a desktop of its own. The device is two USB functions behind one hub:
//! a UVC capture card for the target's HDMI output and a CH340 serial chip
//! that types and points on the target through its USB keyboard and mouse
//! (the CH9329 command frames). The host's user chooses which capture card
//! and which serial port, and whether to offer them at all.

use crate::input::{Event, PointerAction};
use crate::media::{self, VideoStart};
use crate::video::{Choice, Encoded, Targets};
use serde::{Deserialize, Serialize};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex, mpsc as smpsc};
use std::time::{Duration, Instant};
use tokio::sync::mpsc;

#[cfg(target_os = "linux")]
mod linux;
#[cfg(unix)]
mod termios;
#[cfg(target_os = "linux")]
use linux as sys;
#[cfg(target_os = "macos")]
mod mac;
#[cfg(target_os = "macos")]
use mac as sys;
#[cfg(windows)]
mod windows;
#[cfg(windows)]
use windows as sys;

/// The codec name of the capture card's own pictures, sent as they come
/// (Raw): JPEG, each frame on its own.
pub const RAW: &str = "mjpeg";
/// The serial line's speed (the CH9329's default).
const BAUD: u32 = 57_600;
/// The largest coordinate of the absolute pointer, across the picture.
const ABS_MAX: f32 = 4096.0;
/// How often the writer asks the device how the target stands (its USB
/// connected, its lock lights).
const INFO_EVERY: Duration = Duration::from_secs(1);
/// How long the writer waits for the device's answer to GET_INFO.
const INFO_WAIT: Duration = Duration::from_millis(100);
/// How long the capture thread waits before opening the card again after
/// it was lost.
const REOPEN_AFTER: Duration = Duration::from_millis(500);
/// How often the capture thread looks again for room while the session is
/// still sending the last frame.
const ROOM_WAIT: Duration = Duration::from_millis(2);
/// Longest the capture thread waits for a picture before it looks at its
/// stop flag again.
const FRAME_WAIT: Duration = Duration::from_millis(100);
/// Pictures the encoder may take without giving a frame before it counts as
/// failed.
const ENCODER_SILENT: Duration = Duration::from_secs(2);

/// What the host's user chose (Settings, Access, "NanoKVM USB"): whether
/// to offer it, and the capture card's and the serial port's ids as
/// `devices` lists them.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Settings {
    #[serde(default)]
    pub on: bool,
    #[serde(default)]
    pub video: String,
    #[serde(default)]
    pub serial: String,
}

impl Settings {
    /// Whether it is offered: on, with both devices chosen.
    pub fn offered(&self) -> bool {
        self.on && !self.video.is_empty() && !self.serial.is_empty()
    }
}

/// A capture card or a serial port of this computer.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct Port {
    /// What opens it: a device path, or the system's link to it.
    pub id: String,
    /// Its name, as the system gives it.
    pub name: String,
    /// Whether it looks like a NanoKVM USB's (its USB vendor and product).
    pub nanokvm: bool,
    /// The USB hub it hangs on, empty when unknown: a NanoKVM's two
    /// functions share their hub.
    #[serde(skip)]
    pub hub: String,
}

/// The capture cards and serial ports `devices` found, with the pair
/// that looks like one NanoKVM USB.
#[derive(Clone, Debug, Serialize)]
pub struct Devices {
    pub video: Vec<Port>,
    pub serial: Vec<Port>,
    /// `{video, serial}`: the ids of the pair suggested, if any.
    pub suggested: Option<Pair>,
}

/// A capture card and a serial port, by id.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct Pair {
    pub video: String,
    pub serial: String,
}

/// The capture cards and serial ports of this computer, and the pair that
/// looks like one NanoKVM USB.
/// Notes: asks the system; call it off the async runtime.
pub fn devices() -> Devices {
    let (video, serial) = (sys::videos(), sys::serials());
    let suggested = suggest(&video, &serial);
    Devices {
        video,
        serial,
        suggested,
    }
}

/// The capture card and serial port of one NanoKVM USB: the NanoKVM's
/// that share a hub, else the only NanoKVM capture card with the only
/// NanoKVM serial port.
fn suggest(video: &[Port], serial: &[Port]) -> Option<Pair> {
    let kvm_video: Vec<&Port> = video.iter().filter(|p| p.nanokvm).collect();
    let kvm_serial: Vec<&Port> = serial.iter().filter(|p| p.nanokvm).collect();
    for v in &kvm_video {
        if !v.hub.is_empty()
            && let Some(s) = kvm_serial.iter().find(|s| s.hub == v.hub)
        {
            return Some(Pair {
                video: v.id.clone(),
                serial: s.id.clone(),
            });
        }
    }
    match (kvm_video.as_slice(), kvm_serial.as_slice()) {
        ([v], [s]) => Some(Pair {
            video: v.id.clone(),
            serial: s.id.clone(),
        }),
        _ => None,
    }
}

/// The CH9329 commands Kyokai sends.
mod cmd {
    pub const GET_INFO: u8 = 0x01;
    pub const KEYBOARD: u8 = 0x02;
    pub const MOUSE_ABS: u8 = 0x04;
}

/// One command frame: `57 AB`, the address (0), the command, the data's
/// length, the data and the low byte of the sum of everything before it.
fn frame(command: u8, data: &[u8]) -> Vec<u8> {
    let mut f = Vec::with_capacity(6 + data.len());
    f.extend_from_slice(&[0x57, 0xAB, 0x00, command, data.len() as u8]);
    f.extend_from_slice(data);
    let sum = f.iter().fold(0u8, |s, b| s.wrapping_add(*b));
    f.push(sum);
    f
}

/// How the target stands, from the device's answer to GET_INFO: its lock
/// lights, which the controller's are brought in line with.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Info {
    /// The target's USB is connected (it powered the keyboard and mouse),
    /// for the log.
    pub target: bool,
    pub num: bool,
    pub caps: bool,
}

/// The last GET_INFO answer in `bytes`, skipping other frames (the
/// device acknowledges every command) and anything that is not a whole,
/// correct frame.
fn parse_info(bytes: &[u8]) -> Option<Info> {
    let mut found = None;
    let mut i = 0;
    while i + 6 <= bytes.len() {
        if bytes[i] != 0x57 || bytes[i + 1] != 0xAB {
            i += 1;
            continue;
        }
        let len = bytes[i + 4] as usize;
        let end = i + 5 + len;
        if end >= bytes.len() {
            break;
        }
        let sum = bytes[i..end].iter().fold(0u8, |s, b| s.wrapping_add(*b));
        if sum != bytes[end] {
            i += 1;
            continue;
        }
        if bytes[i + 3] == 0x80 | cmd::GET_INFO && len >= 3 {
            let d = &bytes[i + 5..end];
            found = Some(Info {
                target: d[1] != 0,
                num: d[2] & 1 != 0,
                caps: d[2] & 2 != 0,
            });
        }
        i = end + 1;
    }
    found
}

/// A key as the target's USB keyboard reports it: a key's usage, or a
/// modifier's bit.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Usage {
    Key(u8),
    Mod(u8),
}

/// Modifier bits of a keyboard report.
const LCTRL: u8 = 0x01;
const LSHIFT: u8 = 0x02;
const LALT: u8 = 0x04;
const LMETA: u8 = 0x08;
const RCTRL: u8 = 0x10;
const RSHIFT: u8 = 0x20;
const RALT: u8 = 0x40;
const RMETA: u8 = 0x80;
/// The usages of the lock keys the target follows.
const CAPS_LOCK: u8 = 0x39;
const NUM_LOCK: u8 = 0x53;

/// The USB usage of a key by its scan code as Windows numbers it
/// (`crate::keyboard::scan_code`): the US positions, which the target's own
/// layout then reads.
fn scan_usage(code: u32) -> Option<Usage> {
    use Usage::{Key, Mod};
    Some(match code {
        0x01 => Key(0x29),
        0x02..=0x0A => Key(0x1E + (code - 0x02) as u8),
        0x0B => Key(0x27),
        0x0C => Key(0x2D),
        0x0D => Key(0x2E),
        0x0E => Key(0x2A),
        0x0F => Key(0x2B),
        0x10 => Key(0x14),
        0x11 => Key(0x1A),
        0x12 => Key(0x08),
        0x13 => Key(0x15),
        0x14 => Key(0x17),
        0x15 => Key(0x1C),
        0x16 => Key(0x18),
        0x17 => Key(0x0C),
        0x18 => Key(0x12),
        0x19 => Key(0x13),
        0x1A => Key(0x2F),
        0x1B => Key(0x30),
        0x1C => Key(0x28),
        0x1D => Mod(LCTRL),
        0x1E => Key(0x04),
        0x1F => Key(0x16),
        0x20 => Key(0x07),
        0x21 => Key(0x09),
        0x22 => Key(0x0A),
        0x23 => Key(0x0B),
        0x24 => Key(0x0D),
        0x25 => Key(0x0E),
        0x26 => Key(0x0F),
        0x27 => Key(0x33),
        0x28 => Key(0x34),
        0x29 => Key(0x35),
        0x2A => Mod(LSHIFT),
        0x2B => Key(0x31),
        0x2C => Key(0x1D),
        0x2D => Key(0x1B),
        0x2E => Key(0x06),
        0x2F => Key(0x19),
        0x30 => Key(0x05),
        0x31 => Key(0x11),
        0x32 => Key(0x10),
        0x33 => Key(0x36),
        0x34 => Key(0x37),
        0x35 => Key(0x38),
        0x36 => Mod(RSHIFT),
        0x37 => Key(0x55),
        0x38 => Mod(LALT),
        0x39 => Key(0x2C),
        0x3A => Key(CAPS_LOCK),
        0x3B..=0x44 => Key(0x3A + (code - 0x3B) as u8),
        0x45 => Key(NUM_LOCK),
        0x46 => Key(0x47),
        0x47 => Key(0x5F),
        0x48 => Key(0x60),
        0x49 => Key(0x61),
        0x4A => Key(0x56),
        0x4B => Key(0x5C),
        0x4C => Key(0x5D),
        0x4D => Key(0x5E),
        0x4E => Key(0x57),
        0x4F => Key(0x59),
        0x50 => Key(0x5A),
        0x51 => Key(0x5B),
        0x52 => Key(0x62),
        0x53 => Key(0x63),
        0x54 => Key(0x46),
        0x56 => Key(0x64),
        0x57 => Key(0x44),
        0x58 => Key(0x45),
        0x59 => Key(0x67),
        0x64..=0x6E => Key(0x68 + (code - 0x64) as u8),
        0x70 => Key(0x88),
        0x73 => Key(0x87),
        0x76 => Key(0x73),
        0x79 => Key(0x8A),
        0x7B => Key(0x8B),
        0x7D => Key(0x89),
        0x7E => Key(0x85),
        0xF1 => Key(0x91),
        0xF2 => Key(0x90),
        0xE01C => Key(0x58),
        0xE01D => Mod(RCTRL),
        0xE035 => Key(0x54),
        0xE037 => Key(0x46),
        0xE038 => Mod(RALT),
        0xE046 => Key(0x48),
        0xE047 => Key(0x4A),
        0xE048 => Key(0x52),
        0xE049 => Key(0x4B),
        0xE04B => Key(0x50),
        0xE04D => Key(0x4F),
        0xE04F => Key(0x4D),
        0xE050 => Key(0x51),
        0xE051 => Key(0x4E),
        0xE052 => Key(0x49),
        0xE053 => Key(0x4C),
        0xE05B => Mod(LMETA),
        0xE05C => Mod(RMETA),
        0xE05D => Key(0x65),
        0xE05E => Key(0x66),
        0xE11D => Key(0x48),
        _ => return None,
    })
}

/// The USB usage of a key by name (`crate::input::vk`'s names, Legacy).
fn name_usage(name: &str) -> Option<Usage> {
    use Usage::{Key, Mod};
    let b = name.as_bytes();
    if b.len() == 1 && b[0].is_ascii_lowercase() {
        return Some(Key(0x04 + (b[0] - b'a')));
    }
    if b.len() == 1 && b[0].is_ascii_digit() {
        return Some(Key(if b[0] == b'0' {
            0x27
        } else {
            0x1E + (b[0] - b'1')
        }));
    }
    if let Some(n) = name.strip_prefix('f').and_then(|n| n.parse::<u8>().ok()) {
        return match n {
            1..=12 => Some(Key(0x3A + n - 1)),
            13..=24 => Some(Key(0x68 + n - 13)),
            _ => None,
        };
    }
    Some(match name {
        "enter" => Key(0x28),
        "escape" => Key(0x29),
        "backspace" => Key(0x2A),
        "tab" => Key(0x2B),
        "space" => Key(0x2C),
        "delete" => Key(0x4C),
        "insert" => Key(0x49),
        "home" => Key(0x4A),
        "end" => Key(0x4D),
        "page-up" => Key(0x4B),
        "page-down" => Key(0x4E),
        "left" => Key(0x50),
        "up" => Key(0x52),
        "right" => Key(0x4F),
        "down" => Key(0x51),
        "minus" => Key(0x2D),
        "equal" => Key(0x2E),
        "comma" => Key(0x36),
        "period" => Key(0x37),
        "slash" => Key(0x38),
        "semicolon" => Key(0x33),
        "quote" => Key(0x34),
        "bracket-left" => Key(0x2F),
        "bracket-right" => Key(0x30),
        "backslash" => Key(0x31),
        "backquote" => Key(0x35),
        "context-menu" => Key(0x65),
        "caps-lock" => Key(CAPS_LOCK),
        "print-screen" => Key(0x46),
        "shift" => Mod(LSHIFT),
        "ctrl" => Mod(LCTRL),
        "alt" => Mod(LALT),
        "meta" => Mod(LMETA),
        _ => return None,
    })
}

/// How a character is typed on a US keyboard: its key's usage and whether
/// Shift is held. None for characters it has no key for.
fn char_usage(c: char) -> Option<(u8, bool)> {
    let shifted = |plain: char| char_usage(plain).map(|(u, _)| (u, true));
    Some(match c {
        'a'..='z' => (0x04 + (c as u8 - b'a'), false),
        'A'..='Z' => (0x04 + (c as u8 - b'A'), true),
        '1'..='9' => (0x1E + (c as u8 - b'1'), false),
        '0' => (0x27, false),
        '\n' => (0x28, false),
        '\t' => (0x2B, false),
        ' ' => (0x2C, false),
        '-' => (0x2D, false),
        '=' => (0x2E, false),
        '[' => (0x2F, false),
        ']' => (0x30, false),
        '\\' => (0x31, false),
        ';' => (0x33, false),
        '\'' => (0x34, false),
        '`' => (0x35, false),
        ',' => (0x36, false),
        '.' => (0x37, false),
        '/' => (0x38, false),
        '!' => return shifted('1'),
        '@' => return shifted('2'),
        '#' => return shifted('3'),
        '$' => return shifted('4'),
        '%' => return shifted('5'),
        '^' => return shifted('6'),
        '&' => return shifted('7'),
        '*' => return shifted('8'),
        '(' => return shifted('9'),
        ')' => return shifted('0'),
        '_' => return shifted('-'),
        '+' => return shifted('='),
        '{' => return shifted('['),
        '}' => return shifted(']'),
        '|' => return shifted('\\'),
        ':' => return shifted(';'),
        '"' => return shifted('\''),
        '~' => return shifted('`'),
        '<' => return shifted(','),
        '>' => return shifted('.'),
        '?' => return shifted('/'),
        _ => return None,
    })
}

/// The modifier bits a key by name is pressed with.
fn mod_bits(m: crate::input::Mods) -> u8 {
    (if m.ctrl { LCTRL } else { 0 })
        | (if m.shift { LSHIFT } else { 0 })
        | (if m.alt { LALT } else { 0 })
        | (if m.meta { LMETA } else { 0 })
}

/// A report for the writer: the keyboard's (modifiers and up to six keys)
/// or the absolute pointer's (buttons, position, wheel). A pointer report
/// that only moves may be replaced by the next one.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Report {
    Keyboard([u8; 8]),
    Pointer { data: [u8; 7], moves_only: bool },
}

impl Report {
    /// The command frame that sends it.
    fn frame(&self) -> Vec<u8> {
        match self {
            Report::Keyboard(d) => frame(cmd::KEYBOARD, d),
            Report::Pointer { data, .. } => frame(cmd::MOUSE_ABS, data),
        }
    }
}

/// Keeps the last of each run of reports that only move the pointer: the
/// serial line takes some 400 frames a second, so moves wait for nothing
/// they would only delay; presses, releases and keys all go, in order.
fn coalesce(reports: Vec<Report>) -> Vec<Report> {
    let mut out: Vec<Report> = Vec::with_capacity(reports.len());
    for r in reports {
        if let (
            Report::Pointer {
                moves_only: true, ..
            },
            Some(Report::Pointer {
                moves_only: true, ..
            }),
        ) = (r, out.last())
        {
            out.pop();
        }
        out.push(r);
    }
    out
}

/// The target's keyboard and mouse as this session holds them, turned into
/// reports.
#[derive(Debug, Default)]
struct Hold {
    mods: u8,
    keys: Vec<u8>,
    buttons: u8,
    x: u16,
    y: u16,
}

impl Hold {
    fn keyboard(&self) -> Report {
        let mut d = [0u8; 8];
        d[0] = self.mods;
        for (i, k) in self.keys.iter().take(6).enumerate() {
            d[2 + i] = *k;
        }
        Report::Keyboard(d)
    }

    fn pointer(&self, wheel: i8, moves_only: bool) -> Report {
        let [xl, xh] = self.x.to_le_bytes();
        let [yl, yh] = self.y.to_le_bytes();
        Report::Pointer {
            data: [0x02, self.buttons, xl, xh, yl, yh, wheel as u8],
            moves_only,
        }
    }

    /// Presses or releases a key or a modifier.
    fn key(&mut self, u: Usage, down: bool) {
        match (u, down) {
            (Usage::Mod(b), true) => self.mods |= b,
            (Usage::Mod(b), false) => self.mods &= !b,
            (Usage::Key(k), true) => {
                if !self.keys.contains(&k) && self.keys.len() < 6 {
                    self.keys.push(k);
                }
            }
            (Usage::Key(k), false) => self.keys.retain(|&h| h != k),
        }
    }

    /// The reports for one input event, given how the target's lock keys
    /// stand (`lights`, None while unknown).
    /// Returns: the reports, and the lock lights as they will stand after.
    fn event(&mut self, e: &Event, lights: Option<Info>) -> (Vec<Report>, Option<Info>) {
        let mut out = Vec::new();
        let mut lights = lights;
        match e {
            Event::Pointer {
                action,
                button,
                x,
                y,
            } => {
                let at = |f: f32| (f.clamp(0.0, 1.0) * ABS_MAX).min(ABS_MAX - 1.0) as u16;
                (self.x, self.y) = (at(*x), at(*y));
                let bit = match button {
                    0 => 0x01,
                    1 => 0x02,
                    2 => 0x04,
                    _ => 0,
                };
                match action {
                    PointerAction::Move => {}
                    PointerAction::Down => self.buttons |= bit,
                    PointerAction::Up => self.buttons &= !bit,
                }
                out.push(self.pointer(0, *action == PointerAction::Move));
            }
            // Positive dy moves the content down: the wheel turned up.
            Event::Scroll { dy, .. } => {
                let lines = dy.round().clamp(-127.0, 127.0) as i8;
                if lines != 0 {
                    out.push(self.pointer(lines, false));
                    out.push(self.pointer(0, false));
                }
            }
            Event::Scan {
                code,
                down,
                caps,
                num,
            } => {
                let Some(u) = scan_usage(*code) else {
                    return (out, lights);
                };
                // The target's lock brought in line with the controller's
                // before a key it changes goes down (as on a desktop).
                if *down
                    && let Some((lock, want)) = crate::input::lock_for(*code, *caps, *num)
                    && let Some(l) = lights.as_mut()
                {
                    let (on, usage) = if lock == 0x3A {
                        (&mut l.caps, CAPS_LOCK)
                    } else {
                        (&mut l.num, NUM_LOCK)
                    };
                    if *on != want {
                        *on = want;
                        self.key(Usage::Key(usage), true);
                        out.push(self.keyboard());
                        self.key(Usage::Key(usage), false);
                        out.push(self.keyboard());
                    }
                }
                self.key(u, *down);
                out.push(self.keyboard());
            }
            Event::Key { key, down, mods } => {
                let Some(u) = name_usage(key) else {
                    return (out, lights);
                };
                self.key(u, *down);
                // The key's own modifiers while it is down, none after.
                let own = match u {
                    Usage::Mod(b) if *down => b,
                    _ => 0,
                };
                self.mods = if *down { mod_bits(*mods) | own } else { 0 };
                out.push(self.keyboard());
            }
            Event::Text { text } => {
                let (mods, keys) = (self.mods, std::mem::take(&mut self.keys));
                for c in text.chars() {
                    let Some((usage, shift)) = char_usage(c) else {
                        continue;
                    };
                    self.mods = if shift { LSHIFT } else { 0 };
                    self.keys = vec![usage];
                    out.push(self.keyboard());
                    self.keys.clear();
                    out.push(self.keyboard());
                }
                (self.mods, self.keys) = (mods, keys);
                if !text.is_empty() {
                    out.push(self.keyboard());
                }
            }
        }
        (out, lights)
    }

    /// Everything released: the keyboard and the pointer's buttons.
    fn release(&mut self) -> Vec<Report> {
        (self.mods, self.buttons) = (0, 0);
        self.keys.clear();
        vec![self.keyboard(), self.pointer(0, false)]
    }
}

/// The target's keyboard and mouse through the NanoKVM's serial port: a
/// session's input, written by a thread of its own that also asks the
/// device how the target stands. Dropping it releases everything held.
pub struct Hid {
    hold: Hold,
    tx: Option<smpsc::Sender<Vec<Report>>>,
    info: Arc<Mutex<Option<Info>>>,
}

impl Hid {
    /// Opens the serial port `id` and starts its writer.
    /// Returns: why it did not open (missing, taken, no permission).
    pub fn open(id: &str) -> Result<Hid, String> {
        let mut port = sys::Serial::open(id, BAUD)?;
        let (tx, rx) = smpsc::channel::<Vec<Report>>();
        let info: Arc<Mutex<Option<Info>>> = Arc::new(Mutex::new(None));
        let seen = info.clone();
        std::thread::spawn(move || {
            let mut asked = Instant::now() - INFO_EVERY;
            let mut answer = Vec::new();
            loop {
                let wait = INFO_EVERY.saturating_sub(asked.elapsed());
                match rx.recv_timeout(wait) {
                    Ok(mut reports) => {
                        while let Ok(more) = rx.try_recv() {
                            reports.extend(more);
                        }
                        let bytes: Vec<u8> =
                            coalesce(reports).iter().flat_map(|r| r.frame()).collect();
                        if let Err(e) = port.write(&bytes) {
                            log::info!("nanokvm: the serial port failed: {e}");
                            *seen.lock().unwrap_or_else(|e| e.into_inner()) = None;
                            return;
                        }
                    }
                    Err(smpsc::RecvTimeoutError::Timeout) => {
                        asked = Instant::now();
                        answer.clear();
                        let got = port
                            .write(&frame(cmd::GET_INFO, &[]))
                            .and_then(|()| port.read(INFO_WAIT, &mut answer));
                        if let Err(e) = got {
                            log::info!("nanokvm: the serial port failed: {e}");
                            *seen.lock().unwrap_or_else(|e| e.into_inner()) = None;
                            return;
                        }
                        if let Some(i) = parse_info(&answer) {
                            let mut seen = seen.lock().unwrap_or_else(|e| e.into_inner());
                            if seen.is_none_or(|was| was.target != i.target) {
                                log::info!(
                                    "nanokvm: the wired computer's USB is {}",
                                    if i.target {
                                        "connected"
                                    } else {
                                        "not connected"
                                    }
                                );
                            }
                            *seen = Some(i);
                        }
                    }
                    Err(smpsc::RecvTimeoutError::Disconnected) => return,
                }
            }
        });
        Ok(Hid {
            hold: Hold::default(),
            tx: Some(tx),
            info,
        })
    }

    /// Sends one input event to the target.
    /// Returns: whether it was sent (false for a key it has no usage for,
    /// or a serial port that failed).
    pub fn inject(&mut self, e: &Event) -> bool {
        if !e.valid() {
            return false;
        }
        let lights = *self.info.lock().unwrap_or_else(|e| e.into_inner());
        let (reports, after) = self.hold.event(e, lights);
        if reports.is_empty() {
            return false;
        }
        if after != lights {
            *self.info.lock().unwrap_or_else(|e| e.into_inner()) = after;
        }
        self.send(reports)
    }

    /// Ctrl+Alt+Del on the target.
    pub fn ctrl_alt_del(&mut self) -> bool {
        let mut h = Hold {
            mods: LCTRL | LALT,
            keys: vec![0x4C],
            ..Hold::default()
        };
        let press = h.keyboard();
        h.keys.clear();
        h.mods = 0;
        self.send(vec![press, h.keyboard()])
    }

    /// Releases every key and button held on the target.
    pub fn release_all(&mut self) {
        let r = self.hold.release();
        self.send(r);
    }

    fn send(&self, reports: Vec<Report>) -> bool {
        self.tx.as_ref().is_some_and(|tx| tx.send(reports).is_ok())
    }
}

impl Drop for Hid {
    fn drop(&mut self) {
        self.release_all();
        self.tx = None;
    }
}

/// What the capture card gave.
pub(crate) enum Picture<'a> {
    /// One JPEG (Linux and Windows read the card's own MJPEG).
    #[cfg_attr(target_os = "macos", allow(dead_code))]
    Jpeg(&'a [u8]),
    /// A decoded picture (macOS, whose capture decodes it).
    #[cfg_attr(not(target_os = "macos"), allow(dead_code))]
    Bgra(crate::video::Picture<'a>),
}

/// Whether `j` is one whole JPEG: it starts with SOI and ends with EOI
/// (the card's first pictures after it opens can be cut short).
fn whole_jpeg(j: &[u8]) -> bool {
    let end = j.iter().rposition(|&b| b != 0).map_or(0, |i| i + 1);
    j.len() > 4 && j.starts_with(&[0xFF, 0xD8]) && j[..end].ends_with(&[0xFF, 0xD9])
}

/// Decodes a JPEG into `bgra`.
/// Returns: its width and height.
fn decode_jpeg(j: &[u8], bgra: &mut Vec<u8>) -> Result<(usize, usize), String> {
    decode_into(j, bgra, zune_jpeg::zune_core::colorspace::ColorSpace::BGRA)
}

/// Decodes a JPEG into `out` in `colour` (four bytes a pixel).
/// Returns: its width and height.
fn decode_into(
    j: &[u8],
    out: &mut Vec<u8>,
    colour: zune_jpeg::zune_core::colorspace::ColorSpace,
) -> Result<(usize, usize), String> {
    use zune_jpeg::JpegDecoder;
    use zune_jpeg::zune_core::bytestream::ZCursor;
    use zune_jpeg::zune_core::options::DecoderOptions;
    let options = DecoderOptions::default().jpeg_set_out_colorspace(colour);
    let mut d = JpegDecoder::new_with_options(ZCursor::new(j), options);
    d.decode_headers().map_err(|e| e.to_string())?;
    let (w, h) = d.dimensions().ok_or("no picture size")?;
    let size = d.output_buffer_size().ok_or("no picture size")?;
    out.resize(size, 0);
    d.decode_into(out).map_err(|e| e.to_string())?;
    Ok((w, h))
}

/// A viewer's decoder of the capture card's pictures (Raw): each frame a
/// JPEG on its own.
pub(crate) struct JpegDecoder;

impl JpegDecoder {
    /// Decodes one frame into `rgba`.
    /// Returns: the picture's size.
    pub fn decode(
        &mut self,
        frame: &[u8],
        rgba: &mut Vec<u8>,
    ) -> Result<Option<(u32, u32)>, String> {
        let rgba_space = zune_jpeg::zune_core::colorspace::ColorSpace::RGBA;
        let (w, h) = decode_into(frame, rgba, rgba_space)?;
        Ok(Some((w as u32, h as u32)))
    }
}

/// Whether this host can send the capture card's pictures as they come
/// (Raw): where its capture reads the card's own JPEG.
pub fn raw_here() -> bool {
    sys::RAW
}

/// Starts reading the capture card `video` and sending its pictures:
/// as they come for Raw (`RAW`), else encoded in `choice`.
/// Returns: the stream description once the card and the encoder work,
/// with the capture thread, or why not.
pub fn start(
    video: String,
    choice: Choice,
    tx: mpsc::Sender<Encoded>,
    key: Arc<AtomicBool>,
    stop: Arc<AtomicBool>,
    targets: Arc<Targets>,
) -> Result<(VideoStart, std::thread::JoinHandle<()>), String> {
    let (ready_tx, ready) = std::sync::mpsc::channel();
    let thread = std::thread::spawn(move || {
        let raw = choice.codec == RAW;
        let opened = (|| {
            if raw && !sys::RAW {
                return Err("this computer cannot read the card's own pictures".to_owned());
            }
            let (cam, w, h) = sys::Camera::open(&video)?;
            let encoder = if raw {
                None
            } else {
                let (ew, eh) =
                    crate::video::fit(w, h, crate::video::MAX_SIZE.0, crate::video::MAX_SIZE.1);
                Some((
                    crate::video::capture::Encoder::open(choice, ew, eh, true)?,
                    ew,
                    eh,
                ))
            };
            Ok::<_, String>((cam, encoder, w, h))
        })();
        let (mut cam, mut encoder, w, h) = match opened {
            Ok(v) => v,
            Err(e) => {
                log::info!("nanokvm: {} did not open: {e}", choice.codec);
                let _ = ready_tx.send(Err(e));
                return;
            }
        };
        let (name, hardware) = encoder.as_ref().map_or(("", false), |(e, ..)| e.kind());
        let (sw, sh) = encoder.as_ref().map_or((w, h), |(_, ew, eh)| (*ew, *eh));
        log::info!(
            "nanokvm: the card at {w}x{h}, sent as {} at {sw}x{sh}{}",
            choice.codec,
            if name.is_empty() {
                String::new()
            } else {
                format!(" through {name}")
            }
        );
        let _ = ready_tx.send(Ok(VideoStart {
            codec: choice.codec.into(),
            width: sw,
            height: sh,
            i444: encoder.as_ref().is_some_and(|(e, ..)| e.i444()),
            encoder: name.into(),
            hardware,
            placeholder: None,
        }));
        let t0 = Instant::now();
        let mut bitrate = 0u32;
        let mut bgra = Vec::new();
        let mut last_sent: Option<Instant> = None;
        // The last JPEG's hash: an unchanged picture is not encoded again
        // unless a key frame is asked for.
        let mut last_hash = 0u64;
        let mut key_pending = false;
        let mut silent_since: Option<Instant> = None;
        let mut cam_ok = true;
        while !stop.load(Ordering::Relaxed) {
            if !cam_ok {
                std::thread::sleep(REOPEN_AFTER);
                match sys::Camera::open(&video) {
                    Ok((c, _, _)) => {
                        log::info!("nanokvm: the capture card is back");
                        (cam, cam_ok, key_pending) = (c, true, true);
                    }
                    Err(_) => continue,
                }
            }
            let period = Duration::from_secs(1) / targets.fps.load(Ordering::Relaxed).max(1);
            key_pending |= key.swap(false, Ordering::Relaxed);
            let mut out: Option<(Vec<u8>, bool)> = None;
            let mut failed = None;
            let got = cam.frame(FRAME_WAIT, &mut |p| {
                // Pictures come at the card's rate; those between the
                // viewer's frames are dropped here.
                if !key_pending && last_sent.is_some_and(|t| t.elapsed() < period) {
                    return;
                }
                if targets.room.load(Ordering::Relaxed) == 0 {
                    return;
                }
                match (&p, encoder.as_mut()) {
                    (Picture::Jpeg(j), None) => {
                        if whole_jpeg(j) {
                            out = Some((j.to_vec(), true));
                        }
                    }
                    (Picture::Jpeg(j), Some((enc, ..))) => {
                        let hash = fnv(j);
                        if hash == last_hash && !key_pending {
                            return;
                        }
                        if !whole_jpeg(j) {
                            return;
                        }
                        match decode_jpeg(j, &mut bgra) {
                            Ok((pw, ph)) => {
                                last_hash = hash;
                                enc.picture(&bgra, pw, ph, pw * 4);
                                out = Some((Vec::new(), false));
                            }
                            Err(e) => log::debug!("nanokvm: a picture did not decode: {e}"),
                        }
                    }
                    (Picture::Bgra(b), Some((enc, ..))) => {
                        enc.picture(b.bgra, b.width, b.height, b.stride);
                        out = Some((Vec::new(), false));
                    }
                    (Picture::Bgra(_), None) => failed = Some("no JPEG to send".to_owned()),
                }
            });
            if let Some(e) = failed {
                log::info!("nanokvm: {e}");
                return;
            }
            match got {
                crate::video::Got::Lost => {
                    log::info!("nanokvm: the capture card was lost");
                    cam_ok = false;
                    continue;
                }
                crate::video::Got::Nothing => continue,
                crate::video::Got::Picture => {}
            }
            let Some((jpeg, is_raw)) = out else {
                continue;
            };
            let taken = Instant::now();
            let units = if is_raw {
                vec![(jpeg, true)]
            } else {
                let Some((enc, ..)) = encoder.as_mut() else {
                    continue;
                };
                let wanted = targets.bitrate.load(Ordering::Relaxed);
                if wanted != bitrate {
                    bitrate = wanted;
                    enc.set_bitrate(bitrate);
                }
                match enc.encode(key_pending) {
                    Ok(u) if u.is_empty() => {
                        if silent_since.get_or_insert(taken).elapsed() >= ENCODER_SILENT {
                            log::info!(
                                "nanokvm: {} gave nothing for {} s",
                                choice.codec,
                                ENCODER_SILENT.as_secs()
                            );
                            return;
                        }
                        continue;
                    }
                    Ok(u) => {
                        silent_since = None;
                        u
                    }
                    Err(e) => {
                        log::info!("nanokvm: {} failed: {e}", choice.codec);
                        return;
                    }
                }
            };
            if units.iter().any(|(_, k)| *k) {
                key_pending = false;
            }
            last_sent = Some(taken);
            let pts = t0.elapsed().as_micros() as u64;
            let took_us = taken.elapsed().as_micros().min(u32::MAX as u128) as u32;
            for (data, is_key) in units {
                let f = Encoded {
                    flags: if is_key { media::KEY_FRAME } else { 0 },
                    pts,
                    data,
                    took_us,
                };
                // The session's channel holds one frame: the capture waits
                // for it rather than piling frames up.
                loop {
                    match tx.try_reserve() {
                        Ok(permit) => {
                            permit.send(f);
                            break;
                        }
                        Err(mpsc::error::TrySendError::Full(())) => {
                            if stop.load(Ordering::Relaxed) {
                                return;
                            }
                            std::thread::sleep(ROOM_WAIT);
                        }
                        Err(mpsc::error::TrySendError::Closed(())) => return,
                    }
                }
            }
        }
    });
    ready
        .recv()
        .unwrap_or_else(|_| Err("the capture thread ended".into()))
        .map(|start| (start, thread))
}

/// FNV-1a over a JPEG's bytes, to tell an unchanged picture.
fn fnv(b: &[u8]) -> u64 {
    b.iter().fold(0xcbf2_9ce4_8422_2325, |h, &x| {
        (h ^ x as u64).wrapping_mul(0x0000_0100_0000_01b3)
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::input::Mods;

    #[test]
    fn frames_carry_their_sum() {
        assert_eq!(frame(cmd::GET_INFO, &[]), vec![0x57, 0xAB, 0, 1, 0, 0x03]);
        let f = frame(cmd::KEYBOARD, &[0, 0, 0x04, 0, 0, 0, 0, 0]);
        assert_eq!(f.len(), 14);
        assert_eq!(*f.last().unwrap(), 0x10);
    }

    #[test]
    fn info_is_read_past_acknowledgements_and_noise() {
        // The answer the attached device gave: version 1.8, target
        // connected, no lock lit.
        let info = [
            0x57, 0xAB, 0x00, 0x81, 0x08, 0x38, 0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0xC5,
        ];
        let ack = [0x57, 0xAB, 0x00, 0x82, 0x01, 0x00, 0x85];
        let mut bytes = vec![0x00, 0x57];
        bytes.extend(ack);
        bytes.extend(info);
        assert_eq!(
            parse_info(&bytes),
            Some(Info {
                target: true,
                num: false,
                caps: false
            })
        );
        let mut lit = info;
        lit[7] = 0x03;
        lit[13] = lit[13].wrapping_add(3);
        assert_eq!(
            parse_info(&lit),
            Some(Info {
                target: true,
                num: true,
                caps: true
            })
        );
        // A wrong sum or a cut frame is no answer.
        let mut bad = info;
        bad[13] ^= 1;
        assert_eq!(parse_info(&bad), None);
        assert_eq!(parse_info(&info[..10]), None);
    }

    #[test]
    fn scan_codes_reach_their_usb_keys() {
        for (code, want) in [
            (0x1E, Usage::Key(0x04)),   // A
            (0x02, Usage::Key(0x1E)),   // 1
            (0x0B, Usage::Key(0x27)),   // 0
            (0x1C, Usage::Key(0x28)),   // Enter
            (0x3B, Usage::Key(0x3A)),   // F1
            (0x58, Usage::Key(0x45)),   // F12
            (0xE048, Usage::Key(0x52)), // Up
            (0xE01C, Usage::Key(0x58)), // keypad Enter
            (0x1D, Usage::Mod(LCTRL)),
            (0xE038, Usage::Mod(RALT)),
            (0xE05B, Usage::Mod(LMETA)),
            (0xE11D, Usage::Key(0x48)), // Pause
        ] {
            assert_eq!(scan_usage(code), Some(want), "{code:#x}");
        }
        assert_eq!(scan_usage(0xE0FF), None);
        assert_eq!(name_usage("f13"), Some(Usage::Key(0x68)));
        assert_eq!(name_usage("0"), Some(Usage::Key(0x27)));
        assert_eq!(name_usage("q"), Some(Usage::Key(0x14)));
        assert_eq!(name_usage("nope"), None);
    }

    #[test]
    fn text_types_on_a_us_keyboard() {
        let mut h = Hold::default();
        let (r, _) = h.event(
            &Event::Text {
                text: "aA!é".into(),
            },
            None,
        );
        let keys: Vec<[u8; 8]> = r
            .iter()
            .map(|r| match r {
                Report::Keyboard(d) => *d,
                Report::Pointer { .. } => panic!("a pointer report"),
            })
            .collect();
        assert_eq!(
            keys,
            vec![
                [0, 0, 0x04, 0, 0, 0, 0, 0],
                [0; 8],
                [LSHIFT, 0, 0x04, 0, 0, 0, 0, 0],
                [LSHIFT, 0, 0, 0, 0, 0, 0, 0],
                [LSHIFT, 0, 0x1E, 0, 0, 0, 0, 0],
                [LSHIFT, 0, 0, 0, 0, 0, 0, 0],
                [0; 8],
            ]
        );
    }

    #[test]
    fn the_pointer_is_absolute_across_the_picture() {
        let mut h = Hold::default();
        let at = |h: &mut Hold, action, x, y| {
            h.event(
                &Event::Pointer {
                    action,
                    button: 0,
                    x,
                    y,
                },
                None,
            )
            .0
        };
        assert_eq!(
            at(&mut h, PointerAction::Move, 0.5, 1.0),
            vec![Report::Pointer {
                data: [2, 0, 0x00, 0x08, 0xFF, 0x0F, 0],
                moves_only: true
            }]
        );
        assert_eq!(
            at(&mut h, PointerAction::Down, 0.0, 0.0),
            vec![Report::Pointer {
                data: [2, 1, 0, 0, 0, 0, 0],
                moves_only: false
            }]
        );
        // Scrolling down (content up) turns the wheel down, then stops it.
        let (r, _) = h.event(&Event::Scroll { dx: 0.0, dy: -3.0 }, None);
        assert_eq!(r.len(), 2);
        assert!(matches!(r[0], Report::Pointer { data, .. } if data[1] == 1 && data[6] == 0xFD));
        assert_eq!(h.release()[1], h.pointer(0, false));
        assert_eq!(h.buttons, 0);
    }

    #[test]
    fn moves_coalesce_but_presses_stay_in_order() {
        let mv = |x: u8| Report::Pointer {
            data: [2, 0, x, 0, 0, 0, 0],
            moves_only: true,
        };
        let press = Report::Pointer {
            data: [2, 1, 9, 0, 0, 0, 0],
            moves_only: false,
        };
        let key = Report::Keyboard([0, 0, 4, 0, 0, 0, 0, 0]);
        assert_eq!(
            coalesce(vec![mv(1), mv(2), press, mv(3), key, mv(4), mv(5)]),
            vec![mv(2), press, mv(3), key, mv(5)]
        );
    }

    #[test]
    fn the_targets_lock_follows_the_controller() {
        let mut h = Hold::default();
        let off = Some(Info {
            target: true,
            num: false,
            caps: false,
        });
        // The controller has CapsLock on, the target not: CapsLock is
        // pressed and released before the letter.
        let (r, after) = h.event(
            &Event::Scan {
                code: 0x1E,
                down: true,
                caps: true,
                num: false,
            },
            off,
        );
        assert_eq!(
            r,
            vec![
                Report::Keyboard([0, 0, CAPS_LOCK, 0, 0, 0, 0, 0]),
                Report::Keyboard([0; 8]),
                Report::Keyboard([0, 0, 0x04, 0, 0, 0, 0, 0]),
            ]
        );
        assert!(after.unwrap().caps);
        // Unknown lights: the letter alone.
        let mut h = Hold::default();
        let (r, _) = h.event(
            &Event::Scan {
                code: 0x1E,
                down: true,
                caps: true,
                num: false,
            },
            None,
        );
        assert_eq!(r.len(), 1);
        // A named key goes with its modifiers, and lets them go after.
        let mut h = Hold::default();
        let ctrl = Mods {
            ctrl: true,
            ..Mods::default()
        };
        let (r, _) = h.event(
            &Event::Key {
                key: "c".into(),
                down: true,
                mods: ctrl,
            },
            None,
        );
        assert_eq!(r, vec![Report::Keyboard([LCTRL, 0, 0x06, 0, 0, 0, 0, 0])]);
        let (r, _) = h.event(
            &Event::Key {
                key: "c".into(),
                down: false,
                mods: ctrl,
            },
            None,
        );
        assert_eq!(r, vec![Report::Keyboard([0; 8])]);
    }

    #[test]
    fn a_nanokvm_pair_is_suggested_by_its_hub() {
        let port = |id: &str, hub: &str, nanokvm| Port {
            id: id.into(),
            name: id.into(),
            nanokvm,
            hub: hub.into(),
        };
        // Two NanoKVMs: each capture card goes with the serial port on its
        // own hub.
        let video = [
            port("/dev/video2", "1-3", true),
            port("/dev/video0", "1-2", true),
        ];
        let serial = [
            port("/dev/ttyUSB1", "1-2", true),
            port("/dev/ttyUSB0", "1-3", true),
        ];
        assert_eq!(
            suggest(&video, &serial),
            Some(Pair {
                video: "/dev/video2".into(),
                serial: "/dev/ttyUSB0".into()
            })
        );
        // Locations unknown (Windows, macOS): only a single pair.
        let video = [port("v", "", true), port("cam", "", false)];
        let serial = [port("COM5", "", true)];
        assert_eq!(
            suggest(&video, &serial),
            Some(Pair {
                video: "v".into(),
                serial: "COM5".into()
            })
        );
        let two = [port("COM5", "", true), port("COM6", "", true)];
        assert_eq!(suggest(&video, &two), None);
        assert_eq!(suggest(&[], &serial), None);
    }

    #[test]
    fn only_whole_jpegs_are_sent() {
        assert!(whole_jpeg(&[0xFF, 0xD8, 1, 2, 0xFF, 0xD9]));
        assert!(whole_jpeg(&[0xFF, 0xD8, 1, 2, 0xFF, 0xD9, 0, 0]));
        assert!(!whole_jpeg(&[0xFF, 0xD8, 1, 2, 3, 4]));
        assert!(!whole_jpeg(&[0, 0xD8, 1, 2, 0xFF, 0xD9]));
    }

    #[test]
    fn a_jpeg_decodes_to_bgra() {
        let j = TINY_JPEG;
        let mut bgra = Vec::new();
        let (w, h) = decode_jpeg(j, &mut bgra).unwrap();
        assert_eq!((w, h), (8, 8));
        assert_eq!(bgra.len(), 8 * 8 * 4);
        // Red: B low, R high, A opaque.
        assert!(
            bgra[0] < 40 && bgra[2] > 200 && bgra[3] == 255,
            "{:?}",
            &bgra[..4]
        );
        let mut rgba = Vec::new();
        let size = JpegDecoder.decode(j, &mut rgba).unwrap();
        assert_eq!(size, Some((8, 8)));
        assert!(rgba[0] > 200 && rgba[2] < 40);
    }

    /// Against a NanoKVM USB attached to this computer
    /// (`KYOKAI_TEST_NANOKVM=1`): the pair is found, the card gives whole
    /// pictures that decode, and the device says how the target stands. It
    /// sends the target nothing.
    #[test]
    fn live_nanokvm() {
        if std::env::var_os("KYOKAI_TEST_NANOKVM").is_none() {
            return;
        }
        let found = devices();
        println!("{found:#?}");
        let pair = found.suggested.expect("a NanoKVM pair");
        let (mut cam, w, h) = sys::Camera::open(&pair.video).unwrap();
        println!("card at {w}x{h}");
        let (mut whole, mut bytes, mut bgra) = (0, 0, Vec::new());
        let (mut decoding, t0) = (Duration::ZERO, Instant::now());
        while whole < 60 && t0.elapsed() < Duration::from_secs(10) {
            cam.frame(FRAME_WAIT, &mut |p| {
                if let Picture::Jpeg(j) = p
                    && whole_jpeg(j)
                {
                    let at = Instant::now();
                    decode_jpeg(j, &mut bgra).unwrap();
                    decoding += at.elapsed();
                    whole += 1;
                    bytes += j.len();
                }
            });
        }
        let secs = t0.elapsed().as_secs_f64();
        println!(
            "{whole} pictures in {secs:.2} s, {:.1} KB each, decoded in {:.1} ms each",
            bytes as f64 / whole as f64 / 1024.0,
            decoding.as_secs_f64() * 1000.0 / whole as f64
        );
        assert_eq!(whole, 60);
        let hid = Hid::open(&pair.serial).unwrap();
        let t0 = Instant::now();
        while hid.info.lock().unwrap().is_none() && t0.elapsed() < Duration::from_secs(3) {
            std::thread::sleep(Duration::from_millis(50));
        }
        let info = || *hid.info.lock().unwrap();
        println!("{:?}", info());
        assert!(info().is_some());
    }

    /// The card's pictures through the GPU's H.264 and HEVC (VA-API), as a
    /// Linux host sends them to a controller that decodes them: a key frame
    /// first, then P frames, each decoding to the card's size through the
    /// system's FFmpeg. With KYOKAI_TEST_NANOKVM; a codec the GPU or FFmpeg
    /// lacks is skipped.
    #[cfg(target_os = "linux")]
    #[test]
    fn live_nanokvm_through_va_api() {
        if std::env::var_os("KYOKAI_TEST_NANOKVM").is_none() {
            return;
        }
        let pair = devices().suggested.expect("a NanoKVM pair");
        let decodes = crate::ff::decodable();
        for codec in crate::va::encodable() {
            if !decodes.contains(&codec) {
                println!("{codec}: no FFmpeg decoder here");
                continue;
            }
            let (tx, mut rx) = mpsc::channel(8);
            let stop = Arc::new(AtomicBool::new(false));
            let choice = Choice { codec, i444: false };
            let key = Arc::new(AtomicBool::new(true));
            let (start, thread) = super::start(
                pair.video.clone(),
                choice,
                tx,
                key,
                stop.clone(),
                Arc::new(Targets::default()),
            )
            .unwrap();
            assert_eq!((start.encoder.as_str(), start.hardware), ("va-api", true));
            let mut dec = crate::ff::Decoder::new(codec).unwrap();
            let (mut keys, mut took, mut bytes) = (Vec::new(), 0u64, 0usize);
            let mut rgba = Vec::new();
            let t0 = Instant::now();
            while t0.elapsed() < Duration::from_secs(4) {
                if let Ok(e) = rx.try_recv() {
                    took += u64::from(e.took_us);
                    bytes += e.data.len();
                    keys.push(e.flags & crate::media::KEY_FRAME != 0);
                    let size = dec.decode(&e.data, &mut rgba).unwrap();
                    assert_eq!(size, Some((start.width, start.height)), "{codec}");
                } else {
                    std::thread::sleep(Duration::from_millis(2));
                }
            }
            stop.store(true, Ordering::Relaxed);
            drop(rx);
            let _ = thread.join();
            let n = keys.len();
            println!(
                "{codec}: {n} frames at {}x{} in 4 s, {:.2} Mbit/s, {:.1} ms from picture to frame",
                start.width,
                start.height,
                bytes as f64 * 8.0 / 4.0 / 1e6,
                took as f64 / n.max(1) as f64 / 1000.0
            );
            assert!(n > 1, "{codec}: frames came");
            assert!(keys[0], "{codec}: a key frame first");
            assert!(!keys[1..].iter().all(|&k| k), "{codec}: P frames after it");
        }
    }

    /// An 8×8 red baseline JPEG.
    const TINY_JPEG: &[u8] = include_bytes!("kvm/red8.jpg");
}
