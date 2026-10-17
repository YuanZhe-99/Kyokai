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
    let (y_plane, uv_plane) = nv12.split_at_mut(out_w * out_h);
    let src = |x: usize, y: usize| {
        let sx = x * width / out_w;
        let sy = y * height / out_h;
        let i = sy * stride + sx * 4;
        (bgra[i + 2] as i32, bgra[i + 1] as i32, bgra[i] as i32)
    };
    for y in 0..out_h {
        let row = &mut y_plane[y * out_w..(y + 1) * out_w];
        for (x, px) in row.iter_mut().enumerate() {
            let (r, g, b) = src(x, y);
            *px = luma(r, g, b);
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
            (uv_plane[i], uv_plane[i + 1]) = chroma_of(r, g, b);
        }
    }
}

/// BT.601 limited-range luma of an RGB pixel.
fn luma(r: i32, g: i32, b: i32) -> u8 {
    (((66 * r + 129 * g + 25 * b + 128) >> 8) + 16) as u8
}

/// BT.601 limited-range chroma (U, V) of an RGB pixel.
fn chroma_of(r: i32, g: i32, b: i32) -> (u8, u8) {
    (
        (((-38 * r - 74 * g + 112 * b + 128) >> 8) + 128) as u8,
        (((112 * r - 94 * g - 18 * b + 128) >> 8) + 128) as u8,
    )
}

/// Converts a BGRA picture (`stride` bytes per row) into planar YUV laid
/// out as `layout` (the software encoders' I420 or, with `i444`, I444),
/// scaling to the layout's size with the nearest pixel.
/// Notes: `out` is resized to hold the layout; no allocation once it has
/// that size.
pub fn bgra_to_planar(
    bgra: &[u8],
    width: usize,
    height: usize,
    stride: usize,
    layout: &rustdesk_codec::EncodeYuvFormat,
    i444: bool,
    out: &mut Vec<u8>,
) {
    let (out_w, out_h) = (layout.w, layout.h);
    let (cw, ch) = if i444 {
        (out_w, out_h)
    } else {
        (out_w.div_ceil(2), out_h.div_ceil(2))
    };
    let [ys, us, vs] = [layout.stride[0], layout.stride[1], layout.stride[2]];
    out.resize(layout.v + vs * ch, 0);
    let src = |x: usize, y: usize| {
        let sx = x.min(out_w - 1) * width / out_w;
        let sy = y.min(out_h - 1) * height / out_h;
        let i = sy * stride + sx * 4;
        (bgra[i + 2] as i32, bgra[i + 1] as i32, bgra[i] as i32)
    };
    for y in 0..out_h {
        for x in 0..out_w {
            let (r, g, b) = src(x, y);
            out[y * ys + x] = luma(r, g, b);
        }
    }
    for y in 0..ch {
        for x in 0..cw {
            let (r, g, b) = if i444 {
                src(x, y)
            } else {
                // The average of the 2×2 block's corners.
                let (mut r, mut g, mut b) = (0, 0, 0);
                for (dx, dy) in [(0, 0), (1, 0), (0, 1), (1, 1)] {
                    let p = src(x * 2 + dx, y * 2 + dy);
                    r += p.0;
                    g += p.1;
                    b += p.2;
                }
                (r / 4, g / 4, b / 4)
            };
            let (u, v) = chroma_of(r, g, b);
            out[layout.u + y * us + x] = u;
            out[layout.v + y * vs + x] = v;
        }
    }
}

/// Converts planar YUV (I420, or I444 with `i444`; planes Y, U, V with
/// their strides) into RGBA of width×height.
pub fn planar_to_rgba(
    planes: [&[u8]; 3],
    strides: [usize; 3],
    width: usize,
    height: usize,
    i444: bool,
    rgba: &mut Vec<u8>,
) {
    rgba.resize(width * height * 4, 0);
    let shift = usize::from(!i444);
    for y in 0..height {
        let (yr, ur, vr) = (
            &planes[0][y * strides[0]..],
            &planes[1][(y >> shift) * strides[1]..],
            &planes[2][(y >> shift) * strides[2]..],
        );
        for x in 0..width {
            let o = (y * width + x) * 4;
            rgba[o..o + 4].copy_from_slice(&rgb(yr[x], ur[x >> shift], vr[x >> shift]));
        }
    }
}

