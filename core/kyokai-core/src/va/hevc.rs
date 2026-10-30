//! HEVC through VA-API on libva directly: `cros-codecs` has no HEVC encoder
//! and `cros-libva` cannot pass packed headers, which Intel's driver needs.
//! Kyokai writes the VPS, SPS, PPS and each slice header itself (the
//! choices FFmpeg's `hevc_vaapi` makes, doc/en-us/upstream.md "VA-API") and
//! the driver codes the slices: Main profile, low delay (P frames, one
//! reference), constant bitrate, a key frame every 4 seconds and on request.

use std::ffi::c_void;
use std::fs::File;
use std::os::fd::AsRawFd;
use std::sync::OnceLock;

use cros_codecs::libva as va;

/// `VAEncPackedHeaderParameterBuffer`, which the bindings leave out.
#[repr(C)]
struct PackedHeader {
    kind: u32,
    bit_length: u32,
    has_emulation_bytes: u8,
    reserved: [u32; 4],
}

/// A misc parameter buffer: its type, then the parameters.
#[repr(C)]
struct Misc<T> {
    kind: u32,
    data: T,
}

/// `VA_PICTURE_HEVC_INVALID` and `VA_PICTURE_HEVC_RPS_ST_CURR_BEFORE`.
const PICTURE_INVALID: u32 = 1;
const ST_CURR_BEFORE: u32 = 16;
/// NAL unit types: IDR_W_RADL, TRAIL_R, VPS, SPS, PPS.
const IDR: u8 = 19;
const TRAIL: u8 = 1;
const VPS: u8 = 32;
const SPS: u8 = 33;
const PPS: u8 = 34;
/// Level 4 (30 × 4.0), enough for 1920×1080 at 30 fps.
const LEVEL: u8 = 120;
/// The first quantizer, which the PPS carries.
const INIT_QP: u8 = 30;
/// Bits of the picture order count each slice header carries.
const POC_BITS: u32 = 12;

/// The coding tools this driver takes, from what it reports (FFmpeg's
/// defaults where it reports nothing).
#[derive(Clone, Copy)]
struct Tools {
    low_power: bool,
    /// log2 of the coding tree block, the smallest coding block and the
    /// smallest and largest transform blocks; the transform depths.
    ctb: u8,
    min_cb: u8,
    min_tb: u8,
    max_tb: u8,
    depth_inter: u8,
    depth_intra: u8,
    amp: bool,
    cu_qp_delta: bool,
}

/// What the driver offers for HEVC encoding, probed once per process.
fn tools() -> Option<Tools> {
    static TOOLS: OnceLock<Option<Tools>> = OnceLock::new();
    *TOOLS.get_or_init(|| {
        let display = Display::open().ok()?;
        let found = display.tools();
        match found {
            Some(_) => log::info!("va-api: HEVC encoding"),
            None => log::info!("va-api: no HEVC encoder"),
        }
        found
    })
}

/// Whether this host's GPU encodes HEVC through VA-API.
pub fn available() -> bool {
    tools().is_some()
}

/// Fails with the call's name when libva does not answer success.
fn check(status: va::VAStatus, call: &str) -> Result<(), String> {
    if status == va::VA_STATUS_SUCCESS as va::VAStatus {
        Ok(())
    } else {
        Err(format!("{call} failed ({status})"))
    }
}

/// A libva display on a render node, quiet (its messages are not logged).
struct Display {
    _node: File,
    dpy: va::VADisplay,
}

impl Display {
    /// Opens the first render node whose driver initializes.
    fn open() -> Result<Display, String> {
        let mut nodes: Vec<_> = std::fs::read_dir("/dev/dri")
            .map_err(|e| e.to_string())?
            .flatten()
            .map(|e| e.path())
            .filter(|p| {
                p.file_name()
                    .is_some_and(|n| n.to_string_lossy().starts_with("renderD"))
            })
            .collect();
        nodes.sort();
        for path in nodes {
            let Ok(node) = File::options().read(true).write(true).open(&path) else {
                continue;
            };
            // SAFETY: the display is made on an open render node kept with
            // it; the callbacks are cleared before initializing, so libva
            // writes nothing to stderr; it is terminated in Drop.
            unsafe {
                let dpy = va::vaGetDisplayDRM(node.as_raw_fd());
                if dpy.is_null() {
                    continue;
                }
                va::vaSetInfoCallback(dpy, None, std::ptr::null_mut());
                va::vaSetErrorCallback(dpy, None, std::ptr::null_mut());
                let (mut major, mut minor) = (0, 0);
                if va::vaInitialize(dpy, &mut major, &mut minor) == va::VA_STATUS_SUCCESS as i32 {
                    return Ok(Display { _node: node, dpy });
                }
                va::vaTerminate(dpy);
            }
        }
        Err("no VA-API driver".into())
    }

