//! Software video codecs (doc/en-us/computer-use.md, "Video"): VP8 and VP9
//! through libvpx and AV1 through libaom, by RustDesk's wrappers
//! (`core/third_party/rustdesk-codec`), linked statically into the core on
//! every platform. They need no GPU or OS codec, so every host can send and
//! every controller can show them; VP9 and AV1 also carry full colour
//! (4:4:4). Pixels cross as planar YUV (I420 or I444, BT.601 limited range,
//! as the Media Foundation path).
#![cfg_attr(not(windows), allow(dead_code))]

use crate::video;
use rustdesk_codec::aom::{AomDecoder, AomEncoder, AomEncoderConfig};
use rustdesk_codec::vpxcodec::{
    VpxDecoder, VpxDecoderConfig, VpxEncoder, VpxEncoderConfig, VpxVideoCodecId,
};
use rustdesk_codec::{Chroma, EncodeYuvFormat, GoogleImage, STRIDE_ALIGN};

/// A software codec, by the name the wire and the stream description use.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Codec {
    Vp8,
    /// VP9 through libvpx: named apart from Media Foundation's `vp9` so a
    /// controller decodes it with libvpx, which also takes 4:4:4.
    Vp9,
    Av1,
}

impl Codec {
    /// Every software codec, in the host's order of preference.
    pub const ALL: [Codec; 3] = [Codec::Vp9, Codec::Av1, Codec::Vp8];

    /// The name the wire and the stream description use.
    pub fn name(self) -> &'static str {
        match self {
            Codec::Vp8 => "vp8",
            Codec::Vp9 => "vp9-sw",
            Codec::Av1 => "av1",
        }
    }

    /// The codec a name stands for.
    pub fn from_name(name: &str) -> Option<Codec> {
        Codec::ALL.into_iter().find(|c| c.name() == name)
    }

    /// Whether the codec carries full colour (4:4:4): VP9 profile 1 and AV1
    /// profile 1; VP8 has 4:2:0 only.
    pub fn full_colour(self) -> bool {
        matches!(self, Codec::Vp9 | Codec::Av1)
    }
}

/// An encoded frame and whether it is a key frame.
pub type Unit = (Vec<u8>, bool);

/// The encoder of one stream.
enum Inner {
    Vpx(VpxEncoder),
    Aom(AomEncoder),
}

/// A software encoder with its input picture.
pub struct Encoder {
    inner: Inner,
    layout: EncodeYuvFormat,
    i444: bool,
    /// The picture to encode next, in `layout`.
    yuv: Vec<u8>,
    /// Milliseconds per frame, the encoders' time base.
    frame_ms: i64,
    pts: i64,
}

impl Encoder {
    /// Opens an encoder for `width`×`height` pictures (even sides).
    /// Inputs: `bitrate` in bit/s; `i444` asks for full colour, which only
    /// VP9 and AV1 take.
    /// Returns: the encoder, or why libvpx or libaom refused it.
    pub fn new(
        codec: Codec,
        width: u32,
        height: u32,
        fps: u32,
        bitrate: u32,
        i444: bool,
    ) -> Result<Encoder, String> {
        let i444 = i444 && codec.full_colour();
        let base = rustdesk_codec::base_bitrate(width, height).max(1);
        let quality = (bitrate / 1000) as f32 / base as f32;
        // A key frame every 4 seconds, as the Media Foundation encoders.
        let keyframe_interval = Some(4 * fps.max(1) as usize);
        let inner = match codec {
            Codec::Vp8 | Codec::Vp9 => Inner::Vpx(
                VpxEncoder::new(
                    VpxEncoderConfig {
                        width,
                        height,
                        quality,
                        codec: if codec == Codec::Vp8 {
                            VpxVideoCodecId::VP8
                        } else {
                            VpxVideoCodecId::VP9
                        },
                        keyframe_interval,
                    },
                    i444,
                )
                .map_err(|e| format!("libvpx: {e}"))?,
            ),
            Codec::Av1 => Inner::Aom(
                AomEncoder::new(
                    AomEncoderConfig {
                        width,
                        height,
                        quality,
                        keyframe_interval,
                    },
                    i444,
                )
                .map_err(|e| format!("libaom: {e}"))?,
            ),
        };
        let layout = match &inner {
            Inner::Vpx(e) => e.yuvfmt(),
            Inner::Aom(e) => e.yuvfmt(),
        };
        Ok(Encoder {
            inner,
            layout,
            i444,
            yuv: Vec::new(),
            frame_ms: 1000 / fps.max(1) as i64,
            pts: 0,
        })
    }

    /// Whether the stream is in full colour.
    pub fn i444(&self) -> bool {
        self.i444
    }

