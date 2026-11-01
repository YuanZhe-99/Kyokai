# smoltcp 0.14.0, as Kyokai builds it

This directory is the smoltcp 0.14.0 crate exactly as published on crates.io
(`smoltcp-0.14.0.crate`, upstream commit
`cbfe05f835cfc75bf781addc1c0eb2e1a0df1ea3`, https://github.com/smoltcp-rs/smoltcp),
licensed 0BSD (`LICENSE-0BSD.txt`). `core/Cargo.toml` puts it in place of the
crates.io release with `[patch.crates-io]`; EasyTier's in-process TCP/IP stack
is the only user.

The one change: in `src/socket/tcp.rs`, `RTTE_MIN_RTO`, the floor of the
retransmission timeout, is 200 ms (Linux's `TCP_RTO_MIN`) instead of 1 second,
so a lost segment on a fast path is sent again after a fifth of a second
rather than a whole one. The line is marked `Kyokai:`.

On a smoltcp update, unpack the new release's `.crate` here, make the same
change, and run `cargo test --manifest-path core/Cargo.toml --workspace`
(`kyokai-core/tests/smoltcp.rs` checks it). `go run ./tools/deps check`
reports when a newer smoltcp is out.
