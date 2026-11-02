//! AI tasks on the host: the state machine, the runner that talks to the
//! provider and drives the desktop, approvals, and the input lease
//! (doc/en-us/computer-use.md, "AI tasks").
//!
//! One task holds the desktop at a time. It holds the input lease only
//! while it runs: local input pauses it at once, and a model response that
//! arrives after a pause or cancel never acts.

use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};
use tokio::sync::Notify;
use zeroize::Zeroizing;

use super::action::{self, Action, Coordinates};
use super::desktop::{self, Desktop, Frame, Step};
use super::policy::{self, Approval, Class, Verdict};
use super::sidecar::{Agent, FromSidecar, ToSidecar, WireOutcome};
use super::{Client, Outcome, Protocol, Shot, Source, default_sources, key_name};
use crate::store::Secrets;

/// The host's AI settings (`ai.json` in the state directory; the keys live
/// in the secrets store).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Settings {
    /// The providers set up here.
    pub sources: Vec<Source>,
    /// The model tasks use.
    pub model: ModelRef,
    pub approval: Approval,
}

/// One model of one source.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct ModelRef {
    pub source: String,
    pub id: String,
}

/// How much the model thinks before it answers: Claude's effort, OpenAI's
/// reasoning effort (`max` sent as `high`); chat sources take none.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Thinking {
    Low,
    Medium,
    High,
    Max,
}

/// A task's model and thinking, chosen when it starts or between runs;
/// None keeps the settings' model (the task's, for a reply) and the usual
/// thinking.
#[derive(Debug, Clone, Default, Deserialize)]
pub struct Choice {
    #[serde(default)]
    pub model: Option<ModelRef>,
    #[serde(default)]
    pub thinking: Option<Thinking>,
}

impl Default for Settings {
    fn default() -> Self {
        Settings {
            sources: default_sources(),
            model: ModelRef {
                source: "claude".into(),
                id: super::claude::DEFAULT_MODEL.into(),
            },
            approval: Approval::Auto,
        }
    }
}

/// Longest source name, model id and URL accepted.
const MAX_NAME: usize = 64;
const MAX_MODEL: usize = 128;
const MAX_URL: usize = 256;

impl Settings {
    /// Reads `ai.json`, also as the first versions wrote it (one provider
    /// and a model for each of Claude and OpenAI).
    fn load(path: &Path) -> Settings {
        let Some(v) = read_json::<Value>(path) else {
            return Settings::default();
        };
        if v.get("sources").is_some() {
            return serde_json::from_value(v).unwrap_or_default();
        }
        let mut s = Settings::default();
        for (source, field) in [("claude", "claude_model"), ("openai", "openai_model")] {
            if let (Some(id), Some(src)) = (
                v[field].as_str().filter(|m| !m.trim().is_empty()),
                s.sources.iter_mut().find(|x| x.id == source),
            ) {
                src.models[0].id = id.to_string();
            }
        }
        if let Some(p) = v["provider"]
            .as_str()
            .filter(|p| *p == "openai" || *p == "claude")
        {
            let src = s
                .sources
                .iter()
                .find(|x| x.id == p)
                .expect("default source");
            s.model = ModelRef {
                source: p.into(),
                id: src.models[0].id.clone(),
            };
        }
        if let Ok(a) = serde_json::from_value(v["approval"].clone()) {
            s.approval = a;
        }
        s
    }

    /// Checks settings at the local API boundary.
    fn validate(&self) -> Result<(), String> {
        let mut ids = std::collections::HashSet::new();
        for s in &self.sources {
            let id_ok = !s.id.is_empty()
                && s.id.len() <= 32
                && s.id
                    .bytes()
                    .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'-');
            if !id_ok || !ids.insert(s.id.as_str()) {
                return Err(format!("bad or repeated source id {:?}", s.id));
            }
            if s.name.trim().is_empty()
                || s.name.chars().count() > MAX_NAME
                || s.name.chars().any(char::is_control)
            {
                return Err(format!("the source {:?} needs a short name", s.id));
            }
            check_url(&s.url)?;
            if s.models.is_empty() {
                return Err(format!("{} has no model", s.name));
            }
            let mut models = std::collections::HashSet::new();
            for m in &s.models {
                let ok = !m.id.is_empty()
                    && m.id.len() <= MAX_MODEL
                    && !m.id.chars().any(|c| c.is_whitespace() || c.is_control())
                    && m.name.chars().count() <= MAX_MODEL
                    && !m.name.chars().any(char::is_control);
                if !ok || !models.insert(m.id.as_str()) {
                    return Err(format!("bad or repeated model {:?} in {}", m.id, s.name));
                }
                if s.protocol != super::Protocol::OpenaiChat
                    && !matches!(m.coordinates, Coordinates::Auto | Coordinates::Pixels)
                {
                    return Err(format!(
                        "{} answers in pixels; its models take no other coordinates",
                        s.name
                    ));
                }
            }
        }
        if self
            .source(&self.model.source)
            .is_none_or(|s| !s.models.iter().any(|m| m.id == self.model.id))
        {
            return Err("choose one of the sources' models".into());
        }
        Ok(())
    }

    fn source(&self, id: &str) -> Option<&Source> {
        self.sources.iter().find(|s| s.id == id)
    }
}

/// Checks a source's base URL: HTTPS, or HTTP only to this computer (a
/// local server such as Ollama or LM Studio), since screenshots go there.
fn check_url(url: &str) -> Result<(), String> {
    let bad = || format!("{url:?} is not a usable address");
    if url.len() > MAX_URL {
        return Err(bad());
    }
    let u = reqwest::Url::parse(url).map_err(|_| bad())?;
    match u.scheme() {
        "https" if u.host().is_some() => Ok(()),
        "http" if is_loopback(&u) => Ok(()),
        "http" => Err("plain HTTP only reaches this computer; use HTTPS".into()),
        _ => Err(bad()),
    }
}

/// Whether a URL names this computer.
fn is_loopback(u: &reqwest::Url) -> bool {
    match u.host_str() {
        Some(h) if h.eq_ignore_ascii_case("localhost") => true,
        Some(h) => h
            .trim_start_matches('[')
            .trim_end_matches(']')
            .parse::<std::net::IpAddr>()
            .is_ok_and(|a| a.is_loopback()),
        None => false,
    }
}

/// Whether a source may go without a key: a chat server on this computer,
/// or any source over plain HTTP (which only reaches this computer and is
/// never sent a key).
fn keyless(s: &Source) -> bool {
    !https(s)
        || (s.protocol == Protocol::OpenaiChat
            && reqwest::Url::parse(&s.url).is_ok_and(|u| is_loopback(&u)))
}

/// Whether a source's address is HTTPS, the only way a key is ever sent.
fn https(s: &Source) -> bool {
    reqwest::Url::parse(&s.url).is_ok_and(|u| u.scheme() == "https")
}

/// A task's state (doc/en-us/computer-use.md has the diagram).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum State {
    Running,
    /// Waiting for the host user to approve an action.
    Approval,
    Paused,
    Done,
    Failed,
    Cancelled,
}

impl State {
    /// Whether the task is over (its runner has stopped).
    fn finished(self) -> bool {
        matches!(self, State::Done | State::Failed | State::Cancelled)
    }
}

/// What moves a task between states.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Event {
    Pause,
    Resume,
    Cancel,
    Ask,
    Decide,
    Finish,
    Fail,
    Reply,
}

/// The state machine.
/// Returns: the next state, or None when the event does not apply.
fn next(state: State, event: Event) -> Option<State> {
    use State::*;
    Some(match (state, event) {
        (Running | Approval, Event::Pause) => Paused,
        (Paused, Event::Resume) => Running,
        (Running | Approval | Paused, Event::Cancel) => Cancelled,
        (Running, Event::Ask) => Approval,
        (Approval, Event::Decide) => Running,
        (Running | Paused, Event::Finish) => Done,
        (Running | Approval | Paused, Event::Fail) => Failed,
        (Done, Event::Reply) => Running,
        _ => return None,
    })
}

/// Why a task is paused.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum Pause {
    /// The host user pressed pause.
    HostUser,
    /// The host user used the keyboard or mouse.
    LocalInput,
    /// The controller that started the task paused it.
    Controller,
    /// A person at a controller took over with their own input.
    TakeOver,
    /// The controller that started the task disconnected.
    Disconnected,
    /// The computer went to sleep.
    Sleep,
    /// The session is locked or the sign-in screen shows.
    Locked,
}

/// One action of a task, as the history keeps it.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Record {
    pub at: u64,
    /// The action (`Action` as JSON), or null when it could not be read.
    pub action: Value,
    /// `ok`, `failed`, `declined`, `not-executed` or `paused`.
    pub outcome: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub detail: Option<String>,
}

/// An action waiting for the host user's approval.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Pending {
    pub action: Value,
    /// The provider's safety notes, if it flagged the turn.
    pub flagged: Vec<String>,
    /// Why it asks: the policy's class (`policy::Class`; none in a record
    /// from before 0.7.0).
    #[serde(default)]
    pub class: Option<Class>,
}

/// One run of a task: the message that began it (the instruction, then
/// each reply) and the model's answer to it.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Run {
    pub message: String,
    #[serde(default)]
    pub text: String,
    /// Its first step's index in the task's steps.
    #[serde(default)]
    pub first: usize,
    pub at: u64,
}

/// Where a task's kept conversation is.
#[derive(Default)]
enum Kept {
    #[default]
    None,
    /// In memory only (No logs, or the file could not be written).
    Memory(Conversation),
    /// In `ai-conversations/<id>.json`.
    Disk,
}