    /// The library that encodes: `libvpx` or `libaom`.
    pub fn library(&self) -> &'static str {
        match self.inner {
            Inner::Vpx(_) => "libvpx",
            Inner::Aom(_) => "libaom",
        }
    }

    /// Converts a captured BGRA picture (`stride` bytes per row) into the
    /// next picture to encode, scaled to the stream's size.
    pub fn picture(&mut self, bgra: &[u8], width: usize, height: usize, stride: usize) {
        video::bgra_to_planar(
            bgra,
            width,
            height,
            stride,
            &self.layout,
            self.i444,
            &mut self.yuv,
        );
    }

    /// Whether a picture was given yet.
    pub fn has_picture(&self) -> bool {
        !self.yuv.is_empty()
    }

    /// Encodes the last picture; `key` forces a key frame.
    /// Returns: the frames that came out (libvpx and libaom in real-time
    /// mode give one per picture, or none when they drop it).
    pub fn encode(&mut self, key: bool) -> Result<Vec<Unit>, String> {
        self.pts += self.frame_ms;
        let (pts, yuv) = (self.pts, &self.yuv);
        match &mut self.inner {
            Inner::Vpx(e) => Ok(e
                .encode(pts, yuv, STRIDE_ALIGN, key)
                .map_err(|e| format!("libvpx: {e}"))?
                .map(|f| (f.data.to_vec(), f.key))
                .collect()),
            Inner::Aom(e) => Ok(e
                .encode(pts, yuv, STRIDE_ALIGN, key)
                .map_err(|e| format!("libaom: {e}"))?
                .map(|f| (f.data.to_vec(), f.key))
                .collect()),
        }
    }

    /// Sets the target bitrate in bit/s while the encoder runs.
    pub fn set_bitrate(&mut self, bitrate: u32) {
        let kbps = (bitrate / 1000).max(1);
        let _ = match &mut self.inner {
            Inner::Vpx(e) => e.set_bitrate(kbps),
            Inner::Aom(e) => e.set_bitrate(kbps),
        };
    }
}

/// The decoder of one stream.
pub enum Decoder {
    Vpx(VpxDecoder),
    Aom(AomDecoder),
}

impl Decoder {
    /// The library that decodes: `libvpx` or `libaom`.
    pub fn library(&self) -> &'static str {
        match self {
            Decoder::Vpx(_) => "libvpx",
            Decoder::Aom(_) => "libaom",
        }
    }

    /// Opens a decoder for `codec`.
    pub fn new(codec: Codec) -> Result<Decoder, String> {
        Ok(match codec {
            Codec::Vp8 | Codec::Vp9 => Decoder::Vpx(
                VpxDecoder::new(VpxDecoderConfig {
                    codec: if codec == Codec::Vp8 {
                        VpxVideoCodecId::VP8
                    } else {
                        VpxVideoCodecId::VP9
                    },
                })
                .map_err(|e| format!("libvpx: {e}"))?,
            ),
            Codec::Av1 => Decoder::Aom(AomDecoder::new().map_err(|e| format!("libaom: {e}"))?),
        })
    }

    /// Decodes one frame into `rgba` (resized to width×height×4).
    /// Returns: the picture's size when one came out, None otherwise.
    pub fn decode(
        &mut self,
        frame: &[u8],
        rgba: &mut Vec<u8>,
    ) -> Result<Option<(u32, u32)>, String> {
        match self {
            Decoder::Vpx(d) => {
                let last = d.decode(frame).map_err(|e| format!("libvpx: {e}"))?.last();
                Ok(last.map(|img| to_rgba(&img, rgba)))
            }
            Decoder::Aom(d) => {
                let last = d.decode(frame).map_err(|e| format!("libaom: {e}"))?.last();
                Ok(last.map(|img| to_rgba(&img, rgba)))
            }
        }
    }
}

