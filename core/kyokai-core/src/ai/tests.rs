//! The AI runtime's tests: provider fixtures parse into Kyokai actions,
//! requests have the documented shapes, actions become the right input,
//! and tasks behave by the contract (doc/en-us/computer-use.md, "AI
//! tasks") against a fake desktop and a local stand-in for the provider.

use serde_json::{Value, json};
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};

use super::action::{Action, Button, Key, ParseError, Point, normalize_key, parse_combo, validate};
use super::desktop::{self, Frame, Step};
use super::fake::*;
use super::task::{Pause, State, testing};
use super::{End, Outcome, Shot, claude, openai, screens};
use crate::input::{Event, LocalInput, Mods, PointerAction};

fn dir() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("src/ai/fixtures")
}

fn load(rel: &str) -> Value {
    let text = std::fs::read_to_string(dir().join(rel)).unwrap_or_else(|e| panic!("{rel}: {e}"));
    serde_json::from_str(&text).unwrap()
}

fn p(x: i32, y: i32) -> Point {
    Point { x, y }
}

fn shot(b64: &str) -> Shot {
    Shot {
        b64: b64.into(),
        width: 1280,
        height: 720,
    }
}

#[test]
fn openai_batch_actions() {
    let r = openai::parse_response(&load("docs/openai_response_batch.json")).unwrap();
    assert_eq!(r.response_id, "resp_doc_batch");
    assert_eq!(r.text, "Typing the search term.");
    assert_eq!(r.calls.len(), 1);
    let call = &r.calls[0];
    assert_eq!(call.call_id, "call_002");
    let got: Vec<Action> = call.actions.iter().map(|a| a.clone().unwrap()).collect();
    let want = vec![
        Action::Click {
            at: Some(p(405, 157)),
            button: Button::Left,
            count: 1,
            modifiers: vec![],
        },
        Action::Type {
            text: "penguin".into(),
        },
        Action::Click {
            at: Some(p(10, 20)),
            button: Button::Middle,
            count: 1,
            modifiers: vec![Key::Ctrl, Key::Shift],
        },
        Action::Click {
            at: Some(p(30, 40)),
            button: Button::Left,
            count: 2,
            modifiers: vec![],
        },
        Action::Move {
            to: p(50, 60),
            held: vec![Key::Alt],
        },
        Action::Drag {
            path: vec![p(100, 200), p(150, 250), p(200, 300)],
            modifiers: vec![Key::Shift],
        },
        // 300 px down = 3 notches = 360 wheel units.
        Action::Scroll {
            at: Some(p(640, 360)),
            dx: 0,
            dy: 360,
            modifiers: vec![],
        },
        Action::Scroll {
            at: Some(p(640, 360)),
            dx: -60,
            dy: -120,
            modifiers: vec![],
        },
        // Modifiers are moved first.
        Action::KeyCombo {
            keys: vec![Key::Ctrl, Key::Char('a')],
            repeat: 1,
        },
        Action::KeyCombo {
            keys: vec![Key::Named("enter")],
            repeat: 1,
        },
        Action::KeyCombo {
            keys: vec![Key::Named("left")],
            repeat: 1,
        },
        Action::KeyCombo {
            keys: vec![Key::Meta, Key::Named("space")],
            repeat: 1,
        },
        Action::Wait {
            duration_ms: openai::WAIT_MS,
        },
        Action::Screenshot,
    ];
    assert_eq!(got, want);
    for a in &got {
        validate(a, 1280, 720).unwrap();
    }
}

#[test]
fn openai_safety_checks_are_flagged_and_acknowledged_only_after_approval() {
    let resp = load("docs/openai_response_legacy.json");
    let r = openai::parse_response(&resp).unwrap();
    assert_eq!(
        r.calls[0].actions,
        vec![Ok(Action::Click {
            at: Some(p(700, 100)),
            button: Button::Right,
            count: 1,
            modifiers: vec![]
        })]
    );
    for acknowledged in [false, true] {
        let mut c = openai::Conversation::new("m", "sys", "task", &shot("A"));
        let turn = c.accept(&resp).unwrap();
        assert_eq!(turn.flagged.len(), 1);
        c.answer(&[Outcome::Ok], &shot("B"), acknowledged);
        let body = c.body();
        assert_eq!(body["previous_response_id"], r.response_id);
        let ack = &body["input"][0]["acknowledged_safety_checks"];
        if acknowledged {
            assert_eq!(ack[0]["id"], "cu_sc_1");
        } else {
            assert!(ack.is_null());
        }
    }
}

#[test]
fn openai_request_shapes() {
    let mut c = openai::Conversation::new("gpt-6.1-sol", "sys", "do it", &shot("B64"));
    let r1 = c.body();
    assert_eq!(r1["tools"], json!([{"type": "computer"}]));
    assert_eq!(r1["instructions"], "sys");
    assert_eq!(
        r1["input"][0]["content"][1]["image_url"],
        "data:image/png;base64,B64"
    );
    assert!(r1.get("previous_response_id").is_none());
    let turn = c.accept(&load("docs/openai_response_batch.json")).unwrap();
    assert_eq!(turn.end, End::Actions);
    let mut outcomes = vec![Outcome::Ok; turn.actions.len()];
    outcomes[1] = Outcome::Failed("Rejected by the host: no".into());
    outcomes[2] = Outcome::NotExecuted;
    c.answer(&outcomes, &shot("AFTER"), false);
    let r2 = c.body();
    assert_eq!(r2["previous_response_id"], "resp_doc_batch");
    assert_eq!(r2["instructions"], "sys", "instructions are not chained");
    assert_eq!(
        r2["input"][0],
        json!({
            "type": "computer_call_output",
            "call_id": "call_002",
            "output": {"type": "computer_screenshot", "image_url": "data:image/png;base64,AFTER"}
        })
    );
    let note = r2["input"][1]["content"][0]["text"].as_str().unwrap();
    assert!(
        note.contains("Action 2 failed") && note.contains("Action 3 was not executed"),
        "{note}"
    );
}

