//! The AI runtime's tests: provider fixtures parse into Kyokai actions,
//! requests have the documented shapes, actions become the right input,
//! and tasks behave by the contract (doc/en-us/computer-use.md, "AI
//! tasks") against a fake desktop and a local stand-in for the provider.

use serde_json::{Value, json};
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};

use super::action::{
    Action, Button, Coordinates, Key, ParseError, Point, normalize_key, parse_combo, validate,
};
use super::desktop::{self, Frame, Step};
use super::fake::*;
use super::task::{Pause, State, testing};
use super::{claude, openai, screens};
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

/// The computer calls of a Claude response, parsed by the engine's parser
/// (the sidecar picks them out of the response as this does).
fn claude_calls(response: &Value) -> Vec<Result<Action, ParseError>> {
    response["content"]
        .as_array()
        .into_iter()
        .flatten()
        .filter(|b| b["type"] == "tool_use" && b["toolset_name"] == "computer")
        .map(|b| claude::parse_member(b["name"].as_str().unwrap_or(""), &b["input"]))
        .collect()
}

/// The actions of an OpenAI response's computer calls, parsed likewise.
fn openai_actions(response: &Value) -> Vec<Result<Action, ParseError>> {
    response["output"]
        .as_array()
        .into_iter()
        .flatten()
        .filter(|i| i["type"] == "computer_call")
        .flat_map(|i| match (i["actions"].as_array(), i.get("action")) {
            (Some(list), _) if !list.is_empty() => list.clone(),
            (_, Some(a)) if !a.is_null() => vec![a.clone()],
            _ => Vec::new(),
        })
        .map(|a| openai::parse_action(&a))
        .collect()
}
/// What the sidecar answers a Claude action that a pause stopped.
const PAUSED: &str =
    "Not executed: the host user paused the task. Take a screenshot before continuing.";

