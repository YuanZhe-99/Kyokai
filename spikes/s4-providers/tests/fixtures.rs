//! Offline tests: doc-derived fixtures parse into Kyokai actions with per-provider key-name and
//! scroll-unit normalization; request builders produce the documented shapes; live captures
//! (fixtures/live/, written by `cargo run -- --live`) parse too once they exist.

use std::path::{Path, PathBuf};

use serde_json::{json, Value};

use s4_providers::action::{normalize_key, parse_combo, validate, Action, Button, Key, ParseError, Point};
use s4_providers::scrub::Scrubber;
use s4_providers::{claude, openai, screens};

fn dir() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("fixtures")
}

fn load(rel: &str) -> Value {
    let text = std::fs::read_to_string(dir().join(rel)).unwrap_or_else(|e| panic!("{rel}: {e}"));
    serde_json::from_str(&text).unwrap()
}

fn p(x: i32, y: i32) -> Point {
    Point { x, y }
}

#[test]
fn openai_batch_actions() {
    let turn = openai::parse_response(&load("docs/openai_response_batch.json")).unwrap();
    assert_eq!(turn.response_id, "resp_doc_batch");
    assert_eq!(turn.reasoning_items, 1);
    assert_eq!(turn.text, "Typing the search term.");
    assert_eq!(turn.calls.len(), 1);
    let call = &turn.calls[0];
    assert_eq!(call.call_id, "call_002");
    assert!(!call.used_legacy_action);
    let got: Vec<Action> = call.actions.iter().map(|a| a.clone().unwrap()).collect();
    let want = vec![
        Action::Click { at: Some(p(405, 157)), button: Button::Left, count: 1, modifiers: vec![] },
        Action::Type { text: "penguin".into() },
        Action::Click { at: Some(p(10, 20)), button: Button::Middle, count: 1, modifiers: vec![Key::Ctrl, Key::Shift] },
        Action::Click { at: Some(p(30, 40)), button: Button::Left, count: 2, modifiers: vec![] },
        Action::Move { to: p(50, 60), held: vec![Key::Alt] },
        Action::Drag { path: vec![p(100, 200), p(150, 250), p(200, 300)], modifiers: vec![Key::Shift] },
        // 300 px down = 3 notches = 360 wheel units.
        Action::Scroll { at: Some(p(640, 360)), dx: 0, dy: 360, modifiers: vec![] },
        Action::Scroll { at: Some(p(640, 360)), dx: -60, dy: -120, modifiers: vec![] },
        // Modifiers are moved first.
        Action::KeyCombo { keys: vec![Key::Ctrl, Key::Char('a')], repeat: 1 },
        Action::KeyCombo { keys: vec![Key::Named("enter")], repeat: 1 },
        Action::KeyCombo { keys: vec![Key::Named("left")], repeat: 1 },
        Action::KeyCombo { keys: vec![Key::Meta, Key::Named("space")], repeat: 1 },
        Action::Wait { duration_ms: openai::WAIT_MS },
        Action::Screenshot,
    ];
    assert_eq!(got, want);
    for a in &got {
        validate(a, 1280, 720).unwrap();
    }
}

#[test]
fn openai_legacy_action_and_safety_checks() {
    let turn = openai::parse_response(&load("docs/openai_response_legacy.json")).unwrap();
    let call = &turn.calls[0];
    assert!(call.used_legacy_action);
    assert_eq!(
        call.actions,
        vec![Ok(Action::Click { at: Some(p(700, 100)), button: Button::Right, count: 1, modifiers: vec![] })]
    );
    assert_eq!(call.pending_safety_checks.len(), 1);
    assert_eq!(call.pending_safety_checks[0]["code"], "malicious_instructions");

    // Without approval nothing is acknowledged; with approval the checks are echoed back.
    let plain = openai::second_request("m", &turn, "B64", None, false);
    assert!(plain["input"][0].get("acknowledged_safety_checks").is_none());
    let acked = openai::second_request("m", &turn, "B64", None, true);
    assert_eq!(acked["input"][0]["acknowledged_safety_checks"][0]["id"], "cu_sc_1");
}

