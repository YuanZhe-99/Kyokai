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

/// A captured BGRA picture: top-down rows of `stride` bytes.
#[derive(Clone, Copy)]
pub struct Picture<'a> {
    pub bgra: &'a [u8],
    pub width: usize,
    pub height: usize,
    pub stride: usize,
}

/// Where each output column's and row's nearest source pixel is, for one
/// size pair: computed when the sizes change, so that scaling costs no
/// division per pixel.
#[derive(Default)]
struct Nearest {
    /// Source width, height and stride; output width and height.
    size: (usize, usize, usize, usize, usize),
    /// Byte offset in its row of each output column's source pixel.
    cols: Vec<usize>,
    /// Byte offset of each output row's source row.
    rows: Vec<usize>,
}

impl Nearest {
    /// The offsets for `src` shown as `out_w`×`out_h`.
    fn offsets(&mut self, src: &Picture<'_>, out_w: usize, out_h: usize) -> (&[usize], &[usize]) {
        let size = (src.width, src.height, src.stride, out_w, out_h);
        if self.size != size {
            self.size = size;
            self.cols = (0..out_w).map(|x| x * src.width / out_w * 4).collect();
            self.rows = (0..out_h)
                .map(|y| y * src.height / out_h * src.stride)
                .collect();
        }
        (&self.cols, &self.rows)
    }
}

thread_local! {
    /// The scaling offsets of this thread's stream: a capture thread
    /// converts one stream, whose size seldom changes.
    static NEAREST: std::cell::RefCell<Nearest> = std::cell::RefCell::default();
}

/// Converts a BGRA picture (`stride` bytes per row) into NV12 of
/// `out_w`×`out_h`, scaling with the nearest pixel, for the tests (the
/// encoders convert into their own buffers, `bgra_to_nv12_planes`).
/// Notes: `nv12` is resized to out_w × out_h × 3/2; no allocation once it
/// has that size.
#[cfg(test)]
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
    let (y, uv) = nv12.split_at_mut(out_w * out_h);
    let src = Picture {
        bgra,
        width,
        height,
        stride,
    };
    bgra_to_nv12_planes(src, out_w, out_h, y, out_w, uv, out_w);
}

/// Converts a BGRA picture into NV12 planes of `out_w`×`out_h`: `y`, and
/// `uv` with U and V interleaved, each `*_stride` bytes per row, scaling
/// with the nearest pixel; a 2×2 block's chroma is the average of its four
/// pixels.
/// Notes: an unscaled picture goes row by row without offsets; nothing
/// allocates once the sizes settled.
pub fn bgra_to_nv12_planes(
    src: Picture<'_>,
    out_w: usize,
    out_h: usize,
    y: &mut [u8],
    y_stride: usize,
    uv: &mut [u8],
    uv_stride: usize,
) {
    let same = src.width == out_w && src.height == out_h;
    NEAREST.with_borrow_mut(|near| {
        let (cols, rows) = near.offsets(&src, out_w, out_h);
        for (oy, &row) in rows.iter().enumerate() {
            let from = &src.bgra[row..];
            let line = &mut y[oy * y_stride..][..out_w];
            if same {
                for (px, out) in from[..out_w * 4].as_chunks::<4>().0.iter().zip(line) {
                    *out = luma(px[2].into(), px[1].into(), px[0].into());
                }
            } else {
                for (out, &c) in line.iter_mut().zip(cols) {
                    *out = luma(from[c + 2].into(), from[c + 1].into(), from[c].into());
                }
            }
        }
        for cy in 0..out_h / 2 {
            let (a, b) = (&src.bgra[rows[2 * cy]..], &src.bgra[rows[2 * cy + 1]..]);
            let line = &mut uv[cy * uv_stride..][..out_w / 2 * 2];
            if same {
                let pairs = a[..out_w / 2 * 8]
                    .as_chunks::<8>()
                    .0
                    .iter()
                    .zip(b[..out_w / 2 * 8].as_chunks::<8>().0);
                for ((pa, pb), out) in pairs.zip(line.as_chunks_mut::<2>().0) {
                    let avg = |i: usize| {
                        (pa[i] as i32 + pa[i + 4] as i32 + pb[i] as i32 + pb[i + 4] as i32) / 4
                    };
                    (out[0], out[1]) = chroma_of(avg(2), avg(1), avg(0));
                }
            } else {
                for (cx, out) in line.as_chunks_mut::<2>().0.iter_mut().enumerate() {
                    let (c0, c1) = (cols[2 * cx], cols[2 * cx + 1]);
                    let avg = |i: usize| {
                        (a[c0 + i] as i32 + a[c1 + i] as i32 + b[c0 + i] as i32 + b[c1 + i] as i32)
                            / 4
                    };
                    (out[0], out[1]) = chroma_of(avg(2), avg(1), avg(0));
                }
            }
        }
    });
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
    let src = Picture {
        bgra,
        width,
        height,
        stride,
    };
    NEAREST.with_borrow_mut(|near| {
        let (cols, rows) = near.offsets(&src, out_w, out_h);
        let px = |x: usize, y: usize| {
            let i = rows[y.min(out_h - 1)] + cols[x.min(out_w - 1)];
            (bgra[i + 2] as i32, bgra[i + 1] as i32, bgra[i] as i32)
        };
        let (luma_plane, chroma) = out.split_at_mut(layout.u);
        for (y, &row) in rows.iter().enumerate() {
            let from = &bgra[row..];
            for (o, &c) in luma_plane[y * ys..][..out_w].iter_mut().zip(cols) {
                *o = luma(from[c + 2].into(), from[c + 1].into(), from[c].into());
            }
        }
        let (u_plane, v_plane) = chroma.split_at_mut(layout.v - layout.u);
        for y in 0..ch {
            let (u_line, v_line) = (&mut u_plane[y * us..][..cw], &mut v_plane[y * vs..][..cw]);
            for (x, (u, v)) in u_line.iter_mut().zip(v_line).enumerate() {
                let (r, g, b) = if i444 {
                    px(x, y)
                } else {
                    // The average of the 2×2 block's corners.
                    let (mut r, mut g, mut b) = (0, 0, 0);
                    for (dx, dy) in [(0, 0), (1, 0), (0, 1), (1, 1)] {
                        let p = px(x * 2 + dx, y * 2 + dy);
                        r += p.0;
                        g += p.1;
                        b += p.2;
                    }
                    (r / 4, g / 4, b / 4)
                };
                (*u, *v) = chroma_of(r, g, b);
            }
        }
    });
}

/// The chroma terms of a BT.601 limited-range U, V pair, shared by the
/// pixels the pair covers.
#[inline(always)]
fn chroma_terms(u: u8, v: u8) -> (i32, i32, i32) {
    let (d, e) = (u as i32 - 128, v as i32 - 128);
    (409 * e, -100 * d - 208 * e, 516 * d)
}