#[test]
fn openai_batch_actions() {
    let got: Vec<Action> = openai_actions(&load("docs/openai_response_batch.json"))
        .into_iter()
        .map(Result::unwrap)
        .collect();
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

/// An older response's single `action` reads like a batch of one.
#[test]
fn openai_legacy_single_action() {
    assert_eq!(
        openai_actions(&load("docs/openai_response_legacy.json")),
        vec![Ok(Action::Click {
            at: Some(p(700, 100)),
            button: Button::Right,
            count: 1,
            modifiers: vec![]
        })]
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
    let got: Vec<Action> = claude_calls(&load("docs/claude_response_batch.json"))
        .into_iter()
        .map(Result::unwrap)
        .collect();
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
        for c in claude_calls(&serde_json::from_str(&text).unwrap()) {
            let a = c.unwrap_or_else(|e| panic!("{name}: {e}"));
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
    let s = desktop::shot(&frame, [0, 0, 2732, 1536], desktop::SHOT_MAX, 1);
    assert_eq!((s.width, s.height), (1366, 768));
    let s = desktop::shot(&frame, [100, 100, 200, 150], desktop::SHOT_MAX, 1);
    assert_eq!((s.width, s.height), (100, 50), "a zoom is never scaled up");
    assert_eq!(
        desktop::frame_region([683, 384, 1366, 768], (1366, 768), (2732, 1536)),
        [1366, 768, 2732, 1536]
    );
    let small = screens::screen(false).frame();
    let s = desktop::shot(
        &small,
        [0, 0, small.width, small.height],
        desktop::SHOT_MAX,
        1,
    );
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
    assert!(
        first["system"][0]["text"]
            .as_str()
            .unwrap()
            .contains("1280x720")
    );
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
    assert_eq!(results[0]["content"], PAUSED);
    assert_eq!(rig.clicks(), 0);
}

/// Regression: an answer to a request sent before a pause acted after a
/// resume, on the screen the model saw before the pause.
#[test]
fn a_response_after_a_pause_and_a_resume_never_acts() {
    let server = server(vec![
        (400, claude_turn("m1", click_save())),
        (0, claude_end("ok")),
    ]);
    let rig = rig("pause-resume", "claude", &server.url);
    let id = rig.ai.start("k", "Save the file", None).unwrap();
    server.wait_for(1);
    rig.ai.pause(id, Pause::HostUser).unwrap();
    rig.ai.resume(id).unwrap();
    let t = rig.state(id, "done");
    assert_eq!(t["steps"][0]["outcome"], "paused");
    assert_eq!(rig.clicks(), 0, "nothing ran");
    let requests = server.requests.lock().unwrap();
    let results = &requests[1].1["messages"][2]["content"];
    assert_eq!(results[0]["content"], PAUSED);
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
    // While it waits, nobody's input reaches Kyokai's windows.
    assert!(rig.ai.awaiting_approval());
    rig.ai.decide(id, false).unwrap();
    let t = rig.state(id, "done");
    assert_eq!(t["steps"][0]["outcome"], "declined");
    assert!(!rig.ai.awaiting_approval());
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

/// Regression: a source kept its key when its address changed, so a model
/// list sent the key to the new address; a key never goes over plain HTTP.
#[test]
fn keys_go_only_over_https_to_where_they_were_given() {
    let rig = rig("key-url", "claude", "http://127.0.0.1:9");
    let sources = |rig: &Rig| rig.ai.settings()["sources"].clone();
    let set = |rig: &Rig, s: Value| rig.ai.set_settings(&json!({"sources": s})).unwrap();
    // A new name keeps the key; a new address or protocol drops it.
    let mut s = sources(&rig);
    s[0]["name"] = json!("Renamed");
    set(&rig, s);
    assert_eq!(rig.ai.settings()["keys"]["claude"], true, "renamed");
    let mut s = sources(&rig);
    s[0]["url"] = json!("https://elsewhere.example/v1");
    set(&rig, s);
    assert_eq!(rig.ai.settings()["keys"]["claude"], false, "a new address");
    rig.ai.set_key("openai", Some(TEST_KEY)).unwrap();
    let mut s = sources(&rig);
    s[1]["protocol"] = json!("openai-chat");
    set(&rig, s);
    assert_eq!(rig.ai.settings()["keys"]["openai"], false, "a new protocol");
    // A server on this computer over plain HTTP gets no key, though one is
    // stored for it.
    let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
    let port = listener.local_addr().unwrap().port();
    let seen = std::thread::spawn(move || {
        use std::io::{BufRead, Write};
        let (stream, _) = listener.accept().unwrap();
        let mut reader = std::io::BufReader::new(stream.try_clone().unwrap());
        let mut headers = Vec::new();
        loop {
            let mut line = String::new();
            reader.read_line(&mut line).unwrap();
            if line.trim_end().is_empty() {
                break;
            }
            headers.push(line.trim_end().to_string());
        }
        let body = r#"{"data":[{"id":"local-model"}]}"#;
        let mut stream = stream;
        let _ = write!(
            stream,
            "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
            body.len()
        );
        headers
    });
    let mut s = sources(&rig);
    s.as_array_mut().unwrap().push(json!({"id": "local", "name": "Local", "template": "custom",
        "protocol": "openai-chat", "url": format!("http://127.0.0.1:{port}/v1"), "models": [{"id": "m"}]}));
    set(&rig, s);
    rig.ai.set_key("local", Some(TEST_KEY)).unwrap();
    let models = rig.ai.list_models("local").unwrap();
    assert_eq!(models[0].id, "local-model");
    let headers = seen.join().unwrap();
    assert!(headers[0].starts_with("GET /v1/models"));
    assert!(
        !headers
            .iter()
            .any(|h| h.contains(TEST_KEY) || h.to_ascii_lowercase().starts_with("authorization")),
        "no key over plain HTTP: {headers:?}"
    );
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
    let rig = rig("live", "claude", claude::BASE_URL);
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
    let s = desktop::shot(&frame, [0, 0, w, h], desktop::SHOT_MAX, 1);
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
        // As the engine does: each call parsed, only a valid action planned.
        for a in claude_calls(&doc)
            .into_iter()
            .chain(openai_actions(&doc))
            .flatten()
        {
            if validate(&a, 1280, 720).is_ok() {
                let _ = desktop::plan(&a, (1280, 720), Some((0.5, 0.5)));
            }
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

fn chat_said(text: &str) -> Value {
    json!({"choices": [{"finish_reason": "stop", "message": {"role": "assistant", "content": text}}]})
}

/// A reply after the engine restarted continues the kept conversation: a
/// new sidecar resumes it with every screenshot replaced by a note, and the
/// task keeps each run, its name and its export.
#[test]
fn a_reply_after_a_restart_continues_the_kept_conversation() {
    let server = server(vec![
        (0, chat_said("Opened the file.")),
        (0, chat_said("Saved it.")),
    ]);
    let mut rig = rig("resume", "chat", &server.url);
    let id = rig.ai.start("k1", "Open the file", None).unwrap();
    assert_eq!(rig.state(id, "done")["resumable"], true);
    let kept = rig.dir.join("ai-conversations").join(format!("{id}.json"));
    let saved = std::fs::read_to_string(&kept).expect("the conversation is kept");
    assert!(saved.contains("Opened the file.") && saved.contains("not kept"));
    assert!(!saved.contains("base64"), "no screenshot is kept");

    rig.restart(&server.url);
    assert_eq!(rig.task(id)["resumable"], true);
    rig.ai.reply(id, "Now save it").unwrap();
    let t = rig.until(id, "the reply's answer", |t| {
        t["state"] == "done" && t["text"] == "Saved it."
    });
    let runs: Vec<(&str, &str)> = t["runs"]
        .as_array()
        .unwrap()
        .iter()
        .map(|r| (r["message"].as_str().unwrap(), r["text"].as_str().unwrap()))
        .collect();
    assert_eq!(
        runs,
        [
            ("Open the file", "Opened the file."),
            ("Now save it", "Saved it.")
        ]
    );
    let requests = server.requests.lock().unwrap();
    let sent = requests[1].1["messages"].as_array().unwrap();
    assert_eq!(sent[1]["content"][0]["text"], "Open the file");
    assert_eq!(
        sent[1]["content"][1]["type"], "text",
        "the old screenshot is a note"
    );
    assert_eq!(sent[2]["content"], "Opened the file.");
    let reply = sent.last().unwrap();
    assert_eq!(reply["content"][0]["text"], "Now save it");
    assert_eq!(
        reply["content"][1]["type"], "image_url",
        "with the screen now"
    );
    drop(requests);

    rig.ai.rename(id, "Saving").unwrap();
    assert_eq!(rig.task(id)["name"], "Saving");
    let md = rig.ai.export(id).unwrap();
    assert!(md.starts_with("# Saving\n"), "{md}");
    assert!(md.contains("## You\n\nNow save it") && md.contains("## AI\n\nSaved it."));
    rig.ai.rename(id, " ").unwrap();
    assert!(
        rig.task(id).get("name").is_none(),
        "an empty name gives it back"
    );
    assert!(rig.ai.rename(id, &"x".repeat(201)).is_err());

    rig.ai.clear().unwrap();
    assert!(
        !kept.exists(),
        "clearing the history forgets the conversation"
    );
}

/// A fork begins after an earlier run, with that run's steps and answer
/// and the conversation as it stood then; a clone copies it as it is; the
/// original is untouched.
#[test]
fn forks_and_clones_continue_from_where_they_were_taken() {
    let call = json!({"choices": [{"finish_reason": "tool_calls", "message": {"role": "assistant", "content": "",
        "tool_calls": [{"id": "c1", "type": "function", "function": {"name": "left_click", "arguments": "{\"coordinate\": [640, 425]}"}}]}}]});
    let server = server(vec![
        (0, call),
        (0, chat_said("Opened the file.")),
        (0, chat_said("Saved it.")),
        (0, chat_said("Closed it instead.")),
    ]);
    let rig = rig("fork", "chat", &server.url);
    let id = rig.ai.start("k1", "Open the file", None).unwrap();
    rig.state(id, "done");
    rig.ai.reply(id, "Now save it").unwrap();
    rig.until(id, "the reply's answer", |t| {
        t["state"] == "done" && t["text"] == "Saved it."
    });

    let fork = rig.ai.fork(id, Some(0)).unwrap();
    let f = rig.task(fork);
    assert_eq!(f["state"], "done");
    assert_eq!(f["text"], "Opened the file.");
    assert_eq!(f["runs"].as_array().unwrap().len(), 1);
    assert_eq!(
        f["steps"].as_array().unwrap().len(),
        1,
        "the first run's click"
    );
    assert_eq!(f["resumable"], true);
    rig.ai.reply(fork, "Close it").unwrap();
    rig.until(fork, "the fork's answer", |t| {
        t["text"] == "Closed it instead."
    });
    let requests = server.requests.lock().unwrap();
    let sent = serde_json::to_string(&requests[3].1["messages"]).unwrap();
    assert!(sent.contains("Opened the file.") && sent.contains("Close it"));
    assert!(
        !sent.contains("Now save it"),
        "the fork never saw the later run"
    );
    drop(requests);

    let clone = rig.ai.fork(id, None).unwrap();
    let c = rig.task(clone);
    assert_eq!(c["runs"].as_array().unwrap().len(), 2);
    assert_eq!(c["text"], "Saved it.");
    assert_eq!(rig.task(id)["runs"].as_array().unwrap().len(), 2);
    assert!(rig.ai.fork(id, Some(5)).is_err(), "no such run");
}

/// Compaction by hand: the model summarises the kept conversation with its
/// tools off, and the next reply starts afresh from the summary.
#[test]
fn compaction_by_hand_continues_from_the_summary() {
    let server = server(vec![
        (0, chat_said("Opened the file.")),
        (0, chat_said("The file is open; saving is left.")),
        (0, chat_said("Saved it.")),
    ]);
    let rig = rig("compact", "chat", &server.url);
    let id = rig.ai.start("k1", "Open the file", None).unwrap();
    rig.state(id, "done");
    rig.ai.compact(id).unwrap();
    let t = rig.until(id, "the compaction", |t| t.get("compacting").is_none());
    assert!(t.get("error").is_none(), "{t:#}");
    assert_eq!(t["state"], "done");
    let kept = std::fs::read_to_string(rig.dir.join("ai-conversations").join(format!("{id}.json")))
        .unwrap();
    assert!(kept.contains("saving is left") && !kept.contains("Opened the file."));
    rig.ai.reply(id, "Now save it").unwrap();
    rig.until(id, "the reply's answer", |t| {
        t["state"] == "done" && t["text"] == "Saved it."
    });
    let requests = server.requests.lock().unwrap();
    let ask = &requests[1].1;
    assert_eq!(ask["tool_choice"], "none");
    assert!(
        ask["messages"].as_array().unwrap().last().unwrap()["content"]
            .as_str()
            .unwrap()
            .starts_with("Summarize this task")
    );
    let sent = requests[2].1["messages"].as_array().unwrap();
    assert_eq!(
        sent.len(),
        2,
        "the system prompt and the summary with the reply"
    );
    let first = sent[1]["content"][0]["text"].as_str().unwrap();
    assert!(
        first.contains("The file is open; saving is left.") && first.ends_with("Now: Now save it")
    );
}

/// A reply to a task whose last request passed the threshold compacts it
/// first, in the same run.
#[test]
fn a_long_conversation_is_compacted_before_a_reply() {
    let mut long = chat_said("Opened the file.");
    long["usage"] = json!({"prompt_tokens": 200_000, "completion_tokens": 10});
    let server = server(vec![
        (0, long),
        (0, chat_said("The file is open; saving is left.")),
        (0, chat_said("Saved it.")),
    ]);
    let rig = rig("autocompact", "chat", &server.url);
    let id = rig.ai.start("k1", "Open the file", None).unwrap();
    assert_eq!(rig.state(id, "done")["context"], 200_000);
    rig.ai.reply(id, "Now save it").unwrap();
    let t = rig.until(id, "the reply's answer", |t| {
        t["state"] == "done" && t["text"] == "Saved it."
    });
    assert_ne!(t["context"], 200_000);
    let requests = server.requests.lock().unwrap();
    assert_eq!(requests.len(), 3);
    assert_eq!(requests[1].1["tool_choice"], "none", "the summary first");
    let first = requests[2].1["messages"][1]["content"][0]["text"]
        .as_str()
        .unwrap();
    assert!(first.contains("saving is left") && first.ends_with("Now: Now save it"));
}

/// A task runs with the model and thinking chosen for it; a reply may change
/// them within its source, never to another source.
#[test]
fn a_task_keeps_its_model_and_thinking_and_a_reply_may_change_them() {
    use super::task::ModelRef;
    let server = server(vec![(0, claude_end("Opened.")), (0, claude_end("Saved."))]);
    let rig = rig("choice", "claude", &server.url);
    let choice = |source: &str, id: &str, thinking| super::Choice {
        model: Some(ModelRef {
            source: source.into(),
            id: id.into(),
        }),
        thinking,
        on: None,
    };
    let id = rig
        .ai
        .start_with(
            "k1",
            "Open the file",
            None,
            choice("claude", "claude-haiku-5-5", Some(super::Thinking::Low)),
        )
        .unwrap();
    let t = rig.state(id, "done");
    assert_eq!(
        (t["model"].as_str(), t["thinking"].as_str()),
        (Some("claude-haiku-5-5"), Some("low"))
    );
    assert!(
        rig.ai
            .reply_with(id, "Now save it", choice("openai", "gpt-x", None))
            .is_err(),
        "the conversation stays with its provider"
    );
    rig.ai
        .reply_with(
            id,
            "Now save it",
            choice("claude", "claude-opus-5-5", Some(super::Thinking::Max)),
        )
        .unwrap();
    let t = rig.until(id, "the reply's answer", |t| {
        t["state"] == "done" && t["text"] == "Saved."
    });
    assert_eq!(t["model"], "claude-opus-5-5");
    let requests = server.requests.lock().unwrap();
    let (first, second) = (&requests[0].1, &requests[1].1);
    assert_eq!(
        (
            first["model"].as_str(),
            first["output_config"]["effort"].as_str()
        ),
        (Some("claude-haiku-5-5"), Some("low"))
    );
    assert_eq!(
        (
            second["model"].as_str(),
            second["output_config"]["effort"].as_str()
        ),
        (Some("claude-opus-5-5"), Some("max"))
    );
    assert!(
        serde_json::to_string(&second["messages"])
            .unwrap()
            .contains("Opened."),
        "the conversation went on"
    );
}

/// Under No logs a kept conversation stays in memory: a reply continues it
/// while the engine runs, nothing is written, and after a restart the task
/// can no longer continue.
#[test]
fn no_logs_keeps_the_conversation_in_memory() {
    let server = server(vec![
        (0, chat_said("Opened the file.")),
        (0, chat_said("Saved it.")),
    ]);
    let mut rig = rig("resume-nolog", "chat", &server.url);
    rig.secrets
        .set(crate::access::NO_HISTORY_NAME, "true")
        .unwrap();
    let id = rig.ai.start("k1", "Open the file", None).unwrap();
    rig.state(id, "done");
    assert!(
        !rig.dir
            .join("ai-conversations")
            .join(format!("{id}.json"))
            .exists()
    );
    rig.ai.reply(id, "Now save it").unwrap();
    rig.until(id, "the reply's answer", |t| {
        t["state"] == "done" && t["text"] == "Saved it."
    });

    rig.restart(&server.url);
    assert_eq!(rig.task(id)["resumable"], false);
    assert!(rig.ai.reply(id, "And close it").is_err());
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

/// Each mode as it is chosen: by the model's id on `openai-chat` sources,
/// always pixels on Claude's and OpenAI's own tools.
#[test]
fn coordinate_modes_resolve_by_model() {
    use Coordinates::*;
    let cases = [
        (Auto, true, "google/gemini-3-flash", Grid999),
        (Auto, true, "gemini-2.5-computer-use", Grid999),
        (Auto, true, "qwen/qwen3-vl-235b-a22b-instruct", Grid1000),
        (Auto, true, "Qwen3.8-VL-27B", Grid1000),
        (Auto, true, "qwen2.5-vl-72b-instruct", SmartResize28),
        (Auto, true, "Qwen/Qwen2-VL-7B", SmartResize28),
        (Auto, true, "llava-v1.6", Pixels),
        (Auto, true, "qwen3-coder", Pixels),
        (Auto, true, "qwen/qwen3.5-flash-02-23", Grid1000),
        (Auto, true, "qwen/qwen3.8-27b", Grid1000),
        (Auto, true, "qwen/qwen3-32b", Pixels),
        (Grid1000, true, "my-model", Grid1000),
        (SmartResize32, true, "gemini-3", SmartResize32),
        (Auto, false, "gemini-3", Pixels),
        (Grid999, false, "claude-opus-5-5", Pixels),
    ];
    for (set, chat, model, want) in cases {
        assert_eq!(set.resolve(chat, model), want, "{set:?} {chat} {model}");
    }
}

/// A grid answer lands where the model means: an answer on 0–999 used as
/// pixels would land at about 73 % across and 130 % down of the real point.
#[test]
fn grid_positions_become_screenshot_pixels() {
    let size = (1366, 768);
    let click = |x, y| Action::Click {
        at: Some(p(x, y)),
        button: Button::Left,
        count: 1,
        modifiers: vec![],
    };
    // (mode, the model's point, the pixel it means)
    let cases = [
        (Coordinates::Grid999, (500, 500), (683, 384)),
        (Coordinates::Grid999, (0, 0), (0, 0)),
        (Coordinates::Grid999, (999, 999), (1365, 767)),
        (Coordinates::Grid1000, (1000, 1000), (1365, 767)),
        (Coordinates::Grid1000, (250, 750), (342, 576)),
        (Coordinates::Pixels, (500, 500), (500, 500)),
        (Coordinates::SmartResize28, (700, 300), (700, 300)),
    ];
    for (mode, (x, y), (px, py)) in cases {
        assert_eq!(
            mode.to_pixels(click(x, y), size),
            click(px, py),
            "{mode:?} {x},{y}"
        );
        assert!(validate(&mode.to_pixels(click(x, y), size), 1366, 768).is_ok());
    }
    // Past the grid is refused, not clamped onto the edge.
    for (mode, x) in [
        (Coordinates::Grid999, 1000),
        (Coordinates::Grid1000, 1001),
        (Coordinates::Grid999, -1),
    ] {
        let a = mode.to_pixels(click(x, 10), size);
        assert!(validate(&a, 1366, 768).is_err(), "{mode:?} {x}: {a:?}");
    }
    // Every position of an action is mapped.
    let drag = Action::Drag {
        path: vec![p(0, 0), p(500, 500)],
        modifiers: vec![],
    };
    assert_eq!(
        Coordinates::Grid1000.to_pixels(drag, size),
        Action::Drag {
            path: vec![p(0, 0), p(683, 384)],
            modifiers: vec![]
        }
    );
    let zoom = Action::Zoom {
        region: [0, 0, 500, 1000],
    };
    assert_eq!(
        Coordinates::Grid1000.to_pixels(zoom, size),
        Action::Zoom {
            region: [0, 0, 683, 767]
        }
    );
    // The cursor goes back in the model's unit.
    assert_eq!(
        Coordinates::Grid1000.in_model_units(683, 384, size),
        (500, 500)
    );
    assert_eq!(
        Coordinates::Grid999.in_model_units(1365, 767, size),
        (999, 999)
    );
    assert_eq!(
        Coordinates::Pixels.in_model_units(683, 384, size),
        (683, 384)
    );
}

/// A smart-resize model gets a screenshot its own resize leaves as it is.
#[test]
fn smart_resize_screenshots_have_multiple_sides() {
    let frame = Frame {
        bgra: [10, 20, 30, 255].repeat(2732 * 1536),
        width: 2732,
        height: 1536,
    };
    for (multiple, want) in [(1, (1366, 768)), (28, (1344, 756)), (32, (1344, 768))] {
        let s = desktop::shot(&frame, [0, 0, 2732, 1536], desktop::SHOT_MAX, multiple);
        assert_eq!((s.width, s.height), want, "multiple {multiple}");
    }
}

/// A grid mode belongs to `openai-chat` sources only.
#[test]
fn grid_coordinates_need_a_chat_source() {
    let rig = rig("coords-native", "claude", "http://127.0.0.1:9");
    let mut s = rig.ai.settings();
    s["sources"][0]["models"][0]["coordinates"] = json!("0-999");
    assert!(
        rig.ai
            .set_settings(&json!({"sources": s["sources"]}))
            .is_err()
    );
    let rig = rig_with_model(
        "coords-chat",
        "http://127.0.0.1:9",
        json!({"id": "m", "coordinates": "0-1000"}),
    );
    assert_eq!(
        rig.ai.settings()["sources"][2]["models"][0]["coordinates"],
        "0-1000"
    );
}

/// A rig whose `openai-chat` source has this one model.
fn rig_with_model(name: &str, url: &str, model: Value) -> Rig {
    let rig = rig(name, "chat", url);
    let mut s = rig.ai.settings();
    let id = model["id"].clone();
    s["sources"][2]["models"] = json!([model]);
    rig.ai
        .set_settings(&json!({"sources": s["sources"], "model": {"source": "chat", "id": id}}))
        .unwrap();
    rig
}

/// Gemini (0–999), Qwen3-VL (0–1000) and Qwen2.5-VL (smart resize 28)
/// press the test screen's SAVE button (`screens::BUTTON`, 560–720 ×
/// 400–450 of 1280×720) by their own units, through `openai-chat`.
#[test]
fn grid_models_click_where_they_mean() {
    let call = |args: &str| {
        json!({"choices": [{"finish_reason": "tool_calls", "message": {"role": "assistant",
        "content": "", "tool_calls": [{"id": "c1", "type": "function", "function": {"name": "left_click", "arguments": args}}]}}]})
    };
    let done = json!({"choices": [{"finish_reason": "stop", "message": {"role": "assistant", "content": "Saved."}}]});
    // (model, its answer for the button's middle, the sentence its prompt has)
    let cases = [
        (
            "google/gemini-3-flash",
            "{\"coordinate\": [500, 590]}",
            "0-999 grid",
        ),
        (
            "qwen/qwen3-vl-32b-instruct",
            "{\"coordinate\": [500, 590]}",
            "0-1000 grid",
        ),
        (
            "qwen2.5-vl-72b-instruct",
            "{\"coordinate\": [630, 413]}",
            "Screenshots are 1260x700 pixels",
        ),
    ];
    for (model, answer, prompt) in cases {
        let server = server(vec![(0, call(answer)), (0, done.clone())]);
        let rig = rig_with_model(
            &format!("grid-{}", model.len()),
            &server.url,
            json!({"id": model}),
        );
        let id = rig.ai.start("k", "Save the file", None).unwrap();
        rig.state(id, "done");
        assert!(rig.log.lock().unwrap().pressed, "{model} pressed SAVE");
        let first = &server.requests.lock().unwrap()[0].1;
        assert!(
            first["messages"][0]["content"]
                .as_str()
                .unwrap()
                .contains(prompt),
            "{model}: {}",
            first["messages"][0]["content"]
        );
    }
    // The same answer taken as pixels misses the button.
    let server = server(vec![(0, call("{\"coordinate\": [500, 590]}")), (0, done)]);
    let rig = rig_with_model(
        "grid-pixels",
        &server.url,
        json!({"id": "m", "coordinates": "pixels"}),
    );
    let id = rig.ai.start("k", "Save the file", None).unwrap();
    rig.state(id, "done");
    assert!(
        !rig.log.lock().unwrap().pressed,
        "as pixels it lands elsewhere"
    );
}

/// An `openai-chat` task sees the element list beside its screenshot and
/// clicks an element by number, which lands on its centre; a second number
/// in the same turn is refused as stale; an app outside the scope is never
/// listed; a Claude source lists nothing unless turned on.
#[test]
fn elements_are_listed_and_clicked_by_number() {
    use super::elements::{Element, Listed};
    let click = |id: &str| json!({"type": "function", "id": id, "function": {"name": "left_click", "arguments": "{\"element\": 1}"}});
    let turn = json!({"choices": [{"finish_reason": "tool_calls", "message": {"role": "assistant",
        "content": "", "tool_calls": [click("c1"), click("c2")]}}]});
    let done = json!({"choices": [{"finish_reason": "stop", "message": {"role": "assistant", "content": "Saved."}}]});
    let srv = server(vec![(0, turn), (0, done.clone())]);
    let one = rig_with_model(
        "elements",
        &srv.url,
        json!({"id": "m", "coordinates": "pixels"}),
    );
    let b = screens::BUTTON;
    one.log.lock().unwrap().elements = Some(Listed {
        window: "Save".into(),
        program: Some("notepad.exe".into()),
        elements: vec![Element {
            role: "button".into(),
            name: "SAVE".into(),
            text: String::new(),
            rect: [b[0] as i32, b[1] as i32, b[2] as i32, b[3] as i32],
        }],
    });
    let id = one.ai.start("k", "Save the file", None).unwrap();
    let t = one.state(id, "done");
    assert!(
        one.log.lock().unwrap().pressed,
        "the element's centre is SAVE"
    );
    assert_eq!(t["steps"][0]["action"]["at"], json!({"x": 639, "y": 424}));
    assert_eq!(t["steps"][1]["outcome"], "failed");
    assert!(t["steps"][1]["detail"].as_str().unwrap().contains("stale"));
    let requests = srv.requests.lock().unwrap();
    let first = requests[0].1.to_string();
    assert!(first.contains("Elements on screen"), "{first}");
    assert!(
        first.contains("1 button \\\"SAVE\\\" [560,400,719,449]"),
        "{first}"
    );
    assert!(requests[0].1["tools"].to_string().contains("\"element\""));
    drop(requests);

    // Outside the granted apps nothing is listed.
    let srv2 = server(vec![(0, done)]);
    let rig2 = rig_with_model("elements-scope", &srv2.url, json!({"id": "m"}));
    rig2.log.lock().unwrap().elements = one.log.lock().unwrap().elements.clone();
    rig2.ai.set_scope(Some(vec!["code.exe".into()])).unwrap();
    let id = rig2.ai.start("k", "Save the file", None).unwrap();
    rig2.state(id, "done");
    assert!(
        !srv2.requests.lock().unwrap()[0]
            .1
            .to_string()
            .contains("Elements on screen")
    );

    // Claude: off by default.
    let srv3 = server(vec![(0, claude_end("ok"))]);
    let rig3 = rig("elements-claude", "claude", &srv3.url);
    rig3.log.lock().unwrap().elements = one.log.lock().unwrap().elements.clone();
    let id = rig3.ai.start("k", "Save the file", None).unwrap();
    rig3.state(id, "done");
    assert!(
        !srv3.requests.lock().unwrap()[0]
            .1
            .to_string()
            .contains("Elements on screen")
    );
}

/// A task on a pane the GUI serves sees the pane's picture, asks before
/// acting there (a Remote Desktop window to the policy), and its click goes
/// to the pane, not this computer's desktop; this computer's apps scope
/// does not dim the pane; a controller cannot choose a pane; closing the
/// pane fails the task's next look.
#[test]
fn a_task_works_in_a_served_pane() {
    use super::pane::Served;
    let turns = vec![(0, claude_turn("m1", click_save())), (0, claude_end("ok"))];
    let server = server(turns);
    let rig = rig("pane", "claude", &server.url);
    rig.ai.set_scope(Some(vec!["code.exe".into()])).unwrap();
    rig.ai
        .open_pane(Served {
            id: "p1".into(),
            name: "Remote Desktop · NUC".into(),
            kind: "rdp".into(),
        })
        .unwrap();
    assert_eq!(
        rig.ai.settings()["panes"][0]["name"],
        "Remote Desktop · NUC"
    );
    let on = |id: &str| super::Choice {
        on: Some(id.into()),
        ..Default::default()
    };
    assert!(
        rig.ai
            .start_with("c", "Save", Some("controller"), on("p1"))
            .is_err()
    );
    assert!(rig.ai.start_with("k0", "Save", None, on("gone")).is_err());
    // The GUI's side: the synthetic screen, SAVE pressed by a click on it.
    let pane = rig.ai.pane("p1").unwrap();
    let gui = std::thread::spawn(move || {
        let mut pressed = false;
        let mut inputs = Vec::new();
        while let Some(r) = (0..100).find_map(|_| pane.next(Duration::from_millis(100))) {
            let seq = r["seq"].as_u64().unwrap();
            let answer = match r["request"]["op"].as_str().unwrap() {
                "size" => json!({"width": screens::WIDTH, "height": screens::HEIGHT}),
                "capture" => {
                    let frame = screens::screen(pressed).frame();
                    json!({"png": super::desktop::shot(&frame, [0, 0, frame.width, frame.height], (frame.width, frame.height), 1).b64})
                }
                "input" => {
                    let e = &r["request"]["event"];
                    if e["kind"] == "pointer" && e["action"] == "down" {
                        pressed = true;
                    }
                    inputs.push(e.clone());
                    json!({})
                }
                _ => json!({}),
            };
            pane.answer(seq, answer);
        }
        inputs
    });
    let id = rig
        .ai
        .start_with("k", "Save the file", None, on("p1"))
        .unwrap();
    let t = rig.state(id, "approval");
    assert_eq!(t["approval"]["class"], "remote-desktop");
    assert_eq!(t["pane"]["name"], "Remote Desktop · NUC");
    rig.ai.decide(id, true).unwrap();
    rig.state(id, "done");
    // Nothing reached this computer's desktop; the first picture was not
    // dimmed by this computer's apps scope.
    assert!(rig.log.lock().unwrap().events.is_empty());
    let first = &server.requests.lock().unwrap()[0].1;
    let png = first["messages"][0]["content"][0]["source"]["data"]
        .as_str()
        .unwrap();
    let frame = super::pane::decode_png(png).unwrap();
    let mid = ((screens::HEIGHT / 2 * screens::WIDTH + screens::WIDTH / 2) * 4) as usize;
    let shown = screens::screen(false).frame();
    assert_eq!(&frame.bgra[mid..mid + 3], &shown.bgra[mid..mid + 3]);
    rig.ai.close_pane("p1");
    let inputs = gui.join().unwrap();
    assert!(
        inputs
            .iter()
            .any(|e| e["kind"] == "pointer" && e["action"] == "down")
    );
    assert!(rig.ai.settings()["panes"].as_array().unwrap().is_empty());
}

/// Beside the host user: the task sees its window alone, clicks SAVE through
/// the element (no input sent), is not paused by the host user's input in
/// another window, pauses with a reason for a key it cannot press in the
/// background, and after the resume presses it in front; where no window
/// can be worked beside, it says so once and works with the screen.
#[test]
fn a_task_works_beside_the_host_user() {
    use super::desktop::{Beside, BesideAct};
    use super::elements::{Element, Listed};
    let key = json!([{"type": "tool_use", "id": "k1", "name": "key", "toolset_name": "computer", "input": {"text": "ctrl+s"}}]);
    let turns = vec![
        (0, claude_turn("m1", click_save())),
        (0, claude_turn("m2", key.clone())),
        (0, claude_turn("m3", key)),
        (0, claude_end("ok")),
    ];
    let srv0 = server(turns);
    let one = rig("beside", "claude", &srv0.url);
    assert!(
        one.ai.set_beside(true).is_err(),
        "beside needs apps granted"
    );
    one.ai.set_scope(Some(vec!["notepad.exe".into()])).unwrap();
    one.ai.set_beside(true).unwrap();
    assert_eq!(one.ai.settings()["beside"], true);
    let b = screens::BUTTON;
    {
        let mut log = one.log.lock().unwrap();
        log.beside = Some(Beside {
            id: 7,
            program: "notepad.exe".into(),
            rect: [0, 0, screens::WIDTH as i32, screens::HEIGHT as i32],
        });
        log.elements = Some(Listed {
            window: "Save".into(),
            program: Some("notepad.exe".into()),
            elements: vec![Element {
                role: "button".into(),
                name: "SAVE".into(),
                text: String::new(),
                rect: [b[0] as i32, b[1] as i32, b[2] as i32, b[3] as i32],
            }],
        });
        // The host user works in another window.
        log.front = Some(99);
    }
    let id = one.ai.start("k", "Save the file", None).unwrap();
    one.log.lock().unwrap().press_at = Some(Instant::now());
    let t = one.state(id, "paused");
    assert_eq!(t["paused"], "beside");
    assert!(
        t["note"]
            .as_str()
            .unwrap()
            .contains("pointer or the keyboard")
    );
    {
        let log = one.log.lock().unwrap();
        assert!(log.pressed, "SAVE through its element");
        assert_eq!(log.acted, vec![(0, BesideAct::Click)]);
        assert!(log.events.is_empty(), "no input sent beside the host user");
    }
    one.ai.resume(id).unwrap();
    one.state(id, "done");
    let log = one.log.lock().unwrap();
    assert_eq!(log.raised, 1, "the key step in front");
    assert!(!log.events.is_empty(), "the key pressed with the keyboard");
    drop(log);
    // Changing the apps turns it off.
    one.ai.set_scope(None).unwrap();
    assert_eq!(one.ai.settings()["beside"], false);

    // No window to work beside here: once paused, then the screen.
    let srv = server(vec![
        (0, claude_turn("m1", click_save())),
        (0, claude_end("ok")),
    ]);
    let none = rig("beside-none", "claude", &srv.url);
    none.ai.set_scope(Some(vec!["notepad.exe".into()])).unwrap();
    none.ai.set_beside(true).unwrap();
    let id = none.ai.start("k", "Save the file", None).unwrap();
    let t = none.state(id, "paused");
    assert_eq!(t["paused"], "beside");
    assert!(
        t["note"]
            .as_str()
            .unwrap()
            .contains("cannot work beside you here")
    );
    none.ai.resume(id).unwrap();
    none.state(id, "done");
}

/// A target the fake desktop reports from now on.
fn set_target(rig: &Rig, target: super::policy::Target) {
    rig.log.lock().unwrap().target = Some(target);
}

/// The lock screen pauses the task before anything acts; a credential
/// prompt is refused, and the model is told so.
#[test]
fn secure_surfaces_pause_or_refuse() {
    use super::policy::{Surface, Target};
    let server = server(vec![
        (0, claude_turn("m1", click_save())),
        (0, claude_end("ok")),
    ]);
    let rig = rig("locked", "claude", &server.url);
    set_target(
        &rig,
        Target {
            surface: Surface::Locked,
            ..Default::default()
        },
    );
    let id = rig.ai.start("k", "Save the file", None).unwrap();
    let t = rig.until(id, "the pause", |t| t["state"] == "paused");
    assert_eq!(t["paused"], "locked");
    assert!(
        !rig.log.lock().unwrap().pressed,
        "nothing pressed on the lock screen"
    );

    let server = super::fake::server(vec![
        (0, claude_turn("m1", click_save())),
        (0, claude_end("ok")),
    ]);
    let rig = super::fake::rig("credentials", "claude", &server.url);
    set_target(
        &rig,
        Target {
            surface: Surface::Credentials,
            ..Default::default()
        },
    );
    let id = rig.ai.start("k", "Save the file", None).unwrap();
    let t = rig.state(id, "done");
    assert_eq!(t["steps"][0]["outcome"], "refused");
    assert!(!rig.log.lock().unwrap().pressed);
    let answer = server.requests.lock().unwrap()[1].1.to_string();
    assert!(
        answer.contains("Refused by the host"),
        "the model hears why"
    );
}

/// Auto asks for a class it knows, says which, and runs the rest; three
/// refusals in one task leave it asking before every action.
#[test]
fn auto_asks_by_class_and_falls_back_after_refusals() {
    use super::policy::Target;
    let server = server(vec![
        (0, claude_turn("m1", click_save())),
        (0, claude_end("ok")),
    ]);
    let rig = rig("auto-vault", "claude", &server.url);
    set_target(
        &rig,
        Target {
            program: Some("keepassxc.exe".into()),
            ..Default::default()
        },
    );
    let id = rig.ai.start("k", "Save the file", None).unwrap();
    let t = rig.state(id, "approval");
    assert_eq!(t["approval"]["class"], "password-vault");
    rig.ai.decide(id, true).unwrap();
    rig.state(id, "done");
    assert!(rig.log.lock().unwrap().pressed);

    // Three declines, then even a plain editor asks.
    let decline = |rig: &Rig, id| {
        rig.until(id, "an approval", |t| t["state"] == "approval");
        rig.ai.decide(id, false).unwrap();
    };
    let turns = (0..4)
        .map(|i| (0, claude_turn(&format!("m{i}"), click_save())))
        .chain([(0, claude_end("ok"))]);
    let server = super::fake::server(turns.collect());
    let rig = super::fake::rig("auto-fallback", "claude", &server.url);
    set_target(
        &rig,
        Target {
            program: Some("1password.exe".into()),
            ..Default::default()
        },
    );
    let id = rig.ai.start("k", "Save the file", None).unwrap();
    for _ in 0..3 {
        decline(&rig, id);
    }
    set_target(
        &rig,
        Target {
            program: Some("notepad.exe".into()),
            ..Default::default()
        },
    );
    let t = rig.until(id, "the fallback's approval", |t| t["state"] == "approval");
    assert_eq!(t["approval"]["class"], "every-action");
}

/// A task sums the tokens its turns took, as each provider reports them
/// (Claude counts cache reads and writes apart from `input_tokens`).
#[test]
fn tasks_count_their_tokens() {
    let mut first = claude_turn("m1", click_save());
    first["usage"] = json!({"input_tokens": 50, "cache_creation_input_tokens": 3000, "cache_read_input_tokens": 0, "output_tokens": 200});
    let mut last = claude_end("ok");
    last["usage"] = json!({"input_tokens": 80, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 3000, "output_tokens": 20});
    let server = server(vec![(0, first), (0, last)]);
    let rig = rig("tokens", "claude", &server.url);
    let id = rig.ai.start("k", "Save the file", None).unwrap();
    let t = rig.state(id, "done");
    assert_eq!(
        t["tokens"],
        json!({"input": 6130, "cached": 3000, "output": 220})
    );

    let call = |args: &str| {
        json!({"choices": [{"finish_reason": "tool_calls", "message": {"role": "assistant", "content": "",
        "tool_calls": [{"id": "c1", "type": "function", "function": {"name": "left_click", "arguments": args}}]}}],
        "usage": {"prompt_tokens": 1500, "prompt_tokens_details": {"cached_tokens": 0}, "completion_tokens": 30}})
    };
    let done = json!({"choices": [{"finish_reason": "stop", "message": {"role": "assistant", "content": "Saved."}}],
        "usage": {"prompt_tokens": 3100, "prompt_tokens_details": {"cached_tokens": 1500}, "completion_tokens": 5}});
    let server = super::fake::server(vec![(0, call("{\"coordinate\": [640, 425]}")), (0, done)]);
    let rig = super::fake::rig("tokens-chat", "chat", &server.url);
    let id = rig.ai.start("k", "Save the file", None).unwrap();
    let t = rig.state(id, "done");
    assert_eq!(
        t["tokens"],
        json!({"input": 4600, "cached": 1500, "output": 35})
    );
}

/// AI scope dims what the granted programs' windows do not show, by
/// stacking order: a granted window's covered part goes dark too.
#[test]
fn scope_dims_outside_the_granted_windows() {
    use super::desktop::{Shown, dim_outside};
    let mut f = Frame {
        bgra: [200, 200, 200, 255].repeat(100 * 50),
        width: 100,
        height: 50,
    };
    let win = |p: &str, rect| Shown {
        program: Some(p.into()),
        rect,
    };
    // Topmost first: a terminal over the editor's right half.
    let windows = [
        win("cmd.exe", [60, 0, 100, 50]),
        win("code.exe", [0, 0, 80, 50]),
        win("explorer.exe", [0, 0, 100, 50]),
    ];
    dim_outside(&mut f, &windows, &["code.exe".into()]);
    let at = |x: usize, y: usize| f.bgra[(y * 100 + x) * 4];
    assert_eq!(at(10, 10), 200, "the granted editor shows");
    assert_eq!(at(70, 10), 50, "its part under the terminal goes dark");
    assert_eq!(at(90, 10), 50, "the terminal goes dark");
    let mut all = Frame {
        bgra: [200, 200, 200, 255].repeat(100 * 50),
        width: 100,
        height: 50,
    };
    dim_outside(&mut all, &[], &["code.exe".into()]);
    assert_eq!(
        all.bgra[(10 * 100 + 10) * 4],
        50,
        "no window list: nothing shows"
    );
    assert_eq!(all.bgra[(10 * 100 + 10) * 4 + 3], 255, "alpha untouched");
}

/// With a scope, a click on another program is refused and the model told
/// to stay in the granted apps; the granted one's runs.
#[test]
fn scope_refuses_other_programs() {
    use super::policy::Target;
    let turns = vec![(0, claude_turn("m1", click_save())), (0, claude_end("ok"))];
    let server = server(turns);
    let rig = rig("scope", "claude", &server.url);
    rig.ai.set_scope(Some(vec!["Code.exe".into()])).unwrap();
    assert_eq!(rig.ai.settings()["scope"], json!(["code.exe"]));
    set_target(
        &rig,
        Target {
            program: Some("notepad.exe".into()),
            ..Default::default()
        },
    );
    let id = rig.ai.start("k", "Save the file", None).unwrap();
    let t = rig.state(id, "done");
    assert_eq!(t["steps"][0]["outcome"], "refused");
    assert!(!rig.log.lock().unwrap().pressed);
    assert!(
        server.requests.lock().unwrap()[1]
            .1
            .to_string()
            .contains("outside the apps")
    );
}

/// With folders granted, a file changed outside them while the AI acts
/// pauses the task before its next action and stays on its record; changes
/// inside them and bookkeeping (a hidden folder) do not; a full path is
/// required.
#[test]
fn folders_pause_on_changes_outside() {
    let turns = vec![
        (0, claude_turn("m1", click_save())),
        (1500, claude_turn("m2", click_save())),
        (0, claude_end("ok")),
    ];
    let server = server(turns);
    let rig = rig("folders", "claude", &server.url);
    let home = std::env::temp_dir().join(format!("kyokai-home-{}", std::process::id()));
    let granted = home.join("Work");
    std::fs::create_dir_all(&granted).unwrap();
    std::fs::create_dir_all(home.join(".cache")).unwrap();
    assert!(rig.ai.set_folders(Some(vec!["Work".into()])).is_err());
    rig.ai.watch_home(&home);
    rig.ai
        .set_folders(Some(vec![granted.to_string_lossy().into()]))
        .unwrap();
    assert_eq!(rig.ai.settings()["folders"], json!([granted]));
    let t0 = Instant::now();
    while !rig.ai.watching() {
        assert!(
            t0.elapsed() < Duration::from_secs(10),
            "the watch did not start"
        );
        std::thread::sleep(Duration::from_millis(20));
    }
    let id = rig.ai.start("k", "Save the file", None).unwrap();
    while rig.task(id)["steps"]
        .as_array()
        .is_none_or(|s| s.is_empty())
    {
        assert!(t0.elapsed() < Duration::from_secs(20), "no first step");
        std::thread::sleep(Duration::from_millis(20));
    }
    std::fs::write(granted.join("report.txt"), "in").unwrap();
    std::fs::write(home.join(".cache").join("x"), "noise").unwrap();
    let secret = home.join("secret.txt");
    std::fs::write(&secret, "out").unwrap();
    let t = rig.state(id, "paused");
    assert_eq!(t["paused"], "folders");
    assert_eq!(t["outside"], json!([secret]));
    let paused = t["steps"]
        .as_array()
        .unwrap()
        .iter()
        .find(|s| s["outcome"] == "paused")
        .unwrap()
        .clone();
    assert!(paused["detail"].as_str().unwrap().contains("secret.txt"));
    rig.ai.cancel(id).unwrap();
    rig.ai.set_folders(None).unwrap();
    assert!(!rig.ai.watching());
    let _ = std::fs::remove_dir_all(&home);
}

/// Real `openai-chat` round trips through OpenRouter on the synthetic
/// screens (nothing touches the real desktop): each model of
/// `KYOKAI_LIVE_MODELS` (comma-separated) must press SAVE in its own
/// coordinate unit. Runs with `KYOKAI_LIVE_OPENROUTER=1` and
/// `OPENROUTER_API_KEY`; `KYOKAI_LIVE_COORDS` fixes the mode (else Auto).
#[test]
fn live_openrouter_models_click_where_they_mean() {
    let (Ok("1"), Ok(key), Ok(models)) = (
        std::env::var("KYOKAI_LIVE_OPENROUTER").as_deref(),
        std::env::var("OPENROUTER_API_KEY"),
        std::env::var("KYOKAI_LIVE_MODELS"),
    ) else {
        eprintln!(
            "skipped: set KYOKAI_LIVE_OPENROUTER=1, OPENROUTER_API_KEY and KYOKAI_LIVE_MODELS"
        );
        return;
    };
    let coords = std::env::var("KYOKAI_LIVE_COORDS").unwrap_or_default();
    let mut failed = Vec::new();
    for (i, model) in models.split(',').map(str::trim).enumerate() {
        let mut m = json!({"id": model});
        if !coords.is_empty() {
            m["coordinates"] = json!(coords);
        }
        let rig = rig_with_model(&format!("live-or-{i}"), "https://openrouter.ai/api/v1", m);
        rig.ai.set_key("chat", Some(&key)).unwrap();
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
            if t["state"] == "done"
                || t["state"] == "failed"
                || t0.elapsed() > Duration::from_secs(180)
            {
                let _ = rig.ai.cancel(id);
                break t;
            }
            std::thread::sleep(Duration::from_millis(200));
        };
        let pressed = rig.log.lock().unwrap().pressed;
        let clicks: Vec<_> = t["steps"]
            .as_array()
            .into_iter()
            .flatten()
            .map(|s| s["action"].clone())
            .collect();
        eprintln!(
            "{model}: pressed {pressed}, state {}, tokens {}, error {}, actions {}",
            t["state"],
            t["tokens"],
            t["error"],
            json!(clicks)
        );
        if !pressed {
            failed.push(model.to_string());
        }
    }
    assert!(failed.is_empty(), "did not press SAVE: {failed:?}");
}