#[test]
fn openai_rejects_unknown_action_and_key() {
    assert_eq!(
        openai::parse_action(&json!({"type": "teleport"})),
        Err(ParseError::UnknownAction("teleport".into()))
    );
    assert_eq!(
        openai::parse_action(&json!({"type": "keypress", "keys": ["HYPER"]})),
        Err(ParseError::UnknownKey("HYPER".into()))
    );
    assert!(
        openai::parse_action(&json!({"type": "click", "button": "left", "x": 1.5, "y": 2}))
            .is_err()
    );
}

#[test]
fn claude_all_members() {
    let r = claude::parse_response(&load("docs/claude_response_batch.json")).unwrap();
    assert_eq!(r.stop_reason, "tool_use");
    assert_eq!(
        r.progress,
        vec!["Opening the dialog's save button.".to_string()]
    );
    assert_eq!(r.text, "I'll click Save.");
    let got: Vec<Action> = r.calls.iter().map(|c| c.action.clone().unwrap()).collect();
    let want = vec![
        Action::Screenshot,
        Action::Zoom {
            region: [100, 200, 400, 350],
        },
        Action::Click {
            at: Some(p(500, 300)),
            button: Button::Left,
            count: 1,
            modifiers: vec![Key::Shift],
        },
        Action::Click {
            at: Some(p(10, 20)),
            button: Button::Right,
            count: 1,
            modifiers: vec![],
        },
        Action::Click {
            at: None,
            button: Button::Middle,
            count: 1,
            modifiers: vec![],
        },
        Action::Click {
            at: Some(p(30, 40)),
            button: Button::Left,
            count: 2,
            modifiers: vec![Key::Ctrl, Key::Shift],
        },
        Action::Click {
            at: Some(p(50, 60)),
            button: Button::Left,
            count: 3,
            modifiers: vec![],
        },
        Action::Drag {
            path: vec![p(200, 300), p(600, 300)],
            modifiers: vec![],
        },
        Action::Move {
            to: p(70, 80),
            held: vec![],
        },
        Action::MouseDown {
            button: Button::Left,
        },
        Action::MouseUp {
            button: Button::Left,
        },
        Action::CursorPosition,
        // 3 wheel clicks = 360 wheel units.
        Action::Scroll {
            at: Some(p(500, 400)),
            dx: 0,
            dy: 360,
            modifiers: vec![],
        },
        Action::Scroll {
            at: None,
            dx: -240,
            dy: 0,
            modifiers: vec![Key::Shift],
        },
        Action::Type {
            text: "pictures of cats".into(),
        },
        Action::KeyCombo {
            keys: vec![Key::Named("tab")],
            repeat: 4,
        },
        Action::KeyCombo {
            keys: vec![Key::Ctrl, Key::Char('s')],
            repeat: 1,
        },
        Action::KeyCombo {
            keys: vec![Key::Alt, Key::Named("tab")],
            repeat: 1,
        },
        Action::KeyCombo {
            keys: vec![Key::Named("enter")],
            repeat: 1,
        },
        Action::KeyCombo {
            keys: vec![Key::Meta, Key::Named("page-up")],
            repeat: 1,
        },
        Action::HoldKey {
            keys: vec![Key::Shift],
            duration_ms: 1500,
        },
        Action::Wait { duration_ms: 2000 },
    ];
    assert_eq!(got, want);
}

#[test]
fn claude_refusal_and_fallback() {
    let resp = load("docs/claude_response_refusal.json");
    let r = claude::parse_response(&resp).unwrap();
    assert_eq!(r.stop_details.as_ref().unwrap()["category"], "cyber");
    let mut c = claude::Conversation::new("m", "sys", "t", &shot("A"));
    let turn = c.accept(&resp).unwrap();
    assert_eq!(
        turn.end,
        End::Refused("Declined by a safety classifier.".into())
    );
    assert!(turn.actions.is_empty());
}

#[test]
fn claude_request_shapes() {
    let c = claude::Conversation::new("claude-opus-5-5", "sys", "do it", &shot("B64"));
    let r1 = c.body();
    assert_eq!(r1["tools"], json!([{"type": "computer_toolset_20260801"}]));
    assert_eq!(
        r1["thinking"],
        json!({"type": "adaptive", "display": "updates"})
    );
    assert_eq!(r1["output_config"], json!({"effort": "high"}));
    assert_eq!(r1["fallbacks"], "default");
    assert_eq!(r1["system"], "sys");
    assert_eq!(
        r1["context_management"]["edits"][0]["type"],
        "clear_tool_uses_20250919"
    );
    assert!(
        r1.get("tool_choice").is_none(),
        "forced tool_choice 400s on Opus 5.5"
    );
}

#[test]
fn claude_history_is_append_only_and_results_echo_the_toolset() {
    let resp = json!({
        "id": "msg_x", "model": "claude-opus-5-5", "stop_reason": "tool_use",
        "content": [
            {"type": "thinking", "thinking": "", "signature": "sig"},
            {"type": "tool_use", "id": "a", "name": "left_click", "toolset_name": "computer", "input": {"coordinate": [640, 425]}},
            {"type": "tool_use", "id": "b", "name": "zoom", "toolset_name": "computer", "input": {"region": [560, 400, 720, 450]}},
            {"type": "tool_use", "id": "c", "name": "cursor_position", "toolset_name": "computer", "input": {}},
            {"type": "tool_use", "id": "d", "name": "left_click", "toolset_name": "computer", "input": {"coordinate": [5000, 10]}},
            {"type": "tool_use", "id": "e", "name": "screenshot", "toolset_name": "computer", "input": {}}
        ]
    });
    let mut c = claude::Conversation::new("claude-opus-5-5", "sys", "t", &shot("FIRST"));
    let first = c.body().clone();
    let turn = c.accept(&resp).unwrap();
    assert_eq!(turn.actions.len(), 5);
    c.answer(&[
        Outcome::Ok,
        Outcome::Image(shot("ZOOM")),
        Outcome::Text("X=1, Y=2".into()),
        Outcome::Failed("Rejected by the host: out of bounds".into()),
        Outcome::NotExecuted,
    ]);
    let second = c.body();
    let msgs = second["messages"].as_array().unwrap();
    assert_eq!(msgs.len(), 3);
    assert_eq!(msgs[0], first["messages"][0]);
    assert_eq!(msgs[1]["role"], "assistant");
    assert_eq!(
        msgs[1]["content"], resp["content"],
        "assistant content byte for byte"
    );
    let results = msgs[2]["content"].as_array().unwrap();
    assert_eq!(results.len(), 5);
    for (r, id) in results.iter().zip(["a", "b", "c", "d", "e"]) {
        assert_eq!(r["toolset_name"], "computer");
        assert_eq!(r["tool_use_id"], id);
    }
    assert_eq!(results[0]["content"][0]["text"], "OK");
    assert_eq!(results[1]["content"][0]["source"]["data"], "ZOOM");
    assert_eq!(results[2]["content"][0]["text"], "X=1, Y=2");
    assert_eq!(results[3]["is_error"], true);
    assert_eq!(results[4]["content"], claude::NOT_EXECUTED);
}