    /// The coding tools for HEVC Main on the full entry point, else the
    /// low-power one, when the driver takes packed sequence and slice
    /// headers there.
    fn tools(&self) -> Option<Tools> {
        let profile = va::VAProfile::VAProfileHEVCMain;
        let mut entries = vec![0; 16];
        let mut n = 0;
        // SAFETY: the buffer holds as many entry points as libva reports.
        let ok = unsafe {
            va::vaQueryConfigEntrypoints(self.dpy, profile, entries.as_mut_ptr(), &mut n)
        };
        if ok != va::VA_STATUS_SUCCESS as i32 {
            return None;
        }
        entries.truncate(n.max(0) as usize);
        let wanted = va::VA_ENC_PACKED_HEADER_SEQUENCE | va::VA_ENC_PACKED_HEADER_SLICE;
        [false, true].into_iter().find_map(|low_power| {
            let entry = entry(low_power);
            if !entries.contains(&entry) {
                return None;
            }
            let attr = |kind| va::VAConfigAttrib {
                type_: kind,
                value: 0,
            };
            let mut attrs = [
                attr(va::VAConfigAttribType::VAConfigAttribEncPackedHeaders),
                attr(va::VAConfigAttribType::VAConfigAttribRateControl),
                attr(va::VAConfigAttribType::VAConfigAttribEncHEVCFeatures),
                attr(va::VAConfigAttribType::VAConfigAttribEncHEVCBlockSizes),
            ];
            // SAFETY: the attributes are read into the array libva is given.
            let got = unsafe {
                va::vaGetConfigAttributes(self.dpy, profile, entry, attrs.as_mut_ptr(), 4)
            };
            let [packed, rc, features, sizes] = attrs.map(|a| a.value);
            if got != va::VA_STATUS_SUCCESS as i32
                || packed == va::VA_ATTRIB_NOT_SUPPORTED
                || packed & wanted != wanted
                || rc == va::VA_ATTRIB_NOT_SUPPORTED
                || rc & va::VA_RC_CBR == 0
            {
                return None;
            }
            let bits = |v: u32, at: u32| ((v >> at) & 3) as u8;
            let mut t = Tools {
                low_power,
                ctb: 5,
                min_cb: 3,
                min_tb: 2,
                max_tb: 5,
                depth_inter: 3,
                depth_intra: 3,
                amp: true,
                cu_qp_delta: true,
            };
            if sizes != va::VA_ATTRIB_NOT_SUPPORTED {
                t.ctb = bits(sizes, 0) + 3;
                t.min_cb = bits(sizes, 4) + 3;
                t.max_tb = bits(sizes, 6) + 2;
                t.min_tb = bits(sizes, 8) + 2;
                t.depth_inter = bits(sizes, 10);
                t.depth_intra = bits(sizes, 14);
            }
            if features != va::VA_ATTRIB_NOT_SUPPORTED {
                t.amp = bits(features, 4) != 0;
                t.cu_qp_delta = bits(features, 22) != 0;
            }
            Some(t)
        })
    }
}

impl Drop for Display {
    fn drop(&mut self) {
        // SAFETY: the display was initialized and nothing of it outlives it.
        unsafe {
            va::vaTerminate(self.dpy);
        }
    }
}

/// The libva entry point for the full or the low-power encoder.
fn entry(low_power: bool) -> va::VAEntrypoint::Type {
    if low_power {
        va::VAEntrypoint::VAEntrypointEncSliceLP
    } else {
        va::VAEntrypoint::VAEntrypointEncSlice
    }
}

/// An RBSP being written bit by bit, MSB first.
#[derive(Default)]
struct Bits {
    out: Vec<u8>,
    acc: u8,
    n: u32,
}

impl Bits {
    /// Writes the low `n` bits of `v`.
    fn u(&mut self, n: u32, v: u32) {
        for i in (0..n).rev() {
            self.acc = (self.acc << 1) | ((v >> i) & 1) as u8;
            self.n += 1;
            if self.n == 8 {
                self.out.push(self.acc);
                (self.acc, self.n) = (0, 0);
            }
        }
    }

    /// Writes a flag.
    fn flag(&mut self, f: bool) {
        self.u(1, f.into());
    }

    /// Writes an unsigned Exp-Golomb code.
    fn ue(&mut self, v: u32) {
        let v = u64::from(v) + 1;
        let len = 64 - v.leading_zeros();
        self.u(len - 1, 0);
        for i in (0..len).rev() {
            self.u(1, ((v >> i) & 1) as u32);
        }
    }

    /// Writes a signed Exp-Golomb code.
    fn se(&mut self, v: i32) {
        self.ue(if v > 0 {
            (2 * v - 1) as u32
        } else {
            (-2 * v) as u32
        });
    }

    /// Ends the RBSP: the stop bit, then zeros to the byte.
    fn trailing(&mut self) {
        self.u(1, 1);
        while self.n != 0 {
            self.u(1, 0);
        }
    }

    /// The RBSP as a NAL unit of `kind` after a start code, with emulation
    /// prevention bytes, appended to `out`.
    fn nal(self, kind: u8, out: &mut Vec<u8>) {
        debug_assert_eq!(self.n, 0, "a NAL ends on a byte");
        out.extend_from_slice(&[0, 0, 0, 1, kind << 1, 1]);
        let mut zeros = 0;
        for &b in &self.out {
            if zeros == 2 && b <= 3 {
                out.push(3);
                zeros = 0;
            }
            out.push(b);
            zeros = if b == 0 { zeros + 1 } else { 0 };
        }
    }
}