#[test]
fn openai_request_shapes() {
    let r1 = openai::first_request("gpt-6.1-sol", "do it", "B64", "original");
    assert_eq!(r1["tools"], json!([{"type": "computer"}]));
    assert_eq!(r1["input"][0]["content"][1]["image_url"], "data:image/png;base64,B64");
    let turn = openai::parse_response(&load("docs/openai_response_batch.json")).unwrap();
    let r2 = openai::second_request("gpt-6.1-sol", &turn, "B64", None, false);
    assert_eq!(r2["previous_response_id"], "resp_doc_batch");
    assert_eq!(r2["tools"], json!([{"type": "computer"}]));
    assert_eq!(
        r2["input"],
        json!([{
            "type": "computer_call_output",
            "call_id": "call_002",
            "output": {"type": "computer_screenshot", "image_url": "data:image/png;base64,B64"}
        }])
    );
}

#[test]
fn openai_rejects_unknown_action_and_key() {
    assert_eq!(openai::parse_action(&json!({"type": "teleport"})), Err(ParseError::UnknownAction("teleport".into())));
    assert_eq!(
        openai::parse_action(&json!({"type": "keypress", "keys": ["HYPER"]})),
        Err(ParseError::UnknownKey("HYPER".into()))
    );
    assert!(openai::parse_action(&json!({"type": "click", "button": "left", "x": 1.5, "y": 2})).is_err());
}

#[test]
fn claude_all_members() {
    let turn = claude::parse_response(&load("docs/claude_response_batch.json")).unwrap();
    assert_eq!(turn.stop_reason, "tool_use");
    assert_eq!(turn.progress, vec!["Opening the dialog's save button.".to_string()]);
    assert_eq!(turn.text, "I'll click Save.");
    let got: Vec<Action> = turn.calls.iter().map(|c| c.action.clone().unwrap()).collect();
    let want = vec![
        Action::Screenshot,
        Action::Zoom { region: [100, 200, 400, 350] },
        Action::Click { at: Some(p(500, 300)), button: Button::Left, count: 1, modifiers: vec![Key::Shift] },
        Action::Click { at: Some(p(10, 20)), button: Button::Right, count: 1, modifiers: vec![] },
        Action::Click { at: None, button: Button::Middle, count: 1, modifiers: vec![] },
        Action::Click { at: Some(p(30, 40)), button: Button::Left, count: 2, modifiers: vec![Key::Ctrl, Key::Shift] },
        Action::Click { at: Some(p(50, 60)), button: Button::Left, count: 3, modifiers: vec![] },
        Action::Drag { path: vec![p(200, 300), p(600, 300)], modifiers: vec![] },
        Action::Move { to: p(70, 80), held: vec![] },
        Action::MouseDown { button: Button::Left },
        Action::MouseUp { button: Button::Left },
        Action::CursorPosition,
        // 3 wheel clicks = 360 wheel units.
        Action::Scroll { at: Some(p(500, 400)), dx: 0, dy: 360, modifiers: vec![] },
        Action::Scroll { at: None, dx: -240, dy: 0, modifiers: vec![Key::Shift] },
        Action::Type { text: "pictures of cats".into() },
        Action::KeyCombo { keys: vec![Key::Named("tab")], repeat: 4 },
        Action::KeyCombo { keys: vec![Key::Ctrl, Key::Char('s')], repeat: 1 },
        Action::KeyCombo { keys: vec![Key::Alt, Key::Named("tab")], repeat: 1 },
        Action::KeyCombo { keys: vec![Key::Named("enter")], repeat: 1 },
        Action::KeyCombo { keys: vec![Key::Meta, Key::Named("pageup")], repeat: 1 },
        Action::HoldKey { keys: vec![Key::Shift], duration_ms: 1500 },
        Action::Wait { duration_ms: 2000 },
    ];
    assert_eq!(got, want);
    let members: std::collections::BTreeSet<&str> = turn.calls.iter().map(|c| c.name.as_str()).collect();
    assert_eq!(members.len(), 17, "every toolset member is covered");
}

