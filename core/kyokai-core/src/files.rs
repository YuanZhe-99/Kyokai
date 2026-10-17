//! Files on the shared clipboard (doc/en-us/computer-use.md, "Files"): the
//! copying side lists what was copied and offers paths and sizes; the other
//! side checks them, accepts, and receives the contents, streamed in bounded
//! pieces, into a folder of its own for the session. In the helper modes the
//! files are read and written as the console's signed-in user.

use serde::{Deserialize, Serialize};
use std::collections::HashSet;
use std::io::{Read, Write};
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, OnceLock};
use tokio::sync::mpsc;

/// Most entries (files and folders) one offer holds.
pub const MAX_ENTRIES: usize = 1000;
/// Most bytes of all files of one offer.
pub const MAX_TOTAL: u64 = 1 << 30;
/// Most characters of one name in a path.
const MAX_NAME: usize = 255;
/// Most bytes of one path (so an offer always fits one message).
const MAX_PATH: usize = 1024;
/// Most names in one path.
const MAX_DEPTH: usize = 64;
/// Starts a plaintext that carries a piece of an offer's contents.
pub const TAG: u8 = 0x03;
/// Bytes before a piece's contents: the tag and the offer's id (u64).
pub const HEADER: usize = 9;
/// Pieces read ahead of the connection, or queued for the disk.
const AHEAD: usize = 8;

/// One file or folder of an offer: its path below the copied items, with
/// `/` between names.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Entry {
    pub path: String,
    #[serde(default)]
    pub size: u64,
    #[serde(default)]
    pub dir: bool,
}

/// Checks an offer from the wire: within the limits, every path a safe
/// relative path, none twice (ignoring case) and none inside a file.
pub fn check(entries: &[Entry]) -> bool {
    if entries.is_empty() || entries.len() > MAX_ENTRIES {
        return false;
    }
    let files: HashSet<String> = entries
        .iter()
        .filter(|e| !e.dir)
        .map(|e| e.path.to_lowercase())
        .collect();
    let mut seen = HashSet::new();
    let mut total = 0u64;
    for e in entries {
        if !path_ok(&e.path) || (e.dir && e.size != 0) {
            return false;
        }
        total = match total.checked_add(e.size) {
            Some(t) if t <= MAX_TOTAL => t,
            _ => return false,
        };
        let key = e.path.to_lowercase();
        if key
            .match_indices('/')
            .any(|(i, _)| files.contains(&key[..i]))
            || !seen.insert(key)
        {
            return false;
        }
    }
    true
}

/// Whether a path is relative, short enough and made of safe names.
fn path_ok(path: &str) -> bool {
    path.len() <= MAX_PATH && path.split('/').count() <= MAX_DEPTH && path.split('/').all(name_ok)
}

/// Whether one name can be created as itself on Windows: not empty, `.` or
/// `..`, without reserved or control characters, not ending in a dot or a
/// space, and not a device.
fn name_ok(name: &str) -> bool {
    !name.is_empty()
        && name.chars().count() <= MAX_NAME
        && !name.ends_with(['.', ' '])
        && !name
            .chars()
            .any(|c| c < ' ' || matches!(c, '<' | '>' | ':' | '"' | '/' | '\\' | '|' | '?' | '*'))
        && !device(name)
}

/// Whether a name opens a device on Windows (`NUL`, `COM1.txt`, …).
fn device(name: &str) -> bool {
    let stem = name
        .split('.')
        .next()
        .unwrap_or_default()
        .trim_end()
        .to_ascii_uppercase();
    match stem.as_str() {
        "CON" | "PRN" | "AUX" | "NUL" | "CONIN$" | "CONOUT$" => true,
        s if s.starts_with("COM") || s.starts_with("LPT") => {
            let mut rest = s[3..].chars();
            matches!(
                (rest.next(), rest.next()),
                (Some('0'..='9' | '¹' | '²' | '³'), None)
            )
        }
        _ => false,
    }
}

