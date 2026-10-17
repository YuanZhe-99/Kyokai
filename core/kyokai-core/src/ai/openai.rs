//! The OpenAI Responses API with the GA `computer` tool
//! (doc/en-us/upstream.md, "OpenAI computer use").
//!
//! The history is chained on the server (`previous_response_id`); every
//! `computer_call` is answered with the screen after its batch.

use serde_json::{Value, json};

use super::action::{Action, Button, Key, ParseError, Point, WHEEL_DELTA, normalize_key};
use super::{End, Outcome, Shot, Turn};

pub const URL: &str = "https://api.openai.com/v1/responses";
pub const DEFAULT_MODEL: &str = "gpt-6.1-sol";
/// OpenAI's `scroll_x` / `scroll_y` are pixel distances (the reference
/// drivers pass them to `window.scrollBy` / `mouse.wheel`); Kyokai maps
/// 100 px to one wheel notch.
pub const PX_PER_NOTCH: i32 = 100;
/// The `wait` action carries no duration; the reference drivers sleep
/// about 2 s.
pub const WAIT_MS: u32 = 2_000;

/// A task's conversation: what the next request sends.
pub struct Conversation {
    model: String,
    system: String,
    previous: Option<String>,
    input: Vec<Value>,
    /// The last turn's calls: id, number of actions, safety checks.
    pending: Vec<(String, usize, Vec<Value>)>,
}

impl Conversation {
    /// Starts a conversation with the task and the first screenshot.
    pub fn new(model: &str, system: &str, task: &str, shot: &Shot) -> Self {
        Conversation {
            model: model.into(),
            system: system.into(),
            previous: None,
            input: vec![json!({
                "role": "user",
                "content": [
                    {"type": "input_text", "text": task},
                    {"type": "input_image", "image_url": data_url(shot), "detail": "high"},
                ],
            })],
            pending: Vec::new(),
        }
    }

    /// The next request body.
    pub fn body(&self) -> Value {
        let mut body = json!({
            "model": self.model,
            "instructions": self.system,
            "tools": [{"type": "computer"}],
            "truncation": "auto",
            "input": self.input,
        });
        if let Some(p) = &self.previous {
            body["previous_response_id"] = json!(p);
        }
        body
    }

    /// Takes a response into the conversation.
    /// Returns: the turn, its calls' actions in order; `flagged` holds the
    /// safety checks the provider raised, which need the host user's
    /// approval.
    pub fn accept(&mut self, response: &Value) -> Result<Turn, ParseError> {
        let parsed = parse_response(response)?;
        self.previous = Some(parsed.response_id.clone());
        self.input.clear();
        let mut actions = Vec::new();
        let mut flagged = Vec::new();
        self.pending.clear();
        for call in parsed.calls {
            for check in &call.pending_safety_checks {
                flagged.push(
                    check["message"]
                        .as_str()
                        .or(check["code"].as_str())
                        .unwrap_or("")
                        .to_string(),
                );
            }
            self.pending
                .push((call.call_id, call.actions.len(), call.pending_safety_checks));
            actions.extend(call.actions);
        }
        let end = if !actions.is_empty() {
            End::Actions
        } else if let Some(r) = parsed.refusal {
            End::Refused(r)
        } else if let Some(r) = parsed.incomplete_reason {
            End::Incomplete(r)
        } else {
            End::Finished
        };
        Ok(Turn {
            actions,
            flagged,
            text: parsed.text,
            progress: Vec::new(),
            end,
        })
    }