#[test]
fn claude_refusal_and_fallback() {
    let turn = claude::parse_response(&load("docs/claude_response_refusal.json")).unwrap();
    assert_eq!(turn.stop_reason, "refusal");
    assert_eq!(turn.stop_details.as_ref().unwrap()["category"], "cyber");
    assert_eq!(turn.fallback_blocks, 1);
    assert!(turn.fallback_served);
    assert!(turn.calls.is_empty());
}

#[test]
fn claude_request_shapes() {
    let r1 = claude::first_request("claude-opus-5-5", "do it", "B64");
    assert_eq!(r1["tools"], json!([{"type": "computer_toolset_20260801"}]));
    assert_eq!(r1["thinking"], json!({"type": "adaptive", "display": "updates"}));
    assert_eq!(r1["output_config"], json!({"effort": "high"}));
    assert_eq!(r1["fallbacks"], "default");
    assert!(r1.get("tool_choice").is_none(), "forced tool_choice 400s on Opus 5.5");
}

#[test]
fn claude_results_echo_toolset_and_halt_after_failure() {
    let resp = json!({
        "id": "msg_x", "model": "claude-opus-5-5", "stop_reason": "tool_use",
        "content": [
            {"type": "thinking", "thinking": "", "signature": "sig"},
            {"type": "tool_use", "id": "a", "name": "left_click", "toolset_name": "computer", "input": {"coordinate": [640, 425]}},
            {"type": "tool_use", "id": "b", "name": "zoom", "toolset_name": "computer", "input": {"region": [560, 400, 720, 450]}},
            {"type": "tool_use", "id": "c", "name": "screenshot", "toolset_name": "computer", "input": {}},
            {"type": "tool_use", "id": "d", "name": "left_click", "toolset_name": "computer", "input": {"coordinate": [5000, 10]}},
            {"type": "tool_use", "id": "e", "name": "screenshot", "toolset_name": "computer", "input": {}}
        ]
    });
    let turn = claude::parse_response(&resp).unwrap();
    let zoom = |r: [i32; 4]| format!("ZOOM{r:?}");
    let check = |a: &Action| validate(a, 1280, 720);
    let results = claude::results_for(&turn, "AFTER", &zoom, &check);
    assert_eq!(results.len(), 5);
    for r in &results {
        assert_eq!(r["toolset_name"], "computer");
        assert_eq!(r["type"], "tool_result");
    }
    assert_eq!(results[0]["content"][0]["text"], "OK");
    assert_eq!(results[1]["content"][0]["source"]["data"], "ZOOM[560, 400, 720, 450]");
    assert_eq!(results[2]["content"][0]["source"]["data"], "AFTER");
    assert_eq!(results[3]["is_error"], true);
    assert_eq!(results[4]["is_error"], true);
    assert_eq!(results[4]["content"], claude::NOT_EXECUTED);

    // Turn 2 keeps the assistant content byte-for-byte (thinking signature included).
    let first = claude::first_request("claude-opus-5-5", "t", "B64");
    let second = claude::second_request(&first, &resp, results);
    let msgs = second["messages"].as_array().unwrap();
    assert_eq!(msgs.len(), 3);
    assert_eq!(msgs[0], first["messages"][0]);
    assert_eq!(msgs[1]["role"], "assistant");
    assert_eq!(msgs[1]["content"], resp["content"]);
    assert_eq!(msgs[2]["content"].as_array().unwrap().len(), 5);
}

#[test]
fn claude_rejects_bad_members() {
    assert_eq!(claude::parse_member("teleport", &json!({})), Err(ParseError::UnknownAction("teleport".into())));
    assert!(claude::parse_member("left_click", &json!({"text": "a"})).is_err(), "non-modifier in click text");
    assert!(claude::parse_member("wait", &json!({"duration": 301})).is_err());
    assert!(claude::parse_member("scroll", &json!({"scroll_direction": "sideways", "scroll_amount": 1})).is_err());
    assert!(validate(&claude::parse_member("key", &json!({"text": "a", "repeat": 101})).unwrap(), 1280, 720).is_err());
    // Non-computer tool_use blocks are not treated as computer actions.
    let t = claude::parse_response(&json!({"id": "m", "content": [
        {"type": "tool_use", "id": "x", "name": "screenshot", "input": {}}
    ]}))
    .unwrap();
    assert!(t.calls.is_empty());
    assert_eq!(t.other_tool_uses, vec!["screenshot".to_string()]);
}