/// What a local copy offers: the entries, and the files to read for them.
#[derive(Debug)]
pub struct Listing {
    pub entries: Vec<Entry>,
    /// Each file entry's path and size, in the entries' order.
    sources: Vec<(PathBuf, u64)>,
}

/// Lists copied files and folders, walking folders without following links
/// or junctions.
/// Returns: None when the copy is over the limits, has a name another side
/// would refuse, or nothing readable.
pub fn list(roots: &[PathBuf]) -> Option<Listing> {
    let mut l = Listing {
        entries: Vec::new(),
        sources: Vec::new(),
    };
    let mut total = 0u64;
    for root in roots {
        let name = root.file_name()?.to_str()?.to_owned();
        walk(root, name, &mut l, &mut total)?;
    }
    check(&l.entries).then_some(l)
}

/// Adds one item and what it holds to a listing.
/// Returns: None once the limits are passed or a folder cannot be read.
fn walk(path: &Path, rel: String, l: &mut Listing, total: &mut u64) -> Option<()> {
    // Something gone since it was copied is left out.
    let Ok(meta) = std::fs::symlink_metadata(path) else {
        return Some(());
    };
    let kind = meta.file_type();
    if kind.is_symlink() {
        return Some(());
    }
    if l.entries.len() >= MAX_ENTRIES {
        return None;
    }
    if kind.is_dir() {
        l.entries.push(Entry {
            path: rel.clone(),
            size: 0,
            dir: true,
        });
        let mut names: Vec<_> = std::fs::read_dir(path)
            .ok()?
            .filter_map(|e| e.ok().map(|e| e.file_name()))
            .collect();
        names.sort();
        for n in names {
            let child = format!("{rel}/{}", n.to_str()?);
            walk(&path.join(&n), child, l, total)?;
        }
    } else if kind.is_file() {
        *total += meta.len();
        if *total > MAX_TOTAL {
            return None;
        }
        l.entries.push(Entry {
            path: rel,
            size: meta.len(),
            dir: false,
        });
        l.sources.push((path.to_path_buf(), meta.len()));
    }
    Some(())
}

/// Reads an offer's files as pieces ready to seal, each at most `room`
/// bytes of contents, while `stop` is not set.
/// Returns: false when a file could not be read or changed size.
fn read_all(
    id: u64,
    sources: &[(PathBuf, u64)],
    room: usize,
    tx: &mpsc::Sender<Vec<u8>>,
    stop: &AtomicBool,
) -> bool {
    for (path, size) in sources {
        let Ok(mut f) = std::fs::File::open(path) else {
            return false;
        };
        let mut left = *size;
        while left > 0 {
            if stop.load(Ordering::Relaxed) {
                return true;
            }
            let n = left.min(room as u64) as usize;
            let mut piece = vec![0u8; HEADER + n];
            piece[0] = TAG;
            piece[1..HEADER].copy_from_slice(&id.to_be_bytes());
            if f.read_exact(&mut piece[HEADER..]).is_err() {
                return false;
            }
            // Waits while the connection is behind (backpressure).
            if tx.blocking_send(piece).is_err() {
                return true;
            }
            left -= n as u64;
        }
        // A file that grew since it was listed changed too.
        if f.read(&mut [0u8; 1]).map_or(true, |n| n != 0) {
            return false;
        }
    }
    true
}

