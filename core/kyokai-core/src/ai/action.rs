//! Kyokai's provider-neutral action schema: what the host's executor runs
//! (doc/en-us/computer-use.md, "AI tasks").
//!
//! Units: coordinates are screenshot pixels (origin top left); scroll deltas
//! are Windows `WHEEL_DELTA` units (120 = one wheel notch, positive `dy` =
//! down, positive `dx` = right); durations are milliseconds. Keys are
//! canonical lowercase names (see [`Key`]).

use serde::{Serialize, Serializer};
use std::fmt;

/// A point in screenshot pixels.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
pub struct Point {
    pub x: i32,
    pub y: i32,
}

/// A mouse button.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "kebab-case")]
pub enum Button {
    Left,
    Right,
    Middle,
    Back,
    Forward,
}

/// A canonical key. Named keys use lowercase names; printable keys are a
/// single lowercase character.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Key {
    Ctrl,
    Shift,
    Alt,
    /// The Windows key / Command.
    Meta,
    Named(&'static str),
    Char(char),
}

impl Key {
    /// Reports whether the key is a modifier.
    pub fn is_modifier(&self) -> bool {
        matches!(self, Key::Ctrl | Key::Shift | Key::Alt | Key::Meta)
    }
}

impl fmt::Display for Key {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Key::Ctrl => f.write_str("ctrl"),
            Key::Shift => f.write_str("shift"),
            Key::Alt => f.write_str("alt"),
            Key::Meta => f.write_str("meta"),
            Key::Named(n) => f.write_str(n),
            Key::Char(c) => write!(f, "{c}"),
        }
    }
}

impl Serialize for Key {
    fn serialize<S: Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
        s.collect_str(self)
    }
}

/// One action the model proposes. It is serialized for the approval prompt
/// and the task history (`{"kind": "click", …}`).
#[derive(Debug, Clone, PartialEq, Serialize)]
#[serde(tag = "kind", rename_all = "kebab-case")]
pub enum Action {
    Screenshot,
    /// `[x0, y0, x1, y1]` in screenshot pixels.
    Zoom {
        region: [i32; 4],
    },
    Move {
        to: Point,
        held: Vec<Key>,
    },
    /// `at: None` clicks at the current cursor position.
    Click {
        at: Option<Point>,
        button: Button,
        count: u8,
        modifiers: Vec<Key>,
    },
    MouseDown {
        button: Button,
    },
    MouseUp {
        button: Button,
    },
    /// Press at `path[0]`, move through the points, release at the last one.
    Drag {
        path: Vec<Point>,
        modifiers: Vec<Key>,
    },
    Scroll {
        at: Option<Point>,
        dx: i32,
        dy: i32,
        modifiers: Vec<Key>,
    },
    Type {
        text: String,
    },
    /// A combination pressed together (modifiers first), `repeat` times.
    KeyCombo {
        keys: Vec<Key>,
        repeat: u32,
    },
    HoldKey {
        keys: Vec<Key>,
        duration_ms: u32,
    },
    Wait {
        duration_ms: u32,
    },
    CursorPosition,
}

/// One wheel notch.
pub const WHEEL_DELTA: i32 = 120;
/// Longest wait or key hold.
pub const MAX_DURATION_MS: u32 = 300_000;
/// Most repetitions of one key combination.
pub const MAX_REPEAT: u32 = 100;
/// Most wheel notches in one scroll.
pub const MAX_NOTCHES: i32 = 100;
/// Longest text one action types.
pub const MAX_TEXT: usize = 16 * 1024;
/// Most points in one drag.
pub const MAX_PATH: usize = 256;

/// Why a model's proposal was not turned into an action, or was rejected.
#[derive(Debug, Clone, PartialEq)]
pub enum ParseError {
    UnknownAction(String),
    MissingField(&'static str),
    BadField(&'static str, String),
    UnknownKey(String),
    OutOfBounds(String),
}

impl fmt::Display for ParseError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            ParseError::UnknownAction(a) => write!(f, "unknown action {a:?}"),
            ParseError::MissingField(n) => write!(f, "missing field {n}"),
            ParseError::BadField(n, v) => write!(f, "bad field {n}: {v}"),
            ParseError::UnknownKey(k) => write!(f, "unknown key name {k:?}"),
            ParseError::OutOfBounds(m) => write!(f, "out of bounds: {m}"),
        }
    }
}

impl std::error::Error for ParseError {}

