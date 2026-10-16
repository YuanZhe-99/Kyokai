//! Claude Messages API with the `computer_toolset_20260801` client toolset (raw HTTP; shapes
//! from the claude-api skill and platform.claude.com/docs/en/agents-and-tools/tool-use/computer-use-tool).

use serde_json::{json, Value};

use crate::action::{parse_combo, Action, Button, Key, ParseError, Point, WHEEL_DELTA};

pub const URL: &str = "https://api.anthropic.com/v1/messages";
pub const VERSION: &str = "2023-06-01";
pub const DEFAULT_MODEL: &str = "claude-opus-5-5";
pub const BETAS: &str = "thinking-display-updates-2026-08-18,server-side-fallback-2026-07-01";
pub const OAUTH_BETA: &str = "oauth-2025-04-20";
pub const TOOLSET: &str = "computer";
pub const NOT_EXECUTED: &str = "Not executed: an earlier computer action in this turn failed.";

const SYSTEM: &str = "You operate a 1280x720 desktop through the computer tools. End each batch with a screenshot.";

pub fn first_request(model: &str, task: &str, screenshot_b64: &str) -> Value {
    json!({
        "model": model,
        "max_tokens": 16000,
        "thinking": {"type": "adaptive", "display": "updates"},
        "output_config": {"effort": "high"},
        "fallbacks": "default",
        "system": SYSTEM,
        "tools": [{"type": "computer_toolset_20260801"}],
        "messages": [user_task(task, screenshot_b64)]
    })
}

pub fn user_task(task: &str, screenshot_b64: &str) -> Value {
    json!({
        "role": "user",
        "content": [
            image_block(screenshot_b64),
            {"type": "text", "text": task}
        ]
    })
}

pub fn image_block(b64: &str) -> Value {
    json!({"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": b64}})
}

/// Turn 2 = turn-1 request + the assistant content unchanged + one `tool_result` per member call.
/// `results` must come from [`results_for`] (same order as the `tool_use` blocks).
pub fn second_request(first: &Value, response: &Value, results: Vec<Value>) -> Value {
    let mut req = first.clone();
    let messages = req["messages"].as_array_mut().expect("messages");
    messages.push(json!({"role": "assistant", "content": response["content"].clone()}));
    messages.push(json!({"role": "user", "content": results}));
    req
}

/// What the host would send back for this batch, without executing anything: runs in order,
/// stops at the first invalid action, answers the rest with the documented halt text.
/// `screenshot` returns `after_png_b64`; `zoom` returns `zoom_png_b64(region)`.
pub fn results_for(
    turn: &Turn,
    after_png_b64: &str,
    zoom_png_b64: &dyn Fn([i32; 4]) -> String,
    validate: &dyn Fn(&Action) -> Result<(), ParseError>,
) -> Vec<Value> {
    let mut failed = false;
    let mut out = Vec::new();
    for call in &turn.calls {
        let result = |content: Value, is_error: bool| {
            let mut r = json!({"type": "tool_result", "tool_use_id": call.id, "toolset_name": TOOLSET, "content": content});
            if is_error {
                r["is_error"] = json!(true);
            }
            r
        };
        if failed {
            out.push(result(json!(NOT_EXECUTED), true));
            continue;
        }
        match call.action.as_ref().map_err(Clone::clone).and_then(|a| validate(a).map(|_| a)) {
            Err(e) => {
                failed = true;
                out.push(result(json!([{"type": "text", "text": format!("Rejected by host: {e}")}]), true));
            }
            Ok(Action::Screenshot) => out.push(result(json!([image_block(after_png_b64)]), false)),
            Ok(Action::Zoom { region }) => out.push(result(json!([image_block(&zoom_png_b64(*region))]), false)),
            Ok(Action::CursorPosition) => out.push(result(json!([{"type": "text", "text": "X=640, Y=425"}]), false)),
            Ok(_) => out.push(result(json!([{"type": "text", "text": "OK"}]), false)),
        }
    }
    out
}

#[derive(Debug, Clone)]
pub struct MemberCall {
    pub id: String,
    pub name: String,
    pub action: Result<Action, ParseError>,
}

#[derive(Debug, Clone)]
pub struct Turn {
    pub id: String,
    pub model: String,
    pub stop_reason: String,
    pub stop_details: Option<Value>,
    pub calls: Vec<MemberCall>,
    /// Non-empty `thinking` blocks (progress updates under `display: "updates"`).
    pub progress: Vec<String>,
    pub text: String,
    /// Non-computer `tool_use` blocks (would need their own dispatcher).
    pub other_tool_uses: Vec<String>,
    pub fallback_blocks: usize,
    pub fallback_served: bool,
}

