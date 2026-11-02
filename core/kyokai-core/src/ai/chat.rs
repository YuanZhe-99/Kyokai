//! OpenAI-compatible chat completions (doc/en-us/computer-use.md, "Providers"):
//! for providers without a built-in computer tool, Kyokai's actions are
//! offered as function tools named and shaped like Claude's computer
//! toolset members, so the engine checks every answer with one parser
//! (`claude::parse_member`). The agent sidecar sends them (`start.tools`);
//! the conversation itself is the sidecar's (sidecar/src/conversation.ts).

use serde_json::{Value, json};
/// Kyokai's actions as function tools, named and shaped like the members of
/// Claude's computer toolset (positions in the unit the system prompt
/// gives, `Coordinates::describe`).
pub fn tools() -> Value {
    let point = json!({"type": "array", "items": {"type": "integer"}, "minItems": 2, "maxItems": 2,
        "description": "[x, y] on the latest full screenshot, in the unit the instructions give"});
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
                "description": "[x0, y0, x1, y1] on the latest full screenshot, in the unit the instructions give"}}, "required": ["region"]})
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
