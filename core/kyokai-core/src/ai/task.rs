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
use std::time::{Duration, SystemTime, UNIX_EPOCH};
use tokio::sync::Notify;
use zeroize::Zeroizing;

use super::action::{self, Action, Key};
use super::desktop::{self, Desktop, Frame, Step};
use super::{Client, Conversation, End, Endpoint, Outcome, Provider, Shot};
use crate::store::Secrets;

/// When a task asks the host user before acting.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum Approval {
    /// Only what the provider flags, and system shortcuts (the Windows /
    /// Command key).
    Flagged,
    /// Before every action.
    EveryAction,
}

/// The host's AI settings (`ai.json` in the state directory; the keys live
/// in the secrets store).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Settings {
    pub provider: Provider,
    pub openai_model: String,
    pub claude_model: String,
    pub approval: Approval,
}

impl Default for Settings {
    fn default() -> Self {
        Settings {
            provider: Provider::Claude,
            openai_model: Provider::Openai.default_model().into(),
            claude_model: Provider::Claude.default_model().into(),
            approval: Approval::Flagged,
        }
    }
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
    pub provider: Provider,
    pub model: String,
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
    #[serde(skip)]
    decision: Option<bool>,
    /// The host user's next message for a finished task.
    #[serde(skip)]
    reply: Option<String>,
    /// The conversation, kept after the task finishes so it can continue.
    #[serde(skip)]
    conversation: Option<Conversation>,
}

/// Most progress notes and steps kept per task.
const MAX_PROGRESS: usize = 20;
const MAX_STEPS: usize = 500;
/// Most finished tasks kept in the history.
const MAX_HISTORY: usize = 100;
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
}

/// The host's AI runtime, owned by the core.
pub struct Ai {
    shared: Arc<Shared>,
    client: Arc<Client>,
    rt: tokio::runtime::Runtime,
}

impl Ai {
    /// Loads the settings and the task history.
    /// Side effects: starts a small runtime for the runners.
    pub fn new(dir: &Path, secrets: Arc<Secrets>) -> Result<Ai, String> {
        Ai::with(dir, secrets, Arc::new(open_screen), None)
    }

    fn with(
        dir: &Path,
        secrets: Arc<Secrets>,
        open_desktop: OpenDesktop,
        url: Option<String>,
    ) -> Result<Ai, String> {
        let rt = tokio::runtime::Builder::new_multi_thread()
            .worker_threads(2)
            .thread_name("kyokai-ai")
            .enable_all()
            .build()
            .map_err(|e| format!("AI runtime: {e}"))?;
        let settings = read_json(&dir.join(SETTINGS)).unwrap_or_default();
        let tasks: Vec<Task> = read_json(&dir.join(HISTORY)).unwrap_or_default();
        Ok(Ai {
            shared: Arc::new(Shared {
                tasks: Mutex::new(tasks),
                settings: Mutex::new(settings),
                changed: Notify::new(),
                secrets,
                dir: dir.to_path_buf(),
                open_desktop,
                url,
            }),
            client: Arc::new(Client::new()?),
            rt,
        })
    }

