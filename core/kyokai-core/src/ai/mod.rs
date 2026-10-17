//! The host's AI runtime (doc/en-us/computer-use.md, "AI tasks"): a task
//! runs on this computer, with this computer's provider key; the model's
//! proposals are checked and executed here, one at a time.

pub mod action;
mod claude;
mod desktop;
#[cfg(test)]
pub(crate) mod fake;
mod openai;
#[cfg(test)]
mod screens;
mod task;
#[cfg(test)]
mod tests;

pub use task::{Ai, Pause};

use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::time::Duration;

use action::{Action, ParseError};

/// A model provider.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum Provider {
    Openai,
    Claude,
}

impl Provider {
    /// The secrets-store entry holding this provider's API key.
    fn key_name(self) -> &'static str {
        match self {
            Provider::Openai => "ai.openai.key",
            Provider::Claude => "ai.claude.key",
        }
    }

    fn url(self) -> &'static str {
        match self {
            Provider::Openai => openai::URL,
            Provider::Claude => claude::URL,
        }
    }

    fn default_model(self) -> &'static str {
        match self {
            Provider::Openai => openai::DEFAULT_MODEL,
            Provider::Claude => claude::DEFAULT_MODEL,
        }
    }
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
}

impl Conversation {
    fn new(provider: Provider, model: &str, system: &str, task: &str, shot: &Shot) -> Self {
        match provider {
            Provider::Openai => {
                Conversation::Openai(openai::Conversation::new(model, system, task, shot))
            }
            Provider::Claude => {
                Conversation::Claude(claude::Conversation::new(model, system, task, shot))
            }
        }
    }

    fn body(&self) -> Value {
        match self {
            Conversation::Openai(c) => c.body(),
            Conversation::Claude(c) => c.body().clone(),
        }
    }

    fn accept(&mut self, response: &Value) -> Result<Turn, ParseError> {
        match self {
            Conversation::Openai(c) => c.accept(response),
            Conversation::Claude(c) => c.accept(response),
        }
    }

    /// Whether answering needs the screen after the batch (OpenAI answers
    /// every call with one; Claude asks for screenshots itself).
    fn wants_final_shot(&self) -> bool {
        matches!(self, Conversation::Openai(_))
    }

    fn answer(&mut self, outcomes: &[Outcome], shot: Option<&Shot>, acknowledged: bool) {
        match self {
            Conversation::Openai(c) => c.answer(
                outcomes,
                shot.expect("OpenAI answers with a screenshot"),
                acknowledged,
            ),
            Conversation::Claude(c) => c.answer(outcomes),
        }
    }

    fn follow_up(&mut self, text: &str, shot: &Shot) {
        match self {
            Conversation::Openai(c) => c.follow_up(text, shot),
            Conversation::Claude(c) => c.follow_up(text, shot),
        }
    }
}

/// Where a provider's requests go: its API, or a local test server.
#[derive(Debug, Clone)]
struct Endpoint {
    provider: Provider,
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
            let req = match to.provider {
                Provider::Openai => req.bearer_auth(key),
                Provider::Claude => req
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

/// The wait before retry `attempt`: the provider's `retry-after` (at most
/// 30 s) or 2, 4, 8 s.
fn backoff(attempt: u32, retry_after: Option<u64>) -> Duration {
    match retry_after {
        Some(s) => Duration::from_secs(s.min(30)),
        None => Duration::from_secs(1 << attempt),
    }
}
