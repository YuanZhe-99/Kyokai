//! The AI on a pane (doc/en-us/computer-use.md, "AI on a pane"): a Remote
//! Desktop, VNC or NanoKVM USB pane in this computer's GUI that the host
//! user lets the host's AI operate. The GUI serves the pane over the local
//! API and the engine relays (`ai.pane.*`); a task on it sees that pane's
//! picture, and its input goes to that pane's client or the NanoKVM's
//! keyboard and mouse, never to this computer's desktop.

use std::collections::{HashMap, VecDeque};
use std::sync::{Arc, Condvar, Mutex};
use std::time::{Duration, Instant};

use serde::{Deserialize, Serialize};
use serde_json::{Value, json};

use super::desktop::{Desktop, Frame};
use crate::input::{Event, LocalInput};

/// A pane the GUI serves, as the AI's settings list it.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Served {
    /// The GUI's id for it, unique while it is served.
    pub id: String,
    /// The name people see ("Remote Desktop · NUC").
    pub name: String,
    /// `rdp`, `vnc` or `nanokvm`.
    pub kind: String,
}

/// The program name the policy gives a pane: one of its Remote Desktop and
/// VNC windows (pixels only), so in Auto every acting action asks.
pub const PROGRAM: &str = "kyokai pane";

/// Longest wait for the GUI's answer to a picture.
const CAPTURE_WAIT: Duration = Duration::from_secs(10);
/// Longest wait for the GUI to take input or tell the size.
const INPUT_WAIT: Duration = Duration::from_secs(3);
/// Requests waiting for the GUI at most; past it the GUI is not reading.
const MAX_WAITING: usize = 64;

/// What travels between a task and the GUI serving the pane.
#[derive(Default)]
struct Exchange {
    /// Requests for the GUI, oldest first, by number.
    requests: VecDeque<(u64, Value)>,
    /// The GUI's answers by request number, until taken.
    answers: HashMap<u64, Value>,
    next: u64,
    closed: bool,
    /// When the host user last used the pane: a key or button, a move.
    press: Option<Instant>,
    moved: Option<Instant>,
}

/// One served pane: requests out to the GUI, answers and the host user's
/// input in.
pub struct Pane {
    pub served: Served,
    state: Mutex<Exchange>,
    wake: Condvar,
}

impl Pane {
    /// Asks the GUI and waits for its answer.
    /// Returns: the answer, or why there is none (the pane closed, the GUI
    /// did not answer in time, or answered with an error).
    fn ask(&self, request: Value, wait: Duration) -> Result<Value, String> {
        let until = Instant::now() + wait;
        let mut s = self.state.lock().expect("pane");
        if s.closed {
            return Err("the pane is closed".into());
        }
        if s.requests.len() >= MAX_WAITING {
            return Err("the pane does not answer".into());
        }
        s.next += 1;
        let seq = s.next;
        s.requests.push_back((seq, request));
        self.wake.notify_all();
        loop {
            if let Some(a) = s.answers.remove(&seq) {
                return match a.get("error").and_then(Value::as_str) {
                    Some(e) => Err(e.to_owned()),
                    None => Ok(a),
                };
            }
            if s.closed {
                return Err("the pane is closed".into());
            }
            let left = until.saturating_duration_since(Instant::now());
            if left.is_zero() {
                s.requests.retain(|(n, _)| *n != seq);
                return Err("the pane did not answer in time".into());
            }
            s = self.wake.wait_timeout(s, left).expect("pane").0;
        }
    }

    /// The next request for the GUI (the engine's relay), waiting up to
    /// `wait` for one.
    /// Returns: `{seq, request}`, or None when there is none or the pane
    /// closed.
    pub fn next(&self, wait: Duration) -> Option<Value> {
        let until = Instant::now() + wait;
        let mut s = self.state.lock().expect("pane");
        loop {
            if s.closed {
                return None;
            }
            if let Some((seq, request)) = s.requests.pop_front() {
                return Some(json!({"seq": seq, "request": request}));
            }
            let left = until.saturating_duration_since(Instant::now());
            if left.is_zero() {
                return None;
            }
            s = self.wake.wait_timeout(s, left).expect("pane").0;
        }
    }