/// The RGBA pixel of a BT.601 limited-range YUV sample.
fn rgb(y: u8, u: u8, v: u8) -> [u8; 4] {
    let clip = |v: i32| v.clamp(0, 255) as u8;
    let (c, d, e) = (y as i32 - 16, u as i32 - 128, v as i32 - 128);
    [
        clip((298 * c + 409 * e + 128) >> 8),
        clip((298 * c - 100 * d - 208 * e + 128) >> 8),
        clip((298 * c + 516 * d + 128) >> 8),
        255,
    ]
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
    for y in 0..height {
        for x in 0..width {
            let ci = (y / 2) * stride + (x & !1);
            let o = (y * width + x) * 4;
            rgba[o..o + 4].copy_from_slice(&rgb(nv12[y * stride + x], chroma[ci], chroma[ci + 1]));
        }
    }
}

use crate::media::{self, VideoStart};
use serde::{Deserialize, Serialize};
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, AtomicU32, Ordering};
use std::time::{Duration, Instant};
use tokio::sync::mpsc;

/// Frame rate the host captures and encodes at.
pub const FPS: u32 = 30;
/// Highest frame-rate cap a viewer may choose.
pub const MAX_FPS: u32 = 60;
/// Largest picture sent; bigger screens are scaled down.
const MAX_SIZE: (u32, u32) = (1920, 1080);
/// Starting target bitrate (RustDesk's QoS adapts it later).
const BITRATE: u32 = 4_000_000;

/// One encoded frame: flags (media::KEY_FRAME), presentation time in µs,
/// bytes.
pub type Encoded = (u8, u64, Vec<u8>);

/// A way the host may send its video: a codec name and full colour.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize)]
pub struct Choice {
    pub codec: &'static str,
    pub i444: bool,
}

impl Choice {
    /// The choice a codec name stands for, None for a codec this build
    /// does not know.
    pub fn parse(codec: &str, i444: bool) -> Option<Choice> {
        ORDER
            .into_iter()
            .find(|c| *c == codec)
            .map(|codec| Choice { codec, i444 })
    }
}

/// The codecs a host tries, best first: the system's (Media Foundation on
/// Windows) before the software ones; H.264 first (one GPU's HEVC did not
/// decode at 720p and above with Microsoft's HEVC decoder).
#[cfg(windows)]
const ORDER: [&str; 6] = ["h264", "hevc", "vp9", "vp9-sw", "av1", "vp8"];
#[cfg(not(windows))]
const ORDER: [&str; 3] = ["vp9-sw", "av1", "vp8"];

/// The ways a host tries to send, best first, from what the controller
/// decodes (`decode`) and decodes in full colour (`i444`).
/// Inputs: `full_colour` is the viewer's wish; full colour is chosen only
/// when the viewer wants it and both sides do it for the codec.
/// Returns: the candidates; the host opens the first whose encoder works.
/// Notes: a controller that names no codecs (older builds wait for none)
/// gets H.264 on Windows.
pub fn choose(decode: &[String], i444: &[String], full_colour: bool) -> Vec<Choice> {
    let has = |list: &[String], c: &str| list.iter().any(|d| d == c);
    let mut out = Vec::new();
    if full_colour {
        for c in ORDER {
            if has(i444, c)
                && has(decode, c)
                && crate::soft::Codec::from_name(c).is_some_and(|s| s.full_colour())
            {
                out.push(Choice {
                    codec: c,
                    i444: true,
                });
            }
        }
    }
    out.extend(
        ORDER
            .into_iter()
            .filter(|c| has(decode, c))
            .map(|codec| Choice { codec, i444: false }),
    );
    if decode.is_empty() && cfg!(windows) {
        out.push(Choice {
            codec: "h264",
            i444: false,
        });
    }
    out
}

