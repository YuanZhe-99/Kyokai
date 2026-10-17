//! Pixel formats between capture, the codec and the viewer: captured BGRA
//! scaled into NV12 for the encoder, decoded NV12 into RGBA for the viewer
//! (BT.601, limited range, as the H.264 transforms expect by default).
//!
//! The conversions and the screen source run on Windows only so far; macOS
//! capture (ScreenCaptureKit) and its codec come with the Mac pass.
#![cfg_attr(not(windows), allow(dead_code))]

/// The encoded size for a captured screen: the screen scaled down to fit
/// `max_w`×`max_h`, keeping its aspect, with even sides (4:2:0 needs them).
pub fn fit(width: u32, height: u32, max_w: u32, max_h: u32) -> (u32, u32) {
    let scale = (max_w as f64 / width as f64)
        .min(max_h as f64 / height as f64)
        .min(1.0);
    let even = |v: f64| ((v.round() as u32) & !1).max(2);
    (even(width as f64 * scale), even(height as f64 * scale))
}

/// Converts a BGRA picture (`stride` bytes per row) into NV12 of
/// `out_w`×`out_h`, scaling with the nearest pixel.
/// Notes: `nv12` is resized to out_w × out_h × 3/2; no allocation once it
/// has that size.
pub fn bgra_to_nv12(
    bgra: &[u8],
    width: usize,
    height: usize,
    stride: usize,
    out_w: usize,
    out_h: usize,
    nv12: &mut Vec<u8>,
) {
    nv12.resize(out_w * out_h * 3 / 2, 0);
    let (luma, chroma) = nv12.split_at_mut(out_w * out_h);
    let src = |x: usize, y: usize| {
        let sx = x * width / out_w;
        let sy = y * height / out_h;
        let i = sy * stride + sx * 4;
        (bgra[i + 2] as i32, bgra[i + 1] as i32, bgra[i] as i32)
    };
    for y in 0..out_h {
        let row = &mut luma[y * out_w..(y + 1) * out_w];
        for (x, px) in row.iter_mut().enumerate() {
            let (r, g, b) = src(x, y);
            *px = (((66 * r + 129 * g + 25 * b + 128) >> 8) + 16) as u8;
        }
    }
    for y in 0..out_h / 2 {
        for x in 0..out_w / 2 {
            // The average of the 2×2 block's corners.
            let (mut r, mut g, mut b) = (0, 0, 0);
            for (dx, dy) in [(0, 0), (1, 0), (0, 1), (1, 1)] {
                let p = src(x * 2 + dx, y * 2 + dy);
                r += p.0;
                g += p.1;
                b += p.2;
            }
            let (r, g, b) = (r / 4, g / 4, b / 4);
            let i = y * out_w + x * 2;
            chroma[i] = (((-38 * r - 74 * g + 112 * b + 128) >> 8) + 128) as u8;
            chroma[i + 1] = (((112 * r - 94 * g - 18 * b + 128) >> 8) + 128) as u8;
        }
    }
}