/// Converts a decoded picture into RGBA.
fn to_rgba(img: &impl GoogleImage, rgba: &mut Vec<u8>) -> (u32, u32) {
    let (w, h) = (img.width(), img.height());
    let planes = img.planes();
    let stride = img.stride();
    let i444 = img.chroma() == Chroma::I444;
    let (cw, ch) = if i444 {
        (w, h)
    } else {
        (w.div_ceil(2), h.div_ceil(2))
    };
    // SAFETY: libvpx and libaom keep the image's planes alive until the
    // next decode call; each plane holds its rows at its stride, the luma
    // plane `h` rows of `w` samples and the chroma planes `ch` rows of `cw`.
    let [y, u, v] = unsafe {
        [
            std::slice::from_raw_parts(planes[0], stride[0] as usize * (h - 1) + w),
            std::slice::from_raw_parts(planes[1], stride[1] as usize * (ch - 1) + cw),
            std::slice::from_raw_parts(planes[2], stride[2] as usize * (ch - 1) + cw),
        ]
    };
    let strides = [stride[0] as usize, stride[1] as usize, stride[2] as usize];
    video::planar_to_rgba([y, u, v], strides, w, h, i444, rgba);
    (w as u32, h as u32)
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A 320×192 picture: four flat colour blocks and a gradient row band.
    fn picture() -> Vec<u8> {
        let (w, h) = (320, 192);
        let mut bgra = Vec::with_capacity(w * h * 4);
        for y in 0..h {
            for x in 0..w {
                let [r, g, b] = match (x < w / 2, y < h / 2) {
                    (true, true) => [200, 40, 30],
                    (false, true) => [30, 160, 60],
                    (true, false) => [40, 60, 220],
                    (false, false) => [(x % 256) as u8, 128, (y % 256) as u8],
                };
                bgra.extend_from_slice(&[b, g, r, 255]);
            }
        }
        bgra
    }

    /// The mean absolute difference of RGB between two RGBA pictures.
    fn difference(a: &[u8], b: &[u8]) -> f64 {
        let (mut sum, mut n) = (0u64, 0u64);
        for (pa, pb) in a.chunks(4).zip(b.chunks(4)) {
            for i in 0..3 {
                sum += (pa[i] as i32 - pb[i] as i32).unsigned_abs() as u64;
                n += 1;
            }
        }
        sum as f64 / n as f64
    }

    #[test]
    fn every_codec_round_trips_at_320x192() {
        let bgra = picture();
        let rgba_in: Vec<u8> = bgra
            .chunks(4)
            .flat_map(|p| [p[2], p[1], p[0], 255])
            .collect();
        for codec in Codec::ALL {
            for i444 in [false, true] {
                let mut enc = Encoder::new(codec, 320, 192, 30, 2_000_000, i444)
                    .unwrap_or_else(|e| panic!("{codec:?}: {e}"));
                assert_eq!(enc.i444(), i444 && codec.full_colour());
                let mut dec = Decoder::new(codec).unwrap();
                let mut rgba = Vec::new();
                let mut shown = None;
                let mut first_key = None;
                for n in 0..10 {
                    enc.picture(&bgra, 320, 192, 320 * 4);
                    for (unit, key) in enc.encode(n == 0).unwrap() {
                        first_key.get_or_insert(key);
                        if let Some(size) = dec.decode(&unit, &mut rgba).unwrap() {
                            shown = Some(size);
                        }
                    }
                }
                assert_eq!(
                    first_key,
                    Some(true),
                    "{codec:?}: the first frame is a key frame"
                );
                assert_eq!(shown, Some((320, 192)), "{codec:?} i444={i444}");
                let diff = difference(&rgba, &rgba_in);
                eprintln!("{codec:?} i444={i444}: mean difference {diff:.2}");
                assert!(diff < 8.0, "{codec:?} i444={i444}: mean difference {diff}");
            }
        }
    }

    #[test]
    fn full_colour_keeps_a_one_pixel_chroma_detail() {
        // Alternate red and blue columns: 4:2:0 averages them into purple,
        // 4:4:4 keeps them apart.
        let (w, h) = (320usize, 192usize);
        let bgra: Vec<u8> = (0..w * h)
            .flat_map(|i| {
                if i % 2 == 0 {
                    [0, 0, 255, 255]
                } else {
                    [255, 0, 0, 255]
                }
            })
            .collect();
        let red_at_0 = |codec, i444| {
            let mut enc = Encoder::new(codec, w as u32, h as u32, 30, 8_000_000, i444).unwrap();
            let mut dec = Decoder::new(codec).unwrap();
            let mut rgba = Vec::new();
            for n in 0..5 {
                enc.picture(&bgra, w, h, w * 4);
                for (unit, _) in enc.encode(n == 0).unwrap() {
                    dec.decode(&unit, &mut rgba).unwrap();
                }
            }
            // Mean red of the even columns minus that of the odd ones.
            let red = |parity: usize| {
                let px: Vec<u32> = (0..w * h)
                    .filter(|i| i % 2 == parity)
                    .map(|i| rgba[i * 4] as u32)
                    .collect();
                px.iter().sum::<u32>() as f64 / px.len() as f64
            };
            red(0) - red(1)
        };
        for codec in [Codec::Vp9, Codec::Av1] {
            let full = red_at_0(codec, true);
            let sub = red_at_0(codec, false);
            assert!(
                full > 150.0,
                "{codec:?} 4:4:4 keeps the columns apart: {full}"
            );
            assert!(sub < 100.0, "{codec:?} 4:2:0 blends them: {sub}");
        }
    }

    #[test]
    fn names_round_trip() {
        for c in Codec::ALL {
            assert_eq!(Codec::from_name(c.name()), Some(c));
        }
        assert_eq!(Codec::from_name("vp9"), None, "Media Foundation's VP9");
    }
}
