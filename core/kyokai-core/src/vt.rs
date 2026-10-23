//! VideoToolbox (macOS): H.264 and HEVC encoding and decoding in the Mac's
//! media engine (doc/en-us/computer-use.md, "Video"). Pictures cross as NV12
//! (BT.601, limited range), converted by `video` as on the Media Foundation
//! path, so colours match whichever side decodes; the stream is Annex B
//! (`annexb`), so a Mac decodes a Windows host's stream and the other way
//! round.

use crate::annexb::{self, Syntax};
use crate::video::{self, Picture};
use objc2_core_foundation::{
    CFArray, CFBoolean, CFDictionary, CFNumber, CFRetained, CFString, CFType, kCFAllocatorNull,
};
use objc2_core_media::{
    CMBlockBuffer, CMFormatDescription, CMSampleBuffer, CMTime, CMTimeFlags, CMVideoCodecType,
    CMVideoFormatDescriptionCreateFromH264ParameterSets,
    CMVideoFormatDescriptionCreateFromHEVCParameterSets,
    CMVideoFormatDescriptionGetH264ParameterSetAtIndex,
    CMVideoFormatDescriptionGetHEVCParameterSetAtIndex, kCMTimeInvalid, kCMVideoCodecType_H264,
    kCMVideoCodecType_HEVC,
};
use objc2_core_video::{
    CVImageBuffer, CVPixelBuffer, CVPixelBufferGetBaseAddressOfPlane,
    CVPixelBufferGetBytesPerRowOfPlane, CVPixelBufferGetHeight, CVPixelBufferGetHeightOfPlane,
    CVPixelBufferGetWidth, CVPixelBufferLockBaseAddress, CVPixelBufferLockFlags, CVPixelBufferPool,
    CVPixelBufferUnlockBaseAddress, kCVPixelBufferHeightKey, kCVPixelBufferPixelFormatTypeKey,
    kCVPixelBufferWidthKey, kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange, kCVReturnSuccess,
};
use objc2_video_toolbox::{
    VTCompressionSession, VTCopyVideoEncoderList, VTDecodeFrameFlags, VTDecodeInfoFlags,
    VTDecompressionOutputCallbackRecord, VTDecompressionSession, VTEncodeInfoFlags,
    VTIsHardwareDecodeSupported, VTSessionCopyProperty, VTSessionSetProperty,
    kVTCompressionPropertyKey_AllowFrameReordering, kVTCompressionPropertyKey_AverageBitRate,
    kVTCompressionPropertyKey_DataRateLimits, kVTCompressionPropertyKey_ExpectedFrameRate,
    kVTCompressionPropertyKey_MaxKeyFrameInterval,
    kVTCompressionPropertyKey_MaxKeyFrameIntervalDuration,
    kVTCompressionPropertyKey_PrioritizeEncodingSpeedOverQuality,
    kVTCompressionPropertyKey_ProfileLevel, kVTCompressionPropertyKey_RealTime,
    kVTDecompressionPropertyKey_UsingHardwareAcceleratedVideoDecoder,
    kVTEncodeFrameOptionKey_ForceKeyFrame, kVTProfileLevel_H264_High_AutoLevel,
    kVTProfileLevel_HEVC_Main_AutoLevel, kVTVideoEncoderList_CodecType,
    kVTVideoEncoderList_IsHardwareAccelerated,
    kVTVideoEncoderSpecification_EnableLowLatencyRateControl,
    kVTVideoEncoderSpecification_RequireHardwareAcceleratedVideoEncoder,
};
use std::ffi::c_void;
use std::ptr::NonNull;
use std::sync::Mutex;

/// A VideoToolbox codec, by the name the wire and the stream description
/// use (the same as Media Foundation's: the streams interoperate).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Codec {
    H264,
    Hevc,
}

impl Codec {
    /// Every codec, in the host's order of preference.
    pub const ALL: [Codec; 2] = [Codec::H264, Codec::Hevc];

    /// The name the wire and the stream description use.
    pub fn name(self) -> &'static str {
        match self {
            Codec::H264 => "h264",
            Codec::Hevc => "hevc",
        }
    }

    /// The codec a name stands for.
    pub fn from_name(name: &str) -> Option<Codec> {
        Codec::ALL.into_iter().find(|c| c.name() == name)
    }

    fn video_type(self) -> CMVideoCodecType {
        match self {
            Codec::H264 => kCMVideoCodecType_H264,
            Codec::Hevc => kCMVideoCodecType_HEVC,
        }
    }

    fn syntax(self) -> Syntax {
        match self {
            Codec::H264 => Syntax::H264,
            Codec::Hevc => Syntax::Hevc,
        }
    }
}

