//! Video codecs through Windows Media Foundation (doc/en-us/computer-use.md,
//! "Video"): the system's encoder on the host and its decoder on the
//! controller, so the OS vendor's codec licences cover them. H.264 is on
//! every Windows edition with Media Foundation; HEVC and VP9 come with the
//! GPU or the Store's video extensions. The encoder is the GPU's hardware
//! transform where one works (one per process), else a synchronous software
//! transform; the decoder is Microsoft's for H.264, the extensions' for the
//! others (HEVC on the GPU only, through Direct3D 11).
//!
//! Every object here belongs to the thread that made it (COM, multithreaded
//! apartment); pixels cross as NV12.

use std::mem::ManuallyDrop;
use windows::Win32::Media::MediaFoundation::*;
use windows::Win32::System::Com::{
    CLSCTX_INPROC_SERVER, COINIT_MULTITHREADED, CoCreateInstance, CoInitializeEx, CoTaskMemFree,
};
use windows::Win32::System::Variant::{VARIANT, VT_BOOL, VT_UI4};
use windows::core::{GUID, Interface};

/// A codec the stream can be in: the ones Media Foundation offers on
/// Windows (HEVC and VP9 through the GPU or the Store's video extensions).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Codec {
    H264,
    Hevc,
    Vp9,
}

impl Codec {
    /// Every codec, in the host's order of preference.
    pub const ALL: [Codec; 3] = [Codec::H264, Codec::Hevc, Codec::Vp9];

    /// The name the wire and the stream description use.
    pub fn name(self) -> &'static str {
        match self {
            Codec::H264 => "h264",
            Codec::Hevc => "hevc",
            Codec::Vp9 => "vp9",
        }
    }

    /// The codec a name stands for.
    pub fn from_name(name: &str) -> Option<Codec> {
        Codec::ALL.into_iter().find(|c| c.name() == name)
    }

    fn subtype(self) -> GUID {
        match self {
            Codec::H264 => MFVideoFormat_H264,
            Codec::Hevc => MFVideoFormat_HEVC,
            Codec::Vp9 => MFVideoFormat_VP90,
        }
    }
}

/// The codecs this computer decodes: those whose decoder opens (checked
/// once per process).
pub fn decodable() -> Vec<Codec> {
    static FOUND: std::sync::OnceLock<Vec<Codec>> = std::sync::OnceLock::new();
    FOUND
        .get_or_init(|| {
            Codec::ALL
                .into_iter()
                .filter(|&c| Decoder::new(c, 1280, 720).is_ok())
                .collect()
        })
        .clone()
}

/// The codecs this computer has an encoder for, in hardware or software
/// (looked up once per process; an encoder found may still fail to open).
pub fn encodable() -> Vec<Codec> {
    static FOUND: std::sync::OnceLock<Vec<Codec>> = std::sync::OnceLock::new();
    FOUND
        .get_or_init(|| {
            if startup().is_err() {
                return Vec::new();
            }
            Codec::ALL
                .into_iter()
                .filter(|c| {
                    [true, false].into_iter().any(|hardware| {
                        !transforms(
                            MFT_CATEGORY_VIDEO_ENCODER,
                            hardware,
                            MFVideoFormat_NV12,
                            c.subtype(),
                        )
                        .is_empty()
                    })
                })
                .collect()
        })
        .clone()
}

/// An encoded access unit and whether it is a key frame.
pub type Unit = (Vec<u8>, bool);
/// A decoded picture: width, height, row stride and NV12 bytes.
pub type Picture = (u32, u32, u32, Vec<u8>);

/// Starts COM and Media Foundation for the calling thread.
fn startup() -> Result<(), String> {
    // SAFETY: plain initialization calls; a repeated CoInitializeEx on the
    // same thread only returns S_FALSE.
    unsafe {
        let _ = CoInitializeEx(None, COINIT_MULTITHREADED);
        MFStartup(MF_VERSION, MFSTARTUP_NOSOCKET).map_err(|e| format!("Media Foundation: {e}"))
    }
}

fn pack(hi: u32, lo: u32) -> u64 {
    ((hi as u64) << 32) | lo as u64
}

