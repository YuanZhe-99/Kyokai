//! `cargo run -- --live` runs the two-turn round trip against each provider whose key is set.
//! Nothing is executed on a desktop. Keys come only from the environment (or an `ant` profile)
//! and are never printed, logged or written.

use std::path::{Path, PathBuf};
use std::process::ExitCode;
use std::time::Duration;

use reqwest::blocking::Client;
use serde_json::{json, Value};

use s4_providers::action::{validate, Action, ParseError};
use s4_providers::scrub::Scrubber;
use s4_providers::{claude, openai, screens};

const TASK: &str = "Click the SAVE button in the dialog, then confirm that the file was saved.";

struct Opts {
    live: bool,
    provider: String,
    openai_model: String,
    claude_model: String,
    openai_shot_detail: Option<String>,
    openai_input_detail: String,
    ack_safety: bool,
    screens_dir: Option<PathBuf>,
}

fn parse_args() -> Result<Opts, String> {
    let mut o = Opts {
        live: false,
        provider: "all".into(),
        openai_model: std::env::var("KYOKAI_OPENAI_MODEL").unwrap_or_else(|_| openai::DEFAULT_MODEL.into()),
        claude_model: std::env::var("KYOKAI_CLAUDE_MODEL").unwrap_or_else(|_| claude::DEFAULT_MODEL.into()),
        openai_shot_detail: None,
        openai_input_detail: "original".into(),
        ack_safety: false,
        screens_dir: None,
    };
    let mut args = std::env::args().skip(1);
    while let Some(a) = args.next() {
        let mut val = |name: &str| args.next().ok_or(format!("{name} needs a value"));
        match a.as_str() {
            "--live" => o.live = true,
            "--provider" => o.provider = val("--provider")?,
            "--openai-model" => o.openai_model = val("--openai-model")?,
            "--claude-model" => o.claude_model = val("--claude-model")?,
            "--openai-screenshot-detail" => o.openai_shot_detail = Some(val("--openai-screenshot-detail")?),
            "--openai-input-detail" => o.openai_input_detail = val("--openai-input-detail")?,
            "--ack-safety" => o.ack_safety = true,
            "--screens" => o.screens_dir = Some(val("--screens")?.into()),
            "-h" | "--help" => return Err(String::new()),
            other => return Err(format!("unknown argument {other}")),
        }
    }
    if !["all", "openai", "claude"].contains(&o.provider.as_str()) {
        return Err("--provider must be all, openai or claude".into());
    }
    Ok(o)
}

const USAGE: &str = "usage: s4-providers [--screens DIR] [--live [--provider all|openai|claude]
       [--openai-model M] [--claude-model M] [--openai-input-detail original|auto|high|low]
       [--openai-screenshot-detail D] [--ack-safety]]
Without --live nothing is sent. Keys: OPENAI_API_KEY, ANTHROPIC_API_KEY (or an active `ant` profile).";

fn main() -> ExitCode {
    let opts = match parse_args() {
        Ok(o) => o,
        Err(e) => {
            if !e.is_empty() {
                eprintln!("{e}");
            }
            eprintln!("{USAGE}");
            return ExitCode::from(2);
        }
    };
    let before = screens::screen(false);
    let after = screens::screen(true);
    if let Some(dir) = &opts.screens_dir {
        std::fs::create_dir_all(dir).expect("create screens dir");
        std::fs::write(dir.join("screen1.png"), before.png()).expect("write screen1");
        std::fs::write(dir.join("screen2.png"), after.png()).expect("write screen2");
        println!("wrote {}/screen1.png and screen2.png", dir.display());
    }
    if !opts.live {
        println!("{USAGE}");
        return ExitCode::SUCCESS;
    }
    let shots = Shots { before: screens::b64(&before.png()), after: screens::b64(&after.png()), after_img: after };
    let http = Client::builder().timeout(Duration::from_secs(600)).build().expect("http client");
    let out = Path::new(env!("CARGO_MANIFEST_DIR")).join("fixtures").join("live");
    let mut ok = true;
    if opts.provider != "claude" {
        ok &= run_openai(&http, &opts, &shots, &out.join("openai"));
    }
    if opts.provider != "openai" {
        ok &= run_claude(&http, &opts, &shots, &out.join("claude"));
    }
    if ok { ExitCode::SUCCESS } else { ExitCode::FAILURE }
}

struct Shots {
    before: String,
    after: String,
    after_img: screens::Rgb,
}

