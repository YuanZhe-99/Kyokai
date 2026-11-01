//! Tunnels to a host's own SSH, Remote Desktop and VNC inside a Kyokai
//! session (doc/en-us/remote-services.md): one session per tunnel, its
//! bytes as plaintexts tagged 0x05 next to the usual ping, pong and bye.
//! Every buffer between the service, the session and the GUI is bounded, so
//! a fast side waits for a slow one instead of growing memory.

use crate::access::Permission;
use crate::session::{Failure, MAX_FRAME, MAX_PLAINTEXT, PING_EVERY, read_frame, write_frame};
use serde::{Deserialize, Serialize};
use std::collections::VecDeque;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Duration;
use tokio::io::{AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt};
use tokio::sync::{Notify, mpsc};

/// First plaintext byte of a tunnel's bytes (the rest of it).
pub const TAG: u8 = 0x05;
/// Pieces waiting each way between the service (or the GUI) and the
/// session.
const PIECES: usize = 16;
/// Largest piece: a frame's plaintext less the tag (just under 64 KiB).
pub const PIECE: usize = MAX_PLAINTEXT - 1;
/// A tunnel with no frame from its peer for this long is gone (a slow
/// reader on this side does not count: its frames wait in the transport).
const IDLE: Duration = Duration::from_secs(60);
/// How long a relay whose write failed waits for a goodbye already on its way:
/// a peer that said bye and left fails the write before the bye is read.
const BYE_WAIT: Duration = Duration::from_millis(500);
/// How long the host tries to reach its own service before it refuses the
/// tunnel as `service-down`.
pub const SERVICE_WAIT: Duration = Duration::from_secs(3);
/// Most tunnels a host serves at once, besides its desktop sessions.
pub const MAX_TUNNELS: usize = 8;

/// A service a tunnel reaches on the host's own loopback.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum Service {
    Ssh,
    Rdp,
    Vnc,
}

impl Service {
    /// The service's standard port (the host's user may set another:
    /// `access::ServicePorts`).
    pub fn port(self) -> u16 {
        #[cfg(test)]
        if let Some(p) = tests::port(self) {
            return p;
        }
        match self {
            Service::Ssh => 22,
            Service::Rdp => 3389,
            Service::Vnc => 5900,
        }
    }

    /// The permission a tunnel to it needs.
    pub fn permission(self) -> Permission {
        match self {
            Service::Ssh => Permission::UseSsh,
            Service::Rdp => Permission::UseRdp,
            Service::Vnc => Permission::UseVnc,
        }
    }
}

/// The GUI's ends of a controller's tunnel: bytes for the host, and the
/// host's bytes (`kyokai_media_write` and `kyokai_media_read`, packet 14).
#[derive(Clone)]
pub struct Ends {
    pub up: mpsc::Sender<Vec<u8>>,
    pub down: Arc<tokio::sync::Mutex<mpsc::Receiver<Vec<u8>>>>,
}

/// A tunnel's ends on one side, across its connections: the GUI's or the
/// service's bytes to send (`up`) and where the peer's go (`down`); with
/// resumption, the bytes sent and received in all and those sent and not
/// yet confirmed (`relay`).
pub struct Wire {
    pub up: mpsc::Receiver<Vec<u8>>,
    pub down: mpsc::Sender<Vec<u8>>,
    sent: u64,
    got: Arc<AtomicU64>,
    kept: VecDeque<Vec<u8>>,
    kept_bytes: usize,
    /// The last connection got as far as the peer's count (`take_synced`).
    synced: bool,
}

/// A controller tunnel's two bounded pipes.
pub fn pipe() -> (Ends, Wire) {
    let (up_tx, up_rx) = mpsc::channel(PIECES);
    let (down_tx, down_rx) = mpsc::channel(PIECES);
    (
        Ends {
            up: up_tx,
            down: Arc::new(tokio::sync::Mutex::new(down_rx)),
        },
        Wire::new(up_rx, down_tx),
    )
}

