//! The agent sidecar's protocol (doc/en-us/architecture.md, "The agent
//! sidecar"): the engine and `kyokai-agent` (Pi's loop, PLAN.md) speak JSON
//! lines over the sidecar's standard input and output. The sidecar owns the
//! loop and the provider requests; the engine owns everything that acts —
//! it parses each proposed action with its own parser, checks, approves and
//! runs it — and hands the provider key out per request, never to keep.
//!
//! Versioned: the sidecar says `hello {v}` first, and the engine refuses a
//! version it does not speak.
//!
//! The engine drives tasks through it from step 4 of "Pi in the product"
//! (PLAN.md); until then only its tests use it.
#![allow(dead_code)]

use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::io::{BufRead, Read, Write};

use super::action::Coordinates;
use super::{Protocol, Shot, Usage};

/// The protocol version this engine speaks.
pub const VERSION: u32 = 1;

/// The longest line the engine reads from the sidecar: its messages are
/// small (actions, text, usage); screenshots only go the other way.
pub const MAX_LINE: usize = 1 << 20;

/// A screenshot on the wire: a base64 PNG and its size.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct WireShot {
    pub b64: String,
    pub width: u32,
    pub height: u32,
}

impl From<&Shot> for WireShot {
    fn from(s: &Shot) -> Self {
        WireShot {
            b64: s.b64.clone(),
            width: s.width,
            height: s.height,
        }
    }
}

/// What became of one action, as the engine answers it.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "kebab-case")]
pub enum WireOutcome {
    Ok,
    Image { shot: WireShot },
    Text { text: String },
    Failed { error: String },
    NotExecuted,
    Paused,
}

/// Engine → sidecar.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(tag = "t", rename_all = "kebab-case")]
pub enum ToSidecar {
    /// Begins a task: the model and how to reach it (never the key), the
    /// instructions, the task, and the first screenshot.
    Start {
        task: u64,
        protocol: Protocol,
        url: String,
        model: String,
        coordinates: Coordinates,
        system: String,
        instruction: String,
        shot: WireShot,
        /// The function tools an `openai-chat` source is offered (Kyokai's
        /// actions in the computer toolset's shape); null for the others,
        /// whose providers have their own computer tools.
        #[serde(default)]
        tools: Value,
    },
    /// A message from the person for the running turn.
    Steer { text: String },
    /// Continues a finished task with the person's reply.
    FollowUp { text: String, shot: WireShot },
    /// Stops the task: the request in flight is dropped, no action follows.
    Abort,
    /// The provider key for one request (`key?`); None when there is none
    /// (a local server).
    Key { key: Option<String> },
    /// The outcome of an action (`act`), with the screen after the batch
    /// when the provider answers with one.
    Result {
        id: String,
        outcome: WireOutcome,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        shot: Option<WireShot>,
        /// The host user approved the turn's safety checks (`turn.flagged`).
        #[serde(default, skip_serializing_if = "std::ops::Not::not")]
        acknowledged: bool,
    },
}

/// Sidecar → engine.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(tag = "t", rename_all = "kebab-case")]
pub enum FromSidecar {
    Hello {
        v: u32,
    },
    /// Asks for the provider key for the next request.
    #[serde(rename = "key?")]
    KeyRequest,
    /// One proposed action, which the engine parses itself: a computer
    /// toolset member (`left_click` with `coordinate`, …), or OpenAI's
    /// action object under `name: "openai"`. `last` marks the turn's last
    /// action: its result carries the screen after the batch when the
    /// provider answers with one.
    Act {
        id: String,
        name: String,
        input: Value,
        #[serde(default)]
        last: bool,
    },
    /// A model turn: its text, progress notes, the provider's safety notes
    /// and the tokens it took.
    Turn {
        #[serde(default)]
        text: String,
        #[serde(default)]
        progress: Vec<String>,
        #[serde(default)]
        flagged: Vec<String>,
        #[serde(default)]
        usage: Usage,
    },
    Done {
        #[serde(default)]
        text: String,
    },
    Failed {
        error: String,
    },
    Aborted,
}

/// One end of the line link (generic so the tests run it over memory).
pub struct Link<R, W> {
    read: R,
    write: W,
    line: String,
}

impl<R: BufRead, W: Write> Link<R, W> {
    pub fn new(read: R, write: W) -> Self {
        Link {
            read,
            write,
            line: String::new(),
        }
    }

    /// Writes one message as a line.
    pub fn send(&mut self, m: &ToSidecar) -> Result<(), String> {
        let mut bytes = serde_json::to_vec(m).map_err(|e| e.to_string())?;
        bytes.push(b'\n');
        self.write
            .write_all(&bytes)
            .and_then(|()| self.write.flush())
            .map_err(|e| format!("the agent is gone: {e}"))
    }

