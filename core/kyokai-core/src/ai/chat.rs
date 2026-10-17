//! OpenAI-compatible chat completions (doc/en-us/computer-use.md, "Providers"):
//! for providers without a built-in computer tool. Kyokai's actions are
//! offered as function tools named and shaped like Claude's computer
//! toolset members, so one parser checks them (`claude::parse_member`);
//! screenshots go in user messages as images. The model must accept images
//! and call tools.

use super::claude::parse_member;
use super::{End, Outcome, Shot, Turn};
use crate::ai::action::ParseError;
use serde_json::{Value, json};

/// Screenshots kept in the history; older ones are replaced by a note, so
/// a long task does not resend every picture.
const KEEP_IMAGES: usize = 3;

/// A task's conversation in the chat completions format.
#[derive(Clone)]
pub struct Conversation {
    model: String,
    messages: Vec<Value>,
    /// The tool calls of the last turn, in order, answered next.
    calls: Vec<String>,
}

impl Conversation {
    pub fn new(model: &str, system: &str, task: &str, shot: &Shot) -> Self {
        Conversation {
            model: model.into(),
            messages: vec![
                json!({"role": "system", "content": system}),
                json!({"role": "user", "content": [
                    {"type": "text", "text": task},
                    image(&shot.b64),
                ]}),
            ],
            calls: Vec::new(),
        }
    }

    /// The request: the history with only the newest screenshots.
    pub fn body(&self) -> Value {
        let mut messages = self.messages.clone();
        let mut seen = 0;
        for m in messages.iter_mut().rev() {
            let Some(parts) = m["content"].as_array_mut() else {
                continue;
            };
            for p in parts.iter_mut().rev() {
                if p["type"] == "image_url" {
                    seen += 1;
                    if seen > KEEP_IMAGES {
                        *p = json!({"type": "text", "text": "[an earlier screenshot, omitted]"});
                    }
                }
            }
        }
        json!({
            "model": self.model,
            "messages": messages,
            "tools": tools(),
            "tool_choice": "auto",
        })
    }

    /// Reads one response: the model's text and its tool calls.
    pub fn accept(&mut self, response: &Value) -> Result<Turn, ParseError> {
        let choice = &response["choices"][0];
        let message = &choice["message"];
        if !message.is_object() {
            return Err(ParseError::MissingField("choices[0].message"));
        }
        let text = match &message["content"] {
            Value::String(s) => s.clone(),
            Value::Array(parts) => parts
                .iter()
                .filter_map(|p| p["text"].as_str())
                .collect::<Vec<_>>()
                .join("\n"),
            _ => String::new(),
        };
        let mut actions = Vec::new();
        self.calls.clear();
        for call in message["tool_calls"].as_array().into_iter().flatten() {
            let id = call["id"].as_str().unwrap_or_default().to_string();
            let name = call["function"]["name"].as_str().unwrap_or_default();
            // Arguments are a JSON string in the format, an object in some
            // servers.
            let args = match &call["function"]["arguments"] {
                Value::String(s) if s.trim().is_empty() => json!({}),
                Value::String(s) => {
                    serde_json::from_str(s).unwrap_or_else(|_| json!({"__unreadable": s}))
                }
                v => v.clone(),
            };
            actions.push(if args.get("__unreadable").is_some() {
                Err(ParseError::BadField("arguments", name.into()))
            } else {
                parse_member(name, &args)
            });
            self.calls.push(id);
        }
        // The assistant's message goes back as it came.
        let mut kept = json!({"role": "assistant", "content": text});
        if let Some(calls) = message.get("tool_calls").filter(|c| c.is_array()) {
            kept["tool_calls"] = calls.clone();
        }
        self.messages.push(kept);
        let end = if !actions.is_empty() {
            End::Actions
        } else {
            match choice["finish_reason"].as_str() {
                Some("length") => End::Incomplete("the answer was cut off".into()),
                Some("content_filter") => End::Refused(text.clone()),
                _ => End::Finished,
            }
        };
        Ok(Turn {
            actions,
            flagged: Vec::new(),
            text,
            progress: Vec::new(),
            end,
        })
    }