/// Writes an offer's entries below `dir` from the pieces' contents, each
/// file new (never over an existing one).
/// Returns: the copied items' paths, for the clipboard.
fn write_all(
    dir: &Path,
    entries: &[Entry],
    rx: &mut mpsc::Receiver<Vec<u8>>,
) -> std::io::Result<Vec<PathBuf>> {
    let local = |p: &str| p.split('/').fold(dir.to_path_buf(), |acc, n| acc.join(n));
    std::fs::create_dir_all(dir)?;
    for e in entries.iter().filter(|e| e.dir) {
        std::fs::create_dir_all(local(&e.path))?;
    }
    let (mut buf, mut at) = (Vec::new(), 0);
    for e in entries.iter().filter(|e| !e.dir) {
        let path = local(&e.path);
        if let Some(parent) = path.parent() {
            std::fs::create_dir_all(parent)?;
        }
        let mut f = std::fs::File::create_new(&path)?;
        let mut left = e.size;
        while left > 0 {
            if at == buf.len() {
                buf = rx.blocking_recv().ok_or(std::io::ErrorKind::Interrupted)?;
                at = 0;
            }
            let n = left.min((buf.len() - at) as u64) as usize;
            f.write_all(&buf[at..at + n])?;
            at += n;
            left -= n as u64;
        }
    }
    if at != buf.len() {
        return Err(std::io::ErrorKind::InvalidData.into());
    }
    let mut roots: Vec<PathBuf> = Vec::new();
    for e in entries {
        let top = local(e.path.split('/').next().unwrap_or_default());
        if !roots.contains(&top) {
            roots.push(top);
        }
    }
    Ok(roots)
}

/// The session's folder for received files: created on first use, deleted
/// once the session and its last writer are done.
struct Inbox {
    tag: String,
    root: OnceLock<Option<PathBuf>>,
}

impl Inbox {
    /// The folder, created as the user the files are for.
    fn root(&self) -> Option<&Path> {
        self.root
            .get_or_init(|| {
                let root = temp_dir()?.join("Kyokai").join(&self.tag);
                as_user(|| std::fs::create_dir_all(&root))?.ok()?;
                Some(root)
            })
            .as_deref()
    }
}

impl Drop for Inbox {
    fn drop(&mut self) {
        if let Some(Some(root)) = self.root.take() {
            // On a thread of its own: as the user, off the session's thread.
            std::thread::spawn(move || as_user(|| std::fs::remove_dir_all(root)));
        }
    }
}

/// What a session does next for its file transfers.
#[derive(Debug)]
pub enum Step {
    /// Offer this local copy.
    Offer(u64, Vec<Entry>),
    /// Send this piece of contents as it is.
    Piece(Vec<u8>),
    /// The offer being sent cannot be finished: tell the other side.
    Cancel(u64),
    /// The offer being received failed: ask the other side to stop.
    Stop(u64),
    /// Everything of an offer arrived: put these on the clipboard.
    Received(Vec<PathBuf>),
}

/// What the transfers' threads report.
enum Report {
    Listed(u64, Option<Listing>),
    ReadFailed(u64),
    Written(u64, std::io::Result<Vec<PathBuf>>),
}

/// An offer being sent: its pieces, until dropped.
struct Sending {
    id: u64,
    pieces: mpsc::Receiver<Vec<u8>>,
    stop: Arc<AtomicBool>,
}

impl Drop for Sending {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::Relaxed);
    }
}

/// The files to read for an offer, with their sizes.
type Sources = Arc<[(PathBuf, u64)]>;

/// One session's file transfers both ways, until dropped.
/// Notes: every file is read and written on a thread of its own, never on
/// the session's; a newer offer replaces an older one in each direction.
pub struct Transfers {
    /// Most bytes of contents per piece.
    room: usize,
    reports_tx: mpsc::UnboundedSender<Report>,
    reports: mpsc::UnboundedReceiver<Report>,
    /// The id of this side's latest copy.
    next: u64,
    /// This side's offer, listed and sent.
    offer: Option<(u64, Sources)>,
    sending: Option<Sending>,
    /// The other side's offer being received, and the queue to its writer.
    receiving: Option<(u64, mpsc::Sender<Vec<u8>>)>,
    inbox: Arc<Inbox>,
    /// Folders made for offers received so far.
    received: u32,
}