    /// Reads the next message.
    /// Returns: an error for a closed pipe, a line past `MAX_LINE` or one
    /// that does not parse (the engine then ends the task loudly).
    pub fn recv(&mut self) -> Result<FromSidecar, String> {
        loop {
            self.line.clear();
            let n = self
                .read
                .by_ref()
                .take(MAX_LINE as u64 + 1)
                .read_line(&mut self.line)
                .map_err(|e| format!("the agent's output: {e}"))?;
            if n == 0 {
                return Err("the agent ended".into());
            }
            if n > MAX_LINE {
                return Err("the agent sent an overlong line".into());
            }
            if self.line.trim().is_empty() {
                continue;
            }
            return serde_json::from_str(&self.line)
                .map_err(|e| format!("the agent sent something unreadable: {e}"));
        }
    }

    /// Waits for the sidecar's `hello` and checks its version.
    pub fn handshake(&mut self) -> Result<(), String> {
        match self.recv()? {
            FromSidecar::Hello { v } if v == VERSION => Ok(()),
            FromSidecar::Hello { v } => Err(format!(
                "the agent speaks protocol {v}, this engine {VERSION}"
            )),
            other => Err(format!("the agent began with {other:?}, not hello")),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    use std::io::Cursor;

    fn link(input: &str) -> Link<Cursor<Vec<u8>>, Vec<u8>> {
        Link::new(Cursor::new(input.as_bytes().to_vec()), Vec::new())
    }

    fn shot() -> WireShot {
        WireShot {
            b64: "AAAA".into(),
            width: 1280,
            height: 720,
        }
    }

    /// Every message has its documented wire form, both ways.
    #[test]
    fn messages_have_their_wire_form() {
        let to = [
            (
                ToSidecar::Start {
                    task: 7,
                    protocol: Protocol::OpenaiChat,
                    url: "https://openrouter.ai/api/v1".into(),
                    model: "qwen/qwen3.8-27b".into(),
                    coordinates: Coordinates::Grid1000,
                    system: "sys".into(),
                    instruction: "Save".into(),
                    shot: shot(),
                    tools: Value::Null,
                },
                json!({"t": "start", "task": 7, "protocol": "openai-chat", "url": "https://openrouter.ai/api/v1",
                    "model": "qwen/qwen3.8-27b", "coordinates": "0-1000", "system": "sys", "instruction": "Save",
                    "shot": {"b64": "AAAA", "width": 1280, "height": 720}, "tools": null}),
            ),
            (ToSidecar::Abort, json!({"t": "abort"})),
            (
                ToSidecar::Key { key: None },
                json!({"t": "key", "key": null}),
            ),
            (
                ToSidecar::Result {
                    id: "c1".into(),
                    outcome: WireOutcome::Failed { error: "no".into() },
                    shot: None,
                    acknowledged: false,
                },
                json!({"t": "result", "id": "c1", "outcome": {"kind": "failed", "error": "no"}}),
            ),
        ];
        for (m, wire) in to {
            assert_eq!(serde_json::to_value(&m).unwrap(), wire);
            assert_eq!(serde_json::from_value::<ToSidecar>(wire).unwrap(), m);
        }
        let from = [
            (FromSidecar::Hello { v: 1 }, json!({"t": "hello", "v": 1})),
            (FromSidecar::KeyRequest, json!({"t": "key?"})),
            (
                FromSidecar::Act {
                    id: "c1".into(),
                    name: "left_click".into(),
                    input: json!({"coordinate": [1, 2]}),
                    last: true,
                },
                json!({"t": "act", "id": "c1", "name": "left_click", "input": {"coordinate": [1, 2]}, "last": true}),
            ),
            (FromSidecar::Aborted, json!({"t": "aborted"})),
        ];
        for (m, wire) in from {
            assert_eq!(serde_json::to_value(&m).unwrap(), wire);
            assert_eq!(serde_json::from_value::<FromSidecar>(wire).unwrap(), m);
        }
    }

    /// The handshake takes this version only; anything else ends the task
    /// with a reason, never a panic.
    #[test]
    fn the_handshake_checks_the_version() {
        assert!(link("{\"t\":\"hello\",\"v\":1}\n").handshake().is_ok());
        let other = link("{\"t\":\"hello\",\"v\":2}\n").handshake().unwrap_err();
        assert!(other.contains("protocol 2"), "{other}");
        assert!(link("{\"t\":\"done\"}\n").handshake().is_err());
        assert!(link("").handshake().unwrap_err().contains("ended"));
    }

    /// Unreadable, unknown and overlong lines are errors; blank lines are
    /// skipped; a send writes one line.
    #[test]
    fn bad_lines_are_refused() {
        assert!(link("not json\n").recv().is_err());
        assert!(link("{\"t\":\"launch\"}\n").recv().is_err());
        let long = format!("{{\"t\":\"done\",\"text\":\"{}\"}}\n", "x".repeat(MAX_LINE));
        assert!(link(&long).recv().unwrap_err().contains("overlong"));
        assert_eq!(
            link("\n\n{\"t\":\"aborted\"}\n").recv().unwrap(),
            FromSidecar::Aborted
        );
        let mut l = link("");
        l.send(&ToSidecar::Abort).unwrap();
        assert_eq!(l.write, b"{\"t\":\"abort\"}\n");
    }
}