/// The RGBA pixel of a luma sample with its chroma terms.
#[inline(always)]
fn pixel(y: u8, (r, g, b): (i32, i32, i32)) -> [u8; 4] {
    let c = 298 * (y as i32 - 16) + 128;
    let clip = |v: i32| (v >> 8).clamp(0, 255) as u8;
    [clip(c + r), clip(c + g), clip(c + b), 255]
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
    if width == 0 {
        return;
    }
    for (y, line) in rgba.chunks_exact_mut(width * 4).enumerate() {
        let cy = if i444 { y } else { y / 2 };
        let luma_row = &planes[0][y * strides[0]..][..width];
        let (ur, vr) = (&planes[1][cy * strides[1]..], &planes[2][cy * strides[2]..]);
        if i444 {
            let pixels = line.as_chunks_mut::<4>().0.iter_mut();
            for (((out, &l), &u), &v) in pixels.zip(luma_row).zip(ur).zip(vr) {
                *out = pixel(l, chroma_terms(u, v));
            }
        } else {
            let pairs = width / 2;
            let whole = line[..pairs * 8]
                .as_chunks_mut::<8>()
                .0
                .iter_mut()
                .zip(luma_row.as_chunks::<2>().0)
                .zip(ur.iter().zip(vr));
            for ((out, l), (&u, &v)) in whole {
                let t = chroma_terms(u, v);
                out[..4].copy_from_slice(&pixel(l[0], t));
                out[4..].copy_from_slice(&pixel(l[1], t));
            }
            if width % 2 == 1 {
                let t = chroma_terms(ur[pairs], vr[pairs]);
                line[pairs * 8..].copy_from_slice(&pixel(luma_row[width - 1], t));
            }
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
    let (y, uv) = nv12.split_at(stride * rows);
    nv12_planes_to_rgba(y, stride, uv, stride, width, height, rgba);
}

/// Converts NV12 planes (`y`, and `uv` with U and V interleaved, with their
/// strides) into RGBA of width×height.
pub fn nv12_planes_to_rgba(
    y: &[u8],
    y_stride: usize,
    uv: &[u8],
    uv_stride: usize,
    width: usize,
    height: usize,
    rgba: &mut Vec<u8>,
) {
    rgba.resize(width * height * 4, 0);
    if width == 0 {
        return;
    }
    let pairs = width / 2;
    for (row, line) in rgba.chunks_exact_mut(width * 4).enumerate() {
        let luma_row = &y[row * y_stride..][..width];
        let chroma = &uv[row / 2 * uv_stride..][..width.div_ceil(2) * 2];
        let whole = line[..pairs * 8]
            .as_chunks_mut::<8>()
            .0
            .iter_mut()
            .zip(luma_row.as_chunks::<2>().0)
            .zip(chroma.as_chunks::<2>().0);
        for ((out, l), c) in whole {
            let t = chroma_terms(c[0], c[1]);
            out[..4].copy_from_slice(&pixel(l[0], t));
            out[4..].copy_from_slice(&pixel(l[1], t));
        }
        if width % 2 == 1 {
            let t = chroma_terms(chroma[pairs * 2], chroma[pairs * 2 + 1]);
            line[pairs * 8..].copy_from_slice(&pixel(luma_row[width - 1], t));
        }
    }
}

use crate::media::{self, VideoStart};
use serde::{Deserialize, Serialize};
use std::sync::atomic::{AtomicBool, AtomicU32, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};
use tokio::sync::mpsc;

/// Frame rate the host captures and encodes at.
pub const FPS: u32 = 30;
/// Highest frame-rate cap a viewer may choose.
pub const MAX_FPS: u32 = 60;
/// Largest picture sent; bigger screens are scaled down.
pub(crate) const MAX_SIZE: (u32, u32) = (1920, 1080);
/// Starting target bitrate (RustDesk's QoS adapts it later).
const BITRATE: u32 = 4_000_000;
/// How many frames the host sends ahead of the controller's
/// acknowledgements: enough for the frame-rate cap over a relay's round
/// trip, few enough that a slow path delays the picture by a few frames,
/// not seconds. The helper's channel from its agent holds as many.
pub const IN_FLIGHT: u32 = 3;

/// One encoded frame.
pub struct Encoded {
    /// media::KEY_FRAME when it decodes on its own.
    pub flags: u8,
    /// Presentation time in µs.
    pub pts: u64,
    pub data: Vec<u8>,
    /// From taking the captured picture to its encoded frame (conversion
    /// and encoding), in µs: what the host adds to the delay.
    pub took_us: u32,
}

/// Whether captures show Kyokai's own windows: the host user turned "Hide
/// Kyokai from capture" off. Hidden until the GUI says otherwise. macOS's
/// capture filter follows it (live); on Windows the GUI keeps its windows
/// out by display affinity.
static SHOW_KYOKAI: AtomicBool = AtomicBool::new(false);

/// Whether captures show Kyokai's own windows (SHOW_KYOKAI).
pub fn show_kyokai() -> bool {
    SHOW_KYOKAI.load(Ordering::Relaxed)
}

/// Sets whether captures show Kyokai's own windows; running streams follow.
pub fn set_show_kyokai(show: bool) {
    SHOW_KYOKAI.store(show, Ordering::Relaxed);
}

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
/// Windows, VideoToolbox on macOS) before the software ones; H.264 first
/// unless `choose` is told the host has a free hardware HEVC encoder.
#[cfg(windows)]
const ORDER: [&str; 6] = ["h264", "hevc", "vp9", "vp9-sw", "av1", "vp8"];
#[cfg(target_os = "macos")]
const ORDER: [&str; 5] = ["h264", "hevc", "vp9-sw", "av1", "vp8"];
#[cfg(not(any(windows, target_os = "macos")))]
const ORDER: [&str; 3] = ["vp9-sw", "av1", "vp8"];

/// The ways a host tries to send, best first, from what the controller
/// decodes (`decode`) and decodes in full colour (`i444`).
/// Inputs: `full_colour` is the viewer's wish; full colour is chosen only
/// when the viewer wants it and both sides do it for the codec.
/// Returns: the candidates; the host opens the first whose encoder works.
/// `hevc_first` puts HEVC before H.264 (the host has a free hardware HEVC
/// encoder: better pictures for the bits).
/// Notes: a controller that names no codecs (older builds wait for none)
/// gets H.264 on Windows.
pub fn choose(
    decode: &[String],
    i444: &[String],
    full_colour: bool,
    hevc_first: bool,
) -> Vec<Choice> {
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
    let plain = out.len();
    out.extend(
        ORDER
            .into_iter()
            .filter(|c| has(decode, c))
            .map(|codec| Choice { codec, i444: false }),
    );
    if hevc_first && let Some(i) = out[plain..].iter().position(|c| c.codec == "hevc") {
        out[plain..=plain + i].rotate_right(1);
    }
    if decode.is_empty() && cfg!(windows) {
        out.push(Choice {
            codec: "h264",
            i444: false,
        });
    }
    out
}

/// The codecs this host encodes, by name: the system's (the encoders Media
/// Foundation has registered, VideoToolbox's) and the software ones.
pub fn encodable() -> Vec<&'static str> {
    #[cfg(windows)]
    let mut list: Vec<&'static str> = crate::mf::encodable().iter().map(|c| c.name()).collect();
    #[cfg(target_os = "macos")]
    let mut list: Vec<&'static str> = crate::vt::encodable().iter().map(|c| c.name()).collect();
    #[cfg(not(any(windows, target_os = "macos")))]
    let mut list = Vec::new();
    list.extend(crate::soft::Codec::ALL.iter().map(|c| c.name()));
    list
}

/// The codecs a viewer may choose from: what this host encodes
/// (`encodes`) that the controller decodes (`decode`), in the host's
/// order.
pub fn offer(decode: &[String], encodes: &[&str]) -> Vec<&'static str> {
    ORDER
        .into_iter()
        .filter(|c| encodes.contains(c) && decode.iter().any(|d| d == c))
        .collect()
}

/// Puts the choices of the viewer's `codec` first (its full-colour one
/// before its plain one) and keeps the others after them in their order;
/// None (Auto) keeps the order as it is.
pub fn prefer(mut choices: Vec<Choice>, codec: Option<&str>) -> Vec<Choice> {
    if let Some(codec) = codec {
        // A stable sort: the chosen codec's entries move up, nothing else moves.
        choices.sort_by_key(|c| c.codec != codec);
    }
    choices
}

/// Whether this host has a free hardware HEVC encoder (`choose`'s
/// `hevc_first`).
pub fn hevc_hardware() -> bool {
    #[cfg(windows)]
    {
        crate::mf::hardware_free(crate::mf::Codec::Hevc)
    }
    #[cfg(target_os = "macos")]
    {
        crate::vt::hevc_hardware()
    }
    #[cfg(not(any(windows, target_os = "macos")))]
    {
        false
    }
}