#[test]
fn claude_rejects_bad_members() {
    assert_eq!(
        claude::parse_member("teleport", &json!({})),
        Err(ParseError::UnknownAction("teleport".into()))
    );
    assert!(
        claude::parse_member("left_click", &json!({"text": "a"})).is_err(),
        "non-modifier in click text"
    );
    assert!(claude::parse_member("wait", &json!({"duration": 301})).is_err());
    assert!(
        claude::parse_member(
            "scroll",
            &json!({"scroll_direction": "sideways", "scroll_amount": 1})
        )
        .is_err()
    );
    assert!(
        validate(
            &claude::parse_member("key", &json!({"text": "a", "repeat": 101})).unwrap(),
            1280,
            720
        )
        .is_err()
    );
    // Non-computer tool_use blocks are not treated as computer actions.
    let r = claude::parse_response(&json!({"id": "m", "content": [
        {"type": "tool_use", "id": "x", "name": "screenshot", "input": {}}
    ]}))
    .unwrap();
    assert!(r.calls.is_empty());
}

#[test]
fn key_normalization_across_providers() {
    for (a, b) in [
        ("ENTER", "Return"),
        ("ESC", "Escape"),
        ("CTRL", "ctrl"),
        ("CMD", "super"),
        ("PAGEUP", "Page_Up"),
        ("BACKSPACE", "BackSpace"),
        ("ArrowDown", "Down"),
        ("OPTION", "alt"),
        ("SPACE", "space"),
        ("F5", "F5"),
    ] {
        assert_eq!(
            normalize_key(a).unwrap(),
            normalize_key(b).unwrap(),
            "{a} vs {b}"
        );
    }
    assert_eq!(
        parse_combo("ctrl++").unwrap(),
        vec![Key::Ctrl, Key::Char('+')]
    );
    assert_eq!(
        parse_combo("ctrl+plus").unwrap(),
        vec![Key::Ctrl, Key::Char('+')]
    );
    assert_eq!(
        parse_combo("ctrl+shift+T").unwrap(),
        vec![Key::Ctrl, Key::Shift, Key::Char('t')]
    );
    assert!(parse_combo("").is_err());
}

/// Every named key an action can carry is one the input service knows.
#[test]
fn named_keys_reach_the_input_service() {
    let names = [
        "enter",
        "tab",
        "escape",
        "backspace",
        "delete",
        "insert",
        "space",
        "up",
        "down",
        "left",
        "right",
        "home",
        "end",
        "pageup",
        "pagedown",
        "capslock",
        "printscreen",
        "f1",
        "f24",
    ];
    for n in names {
        let k = normalize_key(n).unwrap();
        let steps = desktop::plan(
            &Action::KeyCombo {
                keys: vec![k],
                repeat: 1,
            },
            (100, 100),
            None,
        )
        .unwrap();
        for s in steps {
            if let Step::Inject(e) = s {
                assert!(e.valid(), "{n}: {e:?}");
            }
        }
    }
}

#[test]
fn live_fixtures_parse() {
    let d = dir().join("live").join("claude");
    let mut seen = 0;
    for e in std::fs::read_dir(&d).unwrap().flatten() {
        let path = e.path();
        let name = path.file_name().unwrap().to_string_lossy().to_string();
        let text = std::fs::read_to_string(&path).unwrap();
        assert!(
            !text.contains("iVBORw0KGgo"),
            "{name}: inline PNG left in fixture"
        );
        if !name.ends_with(".response.json") {
            continue;
        }
        seen += 1;
        let r = claude::parse_response(&serde_json::from_str(&text).unwrap()).unwrap();
        for c in r.calls {
            let a = c.action.unwrap_or_else(|e| panic!("{name}: {e}"));
            validate(&a, 1280, 720).unwrap_or_else(|e| panic!("{name}: {a:?}: {e}"));
        }
    }
    assert_eq!(seen, 2);
}

#[test]
fn task_state_machine() {
    use State::*;
    let cases = [
        (Running, "pause", Some(Paused)),
        (Approval, "pause", Some(Paused)),
        (Paused, "pause", None),
        (Paused, "resume", Some(Running)),
        (Running, "resume", None),
        (Running, "cancel", Some(Cancelled)),
        (Paused, "cancel", Some(Cancelled)),
        (Done, "cancel", None),
        (Running, "ask", Some(Approval)),
        (Paused, "ask", None),
        (Approval, "decide", Some(Running)),
        (Running, "decide", None),
        (Paused, "decide", None),
        (Running, "finish", Some(Done)),
        (Cancelled, "finish", None),
        (Approval, "fail", Some(Failed)),
        (Cancelled, "fail", None),
        (Done, "reply", Some(Running)),
        (Failed, "reply", None),
        (Cancelled, "resume", None),
    ];
    for (from, event, want) in cases {
        assert_eq!(testing::next_state(from, event), want, "{from:?} + {event}");
    }
}

fn pointer(action: PointerAction, button: u8, x: f32, y: f32) -> Step {
    Step::Inject(Event::Pointer {
        action,
        button,
        x,
        y,
    })
}

