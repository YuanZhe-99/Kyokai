//! Files on the shared clipboard (doc/en-us/computer-use.md, "Files"): the
//! copying side lists what was copied and offers paths and sizes; the other
//! side checks them, accepts, and receives the contents, streamed in bounded
//! pieces, into a folder of its own for the session. In the Windows helper
//! the files are read and written as the console's signed-in user; a
//! process running as root on macOS or Linux (the helper there) moves no
//! files at all.

use serde::{Deserialize, Serialize};
use std::collections::HashSet;
use std::io::{Read, Write};
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, OnceLock};
use tokio::sync::mpsc;

/// Whether this process may list, read and write clipboard files.
/// Notes: false while it runs as root on macOS or Linux, which has no
/// console user to act as: files there would be read and written with
/// root's rights.
pub fn available() -> bool {
    #[cfg(unix)]
    // SAFETY: geteuid has no preconditions.
    let euid = unsafe { libc::geteuid() };
    #[cfg(not(unix))]
    let euid = 1;
    allowed(cfg!(windows), euid)
}

/// Whether a process with effective user `euid` may move files.
fn allowed(windows: bool, euid: u32) -> bool {
    windows || euid != 0
}

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
    /// Each file entry's file, in the entries' order.
    sources: Vec<Source>,
}

/// One file to read for an offer, as it was listed.
#[derive(Debug, Clone)]
struct Source {
    path: PathBuf,
    size: u64,
    /// Which file it was (see `identity`): a file put in its place since,
    /// a link among them, is not read.
    id: (u64, u64),
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
        l.sources.push(Source {
            path: path.to_path_buf(),
            size: meta.len(),
            id: listed(path, &meta)?,
        });
    }
    Some(())
}

/// Opens a file to read without following a link at its end (on unix
/// without waiting on a pipe put in its place).
fn open(path: &Path) -> std::io::Result<std::fs::File> {
    let mut o = std::fs::OpenOptions::new();
    o.read(true);
    #[cfg(unix)]
    std::os::unix::fs::OpenOptionsExt::custom_flags(&mut o, libc::O_NOFOLLOW | libc::O_NONBLOCK);
    #[cfg(windows)]
    std::os::windows::fs::OpenOptionsExt::custom_flags(
        &mut o,
        windows::Win32::Storage::FileSystem::FILE_FLAG_OPEN_REPARSE_POINT.0,
    );
    o.open(path)
}

/// Which file an open file is: its device and inode on unix, its volume and
/// file index on Windows.
fn identity(f: &std::fs::File) -> Option<(u64, u64)> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::MetadataExt;
        let m = f.metadata().ok()?;
        Some((m.dev(), m.ino()))
    }
    #[cfg(windows)]
    {
        use std::os::windows::io::AsRawHandle;
        use windows::Win32::Storage::FileSystem::{
            BY_HANDLE_FILE_INFORMATION, GetFileInformationByHandle,
        };
        let mut info = BY_HANDLE_FILE_INFORMATION::default();
        // SAFETY: the handle is `f`'s, open for this call; `info` is only
        // written.
        unsafe {
            GetFileInformationByHandle(
                windows::Win32::Foundation::HANDLE(f.as_raw_handle()),
                &mut info,
            )
        }
        .ok()?;
        Some((
            u64::from(info.dwVolumeSerialNumber),
            (u64::from(info.nFileIndexHigh) << 32) | u64::from(info.nFileIndexLow),
        ))
    }
}

/// Which file a listed path is, from its listing's metadata on unix (no
/// link followed), by opening it on Windows.
fn listed(path: &Path, meta: &std::fs::Metadata) -> Option<(u64, u64)> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::MetadataExt;
        let _ = path;
        Some((meta.dev(), meta.ino()))
    }
    #[cfg(windows)]
    {
        let _ = meta;
        identity(&open(path).ok()?)
    }
}