/// The codecs this controller decodes, by name, and those it decodes in
/// full colour: the system's (Media Foundation, checked once; VideoToolbox)
/// and the software ones (always).
pub fn decodable() -> (Vec<String>, Vec<String>) {
    #[cfg(windows)]
    let mut decode: Vec<String> = crate::mf::decodable()
        .iter()
        .map(|c| c.name().to_string())
        .collect();
    #[cfg(target_os = "macos")]
    let mut decode: Vec<String> = crate::vt::decodable()
        .iter()
        .map(|c| c.name().to_string())
        .collect();
    #[cfg(not(any(windows, target_os = "macos")))]
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
    #[cfg(target_os = "macos")]
    {
        crate::mac::displays()
    }
    #[cfg(not(any(windows, target_os = "macos")))]
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
/// test pattern. Dropping it stops the producer; the next source waits for
/// it to let go of its display and encoder (`wait_stopped`).
pub struct Source {
    pub start: VideoStart,
    /// The display shown, None for the test pattern.
    pub display: Option<Display>,
    pub frames: mpsc::Receiver<Encoded>,
    key: Arc<AtomicBool>,
    stop: Arc<AtomicBool>,
    targets: Arc<Targets>,
    /// The capture thread, which ends with the display and the encoder let
    /// go.
    done: Option<std::thread::JoinHandle<()>>,
    /// A stream of the helper's agent, closed as the source drops.
    _agent: Option<crate::agent::AgentVideo>,
}

/// Capture threads of dropped sources that may still hold their display
/// and encoder: a new source waits for them first, STOP_WAIT at most, so
/// that it gets the hardware encoder back (one per process, `mf.rs`).
static STOPPING: Mutex<Vec<std::thread::JoinHandle<()>>> = Mutex::new(Vec::new());
/// Longest a new source waits for the capture threads still stopping.
const STOP_WAIT: Duration = Duration::from_secs(1);

/// Waits until the capture threads of dropped sources have ended (STOP_WAIT
/// at most; a thread that takes longer is left be).
fn wait_stopped() {
    let pending = std::mem::take(&mut *STOPPING.lock().unwrap_or_else(|e| e.into_inner()));
    let until = Instant::now() + STOP_WAIT;
    while pending.iter().any(|t| !t.is_finished()) && Instant::now() < until {
        std::thread::sleep(Duration::from_millis(5));
    }
}

/// What QoS asks of the producer, read on every frame.
pub struct Targets {
    pub fps: AtomicU32,
    /// Bits per second.
    pub bitrate: AtomicU32,
    /// Frames the session may send now (`IN_FLIGHT` less those the
    /// controller has not acknowledged): the capture takes a picture only
    /// while there is room, so the picture that goes out is the newest one,
    /// not one that waited a round trip for the acknowledgements.
    pub room: AtomicU32,
}

impl Default for Targets {
    fn default() -> Self {
        Targets {
            fps: AtomicU32::new(FPS),
            bitrate: AtomicU32::new(BITRATE),
            room: AtomicU32::new(IN_FLIGHT),
        }
    }
}

impl Source {
    /// Starts the best source this host has: `display` (the primary when
    /// None) in the first of `choices` whose encoder opens, or the test
    /// pattern.
    /// Notes: waits first for the capture threads of dropped sources
    /// (`wait_stopped`), and opening a capture can take a second: call it
    /// off the async runtime.
    pub fn open(display: Option<Display>, choices: &[Choice]) -> Source {
        wait_stopped();
        let key = Arc::new(AtomicBool::new(true));
        let stop = Arc::new(AtomicBool::new(false));
        let targets = Arc::new(Targets::default());
        // One frame waits for the session at most: the capture loop takes a
        // picture only once the session has room for it (`Targets::room`).
        // Frames from the helper's agent arrive as it captures, a little
        // behind the room the session reports: they get the whole window.
        let link = crate::agent::link();
        let (tx, frames) = mpsc::channel(if link.is_some() {
            IN_FLIGHT as usize
        } else {
            1
        });
        if let Some(link) = link
            && let Some((start, shown, agent)) = link.open_video(
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
                done: None,
                _agent: Some(agent),
            };
        }
        #[cfg(windows)]
        type Platform = screen::Dxgi;
        #[cfg(target_os = "macos")]
        type Platform = crate::mac::Capture;
        #[cfg(any(windows, target_os = "macos"))]
        if link.is_none()
            && let Some(d) = display.or_else(|| displays().first().copied())
            && let Some((start, done)) = choices.iter().find_map(|&c| {
                capture::start::<Platform>(
                    tx.clone(),
                    key.clone(),
                    stop.clone(),
                    targets.clone(),
                    d,
                    c,
                )
                .ok()
            })
        {
            return Source {
                start,
                display: Some(d),
                frames,
                key,
                stop,
                targets,
                done: Some(done),
                _agent: None,
            };
        }
        let _ = (display, choices);
        Source::pattern()
    }

    /// The test pattern alone: for a host that cannot capture, and when
    /// opening the screen failed outright.
    pub fn pattern() -> Source {
        let stop = Arc::new(AtomicBool::new(false));
        let (tx, frames) = mpsc::channel(1);
        Source {
            start: test_pattern(tx, stop.clone()),
            display: None,
            frames,
            key: Arc::new(AtomicBool::new(true)),
            stop,
            targets: Arc::new(Targets::default()),
            done: None,
            _agent: None,
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

    /// Sets how many frames the session may send now (`Targets::room`).
    pub fn set_room(&self, room: u32) {
        self.targets
            .room
            .store(room.min(IN_FLIGHT), Ordering::Relaxed);
    }

    /// Asks for a key frame next (a viewer opened or lost a frame).
    pub fn key_frame(&self) {
        self.key.store(true, Ordering::Relaxed);
    }
}

impl Drop for Source {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::Relaxed);
        if let Some(done) = self.done.take() {
            STOPPING
                .lock()
                .unwrap_or_else(|e| e.into_inner())
                .push(done);
        }
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
            let frame = Encoded {
                flags: media::KEY_FRAME,
                pts: t0.elapsed().as_micros() as u64,
                data: pattern.next_frame(),
                took_us: 0,
            };
            // The newest frame wins: a full channel drops this one.
            if let Err(mpsc::error::TrySendError::Closed(_)) = tx.try_send(frame) {
                break;
            }
        }
    });
    start
}

/// What one capture attempt gave.
#[cfg(any(windows, target_os = "macos"))]
#[derive(Debug, PartialEq)]
pub(crate) enum Got {
    Picture,
    Nothing,
    /// The display was lost: a mode change, the lock screen or a UAC
    /// prompt took the desktop, or the display went away.
    Lost,
}

/// Receives a captured picture: top-down BGRA, width, height, bytes per row.
#[cfg(any(windows, target_os = "macos"))]
pub(crate) type Take<'a> = dyn FnMut(&[u8], usize, usize, usize) + 'a;

/// A display capturer the encode loop reads: DXGI (or GDI) on Windows,
/// ScreenCaptureKit on macOS.
#[cfg(any(windows, target_os = "macos"))]
pub(crate) trait Grab: Sized {
    /// Opens a display.
    /// Returns: the capturer and its pictures' size in pixels.
    fn open(display: Display) -> Result<(Self, u32, u32), String>;

    /// Waits up to `timeout` for a picture newer than the last one and hands
    /// it to `take` (top-down BGRA, width, height, bytes per row).
    fn frame(&mut self, timeout: Duration, take: &mut Take<'_>) -> Got;

    /// When the last picture handed over was taken, if the capturer knows
    /// better than `take` being called (before a copy of its own).
    fn taken(&self) -> Option<Instant> {
        None
    }
}

#[cfg(any(windows, target_os = "macos"))]
mod capture {
    //! The capture-and-encode loop shared by the platforms, on one thread
    //! (DXGI and Media Foundation are tied to the thread that made them).
    use super::*;

    /// How long the capture thread waits before opening the display again
    /// after it was lost.
    const REOPEN_AFTER: Duration = Duration::from_millis(500);
    /// How often the capture thread looks again for room while the session
    /// is still sending the last frame.
    const ROOM_WAIT: Duration = Duration::from_millis(2);
    /// Longest the capture thread waits for a picture before it looks at
    /// its stop flag again.
    const FRAME_WAIT: Duration = Duration::from_millis(100);
    /// How often a sleeping capture thread looks at its stop flag.
    const STOP_POLL: Duration = Duration::from_millis(20);
    /// How often a key frame that has not come out is asked of the encoder
    /// again.
    const KEY_FORCE_AGAIN: Duration = Duration::from_millis(500);
    /// How often a capture compares the input desktop with
    /// its own (the agent's, in the helper modes).
    const DESK_CHECK: Duration = Duration::from_millis(500);