fn key(name: &str, down: bool, mods: Mods) -> Step {
    Step::Inject(Event::Key {
        key: name.into(),
        down,
        mods,
    })
}

#[test]
fn actions_become_input_steps() {
    let ctrl = Mods {
        ctrl: true,
        ..Mods::default()
    };
    let shift = Mods {
        shift: true,
        ..Mods::default()
    };
    let none = Mods::default();
    let seen = (100, 50);
    let cases: Vec<(Action, Vec<Step>)> = vec![
        (
            Action::Click {
                at: Some(p(49, 24)),
                button: Button::Right,
                count: 1,
                modifiers: vec![],
            },
            vec![
                pointer(PointerAction::Move, 0, 0.495, 0.49),
                pointer(PointerAction::Down, 1, 0.495, 0.49),
                pointer(PointerAction::Up, 1, 0.495, 0.49),
                Step::Release,
            ],
        ),
        (
            // No point: the cursor's position.
            Action::Click {
                at: None,
                button: Button::Left,
                count: 2,
                modifiers: vec![Key::Shift],
            },
            vec![
                key("shift", true, none),
                pointer(PointerAction::Move, 0, 0.25, 0.75),
                pointer(PointerAction::Down, 0, 0.25, 0.75),
                pointer(PointerAction::Up, 0, 0.25, 0.75),
                Step::Sleep(40),
                pointer(PointerAction::Down, 0, 0.25, 0.75),
                pointer(PointerAction::Up, 0, 0.25, 0.75),
                Step::Release,
            ],
        ),
        (
            Action::KeyCombo {
                keys: vec![Key::Ctrl, Key::Char('s')],
                repeat: 2,
            },
            vec![
                key("s", true, ctrl),
                key("s", false, ctrl),
                Step::Sleep(30),
                key("s", true, ctrl),
                key("s", false, ctrl),
                Step::Release,
            ],
        ),
        (
            // Modifiers alone stay held together until the release.
            Action::KeyCombo {
                keys: vec![Key::Ctrl, Key::Shift],
                repeat: 1,
            },
            vec![
                key("ctrl", true, none),
                key("shift", true, ctrl),
                Step::Release,
            ],
        ),
        (
            Action::KeyCombo {
                keys: vec![Key::Ctrl, Key::Char('+')],
                repeat: 1,
            },
            vec![
                key(
                    "equal",
                    true,
                    Mods {
                        ctrl: true,
                        shift: true,
                        ..none
                    },
                ),
                key(
                    "equal",
                    false,
                    Mods {
                        ctrl: true,
                        shift: true,
                        ..none
                    },
                ),
                Step::Release,
            ],
        ),
        (
            Action::Type {
                text: "a\r\nb".into(),
            },
            vec![
                Step::Inject(Event::Text { text: "a".into() }),
                Step::Sleep(30),
                key("enter", true, none),
                key("enter", false, none),
                Step::Sleep(30),
                Step::Inject(Event::Text { text: "b".into() }),
                Step::Release,
            ],
        ),
        (
            Action::Scroll {
                at: None,
                dx: 0,
                dy: 360,
                modifiers: vec![],
            },
            vec![
                Step::Inject(Event::Scroll { dx: 0.0, dy: 3.0 }),
                Step::Release,
            ],
        ),
        (
            Action::HoldKey {
                keys: vec![Key::Shift, Key::Char('a')],
                duration_ms: 500,
            },
            vec![
                key("shift", true, none),
                key("a", true, shift),
                Step::Sleep(500),
                key("a", false, none),
                Step::Release,
            ],
        ),
        (
            Action::Wait { duration_ms: 20 },
            vec![Step::Sleep(20), Step::Release],
        ),
        (Action::Screenshot, vec![]),
    ];
    for (action, want) in cases {
        assert_eq!(
            desktop::plan(&action, seen, Some((0.25, 0.75))).unwrap(),
            want,
            "{action:?}"
        );
    }
    let drag = desktop::plan(
        &Action::Drag {
            path: vec![p(0, 0), p(99, 49)],
            modifiers: vec![],
        },
        seen,
        None,
    )
    .unwrap();
    assert_eq!(drag[1], pointer(PointerAction::Down, 0, 0.005, 0.01));
    assert_eq!(
        drag[drag.len() - 2],
        pointer(PointerAction::Up, 0, 0.995, 0.99)
    );
    assert!(
        drag.iter()
            .filter(|s| matches!(
                s,
                Step::Inject(Event::Pointer {
                    action: PointerAction::Move,
                    ..
                })
            ))
            .count()
            > 2
    );
    assert!(
        desktop::plan(
            &Action::KeyCombo {
                keys: vec![Key::Char('é')],
                repeat: 1
            },
            seen,
            None
        )
        .is_err()
    );
    assert!(
        desktop::plan(
            &Action::Click {
                at: None,
                button: Button::Left,
                count: 1,
                modifiers: vec![]
            },
            seen,
            None
        )
        .is_err()
    );
}

/// Regression (2026-10-17): text sent as one burst came out garbled in
/// Windows 11 Notepad ("Hello iiiiiiiiiii"); it is typed one character
/// per event, 30 ms apart, surrogate pairs kept whole.
#[test]
fn typing_is_paced_one_character_at_a_time() {
    let text = "Hé😀".to_string() + &"x".repeat(5000);
    let steps = desktop::plan(&Action::Type { text: text.clone() }, (100, 100), None).unwrap();
    let mut typed = String::new();
    for (i, s) in steps[..steps.len() - 1].iter().enumerate() {
        match s {
            Step::Inject(Event::Text { text }) if i % 2 == 0 => {
                assert_eq!(text.chars().count(), 1);
                typed += text;
            }
            Step::Sleep(30) if i % 2 == 1 => {}
            other => panic!("step {i}: {other:?}"),
        }
    }
    assert_eq!(typed, text);
    assert_eq!(steps.last(), Some(&Step::Release));
}

