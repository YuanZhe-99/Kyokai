//! Tunnels to a host's own SSH, Remote Desktop and VNC inside a Kyokai
//! session (doc/en-us/remote-services.md): one session per tunnel, its
//! bytes as plaintexts tagged 0x05 next to the usual ping, pong and bye.
//! Every buffer between the service, the session and the GUI is bounded, so
//! a fast side waits for a slow one instead of growing memory.

use crate::access::Permission;
use crate::session::{Failure, MAX_FRAME, MAX_PLAINTEXT, PING_EVERY, read_frame, write_frame};
use serde::{Deserialize, Serialize};
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
    /// The service's port on the host.
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

/// The session's ends of a controller's tunnel, taken once when it
/// connects.
pub struct Wire {
    pub up: mpsc::Receiver<Vec<u8>>,
    pub down: mpsc::Sender<Vec<u8>>,
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
        Wire {
            up: up_rx,
            down: down_tx,
        },
    )
}

/// Reaches a service on this computer's loopback: 127.0.0.1, then ::1.
/// Returns: the connection, or None when neither answered within
/// SERVICE_WAIT.
pub async fn reach(service: Service) -> Option<tokio::net::TcpStream> {
    let port = service.port();
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

/// A tunnel's control messages: the session messages it uses.
#[derive(Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "kebab-case")]
enum Control {
    Ping,
    Pong,
    Bye,
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

/// Carries a tunnel between an established session and its local side
/// until either ends: `local_in` (the service's or the GUI's bytes) goes to
/// the peer, the peer's bytes go to `local_out`.
/// Notes: the reading and the writing halves run apart and share the Noise
/// state under a short lock, so neither waits for the other; a full
/// `local_out` holds the reading half (and so the peer, through the
/// transport) instead of growing.
pub async fn relay<S: AsyncRead + AsyncWrite + Unpin + Send + 'static>(
    stream: S,
    noise: snow::TransportState,
    stop: &Notify,
    mut local_in: mpsc::Receiver<Vec<u8>>,
    local_out: mpsc::Sender<Vec<u8>>,
) -> Result<(), Failure> {
    let noise = Arc::new(Mutex::new(noise));
    let (mut rd, mut wr) = tokio::io::split(stream);
    let (control_tx, mut control) = mpsc::channel::<Control>(4);
    let reader_noise = noise.clone();
    let mut reader = tokio::spawn(async move {
        let mut plain = vec![0u8; MAX_FRAME];
        loop {
            let frame = tokio::time::timeout(IDLE, read_frame(&mut rd))
                .await
                .map_err(|_| Failure::Broken)??;
            let n = reader_noise
                .lock()
                .map_err(|_| Failure::Broken)?
                .read_message(&frame, &mut plain)?;
            match plain[..n].split_first() {
                Some((&TAG, bytes)) => {
                    if local_out.send(bytes.to_vec()).await.is_err() {
                        return Ok(Read::Local);
                    }
                }
                _ => match serde_json::from_slice::<Control>(&plain[..n]) {
                    Ok(Control::Ping) => {
                        let _ = control_tx.try_send(Control::Pong);
                    }
                    Ok(Control::Bye) => return Ok(Read::Bye),
                    Ok(Control::Pong | Control::Unknown) => {}
                    Err(_) => return Err(Failure::Broken),
                },
            }
        }
    });
    let mut buf = vec![0u8; MAX_FRAME];
    let mut plain = Vec::with_capacity(MAX_PLAINTEXT);
    let mut ping = tokio::time::interval(PING_EVERY);
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
    let bye = serde_json::to_vec(&Control::Bye).map_err(|_| Failure::Broken)?;
    let outcome = async {
        loop {
            tokio::select! {
                _ = stop.notified() => {
                    send!(&bye);
                    return Ok(());
                }
                ended = &mut reader => {
                    return match ended.map_err(|_| Failure::Broken)? {
                        Ok(Read::Bye) => Ok(()),
                        Ok(Read::Local) => {
                            send!(&bye);
                            Ok(())
                        }
                        Err(f) => Err(f),
                    };
                }
                Some(c) = control.recv() => {
                    let c = serde_json::to_vec(&c).map_err(|_| Failure::Broken)?;
                    send!(&c);
                }
                piece = local_in.recv() => match piece {
                    Some(piece) => {
                        for part in piece.chunks(PIECE) {
                            plain.clear();
                            plain.push(TAG);
                            plain.extend_from_slice(part);
                            send!(&plain);
                        }
                    }
                    // The local side ended: what it sent is out, the peer
                    // ends too.
                    None => {
                        send!(&bye);
                        return Ok(());
                    }
                },
                _ = ping.tick() => {
                    let p = serde_json::to_vec(&Control::Ping).map_err(|_| Failure::Broken)?;
                    send!(&p);
                }
            }
        }
    }
    .await;
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
            let done =
                tokio::spawn(async move { relay(stream, noise, &s, local_in, local_out).await });
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
}