    /// Sleeps `d`, or less once `stop` is set: a dropped source's thread
    /// ends within STOP_POLL, so the next source gets its encoder soon.
    fn nap(d: Duration, stop: &AtomicBool) {
        let until = Instant::now() + d;
        loop {
            let left = until.saturating_duration_since(Instant::now());
            if left.is_zero() || stop.load(Ordering::Relaxed) {
                return;
            }
            std::thread::sleep(left.min(STOP_POLL));
        }
    }

    /// The stream's encoder with the picture it encodes next.
    enum Encoder {
        /// Media Foundation, fed NV12 in samples of its own.
        #[cfg(windows)]
        Mf(crate::mf::Encoder),
        /// libvpx or libaom, fed planar YUV (boxed: it is far larger than
        /// the system encoders' handles).
        Soft(Box<crate::soft::Encoder>),
        /// VideoToolbox, fed NV12 in buffers of its own.
        #[cfg(target_os = "macos")]
        Vt(crate::vt::Encoder),
    }

    impl Encoder {
        /// Opens the encoder a choice names for `width`×`height`.
        fn open(choice: Choice, width: u32, height: u32) -> Result<Encoder, String> {
            if let Some(c) = crate::soft::Codec::from_name(choice.codec) {
                return crate::soft::Encoder::new(c, width, height, FPS, BITRATE, choice.i444)
                    .map(|e| Encoder::Soft(Box::new(e)));
            }
            #[cfg(windows)]
            {
                let c = crate::mf::Codec::from_name(choice.codec).ok_or("unknown codec")?;
                crate::mf::Encoder::new(c, width, height, FPS, BITRATE).map(Encoder::Mf)
            }
            #[cfg(target_os = "macos")]
            {
                let c = crate::vt::Codec::from_name(choice.codec).ok_or("unknown codec")?;
                crate::vt::Encoder::new(c, width, height, FPS, BITRATE).map(Encoder::Vt)
            }
        }

        /// Which encoder this is, by name, and whether it runs in hardware,
        /// as it reports (a system encoder may fall back to software).
        fn kind(&self) -> (&'static str, bool) {
            match self {
                #[cfg(windows)]
                Encoder::Mf(e) => ("media-foundation", e.hardware()),
                Encoder::Soft(e) => (e.library(), false),
                #[cfg(target_os = "macos")]
                Encoder::Vt(e) => ("videotoolbox", e.hardware()),
            }
        }

        /// Whether the stream is in full colour.
        fn i444(&self) -> bool {
            matches!(self, Encoder::Soft(e) if e.i444())
        }

        /// Takes a captured BGRA picture as the next one to encode.
        fn picture(&mut self, bgra: &[u8], width: usize, height: usize, stride: usize) {
            match self {
                #[cfg(windows)]
                Encoder::Mf(e) => e.picture(Picture {
                    bgra,
                    width,
                    height,
                    stride,
                }),
                Encoder::Soft(e) => e.picture(bgra, width, height, stride),
                #[cfg(target_os = "macos")]
                Encoder::Vt(e) => e.picture(bgra, width, height, stride),
            }
        }

        /// Whether a picture was taken yet.
        fn has_picture(&self) -> bool {
            match self {
                #[cfg(windows)]
                Encoder::Mf(e) => e.has_picture(),
                Encoder::Soft(e) => e.has_picture(),
                #[cfg(target_os = "macos")]
                Encoder::Vt(e) => e.has_picture(),
            }
        }

        /// Encodes the last picture; `key` asks for a key frame.
        fn encode(&mut self, key: bool) -> Result<Vec<(Vec<u8>, bool)>, String> {
            match self {
                #[cfg(windows)]
                Encoder::Mf(e) => e.encode(key),
                Encoder::Soft(e) => e.encode(key),
                #[cfg(target_os = "macos")]
                Encoder::Vt(e) => e.encode(key),
            }
        }

        /// Sets the target bitrate in bit/s.
        fn set_bitrate(&mut self, bitrate: u32) {
            match self {
                #[cfg(windows)]
                Encoder::Mf(e) => e.set_bitrate(bitrate),
                Encoder::Soft(e) => e.set_bitrate(bitrate),
                #[cfg(target_os = "macos")]
                Encoder::Vt(e) => e.set_bitrate(bitrate),
            }
        }
    }