#[test]
fn screenshots_fit_the_model_and_regions_map_back() {
    let frame = Frame {
        bgra: [10, 20, 30, 255].repeat(2732 * 1536),
        width: 2732,
        height: 1536,
    };
    let s = desktop::shot(&frame, [0, 0, 2732, 1536], desktop::SHOT_MAX);
    assert_eq!((s.width, s.height), (1366, 768));
    let s = desktop::shot(&frame, [100, 100, 200, 150], desktop::SHOT_MAX);
    assert_eq!((s.width, s.height), (100, 50), "a zoom is never scaled up");
    assert_eq!(
        desktop::frame_region([683, 384, 1366, 768], (1366, 768), (2732, 1536)),
        [1366, 768, 2732, 1536]
    );
    let small = screens::screen(false).frame();
    let s = desktop::shot(&small, [0, 0, small.width, small.height], desktop::SHOT_MAX);
    assert_eq!((s.width, s.height), (1280, 720));
    use base64::Engine;
    let png = base64::engine::general_purpose::STANDARD
        .decode(&s.b64)
        .unwrap();
    assert_eq!(&png[..8], b"\x89PNG\r\n\x1a\n");
}

// ---- The runtime against a fake desktop and a local provider (ai::fake) ----

#[test]
fn claude_task_clicks_and_finishes() {
    let server = server(vec![
        (0, claude_turn("m1", click_save())),
        (0, claude_end("Saved the file.")),
    ]);
    let rig = rig("claude", "claude", &server.url);
    let id = rig.ai.start("k1", "Save the file", None).unwrap();
    assert_eq!(
        rig.ai.start("k1", "Save the file", None).unwrap(),
        id,
        "one task per idempotency key"
    );
    let t = rig.state(id, "done");
    assert_eq!(t["text"], "Saved the file.");
    assert_eq!(t["progress"], json!(["Pressing Save."]));
    assert_eq!(t["steps"].as_array().unwrap().len(), 2);
    assert_eq!(t["steps"][0]["action"]["kind"], "click");
    assert_eq!(t["steps"][0]["outcome"], "ok");
    assert!(
        rig.log.lock().unwrap().pressed,
        "the click landed on the button"
    );
    let requests = server.requests.lock().unwrap();
    assert_eq!(requests.len(), 2);
    let (headers, first) = &requests[0];
    assert!(
        headers
            .iter()
            .any(|h| h.to_ascii_lowercase() == format!("x-api-key: {TEST_KEY}"))
    );
    assert!(
        !first.to_string().contains(TEST_KEY),
        "the key never goes into a body"
    );
    assert!(first["system"].as_str().unwrap().contains("1280x720"));
    let results = &requests[1].1["messages"][2]["content"];
    assert_eq!(results[0]["content"][0]["text"], "OK");
    assert_eq!(
        results[1]["content"][0]["type"], "image",
        "the screenshot after the click"
    );
    // The finished task is in the saved history; the history can be cleared.
    let saved: Value =
        serde_json::from_slice(&std::fs::read(rig.dir.join("ai-tasks.json")).unwrap()).unwrap();
    assert_eq!(saved[0]["id"], id);
    assert!(
        !saved.to_string().contains("iVBORw0KGgo"),
        "no screenshots in the history"
    );
    rig.ai.clear().unwrap();
    assert_eq!(rig.ai.tasks(), json!([]));
}

#[test]
fn a_response_after_a_pause_never_acts() {
    // The model answers slowly; the host user pauses meanwhile.
    let server = server(vec![
        (400, claude_turn("m1", click_save())),
        (0, claude_end("ok")),
    ]);
    let rig = rig("pause", "claude", &server.url);
    let id = rig.ai.start("k", "Save the file", None).unwrap();
    server.wait_for(1);
    rig.ai.pause(id, Pause::HostUser).unwrap();
    let t = rig.until(id, "the late turn recorded", |t| {
        t["steps"].as_array().unwrap().len() == 2
    });
    assert_eq!(t["state"], "paused");
    assert_eq!(t["paused"], "host-user");
    assert_eq!(t["steps"][0]["outcome"], "paused");
    assert_eq!(rig.clicks(), 0, "nothing ran");
    assert_eq!(
        server.requests.lock().unwrap().len(),
        1,
        "no answer goes out while paused"
    );
    rig.ai.resume(id).unwrap();
    rig.state(id, "done");
    let requests = server.requests.lock().unwrap();
    let results = &requests[1].1["messages"][2]["content"];
    assert_eq!(results[0]["is_error"], true);
    assert_eq!(results[0]["content"], claude::PAUSED);
    assert_eq!(rig.clicks(), 0);
}

#[test]
fn local_input_pauses_the_task_at_once() {
    let server = server(vec![
        (300, claude_turn("m1", click_save())),
        (0, claude_end("ok")),
    ]);
    let rig = rig("local", "claude", &server.url);
    let id = rig.ai.start("k", "Save the file", None).unwrap();
    server.wait_for(1);
    rig.log.lock().unwrap().press_at = Some(Instant::now());
    let t = rig.state(id, "paused");
    assert_eq!(t["paused"], "local-input");
    rig.until(id, "the late turn recorded", |t| {
        t["steps"].as_array().unwrap().len() == 2
    });
    assert_eq!(rig.clicks(), 0, "the host user wins");
    rig.ai.cancel(id).unwrap();
    rig.state(id, "cancelled");
}

#[test]
fn every_action_policy_asks_and_a_decline_runs_nothing() {
    let server = server(vec![
        (0, claude_turn("m1", click_save())),
        (0, claude_end("I could not save.")),
    ]);
    let rig = rig("decline", "claude", &server.url);
    rig.ai
        .set_settings(&json!({"approval": "every-action"}))
        .unwrap();
    let id = rig.ai.start("k", "Save the file", None).unwrap();
    let t = rig.state(id, "approval");
    assert_eq!(t["approval"]["action"]["kind"], "click");
    assert_eq!(rig.clicks(), 0);
    rig.ai.decide(id, false).unwrap();
    let t = rig.state(id, "done");
    assert_eq!(t["steps"][0]["outcome"], "declined");
    assert_eq!(t["steps"][1]["outcome"], "not-executed");
    assert_eq!(rig.clicks(), 0);
    let requests = server.requests.lock().unwrap();
    let results = &requests[1].1["messages"][2]["content"];
    assert_eq!(
        results[0]["content"][0]["text"],
        "The host user declined this action."
    );
}

