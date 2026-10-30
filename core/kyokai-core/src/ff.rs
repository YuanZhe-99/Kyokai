//! H.264 and HEVC decoding on Linux controllers through the system's FFmpeg
//! (libavcodec 58 to 62, FFmpeg 4 to 8), loaded at run time as Firefox does:
//! Kyokai ships no H.264 or HEVC decoder of its own, and a system without
//! FFmpeg, or with one built without them, offers neither
//! (doc/en-us/computer-use.md, "Linux").
//!
//! Only the leading fields of `AVFrame` and `AVPacket` are read or written;
//! they have kept their places since FFmpeg 2.

use std::ffi::{CStr, c_char, c_int, c_void};
use std::sync::OnceLock;

/// The sonames tried, newest first.
const SONAMES: [&CStr; 5] = [
    c"libavcodec.so.62",
    c"libavcodec.so.61",
    c"libavcodec.so.60",
    c"libavcodec.so.59",
    c"libavcodec.so.58",
];
/// `AV_CODEC_ID_H264` and `AV_CODEC_ID_HEVC`.
const H264: c_int = 27;
const HEVC: c_int = 173;
/// `AV_PIX_FMT_YUV420P` and `AV_PIX_FMT_YUVJ420P`.
const YUV420P: c_int = 0;
const YUVJ420P: c_int = 12;
/// `AVERROR(EAGAIN)`.
const AGAIN: c_int = -libc::EAGAIN;
/// The zero bytes libavcodec reads past a packet's end
/// (`AV_INPUT_BUFFER_PADDING_SIZE`).
const PADDING: usize = 64;
/// `AVFrame`'s fields: `data[8]`, `linesize[8]`, then after
/// `extended_data` the width, height and (after `nb_samples`) format.
const FRAME_LINESIZE: usize = 64;
const FRAME_WIDTH: usize = 104;
const FRAME_HEIGHT: usize = 108;
const FRAME_FORMAT: usize = 116;
/// `AVPacket`'s `data` and `size`, after `buf`, `pts` and `dts`.
const PACKET_DATA: usize = 24;
const PACKET_SIZE: usize = 32;

type Ptr = *mut c_void;

/// The libavcodec and libavutil functions Kyokai calls.
struct Lib {
    find_decoder: unsafe extern "C" fn(c_int) -> *const c_void,
    alloc_context: unsafe extern "C" fn(*const c_void) -> Ptr,
    open: unsafe extern "C" fn(Ptr, *const c_void, *mut Ptr) -> c_int,
    free_context: unsafe extern "C" fn(*mut Ptr),
    packet_alloc: unsafe extern "C" fn() -> Ptr,
    packet_free: unsafe extern "C" fn(*mut Ptr),
    send_packet: unsafe extern "C" fn(Ptr, *const c_void) -> c_int,
    receive_frame: unsafe extern "C" fn(Ptr, Ptr) -> c_int,
    frame_alloc: unsafe extern "C" fn() -> Ptr,
    frame_free: unsafe extern "C" fn(*mut Ptr),
    opt_set: unsafe extern "C" fn(Ptr, *const c_char, *const c_char, c_int) -> c_int,
}

/// A function of the library `handle` by name, as `F`.
/// Safety: `F` is an `unsafe extern "C" fn` type with the symbol's C
/// signature.
unsafe fn sym<F: Copy>(handle: *mut c_void, name: &CStr) -> Option<F> {
    assert_eq!(size_of::<F>(), size_of::<*mut c_void>());
    // SAFETY: a lookup in the loaded library and its dependencies
    // (libavutil); null when the symbol is missing.
    let p = unsafe { libc::dlsym(handle, name.as_ptr()) };
    // SAFETY: a non-null function pointer of the signature the caller vouches for.
    (!p.is_null()).then(|| unsafe { std::mem::transmute_copy::<*mut c_void, F>(&p) })
}

