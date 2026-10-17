//! H.264 through Windows Media Foundation (doc/en-us/computer-use.md,
//! "Video"): the system's encoder on the host and its decoder on the
//! controller, so the OS vendor's codec licence covers H.264. Both use the
//! synchronous Microsoft transforms, which every Windows edition with Media
//! Foundation has; hardware transforms come later.
//!
//! Every object here belongs to the thread that made it (COM, multithreaded
//! apartment); pixels cross as NV12.

use std::mem::ManuallyDrop;
use windows::Win32::Media::MediaFoundation::*;
use windows::Win32::System::Com::{
    CLSCTX_INPROC_SERVER, COINIT_MULTITHREADED, CoCreateInstance, CoInitializeEx, CoTaskMemFree,
};
use windows::Win32::System::Variant::{VARIANT, VT_BOOL, VT_UI4};
use windows::core::{GUID, Interface};

/// An encoded access unit and whether it is a key frame.
pub type Unit = (Vec<u8>, bool);
/// A decoded picture: width, height, row stride and NV12 bytes.
pub type Picture = (u32, u32, u32, Vec<u8>);

/// Starts COM and Media Foundation for the calling thread.
fn startup() -> Result<(), String> {
    // SAFETY: plain initialization calls; a repeated CoInitializeEx on the
    // same thread only returns S_FALSE.
    unsafe {
        let _ = CoInitializeEx(None, COINIT_MULTITHREADED);
        MFStartup(MF_VERSION, MFSTARTUP_NOSOCKET).map_err(|e| format!("Media Foundation: {e}"))
    }
}

fn pack(hi: u32, lo: u32) -> u64 {
    ((hi as u64) << 32) | lo as u64
}

fn video_type(
    subtype: &GUID,
    width: u32,
    height: u32,
    fps: u32,
) -> windows::core::Result<IMFMediaType> {
    // SAFETY: COM calls on an object this function owns.
    unsafe {
        let t = MFCreateMediaType()?;
        t.SetGUID(&MF_MT_MAJOR_TYPE, &MFMediaType_Video)?;
        t.SetGUID(&MF_MT_SUBTYPE, subtype)?;
        if width > 0 {
            t.SetUINT64(&MF_MT_FRAME_SIZE, pack(width, height))?;
        }
        if fps > 0 {
            t.SetUINT64(&MF_MT_FRAME_RATE, pack(fps, 1))?;
        }
        t.SetUINT64(&MF_MT_PIXEL_ASPECT_RATIO, pack(1, 1))?;
        t.SetUINT32(&MF_MT_INTERLACE_MODE, MFVideoInterlace_Progressive.0 as u32)?;
        Ok(t)
    }
}

/// Sets one codec property; transforms that lack it keep their default.
fn codec_value(t: &IMFTransform, api: &GUID, value: u32, boolean: bool) {
    let Ok(codec) = t.cast::<ICodecAPI>() else {
        return;
    };
    let mut v = VARIANT::default();
    // SAFETY: the union is written as the tag it is given, then passed by
    // pointer for the call only.
    unsafe {
        let inner = &mut *v.Anonymous.Anonymous;
        if boolean {
            inner.vt = VT_BOOL;
            inner.Anonymous.boolVal = (value != 0).into();
        } else {
            inner.vt = VT_UI4;
            inner.Anonymous.ulVal = value;
        }
        let _ = codec.SetValue(api, &v);
    }
}

/// A buffer-backed sample holding `data`.
fn sample_from(
    data: &[u8],
    time_100ns: i64,
    duration_100ns: i64,
) -> windows::core::Result<IMFSample> {
    // SAFETY: the buffer is locked while written and holds data.len() bytes.
    unsafe {
        let buffer = MFCreateMemoryBuffer(data.len() as u32)?;
        let mut ptr = std::ptr::null_mut();
        buffer.Lock(&mut ptr, None, None)?;
        std::ptr::copy_nonoverlapping(data.as_ptr(), ptr, data.len());
        buffer.Unlock()?;
        buffer.SetCurrentLength(data.len() as u32)?;
        let sample = MFCreateSample()?;
        sample.AddBuffer(&buffer)?;
        sample.SetSampleTime(time_100ns)?;
        sample.SetSampleDuration(duration_100ns)?;
        Ok(sample)
    }
}

/// Copies a sample's bytes out.
fn bytes_of(sample: &IMFSample) -> windows::core::Result<Vec<u8>> {
    // SAFETY: the contiguous buffer is locked while read, for its length.
    unsafe {
        let buffer = sample.ConvertToContiguousBuffer()?;
        let mut ptr = std::ptr::null_mut();
        let mut len = 0u32;
        buffer.Lock(&mut ptr, None, Some(&mut len))?;
        let out = std::slice::from_raw_parts(ptr, len as usize).to_vec();
        buffer.Unlock()?;
        Ok(out)
    }
}

