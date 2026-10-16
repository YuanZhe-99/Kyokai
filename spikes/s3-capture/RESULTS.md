# S3-lite — RustDesk's DXGI capturer into the S2 viewer

Run on Windows 11 ARM64 (Snapdragon dev kit, Adreno 8cx Gen 3, one 3840×2160 display at 59 Hz),
2026-10-16, Rust 1.99.1.

## Setup

- `src/dxgi/` is RustDesk's `libs/scrap/src/dxgi/{mod,gdi,hdr}.rs` at `fada664d`, vendored with a
  provenance header. Changes: the Magnification (privacy mode) module removed, logging through
  the `log` crate instead of `hbb_common`, and `src/shim.rs` supplying what the rest of `scrap`
  provided (frame and buffer types, the adapter description, `would_block_if_equal`, and libyuv's
  ARGB rotate/mirror in plain Rust). No `hbb_common`, `base` or libyuv code is used.
- `src/lib.rs` captures the primary display on its own thread (DXGI objects are not `Send`),
  converts BGRA to RGBA and scales it (nearest neighbour) to the viewer's 1920×1080, and exports
  the S2 ABI so `spikes/s2-viewer` shows the live desktop unchanged.

```bash
cargo build --release --manifest-path spikes/s3-capture/Cargo.toml
./.dev/s2-viewer.exe -lib spikes/s3-capture/target/release/s3_capture.dll -seconds 15
```

## Results

| Check | Result |
|---|---|
| Build | compiles as edition 2018 with `winapi` 0.3; only unused-code warnings in the vendored files |
| Frames | 59.8 / 59.8 fps captured, 59.8 / 59.9 fps shown (two 15 s runs; the viewer on screen keeps the desktop changing) |
| Conversion + 4K→1080p scaling | 14.6–14.7 % of one core ≈ 2.4 ms per frame, scalar code |
| Process total | 37.8–39.6 % of one core (viewer ≈ 23 %, conversion ≈ 15 %, capture ≈ 1–2 %) |

Verdict: passes. The capturer works decoupled from RustDesk; for the product it moves into
`core/third_party/rustdesk` (Phase 3), and the conversion should become part of the encode path
(BGRA → I420 for the codec on the host, I420 → RGBA at the viewer's size on the controller) rather
than a separate full-frame pass. Not measured here: capture latency to the glass, HDR displays,
rotated displays, the GDI fallback.