/// Reads an offer's files as pieces ready to seal, each at most `room`
/// bytes of contents, while `stop` is not set.
/// Returns: false when a file could not be read, changed size, or is no
/// longer the file that was listed.
fn read_all(
    id: u64,
    sources: &[Source],
    room: usize,
    tx: &mpsc::Sender<Vec<u8>>,
    stop: &AtomicBool,
) -> bool {
    for s in sources {
        let Ok(mut f) = open(&s.path) else {
            return false;
        };
        if identity(&f) != Some(s.id) {
            return false;
        }
        let mut left = s.size;
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

/// Writes an offer's entries into the new folder `dir` from the pieces'
/// contents, each file new (never over an existing one) and no link
/// followed below `dir`.
/// Returns: the copied items' paths, for the clipboard.
fn write_all(
    dir: &Path,
    entries: &[Entry],
    rx: &mut mpsc::Receiver<Vec<u8>>,
) -> std::io::Result<Vec<PathBuf>> {
    let local = |p: &str| p.split('/').fold(dir.to_path_buf(), |acc, n| acc.join(n));
    std::fs::create_dir(dir)?;
    for e in entries.iter().filter(|e| e.dir) {
        make_dirs(dir, &e.path)?;
    }
    let (mut buf, mut at) = (Vec::new(), 0);
    for e in entries.iter().filter(|e| !e.dir) {
        if let Some((parent, _)) = e.path.rsplit_once('/') {
            make_dirs(dir, parent)?;
        }
        // A new file: a link in its place is not followed but refused.
        let mut f = std::fs::File::create_new(local(&e.path))?;
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

/// Creates the folders of `rel` (names between `/`) below `dir` one name at
/// a time, going on only through real folders, never through a link.
fn make_dirs(dir: &Path, rel: &str) -> std::io::Result<()> {
    let mut at = dir.to_path_buf();
    for name in rel.split('/') {
        at.push(name);
        match std::fs::create_dir(&at) {
            Ok(()) => {}
            Err(e)
                if e.kind() == std::io::ErrorKind::AlreadyExists
                    && std::fs::symlink_metadata(&at)?.file_type().is_dir() => {}
            Err(e) => return Err(e),
        }
    }
    Ok(())
}

/// The session's folder for received files: created on first use, deleted
/// once the session and its last writer are done.
struct Inbox {
    tag: String,
    root: OnceLock<Option<PathBuf>>,
}

impl Inbox {
    /// The folder, created as the user the files are for; None where files
    /// are not available or the folder is not safely the user's own.
    fn root(&self) -> Option<&Path> {
        self.root
            .get_or_init(|| {
                if !available() {
                    return None;
                }
                let base = base_dir()?;
                as_user(|| make_inbox(&base, &self.tag))?.ok()
            })
            .as_deref()
    }
}

/// Makes the inbox `<base>/Kyokai/<tag>`.
/// Notes: on unix both folders are the user's own, 0700; one that exists
/// and is a link or someone else's is refused.
fn make_inbox(base: &Path, tag: &str) -> std::io::Result<PathBuf> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::DirBuilderExt;
        std::fs::DirBuilder::new()
            .recursive(true)
            .mode(0o700)
            .create(base)?;
        let kyokai = base.join("Kyokai");
        own_dir(&kyokai)?;
        let root = kyokai.join(tag);
        own_dir(&root)?;
        Ok(root)
    }
    #[cfg(not(unix))]
    {
        let root = base.join("Kyokai").join(tag);
        std::fs::create_dir_all(&root)?;
        Ok(root)
    }
}

/// Creates a folder only this user can enter, or accepts one that is
/// already this user's own (a real folder, not a link), closing it to
/// others.
#[cfg(unix)]
fn own_dir(path: &Path) -> std::io::Result<()> {
    use std::os::unix::fs::{DirBuilderExt, MetadataExt, PermissionsExt};
    match std::fs::DirBuilder::new().mode(0o700).create(path) {
        Ok(()) => return Ok(()),
        Err(e) if e.kind() == std::io::ErrorKind::AlreadyExists => {}
        Err(e) => return Err(e),
    }
    let m = std::fs::symlink_metadata(path)?;
    // SAFETY: geteuid has no preconditions.
    if !m.file_type().is_dir() || m.uid() != unsafe { libc::geteuid() } {
        return Err(std::io::ErrorKind::PermissionDenied.into());
    }
    if m.mode() & 0o077 != 0 {
        std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o700))?;
    }
    Ok(())
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
    /// An offer was written: its folder, and the copied items in it.
    Written(u64, std::io::Result<(PathBuf, Vec<PathBuf>)>),
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

/// The files to read for an offer.
type Sources = Arc<[Source]>;

/// One session's file transfers both ways, until dropped.
/// Notes: every file is read and written on a thread of its own, never on
/// the session's; a newer offer replaces an older one in each direction.
/// Only the latest offer received is kept on disk: once a newer one has
/// arrived, the older one's folder is removed (it is no longer on the
/// clipboard), so a session holds at most two offers' worth of files.
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
    /// The folder of the latest offer received whole (on the clipboard).
    shown: Option<PathBuf>,
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
            shown: None,
        }
    }

    /// Takes a local copy of files: lists it on a thread, then offers it
    /// (through `next`), replacing an older offer.
    /// Notes: files from this session's own inbox came from the other side
    /// and are not offered back (another clipboard program, such as
    /// RustDesk, can rewrite the clipboard between Kyokai's write and its
    /// record of it, so the clipboard watch alone does not catch the echo).
    /// Where files are not available (see `available`), nothing is offered.
    pub fn copied(&mut self, paths: Vec<PathBuf>) {
        if !available() {
            return;
        }
        if let Some(Some(root)) = self.inbox.root.get()
            && paths.iter().all(|p| within(p, root))
        {
            return;
        }
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
    /// Returns: whether to accept (then the session asks for the contents);
    /// never where files are not available (see `available`).
    pub fn offered(&mut self, id: u64, entries: Vec<Entry>) -> bool {
        self.receiving = None;
        if !available() || !check(&entries) {
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
                match write_all(&dir, &entries, &mut rx) {
                    Ok(roots) => Ok((dir, roots)),
                    Err(e) => {
                        let _ = std::fs::remove_dir_all(&dir);
                        Err(e)
                    }
                }
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

    /// Gives up both directions, as when the viewer stops sharing the
    /// clipboard: a listing under way is not offered either.
    /// Returns: this side's offer, which the other side is told is
    /// cancelled, and the offer being received, which it is asked to stop.
    pub fn halt(&mut self) -> (Option<u64>, Option<u64>) {
        self.next += 1;
        self.sending = None;
        let sent = self.offer.take().map(|(id, _)| id);
        let received = self.receiving.take().map(|(id, _)| id);
        (sent, received)
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
                Report::Written(id, r) => {
                    let current = self.receiving.as_ref().is_some_and(|(i, _)| *i == id);
                    match r {
                        Ok((dir, roots)) if current => {
                            self.receiving = None;
                            // The older offer leaves the clipboard: its
                            // folder goes.
                            if let Some(old) = self.shown.replace(dir) {
                                discard(old);
                            }
                            return Step::Received(roots);
                        }
                        Err(_) if current => {
                            self.receiving = None;
                            return Step::Stop(id);
                        }
                        // Written whole, but replaced by a newer offer:
                        // never on the clipboard.
                        Ok((dir, _)) => discard(dir),
                        Err(_) => {}
                    }
                }
                _ => {}
            }
        }
    }
}