/// Pulls every output the transform has ready.
/// Returns: the outputs (bytes and whether each is a key frame), and
/// whether the output format changed (decoders, before their first frame).
fn drain(t: &IMFTransform) -> windows::core::Result<(Vec<Unit>, bool)> {
    let mut out = Vec::new();
    loop {
        // SAFETY: COM calls on the transform; the output buffer's sample is
        // ours (allocated here when the transform does not provide one) and
        // released below.
        unsafe {
            let info = t.GetOutputStreamInfo(0)?;
            let provides = info.dwFlags & MFT_OUTPUT_STREAM_PROVIDES_SAMPLES.0 as u32 != 0;
            let mine = if provides {
                None
            } else {
                let s = MFCreateSample()?;
                s.AddBuffer(&MFCreateMemoryBuffer(info.cbSize.max(1 << 20))?)?;
                Some(s)
            };
            let mut buffers = [MFT_OUTPUT_DATA_BUFFER {
                dwStreamID: 0,
                pSample: ManuallyDrop::new(mine),
                dwStatus: 0,
                pEvents: ManuallyDrop::new(None),
            }];
            let mut status = 0u32;
            let result = t.ProcessOutput(0, &mut buffers, &mut status);
            let sample = ManuallyDrop::take(&mut buffers[0].pSample);
            drop(ManuallyDrop::take(&mut buffers[0].pEvents));
            match result {
                Ok(()) => {
                    if let Some(s) = sample {
                        let key = s.GetUINT32(&MFSampleExtension_CleanPoint).unwrap_or(0) != 0;
                        out.push((bytes_of(&s)?, key));
                    }
                }
                Err(e) if e.code() == MF_E_TRANSFORM_NEED_MORE_INPUT => return Ok((out, false)),
                Err(e) if e.code() == MF_E_TRANSFORM_STREAM_CHANGE => return Ok((out, true)),
                Err(e) => return Err(e),
            }
        }
    }
}

/// The system's H.264 encoder, fed NV12 frames.
pub struct Encoder {
    t: IMFTransform,
    width: u32,
    height: u32,
    fps: u32,
    n: i64,
}

impl Encoder {
    /// Opens an encoder for `width`×`height` (even) at `fps` and a target
    /// bitrate, tuned for low latency (no B-frames, frequent key frames).
    /// Returns: an error when this system has no H.264 encoder (Windows N
    /// and Server editions without Media Foundation).
    pub fn new(width: u32, height: u32, fps: u32, bitrate: u32) -> Result<Self, String> {
        startup()?;
        let t = find_encoder().ok_or("this system has no H.264 encoder")?;
        let open = || -> windows::core::Result<()> {
            // Low latency first: some transforms read it only when their types
            // are set.
            // SAFETY: an attribute on the transform's own store.
            unsafe {
                if let Ok(attrs) = t.GetAttributes() {
                    let _ = attrs.SetUINT32(&MF_LOW_LATENCY, 1);
                }
            }
            codec_value(&t, &CODECAPI_AVLowLatencyMode, 1, true);
            let out = video_type(&MFVideoFormat_H264, width, height, fps)?;
            // SAFETY: COM calls on the transform and the types made here.
            unsafe {
                out.SetUINT32(&MF_MT_AVG_BITRATE, bitrate)?;
                out.SetUINT32(&MF_MT_MPEG2_PROFILE, eAVEncH264VProfile_Main.0 as u32)?;
                t.SetOutputType(0, &out, 0)?;
                t.SetInputType(0, &video_type(&MFVideoFormat_NV12, width, height, fps)?, 0)?;
            }
            codec_value(&t, &CODECAPI_AVLowLatencyMode, 1, true);
            codec_value(
                &t,
                &CODECAPI_AVEncCommonRateControlMode,
                eAVEncCommonRateControlMode_CBR.0 as u32,
                false,
            );
            codec_value(&t, &CODECAPI_AVEncCommonMeanBitRate, bitrate, false);
            codec_value(&t, &CODECAPI_AVEncMPVGOPSize, fps * 4, false);
            codec_value(&t, &CODECAPI_AVEncMPVDefaultBPictureCount, 0, false);
            // SAFETY: streaming messages on the configured transform.
            unsafe {
                t.ProcessMessage(MFT_MESSAGE_NOTIFY_BEGIN_STREAMING, 0)?;
                t.ProcessMessage(MFT_MESSAGE_NOTIFY_START_OF_STREAM, 0)?;
            }
            Ok(())
        };
        open().map_err(|e| format!("H.264 encoder: {e}"))?;
        Ok(Encoder {
            t,
            width,
            height,
            fps,
            n: 0,
        })
    }