    /// The GUI's answer to request `seq`.
    pub fn answer(&self, seq: u64, answer: Value) {
        let mut s = self.state.lock().expect("pane");
        if seq != 0 && seq <= s.next {
            s.answers.insert(seq, answer);
        }
        self.wake.notify_all();
    }

    /// The host user used the pane: a key, button or the wheel, or only a
    /// move (`moved`).
    pub fn local(&self, moved: bool) {
        let mut s = self.state.lock().expect("pane");
        let now = Some(Instant::now());
        if moved {
            s.moved = now;
        } else {
            s.press = now;
        }
    }

    /// Whether it was closed.
    pub fn closed(&self) -> bool {
        self.state.lock().expect("pane").closed
    }

    /// Ends it: every wait returns, every later request fails.
    pub fn close(&self) {
        self.state.lock().expect("pane").closed = true;
        self.wake.notify_all();
    }
}

/// The panes the GUI serves now, by id.
#[derive(Default)]
pub struct Panes(Mutex<HashMap<String, Arc<Pane>>>);

impl Panes {
    /// Adds a pane the GUI serves (replacing one with the same id).
    pub fn open(&self, served: Served) -> Arc<Pane> {
        let pane = Arc::new(Pane {
            served: served.clone(),
            state: Mutex::default(),
            wake: Condvar::new(),
        });
        if let Some(old) = self
            .0
            .lock()
            .expect("panes")
            .insert(served.id, pane.clone())
        {
            old.close();
        }
        pane
    }

    /// A served pane by id.
    pub fn get(&self, id: &str) -> Option<Arc<Pane>> {
        self.0.lock().expect("panes").get(id).cloned()
    }

    /// Ends a pane: its task's next look or action fails.
    pub fn close(&self, id: &str) {
        if let Some(p) = self.0.lock().expect("panes").remove(id) {
            p.close();
        }
    }

    /// The served panes, by name.
    pub fn list(&self) -> Vec<Served> {
        let mut out: Vec<Served> = self
            .0
            .lock()
            .expect("panes")
            .values()
            .map(|p| p.served.clone())
            .collect();
        out.sort_by(|a, b| a.name.cmp(&b.name));
        out
    }
}

/// A task's desktop on a pane: the pane's picture and input, the host
/// user's input on the pane as take-over, and this computer's own watch
/// only for the emergency stop.
pub struct PaneDesk {
    pane: Arc<Pane>,
    /// This computer's desktop, for its emergency stop.
    screen: Box<dyn Desktop>,
    /// The size of the last picture.
    size: Option<(u32, u32)>,
}

impl PaneDesk {
    pub fn new(pane: Arc<Pane>, screen: Box<dyn Desktop>) -> PaneDesk {
        PaneDesk {
            pane,
            screen,
            size: None,
        }
    }
}

impl Desktop for PaneDesk {
    fn size(&mut self) -> Result<(u32, u32), String> {
        let a = self.pane.ask(json!({"op": "size"}), INPUT_WAIT)?;
        let size = (
            a["width"].as_u64().unwrap_or(0) as u32,
            a["height"].as_u64().unwrap_or(0) as u32,
        );
        if size.0 == 0 || size.1 == 0 {
            return Err("the pane shows no picture".into());
        }
        Ok(size)
    }

    fn capture(&mut self) -> Result<Frame, String> {
        let a = self.pane.ask(json!({"op": "capture"}), CAPTURE_WAIT)?;
        let png = a["png"].as_str().ok_or("the pane sent no picture")?;
        let frame = decode_png(png)?;
        self.size = Some((frame.width, frame.height));
        Ok(frame)
    }

    fn inject(&mut self, e: &Event) -> bool {
        self.pane
            .ask(json!({"op": "input", "event": e}), INPUT_WAIT)
            .is_ok()
    }

    fn release(&mut self) {
        let _ = self.pane.ask(json!({"op": "release"}), INPUT_WAIT);
    }

    fn cursor(&mut self) -> Option<(f32, f32)> {
        None
    }

    fn local_input(&mut self) -> LocalInput {
        let s = self.pane.state.lock().expect("pane");
        if s.closed {
            // The pane is gone: as if the host user took over, so a task
            // never acts blind.
            let now = Some(Instant::now());
            return LocalInput {
                press: now,
                moved: now,
            };
        }
        LocalInput {
            press: s.press,
            moved: s.moved,
        }
    }

