//! H.264 and HEVC through the GPU on Linux hosts (VA-API): H.264 through
//! `cros-codecs`' stateless encoder on the driver's own surfaces, each
//! frame's picture copied into a new one; HEVC on libva directly (`hevc`)
//! (doc/en-us/computer-use.md, "Linux").
//!
//! Only encoding: `cros-codecs` decodes into GBM buffers, and Mesa allocates
//! no NV12 buffers through GBM, so a Linux controller decodes H.264 and HEVC
//! through the system's FFmpeg (`ff`).

pub mod hevc;

use std::rc::Rc;
use std::sync::{Mutex, OnceLock};

use cros_codecs::backend::vaapi::encoder::VaapiBackend;
use cros_codecs::codec::h264::parser::{Level, Profile};
use cros_codecs::decoder::BlockingMode;
use cros_codecs::encoder::h264::EncoderConfig;
use cros_codecs::encoder::stateless::h264::StatelessEncoder;
use cros_codecs::encoder::{
    FrameMetadata, PredictionStructure, RateControl, Tunings, VideoEncoder,
};
use cros_codecs::libva::{self, Display, Surface, VAEntrypoint, VAProfile};
use cros_codecs::video_frame::{ReadMapping, VideoFrame, WriteMapping};
use cros_codecs::{Fourcc, FrameLayout, Resolution};

use crate::annexb::Syntax;
use crate::video::Picture;

/// Frames between key frames: one every 4 seconds, as the other encoders.
const KEY_EVERY: u16 = 4 * crate::video::FPS as u16;
/// The coarsest quantizer the rate control may use. Uncapped, the driver
/// (Intel's iHD) squeezed a busy screen's key frame into a few frames' bits
/// and sent noise; capped, a key frame goes over the bitrate for a moment
/// instead, as the other encoders' do.
const MAX_QP: u32 = 38;

/// The H.264 profile and entry point this host's driver encodes with.
#[derive(Clone, Copy)]
struct Support {
    profile: Profile,
    /// The profile's name, for the log.
    name: &'static str,
    low_power: bool,
}

/// What the driver offers for H.264 encoding, probed once per process:
/// High, else Main, else Constrained Baseline; the full encoder where the
/// driver has it, else the low-power one.
fn support() -> Option<Support> {
    static SUPPORT: OnceLock<Option<Support>> = OnceLock::new();
    *SUPPORT.get_or_init(|| {
        let display = Display::open()?;
        let found = [
            (VAProfile::VAProfileH264High, Profile::High, "High"),
            (VAProfile::VAProfileH264Main, Profile::Main, "Main"),
            (
                VAProfile::VAProfileH264ConstrainedBaseline,
                Profile::Baseline,
                "Constrained Baseline",
            ),
        ]
        .into_iter()
        .find_map(|(va, profile, name)| {
            let entries = display.query_config_entrypoints(va).ok()?;
            let full = entries.contains(&VAEntrypoint::VAEntrypointEncSlice);
            let low = entries.contains(&VAEntrypoint::VAEntrypointEncSliceLP);
            (full || low).then_some(Support {
                profile,
                name,
                low_power: !full,
            })
        });
        match found {
            Some(s) => log::info!(
                "va-api: H.264 encoding ({}{})",
                s.name,
                if s.low_power { ", low power" } else { "" }
            ),
            None => log::info!("va-api: no H.264 encoder"),
        }
        found
    })
}

/// Whether this host's GPU encodes H.264 through VA-API.
pub fn h264() -> bool {
    support().is_some()
}

/// The codecs this host's GPU encodes through VA-API, by name.
pub fn encodable() -> Vec<&'static str> {
    let mut list = Vec::new();
    if h264() {
        list.push("h264");
    }
    if hevc::available() {
        list.push("hevc");
    }
    list
}

/// A VA-API encoder for one stream, in either codec.
pub enum Encoder {
    H264(H264),
    Hevc(hevc::Encoder),
}