#[test]
fn openai_safety_check_waits_for_approval_and_is_acknowledged() {
    let turn1 = json!({"id": "resp_1", "status": "completed", "output": [{
        "type": "computer_call", "call_id": "call_1", "status": "completed",
        "pending_safety_checks": [{"id": "sc_1", "code": "malicious_instructions", "message": "Check the page."}],
        "actions": [{"type": "click", "button": "left", "x": 640, "y": 425}]
    }]});
    let turn2 = json!({"id": "resp_2", "status": "completed", "output": [{
        "type": "message", "content": [{"type": "output_text", "text": "Done."}]
    }]});
    let server = server(vec![(0, turn1), (0, turn2)]);
    let rig = rig("openai", "openai", &server.url);
    let id = rig.ai.start("k", "Save the file", None).unwrap();
    let t = rig.state(id, "approval");
    assert_eq!(t["approval"]["flagged"], json!(["Check the page."]));
    assert_eq!(rig.clicks(), 0);
    rig.ai.decide(id, true).unwrap();
    rig.state(id, "done");
    assert_eq!(rig.clicks(), 1);
    let requests = server.requests.lock().unwrap();
    let (headers, second) = &requests[1];
    assert!(
        headers
            .iter()
            .any(|h| h.to_ascii_lowercase() == format!("authorization: bearer {TEST_KEY}"))
    );
    assert_eq!(second["previous_response_id"], "resp_1");
    assert_eq!(
        second["input"][0]["acknowledged_safety_checks"][0]["id"],
        "sc_1"
    );
    assert_eq!(second["input"][0]["output"]["type"], "computer_screenshot");
}

#[test]
fn stale_and_out_of_range_actions_are_rejected() {
    let out_of_range = claude_turn(
        "m1",
        json!([
            {"type": "tool_use", "id": "t1", "name": "left_click", "toolset_name": "computer", "input": {"coordinate": [5000, 10]}},
            {"type": "tool_use", "id": "t2", "name": "left_click", "toolset_name": "computer", "input": {"coordinate": [640, 425]}}
        ]),
    );
    let server = server(vec![
        (0, out_of_range),
        (400, claude_turn("m2", click_save())),
        (0, claude_end("ok")),
    ]);
    let rig = rig("stale", "claude", &server.url);
    let id = rig.ai.start("k", "Save the file", None).unwrap();
    // After the first turn the screen changes size: the second turn's click
    // refers to a screenshot that is no longer current.
    rig.until(id, "first turn", |t| {
        t["steps"].as_array().unwrap().len() >= 2
    });
    rig.log.lock().unwrap().size = Some((1920, 1080));
    let t = rig.state(id, "done");
    let outcomes: Vec<&str> = t["steps"]
        .as_array()
        .unwrap()
        .iter()
        .map(|s| s["outcome"].as_str().unwrap())
        .collect();
    assert_eq!(
        outcomes,
        ["failed", "not-executed", "failed", "not-executed"]
    );
    assert_eq!(t["steps"][2]["detail"], "stale");
    assert_eq!(rig.clicks(), 0);
}

#[test]
fn one_task_at_a_time_and_cancel_stops_a_request() {
    let server = server(vec![(3000, claude_end("late"))]);
    let rig = rig("cancel", "claude", &server.url);
    let id = rig.ai.start("a", "First", None).unwrap();
    assert!(rig.ai.start("b", "Second", None).is_err());
    rig.ai.cancel(id).unwrap();
    let t = rig.state(id, "cancelled");
    assert_eq!(t["text"], "", "the late answer never arrives");
    assert!(rig.ai.resume(id).is_err());
    assert!(
        rig.ai.start("", "", None).is_err(),
        "an instruction is needed"
    );
}

#[test]
fn settings_and_keys_never_expose_the_key() {
    let rig = rig("settings", "claude", "http://127.0.0.1:9");
    let s = rig.ai.settings();
    assert_eq!(s["keys"], json!({"openai": false, "claude": true}));
    assert!(!s.to_string().contains(TEST_KEY));
    assert!(rig.ai.set_settings(&json!({"nope": 1})).is_err());
    let mut sources = s["sources"].clone();
    // Refused: a model the sources do not have, plain HTTP to another
    // computer, a repeated id, a source without models.
    assert!(
        rig.ai
            .set_settings(&json!({"model": {"source": "claude", "id": "nope"}}))
            .is_err()
    );
    let custom = |id: &str, url: &str, models: Value| json!({"id": id, "name": "Mine", "template": "custom", "protocol": "openai-chat", "url": url, "models": models});
    for (bad, why) in [
        (
            custom("mine", "http://192.168.1.5:1234/v1", json!([{"id": "m"}])),
            "http to another computer",
        ),
        (
            custom("claude", "https://x.example/v1", json!([{"id": "m"}])),
            "repeated id",
        ),
        (
            custom("mine", "https://x.example/v1", json!([])),
            "no models",
        ),
        (
            custom("Mine!", "https://x.example/v1", json!([{"id": "m"}])),
            "bad id",
        ),
    ] {
        let mut s2 = sources.clone();
        s2.as_array_mut().unwrap().push(bad);
        assert!(
            rig.ai.set_settings(&json!({"sources": s2})).is_err(),
            "{why}"
        );
    }
    // A local server (Ollama) over plain HTTP, and a new model chosen.
    sources.as_array_mut().unwrap().push(custom(
        "ollama",
        "http://localhost:11434/v1",
        json!([{"id": "qwen2.5vl:7b"}]),
    ));
    rig.ai
        .set_settings(
            &json!({"sources": sources, "model": {"source": "ollama", "id": "qwen2.5vl:7b"}}),
        )
        .unwrap();
    let saved = std::fs::read_to_string(rig.dir.join("ai.json")).unwrap();
    assert!(saved.contains("qwen2.5vl:7b") && !saved.contains(TEST_KEY));
    // A local chat server needs no key; Claude does.
    assert!(rig.ai.start("local", "Save", None).is_ok());
    let id = rig.ai.tasks()[0]["id"].as_u64().unwrap();
    let _ = rig.ai.cancel(id);
    rig.state(id, "cancelled");
    rig.ai
        .set_settings(&json!({"model": {"source": "claude", "id": "claude-opus-5-5"}}))
        .unwrap();
    // Removing a source removes its key.
    rig.ai.set_key("ollama", Some("k")).unwrap();
    let only_defaults: Vec<Value> = sources.as_array().unwrap()[..2].to_vec();
    rig.ai
        .set_settings(&json!({"sources": only_defaults}))
        .unwrap();
    assert!(
        rig.ai.set_key("ollama", Some("k")).is_err(),
        "no such source"
    );
    rig.ai.set_key("claude", None).unwrap();
    assert!(rig.ai.start("k", "Save", None).is_err(), "no key, no task");
}