/// Converts NV12 (`stride` bytes per luma row; chroma follows the luma
/// plane of `stride` × `rows`) into RGBA of width×height.
pub fn nv12_to_rgba(
    nv12: &[u8],
    width: usize,
    height: usize,
    stride: usize,
    rows: usize,
    rgba: &mut Vec<u8>,
) {
    rgba.resize(width * height * 4, 0);
    let chroma = &nv12[stride * rows..];
    let clip = |v: i32| v.clamp(0, 255) as u8;
    for y in 0..height {
        for x in 0..width {
            let c = nv12[y * stride + x] as i32 - 16;
            let ci = (y / 2) * stride + (x & !1);
            let d = chroma[ci] as i32 - 128;
            let e = chroma[ci + 1] as i32 - 128;
            let o = (y * width + x) * 4;
            rgba[o] = clip((298 * c + 409 * e + 128) >> 8);
            rgba[o + 1] = clip((298 * c - 100 * d - 208 * e + 128) >> 8);
            rgba[o + 2] = clip((298 * c + 516 * d + 128) >> 8);
            rgba[o + 3] = 255;
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sizes_fit_and_stay_even() {
        assert_eq!(fit(3840, 2160, 1920, 1080), (1920, 1080));
        assert_eq!(fit(1280, 720, 1920, 1080), (1280, 720));
        assert_eq!(fit(2560, 1600, 1920, 1080), (1728, 1080));
        assert_eq!(fit(1366, 769, 1920, 1080), (1366, 768));
    }

    #[test]
    fn colours_survive_the_round_trip() {
        // Flat colour blocks: BGRA → NV12 → RGBA stays within a few levels.
        let colours = [
            [30u8, 60, 200],
            [250, 250, 250],
            [10, 10, 10],
            [200, 120, 40],
        ];
        let (w, h) = (8, 8);
        for [r, g, b] in colours {
            let bgra: Vec<u8> = (0..w * h).flat_map(|_| [b, g, r, 255]).collect();
            let mut nv12 = Vec::new();
            bgra_to_nv12(&bgra, w, h, w * 4, w, h, &mut nv12);
            let mut rgba = Vec::new();
            nv12_to_rgba(&nv12, w, h, w, h, &mut rgba);
            for px in rgba.chunks(4) {
                for (got, want) in px[..3].iter().zip([r, g, b]) {
                    assert!(
                        (*got as i32 - want as i32).abs() <= 3,
                        "{px:?} vs {:?}",
                        [r, g, b]
                    );
                }
            }
        }
    }
}

use crate::media::{self, VideoStart};
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, AtomicU32, Ordering};
use std::time::{Duration, Instant};
use tokio::sync::mpsc;

/// Frame rate the host captures and encodes at.
pub const FPS: u32 = 30;
/// Largest picture sent; bigger screens are scaled down.
const MAX_SIZE: (u32, u32) = (1920, 1080);
/// Starting target bitrate (RustDesk's QoS adapts it later).
const BITRATE: u32 = 4_000_000;

/// One encoded frame: flags (media::KEY_FRAME), presentation time in µs,
/// bytes.
pub type Encoded = (u8, u64, Vec<u8>);

/// The host's video: its screen through H.264 when it can, else the test
/// pattern. Dropping it stops the producer.
pub struct Source {
    pub start: VideoStart,
    pub frames: mpsc::Receiver<Encoded>,
    key: Arc<AtomicBool>,
    stop: Arc<AtomicBool>,
    targets: Arc<Targets>,
}

/// What QoS asks of the producer, read on every frame.
pub struct Targets {
    pub fps: AtomicU32,
    /// Bits per second.
    pub bitrate: AtomicU32,
}

impl Source {
    /// Starts the best source this host has.
    pub fn open() -> Source {
        let key = Arc::new(AtomicBool::new(true));
        let stop = Arc::new(AtomicBool::new(false));
        let targets = Arc::new(Targets {
            fps: AtomicU32::new(FPS),
            bitrate: AtomicU32::new(BITRATE),
        });
        let (tx, frames) = mpsc::channel(2);
        #[cfg(windows)]
        if let Ok(start) = screen::start(tx.clone(), key.clone(), stop.clone(), targets.clone()) {
            return Source {
                start,
                frames,
                key,
                stop,
                targets,
            };
        }
        let start = test_pattern(tx, stop.clone());
        Source {
            start,
            frames,
            key,
            stop,
            targets,
        }
    }

    /// Sets the frame rate and bitrate QoS chose (the test pattern keeps its
    /// own).
    pub fn set_targets(&self, fps: u32, bitrate: u32) {
        self.targets.fps.store(fps.clamp(1, 60), Ordering::Relaxed);
        self.targets
            .bitrate
            .store(bitrate.clamp(100_000, 50_000_000), Ordering::Relaxed);
    }

    /// Asks for a key frame next (a viewer opened or lost a frame).
    pub fn key_frame(&self) {
        self.key.store(true, Ordering::Relaxed);
    }
}

impl Drop for Source {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::Relaxed);
    }
}

/// The 320×180 raw-RGBA test pattern, for hosts that cannot capture.
fn test_pattern(tx: mpsc::Sender<Encoded>, stop: Arc<AtomicBool>) -> VideoStart {
    let mut pattern = media::TestPattern::new();
    let start = pattern.start();
    std::thread::spawn(move || {
        let t0 = Instant::now();
        while !stop.load(Ordering::Relaxed) {
            std::thread::sleep(Duration::from_secs(1) / media::TestPattern::FPS);
            let frame = (
                media::KEY_FRAME,
                t0.elapsed().as_micros() as u64,
                pattern.next_frame(),
            );
            // The newest frame wins: a full channel drops this one.
            if let Err(mpsc::error::TrySendError::Closed(_)) = tx.try_send(frame) {
                break;
            }
        }
    });
    start
}

#[cfg(windows)]
mod screen {
    //! The primary display through RustDesk's DXGI capturer and the system's
    //! H.264 encoder, on one thread (both are tied to the thread that made
    //! them).
    use super::*;
    use crate::mf;
    use rustdesk_dxgi::{Capturer, Displays, Frame};