/// The encoders VideoToolbox lists: each one's codec type and whether it
/// runs in hardware.
fn encoders() -> Vec<(CMVideoCodecType, bool)> {
    let mut list: *const CFArray = std::ptr::null();
    // SAFETY: the out pointer receives a retained array (the copy rule).
    if unsafe { VTCopyVideoEncoderList(None, NonNull::from(&mut list)) } != 0 {
        return Vec::new();
    }
    let Some(list) = NonNull::new(list.cast_mut()) else {
        return Vec::new();
    };
    // SAFETY: the copy function handed over a +1 reference.
    let list = unsafe { CFRetained::from_raw(list) };
    // SAFETY: the encoder list holds one dictionary per encoder.
    let list = unsafe { list.cast_unchecked::<CFDictionary<CFString, CFType>>() };
    // SAFETY: immutable constants of the framework.
    let (codec_key, hardware_key) = unsafe {
        (
            kVTVideoEncoderList_CodecType,
            kVTVideoEncoderList_IsHardwareAccelerated,
        )
    };
    list.iter()
        .filter_map(|e| {
            let codec = e
                .get(codec_key)
                .and_then(|v| v.downcast::<CFNumber>().ok())
                .and_then(|n| n.as_i64())?;
            let hardware = e
                .get(hardware_key)
                .and_then(|v| v.downcast::<CFBoolean>().ok())
                .is_some_and(|b| b.as_bool());
            Some((codec as CMVideoCodecType, hardware))
        })
        .collect()
}

/// Whether this Mac encodes HEVC in hardware (the host then prefers it).
pub fn hevc_hardware() -> bool {
    encoders().contains(&(kCMVideoCodecType_HEVC, true))
}

/// The codecs VideoToolbox encodes here.
pub fn encodable() -> Vec<Codec> {
    let have = encoders();
    Codec::ALL
        .into_iter()
        .filter(|c| have.iter().any(|e| e.0 == c.video_type()))
        .collect()
}

/// The codecs VideoToolbox decodes here: H.264 always, HEVC where the
/// media engine does.
pub fn decodable() -> Vec<Codec> {
    // SAFETY: a plain query of the system's decoders.
    let hevc = unsafe { VTIsHardwareDecodeSupported(kCMVideoCodecType_HEVC) };
    Codec::ALL
        .into_iter()
        .filter(|&c| c == Codec::H264 || hevc)
        .collect()
}

/// Pixel buffer attributes: NV12 (video range), of a size when given.
fn nv12_attributes(size: Option<(u32, u32)>) -> CFRetained<CFDictionary<CFString, CFNumber>> {
    let format = CFNumber::new_i32(kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange as i32);
    // SAFETY: immutable constants of the framework.
    let (format_key, width_key, height_key) = unsafe {
        (
            kCVPixelBufferPixelFormatTypeKey,
            kCVPixelBufferWidthKey,
            kCVPixelBufferHeightKey,
        )
    };
    match size {
        Some((w, h)) => {
            let (w, h) = (CFNumber::new_i32(w as i32), CFNumber::new_i32(h as i32));
            CFDictionary::from_slices(&[format_key, width_key, height_key], &[&format, &w, &h])
        }
        None => CFDictionary::from_slices(&[format_key], &[&format]),
    }
}

/// Sets a session property; one the encoder does not have is left alone.
/// Reads a property of a VideoToolbox session.
/// Returns: its value, or None when the session does not have it.
fn property(session: &CFType, key: &CFString) -> Option<CFRetained<CFType>> {
    let mut value: *const CFType = std::ptr::null();
    // SAFETY: a VideoToolbox session and a key of its properties; the out
    // pointer receives a retained value or stays null.
    let status = unsafe { VTSessionCopyProperty(session, key, None, (&raw mut value).cast()) };
    let value = NonNull::new(value.cast_mut()).filter(|_| status == 0)?;
    // SAFETY: the copy function handed over a +1 reference.
    Some(unsafe { CFRetained::from_raw(value) })
}

fn set(session: &CFType, key: &CFString, value: &CFType) {
    // SAFETY: a VideoToolbox session, a key of its properties and a value
    // of the key's type.
    let _ = unsafe { VTSessionSetProperty(session, key, Some(value)) };
}