/// The stream's fixed shape, for the headers and the parameter buffers.
#[derive(Clone, Copy)]
struct Shape {
    /// The picture's size and the coded one (a multiple of 16 each way).
    width: u32,
    height: u32,
    coded_w: u32,
    coded_h: u32,
    fps: u32,
    tools: Tools,
}

impl Shape {
    /// The coding tree blocks of a picture.
    fn ctbs(&self) -> u32 {
        let ctb = 1 << self.tools.ctb;
        self.coded_w.div_ceil(ctb) * self.coded_h.div_ceil(ctb)
    }

    /// profile_tier_level for Main at LEVEL, no sub-layers.
    fn profile(&self, b: &mut Bits) {
        b.u(2, 0); // general_profile_space
        b.u(1, 0); // general_tier_flag
        b.u(5, 1); // general_profile_idc: Main
        b.u(32, 0x6000_0000); // compatible with Main and Main 10
        b.flag(true); // progressive_source
        b.flag(false); // interlaced_source
        b.flag(true); // non_packed_constraint
        b.flag(true); // frame_only_constraint
        b.u(32, 0);
        b.u(11, 0); // reserved_zero_43bits
        b.u(1, 0); // inbld_flag
        b.u(8, LEVEL.into());
    }

    /// The VPS, SPS and PPS, each after a start code.
    fn parameter_sets(&self) -> Vec<u8> {
        let t = self.tools;
        let mut out = Vec::new();

        let mut b = Bits::default();
        b.u(4, 0); // vps_video_parameter_set_id
        b.flag(true); // vps_base_layer_internal_flag
        b.flag(true); // vps_base_layer_available_flag
        b.u(6, 0); // vps_max_layers_minus1
        b.u(3, 0); // vps_max_sub_layers_minus1
        b.flag(true); // vps_temporal_id_nesting_flag
        b.u(16, 0xFFFF);
        self.profile(&mut b);
        b.flag(false); // vps_sub_layer_ordering_info_present_flag
        b.ue(1); // vps_max_dec_pic_buffering_minus1
        b.ue(0); // vps_max_num_reorder_pics
        b.ue(0); // vps_max_latency_increase_plus1
        b.u(6, 0); // vps_max_layer_id
        b.ue(0); // vps_num_layer_sets_minus1
        b.flag(true); // vps_timing_info_present_flag
        b.u(32, 1); // vps_num_units_in_tick
        b.u(32, self.fps); // vps_time_scale
        b.flag(true); // vps_poc_proportional_to_timing_flag
        b.ue(0); // vps_num_ticks_poc_diff_one_minus1
        b.ue(0); // vps_num_hrd_parameters
        b.flag(false); // vps_extension_flag
        b.trailing();
        b.nal(VPS, &mut out);

        let mut b = Bits::default();
        b.u(4, 0); // sps_video_parameter_set_id
        b.u(3, 0); // sps_max_sub_layers_minus1
        b.flag(true); // sps_temporal_id_nesting_flag
        self.profile(&mut b);
        b.ue(0); // sps_seq_parameter_set_id
        b.ue(1); // chroma_format_idc: 4:2:0
        b.ue(self.coded_w);
        b.ue(self.coded_h);
        let crop = self.coded_w != self.width || self.coded_h != self.height;
        b.flag(crop); // conformance_window_flag
        if crop {
            b.ue(0);
            b.ue((self.coded_w - self.width) / 2);
            b.ue(0);
            b.ue((self.coded_h - self.height) / 2);
        }
        b.ue(0); // bit_depth_luma_minus8
        b.ue(0); // bit_depth_chroma_minus8
        b.ue(POC_BITS - 4); // log2_max_pic_order_cnt_lsb_minus4
        b.flag(false); // sps_sub_layer_ordering_info_present_flag
        b.ue(1); // sps_max_dec_pic_buffering_minus1
        b.ue(0); // sps_max_num_reorder_pics
        b.ue(0); // sps_max_latency_increase_plus1
        b.ue((t.min_cb - 3).into());
        b.ue((t.ctb - t.min_cb).into());
        b.ue((t.min_tb - 2).into());
        b.ue((t.max_tb - t.min_tb).into());
        b.ue(t.depth_inter.into());
        b.ue(t.depth_intra.into());
        b.flag(false); // scaling_list_enabled_flag
        b.flag(t.amp);
        b.flag(false); // sample_adaptive_offset_enabled_flag
        b.flag(false); // pcm_enabled_flag
        b.ue(0); // num_short_term_ref_pic_sets: each slice carries its own
        b.flag(false); // long_term_ref_pics_present_flag
        b.flag(false); // sps_temporal_mvp_enabled_flag
        b.flag(false); // strong_intra_smoothing_enabled_flag
        b.flag(false); // vui_parameters_present_flag
        b.flag(false); // sps_extension_present_flag
        b.trailing();
        b.nal(SPS, &mut out);

        let mut b = Bits::default();
        b.ue(0); // pps_pic_parameter_set_id
        b.ue(0); // pps_seq_parameter_set_id
        b.flag(false); // dependent_slice_segments_enabled_flag
        b.flag(false); // output_flag_present_flag
        b.u(3, 0); // num_extra_slice_header_bits
        b.flag(false); // sign_data_hiding_enabled_flag
        b.flag(false); // cabac_init_present_flag
        b.ue(0); // num_ref_idx_l0_default_active_minus1
        b.ue(0); // num_ref_idx_l1_default_active_minus1
        b.se(i32::from(INIT_QP) - 26); // init_qp_minus26
        b.flag(false); // constrained_intra_pred_flag
        b.flag(false); // transform_skip_enabled_flag
        b.flag(t.cu_qp_delta);
        if t.cu_qp_delta {
            b.ue(0); // diff_cu_qp_delta_depth
        }
        b.se(0); // pps_cb_qp_offset
        b.se(0); // pps_cr_qp_offset
        b.flag(false); // pps_slice_chroma_qp_offsets_present_flag
        b.flag(false); // weighted_pred_flag
        b.flag(false); // weighted_bipred_flag
        b.flag(false); // transquant_bypass_enabled_flag
        b.flag(false); // tiles_enabled_flag
        b.flag(false); // entropy_coding_sync_enabled_flag
        b.flag(true); // pps_loop_filter_across_slices_enabled_flag
        b.flag(false); // deblocking_filter_control_present_flag
        b.flag(false); // pps_scaling_list_data_present_flag
        b.flag(false); // lists_modification_present_flag
        b.ue(0); // log2_parallel_merge_level_minus2
        b.flag(false); // slice_segment_header_extension_present_flag
        b.flag(false); // pps_extension_present_flag
        b.trailing();
        b.nal(PPS, &mut out);
        out
    }