/// A real Claude round trip on the synthetic screens (nothing touches the
/// real desktop). Runs with `KYOKAI_LIVE_CLAUDE=1` and `ANTHROPIC_API_KEY`.
#[test]
fn live_claude_task_on_synthetic_screens() {
    let (Ok("1"), Ok(key)) = (
        std::env::var("KYOKAI_LIVE_CLAUDE").as_deref(),
        std::env::var("ANTHROPIC_API_KEY"),
    ) else {
        eprintln!("skipped: set KYOKAI_LIVE_CLAUDE=1 and ANTHROPIC_API_KEY");
        return;
    };
    let rig = rig("live", "claude", &format!("{}/messages", claude::BASE_URL));
    rig.ai.set_key("claude", Some(&key)).unwrap();
    let id = rig
        .ai
        .start(
            "k",
            "Press the SAVE button in the dialog, then confirm that the file was saved.",
            None,
        )
        .unwrap();
    let t0 = Instant::now();
    let t = loop {
        let t = rig.task(id);
        if t["state"] == "done" || t["state"] == "failed" {
            break t;
        }
        assert!(
            t0.elapsed() < Duration::from_secs(240),
            "live task timed out: {t:#}"
        );
        std::thread::sleep(Duration::from_millis(200));
    };
    eprintln!(
        "live task: {:#}",
        json!({"state": t["state"], "error": t["error"], "text": t["text"], "steps": t["steps"]})
    );
    assert_eq!(t["state"], "done", "{t:#}");
    assert!(rig.log.lock().unwrap().pressed, "the model pressed SAVE");
}

/// The real desktop: a capture of the primary display at its pixel size,
/// and the local-input hooks. Run by hand (`--ignored`) on a machine with a
/// desktop; it reads the screen into memory only and injects nothing.
#[cfg(windows)]
#[test]
#[ignore]
fn real_screen_capture_and_hooks() {
    use super::desktop::Desktop;
    let mut screen = desktop::Screen::open().unwrap();
    let (w, h) = screen.size().unwrap();
    let frame = screen.capture().unwrap();
    assert_eq!((frame.width, frame.height), (w, h));
    assert_eq!(frame.bgra.len(), (w * h * 4) as usize);
    assert!(
        frame.bgra.chunks(4).any(|p| p[..3] != [0, 0, 0]),
        "not a black picture"
    );
    let s = desktop::shot(&frame, [0, 0, w, h], desktop::SHOT_MAX);
    assert!(s.width <= desktop::SHOT_MAX.0 && s.height <= desktop::SHOT_MAX.1);
    let (x, y) = screen.cursor().unwrap();
    assert!((0.0..=1.0).contains(&x) && (0.0..=1.0).contains(&y));
    eprintln!(
        "primary {w}x{h}, shot {}x{}, cursor ({x:.3}, {y:.3}), touched {}",
        s.width,
        s.height,
        screen.local_input().press.is_some()
    );
}

#[test]
fn local_input_counts_after_the_desktop_was_given() {
    let since = Instant::now();
    let before = since - Duration::from_millis(10);
    let soon = since + Duration::from_millis(500);
    let later = since + crate::input::MOVE_GRACE + Duration::from_millis(10);
    let cases = [
        (None, None, false),
        (Some(before), Some(before), false),
        (Some(soon), None, true),
        (None, Some(soon), false),
        (None, Some(later), true),
    ];
    for (press, moved, want) in cases {
        assert_eq!(
            LocalInput { press, moved }.after(since),
            want,
            "{press:?} {moved:?}"
        );
    }
}

#[test]
fn clicking_allow_in_kyokai_is_not_a_take_over() {
    let server = server(vec![
        (0, claude_turn("m1", click_save())),
        (0, claude_end("ok")),
    ]);
    let rig = rig("allow-click", "claude", &server.url);
    rig.ai
        .set_settings(&json!({"approval": "every-action"}))
        .unwrap();
    let id = rig.ai.start("k", "Save the file", None).unwrap();
    rig.state(id, "approval");
    // The host user clicks Allow: a press just before the decision, then the
    // hand leaves the mouse.
    {
        let mut log = rig.log.lock().unwrap();
        log.press_at = Some(Instant::now());
        log.moved_at = Some(Instant::now());
    }
    std::thread::sleep(Duration::from_millis(5));
    rig.ai.decide(id, true).unwrap();
    rig.log.lock().unwrap().moved_at = Some(Instant::now());
    rig.until(id, "the screenshot's approval", |t| {
        t["state"] == "approval" && t["steps"].as_array().unwrap().len() == 1
    });
    assert_eq!(rig.clicks(), 1, "the approved click ran");
    rig.ai.decide(id, true).unwrap();
    rig.state(id, "done");
}

#[test]
fn the_stop_shortcut_cancels_and_a_take_over_pauses() {
    let server = server(vec![
        (500, claude_turn("m1", click_save())),
        (0, claude_end("ok")),
    ]);
    let rig = rig("stop-key", "claude", &server.url);
    let id = rig.ai.start("k", "Save the file", None).unwrap();
    server.wait_for(1);
    rig.ai.take_over();
    let t = rig.state(id, "paused");
    assert_eq!(t["paused"], "take-over");
    assert!(rig.ai.pause(id, Pause::HostUser).is_err(), "already paused");
    rig.log.lock().unwrap().stop = true;
    rig.state(id, "cancelled");
    assert_eq!(rig.clicks(), 0);
}

