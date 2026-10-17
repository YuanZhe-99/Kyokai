// RustDesk's Windows screen capture (libs/scrap/src/dxgi) as a crate of its
// own. Modified by Kyokai: this crate root replaces scrap's lib.rs and
// common module with the few items the dxgi code uses (shim.rs).
#![cfg(windows)]
#![allow(clippy::all, unsafe_op_in_unsafe_fn)]

mod dxgi;
mod shim;

pub mod common {
    pub use crate::shim::{ARGBMirror, ARGBRotate};
}
pub use dxgi::{Capturer, CapturerGDI, Display, Displays};
pub use shim::{would_block_if_equal, AdapterDevice, Frame, PixelBuffer, RotationMode};