/// Whether `path` is `root` or inside it, ignoring case as Windows does (the
/// clipboard reports a folder with the spelling it was first made with).
fn within(path: &Path, root: &Path) -> bool {
    let (p, r) = (
        path.to_string_lossy().to_lowercase(),
        root.to_string_lossy().to_lowercase(),
    );
    p.strip_prefix(&r)
        .is_some_and(|rest| rest.is_empty() || rest.starts_with(['\\', '/']))
}

/// The folder that holds the inbox, of the user the files are for: on
/// Windows their TEMP; on Linux their cache folder (`xdg_base`), on macOS
/// their Caches, never the temporary folder all users share.
fn base_dir() -> Option<PathBuf> {
    #[cfg(windows)]
    {
        if crate::agent::link().is_some() {
            return user::temp_dir();
        }
        Some(std::env::temp_dir())
    }
    #[cfg(target_os = "macos")]
    {
        let home = std::env::var_os("HOME").map(PathBuf::from)?;
        home.is_absolute()
            .then(|| home.join("Library").join("Caches"))
    }
    #[cfg(all(unix, not(target_os = "macos")))]
    {
        xdg_base(|v| std::env::var_os(v))
    }
}

/// The Linux inbox's base from the environment (`var`): the user's cache
/// folder, `$XDG_CACHE_HOME` or else `~/.cache`; the runtime folder only
/// without either (regression: it came first, but it is a tmpfs often
/// capped at a tenth of the memory, and an offer reaches 1 GiB).
/// Returns: None when no absolute folder is named.
#[cfg(any(all(unix, not(target_os = "macos")), test))]
fn xdg_base(var: impl Fn(&str) -> Option<std::ffi::OsString>) -> Option<PathBuf> {
    let abs = |v: &str| var(v).map(PathBuf::from).filter(|p| p.is_absolute());
    abs("XDG_CACHE_HOME")
        .or_else(|| abs("HOME").map(|h| h.join(".cache")))
        .or_else(|| abs("XDG_RUNTIME_DIR"))
}