/// Provider answers are untrusted input: random documents, and real
/// fixtures with random parts replaced, never panic a parser or a
/// conversation.
#[test]
fn garbage_answers_never_panic() {
    let mut seed = 0x9E37_79B9_7F4A_7C15u64;
    let mut next = move || {
        seed ^= seed << 13;
        seed ^= seed >> 7;
        seed ^= seed << 17;
        seed
    };
    fn random(next: &mut impl FnMut() -> u64, depth: u32) -> Value {
        let words = [
            "type",
            "tool_use",
            "computer",
            "computer_call",
            "actions",
            "click",
            "key",
            "text",
            "coordinate",
            "region",
            "x",
            "y",
            "id",
            "content",
            "output",
            "name",
            "input",
            "keys",
            "scroll",
        ];
        match next() % if depth > 3 { 5 } else { 7 } {
            0 => Value::Null,
            1 => json!(next().is_multiple_of(2)),
            2 => json!((next() as i64) >> (next() % 64)),
            3 => json!(
                f64::from_bits(next())
                    .is_finite()
                    .then(|| (next() % 10_000) as f64 / 3.0)
            ),
            4 => json!(words[(next() % words.len() as u64) as usize]),
            5 => Value::Array((0..next() % 5).map(|_| random(next, depth + 1)).collect()),
            _ => Value::Object(
                (0..next() % 6)
                    .map(|_| {
                        (
                            words[(next() % words.len() as u64) as usize].to_string(),
                            random(next, depth + 1),
                        )
                    })
                    .collect(),
            ),
        }
    }
    let fixtures = [
        load("docs/claude_response_batch.json"),
        load("docs/openai_response_batch.json"),
        load("docs/openai_response_legacy.json"),
    ];
    for i in 0..3000usize {
        let doc = if i.is_multiple_of(2) {
            random(&mut next, 0)
        } else {
            // A real answer with one part replaced.
            let mut v = fixtures[i % fixtures.len()].clone();
            let list = if v.get("content").is_some() {
                "content"
            } else {
                "output"
            };
            if let Some(items) = v[list].as_array_mut() {
                let n = items.len();
                if n > 0 {
                    let j = (next() % n as u64) as usize;
                    let field = ["input", "actions", "action", "name", "type", "call_id"]
                        [(next() % 6) as usize];
                    items[j][field] = random(&mut next, 1);
                }
            }
            v
        };
        let _ = claude::parse_response(&doc);
        let _ = openai::parse_response(&doc);
        let mut c = claude::Conversation::new("m", "s", "t", &shot("A"));
        if let Ok(turn) = c.accept(&doc) {
            // As the runtime does: only a valid action is planned.
            for a in turn.actions.iter().flatten() {
                if validate(a, 1280, 720).is_ok() {
                    let _ = desktop::plan(a, (1280, 720), Some((0.5, 0.5)));
                }
            }
            c.answer(&vec![Outcome::Ok; turn.actions.len()]);
        }
        let mut o = openai::Conversation::new("m", "s", "t", &shot("A"));
        if let Ok(turn) = o.accept(&doc) {
            o.answer(
                &vec![Outcome::NotExecuted; turn.actions.len()],
                &shot("B"),
                true,
            );
        }
    }
}

#[test]
fn chat_task_clicks_and_finishes() {
    let call = |id: &str, name: &str, args: &str| json!({"id": id, "type": "function", "function": {"name": name, "arguments": args}});
    let server = server(vec![
        (
            0,
            json!({"choices": [{"finish_reason": "tool_calls", "message": {"role": "assistant",
            "content": "Pressing Save.", "tool_calls": [call("c1", "left_click", "{\"coordinate\": [640, 425]}")]}}]}),
        ),
        (
            0,
            json!({"choices": [{"finish_reason": "stop", "message": {"role": "assistant", "content": "Saved the file."}}]}),
        ),
    ]);
    let rig = rig("chat", "chat", &server.url);
    let id = rig.ai.start("k1", "Save the file", None).unwrap();
    let t = rig.state(id, "done");
    assert_eq!(t["text"], "Saved the file.");
    assert_eq!(t["steps"][0]["action"]["kind"], "click");
    assert!(
        rig.log.lock().unwrap().pressed,
        "the click landed on the button"
    );
    let requests = server.requests.lock().unwrap();
    let (headers, first) = &requests[0];
    assert!(
        headers.iter().any(|h| h.to_ascii_lowercase()
            == format!("authorization: bearer {}", TEST_KEY.to_ascii_lowercase())),
        "chat sources use a bearer key"
    );
    assert_eq!(first["model"], "vision-model");
    assert!(
        first["tools"]
            .as_array()
            .unwrap()
            .iter()
            .any(|t| t["function"]["name"] == "left_click")
    );
    // The answer: the call's result, then the screen after it.
    let answer = requests[1].1["messages"].as_array().unwrap();
    assert!(
        answer
            .iter()
            .any(|m| m["role"] == "tool" && m["tool_call_id"] == "c1")
    );
    assert_eq!(answer.last().unwrap()["content"][1]["type"], "image_url");
}

/// Sleep pauses the task in progress, so it never acts on a screen from
/// before the sleep.
#[test]
fn sleep_pauses_the_task() {
    let server = server(vec![
        (500, claude_turn("m1", click_save())),
        (0, claude_end("ok")),
    ]);
    let rig = rig("sleep", "claude", &server.url);
    let id = rig.ai.start("k", "Save the file", None).unwrap();
    server.wait_for(1);
    crate::power::going_to_sleep(&std::sync::Arc::downgrade(&rig.ai));
    let t = rig.state(id, "paused");
    assert_eq!(t["paused"], "sleep");
    assert_eq!(rig.clicks(), 0);
}