/// A task's kept conversation: the latest one the sidecar handed over
/// (`FromSidecar::State`) and where each finished run left it, so a fork
/// can begin after any of them.
#[derive(Clone, Serialize, Deserialize)]
struct Conversation {
    state: Value,
    #[serde(default)]
    cuts: Vec<Cut>,
    /// The instructions it ran with, which compaction sends again so the
    /// provider's cached prefix still matches.
    #[serde(default)]
    system: String,
}

/// Where a finished run left the conversation: the length of the
/// provider's history (Claude, chat), or OpenAI's response id.
#[derive(Clone, Serialize, Deserialize)]
struct Cut {
    run: usize,
    at: Value,
}

impl Conversation {
    /// The conversation as it was when `run` finished; None when that run
    /// did not finish or the record does not fit.
    fn at(&self, run: usize) -> Option<Value> {
        let cut = self.cuts.iter().find(|c| c.run == run)?;
        match &cut.at {
            Value::Number(n) => {
                let messages = self.state["messages"].as_array()?;
                let n = usize::try_from(n.as_u64()?).ok()?;
                Some(json!({"messages": messages.get(..n)?}))
            }
            Value::String(_) => Some(json!({"previous": cut.at})),
            // A compaction's summary.
            Value::Object(_) => Some(cut.at.clone()),
            _ => None,
        }
    }
}

/// Where a conversation stands: its history's length, OpenAI's last
/// response id, or after a compaction the summary itself.
fn cut_of(state: &Value) -> Value {
    if state.get("summary").is_some() {
        return state.clone();
    }
    state["messages"]
        .as_array()
        .map_or_else(|| state["previous"].clone(), |m| json!(m.len()))
}

/// One AI task.
#[derive(Serialize, Deserialize)]
pub struct Task {
    pub id: u64,
    /// The caller's idempotency key: starting twice with one key starts one
    /// task.
    #[serde(skip)]
    key: String,
    pub instruction: String,
    /// The Device ID of the controller that started the task; None for the
    /// host's own user.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub origin: Option<String>,
    /// The source the task uses (`provider` in the first versions, whose
    /// values are the default sources' ids).
    #[serde(alias = "provider")]
    pub source: String,
    pub model: String,
    /// How much its model thinks (None: the usual).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub thinking: Option<Thinking>,
    pub state: State,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub paused: Option<Pause>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
    /// The model's latest text.
    pub text: String,
    /// The newest progress notes.
    pub progress: Vec<String>,
    pub steps: Vec<Record>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub approval: Option<Pending>,
    pub created: u64,
    pub updated: u64,
    /// Bumped by every pause and cancel: work begun under an older value
    /// never acts.
    #[serde(skip)]
    lease: u64,
    /// When the task was last given the desktop (started, resumed or an
    /// action approved): local input before it is not a take-over.
    #[serde(skip)]
    since: Option<Instant>,
    #[serde(skip)]
    decision: Option<bool>,
    /// Actions the policy refused or the host user declined.
    #[serde(skip)]
    refusals: u32,
    /// The tokens its model turns took, summed.
    #[serde(default)]
    pub tokens: super::Usage,
    /// The name the user gave it; None while its instruction names it.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub name: Option<String>,
    /// Its runs in order (none in a record from before 0.7.0).
    #[serde(default)]
    pub runs: Vec<Run>,
    /// The conversation a reply continues once the sidecar is gone.
    #[serde(skip)]
    kept: Kept,
    /// The input tokens of its latest request: how long its conversation
    /// has grown (`AUTO_COMPACT`).
    #[serde(default)]
    pub context: u64,
    /// Its conversation is being compacted (`Ai::compact`).
    #[serde(
        default,
        skip_deserializing,
        skip_serializing_if = "std::ops::Not::not"
    )]
    pub compacting: bool,
    /// The host user's next message for a finished task.
    #[serde(skip)]
    reply: Option<String>,
    /// The agent sidecar, kept after the task finishes so a reply continues
    /// its conversation (until the engine stops).
    #[serde(skip)]
    conversation: Option<Agent>,
}

/// Most progress notes and steps kept per task.
const MAX_PROGRESS: usize = 20;
const MAX_STEPS: usize = 500;
/// Most finished tasks kept in the history.
const MAX_HISTORY: usize = 100;
/// Most runs a task takes (its instruction and replies).
const MAX_RUNS: usize = 200;
/// Longest name a task may be given.
const MAX_TASK_NAME: usize = 200;
/// The kept conversations' folder in the state directory.
const CONVERSATIONS: &str = "ai-conversations";
/// A reply to a task whose latest request took at least this many input
/// tokens compacts its conversation first.
const AUTO_COMPACT: u64 = 150_000;
/// Steps a session's task snapshot carries.
const SESSION_STEPS: usize = 20;
/// How often local input is checked while the model thinks.
const WATCH: Duration = Duration::from_millis(100);

/// How a task's desktop is opened: the real screen, or a test double.
type OpenDesktop = Arc<dyn Fn() -> Result<Box<dyn Desktop>, String> + Send + Sync>;

/// What the runners share with the calls.
struct Shared {
    tasks: Mutex<Vec<Task>>,
    settings: Mutex<Settings>,
    /// Woken on every task change.
    changed: Notify,
    secrets: Arc<Secrets>,
    dir: PathBuf,
    open_desktop: OpenDesktop,
    /// Provider URLs replaced by a local server in tests.
    url: Option<String>,
    /// The agent sidecar's command (`agent_command`).
    agent: Vec<String>,
    /// The instruction being written, shared by the host's user and every
    /// controller: its text and a serial that changes with it.
    draft: Mutex<(u64, String)>,
    /// The programs the host user granted the AI in this session (AI
    /// scope), or None for the whole desktop; kept in memory only.
    scope: Mutex<Option<Vec<String>>>,
}

/// The host's AI runtime, owned by the core.
pub struct Ai {
    shared: Arc<Shared>,
    client: Arc<Client>,
    /// Taken when the runtime shuts down (see Drop).
    rt: Option<tokio::runtime::Runtime>,
}

impl Ai {
    /// Loads the settings and the task history.
    /// Side effects: starts a small runtime for the runners.
    pub fn new(dir: &Path, secrets: Arc<Secrets>) -> Result<Ai, String> {
        Ai::with(dir, secrets, Arc::new(open_screen), None, agent_command())
    }

    fn with(
        dir: &Path,
        secrets: Arc<Secrets>,
        open_desktop: OpenDesktop,
        url: Option<String>,
        agent: Vec<String>,
    ) -> Result<Ai, String> {
        let rt = tokio::runtime::Builder::new_multi_thread()
            .worker_threads(2)
            .thread_name("kyokai-ai")
            .enable_all()
            .build()
            .map_err(|e| format!("AI runtime: {e}"))?;
        let settings = Settings::load(&dir.join(SETTINGS));
        let mut tasks: Vec<Task> = read_json(&dir.join(HISTORY)).unwrap_or_default();
        for t in &mut tasks {
            if conversation_path(dir, t.id).exists() {
                t.kept = Kept::Disk;
            }
        }
        Ok(Ai {
            shared: Arc::new(Shared {
                tasks: Mutex::new(tasks),
                settings: Mutex::new(settings),
                changed: Notify::new(),
                secrets,
                dir: dir.to_path_buf(),
                open_desktop,
                url,
                agent,
                draft: Mutex::new((0, String::new())),
                scope: Mutex::new(None),
            }),
            client: Arc::new(Client::new()?),
            rt: Some(rt),
        })
    }

    /// The settings and which sources have a key (never the key).
    pub fn settings(&self) -> Value {
        let s = self.shared.settings.lock().expect("settings").clone();
        let keys: serde_json::Map<String, Value> = s
            .sources
            .iter()
            .map(|x| {
                (
                    x.id.clone(),
                    json!(self.shared.secrets.get(&key_name(&x.id)).is_some()),
                )
            })
            .collect();
        json!({
            "sources": s.sources,
            "model": s.model,
            "approval": s.approval,
            "keys": keys,
            "scope": self.shared.scope.lock().expect("scope").clone(),
        })
    }

    /// Grants the AI only these programs' windows (names as
    /// `policy::Target::program` has them), or the whole desktop with None.
    /// Side effects: applies to the running task from its next action and
    /// screenshot; not saved.
    pub fn set_scope(&self, apps: Option<Vec<String>>) -> Result<(), String> {
        let apps = apps.map(|list| {
            let mut list: Vec<String> = list
                .iter()
                .map(|a| a.trim().to_lowercase())
                .filter(|a| !a.is_empty())
                .collect();
            list.sort();
            list.dedup();
            list
        });
        if apps
            .as_ref()
            .is_some_and(|l| l.len() > 64 || l.iter().any(|a| a.len() > 256))
        {
            return Err("too many or too long app names".into());
        }
        *self.shared.scope.lock().expect("scope") = apps;
        self.shared.changed.notify_waiters();
        Ok(())
    }

    /// The programs with windows on the screen now, for choosing a scope:
    /// their names as `set_scope` takes them, sorted.
    /// Side effects: opens the desktop briefly (through the session agent in
    /// the helper modes).
    pub fn apps(&self) -> Result<Vec<String>, String> {
        let mut desk = (self.shared.open_desktop)()?;
        let mut apps: Vec<String> = desk
            .windows()
            .into_iter()
            .filter_map(|w| w.program)
            .collect();
        apps.sort();
        apps.dedup();
        Ok(apps)
    }

