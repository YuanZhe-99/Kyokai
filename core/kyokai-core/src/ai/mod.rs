//! The host's AI runtime (doc/en-us/computer-use.md, "AI tasks"): a task
//! runs on this computer, with this computer's provider key; the model's
//! proposals are checked and executed here, one at a time.

pub mod action;
mod chat;
mod claude;
pub mod desktop;
#[cfg(test)]
pub(crate) mod fake;
mod openai;
#[cfg(test)]
mod screens;
mod task;
#[cfg(test)]
mod tests;

pub use task::{Ai, Pause, open_screen};

use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::time::Duration;

use action::{Action, ParseError};

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

impl Protocol {
    /// The request's path below a source's base URL.
    fn path(self) -> &'static str {
        match self {
            Protocol::Anthropic => "/messages",
            Protocol::OpenaiResponses => "/responses",
            Protocol::OpenaiChat => "/chat/completions",
        }
    }
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
}

/// A model of a source.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Model {
    /// The id the provider expects (`claude-opus-5-5`, `openai/gpt-5`).
    pub id: String,
    /// The name the provider lists, when it lists one.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub name: String,
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
            }],
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
            }],
        },
    ]
}

/// A screenshot as the model sees it: a base64 PNG and its size.
#[derive(Debug, Clone)]
pub struct Shot {
    pub b64: String,
    pub width: u32,
    pub height: u32,
}

/// How a model turn ended.
#[derive(Debug, Clone, PartialEq)]
pub enum End {
    /// Actions to run; the model waits for their results.
    Actions,
    /// The model is done (its text is the answer).
    Finished,
    /// The model declined the task.
    Refused(String),
    /// The response was cut off (length or context).
    Incomplete(String),
}

/// One model turn, provider-neutral.
#[derive(Debug, Clone)]
pub struct Turn {
    /// In order; a parse failure stays in place so later actions are not
    /// run either.
    pub actions: Vec<Result<Action, ParseError>>,
    /// Safety checks the provider raised for this turn.
    pub flagged: Vec<String>,
    pub text: String,
    /// Progress notes between actions.
    pub progress: Vec<String>,
    pub end: End,
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

/// A task's conversation with its provider.
enum Conversation {
    Openai(openai::Conversation),
    Claude(claude::Conversation),
    Chat(chat::Conversation),
}

impl Conversation {
    fn new(protocol: Protocol, model: &str, system: &str, task: &str, shot: &Shot) -> Self {
        match protocol {
            Protocol::OpenaiResponses => {
                Conversation::Openai(openai::Conversation::new(model, system, task, shot))
            }
            Protocol::Anthropic => {
                Conversation::Claude(claude::Conversation::new(model, system, task, shot))
            }
            Protocol::OpenaiChat => {
                Conversation::Chat(chat::Conversation::new(model, system, task, shot))
            }
        }
    }

    fn body(&self) -> Value {
        match self {
            Conversation::Openai(c) => c.body(),
            Conversation::Claude(c) => c.body().clone(),
            Conversation::Chat(c) => c.body(),
        }
    }

    fn accept(&mut self, response: &Value) -> Result<Turn, ParseError> {
        match self {
            Conversation::Openai(c) => c.accept(response),
            Conversation::Claude(c) => c.accept(response),
            Conversation::Chat(c) => c.accept(response),
        }
    }

    /// Whether answering needs the screen after the batch (OpenAI and chat
    /// answer every batch with one; Claude asks for screenshots itself).
    fn wants_final_shot(&self) -> bool {
        matches!(self, Conversation::Openai(_) | Conversation::Chat(_))
    }

    fn answer(&mut self, outcomes: &[Outcome], shot: Option<&Shot>, acknowledged: bool) {
        match self {
            Conversation::Openai(c) => c.answer(
                outcomes,
                shot.expect("OpenAI answers with a screenshot"),
                acknowledged,
            ),
            Conversation::Claude(c) => c.answer(outcomes),
            Conversation::Chat(c) => {
                c.answer(outcomes, shot.expect("chat answers with a screenshot"))
            }
        }
    }