    /// Answers the last turn: one `computer_call_output` per call with the
    /// screen after the batch, and a note naming the actions that did not
    /// run.
    /// Inputs: `acknowledged` — the host user approved the flagged safety
    /// checks.
    pub fn answer(&mut self, outcomes: &[Outcome], shot: &Shot, acknowledged: bool) {
        let mut notes = Vec::new();
        let mut at = 0;
        for (call_id, n, checks) in self.pending.drain(..) {
            for (i, o) in outcomes.iter().enumerate().skip(at).take(n) {
                match o {
                    Outcome::Failed(why) => notes.push(format!("Action {} failed: {why}", i + 1)),
                    Outcome::NotExecuted => {
                        notes.push(format!("Action {} was not executed.", i + 1))
                    }
                    Outcome::Paused => notes.push(format!(
                        "Action {} was not executed: the host user paused the task.",
                        i + 1
                    )),
                    Outcome::Text(t) => notes.push(format!("Action {}: {t}", i + 1)),
                    Outcome::Ok | Outcome::Image(_) => {}
                }
            }
            at += n;
            let mut item = json!({
                "type": "computer_call_output",
                "call_id": call_id,
                "output": {"type": "computer_screenshot", "image_url": data_url(shot)},
            });
            if acknowledged && !checks.is_empty() {
                item["acknowledged_safety_checks"] = Value::Array(checks);
            }
            self.input.push(item);
        }
        if !notes.is_empty() {
            self.input.push(json!({
                "role": "user",
                "content": [{"type": "input_text", "text": notes.join("\n")}],
            }));
        }
    }

    /// Continues a finished conversation with the user's next message.
    pub fn follow_up(&mut self, text: &str, shot: &Shot) {
        self.input.push(json!({
            "role": "user",
            "content": [
                {"type": "input_text", "text": text},
                {"type": "input_image", "image_url": data_url(shot), "detail": "high"},
            ],
        }));
    }
}

fn data_url(shot: &Shot) -> String {
    format!("data:image/png;base64,{}", shot.b64)
}

/// One `computer_call` output item.
#[derive(Debug, Clone)]
pub struct ComputerCall {
    pub call_id: String,
    /// The batched `actions`, or the legacy single `action`, in order.
    pub actions: Vec<Result<Action, ParseError>>,
    pub pending_safety_checks: Vec<Value>,
}

/// A parsed response.
#[derive(Debug, Clone)]
pub struct Response {
    pub response_id: String,
    pub calls: Vec<ComputerCall>,
    pub text: String,
    pub refusal: Option<String>,
    pub incomplete_reason: Option<String>,
}

/// Parses a Responses API response at the provider boundary.
pub fn parse_response(v: &Value) -> Result<Response, ParseError> {
    let mut turn = Response {
        response_id: v["id"]
            .as_str()
            .ok_or(ParseError::MissingField("id"))?
            .to_string(),
        calls: Vec::new(),
        text: String::new(),
        refusal: None,
        incomplete_reason: v["incomplete_details"]["reason"]
            .as_str()
            .map(str::to_string),
    };
    let output = v["output"]
        .as_array()
        .ok_or(ParseError::MissingField("output"))?;
    for item in output {
        match item["type"].as_str() {
            Some("computer_call") => turn.calls.push(parse_call(item)?),
            Some("message") => {
                for part in item["content"].as_array().into_iter().flatten() {
                    if let Some(t) = part["text"].as_str() {
                        turn.text.push_str(t);
                    }
                    if let Some(r) = part["refusal"].as_str() {
                        turn.refusal = Some(r.to_string());
                    }
                }
            }
            _ => {}
        }
    }
    Ok(turn)
}

/// Parses one `computer_call` item.
pub fn parse_call(item: &Value) -> Result<ComputerCall, ParseError> {
    let call_id = item["call_id"]
        .as_str()
        .ok_or(ParseError::MissingField("call_id"))?
        .to_string();
    let raw: Vec<&Value> = match (item["actions"].as_array(), item.get("action")) {
        (Some(list), _) if !list.is_empty() => list.iter().collect(),
        (_, Some(a)) if !a.is_null() => vec![a],
        _ => Vec::new(),
    };
    Ok(ComputerCall {
        call_id,
        actions: raw.into_iter().map(parse_action).collect(),
        pending_safety_checks: item["pending_safety_checks"]
            .as_array()
            .cloned()
            .unwrap_or_default(),
    })
}

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