    /// Starts capturing and encoding.
    /// Returns: the stream description once the capturer and the encoder
    /// work, or why not (no interactive desktop, no encoder).
    pub fn start(
        tx: mpsc::Sender<Encoded>,
        key: Arc<AtomicBool>,
        stop: Arc<AtomicBool>,
        targets: Arc<Targets>,
    ) -> Result<VideoStart, String> {
        let (ready_tx, ready) = std::sync::mpsc::channel();
        std::thread::spawn(move || {
            let opened = (|| {
                let display = Displays::new()
                    .map_err(|e| format!("displays: {e}"))?
                    .next()
                    .ok_or("no display")?;
                let (dw, dh) = (display.width() as u32, display.height() as u32);
                let capturer = Capturer::new(display).map_err(|e| format!("capturer: {e}"))?;
                let (w, h) = fit(dw, dh, MAX_SIZE.0, MAX_SIZE.1);
                let encoder = mf::Encoder::new(w, h, FPS, BITRATE)?;
                Ok::<_, String>((capturer, encoder, w, h))
            })();
            let (mut capturer, mut encoder, w, h) = match opened {
                Ok(v) => v,
                Err(e) => {
                    let _ = ready_tx.send(Err(e));
                    return;
                }
            };
            let _ = ready_tx.send(Ok(VideoStart {
                codec: "h264".into(),
                width: w,
                height: h,
            }));
            let t0 = Instant::now();
            let mut nv12 = Vec::new();
            let mut bitrate = BITRATE;
            // Frames the encoder took without giving output yet: it holds a
            // few before its first output, so a still screen repeats the last
            // picture until they come out.
            let mut held = 0u32;
            while !stop.load(Ordering::Relaxed) {
                let begun = Instant::now();
                let period = Duration::from_secs(1) / targets.fps.load(Ordering::Relaxed).max(1);
                let wanted_rate = targets.bitrate.load(Ordering::Relaxed);
                if wanted_rate != bitrate {
                    bitrate = wanted_rate;
                    encoder.set_bitrate(bitrate);
                }
                let wanted = key.swap(false, Ordering::Relaxed);
                // Unchanged screens time out: nothing is encoded or sent,
                // unless a key frame was asked for.
                match capturer.frame(period.as_millis() as u32) {
                    Ok(Frame::PixelBuffer(pb)) => {
                        bgra_to_nv12(
                            pb.data, pb.width, pb.height, pb.stride, w as usize, h as usize,
                            &mut nv12,
                        );
                    }
                    _ if (wanted || held > 0) && !nv12.is_empty() => {}
                    _ => {
                        if wanted {
                            key.store(true, Ordering::Relaxed);
                        }
                        continue;
                    }
                }
                let Ok(units) = encoder.encode(&nv12, wanted) else {
                    break;
                };
                held = if units.is_empty() {
                    (held + 1).min(FPS)
                } else {
                    0
                };
                let pts = t0.elapsed().as_micros() as u64;
                for (unit, is_key) in units {
                    let flags = if is_key { media::KEY_FRAME } else { 0 };
                    // Waits while the session is busy sending: capture runs
                    // at the speed the network takes frames.
                    if tx.blocking_send((flags, pts, unit)).is_err() {
                        return;
                    }
                }
                if let Some(rest) = period.checked_sub(begun.elapsed()) {
                    std::thread::sleep(rest);
                }
            }
        });
        ready
            .recv()
            .unwrap_or_else(|_| Err("the capture thread ended".into()))
    }
}

#[cfg(all(test, windows))]
mod screen_tests {
    use super::*;

    /// Needs an interactive desktop (skipped when capture is unavailable).
    #[test]
    fn the_screen_source_produces_key_frames_first() {
        let key = Arc::new(AtomicBool::new(true));
        let stop = Arc::new(AtomicBool::new(false));
        let (tx, mut rx) = mpsc::channel(2);
        let start = match screen::start(
            tx,
            key,
            stop.clone(),
            Arc::new(Targets {
                fps: AtomicU32::new(FPS),
                bitrate: AtomicU32::new(BITRATE),
            }),
        ) {
            Ok(s) => s,
            Err(e) => return eprintln!("skipped: {e}"),
        };
        eprintln!("stream {start:?}");
        let first = rx.blocking_recv().expect("a first frame");
        assert_eq!(first.0 & media::KEY_FRAME, media::KEY_FRAME);
        stop.store(true, Ordering::Relaxed);
    }
}