/// The codecs this controller decodes, by name, and those it decodes in
/// full colour: the system's (Media Foundation, checked once) and the
/// software ones (always).
pub fn decodable() -> (Vec<String>, Vec<String>) {
    #[cfg(windows)]
    let mut decode: Vec<String> = crate::mf::decodable()
        .iter()
        .map(|c| c.name().to_string())
        .collect();
    #[cfg(not(windows))]
    let mut decode = Vec::new();
    let soft = crate::soft::Codec::ALL;
    decode.extend(soft.iter().map(|c| c.name().to_string()));
    let i444 = soft
        .iter()
        .filter(|c| c.full_colour())
        .map(|c| c.name().to_string())
        .collect();
    (decode, i444)
}

/// A display of the host: its place on the virtual desktop, in pixels.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Display {
    pub x: i32,
    pub y: i32,
    pub width: u32,
    pub height: u32,
    pub primary: bool,
}

/// The host's displays: the primary first, the others left to right (empty
/// where it cannot capture).
pub fn displays() -> Vec<Display> {
    if let Some(link) = crate::agent::link() {
        return link.displays();
    }
    #[cfg(windows)]
    {
        screen::displays()
    }
    #[cfg(not(windows))]
    {
        Vec::new()
    }
}

/// Puts displays in the order the viewer numbers them: the primary first,
/// the others left to right, then top to bottom.
pub fn order(list: &mut [Display]) {
    list.sort_by_key(|d| (!d.primary, d.x, d.y));
}

/// The host's video: its screen in the best codec it can send, else the
/// test pattern. Dropping it stops the producer.
pub struct Source {
    pub start: VideoStart,
    /// The display shown, None for the test pattern.
    pub display: Option<Display>,
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
    /// Starts the best source this host has: `display` (the primary when
    /// None) in the first of `choices` whose encoder opens, or the test
    /// pattern.
    pub fn open(display: Option<Display>, choices: &[Choice]) -> Source {
        let key = Arc::new(AtomicBool::new(true));
        let stop = Arc::new(AtomicBool::new(false));
        let targets = Arc::new(Targets {
            fps: AtomicU32::new(FPS),
            bitrate: AtomicU32::new(BITRATE),
        });
        let (tx, frames) = mpsc::channel(2);
        // In the helper modes the session agent captures and encodes.
        let link = crate::agent::link();
        if let Some(link) = link
            && let Some((start, shown)) = link.open_video(
                display,
                choices,
                tx.clone(),
                key.clone(),
                targets.clone(),
                stop.clone(),
            )
        {
            return Source {
                start,
                display: shown,
                frames,
                key,
                stop,
                targets,
            };
        }
        #[cfg(windows)]
        if link.is_none()
            && let Some(d) = display.or_else(|| displays().first().copied())
            && let Some(start) = choices.iter().find_map(|&c| {
                screen::start(tx.clone(), key.clone(), stop.clone(), targets.clone(), d, c).ok()
            })
        {
            return Source {
                start,
                display: Some(d),
                frames,
                key,
                stop,
                targets,
            };
        }
        let _ = (display, choices);
        let start = test_pattern(tx, stop.clone());
        Source {
            start,
            display: None,
            frames,
            key,
            stop,
            targets,
        }
    }