/// A connection a resumed tunnel carries on over.
pub trait Conn: AsyncRead + AsyncWrite + Unpin + Send {}
impl<T: AsyncRead + AsyncWrite + Unpin + Send> Conn for T {}

/// A resumed tunnel's new connection and its Noise state.
pub type Taken = (Box<dyn Conn>, snow::TransportState);

/// How long a host's tunnel waits for its controller to resume it, as a
/// desktop session's resume token lasts.
pub const RESUME_WAIT: Duration = Duration::from_secs(60);

/// The host's tunnels a resumed connection may carry on, by session.
static TAKERS: Mutex<Vec<(u64, mpsc::Sender<Taken>)>> = Mutex::new(Vec::new());

/// Registers host session `id` as a tunnel a resumed connection may carry
/// on (`hand_over`).
/// Returns: where those connections come.
pub fn wait_for(id: u64) -> mpsc::Receiver<Taken> {
    let (tx, rx) = mpsc::channel(1);
    if let Ok(mut t) = TAKERS.lock() {
        t.retain(|(i, _)| *i != id);
        t.push((id, tx));
    }
    rx
}

/// Hands a resumed connection to the tunnel session `id` carries on.
/// Returns: false when that session no longer waits.
pub fn hand_over(id: u64, taken: Taken) -> bool {
    let tx = TAKERS
        .lock()
        .ok()
        .and_then(|t| t.iter().find(|(i, _)| *i == id).map(|(_, tx)| tx.clone()));
    tx.is_some_and(|tx| tx.try_send(taken).is_ok())
}

/// Whether the tunnel session `id` waits for a resumed connection.
pub fn waiting(id: u64) -> bool {
    TAKERS.lock().is_ok_and(|t| t.iter().any(|(i, _)| *i == id))
}

/// The tunnel session `id` ended: nothing carries it on any more.
pub fn done(id: u64) {
    if let Ok(mut t) = TAKERS.lock() {
        t.retain(|(i, _)| *i != id);
    }
}

/// Reaches a service on this computer's loopback: 127.0.0.1, then ::1, on
/// `port` (the one its user set, `ServicePorts`).
/// Returns: the connection, or None when neither answered within
/// SERVICE_WAIT.
pub async fn reach(port: u16) -> Option<tokio::net::TcpStream> {
    let attempt = async {
        for ip in [
            std::net::IpAddr::from([127, 0, 0, 1]),
            std::net::IpAddr::from(std::net::Ipv6Addr::LOCALHOST),
        ] {
            if let Ok(s) = tokio::net::TcpStream::connect((ip, port)).await {
                let _ = s.set_nodelay(true);
                return Some(s);
            }
        }
        None
    };
    tokio::time::timeout(SERVICE_WAIT, attempt)
        .await
        .ok()
        .flatten()
}

/// The host's side of a tunnel's service connection: its bytes in pieces,
/// and pieces written to it, through bounded channels.
/// Returns: the pieces read, the sender of pieces to write, and the reading
/// task, which the caller aborts when the tunnel ends (the writing task
/// writes what was queued, then closes the service's side).
pub fn bridge(
    stream: tokio::net::TcpStream,
) -> (
    mpsc::Receiver<Vec<u8>>,
    mpsc::Sender<Vec<u8>>,
    tokio::task::JoinHandle<()>,
) {
    let (mut rd, mut wr) = stream.into_split();
    let (in_tx, in_rx) = mpsc::channel(PIECES);
    let (out_tx, mut out_rx) = mpsc::channel::<Vec<u8>>(PIECES);
    let reading = tokio::spawn(async move {
        let mut buf = vec![0u8; PIECE];
        while let Ok(n @ 1..) = rd.read(&mut buf).await {
            if in_tx.send(buf[..n].to_vec()).await.is_err() {
                break;
            }
        }
    });
    tokio::spawn(async move {
        while let Some(piece) = out_rx.recv().await {
            if wr.write_all(&piece).await.is_err() {
                return;
            }
        }
        let _ = wr.shutdown().await;
    });
    (in_rx, out_tx, reading)
}