fn video_type(
    subtype: &GUID,
    width: u32,
    height: u32,
    fps: u32,
) -> windows::core::Result<IMFMediaType> {
    // SAFETY: COM calls on an object this function owns.
    unsafe {
        let t = MFCreateMediaType()?;
        t.SetGUID(&MF_MT_MAJOR_TYPE, &MFMediaType_Video)?;
        t.SetGUID(&MF_MT_SUBTYPE, subtype)?;
        if width > 0 {
            t.SetUINT64(&MF_MT_FRAME_SIZE, pack(width, height))?;
        }
        if fps > 0 {
            t.SetUINT64(&MF_MT_FRAME_RATE, pack(fps, 1))?;
        }
        t.SetUINT64(&MF_MT_PIXEL_ASPECT_RATIO, pack(1, 1))?;
        t.SetUINT32(&MF_MT_INTERLACE_MODE, MFVideoInterlace_Progressive.0 as u32)?;
        Ok(t)
    }
}

/// Sets one codec property; transforms that lack it keep their default.
fn codec_value(t: &IMFTransform, api: &GUID, value: u32, boolean: bool) {
    let Ok(codec) = t.cast::<ICodecAPI>() else {
        return;
    };
    let mut v = VARIANT::default();
    // SAFETY: the union is written as the tag it is given, then passed by
    // pointer for the call only.
    unsafe {
        let inner = &mut *v.Anonymous.Anonymous;
        if boolean {
            inner.vt = VT_BOOL;
            inner.Anonymous.boolVal = (value != 0).into();
        } else {
            inner.vt = VT_UI4;
            inner.Anonymous.ulVal = value;
        }
        let _ = codec.SetValue(api, &v);
    }
}

/// A buffer-backed sample holding `data`.
fn sample_from(
    data: &[u8],
    time_100ns: i64,
    duration_100ns: i64,
) -> windows::core::Result<IMFSample> {
    // SAFETY: the buffer is locked while written and holds data.len() bytes.
    unsafe {
        let buffer = MFCreateMemoryBuffer(data.len() as u32)?;
        let mut ptr = std::ptr::null_mut();
        buffer.Lock(&mut ptr, None, None)?;
        std::ptr::copy_nonoverlapping(data.as_ptr(), ptr, data.len());
        buffer.Unlock()?;
        buffer.SetCurrentLength(data.len() as u32)?;
        let sample = MFCreateSample()?;
        sample.AddBuffer(&buffer)?;
        sample.SetSampleTime(time_100ns)?;
        sample.SetSampleDuration(duration_100ns)?;
        Ok(sample)
    }
}

/// Copies a sample's bytes out.
fn bytes_of(sample: &IMFSample) -> windows::core::Result<Vec<u8>> {
    // SAFETY: the contiguous buffer is locked while read, for its length.
    unsafe {
        let buffer = sample.ConvertToContiguousBuffer()?;
        let mut ptr = std::ptr::null_mut();
        let mut len = 0u32;
        buffer.Lock(&mut ptr, None, Some(&mut len))?;
        let out = std::slice::from_raw_parts(ptr, len as usize).to_vec();
        buffer.Unlock()?;
        Ok(out)
    }
}

/// What one ProcessOutput call gave.
enum Output {
    Unit(Unit),
    /// The transform has nothing until it gets more input.
    NeedInput,
    /// The output format changed (decoders, before their first frame).
    StreamChange,
}

/// Pulls every output the transform has ready.
/// Returns: the outputs (bytes and whether each is a key frame), and
/// whether the output format changed (decoders, before their first frame).
fn drain(t: &IMFTransform) -> windows::core::Result<(Vec<Unit>, bool)> {
    let mut out = Vec::new();
    loop {
        match output_once(t)? {
            Output::Unit(u) => out.push(u),
            Output::NeedInput => return Ok((out, false)),
            Output::StreamChange => return Ok((out, true)),
        }
    }
}