    /// Changes the target bitrate while running (RustDesk's QoS drives it).
    pub fn set_bitrate(&self, bitrate: u32) {
        codec_value(&self.t, &CODECAPI_AVEncCommonMeanBitRate, bitrate, false);
    }

    /// Encodes one NV12 frame; `key` forces a key frame.
    /// Returns: the encoded access units ready now (Annex B), each with
    /// whether it is a key frame.
    pub fn encode(&mut self, nv12: &[u8], key: bool) -> Result<Vec<Unit>, String> {
        if nv12.len() != (self.width * self.height * 3 / 2) as usize {
            return Err("frame size does not match the encoder".into());
        }
        if key {
            codec_value(&self.t, &CODECAPI_AVEncVideoForceKeyFrame, 1, false);
        }
        let step = 10_000_000 / self.fps as i64;
        let run = || -> windows::core::Result<Vec<Unit>> {
            let sample = sample_from(nv12, self.n * step, step)?;
            // SAFETY: the transform takes its own reference to the sample.
            unsafe { self.t.ProcessInput(0, &sample, 0)? };
            Ok(drain(&self.t)?.0)
        };
        let out = run().map_err(|e| format!("H.264 encoder: {e}"))?;
        self.n += 1;
        Ok(out)
    }
}

/// Finds the first synchronous H.264 encoder that takes NV12.
fn find_encoder() -> Option<IMFTransform> {
    let input = MFT_REGISTER_TYPE_INFO {
        guidMajorType: MFMediaType_Video,
        guidSubtype: MFVideoFormat_NV12,
    };
    let output = MFT_REGISTER_TYPE_INFO {
        guidMajorType: MFMediaType_Video,
        guidSubtype: MFVideoFormat_H264,
    };
    let mut list: *mut Option<IMFActivate> = std::ptr::null_mut();
    let mut count = 0u32;
    // SAFETY: MFTEnumEx fills `list` with `count` activates, which are
    // released (by taking them) and whose array is freed with CoTaskMemFree.
    unsafe {
        MFTEnumEx(
            MFT_CATEGORY_VIDEO_ENCODER,
            MFT_ENUM_FLAG_SYNCMFT | MFT_ENUM_FLAG_LOCALMFT | MFT_ENUM_FLAG_SORTANDFILTER,
            Some(&input),
            Some(&output),
            &mut list,
            &mut count,
        )
        .ok()?;
        let activates: Vec<Option<IMFActivate>> =
            (0..count as usize).map(|i| (*list.add(i)).take()).collect();
        CoTaskMemFree(Some(list as *const _));
        activates
            .into_iter()
            .flatten()
            .find_map(|a| a.ActivateObject::<IMFTransform>().ok())
    }
}

/// The system's H.264 decoder, giving NV12 frames.
pub struct Decoder {
    t: IMFTransform,
    /// The decoded picture's size and row stride, once known.
    format: Option<(u32, u32, u32)>,
    n: i64,
}

impl Decoder {
    /// Opens the Microsoft H.264 decoder in low-latency mode.
    pub fn new() -> Result<Self, String> {
        startup()?;
        let open = || -> windows::core::Result<IMFTransform> {
            // SAFETY: creating and configuring a transform this function owns.
            unsafe {
                let t: IMFTransform =
                    CoCreateInstance(&CLSID_MSH264DecoderMFT, None, CLSCTX_INPROC_SERVER)?;
                if let Ok(attrs) = t.GetAttributes() {
                    let _ = attrs.SetUINT32(&MF_LOW_LATENCY, 1);
                }
                t.SetInputType(0, &video_type(&MFVideoFormat_H264, 0, 0, 0)?, 0)?;
                Ok(t)
            }
        };
        let t = open().map_err(|e| format!("H.264 decoder: {e}"))?;
        let mut d = Decoder {
            t,
            format: None,
            n: 0,
        };
        d.pick_output().map_err(|e| format!("H.264 decoder: {e}"))?;
        // SAFETY: streaming messages on the configured transform.
        unsafe {
            let _ = d.t.ProcessMessage(MFT_MESSAGE_NOTIFY_BEGIN_STREAMING, 0);
            let _ = d.t.ProcessMessage(MFT_MESSAGE_NOTIFY_START_OF_STREAM, 0);
        }
        Ok(d)
    }