/// The two planes of an NV12 pixel buffer, locked for the call.
/// Returns: what `f` returned, or None when the buffer cannot be locked.
fn with_planes<T>(
    pb: &CVPixelBuffer,
    flags: CVPixelBufferLockFlags,
    f: impl FnOnce(&mut [u8], usize, &mut [u8], usize) -> T,
) -> Option<T> {
    // SAFETY: locks a valid pixel buffer; unlocked below.
    if unsafe { CVPixelBufferLockBaseAddress(pb, flags) } != kCVReturnSuccess {
        return None;
    }
    let plane = |i: usize| {
        let base = CVPixelBufferGetBaseAddressOfPlane(pb, i).cast::<u8>();
        let stride = CVPixelBufferGetBytesPerRowOfPlane(pb, i);
        let rows = CVPixelBufferGetHeightOfPlane(pb, i);
        if base.is_null() {
            return None;
        }
        // SAFETY: while the buffer is locked, a plane's base points at its
        // rows of `stride` bytes; the two planes do not overlap.
        let plane = unsafe { std::slice::from_raw_parts_mut(base, stride * rows) };
        Some((plane, stride))
    };
    let out = match (plane(0), plane(1)) {
        (Some((y, ys)), Some((uv, uvs))) => Some(f(y, ys, uv, uvs)),
        _ => None,
    };
    // SAFETY: balances the lock above.
    unsafe { CVPixelBufferUnlockBaseAddress(pb, flags) };
    out
}

/// What an encoder's output callback fills: each frame in Annex B and
/// whether it is a key frame.
struct Encoded {
    syntax: Syntax,
    units: Mutex<Vec<(Vec<u8>, bool)>>,
}

/// A VideoToolbox encoder of one stream, with its next picture.
pub struct Encoder {
    session: CFRetained<VTCompressionSession>,
    /// The callback's target; it lives as long as the session (dropped
    /// after it, see Drop).
    out: Box<Encoded>,
    width: usize,
    height: usize,
    fps: i32,
    /// Frames encoded: the next presentation time, in frames.
    n: i64,
    /// The picture to encode next, from the session's pool.
    picture: Option<CFRetained<CVPixelBuffer>>,
    /// Whether the session had to be on the hardware encoder.
    hardware: bool,
}

impl Encoder {
    /// Opens an encoder for `width`×`height` NV12 pictures.
    /// Inputs: `bitrate` in bit/s; `fps` is the rate the encoder plans for.
    /// Returns: the encoder, or why VideoToolbox refused it.
    /// Notes: the hardware encoder where there is one, with low-latency rate
    /// control where it offers it; real time, no frame reordering; a key
    /// frame every 4 seconds and on request.
    pub fn new(
        codec: Codec,
        width: u32,
        height: u32,
        fps: u32,
        bitrate: u32,
    ) -> Result<Encoder, String> {
        let out = Box::new(Encoded {
            syntax: codec.syntax(),
            units: Mutex::default(),
        });
        let source = nv12_attributes(Some((width, height)));
        // The hardware encoder, with low-latency rate control if it takes
        // it, then without; any encoder where there is no hardware. Only a
        // session that had to be hardware is said to be: the low-latency
        // encoder (rtvc) runs on the hardware but the encoder list calls it
        // software, and the session does not say.
        let (session, hardware) = [(true, true), (true, false), (false, false)]
            .into_iter()
            .find_map(|(hardware, low_latency)| {
                Self::create(codec, width, height, &source, &out, hardware, low_latency)
                    .ok()
                    .map(|s| (s, hardware))
            })
            .ok_or_else(|| format!("VideoToolbox has no {} encoder here", codec.name()))?;
        let fps = fps.max(1) as i32;
        // SAFETY: immutable constants of the framework.
        unsafe {
            set(
                &session,
                kVTCompressionPropertyKey_RealTime,
                CFBoolean::new(true),
            );
            set(
                &session,
                kVTCompressionPropertyKey_AllowFrameReordering,
                CFBoolean::new(false),
            );
            set(
                &session,
                kVTCompressionPropertyKey_PrioritizeEncodingSpeedOverQuality,
                CFBoolean::new(true),
            );
            let level = match codec {
                Codec::H264 => kVTProfileLevel_H264_High_AutoLevel,
                Codec::Hevc => kVTProfileLevel_HEVC_Main_AutoLevel,
            };
            set(&session, kVTCompressionPropertyKey_ProfileLevel, level);
            set(
                &session,
                kVTCompressionPropertyKey_ExpectedFrameRate,
                &CFNumber::new_i32(fps),
            );
            set(
                &session,
                kVTCompressionPropertyKey_MaxKeyFrameInterval,
                &CFNumber::new_i32(fps * 4),
            );
            set(
                &session,
                kVTCompressionPropertyKey_MaxKeyFrameIntervalDuration,
                &CFNumber::new_f64(4.0),
            );
        }
        let enc = Encoder {
            session,
            out,
            width: width as usize,
            height: height as usize,
            fps,
            n: 0,
            picture: None,
            hardware,
        };
        enc.set_bitrate(bitrate);
        // SAFETY: a plain call on the session; it allocates the encoder's
        // resources now rather than at the first frame.
        unsafe { enc.session.prepare_to_encode_frames() };
        Ok(enc)
    }

    /// Whether the session encodes in hardware: it was created to be.
    pub fn hardware(&self) -> bool {
        self.hardware
    }