impl Encoder {
    /// Opens an encoder of `codec` (`h264` or `hevc`) for `width`×`height`
    /// at `fps` and `bitrate` bit/s.
    /// Returns: the encoder, or why the driver refused it.
    pub fn new(
        codec: &str,
        width: u32,
        height: u32,
        fps: u32,
        bitrate: u32,
    ) -> Result<Encoder, String> {
        match codec {
            "h264" => H264::new(width, height, fps, bitrate).map(Encoder::H264),
            "hevc" => hevc::Encoder::new(width, height, fps, bitrate).map(Encoder::Hevc),
            other => Err(format!("no VA-API {other} encoder")),
        }
    }

    /// Takes a captured BGRA picture as the next one to encode.
    pub fn picture(&mut self, bgra: &[u8], width: usize, height: usize, stride: usize) {
        match self {
            Encoder::H264(e) => e.picture(bgra, width, height, stride),
            Encoder::Hevc(e) => e.picture(bgra, width, height, stride),
        }
    }

    /// Whether a picture was taken yet.
    pub fn has_picture(&self) -> bool {
        match self {
            Encoder::H264(e) => e.has_picture(),
            Encoder::Hevc(e) => e.has_picture(),
        }
    }

    /// Encodes the last picture; `key` asks for a key frame.
    pub fn encode(&mut self, key: bool) -> Result<Vec<(Vec<u8>, bool)>, String> {
        match self {
            Encoder::H264(e) => e.encode(key),
            Encoder::Hevc(e) => e.encode(key),
        }
    }

    /// Sets the target bitrate in bit/s.
    pub fn set_bitrate(&mut self, bitrate: u32) {
        match self {
            Encoder::H264(e) => e.set_bitrate(bitrate),
            Encoder::Hevc(e) => e.set_bitrate(bitrate),
        }
    }
}

/// A picture on its way into the encoder: the driver's surface, already
/// holding the converted picture, taken once by `to_native_handle`.
struct Frame {
    size: Resolution,
    surface: Mutex<Option<Surface<()>>>,
}

impl std::fmt::Debug for Frame {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "Frame({}x{})", self.size.width, self.size.height)
    }
}

// SAFETY: a frame, like the encoder, its display and its surfaces, stays on
// the capture thread that made it; `cros-codecs` asks only for the bounds.
unsafe impl Send for Frame {}
// SAFETY: as above; the surface is behind a mutex besides.
unsafe impl Sync for Frame {}

impl VideoFrame for Frame {
    type MemDescriptor = ();
    type NativeHandle = Surface<()>;

    fn fourcc(&self) -> Fourcc {
        Fourcc::from(b"NV12")
    }

    fn resolution(&self) -> Resolution {
        self.size
    }

    fn get_plane_size(&self) -> Vec<usize> {
        let (w, h) = (self.size.width as usize, self.size.height as usize);
        vec![w * h, w * h / 2]
    }

    fn get_plane_pitch(&self) -> Vec<usize> {
        vec![self.size.width as usize; 2]
    }

    fn map<'a>(&'a self) -> Result<Box<dyn ReadMapping<'a> + 'a>, String> {
        Err("a VA-API frame is not mapped".into())
    }

    fn map_mut<'a>(&'a mut self) -> Result<Box<dyn WriteMapping<'a> + 'a>, String> {
        Err("a VA-API frame is not mapped".into())
    }

    fn to_native_handle(&self, _: &Rc<Display>) -> Result<Surface<()>, String> {
        self.surface
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .take()
            .ok_or_else(|| "the frame was already encoded".into())
    }
}

type Inner = StatelessEncoder<Frame, VaapiBackend<(), Surface<()>>>;

/// A VA-API H.264 encoder: low delay (P frames only, one
/// reference), constant bitrate, a key frame every 4 seconds and on request,
/// Annex B out with the parameter sets on each key frame.
pub struct H264 {
    display: Rc<Display>,
    inner: Inner,
    config: EncoderConfig,
    support: Support,
    /// The picture's size and the surfaces' (a multiple of 16 each way).
    size: Resolution,
    coded: Resolution,
    /// The last picture in NV12, `size.width` bytes a row; empty until the
    /// first picture.
    nv12: Vec<u8>,
    /// Frames since the encoder opened: a key frame each KEY_EVERY.
    count: u64,
}