/// A tunnel's control messages: the session messages it uses, and with
/// resumption the count of bytes received (`got`).
#[derive(Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "kebab-case")]
enum Control {
    Ping,
    Pong,
    Bye,
    /// How many of the tunnel's bytes this side has received in all.
    Got {
        n: u64,
    },
    /// Anything else a peer sends: ignored.
    #[serde(other)]
    Unknown,
}

/// How a tunnel's reading half ended.
enum Read {
    /// The peer said goodbye.
    Bye,
    /// This side's consumer is gone (the service closed, or the GUI let go).
    Local,
}

/// Bytes sent and not yet confirmed that a side keeps for a resumed tunnel;
/// beyond them it stops reading its service or the GUI until the peer
/// confirms.
const KEEP: usize = 4 << 20;
/// A resumable tunnel tells its peer what it received after this many new
/// bytes, or GOT_EVERY after the last word, and at least every QUIET (a
/// word that keeps the peer hearing it while nothing else flows).
const GOT_BYTES: u64 = 256 << 10;
const GOT_EVERY: Duration = Duration::from_millis(250);
const QUIET: Duration = Duration::from_secs(1);
/// How often a resumable tunnel looks at its counts and for silence.
const LOOK_EVERY: Duration = Duration::from_millis(250);
/// How long a controller's resumable tunnel may hear nothing from a peer
/// that speaks every QUIET before its path counts as silent: the
/// connection is let go and redialed (doc/en-us/networking.md). Time this
/// side spends handing the peer's bytes to a slow GUI does not count.
const SILENT: Duration = Duration::from_secs(3);

impl Wire {
    /// A tunnel's ends: pieces to send from `up`, pieces received go to
    /// `down`.
    pub fn new(up: mpsc::Receiver<Vec<u8>>, down: mpsc::Sender<Vec<u8>>) -> Wire {
        Wire {
            up,
            down,
            sent: 0,
            got: Arc::new(AtomicU64::new(0)),
            kept: VecDeque::new(),
            kept_bytes: 0,
            synced: false,
        }
    }

    /// Whether the last connection got as far as the peer's count: a
    /// connection that never did carried nothing (`synced`, cleared).
    pub fn take_synced(&mut self) -> bool {
        std::mem::take(&mut self.synced)
    }

    /// Keeps a part about to be sent until the peer confirms it.
    fn keep(&mut self, part: &[u8]) {
        self.sent += part.len() as u64;
        self.kept_bytes += part.len();
        self.kept.push_back(part.to_vec());
    }

    /// The peer confirmed `n` bytes in all: those are kept no longer.
    fn confirm(&mut self, n: u64) {
        let base = self.sent - self.kept_bytes as u64;
        let mut drop = n.saturating_sub(base).min(self.kept_bytes as u64) as usize;
        while drop > 0 {
            let Some(front) = self.kept.front_mut() else {
                break;
            };
            if front.len() <= drop {
                drop -= front.len();
                self.kept_bytes -= front.len();
                self.kept.pop_front();
            } else {
                front.drain(..drop);
                self.kept_bytes -= drop;
                drop = 0;
            }
        }
    }
}

/// How a relay is to treat its connection.
pub struct Relay<'a> {
    /// Ends the tunnel with a goodbye (this side closed it).
    pub stop: &'a Notify,
    /// Ends the connection at once, the tunnel kept (a resumed connection
    /// took its place); None where nothing can.
    pub cut: Option<&'a Notify>,
    /// Both sides announced `move`: bytes are counted and confirmed, kept
    /// until confirmed, and sent again from the peer's count on a new
    /// connection.
    pub resumable: bool,
    /// The controller's side, which lets a silent connection go.
    pub watches: bool,
}