    /// Creates the compression session: only on the hardware encoder when
    /// `hardware`, with low-latency rate control when `low_latency`.
    fn create(
        codec: Codec,
        width: u32,
        height: u32,
        source: &CFDictionary<CFString, CFNumber>,
        out: &Encoded,
        hardware: bool,
        low_latency: bool,
    ) -> Result<CFRetained<VTCompressionSession>, String> {
        // SAFETY: immutable constants of the framework.
        let (require, latency) = unsafe {
            (
                kVTVideoEncoderSpecification_RequireHardwareAcceleratedVideoEncoder,
                kVTVideoEncoderSpecification_EnableLowLatencyRateControl,
            )
        };
        let keys: Vec<&CFString> = [(hardware, require), (low_latency, latency)]
            .into_iter()
            .filter_map(|(on, key)| on.then_some(key))
            .collect();
        let on = CFBoolean::new(true);
        let spec = CFDictionary::from_slices(&keys, &vec![on; keys.len()]);
        let mut session: *mut VTCompressionSession = std::ptr::null_mut();
        // SAFETY: valid dictionaries; the callback's refcon is `out`, which
        // the encoder keeps until the session is invalidated; the out
        // pointer receives a retained session.
        let status = unsafe {
            VTCompressionSession::create(
                None,
                width as i32,
                height as i32,
                codec.video_type(),
                Some(spec.as_ref()),
                Some(source.as_ref()),
                None,
                Some(encoded),
                out as *const Encoded as *mut c_void,
                NonNull::from(&mut session),
            )
        };
        match NonNull::new(session) {
            // SAFETY: the create function handed over a +1 reference.
            Some(s) if status == 0 => Ok(unsafe { CFRetained::from_raw(s) }),
            _ => Err(format!("VideoToolbox {}: {status}", codec.name())),
        }
    }

    /// Takes a captured BGRA picture as the next one to encode, converted
    /// to NV12 of the stream's size in a buffer from the session's pool.
    pub fn picture(&mut self, bgra: &[u8], width: usize, height: usize, stride: usize) {
        // SAFETY: a plain query of the session.
        let Some(pool) = (unsafe { self.session.pixel_buffer_pool() }) else {
            return;
        };
        let mut pb: *mut CVPixelBuffer = std::ptr::null_mut();
        // SAFETY: the out pointer receives a retained buffer from the pool.
        if unsafe { CVPixelBufferPool::create_pixel_buffer(None, &pool, NonNull::from(&mut pb)) }
            != kCVReturnSuccess
        {
            return;
        }
        let Some(pb) = NonNull::new(pb) else {
            return;
        };
        // SAFETY: the pool handed over a +1 reference.
        let pb = unsafe { CFRetained::from_raw(pb) };
        let src = Picture {
            bgra,
            width,
            height,
            stride,
        };
        let (w, h) = (self.width, self.height);
        let done = with_planes(&pb, CVPixelBufferLockFlags(0), |y, ys, uv, uvs| {
            video::bgra_to_nv12_planes(src, w, h, y, ys, uv, uvs);
        });
        if done.is_some() {
            self.picture = Some(pb);
        }
    }

    /// Whether a picture was taken yet.
    pub fn has_picture(&self) -> bool {
        self.picture.is_some()
    }

    /// Encodes the last picture; `key` forces a key frame.
    /// Returns: the frames that came out (Annex B), each with whether it is
    /// a key frame.
    /// Notes: waits until the frame is out, which without reordering is at
    /// once.
    pub fn encode(&mut self, key: bool) -> Result<Vec<(Vec<u8>, bool)>, String> {
        let Some(picture) = &self.picture else {
            return Ok(Vec::new());
        };
        let pts = CMTime {
            value: self.n,
            timescale: self.fps,
            flags: CMTimeFlags::Valid,
            epoch: 0,
        };
        // SAFETY: an immutable constant of the framework.
        let force_key = unsafe { kVTEncodeFrameOptionKey_ForceKeyFrame };
        let options = key.then(|| CFDictionary::from_slices(&[force_key], &[CFBoolean::new(true)]));
        // SAFETY: a picture from the session's pool, a rising presentation
        // time and optional frame options; the output goes to the callback.
        let status = unsafe {
            self.session.encode_frame(
                picture,
                pts,
                kCMTimeInvalid,
                options.as_deref().map(|d| d.as_ref()),
                std::ptr::null_mut(),
                std::ptr::null_mut(),
            )
        };
        if status != 0 {
            return Err(format!("VideoToolbox: {status}"));
        }
        // SAFETY: waits for the frames up to this one; the callback has run
        // for them when it returns.
        unsafe { self.session.complete_frames(pts) };
        self.n += 1;
        let mut units = self.out.units.lock().map_err(|_| "encoder poisoned")?;
        Ok(std::mem::take(&mut *units))
    }

