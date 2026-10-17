// RustDesk's software video codecs (libs/scrap/src/common/{vpxcodec,vpx,aom}.rs)
// as a crate of their own. Modified by Kyokai: this crate root replaces
// scrap's lib.rs and common module with the few items the codec files use
// (error type, call macros, picture layout, decoded image access), taken
// from libs/scrap/src/common/mod.rs and codec.rs.
#![allow(clippy::all, unsafe_op_in_unsafe_fn)]

pub mod aom;
pub mod vpx;
pub mod vpxcodec;

/// The stride alignment RustDesk gives libvpx and libaom pictures.
pub const STRIDE_ALIGN: usize = 64; // commonly used in libvpx vpx_img_alloc caller

/// A codec call that failed: which call, and libvpx's or libaom's code.
#[derive(Debug)]
pub enum Error {
    FailedCall(String),
    BadPtr(String),
}

impl std::fmt::Display for Error {
    fn fmt(&self, f: &mut std::fmt::Formatter) -> std::result::Result<(), std::fmt::Error> {
        write!(f, "{:?}", self)
    }
}

impl std::error::Error for Error {}

pub type Result<T> = std::result::Result<T, Error>;

#[macro_export]
macro_rules! generate_call_macro {
    ($func_name:ident, $allow_err:expr) => {
        macro_rules! $func_name {
            ($x:expr) => {{
                let result = unsafe { $x };
                let result_int = unsafe { std::mem::transmute::<_, i32>(result) };
                if result_int != 0 {
                    let message = format!(
                        "errcode={} {}:{}:{}:{}",
                        result_int,
                        module_path!(),
                        file!(),
                        line!(),
                        column!()
                    );
                    // Modified by Kyokai: a tolerated failure is ignored
                    // (RustDesk logs it; Kyokai's core has no logger).
                    if !$allow_err {
                        return Err(crate::Error::FailedCall(message).into());
                    }
                }
                result
            }};
        }
    };
}

#[macro_export]
macro_rules! generate_call_ptr_macro {
    ($func_name:ident) => {
        macro_rules! $func_name {
            ($x:expr) => {{
                let result = unsafe { $x };
                let result_int = unsafe { std::mem::transmute::<_, isize>(result) };
                if result_int == 0 {
                    return Err(crate::Error::BadPtr(format!(
                        "errcode={} {}:{}:{}:{}",
                        result_int,
                        module_path!(),
                        file!(),
                        line!(),
                        column!()
                    ))
                    .into());
                }
                result
            }};
        }
    };
}

/// Chroma subsampling of a picture: 4:2:0, or full colour 4:4:4.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Chroma {
    I420,
    I444,
}

/// Pixel formats an encoder takes.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Pixfmt {
    I420,
    I444,
}

/// The planar layout an encoder expects its input in: plane strides and the
/// U and V planes' offsets from the start of the buffer.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct EncodeYuvFormat {
    pub pixfmt: Pixfmt,
    pub w: usize,
    pub h: usize,
    pub stride: Vec<usize>,
    pub u: usize,
    pub v: usize,
}

/// One encoded frame, borrowed from the encoder until the next call.
#[derive(Clone, Copy, Debug)]
pub struct EncodeFrame<'a> {
    /// Compressed data.
    pub data: &'a [u8],
    /// Whether the frame is a keyframe.
    pub key: bool,
    /// Presentation timestamp (in timebase units).
    pub pts: i64,
}

/// A decoded picture's planes. Modified by Kyokai: the conversion to RGB
/// through libyuv is left out; Kyokai converts the planes itself.
pub trait GoogleImage {
    fn width(&self) -> usize;
    fn height(&self) -> usize;
    fn stride(&self) -> Vec<i32>;
    fn planes(&self) -> Vec<*mut u8>;
    fn chroma(&self) -> Chroma;
}

/// RustDesk's base bitrate in kbit/s for a picture size (codec.rs); the
/// vendored QoS crate carries the same function.
pub use rustdesk_qos::base_bitrate;

/// How many threads a codec may use, at most `limit`. Modified by Kyokai:
/// half the logical processors instead of RustDesk's measure of CPU load
/// and free memory (sysinfo).
pub fn codec_thread_num(limit: usize) -> usize {
    let max = std::thread::available_parallelism().map_or(1, |n| n.get());
    (max / 2).clamp(1, limit)
}
