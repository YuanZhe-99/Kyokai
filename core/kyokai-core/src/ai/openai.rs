//! OpenAI's computer actions as the engine reads them (doc/en-us/upstream.md,
//! "OpenAI computer use"): the agent sidecar talks to the Responses API with
//! the GA `computer` tool (sidecar/src/conversation.ts) and hands each action
//! of a `computer_call` to the engine (`act` with `name: "openai"`), which
//! parses it here into Kyokai's action.

use serde_json::Value;

use super::action::{Action, Button, Key, ParseError, Point, WHEEL_DELTA, normalize_key};

pub const BASE_URL: &str = "https://api.openai.com/v1";
pub const DEFAULT_MODEL: &str = "gpt-6.1-sol";
/// OpenAI's `scroll_x` / `scroll_y` are pixel distances (the reference
/// drivers pass them to `window.scrollBy` / `mouse.wheel`); Kyokai maps
/// 100 px to one wheel notch.
pub const PX_PER_NOTCH: i32 = 100;
/// The `wait` action carries no duration; the reference drivers sleep
/// about 2 s.
pub const WAIT_MS: u32 = 2_000;
fn int(v: &Value, name: &'static str) -> Result<i32, ParseError> {
    let n = v.get(name).ok_or(ParseError::MissingField(name))?;
    n.as_i64()
        .and_then(|n| i32::try_from(n).ok())
        .ok_or_else(|| ParseError::BadField(name, n.to_string()))
}

fn point(v: &Value) -> Result<Point, ParseError> {
    Ok(Point {
        x: int(v, "x")?,
        y: int(v, "y")?,
    })
}

fn keys(v: &Value) -> Result<Vec<Key>, ParseError> {
    match v.get("keys") {
        None | Some(Value::Null) => Ok(Vec::new()),
        Some(Value::Array(list)) => list
            .iter()
            .map(|k| {
                k.as_str()
                    .ok_or_else(|| ParseError::BadField("keys", k.to_string()))
                    .and_then(normalize_key)
            })
            .collect(),
        Some(other) => Err(ParseError::BadField("keys", other.to_string())),
    }
}

/// Turns one OpenAI action into a Kyokai action.
pub fn parse_action(a: &Value) -> Result<Action, ParseError> {
    let kind = a["type"].as_str().ok_or(ParseError::MissingField("type"))?;
    Ok(match kind {
        "screenshot" => Action::Screenshot,
        "click" => {
            let button = match a["button"]
                .as_str()
                .ok_or(ParseError::MissingField("button"))?
            {
                "left" => Button::Left,
                "right" => Button::Right,
                "wheel" => Button::Middle,
                "back" => Button::Back,
                "forward" => Button::Forward,
                other => return Err(ParseError::BadField("button", other.into())),
            };
            Action::Click {
                at: Some(point(a)?),
                button,
                count: 1,
                modifiers: keys(a)?,
            }
        }
        "double_click" => Action::Click {
            at: Some(point(a)?),
            button: Button::Left,
            count: 2,
            modifiers: keys(a)?,
        },
        "move" => Action::Move {
            to: point(a)?,
            held: keys(a)?,
        },
        "drag" => {
            let path = a["path"]
                .as_array()
                .ok_or(ParseError::MissingField("path"))?;
            Action::Drag {
                path: path.iter().map(point).collect::<Result<_, _>>()?,
                modifiers: keys(a)?,
            }
        }
        "scroll" => Action::Scroll {
            at: Some(point(a)?),
            dx: px_to_wheel(int(a, "scroll_x")?),
            dy: px_to_wheel(int(a, "scroll_y")?),
            modifiers: keys(a)?,
        },
        "type" => Action::Type {
            text: a["text"]
                .as_str()
                .ok_or(ParseError::MissingField("text"))?
                .to_string(),
        },
        "keypress" => {
            let ks = keys(a)?;
            if ks.is_empty() {
                return Err(ParseError::MissingField("keys"));
            }
            Action::KeyCombo {
                keys: modifiers_first(ks),
                repeat: 1,
            }
        }
        "wait" => Action::Wait {
            duration_ms: WAIT_MS,
        },
        other => return Err(ParseError::UnknownAction(other.into())),
    })
}

fn px_to_wheel(px: i32) -> i32 {
    px.saturating_mul(WHEEL_DELTA) / PX_PER_NOTCH
}

fn modifiers_first(mut ks: Vec<Key>) -> Vec<Key> {
    ks.sort_by_key(|k| !k.is_modifier());
    ks
}