    /// Sets the target bitrate in bit/s: the average, and at most one and a
    /// half times that in any second, so that a picture after a still screen
    /// stays small enough to arrive quickly.
    pub fn set_bitrate(&self, bitrate: u32) {
        let average = CFNumber::new_i32(bitrate.min(i32::MAX as u32) as i32);
        let bytes = CFNumber::new_i64(bitrate as i64 / 8 * 3 / 2);
        let second = CFNumber::new_f64(1.0);
        let limits = CFArray::from_objects(&[&*bytes, &*second]);
        // SAFETY: immutable constants of the framework.
        unsafe {
            set(
                &self.session,
                kVTCompressionPropertyKey_AverageBitRate,
                &average,
            );
            set(
                &self.session,
                kVTCompressionPropertyKey_DataRateLimits,
                &limits,
            );
        }
    }
}

impl Drop for Encoder {
    fn drop(&mut self) {
        // SAFETY: ends the session before `out`, its callback's target, is
        // dropped; the session is not used afterwards.
        unsafe { self.session.invalidate() };
    }
}

/// The compression session's output callback: the frame in Annex B, the
/// parameter sets before a key frame.
///
/// # Safety
/// `refcon` is the encoder's `Encoded`; `sample` is null (a dropped frame)
/// or valid for the call.
unsafe extern "C-unwind" fn encoded(
    refcon: *mut c_void,
    _frame: *mut c_void,
    status: i32,
    _flags: VTEncodeInfoFlags,
    sample: *mut CMSampleBuffer,
) {
    // SAFETY: the caller's contract above.
    let (out, sample) = unsafe { (&*(refcon as *const Encoded), sample.as_ref()) };
    if status != 0 {
        return;
    }
    crate::guarded(|| {
        if let Some(frame) = sample.and_then(|s| annex_b(out.syntax, s))
            && let Ok(mut units) = out.units.lock()
        {
            units.push(frame);
        }
    });
}

/// A compressed sample as Annex B, and whether it is a key frame (the
/// parameter sets go first then).
fn annex_b(syntax: Syntax, sample: &CMSampleBuffer) -> Option<(Vec<u8>, bool)> {
    // SAFETY: plain reads of a valid sample buffer.
    let (data, desc) = unsafe { (sample.data_buffer()?, sample.format_description()?) };
    // SAFETY: a plain read of a valid block buffer.
    let len = unsafe { data.data_length() };
    let mut bytes = vec![0u8; len];
    // SAFETY: copies `len` bytes into a buffer of that size.
    if unsafe { data.copy_data_bytes(0, len, NonNull::new(bytes.as_mut_ptr().cast())?) } != 0 {
        return None;
    }
    let (sets, len_size) = parameter_sets(syntax, &desc)?;
    let key = annexb::length_prefixed(&bytes, len_size).any(|u| syntax.key(u));
    let mut out = Vec::with_capacity(len + 256);
    if key {
        for s in &sets {
            annexb::append_annexb(s, &mut out);
        }
    }
    for u in annexb::length_prefixed(&bytes, len_size) {
        annexb::append_annexb(u, &mut out);
    }
    Some((out, key))
}

/// A format description's parameter sets and the length size of its
/// samples' units.
/// Notes: the sets borrow the description's memory.
fn parameter_sets(syntax: Syntax, desc: &CMFormatDescription) -> Option<(Vec<&[u8]>, usize)> {
    let get = match syntax {
        Syntax::H264 => CMVideoFormatDescriptionGetH264ParameterSetAtIndex,
        Syntax::Hevc => CMVideoFormatDescriptionGetHEVCParameterSetAtIndex,
    };
    let (mut count, mut len_size) = (0usize, 0i32);
    // SAFETY: asks for the count and the length size only.
    if unsafe {
        get(
            desc,
            0,
            std::ptr::null_mut(),
            std::ptr::null_mut(),
            &mut count,
            &mut len_size,
        )
    } != 0
    {
        return None;
    }
    let mut sets = Vec::with_capacity(count);
    for i in 0..count {
        let (mut ptr, mut size): (*const u8, usize) = (std::ptr::null(), 0);
        // SAFETY: `ptr` points into the description's memory, valid while
        // the caller holds `desc`.
        if unsafe {
            get(
                desc,
                i,
                &mut ptr,
                &mut size,
                std::ptr::null_mut(),
                std::ptr::null_mut(),
            )
        } != 0
            || ptr.is_null()
        {
            return None;
        }
        // SAFETY: `size` bytes at `ptr`, as the description reported.
        sets.push(unsafe { std::slice::from_raw_parts(ptr, size) });
    }
    Some((sets, len_size.max(1) as usize))
}

/// What a decoder's output callback keeps: the newest picture.
struct Decoded(Mutex<Option<CFRetained<CVPixelBuffer>>>);