/// Calls ProcessOutput once.
fn output_once(t: &IMFTransform) -> windows::core::Result<Output> {
    {
        // SAFETY: COM calls on the transform; the output buffer's sample is
        // ours (allocated here when the transform does not provide one) and
        // released below.
        unsafe {
            let info = t.GetOutputStreamInfo(0)?;
            let provides = info.dwFlags & MFT_OUTPUT_STREAM_PROVIDES_SAMPLES.0 as u32 != 0;
            let mine = if provides {
                None
            } else {
                let s = MFCreateSample()?;
                s.AddBuffer(&MFCreateMemoryBuffer(info.cbSize.max(1 << 20))?)?;
                Some(s)
            };
            let mut buffers = [MFT_OUTPUT_DATA_BUFFER {
                dwStreamID: 0,
                pSample: ManuallyDrop::new(mine),
                dwStatus: 0,
                pEvents: ManuallyDrop::new(None),
            }];
            let mut status = 0u32;
            let result = t.ProcessOutput(0, &mut buffers, &mut status);
            let sample = ManuallyDrop::take(&mut buffers[0].pSample);
            drop(ManuallyDrop::take(&mut buffers[0].pEvents));
            match result {
                Ok(()) => match sample {
                    Some(s) => {
                        let key = s.GetUINT32(&MFSampleExtension_CleanPoint).unwrap_or(0) != 0;
                        Ok(Output::Unit((bytes_of(&s)?, key)))
                    }
                    None => Ok(Output::NeedInput),
                },
                Err(e) if e.code() == MF_E_TRANSFORM_NEED_MORE_INPUT => Ok(Output::NeedInput),
                Err(e) if e.code() == MF_E_TRANSFORM_STREAM_CHANGE => Ok(Output::StreamChange),
                Err(e) => Err(e),
            }
        }
    }
}

/// The system's encoder for a codec, fed NV12 frames.
pub struct Encoder {
    t: IMFTransform,
    width: u32,
    height: u32,
    fps: u32,
    n: i64,
    /// A hardware transform's events (it is asynchronous); None for the
    /// software one.
    events: Option<IMFMediaEventGenerator>,
    /// Inputs the hardware transform asked for and has not had yet.
    wanted: u32,
}

/// Whether a hardware encoder is open in this process: one at a time (the
/// GPU's encoder may not take several, and several crashed one here); the
/// others use the software encoder.
static HARDWARE_IN_USE: std::sync::atomic::AtomicBool = std::sync::atomic::AtomicBool::new(false);

/// How long one encode waits for a hardware transform's output before the
/// frame's output is left for the next call.
const HARDWARE_WAIT: std::time::Duration = std::time::Duration::from_millis(12);
/// Frames a hardware transform may take before its first output, when it
/// is tried at start.
const HARDWARE_TRIAL: usize = 30;

impl Encoder {
    /// Opens an encoder for `width`×`height` (even) at `fps` and a target
    /// bitrate, tuned for low latency (no B-frames, frequent key frames).
    /// Returns: an error when this system has no encoder for the codec
    /// (Windows N and Server editions lack even H.264).
    pub fn new(
        codec: Codec,
        width: u32,
        height: u32,
        fps: u32,
        bitrate: u32,
    ) -> Result<Self, String> {
        startup()?;
        // The GPU's encoder, if it opens and encodes a few frames; some need
        // a Direct3D device or reject system memory, so it is tried first.
        if !HARDWARE_IN_USE.swap(true, std::sync::atomic::Ordering::AcqRel) {
            if let Some(t) = find_transform(
                MFT_CATEGORY_VIDEO_ENCODER,
                true,
                MFVideoFormat_NV12,
                codec.subtype(),
            ) && let Ok(mut e) = Encoder::open(t, codec, width, height, fps, bitrate, true)
                && e.works()
            {
                return Ok(e);
            }
            HARDWARE_IN_USE.store(false, std::sync::atomic::Ordering::Release);
        }
        let t = find_transform(
            MFT_CATEGORY_VIDEO_ENCODER,
            false,
            MFVideoFormat_NV12,
            codec.subtype(),
        )
        .ok_or_else(|| format!("this system has no {} encoder", codec.name()))?;
        Encoder::open(t, codec, width, height, fps, bitrate, false)
    }

    /// Encodes black frames until an output comes, as a trial.
    fn works(&mut self) -> bool {
        let black = vec![16u8; (self.width * self.height) as usize]
            .into_iter()
            .chain(std::iter::repeat_n(
                128u8,
                (self.width * self.height / 2) as usize,
            ))
            .collect::<Vec<u8>>();
        let works =
            (0..HARDWARE_TRIAL).any(|i| self.encode(&black, i == 0).is_ok_and(|u| !u.is_empty()));
        if works {
            self.settle();
        }
        works
    }

