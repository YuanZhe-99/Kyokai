// RustDesk's video quality of service (src/server/video_qos.rs) as a crate
// of its own: frame rate and bitrate from the delays viewers report.
// Modified by Kyokai: this crate root and shim.rs replace what the module
// took from RustDesk's server module and scrap.
#![allow(clippy::all)]
// Upstream's tests carry an unused import and an unread report field.
#![cfg_attr(test, allow(unused_imports, dead_code))]

mod shim;
mod video_qos;

pub use shim::{base_bitrate, ImageQuality, Quality};
pub use video_qos::*;