/// Runs `f` with the rights of the user the files are for: in the Windows
/// helper the console's signed-in user (None when nobody is), else as this
/// process (which on macOS and Linux is never root: see `available`).
/// Notes: called only on threads of the transfers' own.
fn as_user<T>(f: impl FnOnce() -> T) -> Option<T> {
    if !available() {
        return None;
    }
    #[cfg(windows)]
    if crate::agent::link().is_some() {
        return user::run(f);
    }
    Some(f())
}

/// Removes a received offer's folder, on a thread of its own as the user.
fn discard(dir: PathBuf) {
    std::thread::spawn(move || as_user(|| std::fs::remove_dir_all(dir)));
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

    /// Regression: the Linux inbox went to the runtime folder first, a
    /// tmpfs often far smaller than an offer.
    #[test]
    fn the_linux_inbox_prefers_the_cache_folder() {
        let root = std::env::temp_dir();
        let (cache, home, run) = (root.join("cache"), root.join("home"), root.join("run"));
        type Case<'a> = (&'a [(&'a str, &'a Path)], Option<PathBuf>);
        let cases: [Case; 5] = [
            (
                &[
                    ("XDG_RUNTIME_DIR", &run),
                    ("XDG_CACHE_HOME", &cache),
                    ("HOME", &home),
                ],
                Some(cache.clone()),
            ),
            (
                &[("XDG_RUNTIME_DIR", &run), ("HOME", &home)],
                Some(home.join(".cache")),
            ),
            (&[("XDG_RUNTIME_DIR", &run)], Some(run.clone())),
            (
                &[("XDG_CACHE_HOME", Path::new("relative")), ("HOME", &home)],
                Some(home.join(".cache")),
            ),
            (&[], None),
        ];
        for (env, want) in cases {
            let got = xdg_base(|v| {
                env.iter()
                    .find(|(k, _)| *k == v)
                    .map(|(_, p)| p.as_os_str().to_owned())
            });
            assert_eq!(got, want, "{env:?}");
        }
    }

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

    /// Regression: files received from the other side and put on this
    /// clipboard were offered straight back (seen with RustDesk also
    /// watching the clipboard). The inbox is matched without regard to case.
    #[tokio::test]
    async fn received_files_are_never_offered_back() {
        if !available() {
            return; // tests run as root on macOS or Linux: no files at all
        }
        let mut t = Transfers::new(1000);
        let root = t.inbox.root().unwrap().to_path_buf();
        let upper = PathBuf::from(root.to_string_lossy().to_uppercase());
        t.copied(vec![
            root.join("1").join("a.txt"),
            upper.join("1").join("b"),
        ]);
        assert_eq!(t.next, 0, "an inbox copy was offered");
        // A folder that only starts with the inbox's name is not in it.
        let beside = PathBuf::from(format!("{}x", root.display()));
        t.copied(vec![beside.join("c.txt")]);
        assert_eq!(t.next, 1);
        let _ = std::fs::remove_dir_all(&root);
    }

    /// Regression: the macOS and Linux helper read and wrote clipboard
    /// files as root (a controller could fetch /etc/shadow, or plant files
    /// through links in the shared /tmp).
    #[test]
    fn root_on_macos_and_linux_moves_no_files() {
        for (windows, euid, ok) in [
            (false, 0, false),
            (false, 501, true),
            (true, 0, true),
            (true, 1, true),
        ] {
            assert_eq!(allowed(windows, euid), ok, "windows {windows}, euid {euid}");
        }
    }

    /// Regression: a file swapped (for a link, or another file) after it
    /// was listed was read in its place.
    #[test]
    fn a_file_replaced_after_listing_is_not_read() {
        let d = scratch("swap");
        std::fs::write(d.join("a.txt"), b"listed").unwrap();
        let l = list(&[d.join("a.txt")]).unwrap();
        let (tx, mut rx) = mpsc::channel(4);
        assert!(read_all(1, &l.sources, 1000, &tx, &AtomicBool::new(false)));
        assert_eq!(&rx.try_recv().unwrap()[HEADER..], b"listed");
        // Another file of the same size in its place (the listed one kept,
        // so it cannot share its identity).
        std::fs::rename(d.join("a.txt"), d.join("old.txt")).unwrap();
        std::fs::write(d.join("a.txt"), b"secret").unwrap();
        assert!(!read_all(1, &l.sources, 1000, &tx, &AtomicBool::new(false)));
        assert!(rx.try_recv().is_err(), "nothing of it was read");
        #[cfg(unix)]
        {
            std::fs::remove_file(d.join("a.txt")).unwrap();
            std::os::unix::fs::symlink(d.join("old.txt"), d.join("a.txt")).unwrap();
            assert!(!read_all(1, &l.sources, 1000, &tx, &AtomicBool::new(false)));
        }
        let _ = std::fs::remove_dir_all(&d);
    }

    /// Regression: every offer received kept its folder until the session
    /// ended, so a session could fill the disk 1 GiB at a time.
    #[tokio::test]
    async fn only_the_latest_offer_received_stays_on_disk() {
        if !available() {
            return;
        }
        let mut t = Transfers::new(1000);
        let mut folders = Vec::new();
        for id in 1..=3u64 {
            assert!(t.offered(id, vec![file("a.txt", 3)]));
            let mut piece = vec![TAG];
            piece.extend_from_slice(&id.to_be_bytes());
            piece.extend_from_slice(b"abc");
            t.piece(&piece).await;
            let Step::Received(roots) = t.next().await else {
                panic!("offer {id} was not received");
            };
            assert_eq!(std::fs::read(roots[0].as_path()).unwrap(), b"abc");
            folders.push(roots[0].parent().unwrap().to_path_buf());
        }
        let t0 = std::time::Instant::now();
        while folders[..2].iter().any(|f| f.exists()) {
            assert!(t0.elapsed().as_secs() < 10, "older offers still on disk");
            tokio::time::sleep(std::time::Duration::from_millis(10)).await;
        }
        assert!(folders[2].exists(), "the latest stays (on the clipboard)");
        let root = t.inbox.root().unwrap().to_path_buf();
        drop(t);
        let _ = std::fs::remove_dir_all(root);
    }

    #[cfg(unix)]
    #[test]
    fn the_inbox_is_private_and_never_a_link() {
        use std::os::unix::fs::PermissionsExt;
        let d = scratch("inbox");
        let made = make_inbox(&d, "t").unwrap();
        for p in [d.join("Kyokai"), made] {
            let mode = std::fs::metadata(&p).unwrap().permissions().mode();
            assert_eq!(mode & 0o777, 0o700, "{p:?}");
        }
        // A link planted where the inbox goes is refused, not followed.
        let other = scratch("inbox-other");
        std::os::unix::fs::symlink(&other, d.join("Kyokai").join("u")).unwrap();
        assert!(make_inbox(&d, "u").is_err());
        std::fs::create_dir(d.join("Kyokai").join("v")).unwrap();
        std::os::unix::fs::symlink(&other, d.join("Kyokai").join("v").join("1")).unwrap();
        let (_t, mut none) = mpsc::channel(1);
        let entries = [dir("x")];
        assert!(write_all(&d.join("Kyokai").join("v").join("1"), &entries, &mut none).is_err());
        assert!(!other.join("x").exists(), "nothing made through the link");
        let _ = std::fs::remove_dir_all(&d);
        let _ = std::fs::remove_dir_all(&other);
    }

    #[test]
    fn within_matches_whole_folders_without_case() {
        let root = Path::new(r"C:\Temp\Kyokai\ab");
        for (path, inside) in [
            (r"C:\Temp\Kyokai\ab", true),
            (r"c:\temp\kyokai\AB\1\x.txt", true),
            (r"C:\Temp\Kyokai\abc\x.txt", false),
            (r"C:\Temp\Kyokai\x.txt", false),
        ] {
            assert_eq!(within(Path::new(path), root), inside, "{path}");
        }
    }
}