    /// Lets a hardware transform finish the trial's frames, their outputs
    /// dropped, so the stream's first output is its own key frame.
    fn settle(&mut self) {
        let Some(events) = self.events.clone() else {
            return;
        };
        let until = std::time::Instant::now() + std::time::Duration::from_millis(250);
        let mut heard = std::time::Instant::now();
        while std::time::Instant::now() < until
            && heard.elapsed() < std::time::Duration::from_millis(40)
        {
            // SAFETY: a non-blocking event query on the transform.
            match unsafe { events.GetEvent(MF_EVENT_FLAG_NO_WAIT) } {
                Ok(ev) => {
                    heard = std::time::Instant::now();
                    // SAFETY: reading the event's type.
                    match unsafe { ev.GetType() } {
                        Ok(k) if k == METransformNeedInput.0 as u32 => self.wanted += 1,
                        Ok(k) if k == METransformHaveOutput.0 as u32 => {
                            let _ = output_once(&self.t);
                        }
                        _ => {}
                    }
                }
                Err(_) => std::thread::sleep(std::time::Duration::from_millis(1)),
            }
        }
    }

    /// Configures a transform as a low-latency H.264 encoder.
    fn open(
        t: IMFTransform,
        codec: Codec,
        width: u32,
        height: u32,
        fps: u32,
        bitrate: u32,
        hardware: bool,
    ) -> Result<Self, String> {
        let open = || -> windows::core::Result<Option<IMFMediaEventGenerator>> {
            // A hardware transform is asynchronous: it must be unlocked
            // before anything else, and it says by events when it wants
            // input and has output.
            let events = if hardware {
                // SAFETY: an attribute on the transform's own store.
                unsafe {
                    t.GetAttributes()?
                        .SetUINT32(&MF_TRANSFORM_ASYNC_UNLOCK, 1)?
                };
                Some(t.cast::<IMFMediaEventGenerator>()?)
            } else {
                None
            };
            // Low latency first: some transforms read it only when their types
            // are set.
            // SAFETY: an attribute on the transform's own store.
            unsafe {
                if let Ok(attrs) = t.GetAttributes() {
                    let _ = attrs.SetUINT32(&MF_LOW_LATENCY, 1);
                }
            }
            codec_value(&t, &CODECAPI_AVLowLatencyMode, 1, true);
            let out = video_type(&codec.subtype(), width, height, fps)?;
            // SAFETY: COM calls on the transform and the types made here.
            unsafe {
                out.SetUINT32(&MF_MT_AVG_BITRATE, bitrate)?;
                match codec {
                    Codec::H264 => {
                        out.SetUINT32(&MF_MT_MPEG2_PROFILE, eAVEncH264VProfile_Main.0 as u32)?
                    }
                    Codec::Hevc => {
                        out.SetUINT32(&MF_MT_MPEG2_PROFILE, eAVEncH265VProfile_Main_420_8.0 as u32)?
                    }
                    Codec::Vp9 => {}
                }
                t.SetOutputType(0, &out, 0)?;
                t.SetInputType(0, &video_type(&MFVideoFormat_NV12, width, height, fps)?, 0)?;
            }
            codec_value(&t, &CODECAPI_AVLowLatencyMode, 1, true);
            codec_value(
                &t,
                &CODECAPI_AVEncCommonRateControlMode,
                eAVEncCommonRateControlMode_CBR.0 as u32,
                false,
            );
            codec_value(&t, &CODECAPI_AVEncCommonMeanBitRate, bitrate, false);
            codec_value(&t, &CODECAPI_AVEncMPVGOPSize, fps * 4, false);
            codec_value(&t, &CODECAPI_AVEncMPVDefaultBPictureCount, 0, false);
            // SAFETY: streaming messages on the configured transform.
            unsafe {
                t.ProcessMessage(MFT_MESSAGE_NOTIFY_BEGIN_STREAMING, 0)?;
                t.ProcessMessage(MFT_MESSAGE_NOTIFY_START_OF_STREAM, 0)?;
            }
            Ok(events)
        };
        let events = open().map_err(|e| format!("{} encoder: {e}", codec.name()))?;
        Ok(Encoder {
            t,
            width,
            height,
            fps,
            n: 0,
            events,
            wanted: 0,
        })
    }

