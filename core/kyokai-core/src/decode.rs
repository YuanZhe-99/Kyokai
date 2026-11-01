//! The viewer's decoders, for the GUI's own copy of the core
//! (doc/en-us/architecture.md, "FFI"): encoded frames in, RGBA out. They
//! need no started core; each lives under a handle until closed.

use crate::convert::Sink;
use std::collections::HashMap;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex};

/// One stream's decoder.
enum Decoder {
    /// The test pattern: frames are already RGBA.
    Raw { width: u32, height: u32 },
    /// libvpx or libaom, which give the picture in full.
    Soft(crate::soft::Decoder),
    /// Media Foundation's, which converts in place into the RGBA.
    #[cfg(windows)]
    Mf {
        dec: crate::mf::Decoder,
        width: u32,
        height: u32,
    },
    /// VideoToolbox's H.264 and HEVC.
    #[cfg(target_os = "macos")]
    Vt(crate::vt::Decoder),
    /// The system's FFmpeg's H.264 and HEVC (Linux).
    #[cfg(target_os = "linux")]
    Ff(crate::ff::Decoder),
    /// A NanoKVM's own pictures (Raw), each a JPEG.
    #[cfg(any(windows, target_os = "macos", target_os = "linux"))]
    /// Each picture is decoded into its own buffer first.
    Jpeg(crate::kvm::JpegDecoder, Vec<u8>),
}

/// A decoder kept between calls, under its own lock: viewers decode side
/// by side, and the map's lock is held only to find one.
struct Kept(Mutex<Decoder>);

// SAFETY: the Media Foundation decoder is created in the multithreaded
// apartment, whose objects may be called from any of its threads; libvpx
// and libaom decoders, FFmpeg's contexts and VideoToolbox sessions keep no
// thread affinity; the GUI drives each decoder from one goroutine at a time
// (locked to an OS thread it initialized), and its own lock serializes
// every call into it.
unsafe impl Send for Kept {}
// SAFETY: as for Send: every use of the decoder goes through its lock.
unsafe impl Sync for Kept {}

static DECODERS: Mutex<Option<HashMap<u64, Arc<Kept>>>> = Mutex::new(None);
static NEXT: AtomicU64 = AtomicU64::new(1);

/// The decoder under a handle, out of the map (whose lock is let go).
fn find(id: u64) -> Option<Arc<Kept>> {
    DECODERS.lock().ok()?.as_ref()?.get(&id).cloned()
}

/// Opens a decoder for a stream (`media::VideoStart` JSON).
/// Returns: its handle, or why it cannot be opened.
pub fn open(start: &[u8]) -> Result<u64, String> {
    let s: crate::media::VideoStart =
        serde_json::from_slice(start).map_err(|e| format!("bad stream: {e}"))?;
    if s.width == 0 || s.height == 0 || s.width > 16384 || s.height > 16384 {
        return Err("bad picture size".into());
    }
    let d = match s.codec.as_str() {
        "raw-rgba" => Decoder::Raw {
            width: s.width,
            height: s.height,
        },
        #[cfg(any(windows, target_os = "macos", target_os = "linux"))]
        crate::kvm::RAW => Decoder::Jpeg(crate::kvm::JpegDecoder, Vec::new()),
        name if crate::soft::Codec::from_name(name).is_some() => Decoder::Soft(
            crate::soft::Decoder::new(crate::soft::Codec::from_name(name).expect("checked"))?,
        ),
        #[cfg(windows)]
        name if crate::mf::Codec::from_name(name).is_some() => Decoder::Mf {
            dec: crate::mf::Decoder::new(
                crate::mf::Codec::from_name(name).expect("checked"),
                s.width,
                s.height,
            )?,
            width: s.width,
            height: s.height,
        },
        #[cfg(target_os = "macos")]
        name if crate::vt::Codec::from_name(name).is_some() => Decoder::Vt(
            crate::vt::Decoder::new(crate::vt::Codec::from_name(name).expect("checked")),
        ),
        #[cfg(target_os = "linux")]
        name @ ("h264" | "hevc") => Decoder::Ff(crate::ff::Decoder::new(name)?),
        other => return Err(format!("no decoder for {other}")),
    };
    let id = NEXT.fetch_add(1, Ordering::Relaxed);
    DECODERS
        .lock()
        .map_err(|_| "decoders poisoned")?
        .get_or_insert_with(HashMap::new)
        .insert(id, Arc::new(Kept(Mutex::new(d))));
    Ok(id)
}

/// Decodes one frame into `out` (as its `Want` says: RGBA at full or half
/// size, or not converted).
/// Returns: the picture's full size when one is ready, None while the
/// decoder waits for more input; an error also when the picture does not
/// fit (`out.short()`).
pub fn decode(id: u64, frame: &[u8], out: &mut Sink) -> Result<Option<(u32, u32)>, String> {
    let kept = find(id).ok_or("no such decoder")?;
    let mut d = kept.0.lock().map_err(|_| "decoder poisoned")?;
    match &mut *d {
        Decoder::Raw { width, height } => {
            let (w, h) = (*width as usize, *height as usize);
            if frame.len() != w * h * 4 {
                return Err("frame size does not match the stream".into());
            }
            out.rgba(frame, w, h)?;
            Ok(Some((*width, *height)))
        }
        Decoder::Soft(dec) => dec.decode(frame, out),
        #[cfg(windows)]
        Decoder::Mf { dec, width, height } => dec.decode(frame, *width, *height, out),
        #[cfg(target_os = "macos")]
        Decoder::Vt(dec) => dec.decode(frame, out),
        #[cfg(target_os = "linux")]
        Decoder::Ff(dec) => dec.decode(frame, out),
        #[cfg(any(windows, target_os = "macos", target_os = "linux"))]
        Decoder::Jpeg(dec, rgba) => {
            let Some((w, h)) = dec.decode(frame, rgba)? else {
                return Ok(None);
            };
            out.rgba(rgba, w as usize, h as usize)?;
            Ok(Some((w, h)))
        }
    }
}