impl H264 {
    /// Opens an encoder for `width`×`height` at `fps` and `bitrate` bit/s.
    /// Returns: the encoder, or why the driver refused it.
    pub fn new(width: u32, height: u32, fps: u32, bitrate: u32) -> Result<H264, String> {
        let support = support().ok_or("no VA-API H.264 encoder")?;
        let display = Display::open().ok_or("no VA-API display")?;
        let size = Resolution { width, height };
        let coded = Resolution {
            width: width.next_multiple_of(16),
            height: height.next_multiple_of(16),
        };
        let config = EncoderConfig {
            resolution: size,
            profile: support.profile,
            level: Level::L4_1,
            pred_structure: PredictionStructure::LowDelay { limit: KEY_EVERY },
            initial_tunings: Tunings {
                rate_control: RateControl::ConstantBitrate(bitrate.into()),
                framerate: fps,
                max_quality: MAX_QP,
                ..Default::default()
            },
        };
        let inner = open(&display, &config, &coded, support)?;
        Ok(H264 {
            display,
            inner,
            config,
            support,
            size,
            coded,
            nv12: Vec::new(),
            count: 0,
        })
    }

    /// Takes a captured BGRA picture as the next one to encode, converted
    /// into NV12 of the stream's size.
    /// Notes: no allocation once the first picture sized the buffer.
    pub fn picture(&mut self, bgra: &[u8], width: usize, height: usize, stride: usize) {
        let (w, h) = (self.size.width as usize, self.size.height as usize);
        self.nv12.resize(w * h * 3 / 2, 0);
        let (y, uv) = self.nv12.split_at_mut(w * h);
        let src = Picture {
            bgra,
            width,
            height,
            stride,
        };
        crate::video::bgra_to_nv12_planes(src, w, h, y, w, uv, w);
    }

    /// Copies the last picture into a new surface of the driver's.
    /// Notes: a surface per frame: the encoder takes each one it is given
    /// and destroys it once encoded.
    fn surface(&self) -> Result<Surface<()>, String> {
        let surface = self
            .display
            .create_surfaces(
                libva::VA_RT_FORMAT_YUV420,
                Some(libva::VA_FOURCC_NV12),
                self.coded.width,
                self.coded.height,
                Some(libva::UsageHint::USAGE_HINT_ENCODER),
                vec![()],
            )
            .map_err(|e| e.to_string())?
            .pop()
            .ok_or("no surface")?;
        {
            let (w, h) = (self.size.width, self.size.height);
            let mut image =
                libva::Image::derive_from(&surface, (w, h)).map_err(|e| e.to_string())?;
            let va = *image.image();
            let data = image.as_mut();
            let (w, h) = (w as usize, h as usize);
            let rows = self.nv12.chunks_exact(w);
            for (i, row) in rows.enumerate() {
                let (plane, r) = if i < h { (0, i) } else { (1, i - h) };
                let at = va.offsets[plane] as usize + r * va.pitches[plane] as usize;
                data[at..at + w].copy_from_slice(row);
            }
        }
        Ok(surface)
    }

    /// Whether a picture was taken yet.
    pub fn has_picture(&self) -> bool {
        !self.nv12.is_empty()
    }

    /// Encodes the last picture; `key` asks for a key frame.
    /// Returns: the frame in Annex B with whether it is a key frame (nothing
    /// before the first picture).
    /// Notes: `cros-codecs` 0.0.6 answers a forced key frame with an I frame
    /// that is not an IDR, which a decoder cannot start from, so a key frame
    /// asked for opens the encoder again (about 9 ms at 1080p).
    pub fn encode(&mut self, key: bool) -> Result<Vec<(Vec<u8>, bool)>, String> {
        if self.nv12.is_empty() {
            return Ok(Vec::new());
        }
        if key && !self.count.is_multiple_of(u64::from(KEY_EVERY)) {
            self.inner = open(&self.display, &self.config, &self.coded, self.support)?;
            self.count = 0;
        }
        let surface = self.surface()?;
        let meta = FrameMetadata {
            timestamp: self.count,
            layout: FrameLayout {
                format: (Fourcc::from(b"NV12"), 0),
                size: self.size,
                planes: Vec::new(),
            },
            force_keyframe: false,
        };
        let frame = Frame {
            size: self.size,
            surface: Mutex::new(Some(surface)),
        };
        self.inner.encode(meta, frame).map_err(|e| e.to_string())?;
        self.count += 1;
        let mut out = Vec::new();
        while let Some(coded) = self.inner.poll().map_err(|e| e.to_string())? {
            let key = crate::annexb::units(&coded.bitstream).any(|u| Syntax::H264.key(u));
            out.push((coded.bitstream, key));
        }
        Ok(out)
    }