    /// Changes the target bitrate while running (RustDesk's QoS drives it).
    pub fn set_bitrate(&self, bitrate: u32) {
        codec_value(&self.t, &CODECAPI_AVEncCommonMeanBitRate, bitrate, false);
    }

    /// Encodes one NV12 frame; `key` forces a key frame.
    /// Returns: the encoded access units ready now (Annex B), each with
    /// whether it is a key frame.
    pub fn encode(&mut self, nv12: &[u8], key: bool) -> Result<Vec<Unit>, String> {
        if nv12.len() != (self.width * self.height * 3 / 2) as usize {
            return Err("frame size does not match the encoder".into());
        }
        if key {
            codec_value(&self.t, &CODECAPI_AVEncVideoForceKeyFrame, 1, false);
        }
        let step = 10_000_000 / self.fps as i64;
        let sample =
            sample_from(nv12, self.n * step, step).map_err(|e| format!("H.264 encoder: {e}"))?;
        let out = match self.events.clone() {
            None => {
                let run = || -> windows::core::Result<Vec<Unit>> {
                    // SAFETY: the transform takes its own reference to the sample.
                    unsafe { self.t.ProcessInput(0, &sample, 0)? };
                    Ok(drain(&self.t)?.0)
                };
                run()
            }
            Some(events) => self.pump(&events, &sample),
        }
        .map_err(|e| format!("H.264 encoder: {e}"))?;
        self.n += 1;
        Ok(out)
    }

    /// Feeds a hardware transform one frame when it wants it and collects
    /// the outputs it has, waiting a little for this frame's.
    fn pump(
        &mut self,
        events: &IMFMediaEventGenerator,
        sample: &IMFSample,
    ) -> windows::core::Result<Vec<Unit>> {
        let mut out = Vec::new();
        let mut fed = false;
        let until = std::time::Instant::now() + HARDWARE_WAIT;
        loop {
            if !fed && self.wanted > 0 {
                // SAFETY: the transform takes its own reference to the sample.
                unsafe { self.t.ProcessInput(0, sample, 0)? };
                self.wanted -= 1;
                fed = true;
            }
            // SAFETY: a non-blocking event query on the transform.
            match unsafe { events.GetEvent(MF_EVENT_FLAG_NO_WAIT) } {
                Ok(ev) => {
                    // SAFETY: reading the event's type.
                    let kind = unsafe { ev.GetType()? };
                    if kind == METransformNeedInput.0 as u32 {
                        self.wanted += 1;
                    } else if kind == METransformHaveOutput.0 as u32
                        && let Output::Unit(u) = output_once(&self.t)?
                        // Some GPUs' encoders give empty outputs between frames.
                        && !u.0.is_empty()
                    {
                        out.push(u);
                    }
                }
                Err(e) if e.code() == MF_E_NO_EVENTS_AVAILABLE => {
                    if (fed && !out.is_empty()) || std::time::Instant::now() >= until {
                        return Ok(out);
                    }
                    std::thread::sleep(std::time::Duration::from_millis(1));
                }
                Err(e) => return Err(e),
            }
        }
    }
}

impl Drop for Encoder {
    fn drop(&mut self) {
        if self.events.is_some() {
            // An asynchronous transform runs threads of its own: it is shut
            // down before its last reference goes, so none of them calls
            // into freed objects.
            // SAFETY: streaming messages and shutdown on the transform, which
            // is not used afterwards.
            unsafe {
                let _ = self.t.ProcessMessage(MFT_MESSAGE_NOTIFY_END_OF_STREAM, 0);
                let _ = self.t.ProcessMessage(MFT_MESSAGE_NOTIFY_END_STREAMING, 0);
                if let Ok(s) = self.t.cast::<IMFShutdown>() {
                    let _ = s.Shutdown();
                }
            }
            HARDWARE_IN_USE.store(false, std::sync::atomic::Ordering::Release);
        }
    }
}

/// Whether this process may open a hardware encoder for `codec` now: the
/// GPU has one and no stream holds it (the host prefers HEVC then).
/// Notes: it only looks; the hardware encoder may still fail its trial, and
/// then the codec's software encoder is used.
pub fn hardware_free(codec: Codec) -> bool {
    !HARDWARE_IN_USE.load(std::sync::atomic::Ordering::Acquire)
        && startup().is_ok()
        && !transforms(
            MFT_CATEGORY_VIDEO_ENCODER,
            true,
            MFVideoFormat_NV12,
            codec.subtype(),
        )
        .is_empty()
}

