// RustDesk's libvpx bindings (libs/scrap/src/common/vpx.rs, AGPL-3.0).
// Modified by Kyokai: the bindings are generated ahead of time into
// generated/vpx_ffi.rs (doc/en-us/development.md) instead of by build.rs.

#![allow(non_camel_case_types)]
#![allow(non_snake_case)]
#![allow(non_upper_case_globals)]
#![allow(improper_ctypes)]
#![allow(dead_code)]
#![allow(unused_imports)]

impl Default for vpx_codec_enc_cfg {
    fn default() -> Self {
        unsafe { std::mem::zeroed() }
    }
}

impl Default for vpx_codec_ctx {
    fn default() -> Self {
        unsafe { std::mem::zeroed() }
    }
}

impl Default for vpx_image_t {
    fn default() -> Self {
        unsafe { std::mem::zeroed() }
    }
}

include!("../generated/vpx_ffi.rs");