impl Transfers {
    /// Starts with nothing offered or received; `room` is the most contents
    /// one piece may carry.
    pub fn new(room: usize) -> Transfers {
        let (reports_tx, reports) = mpsc::unbounded_channel();
        let mut tag = [0u8; 8];
        let _ = getrandom::fill(&mut tag);
        Transfers {
            room,
            reports_tx,
            reports,
            next: 0,
            offer: None,
            sending: None,
            receiving: None,
            inbox: Arc::new(Inbox {
                tag: tag.iter().map(|b| format!("{b:02x}")).collect(),
                root: OnceLock::new(),
            }),
            received: 0,
        }
    }

    /// Takes a local copy of files: lists it on a thread, then offers it
    /// (through `next`), replacing an older offer.
    pub fn copied(&mut self, paths: Vec<PathBuf>) {
        self.next += 1;
        self.offer = None;
        self.sending = None;
        let (id, tx) = (self.next, self.reports_tx.clone());
        std::thread::spawn(move || {
            let l = as_user(|| list(&paths)).flatten();
            let _ = tx.send(Report::Listed(id, l));
        });
    }

    /// The other side accepted offer `id`: starts reading it.
    pub fn get(&mut self, id: u64) {
        let Some((offered, sources)) = &self.offer else {
            return;
        };
        if *offered != id || self.sending.as_ref().is_some_and(|s| s.id == id) {
            return;
        }
        let (tx, pieces) = mpsc::channel(AHEAD);
        let stop = Arc::new(AtomicBool::new(false));
        let (sources, room, stopped, reports) = (
            sources.clone(),
            self.room,
            stop.clone(),
            self.reports_tx.clone(),
        );
        std::thread::spawn(move || {
            let ok = as_user(|| read_all(id, &sources, room, &tx, &stopped)).unwrap_or(false);
            if !ok {
                let _ = reports.send(Report::ReadFailed(id));
            }
        });
        self.sending = Some(Sending { id, pieces, stop });
    }

    /// The other side wants no more of offer `id`.
    pub fn stop(&mut self, id: u64) {
        if self.sending.as_ref().is_some_and(|s| s.id == id) {
            self.sending = None;
        }
    }

    /// The other side offers files: checks them and, when they pass, starts
    /// a writer for them, replacing an offer still being received.
    /// Returns: whether to accept (then the session asks for the contents).
    pub fn offered(&mut self, id: u64, entries: Vec<Entry>) -> bool {
        self.receiving = None;
        if !check(&entries) {
            return false;
        }
        self.received += 1;
        let (tx, mut rx) = mpsc::channel(AHEAD);
        let (inbox, n, reports) = (self.inbox.clone(), self.received, self.reports_tx.clone());
        std::thread::spawn(move || {
            let written = as_user(|| {
                let dir = inbox
                    .root()
                    .ok_or(std::io::ErrorKind::NotFound)?
                    .join(n.to_string());
                let r = write_all(&dir, &entries, &mut rx);
                if r.is_err() {
                    let _ = std::fs::remove_dir_all(&dir);
                }
                r
            })
            .unwrap_or_else(|| Err(std::io::ErrorKind::PermissionDenied.into()));
            let _ = reports.send(Report::Written(id, written));
        });
        self.receiving = Some((id, tx));
        true
    }

    /// Takes a piece of contents from the other side, waiting while its
    /// writer is behind (backpressure); pieces of other offers are dropped.
    pub async fn piece(&mut self, plain: &[u8]) {
        let Some(id) = plain
            .get(1..HEADER)
            .map(|b| u64::from_be_bytes(b.try_into().expect("8 bytes")))
        else {
            return;
        };
        if let Some((receiving, tx)) = &self.receiving
            && *receiving == id
        {
            // A writer that stopped reports why on its own.
            let _ = tx.send(plain[HEADER..].to_vec()).await;
        }
    }

    /// The other side gave up offer `id`.
    pub fn cancelled(&mut self, id: u64) {
        if self.receiving.as_ref().is_some_and(|(r, _)| *r == id) {
            self.receiving = None;
        }
    }