/// The system's libavcodec, loaded once per process (None without one).
fn lib() -> Option<&'static Lib> {
    static LIB: OnceLock<Option<Lib>> = OnceLock::new();
    LIB.get_or_init(|| {
        let (name, handle) = SONAMES.iter().find_map(|name| {
            // SAFETY: a NUL-terminated name; the handle is kept for the
            // process's life (never closed), so the functions stay valid.
            let h = unsafe { libc::dlopen(name.as_ptr(), libc::RTLD_NOW | libc::RTLD_LOCAL) };
            (!h.is_null()).then_some((name, h))
        })?;
        // SAFETY: each symbol is FFmpeg's function of that name, whose C
        // signature the field's type states.
        let lib = unsafe {
            Lib {
                find_decoder: sym(handle, c"avcodec_find_decoder")?,
                alloc_context: sym(handle, c"avcodec_alloc_context3")?,
                open: sym(handle, c"avcodec_open2")?,
                free_context: sym(handle, c"avcodec_free_context")?,
                packet_alloc: sym(handle, c"av_packet_alloc")?,
                packet_free: sym(handle, c"av_packet_free")?,
                send_packet: sym(handle, c"avcodec_send_packet")?,
                receive_frame: sym(handle, c"avcodec_receive_frame")?,
                frame_alloc: sym(handle, c"av_frame_alloc")?,
                frame_free: sym(handle, c"av_frame_free")?,
                opt_set: sym(handle, c"av_opt_set")?,
            }
        };
        log::info!("ffmpeg: {}", name.to_string_lossy());
        Some(lib)
    })
    .as_ref()
}

/// The FFmpeg codec id of a Kyokai codec name.
fn codec_id(name: &str) -> Option<c_int> {
    match name {
        "h264" => Some(H264),
        "hevc" => Some(HEVC),
        _ => None,
    }
}

/// The codecs the system's FFmpeg decodes, by name (`h264`, `hevc`).
pub fn decodable() -> Vec<&'static str> {
    let Some(lib) = lib() else {
        return Vec::new();
    };
    ["h264", "hevc"]
        .into_iter()
        // SAFETY: a lookup by id; FFmpeg returns null for a decoder it was
        // built without.
        .filter(|n| !unsafe { (lib.find_decoder)(codec_id(n).expect("named")) }.is_null())
        .collect()
}

/// One stream's decoder: a codec context on one thread (FFmpeg's
/// default), low delay, so each frame comes out as it goes in.
pub struct Decoder {
    lib: &'static Lib,
    ctx: Ptr,
    packet: Ptr,
    frame: Ptr,
    /// The frame with libavcodec's zero padding after it.
    padded: Vec<u8>,
}

impl Decoder {
    /// Opens a decoder for `name` (`h264` or `hevc`).
    /// Returns: the decoder, or why FFmpeg cannot.
    pub fn new(name: &str) -> Result<Decoder, String> {
        let lib = lib().ok_or("FFmpeg (libavcodec) is not installed")?;
        let id = codec_id(name).ok_or("not an FFmpeg codec")?;
        // SAFETY: FFmpeg's allocators; every pointer is checked before use
        // and freed in Drop (the free functions take null).
        unsafe {
            let codec = (lib.find_decoder)(id);
            if codec.is_null() {
                return Err(format!("this FFmpeg has no {name} decoder"));
            }
            let d = Decoder {
                lib,
                ctx: (lib.alloc_context)(codec),
                packet: (lib.packet_alloc)(),
                frame: (lib.frame_alloc)(),
                padded: Vec::new(),
            };
            if d.ctx.is_null() || d.packet.is_null() || d.frame.is_null() {
                return Err("FFmpeg could not allocate a decoder".into());
            }
            (lib.opt_set)(d.ctx, c"flags".as_ptr(), c"+low_delay".as_ptr(), 0);
            let opened = (lib.open)(d.ctx, codec, std::ptr::null_mut());
            if opened < 0 {
                return Err(format!("FFmpeg did not open {name} ({opened})"));
            }
            Ok(d)
        }
    }