/// Which decoder a handle is, by name (`videotoolbox`, `media-foundation`,
/// `ffmpeg`, `libvpx`, `libaom`, `jpeg`; empty for the test pattern), and whether it decodes
/// in hardware, as it reports.
/// Returns: None for an unknown handle.
pub fn info(id: u64) -> Option<(&'static str, bool)> {
    let kept = find(id)?;
    let d = kept.0.lock().ok()?;
    Some(match &*d {
        Decoder::Raw { .. } => ("", false),
        Decoder::Soft(dec) => (dec.library(), false),
        #[cfg(windows)]
        Decoder::Mf { dec, .. } => ("media-foundation", dec.hardware()),
        #[cfg(target_os = "macos")]
        Decoder::Vt(dec) => ("videotoolbox", dec.hardware()),
        #[cfg(target_os = "linux")]
        Decoder::Ff(_) => ("ffmpeg", false),
        #[cfg(any(windows, target_os = "macos", target_os = "linux"))]
        Decoder::Jpeg(..) => ("jpeg", false),
    })
}

/// The viewer's sound decoders (Opus), by handle.
static SOUNDS: Mutex<Option<HashMap<u64, crate::opus::Decoder>>> = Mutex::new(None);

/// Opens a decoder of the host's sound.
/// Returns: its handle, or why libopus refused it.
pub fn audio_open() -> Result<u64, String> {
    let d = crate::opus::Decoder::new()?;
    let id = NEXT.fetch_add(1, Ordering::Relaxed);
    SOUNDS
        .lock()
        .map_err(|_| "decoders poisoned")?
        .get_or_insert_with(HashMap::new)
        .insert(id, d);
    Ok(id)
}

/// Decodes one Opus packet into `pcm` (interleaved stereo at 48 kHz).
/// Returns: the frames decoded.
pub fn audio_decode(id: u64, packet: &[u8], pcm: &mut Vec<f32>) -> Result<usize, String> {
    let mut all = SOUNDS.lock().map_err(|_| "decoders poisoned")?;
    all.as_mut()
        .and_then(|m| m.get_mut(&id))
        .ok_or("no such decoder")?
        .decode(packet, pcm)
}

/// Closes a sound decoder.
pub fn audio_close(id: u64) {
    if let Ok(mut all) = SOUNDS.lock()
        && let Some(m) = all.as_mut()
    {
        m.remove(&id);
    }
}

/// Closes a decoder.
pub fn close(id: u64) {
    if let Ok(mut all) = DECODERS.lock()
        && let Some(m) = all.as_mut()
    {
        m.remove(&id);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::convert::Want;

    #[test]
    fn raw_frames_pass_through_and_handles_close() {
        let id = open(br#"{"codec":"raw-rgba","width":2,"height":1}"#).unwrap();
        let mut rgba = Vec::new();
        assert_eq!(
            decode(id, &[1, 2, 3, 4, 5, 6, 7, 8], &mut Sink::vec(&mut rgba)).unwrap(),
            Some((2, 1))
        );
        assert_eq!(rgba, [1, 2, 3, 4, 5, 6, 7, 8]);
        // At half size, the 2×1 picture is one pixel, the average.
        let mut half = [0; 4];
        let mut sink = Sink::new(Want::Half, &mut half);
        assert_eq!(
            decode(id, &[1, 2, 3, 4, 5, 6, 7, 8], &mut sink).unwrap(),
            Some((2, 1))
        );
        assert_eq!(half, [3, 4, 5, 6]);
        // A buffer too small says so.
        let mut small = [0; 4];
        let mut sink = Sink::new(Want::Full, &mut small);
        assert!(decode(id, &[1, 2, 3, 4, 5, 6, 7, 8], &mut sink).is_err() && sink.short());
        assert!(decode(id, &[1, 2, 3], &mut Sink::vec(&mut rgba)).is_err());
        close(id);
        assert!(decode(id, &[0; 8], &mut Sink::vec(&mut rgba)).is_err());
        assert!(open(br#"{"codec":"av9","width":2,"height":1}"#).is_err());
    }

    /// Regression: one lock over every decoder made each viewer wait for
    /// the others' decoding. A decoder held (decoding) leaves the rest free.
    #[test]
    fn a_busy_decoder_leaves_the_others_free() {
        let start = br#"{"codec":"raw-rgba","width":1,"height":1}"#;
        let (busy, free) = (open(start).unwrap(), open(start).unwrap());
        let held = find(busy).unwrap();
        let _decoding = held.0.lock().unwrap();
        let mut rgba = Vec::new();
        assert_eq!(
            decode(free, &[9, 8, 7, 6], &mut Sink::vec(&mut rgba)).unwrap(),
            Some((1, 1))
        );
        assert_eq!(info(free), Some(("", false)));
        close(free);
        close(busy);
    }

    #[test]
    fn software_streams_open_on_every_platform() {
        for (codec, library) in [("vp9-sw", "libvpx"), ("av1", "libaom"), ("vp8", "libvpx")] {
            let start = format!(r#"{{"codec":"{codec}","width":320,"height":192,"i444":true}}"#);
            let id = open(start.as_bytes()).unwrap_or_else(|e| panic!("{codec}: {e}"));
            // Software decoders say so, by library.
            assert_eq!(info(id), Some((library, false)), "{codec}");
            close(id);
            assert_eq!(info(id), None);
        }
    }
}