    fn stop_requested(&mut self) -> bool {
        self.screen.stop_requested()
    }

    fn target(&mut self, _at: Option<(f32, f32)>) -> super::policy::Target {
        super::policy::Target {
            program: Some(PROGRAM.into()),
            ..Default::default()
        }
    }

    fn remote(&self) -> bool {
        true
    }
}

/// A base64 PNG as a BGRA frame.
pub(super) fn decode_png(b64: &str) -> Result<Frame, String> {
    use base64::Engine;
    let bytes = base64::engine::general_purpose::STANDARD
        .decode(b64)
        .map_err(|e| format!("the pane's picture: {e}"))?;
    let mut decoder = png::Decoder::new(std::io::Cursor::new(bytes));
    decoder.set_transformations(png::Transformations::normalize_to_color8());
    let mut reader = decoder
        .read_info()
        .map_err(|e| format!("the pane's picture: {e}"))?;
    let mut buf = vec![
        0;
        reader
            .output_buffer_size()
            .ok_or("the pane's picture: too large")?
    ];
    let info = reader
        .next_frame(&mut buf)
        .map_err(|e| format!("the pane's picture: {e}"))?;
    let px = (info.width * info.height) as usize;
    let mut bgra = Vec::with_capacity(px * 4);
    match info.color_type {
        png::ColorType::Rgba => {
            for p in buf[..px * 4].as_chunks::<4>().0 {
                bgra.extend_from_slice(&[p[2], p[1], p[0], 255]);
            }
        }
        png::ColorType::Rgb => {
            for p in buf[..px * 3].as_chunks::<3>().0 {
                bgra.extend_from_slice(&[p[2], p[1], p[0], 255]);
            }
        }
        other => return Err(format!("the pane's picture: {other:?} is not RGB")),
    }
    Ok(Frame {
        bgra,
        width: info.width,
        height: info.height,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A request reaches the GUI side, its answer the asker; a pane that
    /// closes ends the wait; the host user's input on it is take-over.
    #[test]
    fn requests_and_answers_cross_the_pane() {
        let panes = Panes::default();
        let pane = panes.open(Served {
            id: "p1".into(),
            name: "Remote Desktop · NUC".into(),
            kind: "rdp".into(),
        });
        let gui = pane.clone();
        let t = std::thread::spawn(move || {
            let r = gui.next(Duration::from_secs(5)).unwrap();
            assert_eq!(r["request"]["op"], "size");
            gui.answer(
                r["seq"].as_u64().unwrap(),
                json!({"width": 800, "height": 600}),
            );
            let r = gui.next(Duration::from_secs(5)).unwrap();
            gui.answer(
                r["seq"].as_u64().unwrap(),
                json!({"error": "no picture yet"}),
            );
        });
        assert_eq!(
            pane.ask(json!({"op": "size"}), Duration::from_secs(5))
                .unwrap(),
            json!({"width": 800, "height": 600})
        );
        assert_eq!(
            pane.ask(json!({"op": "capture"}), Duration::from_secs(5)),
            Err("no picture yet".into())
        );
        t.join().unwrap();
        assert!(
            pane.ask(json!({"op": "size"}), Duration::from_millis(50))
                .is_err()
        );
        assert!(panes.list()[0].name.contains("NUC"));
        pane.local(false);
        assert!(pane.state.lock().unwrap().press.is_some());
        panes.close("p1");
        assert!(pane.next(Duration::from_millis(10)).is_none());
        assert_eq!(
            pane.ask(json!({"op": "size"}), Duration::from_secs(1)),
            Err("the pane is closed".into())
        );
    }

    /// The GUI's PNG becomes a BGRA frame.
    #[test]
    fn pictures_decode_to_bgra() {
        let frame = Frame {
            bgra: vec![10, 20, 30, 255, 40, 50, 60, 255],
            width: 2,
            height: 1,
        };
        let shot = super::super::desktop::shot(&frame, [0, 0, 2, 1], (2, 1), 1);
        assert_eq!(decode_png(&shot.b64).unwrap().bgra, frame.bgra);
    }
}