    /// Decodes one frame (Annex B) into `rgba` (resized to width×height×4).
    /// Returns: the picture's size when one came out, None while FFmpeg
    /// waits for more.
    pub fn decode(
        &mut self,
        frame: &[u8],
        rgba: &mut Vec<u8>,
    ) -> Result<Option<(u32, u32)>, String> {
        self.padded.clear();
        self.padded.extend_from_slice(frame);
        self.padded.resize(frame.len() + PADDING, 0);
        let size = c_int::try_from(frame.len()).map_err(|_| "frame too large")?;
        // SAFETY: the packet's data points at `padded`, which outlives the
        // send (FFmpeg copies a packet without a buffer reference); the
        // fields written are AVPacket's `data` and `size`.
        let sent = unsafe {
            let p = self.packet.cast::<u8>();
            p.add(PACKET_DATA)
                .cast::<*const u8>()
                .write(self.padded.as_ptr());
            p.add(PACKET_SIZE).cast::<c_int>().write(size);
            let r = (self.lib.send_packet)(self.ctx, self.packet);
            p.add(PACKET_DATA)
                .cast::<*const u8>()
                .write(std::ptr::null());
            p.add(PACKET_SIZE).cast::<c_int>().write(0);
            r
        };
        if sent < 0 && sent != AGAIN {
            return Err(format!("FFmpeg refused the frame ({sent})"));
        }
        let mut out = None;
        loop {
            // SAFETY: the context and frame are this decoder's; a received
            // frame's planes stay valid until the next receive.
            let got = unsafe { (self.lib.receive_frame)(self.ctx, self.frame) };
            if got == AGAIN {
                return Ok(out);
            }
            if got < 0 {
                return Err(format!("FFmpeg did not decode ({got})"));
            }
            out = Some(self.picture(rgba)?);
        }
    }

    /// Converts the received frame into RGBA.
    fn picture(&self, rgba: &mut Vec<u8>) -> Result<(u32, u32), String> {
        // SAFETY: AVFrame's leading fields (see the offsets); for 4:2:0 the
        // three planes hold `height` and `height/2` rows of their line size.
        unsafe {
            let f = self.frame.cast::<u8>();
            let read = |at: usize| f.add(at).cast::<c_int>().read();
            let (w, h, format) = (read(FRAME_WIDTH), read(FRAME_HEIGHT), read(FRAME_FORMAT));
            if format != YUV420P && format != YUVJ420P {
                return Err(format!("a picture format Kyokai does not show ({format})"));
            }
            let (w, h) = (w.max(0) as usize, h.max(0) as usize);
            let data = f.cast::<*const u8>();
            let mut planes: [&[u8]; 3] = [&[]; 3];
            let mut strides = [0usize; 3];
            for i in 0..3 {
                let stride = read(FRAME_LINESIZE + i * 4).max(0) as usize;
                let rows = if i == 0 { h } else { h.div_ceil(2) };
                planes[i] = std::slice::from_raw_parts(data.add(i).read(), stride * rows);
                strides[i] = stride;
            }
            crate::video::planar_to_rgba(planes, strides, w, h, false, rgba);
            Ok((w as u32, h as u32))
        }
    }
}

impl Drop for Decoder {
    fn drop(&mut self) {
        // SAFETY: FFmpeg's free functions, each given its own allocation
        // (or null) once.
        unsafe {
            (self.lib.frame_free)(&mut self.frame);
            (self.lib.packet_free)(&mut self.packet);
            (self.lib.free_context)(&mut self.ctx);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A frame of the GPU's H.264 comes back as its picture through the
    /// system's FFmpeg (where both are there).
    #[test]
    fn the_gpu_s_h264_decodes_through_ffmpeg() {
        if !crate::va::h264() || !decodable().contains(&"h264") {
            eprintln!("no VA-API H.264 encoder or no FFmpeg H.264 decoder here");
            return;
        }
        let (w, h) = (320usize, 180usize);
        let mut enc = crate::va::Encoder::new("h264", w as u32, h as u32, 30, 2_000_000).unwrap();
        let mut bgra = vec![0u8; w * h * 4];
        for (p, px) in bgra.as_chunks_mut::<4>().0.iter_mut().enumerate() {
            *px = if p % w < w / 2 {
                [40, 80, 200, 255]
            } else {
                [220; 4]
            };
        }
        let mut dec = Decoder::new("h264").unwrap();
        let mut rgba = Vec::new();
        for _ in 0..3 {
            enc.picture(&bgra, w, h, w * 4);
            let (frame, _) = enc.encode(false).unwrap().pop().unwrap();
            assert_eq!(
                dec.decode(&frame, &mut rgba).unwrap(),
                Some((w as u32, h as u32))
            );
        }
        let at = |x: usize, y: usize| &rgba[(y * w + x) * 4..][..4];
        for (x, want) in [(20, [200, 80, 40, 255]), (300, [220, 220, 220, 255])] {
            let got = at(x, 90);
            assert!(
                got.iter().zip(want).all(|(&g, w)| g.abs_diff(w) < 12),
                "{got:?} {want:?}"
            );
        }
    }
}