    /// Answers the last turn's calls, then shows the screen as it is now
    /// (with any zoomed images first).
    pub fn answer(&mut self, outcomes: &[Outcome], shot: &Shot) {
        let mut images = Vec::new();
        for (id, outcome) in self.calls.iter().zip(outcomes) {
            let content = match outcome {
                Outcome::Ok => "done".to_string(),
                Outcome::Image(s) => {
                    images.push(image(&s.b64));
                    "the image follows".to_string()
                }
                Outcome::Text(t) => t.clone(),
                Outcome::Failed(e) => format!("error: {e}"),
                Outcome::NotExecuted => "not executed: an earlier action failed".to_string(),
                Outcome::Paused => "not executed: the user paused the task".to_string(),
            };
            self.messages
                .push(json!({"role": "tool", "tool_call_id": id, "content": content}));
        }
        self.calls.clear();
        images.push(json!({"type": "text", "text": "The screen now:"}));
        images.push(image(&shot.b64));
        self.messages
            .push(json!({"role": "user", "content": images}));
    }

    /// Continues a finished task with the user's reply.
    pub fn follow_up(&mut self, text: &str, shot: &Shot) {
        self.messages.push(json!({"role": "user", "content": [
            {"type": "text", "text": text},
            image(&shot.b64),
        ]}));
    }
}

/// A screenshot as a message part.
fn image(b64: &str) -> Value {
    json!({"type": "image_url", "image_url": {"url": format!("data:image/png;base64,{b64}")}})
}