    /// Changes the settings; absent fields stay.
    /// Side effects: writes `ai.json`.
    pub fn set_settings(&self, change: &Value) -> Result<Value, String> {
        {
            let mut s = self.shared.settings.lock().expect("settings");
            let mut doc = serde_json::to_value(&*s).expect("settings serialize");
            for (k, v) in change.as_object().ok_or("bad AI settings")? {
                if doc.get(k).is_none() || v.is_null() {
                    return Err(format!("unknown AI setting {k:?}"));
                }
                doc[k] = v.clone();
            }
            let new: Settings =
                serde_json::from_value(doc).map_err(|e| format!("bad AI settings: {e}"))?;
            new.validate()?;
            // A removed source takes its key with it, and so does one whose
            // address or protocol changed: a key goes only where it was
            // given for. Dropped before the new settings are saved, so a
            // failure leaves the old ones.
            for old in &s.sources {
                if new
                    .source(&old.id)
                    .is_none_or(|n| n.url != old.url || n.protocol != old.protocol)
                {
                    self.shared.secrets.remove(&key_name(&old.id))?;
                }
            }
            write_json(&self.shared.dir.join(SETTINGS), &new)?;
            *s = new;
        }
        Ok(self.settings())
    }

    /// The models a source lists (`GET <base>/models`), for the user to
    /// pick from; nothing is saved.
    /// Returns: the models, or why they could not be listed.
    pub fn list_models(&self, source: &str) -> Result<Vec<super::Model>, String> {
        // The source and its key are read together, so a key never meets
        // an address it was not given for.
        let (s, key) = {
            let settings = self.shared.settings.lock().expect("settings");
            let s = settings.source(source).cloned().ok_or("no such source")?;
            let key = self.shared.key(&s);
            (s, key)
        };
        let key = key.ok_or("add an API key for the provider first")?;
        let url = format!("{}/models", s.url.trim_end_matches('/'));
        let client = self.client.clone();
        let doc = self
            .rt
            .as_ref()
            .expect("runtime")
            .block_on(async move { client.get(&url, s.protocol, &key).await })?;
        Ok(super::parse_models(&doc))
    }

    /// Whether a task could start now: a model chosen and, unless its
    /// server is on this computer, its provider's key present.
    pub fn ready(&self) -> bool {
        let settings = self.shared.settings.lock().expect("settings").clone();
        settings
            .source(&settings.model.source)
            .is_some_and(|source| self.shared.key(source).is_some())
    }

    /// Stores a source's API key, or removes it (`None`).
    /// Side effects: writes the secrets store; never logs the key.
    /// Notes: holds the settings while it writes, so the key belongs to the
    /// source's address as it is now (a change of address drops it).
    pub fn set_key(&self, source: &str, key: Option<&str>) -> Result<(), String> {
        let settings = self.shared.settings.lock().expect("settings");
        if settings.source(source).is_none() {
            return Err("no such source".into());
        }
        match key.map(str::trim) {
            Some("") | None => self.shared.secrets.remove(&key_name(source)),
            Some(k) => self.shared.secrets.set(&key_name(source), k),
        }
    }

    /// Replaces the shared draft of the next instruction.
    /// Returns: an error for a draft longer than an instruction may be.
    pub fn set_draft(&self, text: &str) -> Result<(), String> {
        if text.len() > MAX_INSTRUCTION {
            return Err("the instruction is too long".into());
        }
        let mut d = self.shared.draft.lock().expect("draft");
        if d.1 != text {
            *d = (d.0 + 1, text.to_owned());
        }
        Ok(())
    }

    /// The shared draft and its serial.
    pub fn draft(&self) -> (u64, String) {
        self.shared.draft.lock().expect("draft").clone()
    }

    /// Starts a task with the settings' model and the usual thinking (the
    /// tests' shorthand).
    #[cfg(test)]
    pub fn start(&self, key: &str, instruction: &str, origin: Option<&str>) -> Result<u64, String> {
        self.start_with(key, instruction, origin, Choice::default())
    }

    /// Starts a task.
    /// Inputs: `key` — the caller's idempotency key; a second start with the
    /// same key returns the first task; `origin` — the Device ID of the
    /// controller that asked, None for the host's own user; `choice` — its
    /// model (None: the settings') and thinking (None: the usual).
    /// Returns: the task's id.
    pub fn start_with(
        &self,
        key: &str,
        instruction: &str,
        origin: Option<&str>,
        choice: Choice,
    ) -> Result<u64, String> {
        let instruction = instruction.trim();
        if instruction.is_empty() || instruction.len() > MAX_INSTRUCTION {
            return Err("describe the task in a few sentences".into());
        }
        let settings = self.shared.settings.lock().expect("settings").clone();
        let model = choice.model.unwrap_or_else(|| settings.model.clone());
        if model.id.trim().is_empty() || model.id.len() > 200 {
            return Err("choose a model first".into());
        }
        let source = settings
            .source(&model.source)
            .ok_or("choose a model first")?;
        if !keyless(source) && self.shared.secrets.get(&key_name(&source.id)).is_none() {
            return Err("add an API key for the provider first".into());
        }
        let id = {
            let mut tasks = self.shared.tasks.lock().expect("tasks");
            if let Some(t) = tasks
                .iter()
                .find(|t| !key.is_empty() && t.key == key && t.origin.as_deref() == origin)
            {
                return Ok(t.id);
            }
            if tasks.iter().any(|t| !t.state.finished()) {
                return Err("another AI task is still in progress".into());
            }
            let id = tasks.iter().map(|t| t.id).max().unwrap_or(0) + 1;
            let now = now();
            tasks.push(Task {
                id,
                key: key.into(),
                instruction: instruction.into(),
                origin: origin.map(str::to_string),
                source: model.source.clone(),
                model: model.id.clone(),
                thinking: choice.thinking,
                state: State::Running,
                paused: None,
                error: None,
                text: String::new(),
                progress: Vec::new(),
                steps: Vec::new(),
                approval: None,
                created: now,
                updated: now,
                lease: 0,
                since: Some(Instant::now()),
                decision: None,
                refusals: 0,
                tokens: Default::default(),
                name: None,
                runs: vec![Run {
                    message: instruction.into(),
                    text: String::new(),
                    first: 0,
                    at: now,
                }],
                kept: Kept::None,
                context: 0,
                compacting: false,
                reply: None,
                conversation: None,
            });
            id
        };
        self.spawn(id);
        // The draft became this task.
        let _ = self.set_draft("");
        Ok(id)
    }

    /// Continues a finished task with the host user's next message (the
    /// tests' shorthand).
    #[cfg(test)]
    pub fn reply(&self, id: u64, text: &str) -> Result<(), String> {
        self.reply_with(id, text, Choice::default())
    }

    /// Continues a finished task, perhaps with another model of its source
    /// or other thinking (`choice`; None keeps the task's): the conversation
    /// then goes on in a new sidecar from the kept one.
    pub fn reply_with(&self, id: u64, text: &str, choice: Choice) -> Result<(), String> {
        let text = text.trim();
        if text.is_empty() || text.len() > MAX_INSTRUCTION {
            return Err("write a message first".into());
        }
        {
            let mut tasks = self.shared.tasks.lock().expect("tasks");
            if tasks.iter().any(|t| t.id != id && !t.state.finished()) {
                return Err("another AI task is still in progress".into());
            }
            let t = tasks
                .iter_mut()
                .find(|t| t.id == id)
                .ok_or("no such task")?;
            if t.conversation.is_none() && matches!(t.kept, Kept::None) {
                return Err("this task can no longer continue".into());
            }
            if t.compacting {
                return Err("the task is being compacted; reply in a moment".into());
            }
            if t.runs.len() >= MAX_RUNS {
                return Err("this task is too long to continue; start a new one".into());
            }
            let model = choice
                .model
                .filter(|m| m.id != t.model || m.source != t.source);
            if let Some(m) = &model {
                // The conversation lives with the provider it began with.
                if m.source != t.source {
                    return Err("a task keeps its provider; start a new task for another".into());
                }
                if m.id.trim().is_empty() || m.id.len() > 200 {
                    return Err("choose a model first".into());
                }
            }
            let thinking = choice.thinking.filter(|&k| Some(k) != t.thinking);
            if (model.is_some() || thinking.is_some()) && matches!(t.kept, Kept::None) {
                return Err("this task can no longer change its model".into());
            }
            t.state = next(t.state, Event::Reply).ok_or("the task is not finished")?;
            if let Some(m) = model {
                t.model = m.id;
                t.conversation = None;
            }
            if let Some(k) = thinking {
                t.thinking = Some(k);
                t.conversation = None;
            }
            t.runs.push(Run {
                message: text.into(),
                text: String::new(),
                first: t.steps.len(),
                at: now(),
            });
            t.reply = Some(text.into());
            t.error = None;
            t.since = Some(Instant::now());
            t.updated = now();
        }
        self.shared.changed.notify_waiters();
        self.spawn(id);
        Ok(())
    }

    fn spawn(&self, id: u64) {
        let shared = self.shared.clone();
        self.rt
            .as_ref()
            .expect("running until dropped")
            .spawn(async move {
                let outcome = run(&shared, id).await;
                shared.end(id, outcome);
            });
    }

    /// Pauses a task, saying who did.
    pub fn pause(&self, id: u64, by: Pause) -> Result<(), String> {
        self.shared.apply(id, Event::Pause, |t| t.paused = Some(by))
    }

    /// Pauses the task in progress because a person at a controller is
    /// operating the desktop: the AI's input lease ends at once.
    // Used by the Windows input service; macOS input comes with the Mac pass.
    #[cfg_attr(not(windows), allow(dead_code))]
    pub fn take_over(&self) {
        self.pause_current(Pause::TakeOver);
    }

