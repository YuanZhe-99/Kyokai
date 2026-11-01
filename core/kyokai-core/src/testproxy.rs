//! A TCP proxy for tests whose path can be cut like a black hole: while cut
//! it passes nothing either way and closes nothing, and what was sent
//! arrives, in order, once it heals.

use std::net::{IpAddr, Ipv4Addr, SocketAddr};
use std::sync::{Arc, Mutex};

use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::tcp::{OwnedReadHalf, OwnedWriteHalf};
use tokio::net::{TcpListener, TcpStream};
use tokio::sync::watch;
use tokio::task::{AbortHandle, JoinHandle};

/// A running proxy: accepts on `addr` and passes each connection on to its
/// target; stops when dropped.
pub(crate) struct Proxy {
    addr: SocketAddr,
    /// True while the path is cut.
    cut: watch::Sender<bool>,
    accepting: JoinHandle<()>,
    /// Every connection it has passed on, aborted (closing both sides) by
    /// `close_all` or on drop.
    conns: Arc<Mutex<Vec<AbortHandle>>>,
}

impl Proxy {
    /// Start listens for connections to pass on to `to`.
    /// Returns: the running proxy, on 127.0.0.1 for a loopback target and on
    /// the target's own address otherwise (EasyTier refuses loopback peers),
    /// a port of its own either way.
    /// Notes: panics when it cannot listen; it is for tests.
    pub(crate) async fn start(to: SocketAddr) -> Proxy {
        let ip = match to.ip() {
            ip if ip.is_loopback() => IpAddr::V4(Ipv4Addr::LOCALHOST),
            ip => ip,
        };
        let listener = TcpListener::bind((ip, 0)).await.expect("proxy listens");
        let addr = listener.local_addr().expect("proxy address");
        let (cut, _) = watch::channel(false);
        let conns: Arc<Mutex<Vec<AbortHandle>>> = Arc::default();
        let accepting = tokio::spawn({
            let (cut, conns) = (cut.clone(), conns.clone());
            async move {
                while let Ok((c, _)) = listener.accept().await {
                    let task = tokio::spawn(pass(c, to, cut.subscribe()));
                    conns.lock().unwrap().push(task.abort_handle());
                }
            }
        });
        Proxy {
            addr,
            cut,
            accepting,
            conns,
        }
    }

    /// Addr is where the proxy accepts connections.
    pub(crate) fn addr(&self) -> SocketAddr {
        self.addr
    }

    /// Cut stops the path both ways: bytes are held, connections stay open,
    /// and a connection accepted meanwhile reaches the target only on `heal`.
    pub(crate) fn cut(&self) {
        self.cut.send_replace(true);
    }

    /// Heal lets the path pass bytes again, the held ones first.
    pub(crate) fn heal(&self) {
        self.cut.send_replace(false);
    }

    /// Close_all closes every connection passed on so far, both sides.
    pub(crate) fn close_all(&self) {
        for c in self.conns.lock().unwrap().drain(..) {
            c.abort();
        }
    }
}

impl Drop for Proxy {
    /// Drop stops accepting and closes every connection.
    fn drop(&mut self) {
        self.accepting.abort();
        self.close_all();
    }
}

/// Pass connects `c` to `to` once the path is not cut and pumps both ways
/// until both directions end.
/// Side effects: closes `c` when `to` cannot be reached.
async fn pass(c: TcpStream, to: SocketAddr, mut cut: watch::Receiver<bool>) {
    if cut.wait_for(|cut| !cut).await.is_err() {
        return;
    }
    let Ok(up) = TcpStream::connect(to).await else {
        return;
    };
    let _ = (c.set_nodelay(true), up.set_nodelay(true));
    let ((cr, cw), (ur, uw)) = (c.into_split(), up.into_split());
    tokio::join!(pump(cr, uw, cut.clone()), pump(ur, cw, cut));
}