    /// Chooses NV12 output and reads the picture's size and stride.
    fn pick_output(&mut self) -> windows::core::Result<()> {
        // SAFETY: COM calls on the transform and the types it offers.
        unsafe {
            for i in 0.. {
                let t = self.t.GetOutputAvailableType(0, i)?;
                if t.GetGUID(&MF_MT_SUBTYPE)? != MFVideoFormat_NV12 {
                    continue;
                }
                self.t.SetOutputType(0, &t, 0)?;
                self.format = t.GetUINT64(&MF_MT_FRAME_SIZE).ok().map(|size| {
                    let (w, h) = ((size >> 32) as u32, size as u32);
                    let stride = t.GetUINT32(&MF_MT_DEFAULT_STRIDE).unwrap_or(w);
                    (w, h, stride.max(w))
                });
                return Ok(());
            }
        }
        Ok(())
    }

    /// Decodes one access unit (Annex B).
    /// Returns: the pictures ready now, each as (width, height, stride,
    /// NV12 bytes); empty while the decoder needs more input.
    pub fn decode(&mut self, unit: &[u8]) -> Result<Vec<Picture>, String> {
        let mut run = || -> windows::core::Result<Vec<Picture>> {
            let sample = sample_from(unit, self.n * 166_666, 166_666)?;
            // SAFETY: the transform takes its own reference to the sample.
            unsafe { self.t.ProcessInput(0, &sample, 0)? };
            let mut pictures = Vec::new();
            loop {
                let (out, changed) = drain(&self.t)?;
                for (bytes, _) in out {
                    if let Some((w, h, stride)) = self.format {
                        pictures.push((w, h, stride, bytes));
                    }
                }
                if !changed {
                    break;
                }
                self.pick_output()?;
            }
            Ok(pictures)
        };
        let out = run().map_err(|e| format!("H.264 decoder: {e}"))?;
        self.n += 1;
        Ok(out)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::video;

    #[test]
    fn frames_survive_encoding_and_decoding() {
        let (w, h) = (320u32, 192u32);
        let mut encoder = match Encoder::new(w, h, 30, 1_000_000) {
            Ok(e) => e,
            // Windows N and Server images without Media Foundation.
            Err(e) => return eprintln!("skipped: {e}"),
        };
        let mut decoder = Decoder::new().expect("the H.264 decoder ships with the encoder");
        let mut pictures = Vec::new();
        let mut nv12 = Vec::new();
        for n in 0..30usize {
            // A grey picture with a bright square moving across it.
            let bgra: Vec<u8> = (0..(w * h) as usize)
                .flat_map(|i| {
                    let (x, y) = (i % w as usize, i / w as usize);
                    let on = (x + 300 - n * 8) % 300 < 40 && (60..120).contains(&y);
                    if on {
                        [240u8, 240, 240, 255]
                    } else {
                        [90, 90, 90, 255]
                    }
                })
                .collect();
            video::bgra_to_nv12(
                &bgra,
                w as usize,
                h as usize,
                w as usize * 4,
                w as usize,
                h as usize,
                &mut nv12,
            );
            for (unit, _) in encoder.encode(&nv12, n == 0).unwrap() {
                pictures.extend(decoder.decode(&unit).unwrap());
            }
        }
        assert!(!pictures.is_empty(), "no picture came back");
        let (pw, ph, stride, bytes) = &pictures[pictures.len() - 1];
        assert!((*pw, *ph) >= (w, h), "decoded {pw}×{ph}");
        // The grey background is grey again (within codec loss).
        let mut rgba = Vec::new();
        video::nv12_to_rgba(
            bytes,
            w as usize,
            h as usize,
            *stride as usize,
            *ph as usize,
            &mut rgba,
        );
        let corner = &rgba[..4];
        assert!(
            corner[..3].iter().all(|c| (*c as i32 - 90).abs() < 12),
            "{corner:?}"
        );
    }
}

#[cfg(test)]
mod latency {
    use super::*;

    /// How many frames the encoder takes before its first output: each one
    /// held is a frame period of added latency.
    #[test]
    fn the_encoder_answers_within_two_frames() {
        let (w, h) = (640u32, 360u32);
        let Ok(mut e) = Encoder::new(w, h, 30, 2_000_000) else {
            return eprintln!("skipped: no encoder");
        };
        let mut n = 0;
        let first = loop {
            n += 1;
            let nv12: Vec<u8> = (0..(w * h * 3 / 2) as usize)
                .map(|i| (i + n * 7) as u8)
                .collect();
            if !e.encode(&nv12, n == 1).unwrap().is_empty() || n == 60 {
                break n;
            }
        };
        eprintln!("first output after {first} frames");
        assert!(first <= 2, "the encoder holds {first} frames");
    }
}
