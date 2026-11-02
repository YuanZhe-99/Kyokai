//! The host's AI runtime (doc/en-us/computer-use.md, "AI tasks"): a task
//! runs on this computer, with this computer's provider key; the model's
//! proposals are checked and executed here, one at a time.

pub mod action;
mod chat;
mod claude;
pub mod desktop;
pub mod elements;
#[cfg(test)]
pub(crate) mod fake;
mod openai;
pub mod policy;
#[cfg(test)]
mod screens;
pub mod sidecar;
mod task;
#[cfg(test)]
mod tests;

pub use task::{Ai, Choice, Pause, Thinking, open_screen};

use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::time::Duration;

/// How a source is spoken to (doc/en-us/computer-use.md, "Providers").
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum Protocol {
    /// Anthropic's Messages API with the computer toolset (Claude).
    Anthropic,
    /// OpenAI's Responses API with the computer tool.
    OpenaiResponses,
    /// OpenAI-compatible chat completions with Kyokai's actions as function
    /// tools: any model that accepts images and calls tools.
    OpenaiChat,
}

/// A model provider the host's user set up: a template's endpoint or their
/// own, and the models they use from it.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Source {
    /// Stable id, also the name of its key's entry (`ai.<id>.key`).
    pub id: String,
    /// The name people see ("Claude", "OpenRouter").
    pub name: String,
    /// The template it came from ("anthropic", "openrouter", "custom").
    #[serde(default)]
    pub template: String,
    pub protocol: Protocol,
    /// The base URL, such as `https://api.anthropic.com/v1`.
    pub url: String,
    pub models: Vec<Model>,
    /// Whether its tasks see the element list beside each screenshot
    /// (`elements`); None: on for `openai-chat` sources only.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub elements: Option<bool>,
}

impl Source {
    /// Whether its tasks see the element list (`Source::elements`).
    pub fn lists_elements(&self) -> bool {
        self.elements
            .unwrap_or(self.protocol == Protocol::OpenaiChat)
    }
}

/// A model of a source.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Model {
    /// The id the provider expects (`claude-opus-5-5`, `openai/gpt-5`).
    pub id: String,
    /// The name the provider lists, when it lists one.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub name: String,
    /// How it gives positions (`openai-chat` sources only).
    #[serde(default, skip_serializing_if = "is_auto")]
    pub coordinates: action::Coordinates,
}

fn is_auto(c: &action::Coordinates) -> bool {
    *c == action::Coordinates::Auto
}

/// The secrets-store entry holding a source's API key.
pub fn key_name(source: &str) -> String {
    format!("ai.{source}.key")
}

/// The two sources every host starts with.
fn default_sources() -> Vec<Source> {
    vec![
        Source {
            id: "claude".into(),
            name: "Claude".into(),
            template: "anthropic".into(),
            protocol: Protocol::Anthropic,
            url: claude::BASE_URL.into(),
            models: vec![Model {
                id: claude::DEFAULT_MODEL.into(),
                name: String::new(),
                coordinates: Default::default(),
            }],
            elements: None,
        },
        Source {
            id: "openai".into(),
            name: "OpenAI".into(),
            template: "openai".into(),
            protocol: Protocol::OpenaiResponses,
            url: openai::BASE_URL.into(),
            models: vec![Model {
                id: openai::DEFAULT_MODEL.into(),
                name: String::new(),
                coordinates: Default::default(),
            }],
            elements: None,
        },
    ]
}

/// A screenshot as the model sees it: a base64 PNG and its size.
#[derive(Debug, Clone)]
pub struct Shot {
    pub b64: String,
    pub width: u32,
    pub height: u32,
    /// The element list beside it (`elements::table`), when it has one.
    pub elements: Option<String>,
}

/// Tokens a model turn took, as its provider reports them: all the input
/// (cached or not), the part read from the provider's prompt cache, and the
/// output (thinking included).
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Usage {
    pub input: u64,
    pub cached: u64,
    pub output: u64,
}

impl Usage {
    /// Adds another turn's.
    pub fn add(&mut self, other: Usage) {
        self.input += other.input;
        self.cached += other.cached;
        self.output += other.output;
    }
}