    /// Sets the frame rate and bitrate QoS chose (the test pattern keeps its
    /// own).
    pub fn set_targets(&self, fps: u32, bitrate: u32) {
        self.targets
            .fps
            .store(fps.clamp(1, MAX_FPS), Ordering::Relaxed);
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
    //! A display through RustDesk's DXGI capturer and the stream's encoder
    //! (Media Foundation's, or libvpx or libaom), on one thread (the capturer
    //! and Media Foundation are tied to the thread that made them).
    //! Where desktop duplication cannot open a display (no GPU output, some
    //! virtual machines and remote sessions), the capturer reads it through
    //! GDI instead and reports an unchanged screen the same way.
    use super::*;
    use crate::mf;
    use rustdesk_dxgi::{Capturer, Displays, Frame};

    /// How long the capture thread waits before opening the display again
    /// after it was lost.
    const REOPEN_AFTER: Duration = Duration::from_millis(500);

    /// Lists the displays attached to the desktop: DXGI's outputs, or GDI's
    /// display devices when DXGI has none.
    fn attached() -> Vec<rustdesk_dxgi::Display> {
        let dxgi: Vec<_> = Displays::new()
            .map(|all| all.filter(|d| d.attached_to_desktop()).collect())
            .unwrap_or_default();
        if dxgi.is_empty() {
            Displays::get_from_gdi()
        } else {
            dxgi
        }
    }

    /// The displays attached to the desktop, in the viewer's order.
    pub fn displays() -> Vec<Display> {
        let mut list: Vec<Display> = attached()
            .iter()
            .map(|d| {
                let (x, y) = d.origin();
                Display {
                    x,
                    y,
                    width: d.width() as u32,
                    height: d.height() as u32,
                    primary: (x, y) == (0, 0),
                }
            })
            .collect();
        order(&mut list);
        list
    }

    /// Opens a display by its place on the virtual desktop.
    /// Returns: its capturer and size in pixels.
    fn open(want: Display) -> Result<(Capturer, u32, u32), String> {
        let display = attached()
            .into_iter()
            .find(|d| d.origin() == (want.x, want.y))
            .ok_or("the display is gone")?;
        let (dw, dh) = (display.width() as u32, display.height() as u32);
        let capturer = Capturer::new(display).map_err(|e| format!("capturer: {e}"))?;
        Ok((capturer, dw, dh))
    }

    /// The stream's encoder with the picture it encodes next.
    enum Encoder {
        /// Media Foundation, fed NV12 of the stream's size.
        Mf {
            enc: mf::Encoder,
            nv12: Vec<u8>,
            width: usize,
            height: usize,
        },
        /// libvpx or libaom, fed planar YUV.
        Soft(crate::soft::Encoder),
    }

    impl Encoder {
        /// Opens the encoder a choice names for `width`×`height`.
        fn open(choice: Choice, width: u32, height: u32) -> Result<Encoder, String> {
            if let Some(c) = crate::soft::Codec::from_name(choice.codec) {
                return crate::soft::Encoder::new(c, width, height, FPS, BITRATE, choice.i444)
                    .map(Encoder::Soft);
            }
            let c = mf::Codec::from_name(choice.codec).ok_or("unknown codec")?;
            Ok(Encoder::Mf {
                enc: mf::Encoder::new(c, width, height, FPS, BITRATE)?,
                nv12: Vec::new(),
                width: width as usize,
                height: height as usize,
            })
        }

        /// Whether the stream is in full colour.
        fn i444(&self) -> bool {
            matches!(self, Encoder::Soft(e) if e.i444())
        }

        /// Takes a captured BGRA picture as the next one to encode.
        fn picture(&mut self, bgra: &[u8], width: usize, height: usize, stride: usize) {
            match self {
                Encoder::Mf {
                    nv12,
                    width: w,
                    height: h,
                    ..
                } => bgra_to_nv12(bgra, width, height, stride, *w, *h, nv12),
                Encoder::Soft(e) => e.picture(bgra, width, height, stride),
            }
        }

        /// Whether a picture was taken yet.
        fn has_picture(&self) -> bool {
            match self {
                Encoder::Mf { nv12, .. } => !nv12.is_empty(),
                Encoder::Soft(e) => e.has_picture(),
            }
        }

        /// Encodes the last picture; `key` asks for a key frame.
        fn encode(&mut self, key: bool) -> Result<Vec<(Vec<u8>, bool)>, String> {
            match self {
                Encoder::Mf { enc, nv12, .. } => enc.encode(nv12, key),
                Encoder::Soft(e) => e.encode(key),
            }
        }

        /// Sets the target bitrate in bit/s.
        fn set_bitrate(&mut self, bitrate: u32) {
            match self {
                Encoder::Mf { enc, .. } => enc.set_bitrate(bitrate),
                Encoder::Soft(e) => e.set_bitrate(bitrate),
            }
        }
    }

    /// What one capture attempt gave.
    enum Got {
        Picture,
        Nothing,
        /// The display was lost: a mode change, the lock screen or a UAC
        /// prompt took the desktop.
        Lost,
    }

    /// Starts capturing and encoding.
    /// Returns: the stream description once the capturer and the encoder
    /// work, or why not (no interactive desktop, no encoder).
    pub fn start(
        tx: mpsc::Sender<Encoded>,
        key: Arc<AtomicBool>,
        stop: Arc<AtomicBool>,
        targets: Arc<Targets>,
        display: Display,
        choice: Choice,
    ) -> Result<VideoStart, String> {
        let (ready_tx, ready) = std::sync::mpsc::channel();
        std::thread::spawn(move || {
            crate::agent::follow_input_desktop();
            let opened = (|| {
                let (capturer, dw, dh) = open(display)?;
                let (w, h) = fit(dw, dh, MAX_SIZE.0, MAX_SIZE.1);
                let encoder = Encoder::open(choice, w, h)?;
                Ok::<_, String>((capturer, encoder, w, h))
            })();
            let (capturer, mut encoder, w, h) = match opened {
                Ok(v) => v,
                Err(e) => {
                    let _ = ready_tx.send(Err(e));
                    return;
                }
            };
            let _ = ready_tx.send(Ok(VideoStart {
                codec: choice.codec.into(),
                width: w,
                height: h,
                i444: encoder.i444(),
            }));
            let t0 = Instant::now();
            let mut bitrate = BITRATE;
            // Frames the encoder took without giving output yet: it holds a
            // few before its first output, so a still screen repeats the last
            // picture until they come out.
            let mut held = 0u32;
            // None while the display is lost: the old duplication is released
            // before a new one is made. The stream keeps its size; a new
            // display mode is scaled into it.
            let mut capturer = Some(capturer);
            while !stop.load(Ordering::Relaxed) {
                let Some(cap) = capturer.as_mut() else {
                    std::thread::sleep(REOPEN_AFTER);
                    // The display is lost when the input desktop changes
                    // (a UAC prompt, the lock screen): the agent follows it.
                    crate::agent::follow_input_desktop();
                    capturer = open(display).ok().map(|(c, _, _)| c);
                    continue;
                };
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
                let got = match cap.frame(period.as_millis() as u32) {
                    Ok(Frame::PixelBuffer(pb)) => {
                        encoder.picture(pb.data, pb.width, pb.height, pb.stride);
                        Got::Picture
                    }
                    Err(e) if e.kind() != std::io::ErrorKind::WouldBlock => Got::Lost,
                    _ => Got::Nothing,
                };
                match got {
                    Got::Picture => {}
                    Got::Nothing if (wanted || held > 0) && encoder.has_picture() => {}
                    Got::Lost => {
                        if wanted {
                            key.store(true, Ordering::Relaxed);
                        }
                        capturer = None;
                        continue;
                    }
                    Got::Nothing => {
                        if wanted {
                            key.store(true, Ordering::Relaxed);
                        }
                        // GDI answers at once instead of waiting the timeout.
                        if let Some(rest) = period.checked_sub(begun.elapsed()) {
                            std::thread::sleep(rest);
                        }
                        continue;
                    }
                }
                let Ok(units) = encoder.encode(wanted) else {
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

    /// The GDI fallback reads a picture of the display's size and then
    /// reports the unchanged screen as nothing new. Needs an interactive
    /// desktop (skipped otherwise).
    #[test]
    fn the_gdi_fallback_captures_and_skips_unchanged_frames() {
        let Some(display) = rustdesk_dxgi::Displays::get_from_gdi().into_iter().next() else {
            return eprintln!("skipped: no display");
        };
        let (w, h) = (display.width() as usize, display.height() as usize);
        let mut cap = match rustdesk_dxgi::Capturer::new(display) {
            Ok(c) => c,
            Err(e) => return eprintln!("skipped: {e}"),
        };
        assert!(cap.is_gdi());
        match cap.frame(0) {
            Ok(rustdesk_dxgi::Frame::PixelBuffer(pb)) => {
                assert_eq!((pb.width, pb.height), (w, h));
                assert!(pb.data.len() >= w * h * 4);
            }
            Ok(_) => panic!("not a pixel buffer"),
            Err(e) => return eprintln!("skipped: {e}"),
        }
        // A still screen gives WouldBlock (a blinking caret may change it).
        let second = cap.frame(0).map(|_| ());
        eprintln!("second frame: {second:?}");
    }

    /// Every encoder kind starts with a key frame: Media Foundation's H.264
    /// and libvpx's VP9 in full colour. Needs an interactive desktop
    /// (skipped when capture is unavailable).
    #[test]
    fn the_screen_source_produces_key_frames_first() {
        let Some(display) = displays().first().copied() else {
            return eprintln!("skipped: no display");
        };
        for choice in [
            Choice {
                codec: "h264",
                i444: false,
            },
            Choice {
                codec: "vp9-sw",
                i444: true,
            },
        ] {
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
                display,
                choice,
            ) {
                Ok(s) => s,
                Err(e) => return eprintln!("skipped: {e}"),
            };
            eprintln!("stream {start:?}");
            assert_eq!(start.i444, choice.i444);
            let first = rx.blocking_recv().expect("a first frame");
            assert_eq!(first.0 & media::KEY_FRAME, media::KEY_FRAME);
            stop.store(true, Ordering::Relaxed);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn displays_are_numbered_primary_first_then_left_to_right() {
        let d = |x, y, primary| Display {
            x,
            y,
            width: 1920,
            height: 1080,
            primary,
        };
        let mut list = vec![
            d(1920, 0, false),
            d(-1920, 0, false),
            d(0, 0, true),
            d(0, 1080, false),
        ];
        order(&mut list);
        assert_eq!(
            list,
            vec![
                d(0, 0, true),
                d(-1920, 0, false),
                d(0, 1080, false),
                d(1920, 0, false)
            ]
        );
    }

    /// A choice case: what the controller decodes, in full colour, whether
    /// the viewer wants full colour, and what the host tries.
    type Case<'a> = (&'a [&'a str], &'a [&'a str], bool, &'a [&'a str]);

    /// The codecs a host tries, by name with `+444` for full colour.
    fn tried(decode: &[&str], i444: &[&str], full_colour: bool) -> Vec<String> {
        let names = |v: &[&str]| v.iter().map(|s| s.to_string()).collect::<Vec<_>>();
        choose(&names(decode), &names(i444), full_colour)
            .into_iter()
            .map(|c| format!("{}{}", c.codec, if c.i444 { "+444" } else { "" }))
            .collect()
    }

    #[test]
    fn hosts_choose_codecs_by_capability() {
        let all = ["h264", "hevc", "vp9", "vp9-sw", "av1", "vp8"];
        let full = ["vp9-sw", "av1"];
        #[cfg(windows)]
        let cases: &[Case] = &[
            // H.264 stays first; the software codecs follow the system's.
            (&all, &full, false, &all),
            (&["vp9", "hevc"], &[], false, &["hevc", "vp9"]),
            // A controller without H.264 (Windows N) gets software VP9.
            (
                &["vp9-sw", "av1", "vp8"],
                &full,
                false,
                &["vp9-sw", "av1", "vp8"],
            ),
            // Full colour only when the viewer wants it and both sides do it.
            (
                &all,
                &full,
                true,
                &[
                    "vp9-sw+444",
                    "av1+444",
                    "h264",
                    "hevc",
                    "vp9",
                    "vp9-sw",
                    "av1",
                    "vp8",
                ],
            ),
            (&all, &[], true, &all),
            (&["h264", "vp8"], &["vp8"], true, &["h264", "vp8"]),
            // Codecs this build does not know are skipped.
            (&["h266", "av1"], &["h266"], true, &["av1"]),
            // A controller that names none gets H.264.
            (&[], &[], false, &["h264"]),
        ];
        #[cfg(not(windows))]
        let cases: &[Case] = &[
            (&all, &full, false, &["vp9-sw", "av1", "vp8"]),
            (
                &all,
                &full,
                true,
                &["vp9-sw+444", "av1+444", "vp9-sw", "av1", "vp8"],
            ),
            (&["h264"], &[], false, &[]),
        ];
        for (decode, i444, want, expected) in cases {
            assert_eq!(
                tried(decode, i444, *want),
                expected.to_vec(),
                "{decode:?} {i444:?} {want}"
            );
        }
    }

    #[test]
    fn this_build_decodes_every_software_codec() {
        let (decode, i444) = decodable();
        for c in ["vp9-sw", "av1", "vp8"] {
            assert!(decode.iter().any(|d| d == c), "{c}");
        }
        assert_eq!(i444, ["vp9-sw", "av1"]);
    }

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
