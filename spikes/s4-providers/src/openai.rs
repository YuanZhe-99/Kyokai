//! OpenAI Responses API, GA `computer` tool. Field names checked against openai-python
//! (`types/responses/response_computer_tool_call.py`, `computer_action.py`,
//! `computer_tool_param.py`, `response_input_item_param.py`, `lib/computer.py`).

use serde_json::{json, Value};

use crate::action::{normalize_key, Action, Button, Key, ParseError, Point, WHEEL_DELTA};

pub const URL: &str = "https://api.openai.com/v1/responses";
pub const DEFAULT_MODEL: &str = "gpt-6.1-sol";
/// OpenAI's `scroll_x` / `scroll_y` are pixel distances (the reference drivers pass them to
/// `window.scrollBy` / `mouse.wheel`); Kyokai maps 100 px to one wheel notch.
pub const PX_PER_NOTCH: i32 = 100;
/// The `wait` action carries no duration; the reference drivers sleep about 2 s.
pub const WAIT_MS: u32 = 2_000;

pub fn first_request(model: &str, task: &str, screenshot_b64: &str, detail: &str) -> Value {
    json!({
        "model": model,
        "tools": [{"type": "computer"}],
        "input": [{
            "role": "user",
            "content": [
                {"type": "input_text", "text": task},
                {"type": "input_image", "image_url": format!("data:image/png;base64,{screenshot_b64}"), "detail": detail}
            ]
        }]
    })
}

/// Answers every computer call with the same screenshot (the spike executes nothing).
/// Safety checks are acknowledged only when `ack_safety` (in Kyokai: after the user approved).
pub fn second_request(model: &str, previous: &Turn, screenshot_b64: &str, detail: Option<&str>, ack_safety: bool) -> Value {
    let input: Vec<Value> = previous
        .calls
        .iter()
        .map(|c| {
            let mut output = json!({
                "type": "computer_screenshot",
                "image_url": format!("data:image/png;base64,{screenshot_b64}")
            });
            if let Some(d) = detail {
                output["detail"] = json!(d);
            }
            let mut item = json!({"type": "computer_call_output", "call_id": c.call_id, "output": output});
            if ack_safety && !c.pending_safety_checks.is_empty() {
                item["acknowledged_safety_checks"] = Value::Array(c.pending_safety_checks.clone());
            }
            item
        })
        .collect();
    json!({
        "model": model,
        "tools": [{"type": "computer"}],
        "previous_response_id": previous.response_id,
        "input": input
    })
}

#[derive(Debug, Clone)]
pub struct ComputerCall {
    pub call_id: String,
    pub status: String,
    /// Batched `actions`, or the legacy single `action`, in order.
    pub actions: Vec<Result<Action, ParseError>>,
    pub used_legacy_action: bool,
    pub pending_safety_checks: Vec<Value>,
}

#[derive(Debug, Clone)]
pub struct Turn {
    pub response_id: String,
    pub status: String,
    pub calls: Vec<ComputerCall>,
    pub text: String,
    pub reasoning_items: usize,
    pub incomplete_reason: Option<String>,
    pub error: Option<Value>,
}

pub fn parse_response(v: &Value) -> Result<Turn, ParseError> {
    let response_id = v["id"].as_str().ok_or(ParseError::MissingField("id"))?.to_string();
    let status = v["status"].as_str().unwrap_or("").to_string();
    let mut turn = Turn {
        response_id,
        status,
        calls: Vec::new(),
        text: String::new(),
        reasoning_items: 0,
        incomplete_reason: v["incomplete_details"]["reason"].as_str().map(str::to_string),
        error: v.get("error").filter(|e| !e.is_null()).cloned(),
    };
    let output = v["output"].as_array().ok_or(ParseError::MissingField("output"))?;
    for item in output {
        match item["type"].as_str() {
            Some("computer_call") => turn.calls.push(parse_call(item)?),
            Some("reasoning") => turn.reasoning_items += 1,
            Some("message") => {
                for part in item["content"].as_array().into_iter().flatten() {
                    if let Some(t) = part["text"].as_str() {
                        turn.text.push_str(t);
                    }
                    if let Some(r) = part["refusal"].as_str() {
                        turn.text.push_str("[refusal] ");
                        turn.text.push_str(r);
                    }
                }
            }
            _ => {}
        }
    }
    Ok(turn)
}