/// A VideoToolbox decoder of one stream (Annex B in, RGBA out). The session
/// starts with the stream's first parameter sets and starts again when they
/// change in a way it cannot follow.
pub struct Decoder {
    syntax: Syntax,
    session: Option<CFRetained<VTDecompressionSession>>,
    format: Option<CFRetained<CMFormatDescription>>,
    /// The parameter sets `format` was made from.
    sets: Vec<Vec<u8>>,
    /// The callback's target, kept as long as the session.
    out: Box<Decoded>,
    /// The frame's units, length-prefixed (reused between frames).
    sample: Vec<u8>,
}

// SAFETY: VideoToolbox sessions and Core Media objects may be used from any
// thread; the viewer drives a decoder from one thread at a time.
unsafe impl Send for Decoder {}

impl Decoder {
    /// A decoder for `codec`; its session starts with the first parameter
    /// sets the stream brings.
    pub fn new(codec: Codec) -> Decoder {
        Decoder {
            syntax: codec.syntax(),
            session: None,
            format: None,
            sets: Vec::new(),
            out: Box::new(Decoded(Mutex::new(None))),
            sample: Vec::new(),
        }
    }

    /// Whether the session decodes in hardware, as it reports; false before
    /// the first key frame opened it.
    pub fn hardware(&self) -> bool {
        self.session.as_ref().is_some_and(|s| {
            // SAFETY: an immutable constant of the framework.
            property(s, unsafe {
                kVTDecompressionPropertyKey_UsingHardwareAcceleratedVideoDecoder
            })
            .and_then(|v| v.downcast::<CFBoolean>().ok())
            .is_some_and(|b| b.as_bool())
        })
    }

    /// Decodes one Annex B frame into `rgba` (resized to width×height×4).
    /// Returns: the picture's size, None for a frame before the first key
    /// frame with its parameter sets, or an error for a key frame that comes
    /// before any parameter set.
    pub fn decode(
        &mut self,
        frame: &[u8],
        rgba: &mut Vec<u8>,
    ) -> Result<Option<(u32, u32)>, String> {
        let syntax = self.syntax;
        let sets: Vec<&[u8]> = annexb::units(frame)
            .filter(|u| syntax.parameter_set(u))
            .collect();
        if !sets.is_empty() && !sets.iter().copied().eq(self.sets.iter().map(Vec::as_slice)) {
            self.open(&sets)?;
        }
        let (Some(session), Some(format)) = (&self.session, &self.format) else {
            // A key frame before any parameter set cannot open the session,
            // which would take it and every frame after and show nothing, for
            // good: an error, so that the viewer asks for another key frame.
            if annexb::units(frame).any(|u| syntax.key(u)) {
                return Err("a key frame without its parameter sets".into());
            }
            return Ok(None);
        };
        self.sample.clear();
        for u in annexb::units(frame).filter(|u| !syntax.parameter_set(u) && !syntax.delimiter(u)) {
            annexb::append_length_prefixed(u, &mut self.sample);
        }
        if self.sample.is_empty() {
            return Ok(None);
        }
        let len = self.sample.len();
        let mut block: *mut CMBlockBuffer = std::ptr::null_mut();
        // SAFETY: a block buffer over `self.sample`, which it does not free
        // (kCFAllocatorNull) and which outlives the synchronous decode
        // below; the out pointer receives a retained buffer.
        let status = unsafe {
            CMBlockBuffer::create_with_memory_block(
                None,
                self.sample.as_mut_ptr().cast(),
                len,
                kCFAllocatorNull,
                std::ptr::null(),
                0,
                len,
                0,
                NonNull::from(&mut block),
            )
        };
        let Some(block) = NonNull::new(block).filter(|_| status == 0) else {
            return Err("VideoToolbox: no block buffer".into());
        };
        // SAFETY: the create function handed over a +1 reference.
        let block = unsafe { CFRetained::from_raw(block) };
        let mut sample: *mut CMSampleBuffer = std::ptr::null_mut();
        // SAFETY: one sample of `len` bytes in `block`, of `format`; the out
        // pointer receives a retained sample buffer.
        let status = unsafe {
            CMSampleBuffer::create_ready(
                None,
                Some(&block),
                Some(format),
                1,
                0,
                std::ptr::null(),
                1,
                &len,
                NonNull::from(&mut sample),
            )
        };
        let Some(sample) = NonNull::new(sample).filter(|_| status == 0) else {
            return Err("VideoToolbox: no sample buffer".into());
        };
        // SAFETY: the create function handed over a +1 reference.
        let sample = unsafe { CFRetained::from_raw(sample) };
        let mut info = VTDecodeInfoFlags(0);
        // SAFETY: a sample of the session's format; without the asynchronous
        // flag the output callback runs before the call returns.
        let status = unsafe {
            session.decode_frame(
                &sample,
                VTDecodeFrameFlags(0),
                std::ptr::null_mut(),
                &mut info,
            )
        };
        if status != 0 {
            return Err(format!("VideoToolbox: {status}"));
        }
        let Some(pb) = self.out.0.lock().ok().and_then(|mut p| p.take()) else {
            return Ok(None);
        };
        let (w, h) = (CVPixelBufferGetWidth(&pb), CVPixelBufferGetHeight(&pb));
        with_planes(&pb, CVPixelBufferLockFlags::ReadOnly, |y, ys, uv, uvs| {
            video::nv12_planes_to_rgba(y, ys, uv, uvs, w, h, rgba);
        })
        .ok_or("VideoToolbox: the picture cannot be read")?;
        Ok(Some((w as u32, h as u32)))
    }