fn env_secret(name: &str) -> Option<String> {
    std::env::var(name).ok().map(|s| s.trim().to_string()).filter(|s| !s.is_empty())
}

struct Recorder<'a> {
    dir: &'a Path,
    scrub: Scrubber<'a>,
}

impl Recorder<'_> {
    fn save(&self, name: &str, v: &Value) {
        std::fs::create_dir_all(self.dir).expect("create fixture dir");
        let path = self.dir.join(format!("{name}.json"));
        let body = serde_json::to_string_pretty(&self.scrub.scrub(v)).expect("json");
        std::fs::write(&path, body + "\n").expect("write fixture");
        println!("  saved {}", path.display());
    }
}

/// POSTs `body`; returns the parsed JSON on 2xx, records and prints the error otherwise.
fn post(rec: &Recorder, name: &str, req: reqwest::blocking::RequestBuilder, body: &Value) -> Option<Value> {
    rec.save(&format!("{name}.request"), body);
    let resp = match req.json(body).send() {
        Ok(r) => r,
        Err(e) => {
            eprintln!("  {name}: request failed: {}", e.without_url());
            return None;
        }
    };
    let status = resp.status();
    let request_id = ["x-request-id", "request-id"]
        .iter()
        .find_map(|h| resp.headers().get(*h).and_then(|v| v.to_str().ok()).map(str::to_string));
    let text = resp.text().unwrap_or_default();
    let v: Value = serde_json::from_str(&text).unwrap_or_else(|_| json!({"non_json_body": text}));
    println!("  {name}: HTTP {status}{}", request_id.map(|r| format!(" (request id {r})")).unwrap_or_default());
    if status.is_success() {
        rec.save(&format!("{name}.response"), &v);
        Some(v)
    } else {
        rec.save(&format!("{name}.error"), &json!({"status": status.as_u16(), "body": v}));
        eprintln!("  {name}: {}", serde_json::to_string_pretty(&rec.scrub.scrub(&v)).unwrap_or_default());
        None
    }
}

fn show_actions<'a>(actions: impl Iterator<Item = &'a Result<Action, ParseError>>) {
    for (i, a) in actions.enumerate() {
        match a {
            Ok(a) => match validate(a, screens::WIDTH as i32, screens::HEIGHT as i32) {
                Ok(()) => println!("    [{i}] {a:?}"),
                Err(e) => println!("    [{i}] {a:?}  REJECTED: {e}"),
            },
            Err(e) => println!("    [{i}] PARSE ERROR: {e}"),
        }
    }
}

fn run_openai(http: &Client, o: &Opts, s: &Shots, dir: &Path) -> bool {
    println!("== OpenAI ({}, {})", openai::URL, o.openai_model);
    let Some(key) = env_secret("OPENAI_API_KEY") else {
        println!("  skipped: OPENAI_API_KEY is not set");
        return true;
    };
    let rec = Recorder {
        dir,
        scrub: Scrubber {
            images: vec![(s.before.clone(), "SCREENSHOT_1"), (s.after.clone(), "SCREENSHOT_2")],
            secrets: vec![key.clone()],
        },
    };
    let send = |name: &str, body: &Value| post(&rec, name, http.post(openai::URL).bearer_auth(&key), body);

    let req1 = openai::first_request(&o.openai_model, TASK, &s.before, &o.openai_input_detail);
    let Some(resp1) = send("turn1", &req1) else { return false };
    let turn1 = match openai::parse_response(&resp1) {
        Ok(t) => t,
        Err(e) => {
            eprintln!("  turn1: unparseable: {e}");
            return false;
        }
    };
    println!("  turn1: status={} reasoning_items={} computer_calls={}", turn1.status, turn1.reasoning_items, turn1.calls.len());
    for c in &turn1.calls {
        println!("   call {} legacy_action={} safety_checks={}", c.call_id, c.used_legacy_action, c.pending_safety_checks.len());
        show_actions(c.actions.iter());
    }
    if !turn1.text.is_empty() {
        println!("  turn1 text: {}", turn1.text);
    }
    if turn1.calls.is_empty() {
        eprintln!("  turn1 returned no computer_call; no round trip to verify");
        return false;
    }
    if turn1.calls.iter().any(|c| !c.pending_safety_checks.is_empty()) && !o.ack_safety {
        println!("  stopping: pending safety checks (rerun with --ack-safety to acknowledge them)");
        return true;
    }
    let req2 = openai::second_request(&o.openai_model, &turn1, &s.after, o.openai_shot_detail.as_deref(), o.ack_safety);
    let Some(resp2) = send("turn2", &req2) else { return false };
    match openai::parse_response(&resp2) {
        Ok(t) => {
            println!("  turn2: status={} computer_calls={} text={:?}", t.status, t.calls.len(), t.text);
            for c in &t.calls {
                show_actions(c.actions.iter());
            }
            true
        }
        Err(e) => {
            eprintln!("  turn2: unparseable: {e}");
            false
        }
    }
}