    /// Waits for what the session does next.
    /// Notes: cancel safe; nothing is taken from a queue without being
    /// returned or handled.
    pub async fn next(&mut self) -> Step {
        loop {
            let Transfers {
                reports, sending, ..
            } = self;
            let got = tokio::select! {
                Some(r) = reports.recv() => Ok(r),
                piece = async {
                    match sending.as_mut() {
                        Some(s) => s.pieces.recv().await,
                        None => std::future::pending().await,
                    }
                } => Err(piece),
            };
            let report = match got {
                Ok(r) => r,
                Err(Some(p)) => return Step::Piece(p),
                // Everything was read.
                Err(None) => {
                    self.sending = None;
                    continue;
                }
            };
            match report {
                Report::Listed(id, Some(l)) if id == self.next => {
                    self.offer = Some((id, l.sources.into()));
                    return Step::Offer(id, l.entries);
                }
                // Its pieces may have ended before this report came.
                Report::ReadFailed(id) if self.offer.as_ref().is_some_and(|(o, _)| *o == id) => {
                    self.offer = None;
                    self.sending = None;
                    return Step::Cancel(id);
                }
                Report::Written(id, r)
                    if self.receiving.as_ref().is_some_and(|(i, _)| *i == id) =>
                {
                    self.receiving = None;
                    return match r {
                        Ok(roots) => Step::Received(roots),
                        Err(_) => Step::Stop(id),
                    };
                }
                _ => {}
            }
        }
    }
}

/// The folder for temporary files of the user the files are for.
fn temp_dir() -> Option<PathBuf> {
    #[cfg(windows)]
    if crate::agent::link().is_some() {
        return user::temp_dir();
    }
    Some(std::env::temp_dir())
}

/// Runs `f` with the rights of the user the files are for: in the helper
/// modes the console's signed-in user (None when nobody is), else as this
/// process.
/// Notes: called only on threads of the transfers' own.
fn as_user<T>(f: impl FnOnce() -> T) -> Option<T> {
    #[cfg(windows)]
    if crate::agent::link().is_some() {
        return user::run(f);
    }
    Some(f())
}

#[cfg(windows)]
mod user {
    //! The console's signed-in user, for the helper modes.
    use std::path::PathBuf;
    use windows::Win32::Foundation::{CloseHandle, HANDLE};
    use windows::Win32::Security::{ImpersonateLoggedOnUser, RevertToSelf};
    use windows::Win32::System::Environment::{CreateEnvironmentBlock, DestroyEnvironmentBlock};
    use windows::Win32::System::RemoteDesktop::{WTSGetActiveConsoleSessionId, WTSQueryUserToken};

    /// The console user's token, closed when dropped.
    struct Token(HANDLE);

    impl Drop for Token {
        fn drop(&mut self) {
            // SAFETY: closes the token handle this struct owns.
            unsafe {
                let _ = CloseHandle(self.0);
            }
        }
    }

    /// The token of the user signed in at the console, if any.
    fn token() -> Option<Token> {
        let mut h = HANDLE::default();
        // SAFETY: fills `h` with a token handle that Token then owns; the
        // caller is SYSTEM (the helper).
        unsafe { WTSQueryUserToken(WTSGetActiveConsoleSessionId(), &mut h) }.ok()?;
        Some(Token(h))
    }

    /// Runs `f` on this thread impersonating the console's user.
    /// Returns: None when nobody is signed in or impersonating failed.
    pub fn run<T>(f: impl FnOnce() -> T) -> Option<T> {
        let t = token()?;
        // SAFETY: impersonates with a valid token on this thread only.
        unsafe { ImpersonateLoggedOnUser(t.0) }.ok()?;
        let r = f();
        // SAFETY: ends this thread's impersonation; a thread that stayed
        // impersonated would only have the user's rights.
        unsafe {
            let _ = RevertToSelf();
        }
        Some(r)
    }