pub fn parse_call(item: &Value) -> Result<ComputerCall, ParseError> {
    let call_id = item["call_id"].as_str().ok_or(ParseError::MissingField("call_id"))?.to_string();
    let (raw, legacy): (Vec<&Value>, bool) = match (item["actions"].as_array(), item.get("action")) {
        (Some(list), _) if !list.is_empty() => (list.iter().collect(), false),
        (_, Some(a)) if !a.is_null() => (vec![a], true),
        _ => (Vec::new(), false),
    };
    Ok(ComputerCall {
        call_id,
        status: item["status"].as_str().unwrap_or("").to_string(),
        actions: raw.into_iter().map(parse_action).collect(),
        used_legacy_action: legacy,
        pending_safety_checks: item["pending_safety_checks"].as_array().cloned().unwrap_or_default(),
    })
}

fn int(v: &Value, name: &'static str) -> Result<i32, ParseError> {
    let n = v.get(name).ok_or(ParseError::MissingField(name))?;
    n.as_i64()
        .and_then(|n| i32::try_from(n).ok())
        .ok_or_else(|| ParseError::BadField(name, n.to_string()))
}

fn point(v: &Value) -> Result<Point, ParseError> {
    Ok(Point { x: int(v, "x")?, y: int(v, "y")? })
}

fn keys(v: &Value) -> Result<Vec<Key>, ParseError> {
    match v.get("keys") {
        None | Some(Value::Null) => Ok(Vec::new()),
        Some(Value::Array(list)) => list
            .iter()
            .map(|k| k.as_str().ok_or_else(|| ParseError::BadField("keys", k.to_string())).and_then(normalize_key))
            .collect(),
        Some(other) => Err(ParseError::BadField("keys", other.to_string())),
    }
}

pub fn parse_action(a: &Value) -> Result<Action, ParseError> {
    let kind = a["type"].as_str().ok_or(ParseError::MissingField("type"))?;
    Ok(match kind {
        "screenshot" => Action::Screenshot,
        "click" => {
            let button = match a["button"].as_str().ok_or(ParseError::MissingField("button"))? {
                "left" => Button::Left,
                "right" => Button::Right,
                "wheel" => Button::Middle,
                "back" => Button::Back,
                "forward" => Button::Forward,
                other => return Err(ParseError::BadField("button", other.into())),
            };
            Action::Click { at: Some(point(a)?), button, count: 1, modifiers: keys(a)? }
        }
        "double_click" => Action::Click { at: Some(point(a)?), button: Button::Left, count: 2, modifiers: keys(a)? },
        "move" => Action::Move { to: point(a)?, held: keys(a)? },
        "drag" => {
            let path = a["path"].as_array().ok_or(ParseError::MissingField("path"))?;
            Action::Drag { path: path.iter().map(point).collect::<Result<_, _>>()?, modifiers: keys(a)? }
        }
        "scroll" => Action::Scroll {
            at: Some(point(a)?),
            dx: px_to_wheel(int(a, "scroll_x")?),
            dy: px_to_wheel(int(a, "scroll_y")?),
            modifiers: keys(a)?,
        },
        "type" => Action::Type { text: a["text"].as_str().ok_or(ParseError::MissingField("text"))?.to_string() },
        "keypress" => {
            let ks = keys(a)?;
            if ks.is_empty() {
                return Err(ParseError::MissingField("keys"));
            }
            Action::KeyCombo { keys: modifiers_first(ks), repeat: 1 }
        }
        "wait" => Action::Wait { duration_ms: WAIT_MS },
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