/// Finds the first transform of a category from `from` to `to`: the GPU's
/// (asynchronous) when `hardware`, else a synchronous software one.
fn find_transform(category: GUID, hardware: bool, from: GUID, to: GUID) -> Option<IMFTransform> {
    transforms(category, hardware, from, to)
        .into_iter()
        // SAFETY: activating a registered transform.
        .find_map(|a| unsafe { a.ActivateObject::<IMFTransform>() }.ok())
}

/// The registered transforms of a category from `from` to `to`, best first,
/// not yet activated.
fn transforms(category: GUID, hardware: bool, from: GUID, to: GUID) -> Vec<IMFActivate> {
    let input = MFT_REGISTER_TYPE_INFO {
        guidMajorType: MFMediaType_Video,
        guidSubtype: from,
    };
    let output = MFT_REGISTER_TYPE_INFO {
        guidMajorType: MFMediaType_Video,
        guidSubtype: to,
    };
    let mut list: *mut Option<IMFActivate> = std::ptr::null_mut();
    let mut count = 0u32;
    // SAFETY: MFTEnumEx fills `list` with `count` activates, which are taken
    // out (the caller owns them) and whose array is freed with CoTaskMemFree.
    unsafe {
        if MFTEnumEx(
            category,
            if hardware {
                MFT_ENUM_FLAG_HARDWARE | MFT_ENUM_FLAG_SORTANDFILTER
            } else {
                MFT_ENUM_FLAG_SYNCMFT | MFT_ENUM_FLAG_LOCALMFT | MFT_ENUM_FLAG_SORTANDFILTER
            },
            Some(&input),
            Some(&output),
            &mut list,
            &mut count,
        )
        .is_err()
        {
            return Vec::new();
        }
        let activates = (0..count as usize)
            .filter_map(|i| (*list.add(i)).take())
            .collect();
        CoTaskMemFree(Some(list as *const _));
        activates
    }
}

/// A Direct3D 11 device manager on the default GPU, for a decoder to decode
/// HEVC with.
/// Returns: an error when the GPU has no HEVC (Main) decoder.
fn hevc_gpu() -> windows::core::Result<IMFDXGIDeviceManager> {
    use windows::Win32::Graphics::Direct3D::D3D_DRIVER_TYPE_HARDWARE;
    use windows::Win32::Graphics::Direct3D11::*;
    use windows::Win32::Graphics::Dxgi::Common::DXGI_FORMAT_NV12;
    // SAFETY: creating and configuring a device and a manager this function
    // owns; the manager keeps its own reference to the device.
    unsafe {
        let mut device = None;
        D3D11CreateDevice(
            None,
            D3D_DRIVER_TYPE_HARDWARE,
            Default::default(),
            D3D11_CREATE_DEVICE_VIDEO_SUPPORT,
            None,
            D3D11_SDK_VERSION,
            Some(&mut device),
            None,
            None,
        )?;
        let device = device.ok_or_else(|| windows::core::Error::from(MF_E_TOPO_CODEC_NOT_FOUND))?;
        let video = device.cast::<ID3D11VideoDevice>()?;
        if !video
            .CheckVideoDecoderFormat(&D3D11_DECODER_PROFILE_HEVC_VLD_MAIN, DXGI_FORMAT_NV12)?
            .as_bool()
        {
            return Err(MF_E_TOPO_CODEC_NOT_FOUND.into());
        }
        // Media Foundation uses the device from its own threads too.
        let _ = device
            .cast::<ID3D11Multithread>()?
            .SetMultithreadProtected(true);
        let mut token = 0;
        let mut manager = None;
        MFCreateDXGIDeviceManager(&mut token, &mut manager)?;
        let manager =
            manager.ok_or_else(|| windows::core::Error::from(MF_E_TOPO_CODEC_NOT_FOUND))?;
        manager.ResetDevice(&device, token)?;
        Ok(manager)
    }
}

/// The system's decoder for a codec, giving NV12 frames.
pub struct Decoder {
    t: IMFTransform,
    /// The decoded picture's size and row stride, once known.
    format: Option<(u32, u32, u32)>,
    n: i64,
}

