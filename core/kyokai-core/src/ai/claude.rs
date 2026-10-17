//! The Claude Messages API with the `computer_toolset_20260801` client
//! toolset (doc/en-us/upstream.md, "Claude computer use").
//!
//! The history is append-only: the assistant content goes back byte for
//! byte, old screenshots are cleared by the server (context editing), never
//! by Kyokai.

use serde_json::{Value, json};

use super::action::{Action, Button, Key, ParseError, Point, WHEEL_DELTA, parse_combo};
use super::{End, Outcome, Shot, Turn};

pub const BASE_URL: &str = "https://api.anthropic.com/v1";
pub const VERSION: &str = "2023-06-01";
pub const DEFAULT_MODEL: &str = "claude-opus-5-5";
/// Progress notes, server-side fallbacks and tool-result clearing.
pub const BETAS: &str = "thinking-display-updates-2026-08-18,server-side-fallback-2026-07-01,context-management-2025-06-27";
pub const TOOLSET: &str = "computer";
pub const NOT_EXECUTED: &str = "Not executed: an earlier computer action in this turn failed.";
pub const PAUSED: &str =
    "Not executed: the host user paused the task. Take a screenshot before continuing.";

/// A task's conversation: the whole request, grown turn by turn.
pub struct Conversation {
    request: Value,
    /// The `tool_use` ids of the last turn, one per action.
    pending: Vec<String>,
}

impl Conversation {
    /// Starts a conversation with the task and the first screenshot.
    pub fn new(model: &str, system: &str, task: &str, shot: &Shot) -> Self {
        Conversation {
            request: json!({
                "model": model,
                "max_tokens": 16000,
                "thinking": {"type": "adaptive", "display": "updates"},
                "output_config": {"effort": "high"},
                "fallbacks": "default",
                "system": system,
                "tools": [{"type": "computer_toolset_20260801"}],
                // Old screenshots go once the conversation is long; the
                // newest few stay.
                "context_management": {"edits": [{
                    "type": "clear_tool_uses_20250919",
                    "trigger": {"type": "input_tokens", "value": 100000},
                    "keep": {"type": "tool_uses", "value": 5},
                }]},
                "messages": [{
                    "role": "user",
                    "content": [image_block(&shot.b64), {"type": "text", "text": task}],
                }],
            }),
            pending: Vec::new(),
        }
    }

    /// The next request body.
    pub fn body(&self) -> &Value {
        &self.request
    }

    /// Takes a response into the history.
    /// Returns: the turn, its actions in the order of its `tool_use` blocks.
    pub fn accept(&mut self, response: &Value) -> Result<Turn, ParseError> {
        let parsed = parse_response(response)?;
        self.messages()
            .push(json!({"role": "assistant", "content": response["content"].clone()}));
        self.pending = parsed.calls.iter().map(|c| c.id.clone()).collect();
        let end = match parsed.stop_reason.as_str() {
            _ if !parsed.calls.is_empty() => End::Actions,
            "refusal" => End::Refused(
                parsed
                    .stop_details
                    .as_ref()
                    .and_then(|d| d["explanation"].as_str())
                    .unwrap_or("")
                    .to_string(),
            ),
            "max_tokens" | "model_context_window_exceeded" => {
                End::Incomplete(parsed.stop_reason.clone())
            }
            _ => End::Finished,
        };
        Ok(Turn {
            actions: parsed.calls.into_iter().map(|c| c.action).collect(),
            flagged: Vec::new(),
            text: parsed.text,
            progress: parsed.progress,
            end,
        })
    }

    /// Answers the last turn: one `tool_result` per action, in order.
    pub fn answer(&mut self, outcomes: &[Outcome]) {
        let results: Vec<Value> = self
            .pending
            .drain(..)
            .zip(outcomes)
            .map(|(id, outcome)| tool_result(&id, outcome))
            .collect();
        self.messages()
            .push(json!({"role": "user", "content": results}));
    }

    /// Continues a finished conversation with the user's next message.
    pub fn follow_up(&mut self, text: &str, shot: &Shot) {
        self.messages().push(json!({
            "role": "user",
            "content": [image_block(&shot.b64), {"type": "text", "text": text}],
        }));
    }

    fn messages(&mut self) -> &mut Vec<Value> {
        self.request["messages"]
            .as_array_mut()
            .expect("the request has messages")
    }
}

/// An image content block.
pub fn image_block(b64: &str) -> Value {
    json!({"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": b64}})
}

/// One `tool_result` for an action's outcome; images only for screenshots
/// and zooms.
fn tool_result(id: &str, outcome: &Outcome) -> Value {
    let (content, error) = match outcome {
        Outcome::Ok => (json!([{"type": "text", "text": "OK"}]), false),
        Outcome::Image(shot) => (json!([image_block(&shot.b64)]), false),
        Outcome::Text(t) => (json!([{"type": "text", "text": t}]), false),
        Outcome::Failed(why) => (json!([{"type": "text", "text": why}]), true),
        Outcome::NotExecuted => (json!(NOT_EXECUTED), true),
        Outcome::Paused => (json!(PAUSED), true),
    };
    let mut r = json!({"type": "tool_result", "tool_use_id": id, "toolset_name": TOOLSET, "content": content});
    if error {
        r["is_error"] = json!(true);
    }
    r
}

/// One computer `tool_use` block.
#[derive(Debug, Clone)]
pub struct MemberCall {
    pub id: String,
    pub action: Result<Action, ParseError>,
}

/// A parsed response.
#[derive(Debug, Clone)]
pub struct Response {
    pub stop_reason: String,
    pub stop_details: Option<Value>,
    pub calls: Vec<MemberCall>,
    /// Non-empty `thinking` blocks (progress notes under `display: "updates"`).
    pub progress: Vec<String>,
    pub text: String,
}

/// Parses a Messages API response at the provider boundary.
pub fn parse_response(v: &Value) -> Result<Response, ParseError> {
    let mut turn = Response {
        stop_reason: v["stop_reason"].as_str().unwrap_or("").to_string(),
        stop_details: v.get("stop_details").filter(|d| !d.is_null()).cloned(),
        calls: Vec::new(),
        progress: Vec::new(),
        text: String::new(),
    };
    let content = v["content"]
        .as_array()
        .ok_or(ParseError::MissingField("content"))?;
    for block in content {
        match block["type"].as_str() {
            Some("tool_use") if block["toolset_name"] == TOOLSET => {
                let name = block["name"]
                    .as_str()
                    .ok_or(ParseError::MissingField("name"))?;
                turn.calls.push(MemberCall {
                    id: block["id"]
                        .as_str()
                        .ok_or(ParseError::MissingField("id"))?
                        .to_string(),
                    action: parse_member(name, &block["input"]),
                });
            }
            Some("thinking") => {
                if let Some(t) = block["thinking"].as_str().filter(|t| !t.is_empty()) {
                    turn.progress.push(t.to_string());
                }
            }
            Some("text") => turn.text.push_str(block["text"].as_str().unwrap_or("")),
            _ => {}
        }
    }
    Ok(turn)
}

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