#[test]
fn key_normalization_across_providers() {
    // Same key, both providers' spellings.
    for (a, b) in [("ENTER", "Return"), ("ESC", "Escape"), ("CTRL", "ctrl"), ("CMD", "super"), ("PAGEUP", "Page_Up"),
        ("BACKSPACE", "BackSpace"), ("ArrowDown", "Down"), ("OPTION", "alt"), ("SPACE", "space"), ("F5", "F5")]
    {
        assert_eq!(normalize_key(a).unwrap(), normalize_key(b).unwrap(), "{a} vs {b}");
    }
    assert_eq!(parse_combo("ctrl++").unwrap(), vec![Key::Ctrl, Key::Char('+')]);
    assert_eq!(parse_combo("ctrl+plus").unwrap(), vec![Key::Ctrl, Key::Char('+')]);
    assert_eq!(parse_combo("ctrl+shift+T").unwrap(), vec![Key::Ctrl, Key::Shift, Key::Char('t')]);
    assert!(parse_combo("").is_err());
}

#[test]
fn screenshots_and_scrub() {
    let a = screens::screen(false).png();
    let b = screens::screen(true).png();
    assert_ne!(a, b);
    assert_eq!(&a[..8], b"\x89PNG\r\n\x1a\n");
    let (b64a, b64b) = (screens::b64(&a), screens::b64(&b));
    let s = Scrubber { images: vec![(b64a.clone(), "SCREENSHOT_1"), (b64b.clone(), "SCREENSHOT_2")], secrets: vec!["sk-test-secret-123".into()] };
    let v = json!({
        "image_url": format!("data:image/png;base64,{b64a}"),
        "source": {"data": b64b},
        "zoom": screens::b64(&screens::screen(true).crop([0, 0, 64, 64]).png()),
        "signature": "abc",
        "note": "key sk-test-secret-123 leaked"
    });
    let out = s.scrub(&v);
    assert_eq!(out["image_url"], "data:image/png;base64,<SCREENSHOT_1>");
    assert_eq!(out["source"]["data"], "<SCREENSHOT_2>");
    assert_eq!(out["zoom"], "<IMAGE>");
    assert_eq!(out["signature"], "<SIGNATURE>");
    assert_eq!(out["note"], "key <REDACTED> leaked");
}

/// Live captures, once `cargo run -- --live` has run: every saved response must parse, every
/// action must normalize and validate, and no fixture may contain inline image data.
#[test]
fn live_fixtures_parse() {
    let mut seen = 0;
    for provider in ["openai", "claude"] {
        let d = dir().join("live").join(provider);
        let Ok(entries) = std::fs::read_dir(&d) else { continue };
        for e in entries.flatten() {
            let path = e.path();
            let name = path.file_name().unwrap().to_string_lossy().to_string();
            let text = std::fs::read_to_string(&path).unwrap();
            assert!(!text.contains("iVBORw0KGgo"), "{name}: inline PNG left in fixture");
            if !name.ends_with(".response.json") {
                continue;
            }
            seen += 1;
            let v: Value = serde_json::from_str(&text).unwrap();
            let actions: Vec<Result<Action, ParseError>> = if provider == "openai" {
                let t = openai::parse_response(&v).unwrap_or_else(|e| panic!("{provider}/{name}: {e}"));
                t.calls.into_iter().flat_map(|c| c.actions).collect()
            } else {
                let t = claude::parse_response(&v).unwrap_or_else(|e| panic!("{provider}/{name}: {e}"));
                t.calls.into_iter().map(|c| c.action).collect()
            };
            for a in actions {
                let a = a.unwrap_or_else(|e| panic!("{provider}/{name}: {e}"));
                validate(&a, 1280, 720).unwrap_or_else(|e| panic!("{provider}/{name}: {a:?}: {e}"));
            }
        }
    }
    if seen == 0 {
        eprintln!("no live fixtures yet (run `cargo run -- --live`)");
    }
}