impl Decoder {
    /// Opens the system's (synchronous) decoder for a codec in low-latency
    /// mode: Microsoft's for H.264, the video extensions' for HEVC and VP9.
    /// Inputs: the stream's picture size, which the extensions' decoders need
    /// up front.
    /// Notes: HEVC decodes only on the GPU: the extension's software path
    /// never outputs pictures for streams with uneven tile columns (what
    /// Qualcomm's hardware encoder sends above 1024 pixels wide).
    pub fn new(codec: Codec, width: u32, height: u32) -> Result<Self, String> {
        startup()?;
        let open = || -> windows::core::Result<IMFTransform> {
            let t = match codec {
                // SAFETY: creating a transform this function owns.
                Codec::H264 => unsafe {
                    CoCreateInstance(&CLSID_MSH264DecoderMFT, None, CLSCTX_INPROC_SERVER)?
                },
                _ => find_transform(
                    MFT_CATEGORY_VIDEO_DECODER,
                    false,
                    codec.subtype(),
                    MFVideoFormat_NV12,
                )
                .ok_or_else(|| windows::core::Error::from(MF_E_TOPO_CODEC_NOT_FOUND))?,
            };
            // SAFETY: configuring the transform this function owns; the
            // transform keeps its own reference to the device manager.
            unsafe {
                if let Ok(attrs) = t.GetAttributes() {
                    let _ = attrs.SetUINT32(&MF_LOW_LATENCY, 1);
                }
                if codec == Codec::Hevc {
                    let gpu = hevc_gpu()?;
                    t.ProcessMessage(MFT_MESSAGE_SET_D3D_MANAGER, gpu.as_raw() as usize)?;
                }
                t.SetInputType(0, &video_type(&codec.subtype(), width, height, 0)?, 0)?;
            }
            Ok(t)
        };
        let t = open().map_err(|e| format!("{} decoder: {e}", codec.name()))?;
        let mut d = Decoder {
            t,
            format: None,
            n: 0,
        };
        d.pick_output()
            .map_err(|e| format!("{} decoder: {e}", codec.name()))?;
        // SAFETY: streaming messages on the configured transform.
        unsafe {
            let _ = d.t.ProcessMessage(MFT_MESSAGE_NOTIFY_BEGIN_STREAMING, 0);
            let _ = d.t.ProcessMessage(MFT_MESSAGE_NOTIFY_START_OF_STREAM, 0);
        }
        Ok(d)
    }

    /// Chooses NV12 output and reads the picture's size and stride.
    fn pick_output(&mut self) -> windows::core::Result<()> {
        // SAFETY: COM calls on the transform and the types it offers.
        unsafe {
            for i in 0.. {
                let t = self.t.GetOutputAvailableType(0, i)?;
                if t.GetGUID(&MF_MT_SUBTYPE)? != MFVideoFormat_NV12 {
                    continue;
                }
                self.t.SetOutputType(0, &t, 0)?;
                self.format = t.GetUINT64(&MF_MT_FRAME_SIZE).ok().map(|size| {
                    let (w, h) = ((size >> 32) as u32, size as u32);
                    let stride = t.GetUINT32(&MF_MT_DEFAULT_STRIDE).unwrap_or(w);
                    (w, h, stride.max(w))
                });
                return Ok(());
            }
        }
        Ok(())
    }