    /// Starts capturing `display` with `G` and encoding it.
    /// Returns: the stream description once the capturer and the encoder
    /// work, with the capture thread (it ends with the display and the
    /// encoder let go), or why not (no interactive desktop, no permission,
    /// no encoder).
    pub fn start<G: Grab>(
        tx: mpsc::Sender<Encoded>,
        key: Arc<AtomicBool>,
        stop: Arc<AtomicBool>,
        targets: Arc<Targets>,
        display: Display,
        choice: Choice,
    ) -> Result<(VideoStart, std::thread::JoinHandle<()>), String> {
        let (ready_tx, ready) = std::sync::mpsc::channel();
        let thread = std::thread::spawn(move || {
            let opened = (|| {
                let (capturer, dw, dh) = G::open(display)?;
                let (w, h) = fit(dw, dh, MAX_SIZE.0, MAX_SIZE.1);
                let encoder = Encoder::open(choice, w, h)?;
                Ok::<_, String>((capturer, encoder, w, h))
            })();
            let colour = if choice.i444 { " 4:4:4" } else { "" };
            let (capturer, mut encoder, w, h) = match opened {
                Ok(v) => v,
                Err(e) => {
                    log::info!("video: {}{colour} did not open: {e}", choice.codec);
                    let _ = ready_tx.send(Err(e));
                    return;
                }
            };
            let (name, hardware) = encoder.kind();
            log::info!(
                "video: {}{colour} at {w}x{h} through {name} in {}",
                choice.codec,
                if hardware { "hardware" } else { "software" }
            );
            let _ = ready_tx.send(Ok(VideoStart {
                codec: choice.codec.into(),
                width: w,
                height: h,
                i444: encoder.i444(),
                encoder: name.into(),
                hardware,
            }));
            let t0 = Instant::now();
            let mut bitrate = BITRATE;
            // Frames the encoder took without giving output yet: it holds a
            // few before its first output, so a still screen repeats the last
            // picture until they come out.
            let mut held = 0u32;
            // A key frame was asked for and has not come out yet: the last
            // picture is encoded again on a still screen until it does, and
            // the encoder is asked again every KEY_FORCE_AGAIN (an
            // asynchronous encoder may answer with frames it held).
            let mut key_pending = false;
            let mut forced: Option<Instant> = None;
            // Since when there has been no picture (the stream's start, a
            // lost display), to wake a dark display, and when it was woken.
            let mut dark_since = Some(t0);
            // Since when the system has reported the display off: a dark
            // display gives nothing, as a still screen does, so a capture
            // that had pictures would never wake it (regression).
            let mut off_since: Option<Instant> = None;
            let mut woke: Option<Instant> = None;
            // When the input desktop was last compared with this thread's.
            let mut desk_checked = t0;
            // The encoder failed once and was opened again.
            let mut reopened = false;
            // Why the display last failed to open again, logged once.
            let mut open_failed = String::new();
            // None while the display is lost: the old capturer is released
            // before a new one is made. The stream keeps its size; a new
            // display mode is scaled into it.
            let mut capturer = Some(capturer);
            while !stop.load(Ordering::Relaxed) {
                off_since = if crate::power::display_off() {
                    off_since.or(Some(Instant::now()))
                } else {
                    None
                };
                if crate::power::wake_due(dark_since.or(off_since), woke, Instant::now()) {
                    woke = Some(Instant::now());
                    log::info!("capture: no picture, or the display is off: waking it");
                    crate::power::wake_display();
                }
                let Some(cap) = capturer.as_mut() else {
                    nap(REOPEN_AFTER, &stop);
                    if stop.load(Ordering::Relaxed) {
                        continue;
                    }
                    // A thread that cannot move to the input desktop (one
                    // that came to own a window or a hook) never captures
                    // it: it ends, and the session opens the stream again
                    // on a fresh thread.
                    if crate::agent::follow_input_desktop() == crate::agent::Desk::Failed {
                        log::info!("capture: ends to start again on a fresh thread");
                        return;
                    }
                    capturer = match G::open(display) {
                        Ok((c, _, _)) => {
                            open_failed.clear();
                            Some(c)
                        }
                        Err(why) => {
                            if why != open_failed {
                                log::info!("capture: the display does not open again: {why}");
                                open_failed = why;
                            }
                            None
                        }
                    };
                    continue;
                };
                // The lock screen, the sign-in screen or a UAC prompt took
                // the input desktop, which a capture does not always report
                // (GDI keeps copying the old desktop's black): followed as a
                // lost display, whatever the capture gives.
                if desk_checked.elapsed() >= DESK_CHECK {
                    desk_checked = Instant::now();
                    if crate::agent::input_desktop_moved() {
                        log::info!("capture: the input desktop moved");
                        capturer = None;
                        dark_since.get_or_insert_with(Instant::now);
                        continue;
                    }
                }
                // Room for the frame first: a picture is taken only when the
                // session can send it, so what goes out is the newest picture
                // rather than one that waited behind the network.
                if targets.room.load(Ordering::Relaxed) == 0 {
                    std::thread::sleep(ROOM_WAIT);
                    continue;
                }
                let permit = match tx.try_reserve() {
                    Ok(permit) => permit,
                    Err(mpsc::error::TrySendError::Full(())) => {
                        std::thread::sleep(ROOM_WAIT);
                        continue;
                    }
                    Err(mpsc::error::TrySendError::Closed(())) => return,
                };
                let begun = Instant::now();
                let period = Duration::from_secs(1) / targets.fps.load(Ordering::Relaxed).max(1);
                let wanted_rate = targets.bitrate.load(Ordering::Relaxed);
                if wanted_rate != bitrate {
                    bitrate = wanted_rate;
                    encoder.set_bitrate(bitrate);
                }
                key_pending |= key.swap(false, Ordering::Relaxed);
                let force = key_pending && forced.is_none_or(|t| t.elapsed() >= KEY_FORCE_AGAIN);
                // Unchanged screens time out: nothing is encoded or sent,
                // unless a key frame was asked for.
                let mut taken = None;
                let got = cap.frame(
                    period.min(FRAME_WAIT),
                    &mut |bgra, width, height, stride| {
                        taken = Some(Instant::now());
                        encoder.picture(bgra, width, height, stride)
                    },
                );
                match got {
                    // The encode time counts from the capture's own copy.
                    Got::Picture => {
                        taken = cap.taken().or(taken);
                        dark_since = None;
                    }
                    Got::Nothing if (key_pending || held > 0) && encoder.has_picture() => {}
                    Got::Lost => {
                        capturer = None;
                        dark_since.get_or_insert_with(Instant::now);
                        continue;
                    }
                    Got::Nothing => {
                        // GDI answers at once instead of waiting the timeout.
                        if let Some(rest) = period.checked_sub(begun.elapsed()) {
                            nap(rest, &stop);
                        }
                        continue;
                    }
                }
                let taken = taken.unwrap_or_else(Instant::now);
                let units = match encoder.encode(force) {
                    Ok(units) => units,
                    Err(e) => {
                        // Once a stream the encoder is opened again (with
                        // the display, for a first picture); a second
                        // failure ends the stream, which the session opens
                        // again.
                        log::info!("video: {}{colour} failed: {e}", choice.codec);
                        drop(permit);
                        if reopened {
                            return;
                        }
                        reopened = true;
                        match Encoder::open(choice, w, h) {
                            Ok(e) => encoder = e,
                            Err(e) => {
                                log::info!(
                                    "video: {}{colour} did not open again: {e}",
                                    choice.codec
                                );
                                return;
                            }
                        }
                        (key_pending, forced, held) = (true, None, 0);
                        capturer = None;
                        continue;
                    }
                };
                if force {
                    forced = Some(Instant::now());
                }
                if units.iter().any(|(_, is_key)| *is_key) {
                    (key_pending, forced) = (false, None);
                }
                held = if units.is_empty() {
                    (held + 1).min(FPS)
                } else {
                    0
                };
                let pts = t0.elapsed().as_micros() as u64;
                let took_us = taken.elapsed().as_micros().min(u32::MAX as u128) as u32;
                let mut frames = units.into_iter().map(|(data, is_key)| Encoded {
                    flags: if is_key { media::KEY_FRAME } else { 0 },
                    pts,
                    data,
                    took_us,
                });
                match frames.next() {
                    Some(f) => permit.send(f),
                    None => drop(permit),
                }
                // More than one unit (an encoder that held frames): the rest
                // wait for the session in turn.
                for f in frames {
                    if tx.blocking_send(f).is_err() {
                        return;
                    }
                }
                if let Some(rest) = period.checked_sub(begun.elapsed()) {
                    nap(rest, &stop);
                }
            }
        });
        ready
            .recv()
            .unwrap_or_else(|_| Err("the capture thread ended".into()))
            .map(|start| (start, thread))
    }
}

#[cfg(windows)]
mod screen {
    //! A display through RustDesk's DXGI capturer. Where desktop
    //! duplication cannot open a display (no GPU output, some virtual
    //! machines and remote sessions), the capturer reads it through GDI
    //! instead and reports an unchanged screen the same way.
    use super::*;
    use rustdesk_dxgi::{Capturer, Displays, Frame};

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

    /// How often a capture that fell back to GDI tries desktop duplication
    /// again (it fails for a moment around a UAC prompt or the lock screen).
    const GDI_RETRY: Duration = Duration::from_secs(2);
    /// How long a new desktop duplication may give no picture before GDI
    /// copies one: duplication hands a frame over only once something is
    /// drawn, and a still lock or sign-in screen may draw nothing for long.
    const FIRST_PICTURE: Duration = Duration::from_secs(1);

    /// A display through desktop duplication (or GDI), scaled on the GPU to
    /// the stream's size.
    pub struct Dxgi {
        cap: Capturer,
        /// The display, by its place on the virtual desktop.
        want: Display,
        /// The stream's size, which the GPU scales to.
        size: (usize, usize),
        /// When desktop duplication was last tried while on GDI.
        tried: Instant,
        /// When the capture opened, and whether it gave a picture since.
        opened: Instant,
        pictured: bool,
    }

    impl Grab for Dxgi {
        /// Opens a display by its place on the virtual desktop, on the
        /// input desktop (the agent follows it to a UAC prompt or the lock
        /// screen), and asks the GPU for pictures of the stream's size: a
        /// 4K desktop crosses to the CPU as 1080p, as ScreenCaptureKit
        /// hands a Mac host its pictures. GDI, a desktop image kept in
        /// system memory and a GPU the pass fails on deliver the display's
        /// size, which the encoder scales.
        fn open(want: Display) -> Result<(Self, u32, u32), String> {
            if crate::agent::follow_input_desktop() == crate::agent::Desk::Failed {
                return Err("this thread cannot move to the input desktop".into());
            }
            let display = attached()
                .into_iter()
                .find(|d| d.origin() == (want.x, want.y))
                .ok_or("the display is gone")?;
            let (dw, dh) = (display.width() as u32, display.height() as u32);
            let mut cap = Capturer::new(display).map_err(|e| format!("capturer: {e}"))?;
            // The input desktop moved while the capture opened (around the
            // lock screen DXGI refuses, and GDI would copy the old desktop):
            // opened again on the new one.
            if crate::agent::input_desktop_moved() {
                return Err("the input desktop moved while the display opened".into());
            }
            let (w, h) = fit(dw, dh, MAX_SIZE.0, MAX_SIZE.1);
            let size = (w as usize, h as usize);
            cap.scale_to(size.0, size.1);
            log::info!(
                "capture: a {dw}x{dh} display through {}",
                if cap.is_gdi() {
                    "GDI"
                } else {
                    "desktop duplication"
                }
            );
            let tried = Instant::now();
            Ok((
                Dxgi {
                    cap,
                    want,
                    size,
                    tried,
                    opened: tried,
                    pictured: false,
                },
                w,
                h,
            ))
        }