    /// Takes new parameter sets: a new format, and a new session unless the
    /// running one follows the format.
    fn open(&mut self, sets: &[&[u8]]) -> Result<(), String> {
        let format = format_description(self.syntax, sets)?;
        // SAFETY: a plain query of a valid session.
        let follows = self
            .session
            .as_ref()
            .is_some_and(|s| unsafe { s.can_accept_format_description(&format) });
        if !follows {
            if let Some(old) = self.session.take() {
                // SAFETY: the old session is not used afterwards.
                unsafe { old.invalidate() };
            }
            let attributes = nv12_attributes(None);
            let callback = VTDecompressionOutputCallbackRecord {
                decompressionOutputCallback: Some(decoded),
                decompressionOutputRefCon: &*self.out as *const Decoded as *mut c_void,
            };
            let mut session: *mut VTDecompressionSession = std::ptr::null_mut();
            // SAFETY: a valid format and attributes; the callback record is
            // copied, its refcon (`self.out`) lives until the session is
            // invalidated; the out pointer receives a retained session.
            let status = unsafe {
                VTDecompressionSession::create(
                    None,
                    &format,
                    None,
                    Some(attributes.as_ref()),
                    &callback,
                    NonNull::from(&mut session),
                )
            };
            let Some(session) = NonNull::new(session).filter(|_| status == 0) else {
                return Err(format!("VideoToolbox decoder: {status}"));
            };
            // SAFETY: the create function handed over a +1 reference.
            self.session = Some(unsafe { CFRetained::from_raw(session) });
        }
        self.format = Some(format);
        self.sets = sets.iter().map(|s| s.to_vec()).collect();
        Ok(())
    }
}

impl Drop for Decoder {
    fn drop(&mut self) {
        if let Some(s) = self.session.take() {
            // SAFETY: ends the session before `out`, its callback's target,
            // is dropped.
            unsafe { s.invalidate() };
        }
    }
}

/// A format description from a stream's parameter sets.
fn format_description(
    syntax: Syntax,
    sets: &[&[u8]],
) -> Result<CFRetained<CMFormatDescription>, String> {
    let mut pointers: Vec<NonNull<u8>> = sets.iter().map(|s| NonNull::from(*s).cast()).collect();
    let mut sizes: Vec<usize> = sets.iter().map(|s| s.len()).collect();
    let (Some(p), Some(n)) = (
        NonNull::new(pointers.as_mut_ptr()),
        NonNull::new(sizes.as_mut_ptr()),
    ) else {
        return Err("no parameter sets".into());
    };
    let mut desc: *const CMFormatDescription = std::ptr::null();
    // SAFETY: `sets.len()` parameter sets at `p` with their sizes at `n`,
    // read during the call; units of the samples are 4-byte length-prefixed
    // (`append_length_prefixed`); the out pointer receives a retained
    // description.
    let status = unsafe {
        match syntax {
            Syntax::H264 => CMVideoFormatDescriptionCreateFromH264ParameterSets(
                None,
                sets.len(),
                p,
                n,
                4,
                NonNull::from(&mut desc),
            ),
            Syntax::Hevc => CMVideoFormatDescriptionCreateFromHEVCParameterSets(
                None,
                sets.len(),
                p,
                n,
                4,
                None,
                NonNull::from(&mut desc),
            ),
        }
    };
    match NonNull::new(desc.cast_mut()) {
        // SAFETY: the create function handed over a +1 reference.
        Some(d) if status == 0 => Ok(unsafe { CFRetained::from_raw(d) }),
        _ => Err(format!(
            "the stream's parameter sets were refused: {status}"
        )),
    }
}