    /// Pauses the task in progress, if there is one, saying why.
    pub fn pause_current(&self, by: Pause) {
        let id = {
            let tasks = self.shared.tasks.lock().expect("tasks");
            tasks
                .iter()
                .find(|t| matches!(t.state, State::Running | State::Approval))
                .map(|t| t.id)
        };
        if let Some(id) = id {
            let _ = self.pause(id, by);
        }
    }

    /// Pauses the task a controller started when its session ends: an AI
    /// task never works on for a controller that is gone.
    pub fn controller_left(&self, device_id: &str) {
        let id = {
            let tasks = self.shared.tasks.lock().expect("tasks");
            tasks
                .iter()
                .find(|t| {
                    t.origin.as_deref() == Some(device_id)
                        && matches!(t.state, State::Running | State::Approval)
                })
                .map(|t| t.id)
        };
        if let Some(id) = id {
            let _ = self.pause(id, Pause::Disconnected);
        }
    }

    /// Whether a task holds the desktop now (running or waiting for
    /// approval).
    #[cfg_attr(not(windows), allow(dead_code))]
    pub fn acting(&self) -> bool {
        let tasks = self.shared.tasks.lock().expect("tasks");
        tasks
            .iter()
            .any(|t| matches!(t.state, State::Running | State::Approval))
    }

    /// Whether a task waits for the host user's approval of an action: while
    /// one does, no session's input reaches Kyokai's own windows (only those
    /// allowed to approve answer it, through the session).
    #[cfg_attr(
        not(any(windows, target_os = "macos", target_os = "linux")),
        allow(dead_code)
    )]
    pub fn awaiting_approval(&self) -> bool {
        let tasks = self.shared.tasks.lock().expect("tasks");
        tasks.iter().any(|t| matches!(t.state, State::Approval))
    }

    /// The newest task, whoever started it (the host's user or a
    /// controller), as sessions show it: one record for every controller,
    /// with its latest steps and progress notes only.
    pub fn latest(&self) -> Option<Value> {
        let tasks = self.shared.tasks.lock().expect("tasks");
        let t = tasks.iter().next_back()?;
        let mut v = record(t);
        let trim = |v: &mut Value, keep: usize| {
            if let Some(a) = v.as_array_mut() {
                let extra = a.len().saturating_sub(keep);
                a.drain(..extra);
            }
        };
        trim(&mut v["steps"], SESSION_STEPS);
        trim(&mut v["progress"], 3);
        trim(&mut v["runs"], SESSION_STEPS);
        Some(v)
    }

    /// Resumes a paused task.
    pub fn resume(&self, id: u64) -> Result<(), String> {
        self.shared.apply(id, Event::Resume, |t| t.paused = None)
    }

    /// Cancels a task.
    pub fn cancel(&self, id: u64) -> Result<(), String> {
        self.shared.apply(id, Event::Cancel, |_| {})
    }

    /// Answers a task's approval request.
    pub fn decide(&self, id: u64, approve: bool) -> Result<(), String> {
        self.shared.apply(id, Event::Decide, |t| {
            t.decision = Some(approve);
            t.approval = None;
        })
    }

    /// The emergency stop: cancels every task that has not finished.
    pub fn stop_all(&self) {
        let ids: Vec<u64> = {
            let tasks = self.shared.tasks.lock().expect("tasks");
            tasks
                .iter()
                .filter(|t| !t.state.finished())
                .map(|t| t.id)
                .collect()
        };
        for id in ids {
            let _ = self.cancel(id);
        }
    }

    /// Every task, newest first, each saying whether a reply can continue
    /// it (`resumable`).
    pub fn tasks(&self) -> Value {
        let tasks = self.shared.tasks.lock().expect("tasks");
        tasks.iter().rev().map(record).collect()
    }

    /// Names a task, or gives it back its instruction's name (empty).
    /// Side effects: saves the history.
    pub fn rename(&self, id: u64, name: &str) -> Result<(), String> {
        let name = name.trim();
        if name.chars().count() > MAX_TASK_NAME {
            return Err("the name is too long".into());
        }
        let mut tasks = self.shared.tasks.lock().expect("tasks");
        let t = tasks
            .iter_mut()
            .find(|t| t.id == id)
            .ok_or("no such task")?;
        t.name = (!name.is_empty()).then(|| name.to_owned());
        t.updated = now();
        let saved = save_history(&self.shared.dir, &tasks);
        drop(tasks);
        self.shared.changed.notify_waiters();
        saved
    }

    /// Compacts a finished task's conversation by hand: the model
    /// summarises it, and a reply then continues from the summary.
    /// Side effects: runs in the background, the task showing `compacting`
    /// meanwhile; a failure is its `error`, the task staying done.
    pub fn compact(&self, id: u64) -> Result<(), String> {
        {
            let mut tasks = self.shared.tasks.lock().expect("tasks");
            let t = tasks
                .iter_mut()
                .find(|t| t.id == id)
                .ok_or("no such task")?;
            if t.state != State::Done {
                return Err("only a finished task can be compacted".into());
            }
            if t.compacting {
                return Err("the task is already being compacted".into());
            }
            if t.conversation.is_none() && matches!(t.kept, Kept::None) {
                return Err("this task's conversation was not kept".into());
            }
            t.compacting = true;
            t.error = None;
        }
        self.shared.changed.notify_waiters();
        let shared = self.shared.clone();
        self.rt
            .as_ref()
            .expect("running until dropped")
            .spawn(async move {
                let outcome = compact(&shared, id).await;
                shared.edit(id, |t| {
                    t.compacting = false;
                    t.error = outcome.err();
                });
                let tasks = shared.tasks.lock().expect("tasks");
                let _ = save_history(&shared.dir, &tasks);
            });
        Ok(())
    }

    /// Starts a new finished task from another's conversation as it was
    /// after run `run` (a fork), or as it is now (a clone, None): the runs,
    /// steps and answer up to there, and the kept conversation a reply
    /// continues; the original is untouched.
    /// Returns: the new task's id.
    /// Side effects: saves the history and the new task's conversation.
    pub fn fork(&self, id: u64, run: Option<usize>) -> Result<u64, String> {
        let (new, conversation) = {
            let mut tasks = self.shared.tasks.lock().expect("tasks");
            let t = tasks.iter().find(|t| t.id == id).ok_or("no such task")?;
            if !t.state.finished() {
                return Err("the task is still working".into());
            }
            let c = self
                .shared
                .conversation(t)
                .ok_or("this task's conversation was not kept")?;
            let run = run
                .or_else(|| c.cuts.last().map(|x| x.run))
                .ok_or("this task never finished a run")?;
            let state = c.at(run).ok_or("the task did not finish that run")?;
            let end = t
                .runs
                .get(run + 1)
                .map_or(t.steps.len(), |r| r.first)
                .min(t.steps.len());
            let new = tasks.iter().map(|t| t.id).max().unwrap_or(0) + 1;
            let now = now();
            let copy = Task {
                id: new,
                key: String::new(),
                instruction: t.instruction.clone(),
                origin: None,
                source: t.source.clone(),
                model: t.model.clone(),
                thinking: t.thinking,
                state: State::Done,
                paused: None,
                error: None,
                text: t.runs.get(run).map(|r| r.text.clone()).unwrap_or_default(),
                progress: Vec::new(),
                steps: t.steps[..end].to_vec(),
                approval: None,
                created: now,
                updated: now,
                lease: 0,
                since: None,
                decision: None,
                refusals: 0,
                tokens: Default::default(),
                name: t.name.clone(),
                runs: t.runs.iter().take(run + 1).cloned().collect(),
                kept: Kept::None,
                context: 0,
                compacting: false,
                reply: None,
                conversation: None,
            };
            let cuts = c.cuts.into_iter().filter(|x| x.run <= run).collect();
            tasks.push(copy);
            trim_history(&self.shared.dir, &mut tasks);
            save_history(&self.shared.dir, &tasks)?;
            (
                new,
                Conversation {
                    state,
                    cuts,
                    system: c.system,
                },
            )
        };
        let kept = self.shared.store(new, conversation);
        self.shared.edit(new, |t| t.kept = kept);
        Ok(new)
    }

    /// A task as Markdown for the user to keep: its name, model and runs,
    /// each with its message, steps and answer; never a screenshot.
    pub fn export(&self, id: u64) -> Result<String, String> {
        let tasks = self.shared.tasks.lock().expect("tasks");
        let t = tasks.iter().find(|t| t.id == id).ok_or("no such task")?;
        Ok(markdown(t))
    }

    /// The task in progress, for the status poll: its id, state and whether
    /// it waits for approval.
    pub fn active(&self) -> Value {
        let tasks = self.shared.tasks.lock().expect("tasks");
        match tasks.iter().find(|t| !t.state.finished()) {
            Some(t) => {
                json!({"id": t.id, "state": t.state, "paused": t.paused, "origin": t.origin})
            }
            None => Value::Null,
        }
    }

    /// Clears the finished tasks from the history.
    /// Side effects: rewrites the history file.
    pub fn clear(&self) -> Result<(), String> {
        let mut tasks = self.shared.tasks.lock().expect("tasks");
        for t in tasks.iter().filter(|t| t.state.finished()) {
            forget_conversation(&self.shared.dir, t.id);
        }
        tasks.retain(|t| !t.state.finished());
        save_history(&self.shared.dir, &tasks)
    }
}