/// Normalizes one key name from either provider.
/// Inputs: `raw` — OpenAI's `keypress.keys` entries (`"CTRL"`, `"ENTER"`,
/// `"ArrowLeft"`) or Claude's xdotool-style names (`"Return"`, `"Page_Up"`,
/// `"super"`); case-insensitive. A single character is kept as it is
/// (lowercased for letters).
pub fn normalize_key(raw: &str) -> Result<Key, ParseError> {
    let mut chars = raw.chars();
    if let (Some(c), None) = (chars.next(), chars.clone().next()) {
        return Ok(Key::Char(c.to_ascii_lowercase()));
    }
    let lower = raw.trim().to_ascii_lowercase().replace(['_', '-', ' '], "");
    let key = match lower.as_str() {
        "ctrl" | "control" | "controlleft" | "controlright" | "lctrl" | "rctrl" | "controll"
        | "controlr" => Key::Ctrl,
        "shift" | "shiftleft" | "shiftright" | "shiftl" | "shiftr" | "lshift" | "rshift" => {
            Key::Shift
        }
        "alt" | "option" | "opt" | "altleft" | "altright" | "altl" | "altr" | "menu" | "altgr" => {
            Key::Alt
        }
        "meta" | "super" | "superl" | "superr" | "win" | "windows" | "cmd" | "command" | "os"
        | "metaleft" | "metaright" | "metal" | "metar" => Key::Meta,
        "enter" | "return" | "kpenter" => Key::Named("enter"),
        "tab" => Key::Named("tab"),
        "esc" | "escape" => Key::Named("escape"),
        "backspace" => Key::Named("backspace"),
        "delete" | "del" => Key::Named("delete"),
        "insert" | "ins" => Key::Named("insert"),
        "space" | "spacebar" => Key::Named("space"),
        "up" | "arrowup" => Key::Named("up"),
        "down" | "arrowdown" => Key::Named("down"),
        "left" | "arrowleft" => Key::Named("left"),
        "right" | "arrowright" => Key::Named("right"),
        "home" => Key::Named("home"),
        "end" => Key::Named("end"),
        "pageup" | "pgup" | "prior" => Key::Named("page-up"),
        "pagedown" | "pgdn" | "next" => Key::Named("page-down"),
        "capslock" => Key::Named("caps-lock"),
        "printscreen" | "print" => Key::Named("print-screen"),
        "plus" => Key::Char('+'),
        "minus" => Key::Char('-'),
        "equal" | "equals" => Key::Char('='),
        "comma" => Key::Char(','),
        "period" => Key::Char('.'),
        "slash" => Key::Char('/'),
        "backslash" => Key::Char('\\'),
        "semicolon" => Key::Char(';'),
        "apostrophe" | "quote" => Key::Char('\''),
        "grave" | "backquote" => Key::Char('`'),
        "bracketleft" => Key::Char('['),
        "bracketright" => Key::Char(']'),
        f if f.len() >= 2
            && f.starts_with('f')
            && f[1..].parse::<u8>().is_ok_and(|n| (1..=24).contains(&n)) =>
        {
            Key::Named(FKEYS[f[1..].parse::<usize>().expect("checked") - 1])
        }
        _ => return Err(ParseError::UnknownKey(raw.to_string())),
    };
    Ok(key)
}

const FKEYS: [&str; 24] = [
    "f1", "f2", "f3", "f4", "f5", "f6", "f7", "f8", "f9", "f10", "f11", "f12", "f13", "f14", "f15",
    "f16", "f17", "f18", "f19", "f20", "f21", "f22", "f23", "f24",
];

/// Splits a `+`-joined combination (`"ctrl+shift+t"`, `"ctrl++"`) into
/// normalized keys.
pub fn parse_combo(text: &str) -> Result<Vec<Key>, ParseError> {
    let text = text.trim();
    if text.is_empty() {
        return Err(ParseError::BadField("key", "empty".into()));
    }
    let mut keys = Vec::new();
    let mut rest = text;
    while !rest.is_empty() {
        // A literal '+' key appears as an empty token ("ctrl++" or "+").
        let (tok, next) = match rest.find('+') {
            Some(0) => ("+", rest[1..].strip_prefix('+').unwrap_or(&rest[1..])),
            Some(i) => (&rest[..i], &rest[i + 1..]),
            None => (rest, ""),
        };
        keys.push(normalize_key(tok)?);
        rest = next;
    }
    Ok(keys)
}

/// Checks an action on the host before anything runs.
/// Inputs: `width`/`height` — the screenshot the model last saw.
/// Returns: why the action is rejected: a coordinate outside that
/// screenshot, or a size past the limits above.
pub fn validate(action: &Action, width: i32, height: i32) -> Result<(), ParseError> {
    let check = |p: &Point| {
        if p.x < 0 || p.y < 0 || p.x >= width || p.y >= height {
            Err(ParseError::OutOfBounds(format!(
                "({}, {}) outside {width}x{height}",
                p.x, p.y
            )))
        } else {
            Ok(())
        }
    };
    match action {
        Action::Zoom {
            region: [x0, y0, x1, y1],
        } => {
            if x1 <= x0 || y1 <= y0 {
                return Err(ParseError::BadField(
                    "region",
                    format!("{:?}", [x0, y0, x1, y1]),
                ));
            }
            check(&Point { x: *x0, y: *y0 })?;
            check(&Point {
                x: x1 - 1,
                y: y1 - 1,
            })?;
        }
        Action::Move { to, .. } => check(to)?,
        Action::Click { at: Some(p), .. } => check(p)?,
        Action::Scroll { at, dx, dy, .. } => {
            if let Some(p) = at {
                check(p)?;
            }
            let limit = MAX_NOTCHES * WHEEL_DELTA;
            if dx.abs() > limit || dy.abs() > limit {
                return Err(ParseError::BadField("scroll", format!("{dx}, {dy}")));
            }
        }
        Action::Drag { path, .. } => {
            if path.len() < 2 || path.len() > MAX_PATH {
                return Err(ParseError::BadField(
                    "path",
                    format!("{} points", path.len()),
                ));
            }
            path.iter().try_for_each(check)?;
        }
        Action::Type { text } if text.is_empty() || text.len() > MAX_TEXT => {
            return Err(ParseError::BadField(
                "text",
                format!("{} bytes", text.len()),
            ));
        }
        Action::KeyCombo { repeat, .. } if *repeat == 0 || *repeat > MAX_REPEAT => {
            return Err(ParseError::BadField("repeat", repeat.to_string()));
        }
        Action::HoldKey { duration_ms, .. } | Action::Wait { duration_ms }
            if *duration_ms > MAX_DURATION_MS =>
        {
            return Err(ParseError::BadField("duration", duration_ms.to_string()));
        }
        _ => {}
    }
    Ok(())
}