    /// The settings and which providers have a key (never the key).
    pub fn settings(&self) -> Value {
        let s = self.shared.settings.lock().expect("settings").clone();
        let has = |p: Provider| self.shared.secrets.get(p.key_name()).is_some();
        json!({
            "provider": s.provider,
            "openai_model": s.openai_model,
            "claude_model": s.claude_model,
            "approval": s.approval,
            "keys": {"openai": has(Provider::Openai), "claude": has(Provider::Claude)},
        })
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
            if new.openai_model.trim().is_empty() || new.claude_model.trim().is_empty() {
                return Err("a model name is needed".into());
            }
            write_json(&self.shared.dir.join(SETTINGS), &new)?;
            *s = new;
        }
        Ok(self.settings())
    }

    /// Stores a provider's API key, or removes it (`None`).
    /// Side effects: writes the secrets store; never logs the key.
    pub fn set_key(&self, provider: Provider, key: Option<&str>) -> Result<(), String> {
        match key.map(str::trim) {
            Some("") | None => self.shared.secrets.remove(provider.key_name()),
            Some(k) => self.shared.secrets.set(provider.key_name(), k),
        }
    }

    /// Starts a task with the current provider and model.
    /// Inputs: `key` — the caller's idempotency key; a second start with the
    /// same key returns the first task.
    /// Returns: the task's id.
    pub fn start(&self, key: &str, instruction: &str) -> Result<u64, String> {
        let instruction = instruction.trim();
        if instruction.is_empty() || instruction.len() > MAX_INSTRUCTION {
            return Err("describe the task in a few sentences".into());
        }
        let settings = self.shared.settings.lock().expect("settings").clone();
        let provider = settings.provider;
        if self.shared.secrets.get(provider.key_name()).is_none() {
            return Err("add an API key for the provider first".into());
        }
        let id = {
            let mut tasks = self.shared.tasks.lock().expect("tasks");
            if let Some(t) = tasks.iter().find(|t| !key.is_empty() && t.key == key) {
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
                provider,
                model: match provider {
                    Provider::Openai => settings.openai_model,
                    Provider::Claude => settings.claude_model,
                },
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
                decision: None,
                reply: None,
                conversation: None,
            });
            id
        };
        self.spawn(id);
        Ok(id)
    }

    /// Continues a finished task with the host user's next message.
    pub fn reply(&self, id: u64, text: &str) -> Result<(), String> {
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
            if t.conversation.is_none() {
                return Err("this task can no longer continue".into());
            }
            t.state = next(t.state, Event::Reply).ok_or("the task is not finished")?;
            t.reply = Some(text.into());
            t.error = None;
            t.updated = now();
        }
        self.shared.changed.notify_waiters();
        self.spawn(id);
        Ok(())
    }

    fn spawn(&self, id: u64) {
        let shared = self.shared.clone();
        let client = self.client.clone();
        self.rt.spawn(async move {
            let outcome = run(&shared, &client, id).await;
            shared.end(id, outcome);
        });
    }

    /// Pauses a task (the host user's pause).
    pub fn pause(&self, id: u64) -> Result<(), String> {
        self.shared.apply(id, Event::Pause, |t| {
            t.paused = Some(Pause::HostUser);
        })
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

    /// Every task, newest first.
    pub fn tasks(&self) -> Value {
        let tasks = self.shared.tasks.lock().expect("tasks");
        tasks.iter().rev().map(|t| json!(t)).collect()
    }

    /// The task in progress, for the status poll: its id, state and whether
    /// it waits for approval.
    pub fn active(&self) -> Value {
        let tasks = self.shared.tasks.lock().expect("tasks");
        match tasks.iter().find(|t| !t.state.finished()) {
            Some(t) => json!({"id": t.id, "state": t.state, "paused": t.paused}),
            None => Value::Null,
        }
    }

    /// Clears the finished tasks from the history.
    /// Side effects: rewrites the history file.
    pub fn clear(&self) -> Result<(), String> {
        let mut tasks = self.shared.tasks.lock().expect("tasks");
        tasks.retain(|t| !t.state.finished());
        save_history(&self.shared.dir, &tasks)
    }
}

impl Drop for Ai {
    fn drop(&mut self) {
        // Nothing acts after the core stops: the runners are cancelled with
        // the runtime, and each runner's desktop releases what it holds.
        self.stop_all();
    }
}

/// Longest task instruction or reply.
const MAX_INSTRUCTION: usize = 8 * 1024;
const SETTINGS: &str = "ai.json";
const HISTORY: &str = "ai-tasks.json";

impl Shared {
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