/// Carries a tunnel between an established session and its local side
/// until either ends: `wire.up` (the service's or the GUI's bytes) goes to
/// the peer, the peer's bytes go to `wire.down`.
/// Returns: Ok when the tunnel ended (a goodbye, or the local side ended);
/// Err when the connection failed, fell silent or was cut — a resumable
/// wire then carries on over the next connection with nothing lost or
/// repeated.
/// Notes: the reading and the writing halves run apart and share the Noise
/// state under a short lock, so neither waits for the other; a full
/// `wire.down` holds the reading half (and so the peer, through the
/// transport) instead of growing. A part is kept before it is written, so
/// one a failed write took goes again. The watch for silence runs beside
/// the loop, so a write waiting on a dead path cannot hold it up.
pub async fn relay<S: AsyncRead + AsyncWrite + Unpin + Send + 'static>(
    stream: S,
    noise: snow::TransportState,
    how: Relay<'_>,
    wire: &mut Wire,
) -> Result<(), Failure> {
    let noise = Arc::new(Mutex::new(noise));
    let (mut rd, mut wr) = tokio::io::split(stream);
    let (control_tx, mut control) = mpsc::channel::<Control>(4);
    let reader_noise = noise.clone();
    let local_out = wire.down.clone();
    let got = wire.got.clone();
    let started = std::time::Instant::now();
    // When the peer was last heard (milliseconds after `started`), whether
    // the reader is handing its bytes to this side's consumer (which a slow
    // one holds up), and the most the peer said it received (`Got`): an
    // ever-growing count, so the newest is all that matters and the reader
    // never waits to pass it on.
    let heard = Arc::new(AtomicU64::new(0));
    let delivering = Arc::new(std::sync::atomic::AtomicBool::new(false));
    let peer_got = Arc::new(AtomicU64::new(0));
    let peer_said = Arc::new(Notify::new());
    let (heard_r, delivering_r, peer_got_r, peer_said_r) = (
        heard.clone(),
        delivering.clone(),
        peer_got.clone(),
        peer_said.clone(),
    );
    let mut reader = tokio::spawn(async move {
        let mut plain = vec![0u8; MAX_FRAME];
        loop {
            let frame = tokio::time::timeout(IDLE, read_frame(&mut rd))
                .await
                .map_err(|_| Failure::Broken)??;
            heard_r.store(started.elapsed().as_millis() as u64, Ordering::Relaxed);
            let n = reader_noise
                .lock()
                .map_err(|_| Failure::Broken)?
                .read_message(&frame, &mut plain)?;
            match plain[..n].split_first() {
                Some((&TAG, bytes)) => {
                    let len = bytes.len() as u64;
                    delivering_r.store(true, Ordering::Relaxed);
                    let sent = local_out.send(bytes.to_vec()).await;
                    delivering_r.store(false, Ordering::Relaxed);
                    heard_r.store(started.elapsed().as_millis() as u64, Ordering::Relaxed);
                    if sent.is_err() {
                        return Ok(Read::Local);
                    }
                    got.fetch_add(len, Ordering::Relaxed);
                }
                _ => match serde_json::from_slice::<Control>(&plain[..n]) {
                    // The peer's ping, to answer.
                    Ok(Control::Ping) => {
                        let _ = control_tx.try_send(Control::Ping);
                    }
                    Ok(Control::Bye) => return Ok(Read::Bye),
                    Ok(Control::Got { n }) => {
                        peer_got_r.fetch_max(n, Ordering::Relaxed);
                        peer_said_r.notify_one();
                    }
                    Ok(Control::Pong | Control::Unknown) => {}
                    Err(_) => return Err(Failure::Broken),
                },
            }
        }
    });
    let mut buf = vec![0u8; MAX_FRAME];
    let mut plain = Vec::with_capacity(MAX_PLAINTEXT);
    let mut ping = tokio::time::interval(PING_EVERY);
    let mut look = tokio::time::interval(LOOK_EVERY);
    // Encrypts one plaintext and writes it; a peer that reads nothing for
    // IDLE is gone.
    macro_rules! send {
        ($plain:expr) => {{
            let n = noise
                .lock()
                .map_err(|_| Failure::Broken)?
                .write_message($plain, &mut buf)?;
            tokio::time::timeout(IDLE, write_frame(&mut wr, &buf[..n]))
                .await
                .map_err(|_| Failure::Broken)??;
        }};
    }
    // Sends one piece of the tunnel's bytes.
    macro_rules! data {
        ($part:expr) => {{
            let part: &[u8] = $part;
            plain.clear();
            plain.push(TAG);
            plain.extend_from_slice(part);
            send!(&plain);
        }};
    }
    let bye = serde_json::to_vec(&Control::Bye).map_err(|_| Failure::Broken)?;
    let pong = serde_json::to_vec(&Control::Pong).map_err(|_| Failure::Broken)?;
    let ping_msg = serde_json::to_vec(&Control::Ping).map_err(|_| Failure::Broken)?;
    let mut read_done = false;
    // A resumable wire says first what it received; it sends new bytes only
    // once the peer said the same and what it missed went again.
    let mut synced = !how.resumable;
    wire.synced = false;
    let mut told = wire.got.load(Ordering::Relaxed);
    let mut told_at = std::time::Instant::now();
    let cut = async {
        match how.cut {
            Some(c) => c.notified().await,
            None => std::future::pending().await,
        }
    };
    tokio::pin!(cut);
    // The controller's watch: a peer that speaks every QUIET and was not
    // heard for SILENT (time spent handing its bytes to a slow GUI aside).
    let silent = async {
        if !(how.watches && how.resumable) {
            return std::future::pending().await;
        }
        let mut look = tokio::time::interval(LOOK_EVERY);
        loop {
            look.tick().await;
            let quiet = started
                .elapsed()
                .saturating_sub(Duration::from_millis(heard.load(Ordering::Relaxed)));
            if quiet >= SILENT && !delivering.load(Ordering::Relaxed) {
                return quiet;
            }
        }
    };
    let outcome = async {
        if how.resumable {
            let g = serde_json::to_vec(&Control::Got { n: told }).map_err(|_| Failure::Broken)?;
            send!(&g);
        }
        loop {
            tokio::select! {
                _ = how.stop.notified() => {
                    send!(&bye);
                    return Ok(());
                }
                () = &mut cut => return Err(Failure::Broken),
                ended = &mut reader => {
                    read_done = true;
                    return match ended.map_err(|_| Failure::Broken)? {
                        Ok(Read::Bye) => Ok(()),
                        Ok(Read::Local) => {
                            send!(&bye);
                            Ok(())
                        }
                        Err(f) => Err(f),
                    };
                }
                () = peer_said.notified() => {
                    wire.confirm(peer_got.load(Ordering::Relaxed));
                    if !synced {
                        synced = true;
                        wire.synced = true;
                        // What the peer missed goes again, in order,
                        // before anything new.
                        let again: Vec<Vec<u8>> = wire.kept.iter().cloned().collect();
                        for piece in &again {
                            data!(piece);
                        }
                    }
                }
                Some(Control::Ping) = control.recv() => send!(&pong),
                piece = wire.up.recv(), if synced && wire.kept_bytes < KEEP => match piece {
                    Some(piece) => {
                        for part in piece.chunks(PIECE) {
                            if how.resumable {
                                wire.keep(part);
                            }
                            data!(part);
                        }
                    }
                    // The local side ended: what it sent is out, the peer
                    // ends too.
                    None => {
                        send!(&bye);
                        return Ok(());
                    }
                },
                _ = ping.tick() => send!(&ping_msg),
                _ = look.tick(), if how.resumable => {
                    let n = wire.got.load(Ordering::Relaxed);
                    let due = n > told && (n - told >= GOT_BYTES || told_at.elapsed() >= GOT_EVERY);
                    if due || told_at.elapsed() >= QUIET {
                        let g = serde_json::to_vec(&Control::Got { n }).map_err(|_| Failure::Broken)?;
                        send!(&g);
                        (told, told_at) = (n, std::time::Instant::now());
                    }
                }
            }
        }
    };
    let outcome = tokio::select! {
        o = outcome => o,
        quiet = silent => {
            log::info!("tunnel: nothing heard for {} ms: the connection is let go", quiet.as_millis());
            Err(Failure::Broken)
        }
    };
    // A write failed: the peer may have said bye and left just before (a
    // pong answering its last ping, say), which is an end, not a failure
    // (regression: a tunnel that ended cleanly was sometimes reported broken).
    let outcome = match outcome {
        Err(_) if !read_done => match tokio::time::timeout(BYE_WAIT, &mut reader).await {
            Ok(Ok(Ok(Read::Bye))) => Ok(()),
            _ => outcome,
        },
        done => done,
    };
    reader.abort();
    let _ = wr.flush().await;
    outcome
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;
    use std::collections::HashMap;
    use std::sync::atomic::{AtomicUsize, Ordering};

    /// Ports tests give the services instead of the real ones.
    static PORTS: Mutex<Option<HashMap<Service, u16>>> = Mutex::new(None);

    /// The port a test gave a service, if any.
    pub(super) fn port(s: Service) -> Option<u16> {
        PORTS.lock().ok()?.as_ref()?.get(&s).copied()
    }

    /// Makes `reach` dial `port` for a service (each test its own service).
    pub(crate) fn serve_on(s: Service, port: u16) {
        PORTS
            .lock()
            .unwrap()
            .get_or_insert_with(HashMap::new)
            .insert(s, port);
    }

    /// Two ends of an established Noise session over an in-memory pipe.
    fn noise_pair() -> (
        (tokio::io::DuplexStream, snow::TransportState),
        (tokio::io::DuplexStream, snow::TransportState),
    ) {
        let params: snow::params::NoiseParams =
            "Noise_XX_25519_ChaChaPoly_BLAKE2s".parse().unwrap();
        let make = |initiator: bool| {
            let b = snow::Builder::new(params.clone());
            let k = b.generate_keypair().unwrap();
            let b = b.local_private_key(&k.private).unwrap();
            if initiator {
                b.build_initiator().unwrap()
            } else {
                b.build_responder().unwrap()
            }
        };
        let (mut i, mut r) = (make(true), make(false));
        let (mut msg, mut out) = (vec![0u8; MAX_FRAME], vec![0u8; MAX_FRAME]);
        for from_i in [true, false, true] {
            let (w, rd) = if from_i {
                (&mut i, &mut r)
            } else {
                (&mut r, &mut i)
            };
            let n = w.write_message(&[], &mut msg).unwrap();
            rd.read_message(&msg[..n], &mut out).unwrap();
        }
        let (a, b) = tokio::io::duplex(1 << 16);
        (
            (a, i.into_transport_mode().unwrap()),
            (b, r.into_transport_mode().unwrap()),
        )
    }

    /// One side of a relayed tunnel, as its local side sees it.
    struct Side {
        send: mpsc::Sender<Vec<u8>>,
        recv: mpsc::Receiver<Vec<u8>>,
        stop: Arc<Notify>,
        done: tokio::task::JoinHandle<Result<(), Failure>>,
    }

    /// Relays a tunnel between two sides.
    fn tunnel() -> (Side, Side) {
        let ((a, na), (b, nb)) = noise_pair();
        let side = |stream: tokio::io::DuplexStream, noise| {
            let (send, local_in) = mpsc::channel(PIECES);
            let (local_out, recv) = mpsc::channel(PIECES);
            let stop = Arc::new(Notify::new());
            let s = stop.clone();
            let done = tokio::spawn(async move {
                let mut wire = Wire::new(local_in, local_out);
                let how = Relay {
                    stop: &s,
                    cut: None,
                    resumable: false,
                    watches: false,
                };
                relay(stream, noise, how, &mut wire).await
            });
            Side {
                send,
                recv,
                stop,
                done,
            }
        };
        (side(a, na), side(b, nb))
    }

    /// A byte of a test stream at an offset, so order and content are
    /// checked.
    fn byte(at: usize) -> u8 {
        (at % 251) as u8
    }

    /// Writes `total` bytes into a side in pieces, counting what went in.
    fn produce(
        send: mpsc::Sender<Vec<u8>>,
        total: usize,
        sent: Arc<AtomicUsize>,
    ) -> tokio::task::JoinHandle<()> {
        tokio::spawn(async move {
            let mut at = 0;
            while at < total {
                let n = PIECE.min(total - at);
                let piece: Vec<u8> = (at..at + n).map(byte).collect();
                if send.send(piece).await.is_err() {
                    return;
                }
                at += n;
                sent.store(at, Ordering::Relaxed);
            }
        })
    }

    /// Reads `total` bytes from a side, checking each, pausing `pause`
    /// after each piece.
    async fn consume(recv: &mut mpsc::Receiver<Vec<u8>>, total: usize, pause: Duration) {
        let mut at = 0;
        while at < total {
            let piece = recv.recv().await.expect("the tunnel ended early");
            for (i, b) in piece.iter().enumerate() {
                assert_eq!(*b, byte(at + i), "byte {}", at + i);
            }
            at += piece.len();
            tokio::time::sleep(pause).await;
        }
    }

    /// A fast side waits for a slow one: what is in flight stays bounded
    /// (the pieces queued each way and the transport's buffer), however much
    /// the fast side has to send.
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn a_fast_side_cannot_grow_memory() {
        let (a, mut b) = tunnel();
        let total = 50 << 20;
        let sent = Arc::new(AtomicUsize::new(0));
        let producer = produce(a.send.clone(), total, sent.clone());
        // Nobody reads for a while: the producer is held back.
        tokio::time::sleep(Duration::from_millis(500)).await;
        let held = sent.load(Ordering::Relaxed);
        let bound = 2 * PIECES * PIECE + (1 << 16) + 4 * PIECE;
        assert!(held <= bound, "{held} bytes went in with nobody reading");
        // A slow reader takes it all, in order, and the gap stays bounded.
        let mut read = 0;
        while read < total {
            let piece = b.recv.recv().await.unwrap();
            assert_eq!(piece[0], byte(read));
            read += piece.len();
            let ahead = sent.load(Ordering::Relaxed) - read;
            assert!(ahead <= bound, "{ahead} bytes ahead of the reader");
        }
        producer.await.unwrap();
        a.stop.notify_one();
        assert!(a.done.await.unwrap().is_ok());
        assert!(
            b.done.await.unwrap().is_ok(),
            "the goodbye ends the other side"
        );
    }

    /// Both directions full at once never wait on each other.
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn both_directions_full_do_not_deadlock() {
        let (mut a, mut b) = tunnel();
        let total = 8 << 20;
        let (sa, sb) = (Arc::new(AtomicUsize::new(0)), Arc::new(AtomicUsize::new(0)));
        let pa = produce(a.send.clone(), total, sa);
        let pb = produce(b.send.clone(), total, sb);
        // Neither side reads until both queues are full.
        tokio::time::sleep(Duration::from_millis(300)).await;
        let both = async {
            tokio::join!(
                consume(&mut a.recv, total, Duration::ZERO),
                consume(&mut b.recv, total, Duration::ZERO)
            )
        };
        tokio::time::timeout(Duration::from_secs(60), both)
            .await
            .expect("both directions finish");
        pa.await.unwrap();
        pb.await.unwrap();
    }

    /// When a local side ends, what it sent still arrives, then the other
    /// side's local end closes.
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn the_end_of_one_side_closes_the_other_after_its_bytes() {
        let (a, mut b) = tunnel();
        a.send.send(b"last words".to_vec()).await.unwrap();
        drop(a.send);
        assert_eq!(b.recv.recv().await.unwrap(), b"last words");
        assert!(b.recv.recv().await.is_none(), "closed after the bytes");
        assert!(a.done.await.unwrap().is_ok());
        assert!(b.done.await.unwrap().is_ok());
    }

    /// One side of a resumable tunnel over a run of connections, until its
    /// local side ends: each connection comes through `conns`, and `cut`
    /// ends the current one as a resume would.
    async fn carry(
        mut wire: Wire,
        mut conns: mpsc::Receiver<(tokio::io::DuplexStream, snow::TransportState)>,
        cut: Arc<Notify>,
        watches: bool,
    ) {
        let stop = Notify::new();
        while let Some((stream, noise)) = conns.recv().await {
            let how = Relay {
                stop: &stop,
                cut: Some(&cut),
                resumable: true,
                watches,
            };
            if relay(stream, noise, how, &mut wire).await.is_ok() {
                return;
            }
        }
    }

    /// A resumable tunnel whose connection is cut in the middle of a copy
    /// carries on over a new one: the other side gets every byte once, in
    /// order, the bytes in flight at the cut sent again from its count.
    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn a_resumed_tunnel_loses_and_repeats_nothing() {
        let (a_up_tx, a_up) = mpsc::channel(PIECES);
        let (a_down, _a_down_rx) = mpsc::channel(PIECES);
        let (b_up_tx, b_up) = mpsc::channel::<Vec<u8>>(PIECES);
        let (b_down, mut b_down_rx) = mpsc::channel(PIECES);
        let (a_conns_tx, a_conns) = mpsc::channel(1);
        let (b_conns_tx, b_conns) = mpsc::channel(1);
        let (a_cut, b_cut) = (Arc::new(Notify::new()), Arc::new(Notify::new()));
        tokio::spawn(carry(Wire::new(a_up, a_down), a_conns, a_cut.clone(), true));
        tokio::spawn(carry(
            Wire::new(b_up, b_down),
            b_conns,
            b_cut.clone(),
            false,
        ));
        let connect = |a: &mpsc::Sender<_>, b: &mpsc::Sender<_>| {
            let ((sa, na), (sb, nb)) = noise_pair();
            (a.try_send((sa, na)).is_ok(), b.try_send((sb, nb)).is_ok())
        };
        assert_eq!(connect(&a_conns_tx, &b_conns_tx), (true, true));
        // Two MiB in pieces of a few KiB, numbered by their bytes.
        const TOTAL: usize = 2 << 20;
        let data: Vec<u8> = (0..TOTAL).map(|i| (i % 251) as u8).collect();
        let sent = data.clone();
        tokio::spawn(async move {
            for piece in sent.chunks(3001) {
                if a_up_tx.send(piece.to_vec()).await.is_err() {
                    return;
                }
            }
        });
        let mut got = Vec::with_capacity(TOTAL);
        let mut cut_done = false;
        while got.len() < TOTAL {
            let piece = tokio::time::timeout(Duration::from_secs(10), b_down_rx.recv())
                .await
                .expect("bytes keep coming")
                .expect("the tunnel is open");
            got.extend_from_slice(&piece);
            if !cut_done && got.len() > TOTAL / 3 {
                cut_done = true;
                a_cut.notify_one();
                b_cut.notify_one();
                tokio::time::sleep(Duration::from_millis(50)).await;
                assert_eq!(connect(&a_conns_tx, &b_conns_tx), (true, true));
            }
        }
        assert_eq!(got.len(), TOTAL, "nothing repeated");
        assert!(got == data, "every byte once, in order");
        drop(b_up_tx);
    }
}