        fn frame(&mut self, timeout: Duration, take: &mut Take<'_>) -> Got {
            if self.cap.is_gdi() && self.tried.elapsed() >= GDI_RETRY {
                self.tried = Instant::now();
                // Duplication opens only on the input desktop: a capture
                // whose thread had to move there is opened again, never
                // kept on what it copied before (regression: GDI made on the
                // user's desktop went on copying it under the lock screen).
                if self.duplicate_again() {
                    return Got::Lost;
                }
            }
            // A still lock or sign-in screen gives a new duplication nothing
            // to hand over: GDI copies a picture, and duplication is tried
            // again GDI_RETRY later.
            if !self.pictured && !self.cap.is_gdi() && self.opened.elapsed() >= FIRST_PICTURE {
                self.pictured = true;
                if self.cap.set_gdi() {
                    self.tried = Instant::now();
                    log::info!("capture: no picture yet: GDI copies one");
                }
            }
            match self.cap.frame(timeout.as_millis() as u32) {
                Ok(Frame::PixelBuffer(pb)) => {
                    self.pictured = true;
                    take(pb.data, pb.width, pb.height, pb.stride);
                    Got::Picture
                }
                Err(e) => missed(e.kind()),
                Ok(Frame::Texture(_)) => Got::Nothing,
            }
        }

        /// When desktop duplication handed the last frame over, or GDI began
        /// copying it: the copy to system memory counts in the encode time.
        fn taken(&self) -> Option<Instant> {
            self.cap.acquired()
        }
    }

    impl Dxgi {
        /// Switches a capture that fell back to GDI to desktop duplication
        /// again where it opens now (regression: a moment's failure, as
        /// around a UAC prompt, kept the stream on GDI to its end). A display
        /// that DXGI does not list (no GPU output, some virtual machines) is
        /// left on GDI without trying. The thread follows the input desktop
        /// first: duplication opens only on the desktop that has the input.
        /// Returns: whether the thread moved to another desktop, where this
        /// capture must open again.
        fn duplicate_again(&mut self) -> bool {
            match crate::agent::follow_input_desktop() {
                crate::agent::Desk::Failed => return false,
                crate::agent::Desk::Switched => return true,
                crate::agent::Desk::Same => {}
            }
            let Some(display) = Displays::new().ok().and_then(|mut all| {
                all.find(|d| d.attached_to_desktop() && d.origin() == (self.want.x, self.want.y))
            }) else {
                return false;
            };
            if let Ok(mut cap) = Capturer::new(display)
                && !cap.is_gdi()
            {
                cap.scale_to(self.size.0, self.size.1);
                self.cap = cap;
                log::info!("capture: desktop duplication is back");
            }
            false
        }
    }

    /// What a frame that did not come means: the wait timing out on a still
    /// screen (TimedOut, as the vendored capturer reports
    /// DXGI_ERROR_WAIT_TIMEOUT) or a frame where only the pointer moved
    /// (WouldBlock) is nothing new; anything else lost the display (access
    /// lost to a mode change, the lock screen or a UAC prompt, the session
    /// gone, no duplication).
    /// Notes: RustDesk's own wrapper turns the timeout into WouldBlock; the
    /// vendored module alone does not (regression: every still frame period
    /// reopened the display after half a second).
    pub(super) fn missed(kind: std::io::ErrorKind) -> Got {
        match kind {
            std::io::ErrorKind::WouldBlock | std::io::ErrorKind::TimedOut => Got::Nothing,
            _ => Got::Lost,
        }
    }
}

#[cfg(all(test, windows))]
mod screen_tests {
    use super::*;