    /// The task's state and lease.
    fn phase(&self, id: u64) -> (State, u64) {
        let tasks = self.tasks.lock().expect("tasks");
        tasks
            .iter()
            .find(|t| t.id == id)
            .map_or((State::Cancelled, 0), |t| (t.state, t.lease))
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
    fn end(&self, id: u64, outcome: Result<Conversation, Stop>) {
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
            }
            let over = tasks.iter().filter(|t| t.state.finished()).count();
            if over > MAX_HISTORY {
                let mut drop = over - MAX_HISTORY;
                tasks.retain(|t| {
                    let gone = drop > 0 && t.state.finished();
                    drop -= gone as usize;
                    !gone
                });
            }
            let _ = save_history(&self.dir, &tasks);
        }
        self.changed.notify_waiters();
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

/// The last screenshot the model saw and the frame it came from.
struct Seen {
    frame: Frame,
    shot: Shot,
}

impl Seen {
    fn size(&self) -> (u32, u32) {
        (self.shot.width, self.shot.height)
    }
}

/// The desktop a runner drives, shared with blocking capture calls.
type Desk = Arc<Mutex<Box<dyn Desktop>>>;

/// Captures the screen on a blocking thread.
async fn capture(desk: &Desk) -> Result<Frame, String> {
    let desk = desk.clone();
    tokio::task::spawn_blocking(move || desk.lock().expect("desktop").capture())
        .await
        .map_err(|_| "capture failed".to_string())?
}

/// Takes a screenshot for the model.
async fn observe(desk: &Desk) -> Result<Seen, String> {
    let frame = capture(desk).await?;
    let shot = tokio::task::block_in_place(|| {
        desktop::shot(&frame, [0, 0, frame.width, frame.height], desktop::SHOT_MAX)
    });
    Ok(Seen { frame, shot })
}

/// The instructions every task starts with.
fn system_prompt(width: u32, height: u32) -> String {
    let os = if cfg!(windows) { "Windows" } else { "macOS" };
    format!(
        "You are operating a {os} computer for its user through the computer tools, on the \
         screen the user sees. Screenshots are {width}x{height} pixels; coordinates refer to \
         the latest full screenshot. The user can pause or stop you at any time and may decline \
         an action. Before anything irreversible or sensitive - deleting data, sending messages, \
         purchases, changing security settings, entering passwords - stop and ask the user in \
         text instead of acting. After acting, take a screenshot to check the result. When the \
         task is done, say briefly what you did."
    )
}

/// Opens the real desktop.
fn open_screen() -> Result<Box<dyn Desktop>, String> {
    #[cfg(windows)]
    {
        Ok(Box::new(desktop::Screen::open()?))
    }
    #[cfg(not(windows))]
    {
        Err("AI tasks are not available on this system yet".into())
    }
}

/// Runs a task until it finishes, fails or is cancelled.
/// Returns: the conversation when the model finished (so the task can
/// continue later).
async fn run(shared: &Arc<Shared>, client: &Client, id: u64) -> Result<Conversation, Stop> {
    let (provider, model, instruction, reply, conversation) = shared
        .edit(id, |t| {
            (
                t.provider,
                t.model.clone(),
                t.instruction.clone(),
                t.reply.take(),
                t.conversation.take(),
            )
        })
        .ok_or(Stop::Cancelled)?;
    let key: Zeroizing<String> = shared
        .secrets
        .get(provider.key_name())
        .ok_or_else(|| "the provider's API key is missing".to_string())?;
    let endpoint = Endpoint {
        provider,
        url: shared.url.clone().unwrap_or_else(|| provider.url().into()),
    };
    let desk: Desk = Arc::new(Mutex::new((shared.open_desktop)()?));
    let result = drive(
        shared,
        client,
        id,
        &desk,
        &endpoint,
        &key,
        Start {
            model,
            instruction,
            reply,
            conversation,
        },
    )
    .await;
    desk.lock().expect("desktop").release();
    result
}

/// How a runner begins: a new conversation, or a reply to a finished one.
struct Start {
    model: String,
    instruction: String,
    reply: Option<String>,
    conversation: Option<Conversation>,
}

async fn drive(
    shared: &Arc<Shared>,
    client: &Client,
    id: u64,
    desk: &Desk,
    endpoint: &Endpoint,
    key: &str,
    start: Start,
) -> Result<Conversation, Stop> {
    // Input before the task began is not a reason to pause it.
    desk.lock().expect("desktop").touched();
    let mut seen = observe(desk).await?;
    let mut conv = match (start.conversation, start.reply) {
        (Some(mut c), Some(text)) => {
            c.follow_up(&text, &seen.shot);
            c
        }
        _ => Conversation::new(
            endpoint.provider,
            &start.model,
            &system_prompt(seen.shot.width, seen.shot.height),
            &start.instruction,
            &seen.shot,
        ),
    };
    loop {
        shared.running(id).await?;
        let body = conv.body();
        let response = ask_model(shared, client, id, desk, endpoint, key, &body).await?;
        let turn = conv
            .accept(&response)
            .map_err(|e| format!("the provider's answer could not be read: {e}"))?;
        shared.edit(id, |t| {
            if !turn.text.is_empty() {
                t.text = turn.text.clone();
            }
            t.progress.extend(turn.progress.iter().cloned());
            let extra = t.progress.len().saturating_sub(MAX_PROGRESS);
            t.progress.drain(..extra);
        });
        match &turn.end {
            End::Actions => {}
            End::Finished => return Ok(conv),
            End::Refused(why) => {
                return Err(Stop::Failed(if why.is_empty() {
                    "the model declined the task".into()
                } else {
                    format!("the model declined the task: {why}")
                }));
            }
            End::Incomplete(why) => {
                return Err(Stop::Failed(format!(
                    "the model's answer was cut off ({why})"
                )));
            }
        }
        let (outcomes, acknowledged) = act(shared, id, desk, &turn, &mut seen).await?;
        // The answer goes out only while the task runs: after a pause it
        // waits for the resume, with a fresh screenshot.
        shared.running(id).await?;
        desk.lock().expect("desktop").touched();
        let last = if conv.wants_final_shot() {
            seen = observe(desk).await?;
            Some(seen.shot.clone())
        } else {
            None
        };
        conv.answer(&outcomes, last.as_ref(), acknowledged);
    }
}

/// Sends one request, pausing the task when the host user touches the
/// keyboard or mouse meanwhile.
async fn ask_model(
    shared: &Arc<Shared>,
    client: &Client,
    id: u64,
    desk: &Desk,
    endpoint: &Endpoint,
    key: &str,
    body: &Value,
) -> Result<Value, Stop> {
    let send = client.send(endpoint, key, body);
    tokio::pin!(send);
    let mut tick = tokio::time::interval(WATCH);
    loop {
        tokio::select! {
            r = &mut send => return r.map_err(Stop::Failed),
            _ = tick.tick() => {
                if shared.phase(id).0.finished() {
                    return Err(Stop::Cancelled);
                }
                if desk.lock().expect("desktop").touched() {
                    let _ = shared.apply(id, Event::Pause, |t| t.paused = Some(Pause::LocalInput));
                }
            }
        }
    }
}

/// Runs one turn's actions in order.
/// Returns: one outcome per action, and whether the host user approved the
/// provider's safety checks.
async fn act(
    shared: &Arc<Shared>,
    id: u64,
    desk: &Desk,
    turn: &super::Turn,
    seen: &mut Seen,
) -> Result<(Vec<Outcome>, bool), Stop> {
    let approval = shared.settings.lock().expect("settings").approval;
    let mut outcomes = Vec::with_capacity(turn.actions.len());
    let mut failed = false;
    let mut acknowledged = turn.flagged.is_empty();
    let lease = match shared.phase(id) {
        (State::Running, lease) => lease,
        (s, _) if s.finished() => return Err(Stop::Cancelled),
        _ => u64::MAX, // paused while the model thought: nothing runs
    };
    for proposed in &turn.actions {
        let action_json = proposed.as_ref().map_or(Value::Null, |a| json!(a));
        if failed {
            outcomes.push(Outcome::NotExecuted);
            shared.record(id, action_json, "not-executed", None);
            continue;
        }
        if !holds(shared, id, lease, desk) {
            outcomes.push(Outcome::Paused);
            shared.record(id, action_json, "paused", None);
            continue;
        }
        let checked = proposed.as_ref().map_err(|e| e.to_string()).and_then(|a| {
            let (w, h) = seen.size();
            action::validate(a, w as i32, h as i32).map_err(|e| e.to_string())?;
            Ok(a)
        });
        let action = match checked {
            Ok(a) => a,
            Err(why) => {
                failed = true;
                outcomes.push(Outcome::Failed(format!("Rejected by the host: {why}")));
                shared.record(id, action_json, "failed", Some(why));
                continue;
            }
        };
        if uses_coordinates(action) && stale(desk, seen).await {
            failed = true;
            let why = "The screen changed size since the last screenshot; take a new one.";
            outcomes.push(Outcome::Failed(why.into()));
            shared.record(id, action_json, "failed", Some("stale".into()));
            continue;
        }
        let ask = !acknowledged || approval == Approval::EveryAction || sensitive(action);
        if ask {
            match approve(shared, id, lease, &action_json, &turn.flagged).await? {
                Some(true) => acknowledged = true,
                Some(false) => {
                    failed = true;
                    outcomes.push(Outcome::Failed(
                        "The host user declined this action.".into(),
                    ));
                    shared.record(id, action_json, "declined", None);
                    continue;
                }
                None => {
                    outcomes.push(Outcome::Paused);
                    shared.record(id, action_json, "paused", None);
                    continue;
                }
            }
        }
        let outcome = execute(shared, id, lease, desk, action, seen).await;
        let (name, detail) = match &outcome {
            Outcome::Failed(why) => ("failed", Some(why.clone())),
            Outcome::Paused => ("paused", None),
            _ => ("ok", None),
        };
        failed = matches!(outcome, Outcome::Failed(_));
        shared.record(id, action_json, name, detail);
        outcomes.push(outcome);
    }
    Ok((outcomes, acknowledged && !turn.flagged.is_empty()))
}

/// Whether the task still holds the input lease; local input pauses it.
fn holds(shared: &Shared, id: u64, lease: u64, desk: &Desk) -> bool {
    if desk.lock().expect("desktop").touched() {
        let _ = shared.apply(id, Event::Pause, |t| t.paused = Some(Pause::LocalInput));
        return false;
    }
    shared.phase(id) == (State::Running, lease)
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

/// Actions the Flagged policy always asks about: shortcuts with the
/// Windows / Command key (launchers, system menus).
fn sensitive(a: &Action) -> bool {
    match a {
        Action::KeyCombo { keys, .. } | Action::HoldKey { keys, .. } => keys.contains(&Key::Meta),
        _ => false,
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
) -> Result<Option<bool>, Stop> {
    let asked = shared.apply(id, Event::Ask, |t| {
        t.approval = Some(Pending {
            action: action.clone(),
            flagged: flagged.to_vec(),
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
        Action::Screenshot => match observe(desk).await {
            Ok(s) => {
                *seen = s;
                Outcome::Image(seen.shot.clone())
            }
            Err(why) => Outcome::Failed(why),
        },
        Action::Zoom { region } => match capture(desk).await {
            Ok(frame) => {
                let r = desktop::frame_region(*region, seen.size(), (frame.width, frame.height));
                Outcome::Image(tokio::task::block_in_place(|| {
                    desktop::shot(&frame, r, desktop::SHOT_MAX)
                }))
            }
            Err(why) => Outcome::Failed(why),
        },
        Action::CursorPosition => match desk.lock().expect("desktop").cursor() {
            Some((x, y)) => {
                let (w, h) = seen.size();
                Outcome::Text(format!(
                    "X={}, Y={}",
                    ((x * w as f32) as u32).min(w - 1),
                    ((y * h as f32) as u32).min(h - 1)
                ))
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
                        let mut d = desk.lock().expect("desktop");
                        if d.touched() {
                            d.release();
                            drop(d);
                            let _ = shared
                                .apply(id, Event::Pause, |t| t.paused = Some(Pause::LocalInput));
                            return Outcome::Paused;
                        }
                        if !d.inject(&e) {
                            d.release();
                            return Outcome::Failed(
                                "Kyokai's own window has the focus; nothing was sent to it.".into(),
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
        Ai::with(dir, secrets, open_desktop, Some(url)).expect("AI runtime")
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