impl Drop for Ai {
    fn drop(&mut self) {
        // Nothing acts after the core stops: the runners are cancelled with
        // the runtime, and each runner's desktop releases what it holds. The
        // last owner may be a session task, so the runtime shuts down without
        // blocking.
        self.stop_all();
        if let Some(rt) = self.rt.take() {
            rt.shutdown_background();
        }
    }
}

/// Longest task instruction or reply.
const MAX_INSTRUCTION: usize = 8 * 1024;
const SETTINGS: &str = "ai.json";
const HISTORY: &str = "ai-tasks.json";

impl Shared {
    /// The session's AI scope (`Ai::set_scope`).
    fn scope(&self) -> Option<Vec<String>> {
        self.scope.lock().expect("scope").clone()
    }

    /// Applies an event to a task and wakes its runner.
    fn apply(&self, id: u64, event: Event, f: impl FnOnce(&mut Task)) -> Result<(), String> {
        {
            let mut tasks = self.tasks.lock().expect("tasks");
            let t = tasks
                .iter_mut()
                .find(|t| t.id == id)
                .ok_or("no such task")?;
            t.state = next(t.state, event).ok_or("not possible in the task's state")?;
            if matches!(event, Event::Pause | Event::Cancel) {
                t.lease += 1;
                t.approval = None;
                t.decision = None;
            }
            if matches!(event, Event::Resume | Event::Decide) {
                t.since = Some(Instant::now());
            }
            t.updated = now();
            f(t);
        }
        self.changed.notify_waiters();
        Ok(())
    }

    /// Changes a task without a state change.
    fn edit<T>(&self, id: u64, f: impl FnOnce(&mut Task) -> T) -> Option<T> {
        let out = {
            let mut tasks = self.tasks.lock().expect("tasks");
            let t = tasks.iter_mut().find(|t| t.id == id)?;
            t.updated = now();
            f(t)
        };
        self.changed.notify_waiters();
        Some(out)
    }

    /// The key to send to a source: its stored key over HTTPS, nothing over
    /// plain HTTP.
    /// Returns: None when the source needs a key and has none.
    /// Notes: the caller holds the settings `s` came from, so the key and
    /// the address belong together.
    fn key(&self, s: &Source) -> Option<Zeroizing<String>> {
        if !https(s) {
            return Some(Zeroizing::new(String::new()));
        }
        self.secrets
            .get(&key_name(&s.id))
            .or_else(|| keyless(s).then(|| Zeroizing::new(String::new())))
    }

    /// The task's state and lease.
    fn phase(&self, id: u64) -> (State, u64) {
        let tasks = self.tasks.lock().expect("tasks");
        tasks
            .iter()
            .find(|t| t.id == id)
            .map_or((State::Cancelled, 0), |t| (t.state, t.lease))
    }

    /// Whether the host user used the keyboard or mouse since the task was
    /// last given the desktop.
    fn local_input(&self, id: u64, desk: &Desk) -> bool {
        let since = {
            let tasks = self.tasks.lock().expect("tasks");
            tasks.iter().find(|t| t.id == id).and_then(|t| t.since)
        };
        since.is_some_and(|s| desk.lock().expect("desktop").local_input().after(s))
    }

    /// Pauses a running task because of local input.
    fn yield_to_user(&self, id: u64) {
        let _ = self.apply(id, Event::Pause, |t| t.paused = Some(Pause::LocalInput));
    }

    /// Waits until the task runs.
    /// Returns: its lease, or `Stopped` once it is cancelled.
    async fn running(&self, id: u64) -> Result<u64, Stop> {
        loop {
            let woken = self.changed.notified();
            tokio::pin!(woken);
            woken.as_mut().enable();
            match self.phase(id) {
                (State::Running, lease) => return Ok(lease),
                (State::Approval | State::Paused, _) => woken.await,
                _ => return Err(Stop::Cancelled),
            }
        }
    }

    /// Waits until the lease changes or the task stops running.
    async fn interrupted(&self, id: u64, lease: u64) {
        loop {
            let woken = self.changed.notified();
            tokio::pin!(woken);
            woken.as_mut().enable();
            match self.phase(id) {
                (State::Running | State::Approval, l) if l == lease => woken.await,
                _ => return,
            }
        }
    }

    /// Records the runner's end and saves the history.
    fn end(&self, id: u64, outcome: Result<Agent, Stop>) {
        {
            let mut tasks = self.tasks.lock().expect("tasks");
            if let Some(t) = tasks.iter_mut().find(|t| t.id == id) {
                match outcome {
                    Ok(conversation) => {
                        if let Some(s) = next(t.state, Event::Finish) {
                            t.state = s;
                        }
                        t.conversation = Some(conversation);
                    }
                    Err(Stop::Cancelled) => {}
                    Err(Stop::Failed(why)) => {
                        if let Some(s) = next(t.state, Event::Fail) {
                            t.state = s;
                            t.error = Some(why);
                        }
                    }
                }
                t.approval = None;
                t.updated = now();
                let text = t.text.clone();
                if let Some(run) = t.runs.last_mut() {
                    run.text = text;
                }
            }
            trim_history(&self.dir, &mut tasks);
            let _ = save_history(&self.dir, &tasks);
        }
        self.changed.notify_waiters();
    }

    /// A task's kept conversation, read from disk when it is there.
    fn conversation(&self, t: &Task) -> Option<Conversation> {
        match &t.kept {
            Kept::None => None,
            Kept::Memory(c) => Some(c.clone()),
            Kept::Disk => read_json(&conversation_path(&self.dir, t.id)),
        }
    }

    /// Stores a task's kept conversation: on disk, or only in memory while
    /// No logs is on.
    /// Returns: where it is now.
    fn store(&self, id: u64, c: Conversation) -> Kept {
        let memory = self.secrets.get(crate::access::NO_HISTORY_NAME).is_some();
        let path = conversation_path(&self.dir, id);
        if !memory
            && std::fs::create_dir_all(self.dir.join(CONVERSATIONS)).is_ok()
            && write_json(&path, &c).is_ok()
        {
            Kept::Disk
        } else {
            let _ = std::fs::remove_file(&path);
            Kept::Memory(c)
        }
    }

    /// Keeps a task's conversation as the sidecar handed it over after its
    /// latest run, noting where that run left it and the instructions it
    /// ran with.
    fn keep(&self, id: u64, state: Value, system: &str) {
        let Some((old, run)) = ({
            let tasks = self.tasks.lock().expect("tasks");
            tasks
                .iter()
                .find(|t| t.id == id)
                .map(|t| (self.conversation(t), t.runs.len().saturating_sub(1)))
        }) else {
            return;
        };
        let mut c = old.unwrap_or(Conversation {
            state: Value::Null,
            cuts: Vec::new(),
            system: String::new(),
        });
        c.cuts.retain(|x| x.run < run);
        c.cuts.push(Cut {
            run,
            at: cut_of(&state),
        });
        c.state = state;
        c.system = system.to_owned();
        let kept = self.store(id, c);
        self.edit(id, |t| t.kept = kept);
    }

    /// Adds a step to the task's history.
    fn record(&self, id: u64, action: Value, outcome: &str, detail: Option<String>) {
        self.edit(id, |t| {
            if t.steps.len() < MAX_STEPS {
                t.steps.push(Record {
                    at: now(),
                    action,
                    outcome: outcome.into(),
                    detail,
                });
            }
        });
    }
}

/// Why a runner stopped early.
#[derive(Debug)]
enum Stop {
    Cancelled,
    /// The reason is safe to show.
    Failed(String),
}

impl From<String> for Stop {
    fn from(why: String) -> Self {
        Stop::Failed(why)
    }
}

/// The last screenshot the model saw, the frame it came from, and how the
/// model gives positions on it.
struct Seen {
    frame: Frame,
    shot: Shot,
    coords: Coordinates,
}

impl Seen {
    fn size(&self) -> (u32, u32) {
        (self.shot.width, self.shot.height)
    }
}

/// The desktop a runner drives, shared with blocking capture calls.
type Desk = Arc<Mutex<Box<dyn Desktop>>>;

/// Captures the screen on a blocking thread, dimmed outside the granted
/// programs' windows when the AI has a scope (`desktop::dim_outside`).
async fn capture(desk: &Desk, scope: Option<Vec<String>>) -> Result<Frame, String> {
    let desk = desk.clone();
    tokio::task::spawn_blocking(move || {
        let mut desk = desk.lock().expect("desktop");
        let mut frame = desk.capture()?;
        if let Some(apps) = scope {
            desktop::dim_outside(&mut frame, &desk.windows(), &apps);
        }
        Ok(frame)
    })
    .await
    .map_err(|_| "capture failed".to_string())?
}

/// Takes a screenshot for a model that gives positions as `coords` says.
async fn observe(
    desk: &Desk,
    coords: Coordinates,
    scope: Option<Vec<String>>,
) -> Result<Seen, String> {
    let multiple = coords.multiple();
    let frame = capture(desk, scope).await?;
    let shot = tokio::task::block_in_place(|| {
        desktop::shot(
            &frame,
            [0, 0, frame.width, frame.height],
            desktop::SHOT_MAX,
            multiple,
        )
    });
    Ok(Seen {
        frame,
        shot,
        coords,
    })
}

/// The instructions every task starts with.
fn system_prompt(seen: &Seen) -> String {
    let os = if cfg!(windows) {
        "Windows"
    } else if cfg!(target_os = "linux") {
        "Linux"
    } else {
        "macOS"
    };
    let (width, height) = seen.size();
    let coordinates = seen.coords.describe(width, height);
    format!(
        "You are operating a {os} computer for its user through the computer tools, on the \
         screen the user sees. {coordinates} The user can pause or stop you at any time and may decline \
         an action. Before anything irreversible or sensitive - deleting data, sending messages, \
         purchases, changing security settings, entering passwords - stop and ask the user in \
         text instead of acting. After acting, take a screenshot to check the result. When the \
         task is done, say briefly what you did."
    )
}

