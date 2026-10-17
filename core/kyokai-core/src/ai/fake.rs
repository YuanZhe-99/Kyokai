//! Test doubles for the AI runtime: a fake desktop showing the synthetic
//! screens, and a local stand-in for a provider. The runtime and session
//! tests drive them; nothing here touches the real desktop or a provider.

use serde_json::{Value, json};
use std::io::{BufRead, BufReader, Read, Write};
use std::net::TcpListener;
use std::path::PathBuf;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use super::desktop::{Desktop, Frame};
use super::{Ai, screens, task::testing};
use crate::input::{Event, LocalInput, PointerAction};
use crate::store::Secrets;

/// What the fake desktop saw and does.
#[derive(Default)]
pub struct Log {
    pub events: Vec<Event>,
    /// Whether the SAVE button was pressed.
    pub pressed: bool,
    /// When the "host user" last pressed a key or button, or moved.
    pub press_at: Option<Instant>,
    pub moved_at: Option<Instant>,
    /// The emergency stop, pressed.
    pub stop: bool,
    /// The size `size()` reports, when not the capture's.
    pub size: Option<(u32, u32)>,
    pub released: u32,
}

/// A desktop that shows the synthetic screens and records what it is sent.
pub struct Fake(pub Arc<Mutex<Log>>);

impl Desktop for Fake {
    fn size(&mut self) -> Result<(u32, u32), String> {
        Ok(self
            .0
            .lock()
            .unwrap()
            .size
            .unwrap_or((screens::WIDTH, screens::HEIGHT)))
    }

    fn capture(&mut self) -> Result<Frame, String> {
        Ok(screens::screen(self.0.lock().unwrap().pressed).frame())
    }

    fn inject(&mut self, e: &Event) -> bool {
        let mut log = self.0.lock().unwrap();
        if let Event::Pointer {
            action: PointerAction::Down,
            button: 0,
            x,
            y,
        } = e
        {
            let [x0, y0, x1, y1] = screens::BUTTON;
            let (px, py) = (x * screens::WIDTH as f32, y * screens::HEIGHT as f32);
            if (x0 as f32..x1 as f32).contains(&px) && (y0 as f32..y1 as f32).contains(&py) {
                log.pressed = true;
            }
        }
        log.events.push(e.clone());
        true
    }

    fn release(&mut self) {
        self.0.lock().unwrap().released += 1;
    }

    fn cursor(&mut self) -> Option<(f32, f32)> {
        Some((0.5, 0.5))
    }

    fn local_input(&mut self) -> LocalInput {
        let log = self.0.lock().unwrap();
        LocalInput {
            press: log.press_at,
            moved: log.moved_at,
        }
    }

    fn stop_requested(&mut self) -> bool {
        std::mem::take(&mut self.0.lock().unwrap().stop)
    }
}

/// What the stand-in received: each request's header lines and body.
pub type Requests = Arc<Mutex<Vec<(Vec<String>, Value)>>>;

/// A local stand-in for a provider: answers each request with the next
/// response, after its delay.
pub struct Server {
    pub url: String,
    pub requests: Requests,
}

/// Starts a stand-in that answers with `responses` (delay in ms, body).
pub fn server(responses: Vec<(u64, Value)>) -> Server {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let url = format!("http://{}/v1/test", listener.local_addr().unwrap());
    let requests = Arc::new(Mutex::new(Vec::new()));
    let log = requests.clone();
    std::thread::spawn(move || {
        for (delay, response) in responses {
            let Ok((stream, _)) = listener.accept() else {
                return;
            };
            let mut reader = BufReader::new(stream.try_clone().unwrap());
            let mut headers = Vec::new();
            let mut len = 0;
            loop {
                let mut line = String::new();
                reader.read_line(&mut line).unwrap();
                let line = line.trim_end().to_string();
                if line.is_empty() {
                    break;
                }
                if let Some(v) = line.to_ascii_lowercase().strip_prefix("content-length:") {
                    len = v.trim().parse().unwrap();
                }
                headers.push(line);
            }
            let mut body = vec![0; len];
            reader.read_exact(&mut body).unwrap();
            log.lock()
                .unwrap()
                .push((headers, serde_json::from_slice(&body).unwrap()));
            std::thread::sleep(Duration::from_millis(delay));
            let text = response.to_string();
            let mut stream = stream;
            let _ = write!(
                stream,
                "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{text}",
                text.len()
            );
        }
    });
    Server { url, requests }
}