/// Kyokai's actions as function tools, named and shaped like the members of
/// Claude's computer toolset (coordinates in screenshot pixels).
fn tools() -> Value {
    let point = json!({"type": "array", "items": {"type": "integer"}, "minItems": 2, "maxItems": 2,
        "description": "[x, y] in screenshot pixels"});
    let modifiers = json!({"type": "string", "description": "keys held during the action, such as \"shift\" or \"ctrl+alt\""});
    let at = |what: &str| {
        json!({"type": "object", "properties": {"coordinate": point, "text": modifiers},
            "required": ["coordinate"], "description": what})
    };
    let none = json!({"type": "object", "properties": {}});
    let f = |name: &str, description: &str, parameters: Value| json!({"type": "function", "function": {"name": name, "description": description, "parameters": parameters}});
    json!([
        f(
            "screenshot",
            "Take a screenshot of the screen.",
            none.clone()
        ),
        f(
            "zoom",
            "View a region of the screen at full resolution.",
            json!({"type": "object",
            "properties": {"region": {"type": "array", "items": {"type": "integer"}, "minItems": 4, "maxItems": 4,
                "description": "[x0, y0, x1, y1] in screenshot pixels"}}, "required": ["region"]})
        ),
        f(
            "left_click",
            "Click the left mouse button.",
            at("where to click")
        ),
        f(
            "right_click",
            "Click the right mouse button.",
            at("where to click")
        ),
        f(
            "middle_click",
            "Click the middle mouse button.",
            at("where to click")
        ),
        f(
            "double_click",
            "Double-click the left mouse button.",
            at("where to click")
        ),
        f(
            "triple_click",
            "Triple-click the left mouse button.",
            at("where to click")
        ),
        f(
            "left_click_drag",
            "Drag with the left button held.",
            json!({"type": "object",
            "properties": {"start_coordinate": point, "coordinate": point, "text": modifiers},
            "required": ["start_coordinate", "coordinate"]})
        ),
        f(
            "mouse_move",
            "Move the mouse.",
            json!({"type": "object", "properties": {"coordinate": point},
            "required": ["coordinate"]})
        ),
        f(
            "left_mouse_down",
            "Press the left mouse button.",
            none.clone()
        ),
        f(
            "left_mouse_up",
            "Release the left mouse button.",
            none.clone()
        ),
        f(
            "cursor_position",
            "Report where the mouse pointer is.",
            none.clone()
        ),
        f(
            "scroll",
            "Scroll the mouse wheel.",
            json!({"type": "object", "properties": {
            "coordinate": point,
            "scroll_direction": {"type": "string", "enum": ["up", "down", "left", "right"]},
            "scroll_amount": {"type": "integer", "description": "wheel notches"},
            "text": modifiers},
            "required": ["scroll_direction", "scroll_amount"]})
        ),
        f(
            "type",
            "Type text.",
            json!({"type": "object", "properties": {"text": {"type": "string"}},
            "required": ["text"]})
        ),
        f(
            "key",
            "Press a key or a combination such as \"ctrl+s\" or \"Return\".",
            json!({"type": "object",
            "properties": {"text": {"type": "string"}, "repeat": {"type": "integer"}}, "required": ["text"]})
        ),
        f(
            "hold_key",
            "Hold keys down for a while.",
            json!({"type": "object", "properties": {
            "text": {"type": "string"}, "duration": {"type": "number", "description": "seconds"}},
            "required": ["text", "duration"]})
        ),
        f(
            "wait",
            "Wait, for example for something to load.",
            json!({"type": "object", "properties": {
            "duration": {"type": "number", "description": "seconds"}}, "required": ["duration"]})
        ),
    ])
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::ai::action::{Action, Button, Point};

    fn shot(b64: &str) -> Shot {
        Shot {
            b64: b64.into(),
            width: 1280,
            height: 720,
        }
    }

    #[test]
    fn calls_become_actions_and_their_answers_follow_the_format() {
        let mut c = Conversation::new("vision-model", "sys", "Open the menu", &shot("AAA"));
        let response = json!({"choices": [{"finish_reason": "tool_calls", "message": {
        "role": "assistant", "content": "Clicking the menu.",
        "tool_calls": [
            {"id": "c1", "type": "function", "function": {"name": "left_click", "arguments": "{\"coordinate\": [100, 200]}"}},
            {"id": "c2", "type": "function", "function": {"name": "key", "arguments": {"text": "ctrl+s"}}},
            {"id": "c3", "type": "function", "function": {"name": "teleport", "arguments": "{}"}},
        ]}}]});
        let turn = c.accept(&response).unwrap();
        assert_eq!(turn.end, End::Actions);
        assert_eq!(turn.text, "Clicking the menu.");
        assert_eq!(
            turn.actions[0],
            Ok(Action::Click {
                at: Some(Point { x: 100, y: 200 }),
                button: Button::Left,
                count: 1,
                modifiers: vec![],
            })
        );
        assert!(matches!(turn.actions[1], Ok(Action::KeyCombo { .. })));
        assert!(turn.actions[2].is_err(), "unknown tools are refused");
        c.answer(
            &[
                Outcome::Ok,
                Outcome::Failed("no".into()),
                Outcome::NotExecuted,
            ],
            &shot("BBB"),
        );
        let body = c.body();
        let m = body["messages"].as_array().unwrap();
        let tools: Vec<_> = m.iter().filter(|x| x["role"] == "tool").collect();
        assert_eq!(tools.len(), 3);
        assert_eq!(tools[0]["tool_call_id"], "c1");
        assert_eq!(tools[1]["content"], "error: no");
        assert_eq!(
            m.last().unwrap()["role"],
            "user",
            "the new screen comes last"
        );
        assert!(body["tools"].as_array().unwrap().len() >= 15);
    }

    #[test]
    fn a_reply_without_calls_finishes_and_old_screenshots_are_dropped() {
        let mut c = Conversation::new("m", "sys", "task", &shot("S0"));
        for i in 1..=5 {
            c.follow_up("more", &shot(&format!("S{i}")));
        }
        let body = c.body().to_string();
        assert!(
            !body.contains("S0") && !body.contains("S2"),
            "older pictures leave"
        );
        assert!(body.contains("S3") && body.contains("S5"));
        let done = json!({"choices": [{"finish_reason": "stop", "message": {"role": "assistant", "content": "Done."}}]});
        assert_eq!(c.accept(&done).unwrap().end, End::Finished);
        let cut = json!({"choices": [{"finish_reason": "length", "message": {"role": "assistant", "content": "…"}}]});
        assert!(matches!(c.accept(&cut).unwrap().end, End::Incomplete(_)));
        assert!(c.accept(&json!({"error": {"message": "x"}})).is_err());
    }
}
