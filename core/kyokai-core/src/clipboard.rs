//! Clipboard sharing in a session (doc/en-us/computer-use.md,
//! "Clipboard"): while a session may share the clipboard, text copied on
//! either side is offered to the other. Text that Kyokai itself put there
//! (the Device ID, a password) is never offered.

use std::hash::{DefaultHasher, Hash, Hasher};
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::Duration;
use tokio::sync::mpsc;

/// Most text one side offers.
pub const MAX_TEXT: usize = 1 << 20;
/// How often the clipboard is read for changes (it announces none).
const POLL: Duration = Duration::from_millis(500);

/// A clipboard: the system's, or a test double.
pub trait Clipboard: Send {
    /// The text on the clipboard, unless there is none or Kyokai put it
    /// there.
    fn text(&mut self) -> Option<String>;
    /// Puts text on the clipboard. Returns: whether it worked.
    fn set_text(&mut self, text: &str) -> bool;
}

/// How a session opens its side's clipboard: the system's, or a test
/// double.
pub type Open = Arc<dyn Fn() -> Option<Box<dyn Clipboard>> + Send + Sync>;

/// Opens this computer's clipboard.
pub fn system() -> Option<Box<dyn Clipboard>> {
    let c = arboard::Clipboard::new().ok()?;
    Some(Box::new(System(c)))
}

struct System(arboard::Clipboard);

impl Clipboard for System {
    fn text(&mut self) -> Option<String> {
        if owned_by_kyokai() {
            return None;
        }
        self.0.get_text().ok()
    }

    fn set_text(&mut self, text: &str) -> bool {
        self.0.set_text(text).is_ok()
    }
}

/// Whether one of Kyokai's processes put the clipboard's contents there
/// (its owner window belongs to the GUI or the engine).
#[cfg(windows)]
fn owned_by_kyokai() -> bool {
    use windows::Win32::System::DataExchange::GetClipboardOwner;
    use windows::Win32::UI::WindowsAndMessaging::GetWindowThreadProcessId;
    // SAFETY: plain queries; the owner window is only read.
    unsafe {
        let Ok(owner) = GetClipboardOwner() else {
            return false;
        };
        let mut pid = 0u32;
        GetWindowThreadProcessId(owner, Some(&mut pid));
        pid != 0 && crate::input::kyokai_process(pid)
    }
}

#[cfg(not(windows))]
fn owned_by_kyokai() -> bool {
    false
}

/// Tells a local copy from the text this side last saw or wrote, so a copy
/// is offered once and a text from the other side never echoes back.
pub struct Watch {
    last: Option<u64>,
}

impl Watch {
    /// Starts from what the clipboard holds now, which is not offered: only
    /// later copies are.
    pub fn new(now: Option<&str>) -> Self {
        Watch {
            last: now.map(fingerprint),
        }
    }

    /// Returns the local text to offer, when it changed and fits.
    pub fn changed(&mut self, now: Option<&str>) -> Option<String> {
        let text = now.filter(|t| !t.is_empty() && t.len() <= MAX_TEXT)?;
        let f = fingerprint(text);
        if self.last == Some(f) {
            return None;
        }
        self.last = Some(f);
        Some(text.to_owned())
    }

    /// Notes a text written from the other side.
    pub fn wrote(&mut self, text: &str) {
        self.last = Some(fingerprint(text));
    }
}

fn fingerprint(text: &str) -> u64 {
    let mut h = DefaultHasher::new();
    text.hash(&mut h);
    h.finish()
}

/// One session's clipboard sharing, until dropped.
pub struct Share {
    /// Local copies to offer to the other side.
    pub offers: mpsc::Receiver<String>,
    /// Texts from the other side, to put on the local clipboard.
    pub writes: std::sync::mpsc::Sender<String>,
    stop: Arc<AtomicBool>,
}

impl Drop for Share {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::Relaxed);
    }
}

/// Starts sharing a clipboard on a thread of its own (the system
/// clipboard's calls block and may wait for other programs).
pub fn share(mut clipboard: Box<dyn Clipboard>) -> Share {
    let (offer_tx, offers) = mpsc::channel(4);
    let (writes, write_rx) = std::sync::mpsc::channel::<String>();
    let stop = Arc::new(AtomicBool::new(false));
    let stopped = stop.clone();
    std::thread::spawn(move || {
        let mut watch = Watch::new(clipboard.text().as_deref());
        while !stopped.load(Ordering::Relaxed) {
            while let Ok(text) = write_rx.try_recv() {
                if clipboard.set_text(&text) {
                    watch.wrote(&text);
                }
            }
            if let Some(text) = watch.changed(clipboard.text().as_deref())
                && offer_tx.try_send(text).is_err()
                && offer_tx.is_closed()
            {
                break;
            }
            std::thread::sleep(POLL);
        }
    });
    Share {
        offers,
        writes,
        stop,
    }
}

#[cfg(test)]
pub(crate) mod fake {
    //! A clipboard in memory for the session tests.
    use super::*;
    use std::sync::Mutex;

    /// The text a fake clipboard holds; tests read and write it.
    pub type Board = Arc<Mutex<Option<String>>>;

    pub struct Fake(pub Board);

    impl Clipboard for Fake {
        fn text(&mut self) -> Option<String> {
            self.0.lock().unwrap().clone()
        }

        fn set_text(&mut self, text: &str) -> bool {
            *self.0.lock().unwrap() = Some(text.to_owned());
            true
        }
    }

    /// An opener that gives every session the same fake clipboard.
    pub fn opener(board: Board) -> Open {
        Arc::new(move || Some(Box::new(Fake(board.clone())) as Box<dyn Clipboard>))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn copies_are_offered_once_and_never_echo() {
        let mut w = Watch::new(Some("before the session"));
        assert_eq!(
            w.changed(Some("before the session")),
            None,
            "what was there is not offered"
        );
        assert_eq!(w.changed(Some("copied")), Some("copied".into()));
        assert_eq!(w.changed(Some("copied")), None, "offered once");
        w.wrote("from the other side");
        assert_eq!(w.changed(Some("from the other side")), None, "no echo");
        assert_eq!(w.changed(None), None);
        assert_eq!(w.changed(Some("")), None);
        let big = "x".repeat(MAX_TEXT + 1);
        assert_eq!(w.changed(Some(&big)), None, "too large to offer");
        assert_eq!(
            w.changed(Some("copied")),
            Some("copied".into()),
            "a copy again after another"
        );
    }
}