    /// Sets the target bitrate in bit/s (from the next frame on).
    pub fn set_bitrate(&mut self, bitrate: u32) {
        let tunings = Tunings {
            rate_control: RateControl::ConstantBitrate(bitrate.into()),
            ..self.config.initial_tunings.clone()
        };
        self.config.initial_tunings = tunings.clone();
        if let Err(e) = self.inner.tune(tunings) {
            log::warn!("va-api: the bitrate did not change: {e}");
        }
    }
}

/// Opens `cros-codecs`' encoder on `display`.
fn open(
    display: &Rc<Display>,
    config: &EncoderConfig,
    coded: &Resolution,
    support: Support,
) -> Result<Inner, String> {
    Inner::new_vaapi(
        Rc::clone(display),
        config.clone(),
        Fourcc::from(b"NV12"),
        *coded,
        support.low_power,
        BlockingMode::Blocking,
    )
    .map_err(|e| format!("{e}"))
}

#[cfg(test)]
mod tests {
    use super::*;

    /// The units' types in an encoded frame.
    fn kinds(frame: &[u8]) -> Vec<u8> {
        crate::annexb::units(frame)
            .map(|u| Syntax::H264.kind(u))
            .collect()
    }

    /// The stream a viewer can join: an IDR with its parameter sets first,
    /// on request and every KEY_EVERY frames; P frames between. Runs where
    /// the GPU encodes H.264 through VA-API (nothing to check elsewhere).
    #[test]
    fn key_frames_are_idrs_with_their_parameter_sets() {
        if !h264() {
            eprintln!("no VA-API H.264 encoder here");
            return;
        }
        let (w, h) = (320usize, 180usize);
        let mut enc = H264::new(w as u32, h as u32, 30, 1_000_000).unwrap();
        assert!(
            enc.encode(false).unwrap().is_empty(),
            "nothing before a picture"
        );
        // As a capture loop does: the session's bitrate before the first frame.
        enc.set_bitrate(4_000_000);
        let mut bgra = vec![0u8; w * h * 4];
        let mut frame = |enc: &mut H264, i: usize, key: bool| {
            for (p, px) in bgra.as_chunks_mut::<4>().0.iter_mut().enumerate() {
                *px = if (p % w + i * 4) % 64 < 32 {
                    [40, 80, 200, 255]
                } else {
                    [220; 4]
                };
            }
            enc.picture(&bgra, w, h, w * 4);
            let out = enc.encode(key).unwrap();
            assert_eq!(out.len(), 1, "one frame out per frame in");
            out.into_iter().next().unwrap()
        };
        let (first, key) = frame(&mut enc, 0, false);
        assert!(key);
        assert_eq!(kinds(&first)[..3], [7, 8, 5], "SPS, PPS, IDR");
        let (second, key) = frame(&mut enc, 1, false);
        assert!(!key);
        assert_eq!(kinds(&second), [1]);
        let (asked, key) = frame(&mut enc, 2, true);
        assert!(key);
        assert_eq!(kinds(&asked)[..3], [7, 8, 5], "a key frame asked for");
        // A still screen: the same picture again, as a P frame.
        let (again, key) = enc.encode(false).unwrap().into_iter().next().unwrap();
        assert!(!key);
        assert_eq!(kinds(&again), [1]);
        enc.set_bitrate(500_000);
        // Frames 2 and 3 were the encoder's first two since it opened again.
        let keys: Vec<usize> = (4..=2 * KEY_EVERY as usize + 3)
            .filter(|&i| frame(&mut enc, i, false).1)
            .collect();
        assert_eq!(keys, [KEY_EVERY as usize + 2, 2 * KEY_EVERY as usize + 2]);
    }
}