/// The agent sidecar's command: `kyokai-agent` beside this engine; in a
/// debug build `KYOKAI_AGENT` may name another program, or a `.ts` file Bun
/// runs (development).
fn agent_command() -> Vec<String> {
    if cfg!(debug_assertions)
        && let Ok(a) = std::env::var("KYOKAI_AGENT")
        && !a.is_empty()
    {
        return if a.ends_with(".ts") {
            vec!["bun".into(), a]
        } else {
            vec![a]
        };
    }
    let name = if cfg!(windows) {
        "kyokai-agent.exe"
    } else {
        "kyokai-agent"
    };
    let path = std::env::current_exe()
        .ok()
        .and_then(|e| e.parent().map(|d| d.join(name)))
        .unwrap_or_else(|| name.into());
    vec![path.to_string_lossy().into_owned()]
}

/// Opens the real desktop: this process's, or in the helper modes the
/// session agent's.
pub fn open_screen() -> Result<Box<dyn Desktop>, String> {
    if let Some(link) = crate::agent::link() {
        return Ok(Box::new(link.desktop()?));
    }
    #[cfg(any(windows, target_os = "macos", target_os = "linux"))]
    {
        Ok(Box::new(desktop::Screen::open()?))
    }
    #[cfg(not(any(windows, target_os = "macos", target_os = "linux")))]
    {
        Err("AI tasks are not available on this system yet".into())
    }
}

/// The source a task uses, checked to have its key (the sidecar asks for
/// the key per request).
fn task_source(shared: &Shared, id: &str) -> Result<Source, String> {
    let settings = shared.settings.lock().expect("settings");
    let source = settings
        .source(id)
        .cloned()
        .ok_or("the task's provider was removed")?;
    shared
        .key(&source)
        .ok_or("the provider's API key is missing")?;
    Ok(source)
}

/// The function tools an `openai-chat` source is offered; null for the
/// others, whose providers have their own computer tools.
fn chat_tools(source: &Source) -> Value {
    if source.protocol == Protocol::OpenaiChat {
        super::chat::tools()
    } else {
        Value::Null
    }
}

/// Compacts a kept conversation: the model summarises it with its tools
/// off, in a sidecar of its own (no screen, no lease, no action), and the
/// summary replaces it, as where run `run` left it.
/// Returns: the compacted conversation, for the caller to store.
/// Side effects: adds the summary's tokens to the task.
async fn summarize(
    shared: &Arc<Shared>,
    id: u64,
    source: &Source,
    model: &str,
    thinking: Option<Thinking>,
    c: &Conversation,
    run: usize,
) -> Result<Conversation, String> {
    let mut a = Agent::start(&shared.agent).await?;
    a.send(&ToSidecar::Start {
        task: id,
        protocol: source.protocol,
        url: shared.url.clone().unwrap_or_else(|| source.url.clone()),
        model: model.to_owned(),
        coordinates: Coordinates::Auto,
        system: c.system.clone(),
        instruction: String::new(),
        shot: super::sidecar::WireShot {
            b64: String::new(),
            width: 0,
            height: 0,
        },
        tools: chat_tools(source),
        resume: Some(c.state.clone()),
        compact: true,
        thinking,
    })?;
    loop {
        match a.recv().await? {
            FromSidecar::KeyRequest => {
                let key = shared
                    .key(source)
                    .ok_or("the provider's API key is missing")?;
                a.send(&ToSidecar::Key {
                    key: (!key.is_empty()).then(|| key.to_string()),
                })?;
            }
            FromSidecar::Turn { usage, .. } => {
                shared.edit(id, |t| t.tokens.add(usage));
            }
            FromSidecar::State { state } => {
                return Ok(Conversation {
                    cuts: vec![Cut {
                        run,
                        at: state.clone(),
                    }],
                    state,
                    system: c.system.clone(),
                });
            }
            FromSidecar::Failed { error } => return Err(format!("compaction failed: {error}")),
            _ => return Err("compaction failed: the summary did not come".into()),
        }
    }
}

/// Compacts a finished task's kept conversation by hand (`Ai::compact`).
async fn compact(shared: &Arc<Shared>, id: u64) -> Result<(), String> {
    let (source, model, thinking, kept, runs) = shared
        .edit(id, |t| {
            (
                t.source.clone(),
                t.model.clone(),
                t.thinking,
                shared.conversation(t),
                t.runs.len(),
            )
        })
        .ok_or("no such task")?;
    let kept = kept.ok_or("this task's conversation was not kept")?;
    let source = task_source(shared, &source)?;
    let c = summarize(
        shared,
        id,
        &source,
        &model,
        thinking,
        &kept,
        runs.saturating_sub(1),
    )
    .await?;
    let kept = shared.store(id, c);
    shared.edit(id, |t| {
        t.kept = kept;
        t.context = 0;
        // The sidecar's conversation is the long one: a reply resumes the
        // summary in a new sidecar.
        t.conversation = None;
    });
    Ok(())
}

/// Runs a task until it finishes, fails or is cancelled.
/// Returns: the agent sidecar when the model finished (so the task can
/// continue later).
async fn run(shared: &Arc<Shared>, id: u64) -> Result<Agent, Stop> {
    let (source, model, thinking, instruction, reply, conversation, kept, context, runs) = shared
        .edit(id, |t| {
            (
                t.source.clone(),
                t.model.clone(),
                t.thinking,
                t.instruction.clone(),
                t.reply.take(),
                t.conversation.take(),
                shared.conversation(t),
                t.context,
                t.runs.len(),
            )
        })
        .ok_or(Stop::Cancelled)?;
    if reply.is_some() && conversation.is_none() && kept.is_none() {
        return Err(Stop::Failed("the task's conversation was not kept".into()));
    }
    let source = task_source(shared, &source)?;
    // A long conversation is compacted before a reply continues it; the
    // reply then starts afresh from the summary.
    let (conversation, kept) = match kept {
        Some(c) if reply.is_some() && context >= AUTO_COMPACT => {
            drop(conversation);
            let c = summarize(
                shared,
                id,
                &source,
                &model,
                thinking,
                &c,
                runs.saturating_sub(2),
            )
            .await?;
            let state = c.state.clone();
            let kept = shared.store(id, c);
            shared.edit(id, |t| {
                t.kept = kept;
                t.context = 0;
            });
            (None, Some(state))
        }
        other => (conversation, other.map(|c| c.state)),
    };
    let coords = source
        .models
        .iter()
        .find(|m| m.id == model)
        .map_or(Coordinates::Auto, |m| m.coordinates)
        .resolve(source.protocol == super::Protocol::OpenaiChat, &model);
    let desk: Desk = Arc::new(Mutex::new((shared.open_desktop)()?));
    let watcher = tokio::spawn(watch(shared.clone(), id, desk.clone()));
    let result = drive(
        shared,
        id,
        &desk,
        &source,
        Start {
            model,
            thinking,
            instruction,
            reply,
            conversation,
            resume: kept,
            coords,
        },
    )
    .await;
    watcher.abort();
    desk.lock().expect("desktop").release();
    result
}

/// Watches the host user for as long as the task's runner lives: local
/// input pauses a running task at once (also while the model thinks), and
/// Ctrl+Alt+Shift+Esc cancels it.
async fn watch(shared: Arc<Shared>, id: u64, desk: Desk) {
    let mut tick = tokio::time::interval(WATCH);
    loop {
        tick.tick().await;
        if desk.lock().expect("desktop").stop_requested() {
            let _ = shared.apply(id, Event::Cancel, |_| {});
            return;
        }
        if shared.phase(id).0 == State::Running && shared.local_input(id, &desk) {
            shared.yield_to_user(id);
        }
    }
}

/// How a runner begins: a new conversation, or a reply to a finished one.
struct Start {
    model: String,
    thinking: Option<Thinking>,
    /// How the model gives positions (resolved).
    coords: Coordinates,
    instruction: String,
    reply: Option<String>,
    conversation: Option<Agent>,
    /// The kept conversation a reply continues when the sidecar is gone.
    resume: Option<Value>,
}