    /// The slice header of a picture: an IDR, or a P picture referring to
    /// the one before it.
    fn slice_header(&self, idr: bool, poc: u32) -> Vec<u8> {
        let mut b = Bits::default();
        b.flag(true); // first_slice_segment_in_pic_flag
        if idr {
            b.flag(false); // no_output_of_prior_pics_flag
        }
        b.ue(0); // slice_pic_parameter_set_id
        b.ue(if idr { 2 } else { 1 }); // slice_type: I or P
        if !idr {
            b.u(POC_BITS, poc & ((1 << POC_BITS) - 1));
            b.flag(false); // short_term_ref_pic_set_sps_flag
            b.ue(1); // num_negative_pics
            b.ue(0); // num_positive_pics
            b.ue(0); // delta_poc_s0_minus1: the picture before
            b.flag(true); // used_by_curr_pic_s0_flag
            b.flag(false); // num_ref_idx_active_override_flag
            b.ue(0); // five_minus_max_num_merge_cand
        }
        b.se(0); // slice_qp_delta
        b.flag(false); // slice_loop_filter_across_slices_enabled_flag
        b.trailing(); // byte_alignment
        let mut out = Vec::new();
        b.nal(if idr { IDR } else { TRAIL }, &mut out);
        out
    }
}

/// A VA-API HEVC encoder for one stream, its surfaces made once: one input
/// and two reconstructed pictures used in turn.
pub struct Encoder {
    display: Display,
    shape: Shape,
    config: va::VAConfigID,
    context: va::VAContextID,
    input: va::VASurfaceID,
    recon: [va::VASurfaceID; 2],
    coded: va::VABufferID,
    /// The VPS, SPS and PPS, sent before each key frame.
    headers: Vec<u8>,
    bitrate: u32,
    /// The rate control changed: its parameters go with the next frame.
    retune: bool,
    /// The last picture in NV12, `width` bytes a row; empty until the
    /// first picture.
    nv12: Vec<u8>,
    /// Frames since the last key frame.
    count: u32,
}

