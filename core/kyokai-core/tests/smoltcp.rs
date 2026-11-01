//! The data plane's TCP (doc/en-us/networking.md): EasyTier's in-process
//! smoltcp stack runs with CUBIC congestion control and a 200 ms floor on its
//! retransmission timeout. Both come from Kyokai's build, not EasyTier's
//! code, so a smoltcp or EasyTier upgrade could drop them silently; these
//! tests fail when it does.

use smoltcp::socket::tcp::{CongestionControl, Socket, SocketBuffer};

/// A TCP socket made the way EasyTier makes them (no controller chosen)
/// gets CUBIC: kyokai-core's `socket-tcp-cubic` feature reaches every
/// smoltcp socket in the build.
#[test]
fn every_tcp_socket_starts_with_cubic() {
    let socket = Socket::new(
        SocketBuffer::new(vec![0; 64]),
        SocketBuffer::new(vec![0; 64]),
    );
    assert_eq!(socket.congestion_control(), CongestionControl::Cubic);
}

/// The build takes smoltcp from core/third_party/smoltcp (core/Cargo.toml
/// [patch.crates-io]; a patch whose version no longer matches is dropped
/// with only a warning), and that copy keeps its retransmission timeout's
/// floor at 200 ms.
#[test]
fn the_vendored_smoltcp_retransmits_after_200_ms() {
    let lock = include_str!("../../Cargo.lock").replace("\r\n", "\n");
    let entry = lock
        .split("\n\n")
        .find(|p| p.contains("\nname = \"smoltcp\"\n"))
        .expect("smoltcp in Cargo.lock");
    assert!(
        !entry.contains("source ="),
        "smoltcp comes from a registry:\n{entry}"
    );
    let tcp = include_str!("../../third_party/smoltcp/src/socket/tcp.rs");
    assert!(tcp.contains("\nconst RTTE_MIN_RTO: u32 = 200;"));
}