    /// Desktop duplication's wait timing out on a still screen is nothing
    /// new, never a lost display (regression: every still frame period tore
    /// the capture down and opened it again half a second later).
    #[test]
    fn a_still_screen_is_not_a_lost_display() {
        use std::io::ErrorKind::*;
        for (kind, want) in [
            (TimedOut, Got::Nothing),
            (WouldBlock, Got::Nothing),
            (ConnectionReset, Got::Lost),
            (ConnectionAborted, Got::Lost),
            (PermissionDenied, Got::Lost),
            (InvalidData, Got::Lost),
            (AddrNotAvailable, Got::Lost),
            (Interrupted, Got::Lost),
            (Other, Got::Lost),
        ] {
            assert_eq!(screen::missed(kind), want, "{kind:?}");
        }
    }

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
            let (tx, mut rx) = mpsc::channel(1);
            let start = match capture::start::<screen::Dxgi>(
                tx,
                key,
                stop.clone(),
                Arc::new(Targets::default()),
                display,
                choice,
            ) {
                Ok((s, _)) => s,
                Err(e) => return eprintln!("skipped: {e}"),
            };
            eprintln!("stream {start:?}");
            assert_eq!(start.i444, choice.i444);
            let first = rx.blocking_recv().expect("a first frame");
            assert_eq!(first.flags & media::KEY_FRAME, media::KEY_FRAME);
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
    fn tried(decode: &[&str], i444: &[&str], full_colour: bool, hevc_first: bool) -> Vec<String> {
        let names = |v: &[&str]| v.iter().map(|s| s.to_string()).collect::<Vec<_>>();
        choose(&names(decode), &names(i444), full_colour, hevc_first)
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
        // VideoToolbox's H.264 and HEVC go before the software codecs.
        #[cfg(target_os = "macos")]
        let cases: &[Case] = &[
            (
                &all,
                &full,
                false,
                &["h264", "hevc", "vp9-sw", "av1", "vp8"],
            ),
            (
                &all,
                &full,
                true,
                &[
                    "vp9-sw+444",
                    "av1+444",
                    "h264",
                    "hevc",
                    "vp9-sw",
                    "av1",
                    "vp8",
                ],
            ),
            (&["h264"], &[], false, &["h264"]),
            // Media Foundation's VP9 is not a Mac's.
            (&["vp9", "vp8"], &[], false, &["vp8"]),
        ];
        #[cfg(not(any(windows, target_os = "macos")))]
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
                tried(decode, i444, *want, false),
                expected.to_vec(),
                "{decode:?} {i444:?} {want}"
            );
        }
        // A free hardware HEVC encoder puts HEVC before H.264 (full colour
        // stays first).
        #[cfg(target_os = "macos")]
        let cases: &[Case] = &[
            (
                &all,
                &full,
                false,
                &["hevc", "h264", "vp9-sw", "av1", "vp8"],
            ),
            (
                &all,
                &full,
                true,
                &[
                    "vp9-sw+444",
                    "av1+444",
                    "hevc",
                    "h264",
                    "vp9-sw",
                    "av1",
                    "vp8",
                ],
            ),
            (&["h264", "vp8"], &[], false, &["h264", "vp8"]),
        ];
        #[cfg(windows)]
        let cases: &[Case] = &[
            (
                &all,
                &full,
                false,
                &["hevc", "h264", "vp9", "vp9-sw", "av1", "vp8"],
            ),
            (
                &all,
                &full,
                true,
                &[
                    "vp9-sw+444",
                    "av1+444",
                    "hevc",
                    "h264",
                    "vp9",
                    "vp9-sw",
                    "av1",
                    "vp8",
                ],
            ),
            (&["h264", "vp9"], &[], false, &["h264", "vp9"]),
            (&[], &[], false, &["h264"]),
        ];
        for (decode, i444, want, expected) in cases {
            assert_eq!(
                tried(decode, i444, *want, true),
                expected.to_vec(),
                "{decode:?} {i444:?} {want}"
            );
        }
    }

    #[test]
    fn the_viewer_s_codec_goes_first() {
        let names = |v: &[Choice]| {
            v.iter()
                .map(|c| format!("{}{}", c.codec, if c.i444 { "+444" } else { "" }))
                .collect::<Vec<_>>()
        };
        let c = |codec, i444| Choice { codec, i444 };
        let order = vec![
            c("vp9-sw", true),
            c("av1", true),
            c("h264", false),
            c("vp9-sw", false),
            c("av1", false),
        ];
        let cases: &[(Option<&str>, &[&str])] = &[
            // Auto keeps the host's order.
            (None, &["vp9-sw+444", "av1+444", "h264", "vp9-sw", "av1"]),
            // A codec without full colour goes first, as it is.
            (
                Some("h264"),
                &["h264", "vp9-sw+444", "av1+444", "vp9-sw", "av1"],
            ),
            // A codec with full colour: in full colour first, then plain.
            (
                Some("av1"),
                &["av1+444", "av1", "vp9-sw+444", "h264", "vp9-sw"],
            ),
            // A name the host does not send changes nothing.
            (
                Some("h266"),
                &["vp9-sw+444", "av1+444", "h264", "vp9-sw", "av1"],
            ),
        ];
        for (codec, want) in cases {
            assert_eq!(names(&prefer(order.clone(), *codec)), *want, "{codec:?}");
        }
        // The offer: what both sides do, in the host's order.
        let decode: Vec<String> = ["vp8", "av1", "h264"].map(String::from).to_vec();
        assert_eq!(
            offer(&decode, &["h264", "vp9-sw", "av1", "vp8"]),
            ["h264", "av1", "vp8"]
        );
        assert_eq!(offer(&decode, &["vp9-sw"]), Vec::<&str>::new());
        assert!(encodable().contains(&"vp9-sw"));
    }

    #[test]
    fn this_build_decodes_every_software_codec() {
        let (decode, i444) = decodable();
        for c in ["vp9-sw", "av1", "vp8"] {
            assert!(decode.iter().any(|d| d == c), "{c}");
        }
        assert_eq!(i444, ["vp9-sw", "av1"]);
    }

    /// Whether a test capturer (`Fake`) is open.
    #[cfg(any(windows, target_os = "macos"))]
    static FAKE_OPEN: AtomicBool = AtomicBool::new(false);

    /// A capturer for the tests: a still grey picture, let go slowly as a
    /// GPU capturer is.
    #[cfg(any(windows, target_os = "macos"))]
    struct Fake;

    #[cfg(any(windows, target_os = "macos"))]
    impl Grab for Fake {
        fn open(_: Display) -> Result<(Self, u32, u32), String> {
            FAKE_OPEN.store(true, Ordering::SeqCst);
            Ok((Fake, 64, 48))
        }

        fn frame(&mut self, timeout: Duration, take: &mut Take<'_>) -> Got {
            std::thread::sleep(timeout.min(Duration::from_millis(5)));
            take(&[128u8; 64 * 48 * 4], 64, 48, 64 * 4);
            Got::Picture
        }
    }

    #[cfg(any(windows, target_os = "macos"))]
    impl Drop for Fake {
        fn drop(&mut self) {
            std::thread::sleep(Duration::from_millis(100));
            FAKE_OPEN.store(false, Ordering::SeqCst);
        }
    }

    /// A dropped source's capture lets go of its display and its encoder
    /// before the next source opens (regression: the next one opened at once
    /// and found the one hardware encoder still taken).
    #[cfg(any(windows, target_os = "macos"))]
    #[test]
    fn the_next_source_waits_for_the_last_one_to_let_go() {
        let (tx, frames) = mpsc::channel(1);
        let key = Arc::new(AtomicBool::new(true));
        let stop = Arc::new(AtomicBool::new(false));
        let targets = Arc::new(Targets::default());
        let display = Display {
            x: 0,
            y: 0,
            width: 64,
            height: 48,
            primary: true,
        };
        let choice = Choice {
            codec: "vp8",
            i444: false,
        };
        let (start, done) = capture::start::<Fake>(
            tx,
            key.clone(),
            stop.clone(),
            targets.clone(),
            display,
            choice,
        )
        .expect("VP8 opens");
        let source = Source {
            start,
            display: Some(display),
            frames,
            key,
            stop,
            targets,
            done: Some(done),
            _agent: None,
        };
        assert!(FAKE_OPEN.load(Ordering::SeqCst));
        drop(source);
        wait_stopped();
        assert!(
            !FAKE_OPEN.load(Ordering::SeqCst),
            "the capture still held its display"
        );
    }

    /// A capturer for the tests whose screen stays still after its first
    /// picture.
    #[cfg(any(windows, target_os = "macos"))]
    struct Still(bool);

    #[cfg(any(windows, target_os = "macos"))]
    impl Grab for Still {
        fn open(_: Display) -> Result<(Self, u32, u32), String> {
            Ok((Still(false), 64, 48))
        }

        fn frame(&mut self, timeout: Duration, take: &mut Take<'_>) -> Got {
            if std::mem::replace(&mut self.0, true) {
                std::thread::sleep(timeout);
                return Got::Nothing;
            }
            take(&[90u8; 64 * 48 * 4], 64, 48, 64 * 4);
            Got::Picture
        }
    }

    /// Starts a VP8 stream from a test capturer.
    #[cfg(any(windows, target_os = "macos"))]
    fn test_stream<G: Grab>(
        targets: Arc<Targets>,
    ) -> (mpsc::Receiver<Encoded>, Arc<AtomicBool>, Arc<AtomicBool>) {
        let (tx, frames) = mpsc::channel(1);
        let key = Arc::new(AtomicBool::new(true));
        let stop = Arc::new(AtomicBool::new(false));
        let display = Display {
            x: 0,
            y: 0,
            width: 64,
            height: 48,
            primary: true,
        };
        let choice = Choice {
            codec: "vp8",
            i444: false,
        };
        capture::start::<G>(tx, key.clone(), stop.clone(), targets, display, choice)
            .expect("VP8 opens");
        (frames, key, stop)
    }

    /// The next frame within `wait`.
    #[cfg(any(windows, target_os = "macos"))]
    fn next_frame(frames: &mut mpsc::Receiver<Encoded>, wait: Duration) -> Option<Encoded> {
        let until = Instant::now() + wait;
        while Instant::now() < until {
            match frames.try_recv() {
                Ok(f) => return Some(f),
                Err(mpsc::error::TryRecvError::Empty) => {
                    std::thread::sleep(Duration::from_millis(5))
                }
                Err(mpsc::error::TryRecvError::Disconnected) => return None,
            }
        }
        None
    }

    /// The capture takes a picture only while the session has room for its
    /// frame: none while the controller's acknowledgements are behind, the
    /// newest one once there is room again.
    #[cfg(any(windows, target_os = "macos"))]
    #[test]
    fn the_capture_takes_pictures_only_with_room() {
        let targets = Arc::new(Targets::default());
        targets.room.store(0, Ordering::Relaxed);
        let (mut frames, _, stop) = test_stream::<Fake>(targets.clone());
        assert!(
            next_frame(&mut frames, Duration::from_millis(300)).is_none(),
            "a frame without room"
        );
        targets.room.store(1, Ordering::Relaxed);
        assert!(next_frame(&mut frames, Duration::from_secs(2)).is_some());
        stop.store(true, Ordering::Relaxed);
    }

    /// A key frame asked for on a still screen comes out: the last picture
    /// is encoded again for it (regression: a still screen never answered,
    /// and a viewer that lost a frame waited for a change on the host).
    #[cfg(any(windows, target_os = "macos"))]
    #[test]
    fn a_key_frame_asked_for_comes_out_on_a_still_screen() {
        let (mut frames, key, stop) = test_stream::<Still>(Arc::new(Targets::default()));
        let first = next_frame(&mut frames, Duration::from_secs(2)).expect("a first frame");
        assert_eq!(first.flags & media::KEY_FRAME, media::KEY_FRAME);
        while next_frame(&mut frames, Duration::from_millis(200)).is_some() {}
        key.store(true, Ordering::Relaxed);
        let again = next_frame(&mut frames, Duration::from_secs(2)).expect("an answer");
        assert_eq!(again.flags & media::KEY_FRAME, media::KEY_FRAME);
        stop.store(true, Ordering::Relaxed);
    }

    #[test]
    fn sizes_fit_and_stay_even() {
        assert_eq!(fit(3840, 2160, 1920, 1080), (1920, 1080));
        assert_eq!(fit(1280, 720, 1920, 1080), (1280, 720));
        assert_eq!(fit(2560, 1600, 1920, 1080), (1728, 1080));
        assert_eq!(fit(1366, 769, 1920, 1080), (1366, 768));
    }

    /// The conversions as they were first written, a pixel at a time: the
    /// faster ones must give the same bytes.
    mod reference {
        use super::super::{chroma_of, luma};

        pub fn bgra_to_nv12(
            bgra: &[u8],
            w: usize,
            h: usize,
            stride: usize,
            ow: usize,
            oh: usize,
        ) -> Vec<u8> {
            let mut nv12 = vec![0u8; ow * oh * 3 / 2];
            let src = |x: usize, y: usize| {
                let i = (y * h / oh) * stride + (x * w / ow) * 4;
                (bgra[i + 2] as i32, bgra[i + 1] as i32, bgra[i] as i32)
            };
            for y in 0..oh {
                for x in 0..ow {
                    let (r, g, b) = src(x, y);
                    nv12[y * ow + x] = luma(r, g, b);
                }
            }
            for y in 0..oh / 2 {
                for x in 0..ow / 2 {
                    let (mut r, mut g, mut b) = (0, 0, 0);
                    for (dx, dy) in [(0, 0), (1, 0), (0, 1), (1, 1)] {
                        let p = src(x * 2 + dx, y * 2 + dy);
                        (r, g, b) = (r + p.0, g + p.1, b + p.2);
                    }
                    let i = ow * oh + y * ow + x * 2;
                    (nv12[i], nv12[i + 1]) = chroma_of(r / 4, g / 4, b / 4);
                }
            }
            nv12
        }

        pub fn rgb(y: u8, u: u8, v: u8) -> [u8; 4] {
            let clip = |v: i32| v.clamp(0, 255) as u8;
            let (c, d, e) = (y as i32 - 16, u as i32 - 128, v as i32 - 128);
            [
                clip((298 * c + 409 * e + 128) >> 8),
                clip((298 * c - 100 * d - 208 * e + 128) >> 8),
                clip((298 * c + 516 * d + 128) >> 8),
                255,
            ]
        }

        pub fn nv12_to_rgba(
            nv12: &[u8],
            w: usize,
            h: usize,
            stride: usize,
            rows: usize,
        ) -> Vec<u8> {
            let chroma = &nv12[stride * rows..];
            let mut rgba = vec![0u8; w * h * 4];
            for y in 0..h {
                for x in 0..w {
                    let ci = (y / 2) * stride + (x & !1);
                    let o = (y * w + x) * 4;
                    rgba[o..o + 4].copy_from_slice(&rgb(
                        nv12[y * stride + x],
                        chroma[ci],
                        chroma[ci + 1],
                    ));
                }
            }
            rgba
        }
    }

    /// Bytes that look like nothing in particular, the same every run.
    fn noise(len: usize, seed: u32) -> Vec<u8> {
        let mut x = seed.wrapping_mul(2_654_435_761).max(1);
        (0..len)
            .map(|_| {
                x ^= x << 13;
                x ^= x >> 17;
                x ^= x << 5;
                x as u8
            })
            .collect()
    }

    #[test]
    fn faster_conversions_give_the_same_bytes() {
        // (source width, height, stride padding; output width, height):
        // unscaled, halved, an odd scale, and a padded source.
        let sizes = [
            (64, 36, 0, 64, 36),
            (64, 36, 0, 32, 18),
            (97, 55, 0, 50, 28),
            (320, 180, 16, 214, 120),
            (40, 30, 8, 40, 30),
        ];
        for (n, (w, h, pad, ow, oh)) in sizes.into_iter().enumerate() {
            let stride = w * 4 + pad;
            let bgra = noise(stride * h, n as u32 + 1);
            let mut nv12 = Vec::new();
            bgra_to_nv12(&bgra, w, h, stride, ow, oh, &mut nv12);
            assert_eq!(
                nv12,
                reference::bgra_to_nv12(&bgra, w, h, stride, ow, oh),
                "{w}×{h} → {ow}×{oh}"
            );
            // And back, with a padded stride and rows as decoders give.
            let (stride, rows) = (ow + 32, oh + 8);
            let mut padded = vec![0u8; stride * rows * 3 / 2];
            for y in 0..oh {
                padded[y * stride..][..ow].copy_from_slice(&nv12[y * ow..][..ow]);
            }
            for y in 0..oh / 2 {
                padded[stride * rows + y * stride..][..ow]
                    .copy_from_slice(&nv12[ow * oh + y * ow..][..ow]);
            }
            let mut rgba = Vec::new();
            nv12_to_rgba(&padded, ow, oh, stride, rows, &mut rgba);
            assert_eq!(
                rgba,
                reference::nv12_to_rgba(&padded, ow, oh, stride, rows),
                "{ow}×{oh} back"
            );
        }
        // Planar YUV, 4:2:0 and 4:4:4, against the per-pixel formula.
        for i444 in [false, true] {
            let (w, h) = (34, 20);
            let (cw, ch) = if i444 { (w, h) } else { (w / 2, h / 2) };
            let (y, u, v) = (noise(w * h, 7), noise(cw * ch, 8), noise(cw * ch, 9));
            let mut rgba = Vec::new();
            planar_to_rgba([&y, &u, &v], [w, cw, cw], w, h, i444, &mut rgba);
            for (i, px) in rgba.chunks(4).enumerate() {
                let (x, row) = (i % w, i / w);
                let c = if i444 {
                    row * cw + x
                } else {
                    row / 2 * cw + x / 2
                };
                assert_eq!(
                    px,
                    reference::rgb(y[row * w + x], u[c], v[c]),
                    "pixel {x},{row}"
                );
            }
        }
    }

    /// How long the conversions take for a 1080p stream (run with
    /// `--ignored --nocapture`, in release).
    #[test]
    #[ignore]
    fn conversion_speed_at_1080p() {
        let time = |name: &str, f: &mut dyn FnMut()| {
            f();
            let t = Instant::now();
            for _ in 0..20 {
                f();
            }
            eprintln!(
                "{name}: {:.2} ms",
                t.elapsed().as_secs_f64() * 1000.0 / 20.0
            );
        };
        let bgra = noise(1920 * 1080 * 4, 1);
        let big = noise(3840 * 2160 * 4, 2);
        let mut nv12 = Vec::new();
        time("BGRA → NV12, 1080p, a pixel at a time", &mut || {
            nv12 = reference::bgra_to_nv12(&bgra, 1920, 1080, 1920 * 4, 1920, 1080)
        });
        time("BGRA → NV12, 1080p", &mut || {
            bgra_to_nv12(&bgra, 1920, 1080, 1920 * 4, 1920, 1080, &mut nv12)
        });
        time("BGRA → NV12, 4K scaled to 1080p", &mut || {
            bgra_to_nv12(&big, 3840, 2160, 3840 * 4, 1920, 1080, &mut nv12)
        });
        let mut rgba = Vec::new();
        time("NV12 → RGBA, 1080p, a pixel at a time", &mut || {
            rgba = reference::nv12_to_rgba(&nv12, 1920, 1080, 1920, 1080)
        });
        time("NV12 → RGBA, 1080p", &mut || {
            nv12_to_rgba(&nv12, 1920, 1080, 1920, 1080, &mut rgba)
        });
        let (y, u, v) = (
            noise(1920 * 1080, 3),
            noise(960 * 540, 4),
            noise(960 * 540, 5),
        );
        time("I420 → RGBA, 1080p", &mut || {
            planar_to_rgba([&y, &u, &v], [1920, 960, 960], 1920, 1080, false, &mut rgba)
        });
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