impl Encoder {
    /// Opens an encoder for `width`×`height` at `fps` and `bitrate` bit/s.
    /// Returns: the encoder, or why the driver refused it.
    pub fn new(width: u32, height: u32, fps: u32, bitrate: u32) -> Result<Encoder, String> {
        let tools = tools().ok_or("no VA-API HEVC encoder")?;
        let display = Display::open()?;
        let shape = Shape {
            width,
            height,
            coded_w: width.next_multiple_of(16),
            coded_h: height.next_multiple_of(16),
            fps,
            tools,
        };
        let dpy = display.dpy;
        let mut e = Encoder {
            headers: shape.parameter_sets(),
            display,
            shape,
            config: va::VA_INVALID_ID,
            context: va::VA_INVALID_ID,
            input: va::VA_INVALID_SURFACE,
            recon: [va::VA_INVALID_SURFACE; 2],
            coded: va::VA_INVALID_ID,
            bitrate,
            retune: true,
            nv12: Vec::new(),
            count: 0,
        };
        let mut attrs = [
            va::VAConfigAttrib {
                type_: va::VAConfigAttribType::VAConfigAttribRTFormat,
                value: va::VA_RT_FORMAT_YUV420,
            },
            va::VAConfigAttrib {
                type_: va::VAConfigAttribType::VAConfigAttribRateControl,
                value: va::VA_RC_CBR,
            },
            va::VAConfigAttrib {
                type_: va::VAConfigAttribType::VAConfigAttribEncPackedHeaders,
                value: va::VA_ENC_PACKED_HEADER_SEQUENCE | va::VA_ENC_PACKED_HEADER_SLICE,
            },
        ];
        let (w, h) = (shape.coded_w, shape.coded_h);
        // SAFETY: each object is created on this display into the field
        // Drop destroys; the arrays passed hold the counts given.
        unsafe {
            check(
                va::vaCreateConfig(
                    dpy,
                    va::VAProfile::VAProfileHEVCMain,
                    entry(tools.low_power),
                    attrs.as_mut_ptr(),
                    attrs.len() as i32,
                    &mut e.config,
                ),
                "vaCreateConfig",
            )?;
            let mut surfaces = [va::VA_INVALID_SURFACE; 3];
            let mut nv12 = va::VASurfaceAttrib::new_pixel_format(va::VA_FOURCC_NV12);
            check(
                va::vaCreateSurfaces(
                    dpy,
                    va::VA_RT_FORMAT_YUV420,
                    w,
                    h,
                    surfaces.as_mut_ptr(),
                    3,
                    &mut nv12,
                    1,
                ),
                "vaCreateSurfaces",
            )?;
            (e.input, e.recon) = (surfaces[0], [surfaces[1], surfaces[2]]);
            check(
                va::vaCreateContext(
                    dpy,
                    e.config,
                    w as i32,
                    h as i32,
                    va::VA_PROGRESSIVE as i32,
                    surfaces.as_mut_ptr(),
                    3,
                    &mut e.context,
                ),
                "vaCreateContext",
            )?;
            check(
                va::vaCreateBuffer(
                    dpy,
                    e.context,
                    va::VABufferType::VAEncCodedBufferType,
                    // FFmpeg's size: Intel's driver hangs the GPU on a
                    // smaller one (half a frame did).
                    3 * w * h + (1 << 16),
                    1,
                    std::ptr::null_mut(),
                    &mut e.coded,
                ),
                "vaCreateBuffer",
            )?;
        }
        Ok(e)
    }

    /// Takes a captured BGRA picture as the next one to encode, converted
    /// into NV12 of the stream's size.
    /// Notes: no allocation once the first picture sized the buffer.
    pub fn picture(&mut self, bgra: &[u8], width: usize, height: usize, stride: usize) {
        let (w, h) = (self.shape.width as usize, self.shape.height as usize);
        self.nv12.resize(w * h * 3 / 2, 0);
        let (y, uv) = self.nv12.split_at_mut(w * h);
        let src = crate::video::Picture {
            bgra,
            width,
            height,
            stride,
        };
        crate::video::bgra_to_nv12_planes(src, w, h, y, w, uv, w);
    }

    /// Whether a picture was taken yet.
    pub fn has_picture(&self) -> bool {
        !self.nv12.is_empty()
    }

    /// Sets the target bitrate in bit/s (from the next frame on).
    pub fn set_bitrate(&mut self, bitrate: u32) {
        if bitrate != self.bitrate {
            (self.bitrate, self.retune) = (bitrate, true);
        }
    }

    /// Encodes the last picture; `key` asks for a key frame.
    /// Returns: the frame in Annex B with whether it is a key frame (nothing
    /// before the first picture).
    pub fn encode(&mut self, key: bool) -> Result<Vec<(Vec<u8>, bool)>, String> {
        if self.nv12.is_empty() {
            return Ok(Vec::new());
        }
        if key || self.count >= u32::from(super::KEY_EVERY) {
            self.count = 0;
        }
        let idr = self.count == 0;
        self.upload()?;
        let mut buffers = Vec::new();
        let rendered = self.render(idr, &mut buffers);
        for b in buffers {
            // SAFETY: each buffer was created on this display for this
            // picture and is destroyed once.
            unsafe {
                va::vaDestroyBuffer(self.display.dpy, b);
            }
        }
        rendered?;
        self.count += 1;
        Ok(vec![(self.read_coded()?, idr)])
    }

    /// Copies the last picture into the input surface.
    fn upload(&self) -> Result<(), String> {
        let dpy = self.display.dpy;
        let (w, h) = (self.shape.width as usize, self.shape.height as usize);
        // SAFETY: the image is derived from the input surface, mapped while
        // its planes are written within its pitches and offsets, then
        // unmapped and destroyed.
        unsafe {
            let mut image: va::VAImage = std::mem::zeroed();
            check(
                va::vaDeriveImage(dpy, self.input, &mut image),
                "vaDeriveImage",
            )?;
            let mut data: *mut c_void = std::ptr::null_mut();
            let mapped = check(va::vaMapBuffer(dpy, image.buf, &mut data), "vaMapBuffer");
            if mapped.is_ok() {
                let base = data.cast::<u8>();
                for (i, row) in self.nv12.chunks_exact(w).enumerate() {
                    let (plane, r) = if i < h { (0, i) } else { (1, i - h) };
                    let at = image.offsets[plane] as usize + r * image.pitches[plane] as usize;
                    std::ptr::copy_nonoverlapping(row.as_ptr(), base.add(at), w);
                }
                va::vaUnmapBuffer(dpy, image.buf);
            }
            va::vaDestroyImage(dpy, image.image_id);
            mapped
        }
    }