    /// The console user's TEMP folder, from their environment.
    pub fn temp_dir() -> Option<PathBuf> {
        let t = token()?;
        let mut env = std::ptr::null_mut();
        // SAFETY: builds the user's environment block, read below and
        // destroyed before returning.
        unsafe { CreateEnvironmentBlock(&mut env, Some(t.0), false) }.ok()?;
        let mut found = None;
        // SAFETY: the block is a list of NUL-terminated UTF-16 strings that
        // ends with an empty one.
        unsafe {
            let mut p = env as *const u16;
            loop {
                let len = (0..).take_while(|&i| *p.add(i) != 0).count();
                if len == 0 {
                    break;
                }
                let var = String::from_utf16_lossy(std::slice::from_raw_parts(p, len));
                if let Some((k, v)) = var.split_once('=')
                    && k.eq_ignore_ascii_case("TEMP")
                {
                    found = Some(PathBuf::from(v));
                }
                p = p.add(len + 1);
            }
            let _ = DestroyEnvironmentBlock(env);
        }
        found
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn file(path: &str, size: u64) -> Entry {
        Entry {
            path: path.into(),
            size,
            dir: false,
        }
    }

    fn dir(path: &str) -> Entry {
        Entry {
            path: path.into(),
            size: 0,
            dir: true,
        }
    }

    /// An empty folder of its own for a test.
    fn scratch(name: &str) -> PathBuf {
        let d = std::env::temp_dir().join(format!("kyokai-files-{name}-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&d);
        std::fs::create_dir_all(&d).unwrap();
        d
    }

    #[test]
    fn offers_with_unsafe_paths_are_refused() {
        let cases: &[(&str, bool)] = &[
            ("report.txt", true),
            ("folder/inner/report.txt", true),
            ("名前.txt", true),
            ("..", false),
            ("../evil.txt", false),
            ("folder/../../evil.txt", false),
            ("./a.txt", false),
            ("/etc/passwd", false),
            ("C:/Windows/evil.dll", false),
            ("C:evil", false),
            ("a\\..\\evil", false),
            ("\\\\server\\share\\x", false),
            ("folder//x", false),
            ("", false),
            ("trailing.", false),
            ("trailing ", false),
            ("NUL", false),
            ("nul.txt", false),
            ("folder/COM1", false),
            ("lpt9.log", false),
            ("COM¹", false),
            ("CONOUT$", false),
            ("console.txt", true),
            ("COM10", true),
            ("a|b", false),
            ("a?b", false),
            ("a*b", false),
            ("bell\u{7}", false),
        ];
        for &(path, ok) in cases {
            assert_eq!(check(&[file(path, 1)]), ok, "{path:?}");
        }
        assert!(!check(&[file(&"x".repeat(MAX_NAME + 1), 1)]), "long name");
        let deep = vec!["d"; MAX_DEPTH + 1].join("/");
        assert!(!check(&[file(&deep, 1)]), "too deep");
    }

    #[test]
    fn offers_over_the_limits_or_with_clashes_are_refused() {
        assert!(!check(&[]), "nothing");
        assert!(check(&[file("a", MAX_TOTAL)]));
        assert!(!check(&[file("a", MAX_TOTAL + 1)]), "too large");
        assert!(
            !check(&[file("a", MAX_TOTAL), file("b", 1)]),
            "too large together"
        );
        assert!(
            !check(&[file("a", u64::MAX), file("b", 2)]),
            "sizes that overflow"
        );
        let many: Vec<Entry> = (0..=MAX_ENTRIES)
            .map(|i| file(&format!("f{i}"), 0))
            .collect();
        assert!(!check(&many), "too many");
        assert!(check(&many[..MAX_ENTRIES]));
        assert!(!check(&[file("A.txt", 1), file("a.TXT", 1)]), "twice");
        assert!(
            !check(&[file("a", 1), file("a/b", 1)]),
            "an entry inside a file"
        );
        assert!(
            !check(&[Entry {
                path: "d".into(),
                size: 5,
                dir: true
            }]),
            "a folder with a size"
        );
        assert!(check(&[dir("d"), file("d/x", 1), dir("d/e")]));
    }

    #[test]
    fn listing_walks_folders_and_stops_at_the_limits() {
        let d = scratch("list");
        std::fs::create_dir_all(d.join("folder/inner")).unwrap();
        std::fs::write(d.join("folder/inner/b.txt"), b"bb").unwrap();
        std::fs::write(d.join("folder/a.txt"), b"a").unwrap();
        std::fs::write(d.join("top.bin"), [7u8; 5]).unwrap();
        let l = list(&[d.join("folder"), d.join("top.bin")]).unwrap();
        assert_eq!(
            l.entries,
            vec![
                dir("folder"),
                file("folder/a.txt", 1),
                dir("folder/inner"),
                file("folder/inner/b.txt", 2),
                file("top.bin", 5),
            ]
        );
        assert_eq!(l.sources.len(), 3);
        // More entries than an offer may hold: nothing is offered.
        let many = d.join("many");
        std::fs::create_dir_all(&many).unwrap();
        for i in 0..MAX_ENTRIES {
            std::fs::write(many.join(format!("{i}")), b"").unwrap();
        }
        assert!(list(std::slice::from_ref(&many)).is_none(), "too many");
        // More bytes than an offer may hold.
        let big = d.join("big.bin");
        std::fs::File::create(&big)
            .unwrap()
            .set_len(MAX_TOTAL + 1)
            .unwrap();
        assert!(list(&[big]).is_none(), "too large");
        let _ = std::fs::remove_dir_all(&d);
    }

    #[test]
    fn contents_cross_in_pieces_and_are_written_as_new_files() {
        let d = scratch("rw");
        std::fs::create_dir_all(d.join("src/sub")).unwrap();
        let big: Vec<u8> = (0..100_000u32).map(|i| (i % 251) as u8).collect();
        std::fs::write(d.join("src/sub/big.bin"), &big).unwrap();
        std::fs::write(d.join("src/empty"), b"").unwrap();
        let l = list(&[d.join("src")]).unwrap();
        let (tx, mut rx) = mpsc::channel(4);
        let sources = l.sources.clone();
        let reader =
            std::thread::spawn(move || read_all(42, &sources, 1000, &tx, &AtomicBool::new(false)));
        // The pieces carry the tag and the id; their contents feed the writer.
        let (wtx, mut wrx) = mpsc::channel(4);
        let out = d.join("out");
        let entries = l.entries.clone();
        let writer = std::thread::spawn(move || write_all(&out, &entries, &mut wrx));
        while let Some(p) = rx.blocking_recv() {
            assert_eq!(p[0], TAG);
            assert_eq!(u64::from_be_bytes(p[1..HEADER].try_into().unwrap()), 42);
            assert!(p.len() <= HEADER + 1000);
            wtx.blocking_send(p[HEADER..].to_vec()).unwrap();
        }
        drop(wtx);
        assert!(reader.join().unwrap());
        let roots = writer.join().unwrap().unwrap();
        assert_eq!(roots, vec![d.join("out").join("src")]);
        assert_eq!(std::fs::read(d.join("out/src/sub/big.bin")).unwrap(), big);
        assert_eq!(std::fs::read(d.join("out/src/empty")).unwrap(), b"");
        // A second write into the same place never overwrites.
        let (_t, mut none) = mpsc::channel(1);
        assert!(write_all(&d.join("out"), &l.entries, &mut none).is_err());
        let _ = std::fs::remove_dir_all(&d);
    }

    #[tokio::test]
    async fn a_refused_offer_writes_nothing() {
        let mut t = Transfers::new(1000);
        assert!(!t.offered(1, vec![file("../evil.txt", 3)]));
        assert!(!t.offered(2, vec![file("ok.txt", MAX_TOTAL + 1)]));
        assert!(t.receiving.is_none());
        assert!(t.inbox.root.get().is_none(), "no folder was made");
    }
}