    fn follow_up(&mut self, text: &str, shot: &Shot) {
        match self {
            Conversation::Openai(c) => c.follow_up(text, shot),
            Conversation::Claude(c) => c.follow_up(text, shot),
            Conversation::Chat(c) => c.follow_up(text, shot),
        }
    }
}

/// Where a source's requests go: its API, or a local test server.
#[derive(Debug, Clone)]
struct Endpoint {
    protocol: Protocol,
    url: String,
}

/// How long one model request may take, thinking included.
const REQUEST_TIMEOUT: Duration = Duration::from_secs(300);
/// Retries of a request the provider answered as overloaded or failed.
const RETRIES: u32 = 3;

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

    /// Sends one request, retrying overload and server errors.
    /// Returns: the response document, or an error that is safe to show
    /// (never the key).
    async fn send(&self, to: &Endpoint, key: &str, body: &Value) -> Result<Value, String> {
        let mut attempt = 0;
        loop {
            let req = self.http.post(&to.url).json(body);
            let req = match to.protocol {
                Protocol::OpenaiResponses => req.bearer_auth(key),
                // A local server (Ollama, LM Studio) needs no key.
                Protocol::OpenaiChat if key.is_empty() => req,
                Protocol::OpenaiChat => req.bearer_auth(key),
                Protocol::Anthropic => req
                    .header("x-api-key", key)
                    .header("anthropic-version", claude::VERSION)
                    .header("anthropic-beta", claude::BETAS),
            };
            let resp = match req.send().await {
                Ok(r) => r,
                Err(e) if e.is_timeout() => {
                    return Err("the provider did not answer in time".into());
                }
                Err(e) if attempt < RETRIES && e.is_connect() => {
                    attempt += 1;
                    tokio::time::sleep(backoff(attempt, None)).await;
                    continue;
                }
                Err(_) => return Err("the provider could not be reached".into()),
            };
            let status = resp.status().as_u16();
            let retry_after = resp
                .headers()
                .get("retry-after")
                .and_then(|v| v.to_str().ok()?.parse::<u64>().ok());
            let doc: Value = resp.json().await.unwrap_or(Value::Null);
            match status {
                200..=299 => return Ok(doc),
                408 | 429 | 500 | 502 | 503 | 504 | 529 if attempt < RETRIES => {
                    attempt += 1;
                    tokio::time::sleep(backoff(attempt, retry_after)).await;
                }
                401 | 403 => return Err("the provider did not accept the API key".into()),
                _ => {
                    let message = doc["error"]["message"].as_str().unwrap_or("");
                    return Err(format!("the provider answered {status}: {message}"));
                }
            }
        }
    }
}

impl Client {
    /// Sends one GET (a model list) with the protocol's credentials.
    /// Returns: the response document, or an error that is safe to show.
    async fn get(&self, url: &str, protocol: Protocol, key: &str) -> Result<Value, String> {
        let req = self.http.get(url);
        let req = match protocol {
            Protocol::Anthropic => req
                .header("x-api-key", key)
                .header("anthropic-version", claude::VERSION),
            _ if key.is_empty() => req,
            _ => req.bearer_auth(key),
        };
        let resp = req
            .send()
            .await
            .map_err(|_| "the provider could not be reached".to_string())?;
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
            })
        })
        .collect();
    out.sort_by(|a, b| a.id.cmp(&b.id));
    out.dedup_by(|a, b| a.id == b.id);
    out
}

/// The wait before retry `attempt`: the provider's `retry-after` (at most
/// 30 s) or 2, 4, 8 s.
fn backoff(attempt: u32, retry_after: Option<u64>) -> Duration {
    match retry_after {
        Some(s) => Duration::from_secs(s.min(30)),
        None => Duration::from_secs(1 << attempt),
    }
}