    /// Creates a parameter buffer holding `data` and keeps its id.
    fn buffer<T>(
        &self,
        kind: va::VABufferType::Type,
        data: &T,
        ids: &mut Vec<va::VABufferID>,
    ) -> Result<(), String> {
        self.bytes(kind, std::ptr::from_ref(data).cast(), size_of::<T>(), ids)
    }

    /// Creates a buffer of `len` bytes from `data` and keeps its id.
    fn bytes(
        &self,
        kind: va::VABufferType::Type,
        data: *const u8,
        len: usize,
        ids: &mut Vec<va::VABufferID>,
    ) -> Result<(), String> {
        let mut id = va::VA_INVALID_ID;
        // SAFETY: libva copies `len` bytes from `data` into a new buffer of
        // this context.
        unsafe {
            check(
                va::vaCreateBuffer(
                    self.display.dpy,
                    self.context,
                    kind,
                    len as u32,
                    1,
                    data.cast_mut().cast(),
                    &mut id,
                ),
                "vaCreateBuffer",
            )?;
        }
        ids.push(id);
        Ok(())
    }

    /// Creates a packed header of `kind` (`VAEncPackedHeaderTypeHEVC`, not
    /// the config's `VA_ENC_PACKED_HEADER_*` bits: the slice's is 3, and 4,
    /// raw data, left the slice without a header and hung the GPU) from
    /// its NAL units.
    fn packed(&self, kind: u32, nal: &[u8], ids: &mut Vec<va::VABufferID>) -> Result<(), String> {
        let param = PackedHeader {
            kind,
            bit_length: (nal.len() * 8) as u32,
            has_emulation_bytes: 1,
            reserved: [0; 4],
        };
        self.buffer(
            va::VABufferType::VAEncPackedHeaderParameterBufferType,
            &param,
            ids,
        )?;
        self.bytes(
            va::VABufferType::VAEncPackedHeaderDataBufferType,
            nal.as_ptr(),
            nal.len(),
            ids,
        )
    }

    /// Creates a misc parameter buffer.
    fn misc<T>(&self, kind: u32, data: T, ids: &mut Vec<va::VABufferID>) -> Result<(), String> {
        self.buffer(
            va::VABufferType::VAEncMiscParameterBufferType,
            &Misc { kind, data },
            ids,
        )
    }

