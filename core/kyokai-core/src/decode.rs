//! The viewer's decoders, for the GUI's own copy of the core
//! (doc/en-us/architecture.md, "FFI"): encoded frames in, RGBA out. They
//! need no started core; each lives under a handle until closed.

#[cfg(windows)]
use crate::video;
use std::collections::HashMap;
use std::sync::Mutex;
use std::sync::atomic::{AtomicU64, Ordering};

/// One stream's decoder.
enum Decoder {
    /// The test pattern: frames are already RGBA.
    Raw { width: u32, height: u32 },
    #[cfg(windows)]
    H264 {
        dec: crate::mf::Decoder,
        width: u32,
        height: u32,
        nv12: Vec<u8>,
    },
}

/// A decoder kept between calls.
struct Kept(Decoder);

// SAFETY: the Media Foundation decoder is created in the multithreaded
// apartment, whose objects may be called from any of its threads; the GUI
// drives each decoder from one goroutine at a time (locked to an OS thread
// it initialized), and the map's lock serializes every call.
unsafe impl Send for Kept {}

static DECODERS: Mutex<Option<HashMap<u64, Kept>>> = Mutex::new(None);
static NEXT: AtomicU64 = AtomicU64::new(1);

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
        #[cfg(windows)]
        "h264" => Decoder::H264 {
            dec: crate::mf::Decoder::new()?,
            width: s.width,
            height: s.height,
            nv12: Vec::new(),
        },
        other => return Err(format!("no decoder for {other}")),
    };
    let id = NEXT.fetch_add(1, Ordering::Relaxed);
    DECODERS
        .lock()
        .map_err(|_| "decoders poisoned")?
        .get_or_insert_with(HashMap::new)
        .insert(id, Kept(d));
    Ok(id)
}

/// Decodes one frame into `rgba` (resized to width×height×4).
/// Returns: the picture's size when one is ready, None while the decoder
/// waits for more input.
pub fn decode(id: u64, frame: &[u8], rgba: &mut Vec<u8>) -> Result<Option<(u32, u32)>, String> {
    let mut all = DECODERS.lock().map_err(|_| "decoders poisoned")?;
    let d = all
        .as_mut()
        .and_then(|m| m.get_mut(&id))
        .ok_or("no such decoder")?;
    match &mut d.0 {
        Decoder::Raw { width, height } => {
            if frame.len() != (*width * *height * 4) as usize {
                return Err("frame size does not match the stream".into());
            }
            rgba.clear();
            rgba.extend_from_slice(frame);
            Ok(Some((*width, *height)))
        }
        #[cfg(windows)]
        Decoder::H264 {
            dec,
            width,
            height,
            nv12,
        } => {
            let Some((pw, ph, stride, bytes)) = dec.decode(frame)?.pop() else {
                return Ok(None);
            };
            // The decoder pads to whole macroblocks: show the stream's size.
            let (w, h) = ((*width).min(pw), (*height).min(ph));
            *nv12 = bytes;
            video::nv12_to_rgba(
                nv12,
                w as usize,
                h as usize,
                stride as usize,
                ph as usize,
                rgba,
            );
            Ok(Some((w, h)))
        }
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

    #[test]
    fn raw_frames_pass_through_and_handles_close() {
        let id = open(br#"{"codec":"raw-rgba","width":2,"height":1}"#).unwrap();
        let mut rgba = Vec::new();
        assert_eq!(
            decode(id, &[1, 2, 3, 4, 5, 6, 7, 8], &mut rgba).unwrap(),
            Some((2, 1))
        );
        assert_eq!(rgba, [1, 2, 3, 4, 5, 6, 7, 8]);
        assert!(decode(id, &[1, 2, 3], &mut rgba).is_err());
        close(id);
        assert!(decode(id, &[0; 8], &mut rgba).is_err());
        assert!(open(br#"{"codec":"av9","width":2,"height":1}"#).is_err());
    }
}