    /// Decodes one access unit (Annex B for H.264 and HEVC, a frame for VP9).
    /// Returns: the pictures ready now, each as (width, height, stride,
    /// NV12 bytes); empty while the decoder needs more input.
    pub fn decode(&mut self, unit: &[u8]) -> Result<Vec<Picture>, String> {
        let mut run = || -> windows::core::Result<Vec<Picture>> {
            let sample = sample_from(unit, self.n * 166_666, 166_666)?;
            // SAFETY: the transform takes its own reference to the sample.
            unsafe { self.t.ProcessInput(0, &sample, 0)? };
            let mut pictures = Vec::new();
            loop {
                let (out, changed) = drain(&self.t)?;
                for (bytes, _) in out {
                    if let Some((w, h, stride)) = self.format {
                        pictures.push((w, h, stride, bytes));
                    }
                }
                if !changed {
                    break;
                }
                self.pick_output()?;
            }
            Ok(pictures)
        };
        let out = run().map_err(|e| format!("decoder: {e}"))?;
        self.n += 1;
        Ok(out)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::video;

    /// Every codec this machine both encodes and decodes carries a moving
    /// picture through (H.264 wherever Media Foundation is; HEVC and VP9
    /// with the GPU or the Store's video extensions).
    #[test]
    fn frames_survive_encoding_and_decoding() {
        let mut tried = Vec::new();
        for codec in Codec::ALL {
            let (Ok(encoder), Ok(decoder)) = (
                Encoder::new(codec, 320, 192, 30, 1_000_000),
                Decoder::new(codec, 320, 192),
            ) else {
                // Windows N and Server images without Media Foundation; a
                // codec without its extension.
                eprintln!("skipped: {}", codec.name());
                continue;
            };
            tried.push((codec.name(), encoder.events.is_some()));
            round_trip(encoder, decoder, 320, 192);
        }
        eprintln!("round trips (codec, hardware): {tried:?}");
    }

    /// The GPU's HEVC encoder's 1080p stream decodes (Qualcomm's splits it
    /// into uneven tile columns, which only the GPU decoder takes).
    #[test]
    #[ignore = "needs a GPU that encodes and decodes HEVC; run alone"]
    fn hardware_hevc_survives_at_1080p() {
        let (w, h) = (1920, 1080);
        let encoder = Encoder::new(Codec::Hevc, w, h, 30, 4_000_000).unwrap();
        assert!(encoder.events.is_some(), "no hardware HEVC encoder");
        round_trip(encoder, Decoder::new(Codec::Hevc, w, h).unwrap(), w, h);
    }

    /// Carries 30 frames of a moving picture through, checking the last one.
    fn round_trip(mut encoder: Encoder, mut decoder: Decoder, w: u32, h: u32) {
        let mut pictures = Vec::new();
        let mut nv12 = Vec::new();
        for n in 0..30usize {
            // A grey picture with a bright square moving across it.
            let bgra: Vec<u8> = (0..(w * h) as usize)
                .flat_map(|i| {
                    let (x, y) = (i % w as usize, i / w as usize);
                    let on = (x + 300 - n * 8) % 300 < 40 && (60..120).contains(&y);
                    if on {
                        [240u8, 240, 240, 255]
                    } else {
                        [90, 90, 90, 255]
                    }
                })
                .collect();
            video::bgra_to_nv12(
                &bgra,
                w as usize,
                h as usize,
                w as usize * 4,
                w as usize,
                h as usize,
                &mut nv12,
            );
            for (unit, _) in encoder.encode(&nv12, n == 0).unwrap() {
                pictures.extend(decoder.decode(&unit).unwrap());
            }
        }
        assert!(!pictures.is_empty(), "no picture came back");
        let (pw, ph, stride, bytes) = &pictures[pictures.len() - 1];
        assert!((*pw, *ph) >= (w, h), "decoded {pw}×{ph}");
        // The grey background is grey again (within codec loss).
        let mut rgba = Vec::new();
        video::nv12_to_rgba(
            bytes,
            w as usize,
            h as usize,
            *stride as usize,
            *ph as usize,
            &mut rgba,
        );
        let corner = &rgba[..4];
        assert!(
            corner[..3].iter().all(|c| (*c as i32 - 90).abs() < 12),
            "{corner:?}"
        );
    }
}

#[cfg(test)]
mod latency {
    use super::*;

    /// How many frames the encoder takes before its first output: each one
    /// held is a frame period of added latency.
    #[test]
    fn the_encoder_answers_within_two_frames() {
        let (w, h) = (640u32, 360u32);
        let Ok(mut e) = Encoder::new(Codec::H264, w, h, 30, 2_000_000) else {
            return eprintln!("skipped: no encoder");
        };
        let mut n = 0;
        let first = loop {
            n += 1;
            let nv12: Vec<u8> = (0..(w * h * 3 / 2) as usize)
                .map(|i| (i + n * 7) as u8)
                .collect();
            if !e.encode(&nv12, n == 1).unwrap().is_empty() || n == 60 {
                break n;
            }
        };
        eprintln!(
            "first output after {first} frames (hardware: {})",
            e.events.is_some()
        );
        assert!(first <= 2, "the encoder holds {first} frames");
    }
}