    /// Builds the picture's buffers and has the driver code it into the
    /// coded buffer.
    fn render(&mut self, idr: bool, ids: &mut Vec<va::VABufferID>) -> Result<(), String> {
        let s = self.shape;
        let t = s.tools;
        let poc = self.count;
        let cur = self.recon[(poc % 2) as usize];
        let prev = self.recon[((poc + 1) % 2) as usize];
        let invalid = va::VAPictureHEVC {
            picture_id: va::VA_INVALID_SURFACE,
            flags: PICTURE_INVALID,
            ..Default::default()
        };
        let reference = va::VAPictureHEVC {
            picture_id: prev,
            pic_order_cnt: poc as i32 - 1,
            flags: ST_CURR_BEFORE,
            ..Default::default()
        };
        // SAFETY: plain C parameter structs, zero being valid for each; the
        // bitfield setters write their own fields.
        unsafe {
            if idr || self.retune {
                let mut seq: va::VAEncSequenceParameterBufferHEVC = std::mem::zeroed();
                seq.general_profile_idc = 1;
                seq.general_level_idc = LEVEL;
                seq.intra_period = super::KEY_EVERY.into();
                seq.intra_idr_period = super::KEY_EVERY.into();
                seq.ip_period = 1;
                seq.bits_per_second = self.bitrate;
                seq.pic_width_in_luma_samples = s.coded_w as u16;
                seq.pic_height_in_luma_samples = s.coded_h as u16;
                let f = &mut seq.seq_fields.bits;
                f.set_chroma_format_idc(1);
                f.set_amp_enabled_flag(t.amp.into());
                seq.log2_min_luma_coding_block_size_minus3 = t.min_cb - 3;
                seq.log2_diff_max_min_luma_coding_block_size = t.ctb - t.min_cb;
                seq.log2_min_transform_block_size_minus2 = t.min_tb - 2;
                seq.log2_diff_max_min_transform_block_size = t.max_tb - t.min_tb;
                seq.max_transform_hierarchy_depth_inter = t.depth_inter;
                seq.max_transform_hierarchy_depth_intra = t.depth_intra;
                self.buffer(
                    va::VABufferType::VAEncSequenceParameterBufferType,
                    &seq,
                    ids,
                )?;

                let mut rc: va::VAEncMiscParameterRateControl = std::mem::zeroed();
                rc.bits_per_second = self.bitrate;
                rc.target_percentage = 100;
                rc.window_size = 1000;
                rc.max_qp = super::MAX_QP;
                // No macroblock rate control (FFmpeg's choice).
                rc.rc_flags.bits.set_mb_rate_control(2);
                self.misc(
                    va::VAEncMiscParameterType::VAEncMiscParameterTypeRateControl,
                    rc,
                    ids,
                )?;
                let mut hrd: va::VAEncMiscParameterHRD = std::mem::zeroed();
                hrd.buffer_size = self.bitrate;
                hrd.initial_buffer_fullness = self.bitrate / 4 * 3;
                self.misc(
                    va::VAEncMiscParameterType::VAEncMiscParameterTypeHRD,
                    hrd,
                    ids,
                )?;
                let mut rate: va::VAEncMiscParameterFrameRate = std::mem::zeroed();
                rate.framerate = s.fps | (1 << 16);
                self.misc(
                    va::VAEncMiscParameterType::VAEncMiscParameterTypeFrameRate,
                    rate,
                    ids,
                )?;
                self.retune = false;
            }

            let mut pic: va::VAEncPictureParameterBufferHEVC = std::mem::zeroed();
            pic.decoded_curr_pic = va::VAPictureHEVC {
                picture_id: cur,
                pic_order_cnt: poc as i32,
                ..Default::default()
            };
            pic.reference_frames = [invalid; 15];
            if !idr {
                pic.reference_frames[0] = reference;
            }
            pic.coded_buf = self.coded;
            pic.collocated_ref_pic_index = 0xFF;
            pic.pic_init_qp = INIT_QP;
            pic.nal_unit_type = if idr { IDR } else { TRAIL };
            let f = &mut pic.pic_fields.bits;
            f.set_idr_pic_flag(idr.into());
            f.set_coding_type(if idr { 1 } else { 2 });
            f.set_reference_pic_flag(1);
            f.set_cu_qp_delta_enabled_flag(t.cu_qp_delta.into());
            f.set_pps_loop_filter_across_slices_enabled_flag(1);
            self.buffer(va::VABufferType::VAEncPictureParameterBufferType, &pic, ids)?;

            if idr {
                let h = &self.headers;
                self.packed(
                    va::VAEncPackedHeaderTypeHEVC::VAEncPackedHeaderHEVC_SPS,
                    h,
                    ids,
                )?;
            }
            self.packed(
                va::VAEncPackedHeaderTypeHEVC::VAEncPackedHeaderHEVC_Slice,
                &s.slice_header(idr, poc),
                ids,
            )?;

            let mut slice: va::VAEncSliceParameterBufferHEVC = std::mem::zeroed();
            slice.num_ctu_in_slice = s.ctbs();
            slice.slice_type = if idr { 2 } else { 1 };
            slice.ref_pic_list0 = [invalid; 15];
            slice.ref_pic_list1 = [invalid; 15];
            if !idr {
                slice.ref_pic_list0[0] = reference;
            }
            slice.max_num_merge_cand = 5;
            let f = &mut slice.slice_fields.bits;
            f.set_last_slice_of_pic_flag(1);
            f.set_collocated_from_l0_flag((!idr).into());
            self.buffer(va::VABufferType::VAEncSliceParameterBufferType, &slice, ids)?;

            let dpy = self.display.dpy;
            check(
                va::vaBeginPicture(dpy, self.context, self.input),
                "vaBeginPicture",
            )?;
            check(
                va::vaRenderPicture(dpy, self.context, ids.as_mut_ptr(), ids.len() as i32),
                "vaRenderPicture",
            )?;
            check(va::vaEndPicture(dpy, self.context), "vaEndPicture")?;
            check(va::vaSyncSurface(dpy, self.input), "vaSyncSurface")
        }
    }

    /// The coded frame, read from the coded buffer's segments.
    fn read_coded(&self) -> Result<Vec<u8>, String> {
        let dpy = self.display.dpy;
        let mut out = Vec::new();
        // SAFETY: the coded buffer maps to a list of segments that stay
        // valid until it is unmapped.
        unsafe {
            let mut seg: *mut c_void = std::ptr::null_mut();
            check(va::vaMapBuffer(dpy, self.coded, &mut seg), "vaMapBuffer")?;
            let mut s = seg.cast::<va::VACodedBufferSegment>();
            while !s.is_null() {
                let segment = &*s;
                out.extend_from_slice(std::slice::from_raw_parts(
                    segment.buf.cast::<u8>(),
                    segment.size as usize,
                ));
                s = segment.next.cast();
            }
            va::vaUnmapBuffer(dpy, self.coded);
        }
        Ok(out)
    }
}