/// Runs a task through the agent sidecar (doc/en-us/architecture.md, "The
/// agent sidecar"): it owns the loop and the provider's requests; every
/// action it proposes is run here, one at a time, under the lease, the
/// policy and the approvals.
/// Returns: the sidecar when the model finished, kept so a reply continues
/// the conversation.
async fn drive(
    shared: &Arc<Shared>,
    id: u64,
    desk: &Desk,
    source: &Source,
    start: Start,
) -> Result<Agent, Stop> {
    let mut seen = observe(desk, start.coords, shared.scope()).await?;
    let system = system_prompt(&seen);
    let mut agent = match (start.conversation, start.reply) {
        (Some(mut a), Some(text)) => {
            a.send(&ToSidecar::FollowUp {
                text,
                shot: (&seen.shot).into(),
            })?;
            a
        }
        (_, reply) => {
            // A reply to a task whose sidecar is gone continues the kept
            // conversation in a new one.
            let resume = reply.is_some().then_some(start.resume).flatten();
            let instruction = reply.unwrap_or(start.instruction);
            let mut a = Agent::start(&shared.agent).await?;
            let url = shared.url.clone().unwrap_or_else(|| source.url.clone());
            a.send(&ToSidecar::Start {
                task: id,
                protocol: source.protocol,
                url,
                model: start.model.clone(),
                coordinates: start.coords,
                system: system.clone(),
                instruction,
                shot: (&seen.shot).into(),
                tools: chat_tools(source),
                resume,
                compact: false,
                thinking: start.thinking,
            })?;
            a
        }
    };
    let mut turn = TurnState::default();
    let mut tick = tokio::time::interval(WATCH);
    loop {
        let message = tokio::select! {
            m = agent.recv() => m?,
            _ = tick.tick() => {
                if shared.phase(id).0.finished() {
                    // Cancelled: the request in flight is dropped, nothing
                    // more acts; the sidecar ends when dropped.
                    let _ = agent.send(&ToSidecar::Abort);
                    return Err(Stop::Cancelled);
                }
                continue;
            }
        };
        match message {
            // Each request goes out only while the task runs: a paused task
            // holds its next request here, and the turn it brings acts only
            // under this lease.
            FromSidecar::KeyRequest => {
                let lease = shared.running(id).await?;
                turn = TurnState {
                    asked: lease,
                    ..TurnState::default()
                };
                // Read for each request, never kept; empty for a local server.
                let key = shared
                    .key(source)
                    .ok_or_else(|| "the provider's API key is missing".to_string())?;
                agent.send(&ToSidecar::Key {
                    key: (!key.is_empty()).then(|| key.to_string()),
                })?;
            }
            FromSidecar::Turn {
                text,
                progress,
                flagged,
                usage,
            } => {
                shared.edit(id, |t| {
                    if !text.is_empty() {
                        t.text = text.clone();
                    }
                    t.tokens.add(usage);
                    t.context = usage.input;
                    t.progress.extend(progress.iter().cloned());
                    let extra = t.progress.len().saturating_sub(MAX_PROGRESS);
                    t.progress.drain(..extra);
                });
                turn.flagged = flagged;
            }
            FromSidecar::Act {
                id: call,
                name,
                input,
                last,
            } => {
                let proposed = if name == "openai" {
                    super::openai::parse_action(&input)
                } else {
                    super::claude::parse_member(&name, &input)
                };
                let outcome = act_one(shared, id, &mut turn, desk, proposed, &mut seen).await?;
                // The turn's answer goes out only while the task runs: after
                // a pause it waits for the resume, with a fresh screenshot
                // where the provider answers with one.
                let mut shot = None;
                if last {
                    shared.running(id).await?;
                    if source.protocol != Protocol::Anthropic {
                        seen = observe(desk, seen.coords, shared.scope()).await?;
                        shot = Some((&seen.shot).into());
                    }
                }
                agent.send(&ToSidecar::Result {
                    id: call,
                    outcome: wire(&outcome),
                    shot,
                    acknowledged: turn.acknowledged && !turn.flagged.is_empty(),
                })?;
            }
            FromSidecar::State { state } => shared.keep(id, state, &system),
            FromSidecar::Done { .. } => return Ok(agent),
            FromSidecar::Failed { error } => return Err(Stop::Failed(error)),
            FromSidecar::Aborted => return Err(Stop::Cancelled),
            FromSidecar::Hello { .. } => {}
        }
    }
}

/// An outcome as the sidecar takes it.
fn wire(o: &Outcome) -> WireOutcome {
    match o {
        Outcome::Ok => WireOutcome::Ok,
        Outcome::Image(s) => WireOutcome::Image { shot: s.into() },
        Outcome::Text(text) => WireOutcome::Text { text: text.clone() },
        Outcome::Failed(error) => WireOutcome::Failed {
            error: error.clone(),
        },
        Outcome::NotExecuted => WireOutcome::NotExecuted,
        Outcome::Paused => WireOutcome::Paused,
    }
}

/// One model turn as the engine follows it, action by action.
#[derive(Default)]
struct TurnState {
    /// The lease the model was asked under (its request's `key?`).
    asked: u64,
    /// The lease the turn's actions run under, fixed at its first action:
    /// `u64::MAX` when the task was paused (and perhaps resumed) since the
    /// model was asked, so nothing of the turn runs.
    lease: Option<u64>,
    /// An action failed: the rest of the turn is not executed.
    failed: bool,
    /// The provider's safety notes for the turn, and whether the host user
    /// approved them.
    flagged: Vec<String>,
    acknowledged: bool,
}

/// Runs one proposed action of a turn: parsed, mapped, checked, decided by
/// the policy (approval when it asks), executed and recorded.
/// Returns: its outcome for the model.
async fn act_one(
    shared: &Arc<Shared>,
    id: u64,
    turn: &mut TurnState,
    desk: &Desk,
    proposed: Result<Action, action::ParseError>,
    seen: &mut Seen,
) -> Result<Outcome, Stop> {
    let lease = match turn.lease {
        Some(l) => l,
        None => {
            let l = match shared.phase(id) {
                (State::Running, lease) if lease == turn.asked => lease,
                (s, _) if s.finished() => return Err(Stop::Cancelled),
                // Paused while the model thought, even if resumed since.
                _ => u64::MAX,
            };
            turn.lease = Some(l);
            l
        }
    };
    let approval = shared.settings.lock().expect("settings").approval;
    // Positions on a grid become screenshot pixels before anything checks,
    // shows or records them.
    let proposed = proposed.map(|a| seen.coords.to_pixels(a, seen.size()));
    let action_json = proposed.as_ref().map_or(Value::Null, |a| json!(a));
    if turn.failed {
        shared.record(id, action_json, "not-executed", None);
        return Ok(Outcome::NotExecuted);
    }
    if !holds(shared, id, lease, desk) {
        shared.record(id, action_json, "paused", None);
        return Ok(Outcome::Paused);
    }
    let checked = proposed.map_err(|e| e.to_string()).and_then(|a| {
        let (w, h) = seen.size();
        action::validate(&a, w as i32, h as i32).map_err(|e| e.to_string())?;
        Ok(a)
    });
    let action = match checked {
        Ok(a) => a,
        Err(why) => {
            turn.failed = true;
            shared.record(id, action_json, "failed", Some(why.clone()));
            return Ok(Outcome::Failed(format!("Rejected by the host: {why}")));
        }
    };
    if uses_coordinates(&action) && stale(desk, seen).await {
        turn.failed = true;
        shared.record(id, action_json, "failed", Some("stale".into()));
        return Ok(Outcome::Failed(
            "The screen changed size since the last screenshot; take a new one.".into(),
        ));
    }
    // Repeated refusals leave the task asking before every action.
    let refusals = shared.edit(id, |t| t.refusals).unwrap_or(0);
    let mode = if refusals >= policy::REFUSALS_BEFORE_EVERY_ACTION {
        Approval::EveryAction
    } else {
        approval
    };
    // UI Automation can wait on a slow program: off the async thread.
    let target = tokio::task::block_in_place(|| target_of(desk, &action, seen));
    let scope = shared.scope();
    let unacknowledged = !turn.flagged.is_empty() && !turn.acknowledged;
    let class = match policy::decide(mode, unacknowledged, &action, &target, scope.as_deref()) {
        Verdict::Run => None,
        Verdict::Ask(class) => Some(class),
        Verdict::Refuse(class) => {
            turn.failed = true;
            shared.edit(id, |t| t.refusals += 1);
            shared.record(
                id,
                action_json,
                "refused",
                Some(json!(class).as_str().unwrap_or_default().into()),
            );
            return Ok(Outcome::Failed(format!(
                "Refused by the host: {}",
                refusal(class)
            )));
        }
        Verdict::Pause => {
            let _ = shared.apply(id, Event::Pause, |t| t.paused = Some(Pause::Locked));
            shared.record(id, action_json, "paused", None);
            return Ok(Outcome::Paused);
        }
    };
    if let Some(class) = class {
        match approve(shared, id, lease, &action_json, &turn.flagged, class).await? {
            Some(true) => turn.acknowledged = true,
            Some(false) => {
                turn.failed = true;
                shared.edit(id, |t| t.refusals += 1);
                shared.record(id, action_json, "declined", None);
                return Ok(Outcome::Failed(
                    "The host user declined this action.".into(),
                ));
            }
            None => {
                shared.record(id, action_json, "paused", None);
                return Ok(Outcome::Paused);
            }
        }
    }
    let outcome = execute(shared, id, lease, desk, &action, seen).await;
    let (name, detail) = match &outcome {
        Outcome::Failed(why) => ("failed", Some(why.clone())),
        Outcome::Paused => ("paused", None),
        _ => ("ok", None),
    };
    turn.failed = matches!(outcome, Outcome::Failed(_));
    shared.record(id, action_json, name, detail);
    Ok(outcome)
}

/// Whether the task still holds the input lease; local input pauses it.
fn holds(shared: &Shared, id: u64, lease: u64, desk: &Desk) -> bool {
    if shared.phase(id) != (State::Running, lease) {
        return false;
    }
    if shared.local_input(id, desk) {
        shared.yield_to_user(id);
        return false;
    }
    true
}

/// Whether an action points at the screen.
fn uses_coordinates(a: &Action) -> bool {
    matches!(
        a,
        Action::Zoom { .. }
            | Action::Move { .. }
            | Action::Click { at: Some(_), .. }
            | Action::Drag { .. }
            | Action::Scroll { at: Some(_), .. }
    )
}

/// Whether the screen's size changed since the model's screenshot.
async fn stale(desk: &Desk, seen: &Seen) -> bool {
    let desk = desk.clone();
    let now = tokio::task::spawn_blocking(move || desk.lock().expect("desktop").size()).await;
    !matches!(now, Ok(Ok(size)) if size == (seen.frame.width, seen.frame.height))
}