/// What became of one action.
#[derive(Debug, Clone)]
pub enum Outcome {
    Ok,
    /// A screenshot or zoom.
    Image(Shot),
    /// The cursor position.
    Text(String),
    /// Rejected or failed; the text goes to the model.
    Failed(String),
    /// Skipped after an earlier failure.
    NotExecuted,
    /// Skipped because the host user paused the task.
    Paused,
}

/// How long one model request may take, thinking included.
const REQUEST_TIMEOUT: Duration = Duration::from_secs(300);
/// How long listing a source's models may take.
const LIST_TIMEOUT: Duration = Duration::from_secs(15);
/// The HTTPS client for both providers.
struct Client {
    http: reqwest::Client,
}

impl Client {
    /// Builds the client: rustls with the ring provider, the system's roots.
    fn new() -> Result<Self, String> {
        // Several parts of the process may install it; the first one wins.
        let _ = rustls::crypto::ring::default_provider().install_default();
        let http = reqwest::Client::builder()
            .connect_timeout(Duration::from_secs(20))
            .timeout(REQUEST_TIMEOUT)
            .build()
            .map_err(|e| format!("HTTPS client: {e}"))?;
        Ok(Client { http })
    }
}

impl Client {
    /// Sends one GET (a model list) with the protocol's credentials.
    /// Returns: the response document, or an error that is safe to show.
    /// Notes: it may take LIST_TIMEOUT, far less than a model's answer.
    async fn get(&self, url: &str, protocol: Protocol, key: &str) -> Result<Value, String> {
        let req = self.http.get(url).timeout(LIST_TIMEOUT);
        let req = match protocol {
            Protocol::Anthropic => req.header("anthropic-version", claude::VERSION),
            Protocol::OpenaiResponses | Protocol::OpenaiChat => req,
        };
        let resp = match authorized(req, protocol, key).send().await {
            Ok(r) => r,
            Err(e) if e.is_timeout() => return Err("the provider did not answer in time".into()),
            Err(_) => return Err("the provider could not be reached".into()),
        };
        let status = resp.status().as_u16();
        let doc: Value = resp.json().await.unwrap_or(Value::Null);
        match status {
            200..=299 => Ok(doc),
            401 | 403 => Err("the provider did not accept the API key".into()),
            _ => Err(format!(
                "the provider answered {status}: {}",
                doc["error"]["message"].as_str().unwrap_or("")
            )),
        }
    }
}

/// Adds a request's key as its protocol carries it; an empty key (a server
/// on this computer) adds nothing.
fn authorized(
    req: reqwest::RequestBuilder,
    protocol: Protocol,
    key: &str,
) -> reqwest::RequestBuilder {
    match protocol {
        _ if key.is_empty() => req,
        Protocol::Anthropic => req.header("x-api-key", key),
        Protocol::OpenaiResponses | Protocol::OpenaiChat => req.bearer_auth(key),
    }
}

/// Reads a model list: OpenAI-style `{data: [{id, name?}]}` (OpenRouter
/// names its models, Anthropic has `display_name`), Ollama's `{models:
/// [{name}]}` or a bare list; embedding, speech, image and moderation models
/// are left out (they cannot operate a desktop).
fn parse_models(doc: &Value) -> Vec<Model> {
    let list = doc["data"]
        .as_array()
        .or(doc["models"].as_array())
        .or(doc.as_array())
        .cloned()
        .unwrap_or_default();
    let not_chat = [
        "embed",
        "whisper",
        "tts",
        "dall-e",
        "moderation",
        "rerank",
        "image",
    ];
    let mut out: Vec<Model> = list
        .iter()
        .filter_map(|m| {
            let id = m["id"]
                .as_str()
                .or(m["model"].as_str())
                .or(m["name"].as_str())?;
            let lower = id.to_ascii_lowercase();
            if not_chat.iter().any(|w| lower.contains(w)) || id.len() > 128 {
                return None;
            }
            let name = m["display_name"]
                .as_str()
                .or(m["name"].as_str().filter(|n| *n != id))
                .unwrap_or("");
            Some(Model {
                id: id.to_string(),
                name: name.chars().take(128).collect(),
                coordinates: Default::default(),
            })
        })
        .collect();
    out.sort_by(|a, b| a.id.cmp(&b.id));
    out.dedup_by(|a, b| a.id == b.id);
    out
}