pub fn parse_response(v: &Value) -> Result<Turn, ParseError> {
    let mut turn = Turn {
        id: v["id"].as_str().ok_or(ParseError::MissingField("id"))?.to_string(),
        model: v["model"].as_str().unwrap_or("").to_string(),
        stop_reason: v["stop_reason"].as_str().unwrap_or("").to_string(),
        stop_details: v.get("stop_details").filter(|d| !d.is_null()).cloned(),
        calls: Vec::new(),
        progress: Vec::new(),
        text: String::new(),
        other_tool_uses: Vec::new(),
        fallback_blocks: 0,
        fallback_served: v["usage"]["iterations"]
            .as_array()
            .map_or(false, |its| its.iter().any(|i| i["type"] == "fallback_message")),
    };
    let content = v["content"].as_array().ok_or(ParseError::MissingField("content"))?;
    for block in content {
        match block["type"].as_str() {
            Some("tool_use") if block["toolset_name"] == TOOLSET => {
                let name = block["name"].as_str().ok_or(ParseError::MissingField("name"))?.to_string();
                turn.calls.push(MemberCall {
                    id: block["id"].as_str().ok_or(ParseError::MissingField("id"))?.to_string(),
                    action: parse_member(&name, &block["input"]),
                    name,
                });
            }
            Some("tool_use") => turn.other_tool_uses.push(block["name"].as_str().unwrap_or("").to_string()),
            Some("thinking") => {
                if let Some(t) = block["thinking"].as_str().filter(|t| !t.is_empty()) {
                    turn.progress.push(t.to_string());
                }
            }
            Some("text") => turn.text.push_str(block["text"].as_str().unwrap_or("")),
            Some("fallback") => turn.fallback_blocks += 1,
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

fn text<'a>(input: &'a Value) -> Result<&'a str, ParseError> {
    input["text"].as_str().ok_or(ParseError::MissingField("text"))
}

/// Optional `text` holding modifiers during a click/drag/scroll, e.g. `"ctrl+shift"`.
fn modifiers(input: &Value) -> Result<Vec<Key>, ParseError> {
    match input["text"].as_str() {
        None | Some("") => Ok(Vec::new()),
        Some(t) => {
            let ks = parse_combo(t)?;
            if let Some(k) = ks.iter().find(|k| !k.is_modifier()) {
                return Err(ParseError::BadField("text", format!("{k} is not a modifier")));
            }
            Ok(ks)
        }
    }
}

fn seconds_ms(input: &Value) -> Result<u32, ParseError> {
    let s = input["duration"].as_f64().ok_or(ParseError::MissingField("duration"))?;
    if !(0.0..=300.0).contains(&s) {
        return Err(ParseError::BadField("duration", s.to_string()));
    }
    Ok((s * 1000.0).round() as u32)
}

pub fn parse_member(name: &str, input: &Value) -> Result<Action, ParseError> {
    let click = |button, count| -> Result<Action, ParseError> {
        Ok(Action::Click { at: coord(input, "coordinate")?, button, count, modifiers: modifiers(input)? })
    };
    Ok(match name {
        "screenshot" => Action::Screenshot,
        "zoom" => {
            let r = input["region"].as_array().ok_or(ParseError::MissingField("region"))?;
            let v: Vec<i32> = r.iter().filter_map(|n| n.as_i64().and_then(|n| i32::try_from(n).ok())).collect();
            let region: [i32; 4] = v.try_into().map_err(|_| ParseError::BadField("region", input["region"].to_string()))?;
            Action::Zoom { region }
        }
        "left_click" => click(Button::Left, 1)?,
        "right_click" => click(Button::Right, 1)?,
        "middle_click" => click(Button::Middle, 1)?,
        "double_click" => click(Button::Left, 2)?,
        "triple_click" => click(Button::Left, 3)?,
        "left_click_drag" => Action::Drag {
            path: vec![req_coord(input, "start_coordinate")?, req_coord(input, "coordinate")?],
            modifiers: modifiers(input)?,
        },
        "mouse_move" => Action::Move { to: req_coord(input, "coordinate")?, held: Vec::new() },
        "left_mouse_down" => Action::MouseDown { button: Button::Left },
        "left_mouse_up" => Action::MouseUp { button: Button::Left },
        "cursor_position" => Action::CursorPosition,
        "scroll" => {
            let amount = input["scroll_amount"].as_i64().ok_or(ParseError::MissingField("scroll_amount"))?;
            let amount = i32::try_from(amount).map_err(|_| ParseError::BadField("scroll_amount", amount.to_string()))?;
            let d = amount.saturating_mul(WHEEL_DELTA);
            let (dx, dy) = match input["scroll_direction"].as_str().ok_or(ParseError::MissingField("scroll_direction"))? {
                "up" => (0, -d),
                "down" => (0, d),
                "left" => (-d, 0),
                "right" => (d, 0),
                other => return Err(ParseError::BadField("scroll_direction", other.into())),
            };
            Action::Scroll { at: coord(input, "coordinate")?, dx, dy, modifiers: modifiers(input)? }
        }
        "type" => Action::Type { text: text(input)?.to_string() },
        "key" => {
            let repeat = match input.get("repeat") {
                None | Some(Value::Null) => 1,
                Some(r) => r.as_u64().and_then(|r| u32::try_from(r).ok()).ok_or_else(|| ParseError::BadField("repeat", r.to_string()))?,
            };
            Action::KeyCombo { keys: parse_combo(text(input)?)?, repeat }
        }
        "hold_key" => Action::HoldKey { keys: parse_combo(text(input)?)?, duration_ms: seconds_ms(input)? },
        "wait" => Action::Wait { duration_ms: seconds_ms(input)? },
        other => return Err(ParseError::UnknownAction(other.into())),
    })
}
