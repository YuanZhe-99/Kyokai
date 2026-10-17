// Kyokai shim for vendored RustDesk code (AGPL-3.0, like RustDesk): the
// scrap types and libyuv calls the dxgi module expects.
//! What the vendored RustDesk DXGI code expects from the rest of `scrap`:
//! its frame and buffer types, the adapter description, and libyuv's ARGB
//! rotation and mirroring (here in plain Rust, for whole 32-bit pixels).

use std::ffi::c_void;
use std::io;

/// A captured frame: pixels in memory, or a GPU texture.
pub enum Frame<'a> {
    PixelBuffer(PixelBuffer<'a>),
    #[allow(dead_code)]
    Texture((*mut c_void, usize)),
}

/// BGRA pixels with their row stride.
pub struct PixelBuffer<'a> {
    pub data: &'a [u8],
    pub width: usize,
    pub height: usize,
    pub stride: usize,
}

impl<'a> PixelBuffer<'a> {
    #[allow(non_snake_case)]
    pub fn with_BGRA(data: &'a [u8], width: usize, height: usize) -> Self {
        PixelBuffer {
            data,
            width,
            height,
            stride: data.len() / height.max(1),
        }
    }
}

/// The D3D11 device and adapter a capturer runs on.
#[allow(dead_code)]
pub struct AdapterDevice {
    pub device: *mut c_void,
    pub vendor_id: u32,
    pub luid: i64,
}

/// libyuv's rotation modes.
#[allow(non_camel_case_types, dead_code)]
#[derive(Clone, Copy, PartialEq)]
pub enum RotationMode {
    kRotate0 = 0,
    kRotate90 = 90,
    kRotate180 = 180,
    kRotate270 = 270,
}

/// Reports WouldBlock when the buffer equals the previous one, else keeps a
/// copy of it (scrap's helper).
pub fn would_block_if_equal(old: &mut Vec<u8>, b: &[u8]) -> io::Result<()> {
    if b == &old[..] {
        return Err(io::ErrorKind::WouldBlock.into());
    }
    old.resize(b.len(), 0);
    old.copy_from_slice(b);
    Ok(())
}

/// Rotates a width×height ARGB image (libyuv's ARGBRotate).
///
/// # Safety
/// src holds height rows of src_stride bytes; dst has room for the rotated
/// image at dst_stride.
#[allow(non_snake_case)]
pub unsafe fn ARGBRotate(
    src: *const u8,
    src_stride: i32,
    dst: *mut u8,
    dst_stride: i32,
    width: i32,
    height: i32,
    mode: RotationMode,
) -> i32 {
    let (w, h) = (width as usize, height as usize);
    for y in 0..h {
        for x in 0..w {
            let (dx, dy) = match mode {
                RotationMode::kRotate0 => (x, y),
                RotationMode::kRotate90 => (h - 1 - y, x),
                RotationMode::kRotate180 => (w - 1 - x, h - 1 - y),
                RotationMode::kRotate270 => (y, w - 1 - x),
            };
            // SAFETY: both offsets are inside the images, per the contract.
            unsafe {
                let s = src.add(y * src_stride as usize + x * 4) as *const u32;
                let d = dst.add(dy * dst_stride as usize + dx * 4) as *mut u32;
                d.write_unaligned(s.read_unaligned());
            }
        }
    }
    0
}

/// Mirrors a width×height ARGB image left to right (libyuv's ARGBMirror).
///
/// # Safety
/// As ARGBRotate.
#[allow(non_snake_case)]
pub unsafe fn ARGBMirror(
    src: *const u8,
    src_stride: i32,
    dst: *mut u8,
    dst_stride: i32,
    width: i32,
    height: i32,
) -> i32 {
    let (w, h) = (width as usize, height as usize);
    for y in 0..h {
        for x in 0..w {
            // SAFETY: both offsets are inside the images, per the contract.
            unsafe {
                let s = src.add(y * src_stride as usize + x * 4) as *const u32;
                let d = dst.add(y * dst_stride as usize + (w - 1 - x) * 4) as *mut u32;
                d.write_unaligned(s.read_unaligned());
            }
        }
    }
    0
}