impl Drop for Encoder {
    fn drop(&mut self) {
        let dpy = self.display.dpy;
        // SAFETY: each object was created on this display (or is still
        // invalid, which libva refuses harmlessly) and is destroyed once,
        // before the display terminates.
        unsafe {
            va::vaDestroyBuffer(dpy, self.coded);
            va::vaDestroyContext(dpy, self.context);
            let mut surfaces = [self.input, self.recon[0], self.recon[1]];
            va::vaDestroySurfaces(dpy, surfaces.as_mut_ptr(), 3);
            va::vaDestroyConfig(dpy, self.config);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Exp-Golomb codes and emulation prevention, as the standard writes
    /// them.
    #[test]
    fn bits_are_written_as_the_standard_says() {
        let mut b = Bits::default();
        for v in [0, 1, 2, 3, 7] {
            b.ue(v);
        }
        b.se(-1);
        b.se(2);
        b.trailing();
        // 1 010 011 00100 0001000 011 00100 1
        assert_eq!(b.out, [0b1010_0110, 0b0100_0001, 0b0000_1100, 0b1001_0000]);
        let mut b = Bits::default();
        b.u(32, 0x0000_0100);
        let mut nal = Vec::new();
        b.nal(SPS, &mut nal);
        assert_eq!(nal, [0, 0, 0, 1, 0x42, 1, 0, 0, 3, 1, 0]);
    }

    /// FFmpeg's `hevc_vaapi` headers for 1920×1080 at 30 fps on this
    /// computer's Intel driver (from libva's trace), whose choices these
    /// are: the slice headers match byte for byte.
    #[test]
    fn slice_headers_match_ffmpeg_s() {
        let shape = Shape {
            width: 1920,
            height: 1080,
            coded_w: 1920,
            coded_h: 1088,
            fps: 30,
            tools: Tools {
                low_power: false,
                ctb: 5,
                min_cb: 3,
                min_tb: 2,
                max_tb: 5,
                depth_inter: 3,
                depth_intra: 3,
                amp: true,
                cu_qp_delta: true,
            },
        };
        assert_eq!(
            shape.slice_header(true, 0),
            [0, 0, 0, 1, 0x26, 1, 0xAE, 0x80]
        );
        let p = |poc| shape.slice_header(false, poc);
        assert_eq!(p(1), [0, 0, 0, 1, 2, 1, 0xD0, 0, 0x97, 0x68]);
        assert_eq!(p(2), [0, 0, 0, 1, 2, 1, 0xD0, 1, 0x17, 0x68]);
        assert_eq!(shape.ctbs(), 60 * 34);
    }

    /// The GPU's HEVC: VPS, SPS, PPS and an IDR first, on request and
    /// every KEY_EVERY frames, P frames between, through a bitrate change;
    /// the picture comes back through the system's FFmpeg. Runs where the
    /// GPU encodes HEVC through VA-API and FFmpeg decodes it.
    #[test]
    fn the_gpu_s_hevc_round_trips() {
        if !available() || !crate::ff::decodable().contains(&"hevc") {
            eprintln!("no VA-API HEVC encoder or no FFmpeg HEVC decoder here");
            return;
        }
        let (w, h) = (320usize, 180usize);
        let mut enc = Encoder::new(w as u32, h as u32, 30, 1_000_000).unwrap();
        assert!(
            enc.encode(false).unwrap().is_empty(),
            "nothing before a picture"
        );
        let mut dec = crate::ff::Decoder::new("hevc").unwrap();
        let kinds = |f: &[u8]| -> Vec<u8> {
            crate::annexb::units(f)
                .map(|u| crate::annexb::Syntax::Hevc.kind(u))
                .collect()
        };
        let mut bgra = vec![0u8; w * h * 4];
        let mut rgba = Vec::new();
        let mut keys = Vec::new();
        for i in 0..2 * super::super::KEY_EVERY as usize + 4 {
            for (p, px) in bgra.as_chunks_mut::<4>().0.iter_mut().enumerate() {
                *px = if (p % w + i) % w < w / 2 {
                    [40, 80, 200, 255]
                } else {
                    [220; 4]
                };
            }
            enc.picture(&bgra, w, h, w * 4);
            if i == 5 {
                enc.set_bitrate(500_000);
            }
            let mut out = enc.encode(i == 3).unwrap();
            assert_eq!(out.len(), 1, "one frame out per frame in");
            let (frame, key) = out.pop().unwrap();
            let k = kinds(&frame);
            if key {
                keys.push(i);
                assert_eq!(k, [VPS, SPS, PPS, IDR], "frame {i}");
            } else {
                assert_eq!(k, [TRAIL], "frame {i}");
            }
            assert_eq!(
                dec.decode(&frame, &mut rgba).unwrap(),
                Some((w as u32, h as u32)),
                "frame {i}"
            );
            // The middle of each half, which moves a pixel a frame.
            for (mid, want) in [(w / 4, [200, 80, 40]), (w * 3 / 4, [220, 220, 220])] {
                let x = (mid + w - i % w) % w;
                let px = &rgba[(90 * w + x) * 4..][..3];
                assert!(
                    px.iter().zip(want).all(|(&g, w)| g.abs_diff(w) < 24),
                    "frame {i}: {px:?} {want:?}"
                );
            }
        }
        let every = super::super::KEY_EVERY as usize;
        assert_eq!(keys, [0, 3, 3 + every, 3 + 2 * every]);
    }
}