impl Server {
    /// Waits until the stand-in has received `n` requests.
    pub fn wait_for(&self, n: usize) {
        let t0 = Instant::now();
        while self.requests.lock().unwrap().len() < n {
            assert!(
                t0.elapsed() < Duration::from_secs(20),
                "waiting for request {n}"
            );
            std::thread::sleep(Duration::from_millis(5));
        }
    }
}

/// An AI runtime on the fake desktop with a stored key, in its own state
/// directory (removed on drop).
pub struct Rig {
    pub ai: Arc<Ai>,
    pub log: Arc<Mutex<Log>>,
    pub dir: PathBuf,
}

impl Drop for Rig {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.dir);
    }
}

/// The key the rigs store; never a real one.
pub const TEST_KEY: &str = "sk-test-not-a-real-key";

/// Builds a rig whose tasks use `provider_kind` and send to `url`.
pub fn rig(name: &str, provider_kind: &str, url: &str) -> Rig {
    let dir = std::env::temp_dir().join(format!("kyokai-ai-{name}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).unwrap();
    let log = Arc::new(Mutex::new(Log::default()));
    let shared = log.clone();
    let ai = testing::ai(
        &dir,
        Arc::new(Secrets::memory()),
        Arc::new(move || Ok(Box::new(Fake(shared.clone())) as Box<dyn Desktop>)),
        url.into(),
    );
    // `claude` and `openai` are the default sources; `chat` adds an
    // OpenAI-compatible one.
    let mut sources = ai.settings()["sources"].clone();
    if provider_kind == "chat" {
        sources.as_array_mut().unwrap().push(json!({"id": "chat", "name": "Chat", "template": "custom",
            "protocol": "openai-chat", "url": "https://chat.example", "models": [{"id": "vision-model"}]}));
    }
    let model = sources
        .as_array()
        .unwrap()
        .iter()
        .find(|s| s["id"] == provider_kind)
        .unwrap()["models"][0]["id"]
        .clone();
    ai.set_settings(&json!({"sources": sources, "model": {"source": provider_kind, "id": model}}))
        .unwrap();
    ai.set_key(provider_kind, Some(TEST_KEY)).unwrap();
    Rig {
        ai: Arc::new(ai),
        log,
        dir,
    }
}

impl Rig {
    pub fn task(&self, id: u64) -> Value {
        self.ai
            .tasks()
            .as_array()
            .unwrap()
            .iter()
            .find(|t| t["id"] == id)
            .unwrap()
            .clone()
    }

    /// Waits until the task satisfies `f`.
    pub fn until(&self, id: u64, what: &str, f: impl Fn(&Value) -> bool) -> Value {
        let t0 = Instant::now();
        loop {
            let t = self.task(id);
            if f(&t) {
                return t;
            }
            assert!(
                t0.elapsed() < Duration::from_secs(20),
                "waiting for {what}: {t:#}"
            );
            std::thread::sleep(Duration::from_millis(10));
        }
    }

    pub fn state(&self, id: u64, state: &str) -> Value {
        self.until(id, state, |t| t["state"] == state)
    }

    /// Button presses the fake desktop received.
    pub fn clicks(&self) -> usize {
        let log = self.log.lock().unwrap();
        log.events
            .iter()
            .filter(|e| {
                matches!(
                    e,
                    Event::Pointer {
                        action: PointerAction::Down,
                        ..
                    }
                )
            })
            .count()
    }
}

/// A Claude turn with these content blocks.
pub fn claude_turn(id: &str, blocks: Value) -> Value {
    json!({"id": id, "model": "claude-opus-5-5", "stop_reason": "tool_use", "content": blocks})
}

/// A Claude turn that ends the task with `text`.
pub fn claude_end(text: &str) -> Value {
    json!({"id": "msg_end", "model": "claude-opus-5-5", "stop_reason": "end_turn",
        "content": [{"type": "text", "text": text}]})
}

/// Claude's turn that clicks SAVE and takes a screenshot.
pub fn click_save() -> Value {
    json!([
        {"type": "thinking", "thinking": "Pressing Save.", "signature": "sig"},
        {"type": "tool_use", "id": "t1", "name": "left_click", "toolset_name": "computer", "input": {"coordinate": [640, 425]}},
        {"type": "tool_use", "id": "t2", "name": "screenshot", "toolset_name": "computer", "input": {}}
    ])
}