/// The decompression session's output callback: keeps the picture.
///
/// # Safety
/// `refcon` is the decoder's `Decoded`; `image` is null or valid for the
/// call.
unsafe extern "C-unwind" fn decoded(
    refcon: *mut c_void,
    _frame: *mut c_void,
    status: i32,
    _flags: VTDecodeInfoFlags,
    image: *mut CVImageBuffer,
    _pts: CMTime,
    _duration: CMTime,
) {
    if status != 0 {
        return;
    }
    // SAFETY: the caller's contract above.
    let out = unsafe { &*(refcon as *const Decoded) };
    crate::guarded(|| {
        if let Some(image) = NonNull::new(image)
            && let Ok(mut picture) = out.0.lock()
        {
            // SAFETY: retains the picture past the callback.
            *picture = Some(unsafe { CFRetained::retain(image) });
        }
    });
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A grey picture with a bright square at `n * 8` pixels across.
    fn picture(w: usize, h: usize, n: usize) -> Vec<u8> {
        (0..w * h)
            .flat_map(|i| {
                let (x, y) = (i % w, i / w);
                let on = (x + w - (n * 8) % w) % w < w / 8 && (h / 3..h / 2).contains(&y);
                if on {
                    [240u8, 240, 240, 255]
                } else {
                    [90, 90, 90, 255]
                }
            })
            .collect()
    }

    /// Every codec this Mac encodes round-trips through its decoder: the
    /// first frame is a key frame with its parameter sets, and the picture
    /// comes back grey where it was grey.
    #[test]
    fn each_codec_round_trips() {
        let decodes = decodable();
        eprintln!(
            "VideoToolbox here: encodes {:?}, decodes {decodes:?}, hardware HEVC {}",
            encodable(),
            hevc_hardware()
        );
        for codec in encodable().into_iter().filter(|c| decodes.contains(c)) {
            for (w, h) in [(320, 192), (1920, 1080)] {
                let mut enc = Encoder::new(codec, w, h, 30, 2_000_000)
                    .unwrap_or_else(|e| panic!("{codec:?}: {e}"));
                let mut dec = Decoder::new(codec);
                let mut shown = 0;
                let mut rgba = Vec::new();
                for n in 0..10 {
                    let bgra = picture(w as usize, h as usize, n);
                    enc.picture(&bgra, w as usize, h as usize, w as usize * 4);
                    let units = enc.encode(n == 0).unwrap();
                    if n == 0 {
                        let (first, key) = &units[0];
                        assert!(key, "{codec:?}: the first frame is a key frame");
                        let syntax = codec.syntax();
                        assert!(annexb::units(first).any(|u| syntax.parameter_set(u)));
                    }
                    for (unit, _) in units {
                        if let Some((pw, ph)) = dec.decode(&unit, &mut rgba).unwrap() {
                            assert_eq!((pw, ph), (w, h));
                            shown += 1;
                        }
                    }
                }
                assert!(shown >= 9, "{codec:?} {w}×{h}: {shown} of 10 pictures");
                // The sessions say how they run: an encoder listed in
                // hardware is used in hardware.
                eprintln!(
                    "{codec:?} {w}×{h}: encodes in hardware {}, decodes in hardware {}",
                    enc.hardware(),
                    dec.hardware()
                );
                if codec == Codec::Hevc {
                    assert_eq!(enc.hardware(), hevc_hardware());
                }
                let corner = &rgba[..4];
                assert!(
                    corner[..3].iter().all(|c| (*c as i32 - 90).abs() < 12),
                    "{codec:?}: {corner:?}"
                );
            }
        }
    }

    /// A key frame that reaches a fresh decoder without its parameter sets
    /// is an error, for the viewer to ask for another, not a silent wait;
    /// the whole key frame then opens the session.
    #[test]
    fn a_key_frame_without_its_parameter_sets_is_an_error() {
        let (w, h) = (320usize, 192usize);
        let mut enc = Encoder::new(Codec::H264, w as u32, h as u32, 30, 2_000_000).unwrap();
        enc.picture(&picture(w, h, 0), w, h, w * 4);
        let (key, _) = enc
            .encode(true)
            .unwrap()
            .into_iter()
            .find(|(_, key)| *key)
            .expect("a key frame");
        let syntax = Codec::H264.syntax();
        let mut bare = Vec::new();
        for u in annexb::units(&key).filter(|u| !syntax.parameter_set(u)) {
            annexb::append_annexb(u, &mut bare);
        }
        let mut dec = Decoder::new(Codec::H264);
        let mut rgba = Vec::new();
        assert!(dec.decode(&bare, &mut rgba).is_err());
        assert_eq!(
            dec.decode(&key, &mut rgba).unwrap(),
            Some((w as u32, h as u32))
        );
    }

    #[test]
    fn h264_is_always_there() {
        assert!(encodable().contains(&Codec::H264));
        assert!(decodable().contains(&Codec::H264));
        assert_eq!(Codec::from_name("hevc"), Some(Codec::Hevc));
        assert_eq!(Codec::from_name("vp9"), None);
    }
}