enum ClaudeAuth {
    ApiKey(String),
    Bearer(String),
}

fn claude_auth() -> Option<ClaudeAuth> {
    if let Some(k) = env_secret("ANTHROPIC_API_KEY") {
        return Some(ClaudeAuth::ApiKey(k));
    }
    if let Some(t) = env_secret("ANTHROPIC_AUTH_TOKEN") {
        return Some(ClaudeAuth::Bearer(t));
    }
    // An active `ant auth login` profile; the token is captured, never echoed.
    let out = std::process::Command::new("ant")
        .args(["auth", "print-credentials", "--access-token"])
        .stderr(std::process::Stdio::null())
        .output()
        .ok()?;
    let token = String::from_utf8(out.stdout).ok()?.trim().to_string();
    (out.status.success() && !token.is_empty()).then_some(ClaudeAuth::Bearer(token))
}

fn run_claude(http: &Client, o: &Opts, s: &Shots, dir: &Path) -> bool {
    println!("== Claude ({}, {})", claude::URL, o.claude_model);
    let Some(auth) = claude_auth() else {
        println!("  skipped: ANTHROPIC_API_KEY is not set and no active `ant` profile");
        return true;
    };
    let secret = match &auth {
        ClaudeAuth::ApiKey(k) | ClaudeAuth::Bearer(k) => k.clone(),
    };
    let rec = Recorder {
        dir,
        scrub: Scrubber {
            images: vec![(s.before.clone(), "SCREENSHOT_1"), (s.after.clone(), "SCREENSHOT_2")],
            secrets: vec![secret],
        },
    };
    let request = || {
        let r = http.post(claude::URL).header("anthropic-version", claude::VERSION);
        match &auth {
            ClaudeAuth::ApiKey(k) => r.header("x-api-key", k).header("anthropic-beta", claude::BETAS),
            ClaudeAuth::Bearer(t) => {
                r.bearer_auth(t).header("anthropic-beta", format!("{},{}", claude::BETAS, claude::OAUTH_BETA))
            }
        }
    };

    let req1 = claude::first_request(&o.claude_model, TASK, &s.before);
    let Some(resp1) = post(&rec, "turn1", request(), &req1) else { return false };
    let Some(turn1) = report_claude("turn1", &resp1) else { return false };
    if turn1.stop_reason != "tool_use" || turn1.calls.is_empty() {
        eprintln!("  turn1 did not stop on tool_use with member calls; no round trip to verify");
        return turn1.stop_reason == "refusal"; // a refusal is a valid observation, not a spike failure
    }
    let zoom = |r: [i32; 4]| {
        let r = r.map(|n| n.max(0) as u32);
        screens::b64(&s.after_img.crop(r).png())
    };
    let check = |a: &Action| validate(a, screens::WIDTH as i32, screens::HEIGHT as i32);
    let results = claude::results_for(&turn1, &s.after, &zoom, &check);
    let req2 = claude::second_request(&req1, &resp1, results);
    let Some(resp2) = post(&rec, "turn2", request(), &req2) else { return false };
    report_claude("turn2", &resp2).is_some()
}

fn report_claude(name: &str, v: &Value) -> Option<claude::Turn> {
    match claude::parse_response(v) {
        Ok(t) => {
            println!(
                "  {name}: model={} stop_reason={} member_calls={} fallback_blocks={} fallback_served={}",
                t.model, t.stop_reason, t.calls.len(), t.fallback_blocks, t.fallback_served
            );
            if let Some(d) = &t.stop_details {
                println!("  {name} stop_details: {d}");
            }
            for p in &t.progress {
                println!("  {name} progress: {p}");
            }
            for c in &t.calls {
                println!("   {} {}", c.id, c.name);
            }
            show_actions(t.calls.iter().map(|c| &c.action));
            if !t.text.is_empty() {
                println!("  {name} text: {}", t.text);
            }
            if !t.other_tool_uses.is_empty() {
                println!("  {name} non-computer tool_use: {:?}", t.other_tool_uses);
            }
            Some(t)
        }
        Err(e) => {
            eprintln!("  {name}: unparseable: {e}");
            None
        }
    }
}
