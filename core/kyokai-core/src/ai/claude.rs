//! The Claude computer toolset's members as the engine reads them
//! (doc/en-us/upstream.md, "Claude computer use"): the agent sidecar talks
//! to the Messages API (sidecar/src/conversation.ts) and hands each proposed
//! member call to the engine, which parses it here into Kyokai's action.
//! `openai-chat` sources answer in the same shape (`chat::tools`).

use serde_json::Value;

use super::action::{Action, Button, Key, ParseError, Point, WHEEL_DELTA, parse_combo};

pub const BASE_URL: &str = "https://api.anthropic.com/v1";
pub const VERSION: &str = "2023-06-01";
pub const DEFAULT_MODEL: &str = "claude-opus-5-5";
fn coord(input: &Value, name: &'static str) -> Result<Option<Point>, ParseError> {
    match input.get(name) {
        None | Some(Value::Null) => Ok(None),
        Some(Value::Array(xy)) if xy.len() == 2 => {
            let n = |v: &Value| v.as_i64().and_then(|n| i32::try_from(n).ok());
            match (n(&xy[0]), n(&xy[1])) {
                (Some(x), Some(y)) => Ok(Some(Point { x, y })),
                _ => Err(ParseError::BadField(name, input[name].to_string())),
            }
        }
        Some(other) => Err(ParseError::BadField(name, other.to_string())),
    }
}

fn req_coord(input: &Value, name: &'static str) -> Result<Point, ParseError> {
    coord(input, name)?.ok_or(ParseError::MissingField(name))
}

fn text(input: &Value) -> Result<&str, ParseError> {
    input["text"]
        .as_str()
        .ok_or(ParseError::MissingField("text"))
}

/// The optional `text` holding modifiers during a click, drag or scroll,
/// such as `"ctrl+shift"`.
fn modifiers(input: &Value) -> Result<Vec<Key>, ParseError> {
    match input["text"].as_str() {
        None | Some("") => Ok(Vec::new()),
        Some(t) => {
            let ks = parse_combo(t)?;
            if let Some(k) = ks.iter().find(|k| !k.is_modifier()) {
                return Err(ParseError::BadField(
                    "text",
                    format!("{k} is not a modifier"),
                ));
            }
            Ok(ks)
        }
    }
}

fn seconds_ms(input: &Value) -> Result<u32, ParseError> {
    let s = input["duration"]
        .as_f64()
        .ok_or(ParseError::MissingField("duration"))?;
    if !(0.0..=300.0).contains(&s) {
        return Err(ParseError::BadField("duration", s.to_string()));
    }
    Ok((s * 1000.0).round() as u32)
}

/// Turns one toolset member call into an action.
pub fn parse_member(name: &str, input: &Value) -> Result<Action, ParseError> {
    let click = |button, count| -> Result<Action, ParseError> {
        Ok(Action::Click {
            at: coord(input, "coordinate")?,
            button,
            count,
            modifiers: modifiers(input)?,
        })
    };
    Ok(match name {
        "screenshot" => Action::Screenshot,
        "zoom" => {
            let r = input["region"]
                .as_array()
                .ok_or(ParseError::MissingField("region"))?;
            let v: Vec<i32> = r
                .iter()
                .filter_map(|n| n.as_i64().and_then(|n| i32::try_from(n).ok()))
                .collect();
            let region: [i32; 4] = v
                .try_into()
                .map_err(|_| ParseError::BadField("region", input["region"].to_string()))?;
            Action::Zoom { region }
        }
        "left_click" => click(Button::Left, 1)?,
        "right_click" => click(Button::Right, 1)?,
        "middle_click" => click(Button::Middle, 1)?,
        "double_click" => click(Button::Left, 2)?,
        "triple_click" => click(Button::Left, 3)?,
        "left_click_drag" => Action::Drag {
            path: vec![
                req_coord(input, "start_coordinate")?,
                req_coord(input, "coordinate")?,
            ],
            modifiers: modifiers(input)?,
        },
        "mouse_move" => Action::Move {
            to: req_coord(input, "coordinate")?,
            held: Vec::new(),
        },
        "left_mouse_down" => Action::MouseDown {
            button: Button::Left,
        },
        "left_mouse_up" => Action::MouseUp {
            button: Button::Left,
        },
        "cursor_position" => Action::CursorPosition,
        "scroll" => {
            let amount = input["scroll_amount"]
                .as_i64()
                .ok_or(ParseError::MissingField("scroll_amount"))?;
            let amount = i32::try_from(amount)
                .map_err(|_| ParseError::BadField("scroll_amount", amount.to_string()))?;
            let d = amount.saturating_mul(WHEEL_DELTA);
            let (dx, dy) = match input["scroll_direction"]
                .as_str()
                .ok_or(ParseError::MissingField("scroll_direction"))?
            {
                "up" => (0, -d),
                "down" => (0, d),
                "left" => (-d, 0),
                "right" => (d, 0),
                other => return Err(ParseError::BadField("scroll_direction", other.into())),
            };
            Action::Scroll {
                at: coord(input, "coordinate")?,
                dx,
                dy,
                modifiers: modifiers(input)?,
            }
        }
        "type" => Action::Type {
            text: text(input)?.to_string(),
        },
        "key" => {
            let repeat = match input.get("repeat") {
                None | Some(Value::Null) => 1,
                Some(r) => r
                    .as_u64()
                    .and_then(|r| u32::try_from(r).ok())
                    .ok_or_else(|| ParseError::BadField("repeat", r.to_string()))?,
            };
            Action::KeyCombo {
                keys: parse_combo(text(input)?)?,
                repeat,
            }
        }
        "hold_key" => Action::HoldKey {
            keys: parse_combo(text(input)?)?,
            duration_ms: seconds_ms(input)?,
        },
        "wait" => Action::Wait {
            duration_ms: seconds_ms(input)?,
        },
        other => return Err(ParseError::UnknownAction(other.into())),
    })
}