/// Pump copies `from` to `to`, holding what it read while the path is cut,
/// and passes the end on (as a shutdown) only when the path is not cut.
async fn pump(mut from: OwnedReadHalf, mut to: OwnedWriteHalf, mut cut: watch::Receiver<bool>) {
    let mut buf = vec![0; 16 << 10];
    loop {
        let n = from.read(&mut buf).await.unwrap_or(0);
        if cut.wait_for(|cut| !cut).await.is_err() {
            return;
        }
        if n == 0 || to.write_all(&buf[..n]).await.is_err() {
            break;
        }
    }
    let _ = to.shutdown().await;
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::Duration;
    use tokio::time::timeout;

    /// Echo serves on loopback, writing back what each connection sends.
    async fn echo() -> SocketAddr {
        let l = TcpListener::bind((Ipv4Addr::LOCALHOST, 0)).await.unwrap();
        let addr = l.local_addr().unwrap();
        tokio::spawn(async move {
            while let Ok((mut c, _)) = l.accept().await {
                tokio::spawn(async move {
                    let (mut r, mut w) = c.split();
                    let _ = tokio::io::copy(&mut r, &mut w).await;
                });
            }
        });
        addr
    }

    /// Round_trip sends `msg` and reads as many bytes back, within `wait`.
    /// Returns: None when nothing came back in time.
    async fn round_trip(c: &mut TcpStream, msg: &[u8], wait: Duration) -> Option<Vec<u8>> {
        c.write_all(msg).await.unwrap();
        let mut got = vec![0; msg.len()];
        timeout(wait, c.read_exact(&mut got)).await.ok()?.ok()?;
        Some(got)
    }

    const SHORT: Duration = Duration::from_millis(300);
    const LONG: Duration = Duration::from_secs(5);

    #[tokio::test]
    async fn bytes_pass_both_ways() {
        let p = Proxy::start(echo().await).await;
        assert!(p.addr().ip().is_loopback());
        let mut c = TcpStream::connect(p.addr()).await.unwrap();
        assert_eq!(round_trip(&mut c, b"hello", LONG).await.unwrap(), b"hello");
    }

    #[tokio::test]
    async fn a_cut_holds_bytes_and_closes_nothing_until_it_heals() {
        let p = Proxy::start(echo().await).await;
        let mut c = TcpStream::connect(p.addr()).await.unwrap();
        assert!(round_trip(&mut c, b"one", LONG).await.is_some());
        p.cut();
        // Nothing comes back, and the connection is not closed either: a
        // closed one would answer the read at once.
        assert!(round_trip(&mut c, b"two", SHORT).await.is_none());
        let mut fresh = TcpStream::connect(p.addr()).await.unwrap();
        assert!(round_trip(&mut fresh, b"new", SHORT).await.is_none());
        p.heal();
        let mut got = [0; 3];
        timeout(LONG, c.read_exact(&mut got))
            .await
            .unwrap()
            .unwrap();
        assert_eq!(&got, b"two");
        timeout(LONG, fresh.read_exact(&mut got))
            .await
            .unwrap()
            .unwrap();
        assert_eq!(&got, b"new");
        assert_eq!(round_trip(&mut c, b"three", LONG).await.unwrap(), b"three");
    }

    #[tokio::test]
    async fn close_all_closes_every_connection() {
        let p = Proxy::start(echo().await).await;
        let mut c = TcpStream::connect(p.addr()).await.unwrap();
        assert!(round_trip(&mut c, b"one", LONG).await.is_some());
        p.cut();
        p.close_all();
        let mut b = [0; 1];
        // Closed, even while cut: the read ends (0 or a reset), not waits.
        let read = timeout(LONG, c.read(&mut b))
            .await
            .expect("the close arrives");
        assert!(matches!(read, Ok(0) | Err(_)));
        // The proxy still accepts afterwards.
        p.heal();
        let mut again = TcpStream::connect(p.addr()).await.unwrap();
        assert_eq!(
            round_trip(&mut again, b"again", LONG).await.unwrap(),
            b"again"
        );
    }
}