/// What an action would reach: the window under its point (its pointer
/// position for a click in place), or the focused one for keys.
fn target_of(desk: &Desk, action: &Action, seen: &Seen) -> policy::Target {
    let (w, h) = seen.size();
    let frac = |p: &action::Point| ((p.x as f32 + 0.5) / w as f32, (p.y as f32 + 0.5) / h as f32);
    let mut desk = desk.lock().expect("desktop");
    let at = match action {
        Action::Click { at: Some(p), .. }
        | Action::Scroll { at: Some(p), .. }
        | Action::Move { to: p, .. } => Some(frac(p)),
        Action::Drag { path, .. } => path.first().map(frac),
        Action::Click { at: None, .. }
        | Action::Scroll { at: None, .. }
        | Action::MouseDown { .. }
        | Action::MouseUp { .. } => desk.cursor(),
        _ => None,
    };
    desk.target(at)
}

/// What the model is told when the policy refuses an action.
fn refusal(class: Class) -> &'static str {
    match class {
        Class::SecureSurface => {
            "a credential prompt is on screen; only the person at the computer may answer it"
        }
        Class::OutsideScope => {
            "it reaches a program outside the apps the host user granted; stay in those apps' windows"
        }
        _ => "this action is not allowed here",
    }
}

/// Asks the host user to approve an action.
/// Returns: the decision, or None when the task was paused meanwhile.
async fn approve(
    shared: &Arc<Shared>,
    id: u64,
    lease: u64,
    action: &Value,
    flagged: &[String],
    class: Class,
) -> Result<Option<bool>, Stop> {
    let asked = shared.apply(id, Event::Ask, |t| {
        t.approval = Some(Pending {
            action: action.clone(),
            flagged: flagged.to_vec(),
            class: Some(class),
        });
        t.decision = None;
    });
    if asked.is_err() {
        return match shared.phase(id).0 {
            s if s.finished() => Err(Stop::Cancelled),
            _ => Ok(None),
        };
    }
    loop {
        let woken = shared.changed.notified();
        tokio::pin!(woken);
        woken.as_mut().enable();
        let (state, decision) = {
            let tasks = shared.tasks.lock().expect("tasks");
            let t = tasks.iter().find(|t| t.id == id).ok_or(Stop::Cancelled)?;
            if t.lease != lease {
                (State::Paused, None)
            } else {
                (t.state, t.decision)
            }
        };
        match (state, decision) {
            (State::Running, Some(d)) => return Ok(Some(d)),
            (State::Approval, _) => woken.await,
            (s, _) if s.finished() => return Err(Stop::Cancelled),
            _ => return Ok(None),
        }
    }
}

/// Executes one checked action.
async fn execute(
    shared: &Arc<Shared>,
    id: u64,
    lease: u64,
    desk: &Desk,
    action: &Action,
    seen: &mut Seen,
) -> Outcome {
    match action {
        Action::Screenshot => match observe(desk, seen.coords, shared.scope()).await {
            Ok(s) => {
                *seen = s;
                Outcome::Image(seen.shot.clone())
            }
            Err(why) => Outcome::Failed(why),
        },
        Action::Zoom { region } => match capture(desk, shared.scope()).await {
            Ok(frame) => {
                let r = desktop::frame_region(*region, seen.size(), (frame.width, frame.height));
                Outcome::Image(tokio::task::block_in_place(|| {
                    desktop::shot(&frame, r, desktop::SHOT_MAX, 1)
                }))
            }
            Err(why) => Outcome::Failed(why),
        },
        Action::CursorPosition => match desk.lock().expect("desktop").cursor() {
            Some((x, y)) => {
                let (w, h) = seen.size();
                let px = (
                    ((x * w as f32) as u32).min(w - 1),
                    ((y * h as f32) as u32).min(h - 1),
                );
                let (x, y) = seen.coords.in_model_units(px.0, px.1, (w, h));
                Outcome::Text(format!("X={x}, Y={y}"))
            }
            None => Outcome::Failed("the cursor position is not known".into()),
        },
        input => {
            let cursor = desk.lock().expect("desktop").cursor();
            let steps = match desktop::plan(input, seen.size(), cursor) {
                Ok(s) => s,
                Err(why) => return Outcome::Failed(why),
            };
            for step in steps {
                match step {
                    Step::Inject(e) => {
                        if !holds(shared, id, lease, desk) {
                            desk.lock().expect("desktop").release();
                            return Outcome::Paused;
                        }
                        let mut d = desk.lock().expect("desktop");
                        if !d.inject(&e) {
                            d.release();
                            return Outcome::Failed(
                                "That was aimed at Kyokai's own window; nothing was sent to it."
                                    .into(),
                            );
                        }
                    }
                    Step::Sleep(ms) => {
                        tokio::select! {
                            _ = tokio::time::sleep(Duration::from_millis(ms.into())) => {}
                            _ = shared.interrupted(id, lease) => {
                                desk.lock().expect("desktop").release();
                                return Outcome::Paused;
                            }
                        }
                    }
                    Step::Release => desk.lock().expect("desktop").release(),
                }
            }
            Outcome::Ok
        }
    }
}

fn now() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |d| d.as_secs())
}

fn read_json<T: for<'de> Deserialize<'de>>(path: &Path) -> Option<T> {
    serde_json::from_slice(&std::fs::read(path).ok()?).ok()
}

fn write_json<T: Serialize>(path: &Path, value: &T) -> Result<(), String> {
    let data = serde_json::to_vec_pretty(value).map_err(|e| e.to_string())?;
    crate::store::write_atomic(path, &data)
}

/// A task as the API shows it: its record and whether a reply can continue
/// it.
fn record(t: &Task) -> Value {
    let mut v = json!(t);
    v["resumable"] = json!(
        t.state == State::Done && (t.conversation.is_some() || !matches!(t.kept, Kept::None))
    );
    v
}

/// Drops the oldest finished tasks past `MAX_HISTORY`, with their kept
/// conversations.
fn trim_history(dir: &Path, tasks: &mut Vec<Task>) {
    let over = tasks.iter().filter(|t| t.state.finished()).count();
    let mut drop = over.saturating_sub(MAX_HISTORY);
    tasks.retain(|t| {
        let gone = drop > 0 && t.state.finished();
        drop -= gone as usize;
        if gone {
            forget_conversation(dir, t.id);
        }
        !gone
    });
}

/// Where a task's kept conversation is written.
fn conversation_path(dir: &Path, id: u64) -> PathBuf {
    dir.join(CONVERSATIONS).join(format!("{id}.json"))
}

/// Deletes a task's kept conversation, if it was written.
fn forget_conversation(dir: &Path, id: u64) {
    let _ = std::fs::remove_file(conversation_path(dir, id));
}

/// A task as Markdown (`Ai::export`).
fn markdown(t: &Task) -> String {
    use std::fmt::Write;
    let title = t
        .name
        .clone()
        .unwrap_or_else(|| t.instruction.lines().next().unwrap_or("").to_owned());
    let mut out = format!("# {title}\n\n{}:{}\n", t.source, t.model);
    // A task from before 0.7.0 has no runs: its instruction and last answer.
    let runs = if t.runs.is_empty() {
        vec![Run {
            message: t.instruction.clone(),
            text: t.text.clone(),
            first: 0,
            at: t.created,
        }]
    } else {
        t.runs.clone()
    };
    for (i, run) in runs.iter().enumerate() {
        let end = runs.get(i + 1).map_or(t.steps.len(), |r| r.first);
        let _ = write!(out, "\n## You\n\n{}\n", run.message);
        let steps = t.steps.get(run.first.min(end)..end).unwrap_or_default();
        if !steps.is_empty() {
            out.push_str("\n### Steps\n\n");
            for s in steps {
                let _ = write!(out, "- {} `{}`", s.outcome, s.action);
                if let Some(d) = &s.detail {
                    let _ = write!(out, " ({d})");
                }
                out.push('\n');
            }
        }
        if !run.text.is_empty() {
            let _ = write!(out, "\n## AI\n\n{}\n", run.text);
        }
    }
    if let Some(e) = &t.error {
        let _ = write!(out, "\n*Failed: {e}*\n");
    }
    out
}

/// Saves the finished tasks (instructions, texts and actions; never
/// screenshots).
fn save_history(dir: &Path, tasks: &[Task]) -> Result<(), String> {
    let done: Vec<&Task> = tasks.iter().filter(|t| t.state.finished()).collect();
    write_json(&dir.join(HISTORY), &done)
}

#[cfg(test)]
pub(super) mod testing {
    //! Hooks for the runtime tests in `ai::tests`.
    use super::*;

    /// An `Ai` whose tasks drive `open_desktop` and send to `url`.
    pub fn ai(dir: &Path, secrets: Arc<Secrets>, open_desktop: OpenDesktop, url: String) -> Ai {
        // The sidecar from source, through Bun (doc/en-us/development.md).
        let main = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../sidecar/src/main.ts");
        let agent = vec!["bun".into(), main.to_string_lossy().into_owned()];
        Ai::with(dir, secrets, open_desktop, Some(url), agent).expect("AI runtime")
    }

    pub fn next_state(state: State, event: &str) -> Option<State> {
        let event = match event {
            "pause" => Event::Pause,
            "resume" => Event::Resume,
            "cancel" => Event::Cancel,
            "ask" => Event::Ask,
            "decide" => Event::Decide,
            "finish" => Event::Finish,
            "fail" => Event::Fail,
            "reply" => Event::Reply,
            other => panic!("unknown event {other}"),
        };
        next(state, event)
    }
}
