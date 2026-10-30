//! The host's sound (doc/en-us/computer-use.md, "Sound"): what the
//! computer plays, captured (ScreenCaptureKit on macOS, WASAPI loopback on
//! Windows, the default output's monitor through PipeWire on Linux), turned
//! into 48 kHz stereo and sent as 10 ms Opus packets, none
//! while it is silent. In
//! the helper's modes the session agent captures it on the user's desktop.
#![cfg_attr(
    not(any(windows, target_os = "macos", target_os = "linux")),
    allow(dead_code)
)]

use crate::opus;
use std::collections::VecDeque;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Condvar, Mutex};
use std::time::{Duration, Instant};
use tokio::sync::mpsc;

/// Packets waiting for the session: about half a second; newer ones are
/// dropped beyond it.
const QUEUE: usize = 50;
/// How often a sound capture that stopped or could not start is tried again.
const RESTART: Duration = Duration::from_secs(2);
/// Silent frames sent before the host goes quiet: half a second, so the
/// viewer plays the sound's tail out (as RustDesk's zero gate).
const SILENT_AFTER: u32 = 50;
/// Below this a sample counts as silence.
const QUIET: f32 = 1e-4;
/// The most sound kept before it is encoded: a second.
const MOST: usize = opus::RATE * opus::CHANNELS;

/// Sound captured and not yet encoded: interleaved stereo at 48 kHz, as
/// the capture delivers it at its own rate and channels.
#[derive(Default)]
pub struct Pcm {
    inner: Mutex<PcmInner>,
    ready: Condvar,
}

#[derive(Default)]
struct PcmInner {
    samples: VecDeque<f32>,
    resampler: Resampler,
}

impl Pcm {
    /// Adds captured sound: `frames` interleaved, of `channels` (the first
    /// two kept, mono doubled) at `rate` frames a second.
    pub fn push(&self, frames: &[f32], channels: usize, rate: u32) {
        if channels == 0 || rate == 0 {
            return;
        }
        let Ok(mut p) = self.inner.lock() else {
            return;
        };
        let PcmInner { samples, resampler } = &mut *p;
        let stereo = frames.chunks_exact(channels).map(|f| {
            let l = f[0];
            [l, if channels > 1 { f[1] } else { l }]
        });
        resampler.run(stereo, rate, |f| samples.extend(f));
        let extra = samples.len().saturating_sub(MOST);
        samples.drain(..extra);
        self.ready.notify_all();
    }

    /// Waits up to `timeout` for one Opus frame of sound.
    /// Returns: the frame, or None when not enough came.
    fn take(&self, timeout: Duration) -> Option<Vec<f32>> {
        let want = opus::FRAME * opus::CHANNELS;
        let p = self.inner.lock().ok()?;
        let (mut p, _) = self
            .ready
            .wait_timeout_while(p, timeout, |p| p.samples.len() < want)
            .ok()?;
        (p.samples.len() >= want).then(|| p.samples.drain(..want).collect())
    }
}

/// The silence gate: whether a frame goes out. After SILENT_AFTER silent
/// frames in a row none do, until sound comes back.
fn gate(silent: &mut u32, frame: &[f32]) -> bool {
    if frame.iter().any(|s| s.abs() >= QUIET) {
        *silent = 0;
        return true;
    }
    *silent = silent.saturating_add(1);
    *silent <= SILENT_AFTER
}

/// A linear resampler to 48 kHz that carries its place across calls.
#[derive(Default)]
struct Resampler {
    /// The last frame of the previous call.
    prev: [f32; 2],
    /// Where the next output frame lies: 0 is `prev`, 1 the call's first
    /// frame.
    at: f64,
}

impl Resampler {
    /// Resamples `input` frames at `rate` to 48 kHz, handing each output
    /// frame to `out`.
    fn run(
        &mut self,
        input: impl Iterator<Item = [f32; 2]>,
        rate: u32,
        mut out: impl FnMut([f32; 2]),
    ) {
        if rate as usize == opus::RATE {
            for f in input {
                out(f);
            }
            return;
        }
        let step = rate as f64 / opus::RATE as f64;
        let mut frames = Vec::with_capacity(1024);
        frames.push(self.prev);
        frames.extend(input);
        let n = frames.len() - 1;
        if n == 0 {
            return;
        }
        while self.at < n as f64 {
            let i = self.at as usize;
            let t = (self.at - i as f64) as f32;
            let (a, b) = (frames[i], frames[i + 1]);
            out([a[0] + (b[0] - a[0]) * t, a[1] + (b[1] - a[1]) * t]);
            self.at += step;
        }
        self.at -= n as f64;
        self.prev = frames[n];
    }
}

/// The host's sound for one session: Opus packets until it is dropped.
pub struct Source {
    pub packets: mpsc::Receiver<Vec<u8>>,
    keep: Keep,
}

/// What keeps a source going and stops it when the source is dropped.
enum Keep {
    /// Captured here: the encoder thread's stop flag (the thread owns the
    /// capture).
    Local { stop: Arc<AtomicBool> },
    /// Captured by the helper's session agent.
    Agent {
        link: Arc<crate::agent::Link>,
        stream: u32,
    },
}

impl Drop for Source {
    fn drop(&mut self) {
        match &self.keep {
            Keep::Local { stop } => stop.store(true, Ordering::Relaxed),
            Keep::Agent { link, stream } => link.close_audio(*stream),
        }
    }
}

impl Source {
    /// Starts the computer's sound: through the session agent in the
    /// helper's modes, here otherwise.
    /// Returns: why it cannot (no output device, no permission).
    pub fn open() -> Result<Source, String> {
        match crate::agent::link() {
            Some(link) => link.open_audio(),
            None => Source::local(),
        }
    }

    /// A source fed by the agent's packets.
    pub(crate) fn from_agent(
        packets: mpsc::Receiver<Vec<u8>>,
        link: Arc<crate::agent::Link>,
        stream: u32,
    ) -> Source {
        Source {
            packets,
            keep: Keep::Agent { link, stream },
        }
    }

    /// Captures and encodes the sound in this process, from the source the
    /// host's user chose; a new choice applies to it at once.
    pub fn local() -> Result<Source, String> {
        let pcm = Arc::new(Pcm::default());
        let (mut generation, source) = chosen();
        let mut capture = Some(Capture::start(pcm.clone(), &source)?);
        let mut encoder = opus::Encoder::new()?;
        let (tx, packets) = mpsc::channel(QUEUE);
        let stop = Arc::new(AtomicBool::new(false));
        let stopped = stop.clone();
        std::thread::spawn(move || {
            let mut silent = 0u32;
            let mut tried = Instant::now();
            while !stopped.load(Ordering::Relaxed) {
                let (now, source) = chosen();
                // A new choice applies at once; a capture that stopped by
                // itself (macOS ends it when the display sleeps) or could
                // not start is tried again every RESTART.
                let ended = capture.as_ref().is_none_or(Capture::stopped);
                if now != generation || (ended && tried.elapsed() >= RESTART) {
                    generation = now;
                    tried = Instant::now();
                    drop(capture.take());
                    capture = match Capture::start(pcm.clone(), &source) {
                        Ok(c) => Some(c),
                        Err(e) => {
                            log::info!("sound: cannot capture: {e}");
                            None
                        }
                    };
                }
                let Some(frame) = pcm.take(Duration::from_millis(100)) else {
                    continue;
                };
                if !gate(&mut silent, &frame) {
                    continue;
                }
                let Ok(packet) = encoder.encode(&frame) else {
                    continue;
                };
                // A session that falls behind loses sound, not time.
                if let Err(mpsc::error::TrySendError::Closed(_)) = tx.try_send(packet) {
                    break;
                }
            }
        });
        Ok(Source {
            packets,
            keep: Keep::Local { stop },
        })
    }
}

/// The sound to share, as the host's user chose it: "" for what the
/// computer plays, or an input device's id (`devices`); and how often it
/// was chosen, so running sources follow a change.
static SOURCE: std::sync::Mutex<(u64, String)> = std::sync::Mutex::new((0, String::new()));

/// Sets the sound to share. Running sources switch to it.
pub fn set_source(source: &str) {
    if let Ok(mut s) = SOURCE.lock()
        && s.1 != source
    {
        *s = (s.0 + 1, source.to_owned());
    }
}

/// The sound to share and its generation.
pub(crate) fn chosen() -> (u64, String) {
    SOURCE.lock().map(|s| s.clone()).unwrap_or_default()
}

/// An input device the host may share instead of what it plays.
#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize, serde::Deserialize)]
pub struct Device {
    pub id: String,
    pub name: String,
}

/// The input devices of this computer (microphones, line inputs, capture
/// cards), by their system ids.
pub fn devices() -> Vec<Device> {
    #[cfg(target_os = "macos")]
    return input::devices();
    #[cfg(windows)]
    return loopback::devices();
    #[cfg(target_os = "linux")]
    return pipe::devices();
    #[allow(unreachable_code)]
    Vec::new()
}

/// What captures the sound to share.
enum Capture {
    /// What the computer plays.
    #[cfg(target_os = "macos")]
    System(crate::mac::AudioCapture),
    /// An input device through an audio queue.
    #[cfg(target_os = "macos")]
    Device(#[allow(dead_code)] input::Input),
    /// What the computer plays, or an input device, through WASAPI.
    #[cfg(windows)]
    Wasapi(#[allow(dead_code)] loopback::Loopback),
    /// What the computer plays, or an input device, through PipeWire.
    #[cfg(target_os = "linux")]
    Pipe(pipe::Pipe),
}

impl Capture {
    /// Starts capturing `source` ("" for what the computer plays) into
    /// `pcm`.
    /// Returns: why it cannot (no such device, no permission).
    fn start(pcm: Arc<Pcm>, source: &str) -> Result<Capture, String> {
        #[cfg(target_os = "macos")]
        return match source {
            "" => crate::mac::AudioCapture::start(pcm).map(Capture::System),
            id => input::Input::start(pcm, id).map(Capture::Device),
        };
        #[cfg(windows)]
        return loopback::Loopback::start(pcm, source).map(Capture::Wasapi);
        #[cfg(target_os = "linux")]
        return pipe::Pipe::start(pcm, source).map(Capture::Pipe);
        #[allow(unreachable_code)]
        {
            let _ = (pcm, source);
            Err("sound is not captured on this system".into())
        }
    }

    /// Whether the capture stopped by itself and needs starting again.
    fn stopped(&self) -> bool {
        match self {
            #[cfg(target_os = "macos")]
            Capture::System(c) => c.stopped(),
            #[cfg(target_os = "linux")]
            Capture::Pipe(c) => c.stopped(),
            #[allow(unreachable_patterns)]
            _ => false,
        }
    }
}

/// An input device on macOS: listed through Core Audio, captured through an
/// audio queue, which converts it to 48 kHz stereo floats (the Microphone
/// permission covers it).
#[cfg(target_os = "macos")]
mod input {
    use super::{Device, Pcm};
    use objc2_core_foundation::{CFRetained, CFString};
    use std::ffi::c_void;
    use std::ptr::NonNull;
    use std::sync::Arc;

    type AudioObjectId = u32;
    type AudioQueueRef = *mut c_void;

    #[repr(C)]
    struct PropertyAddress {
        selector: u32,
        scope: u32,
        element: u32,
    }

    #[repr(C)]
    struct StreamDescription {
        sample_rate: f64,
        format_id: u32,
        format_flags: u32,
        bytes_per_packet: u32,
        frames_per_packet: u32,
        bytes_per_frame: u32,
        channels_per_frame: u32,
        bits_per_channel: u32,
        reserved: u32,
    }

    #[repr(C)]
    struct QueueBuffer {
        capacity: u32,
        data: *mut c_void,
        size: u32,
        user: *mut c_void,
        description_capacity: u32,
        descriptions: *mut c_void,
        description_count: u32,
    }

    type InputCallback = unsafe extern "C" fn(
        user: *mut c_void,
        queue: AudioQueueRef,
        buffer: *mut QueueBuffer,
        start: *const c_void,
        packets: u32,
        descriptions: *const c_void,
    );

    #[link(name = "CoreAudio", kind = "framework")]
    unsafe extern "C" {
        fn AudioObjectGetPropertyDataSize(
            object: AudioObjectId,
            address: *const PropertyAddress,
            qualifier_size: u32,
            qualifier: *const c_void,
            size: *mut u32,
        ) -> i32;
        fn AudioObjectGetPropertyData(
            object: AudioObjectId,
            address: *const PropertyAddress,
            qualifier_size: u32,
            qualifier: *const c_void,
            size: *mut u32,
            data: *mut c_void,
        ) -> i32;
    }

    #[link(name = "AudioToolbox", kind = "framework")]
    unsafe extern "C" {
        fn AudioQueueNewInput(
            format: *const StreamDescription,
            callback: InputCallback,
            user: *mut c_void,
            run_loop: *const c_void,
            run_loop_mode: *const c_void,
            flags: u32,
            queue: *mut AudioQueueRef,
        ) -> i32;
        fn AudioQueueSetProperty(
            queue: AudioQueueRef,
            id: u32,
            data: *const c_void,
            size: u32,
        ) -> i32;
        fn AudioQueueAllocateBuffer(
            queue: AudioQueueRef,
            size: u32,
            buffer: *mut *mut QueueBuffer,
        ) -> i32;
        fn AudioQueueEnqueueBuffer(
            queue: AudioQueueRef,
            buffer: *mut QueueBuffer,
            descriptions: u32,
            description: *const c_void,
        ) -> i32;
        fn AudioQueueStart(queue: AudioQueueRef, start: *const c_void) -> i32;
        fn AudioQueueStop(queue: AudioQueueRef, immediate: u8) -> i32;
        fn AudioQueueDispose(queue: AudioQueueRef, immediate: u8) -> i32;
    }

    const fn code(c: &[u8; 4]) -> u32 {
        u32::from_be_bytes(*c)
    }
    const SYSTEM_OBJECT: AudioObjectId = 1;
    const DEVICES: u32 = code(b"dev#");
    const SCOPE_GLOBAL: u32 = code(b"glob");
    const SCOPE_INPUT: u32 = code(b"inpt");
    const STREAM_CONFIGURATION: u32 = code(b"slay");
    const NAME: u32 = code(b"lnam");
    const UID: u32 = code(b"uid ");
    const LINEAR_PCM: u32 = code(b"lpcm");
    /// Float, packed (interleaved).
    const FLOAT_PACKED: u32 = 1 | 8;
    const CURRENT_DEVICE: u32 = code(b"aqcd");
    /// Buffers in flight, 10 ms each.
    const BUFFERS: usize = 4;

    /// Reads a property's raw bytes.
    fn property(object: AudioObjectId, selector: u32, scope: u32) -> Option<Vec<u8>> {
        let address = PropertyAddress {
            selector,
            scope,
            element: 0,
        };
        let mut size = 0u32;
        // SAFETY: a valid address; `size` receives the data's size.
        if unsafe {
            AudioObjectGetPropertyDataSize(object, &address, 0, std::ptr::null(), &mut size)
        } != 0
        {
            return None;
        }
        // u64s keep the bytes aligned for the structures read from them.
        let mut data = vec![0u64; (size as usize).div_ceil(8)];
        // SAFETY: `data` holds at least `size` bytes.
        let status = unsafe {
            AudioObjectGetPropertyData(
                object,
                &address,
                0,
                std::ptr::null(),
                &mut size,
                data.as_mut_ptr().cast(),
            )
        };
        if status != 0 {
            return None;
        }
        // SAFETY: the u64s are plain bytes, `size` of them written.
        let bytes =
            unsafe { std::slice::from_raw_parts(data.as_ptr().cast::<u8>(), size as usize) };
        Some(bytes.to_vec())
    }

    /// Reads a string property (a +1 CFString the caller releases).
    fn string(object: AudioObjectId, selector: u32) -> Option<String> {
        let bytes = property(object, selector, SCOPE_GLOBAL)?;
        let raw = usize::from_ne_bytes(bytes.get(..size_of::<usize>())?.try_into().ok()?);
        let s = NonNull::new(raw as *mut CFString)?;
        // SAFETY: Core Audio handed over a retained string.
        Some(unsafe { CFRetained::from_raw(s) }.to_string())
    }

    /// The input channels of a device: the channels of its input streams.
    fn input_channels(device: AudioObjectId) -> u32 {
        let Some(bytes) = property(device, STREAM_CONFIGURATION, SCOPE_INPUT) else {
            return 0;
        };
        // An AudioBufferList: a u32 count, then 16-byte AudioBuffers from
        // offset 8, whose first u32 is the channels.
        let count = bytes
            .get(..4)
            .map_or(0, |b| u32::from_ne_bytes(b.try_into().expect("4 bytes")));
        (0..count as usize)
            .filter_map(|i| bytes.get(8 + i * 16..12 + i * 16))
            .map(|b| u32::from_ne_bytes(b.try_into().expect("4 bytes")))
            .sum()
    }

    /// The devices with inputs, by their unique ids.
    pub fn devices() -> Vec<Device> {
        let Some(bytes) = property(SYSTEM_OBJECT, DEVICES, SCOPE_GLOBAL) else {
            return Vec::new();
        };
        bytes
            .as_chunks::<4>()
            .0
            .iter()
            .map(|b| u32::from_ne_bytes(*b))
            .filter(|&d| input_channels(d) > 0)
            .filter_map(|d| {
                Some(Device {
                    id: string(d, UID)?,
                    name: string(d, NAME).unwrap_or_default(),
                })
            })
            .collect()
    }

    /// An input device's sound, captured by an audio queue on its own
    /// thread.
    pub struct Input {
        queue: AudioQueueRef,
        /// The callback's target; it lives as long as the queue (freed
        /// after the queue is disposed, see Drop).
        pcm: *mut Arc<Pcm>,
    }

    // SAFETY: the queue is only started, then stopped and disposed (on
    // drop); audio queues may be stopped from any thread.
    unsafe impl Send for Input {}

    /// Hands a filled buffer to the sound and gives it back to the queue.
    unsafe extern "C" fn filled(
        user: *mut c_void,
        queue: AudioQueueRef,
        buffer: *mut QueueBuffer,
        _start: *const c_void,
        _packets: u32,
        _descriptions: *const c_void,
    ) {
        // SAFETY: `user` is the input's `Arc<Pcm>`, alive until the queue
        // is disposed; `buffer` is one of its buffers, holding `size` bytes
        // of interleaved stereo floats.
        unsafe {
            let pcm = &*(user as *const Arc<Pcm>);
            let b = &*buffer;
            let samples = std::slice::from_raw_parts(b.data as *const f32, b.size as usize / 4);
            // A panic must not reach the C caller (it would abort).
            crate::guarded(|| pcm.push(samples, 2, crate::opus::RATE as u32));
            AudioQueueEnqueueBuffer(queue, buffer, 0, std::ptr::null());
        }
    }

    impl Input {
        /// Starts capturing the device `id` into `pcm`.
        /// Returns: why the queue refused (no such device, no Microphone
        /// permission).
        pub fn start(pcm: Arc<Pcm>, id: &str) -> Result<Input, String> {
            let format = StreamDescription {
                sample_rate: crate::opus::RATE as f64,
                format_id: LINEAR_PCM,
                format_flags: FLOAT_PACKED,
                bytes_per_packet: 8,
                frames_per_packet: 1,
                bytes_per_frame: 8,
                channels_per_frame: 2,
                bits_per_channel: 32,
                reserved: 0,
            };
            let user = Box::into_raw(Box::new(pcm));
            let mut queue: AudioQueueRef = std::ptr::null_mut();
            // SAFETY: a valid format and callback; `user` outlives the
            // queue; the queue runs its callbacks on its own thread.
            let status = unsafe {
                AudioQueueNewInput(
                    &format,
                    filled,
                    user.cast(),
                    std::ptr::null(),
                    std::ptr::null(),
                    0,
                    &mut queue,
                )
            };
            if status != 0 || queue.is_null() {
                // SAFETY: no queue holds `user`.
                drop(unsafe { Box::from_raw(user) });
                return Err(format!("the sound input cannot open: {status}"));
            }
            let input = Input { queue, pcm: user };
            let device = CFString::from_str(id);
            let raw = CFRetained::as_ptr(&device).as_ptr() as *const c_void;
            // SAFETY: the queue's current device is a CFString uid, passed
            // by reference.
            let status = unsafe {
                AudioQueueSetProperty(
                    queue,
                    CURRENT_DEVICE,
                    (&raw const raw).cast(),
                    size_of::<*const c_void>() as u32,
                )
            };
            if status != 0 {
                return Err(format!("the sound input {id} cannot be used: {status}"));
            }
            let bytes = (crate::opus::FRAME * 8) as u32;
            for _ in 0..BUFFERS {
                let mut buffer = std::ptr::null_mut();
                // SAFETY: the queue allocates and owns the buffer.
                unsafe {
                    if AudioQueueAllocateBuffer(queue, bytes, &mut buffer) == 0 {
                        AudioQueueEnqueueBuffer(queue, buffer, 0, std::ptr::null());
                    }
                }
            }
            // SAFETY: a configured queue, started now.
            let status = unsafe { AudioQueueStart(queue, std::ptr::null()) };
            if status != 0 {
                return Err(format!("the sound input cannot start: {status}"));
            }
            Ok(input)
        }
    }

    impl Drop for Input {
        fn drop(&mut self) {
            // SAFETY: stops and disposes of the queue this input made, then
            // frees the callback's target, which no callback uses any more.
            unsafe {
                AudioQueueStop(self.queue, 1);
                AudioQueueDispose(self.queue, 1);
                drop(Box::from_raw(self.pcm));
            }
        }
    }
}

/// What the default output plays (its monitor), or an input device, through
/// PipeWire, as 48 kHz stereo floats.
#[cfg(target_os = "linux")]
mod pipe {
    use super::{Device, Pcm};
    use pipewire as pw;
    use pw::spa;
    use std::sync::Arc;
    use std::sync::atomic::{AtomicBool, Ordering};

    /// The capture thread, stopped when this is dropped.
    pub struct Pipe {
        quit: pw::channel::Sender<()>,
        thread: Option<std::thread::JoinHandle<()>>,
        stopped: Arc<AtomicBool>,
    }

    impl Pipe {
        /// Captures `source` ("" for what the default output plays, else an
        /// input node's name) into `pcm`.
        /// Returns: why not (no PipeWire).
        pub fn start(pcm: Arc<Pcm>, source: &str) -> Result<Pipe, String> {
            let (quit, quit_rx) = pw::channel::channel();
            let (ready_tx, ready) = std::sync::mpsc::channel();
            let stopped = Arc::new(AtomicBool::new(false));
            let ended = stopped.clone();
            let source = source.to_owned();
            let thread = std::thread::spawn(move || {
                if let Err(e) = run(&pcm, &source, quit_rx, &ready_tx) {
                    let _ = ready_tx.send(Err(e));
                }
                ended.store(true, Ordering::Relaxed);
            });
            ready
                .recv()
                .map_err(|_| "the sound capture did not start".to_string())??;
            Ok(Pipe {
                quit,
                thread: Some(thread),
                stopped,
            })
        }

        /// Whether the stream ended by itself (PipeWire went away).
        pub fn stopped(&self) -> bool {
            self.stopped.load(Ordering::Relaxed)
        }
    }

    impl Drop for Pipe {
        fn drop(&mut self) {
            let _ = self.quit.send(());
            if let Some(t) = self.thread.take() {
                let _ = t.join();
            }
        }
    }

    /// The stream's thread: connects, says so, and runs until `quit`.
    fn run(
        pcm: &Arc<Pcm>,
        source: &str,
        quit: pw::channel::Receiver<()>,
        ready: &std::sync::mpsc::Sender<Result<(), String>>,
    ) -> Result<(), String> {
        pw::init();
        let mainloop = pw::main_loop::MainLoopRc::new(None).map_err(|e| e.to_string())?;
        let context = pw::context::ContextRc::new(&mainloop, None).map_err(|e| e.to_string())?;
        let core = context
            .connect_rc(None)
            .map_err(|e| format!("PipeWire: {e}"))?;
        let mut props = pw::properties::properties! {
            *pw::keys::MEDIA_TYPE => "Audio",
            *pw::keys::MEDIA_CATEGORY => "Capture",
            *pw::keys::MEDIA_ROLE => "Music",
            *pw::keys::NODE_NAME => "kyokai-sound",
        };
        if source.is_empty() {
            // The default output's monitor: what the computer plays.
            props.insert("stream.capture.sink", "true");
        } else {
            props.insert("target.object", source);
        }
        let stream =
            pw::stream::StreamBox::new(&core, "kyokai-sound", props).map_err(|e| e.to_string())?;
        let ended = mainloop.clone();
        let _quit = quit.attach(mainloop.loop_(), move |()| ended.quit());
        let failed = mainloop.clone();
        let into = pcm.clone();
        let _listener = stream
            .add_local_listener::<()>()
            .state_changed(move |_, _, _, new| {
                if matches!(new, pw::stream::StreamState::Error(_)) {
                    failed.quit();
                }
            })
            .process(move |stream, _| {
                let Some(mut buffer) = stream.dequeue_buffer() else {
                    return;
                };
                let Some(data) = buffer.datas_mut().first_mut() else {
                    return;
                };
                let size = data.chunk().size() as usize;
                let Some(bytes) = data.data() else {
                    return;
                };
                let samples: Vec<f32> = bytes[..size.min(bytes.len())]
                    .as_chunks::<4>()
                    .0
                    .iter()
                    .map(|b| f32::from_le_bytes(*b))
                    .collect();
                into.push(&samples, CHANNELS, RATE);
            })
            .register()
            .map_err(|e| e.to_string())?;
        let mut info = spa::param::audio::AudioInfoRaw::new();
        info.set_format(spa::param::audio::AudioFormat::F32LE);
        info.set_rate(RATE);
        info.set_channels(CHANNELS as u32);
        let obj = spa::pod::Object {
            type_: spa::utils::SpaTypes::ObjectParamFormat.as_raw(),
            id: spa::param::ParamType::EnumFormat.as_raw(),
            properties: info.into(),
        };
        let values: Vec<u8> = spa::pod::serialize::PodSerializer::serialize(
            std::io::Cursor::new(Vec::new()),
            &spa::pod::Value::Object(obj),
        )
        .map_err(|e| format!("{e:?}"))?
        .0
        .into_inner();
        let mut params = [spa::pod::Pod::from_bytes(&values).ok_or("bad format")?];
        stream
            .connect(
                spa::utils::Direction::Input,
                None,
                pw::stream::StreamFlags::AUTOCONNECT
                    | pw::stream::StreamFlags::MAP_BUFFERS
                    | pw::stream::StreamFlags::RT_PROCESS,
                &mut params,
            )
            .map_err(|e| e.to_string())?;
        let _ = ready.send(Ok(()));
        mainloop.run();
        Ok(())
    }

    /// What the stream asks for: PipeWire converts to it.
    const RATE: u32 = 48_000;
    const CHANNELS: usize = 2;

    /// The input devices (PipeWire's `Audio/Source` nodes), by node name.
    pub fn devices() -> Vec<Device> {
        list().unwrap_or_default()
    }

    fn list() -> Result<Vec<Device>, pw::Error> {
        use std::cell::{Cell, RefCell};
        use std::rc::Rc;
        pw::init();
        let mainloop = pw::main_loop::MainLoopRc::new(None)?;
        let context = pw::context::ContextRc::new(&mainloop, None)?;
        let core = context.connect_rc(None)?;
        let registry = core.get_registry()?;
        let found = Rc::new(RefCell::new(Vec::new()));
        let done = Rc::new(Cell::new(false));
        let pending = core.sync(0)?;
        let (seen, finished, stop) = (found.clone(), done.clone(), mainloop.clone());
        let _core = core
            .add_listener_local()
            .done(move |id, seq| {
                if id == pw::core::PW_ID_CORE && seq == pending {
                    finished.set(true);
                    stop.quit();
                }
            })
            .register();
        let _registry = registry
            .add_listener_local()
            .global(move |g| {
                let Some(props) = g.props else {
                    return;
                };
                if props.get("media.class") != Some("Audio/Source") {
                    return;
                }
                if let Some(id) = props.get("node.name") {
                    let name = props.get("node.description").unwrap_or(id);
                    seen.borrow_mut().push(Device {
                        id: id.to_owned(),
                        name: name.to_owned(),
                    });
                }
            })
            .register();
        while !done.get() {
            mainloop.run();
        }
        Ok(found.take())
    }
}

/// What the default output device plays (WASAPI loopback), or an input
/// device, through WASAPI.
#[cfg(windows)]
mod loopback {
    use super::{Device, Pcm};
    use std::sync::Arc;
    use std::sync::atomic::{AtomicBool, Ordering};
    use std::time::Duration;
    use windows::Win32::Devices::FunctionDiscovery::PKEY_Device_FriendlyName;
    use windows::Win32::Media::Audio::{
        AUDCLNT_BUFFERFLAGS_SILENT, AUDCLNT_SHAREMODE_SHARED, AUDCLNT_STREAMFLAGS_LOOPBACK,
        DEVICE_STATE_ACTIVE, IAudioCaptureClient, IAudioClient, IMMDevice, IMMDeviceEnumerator,
        MMDeviceEnumerator, WAVEFORMATEX, eCapture, eConsole, eRender,
    };
    use windows::Win32::System::Com::StructuredStorage::PropVariantToStringAlloc;
    use windows::Win32::System::Com::{
        CLSCTX_ALL, COINIT_MULTITHREADED, CoCreateInstance, CoInitializeEx, CoTaskMemFree,
        CoUninitialize, STGM_READ,
    };
    use windows::core::{HSTRING, PWSTR};

    /// The capture thread, stopped when this is dropped.
    pub struct Loopback {
        stop: Arc<AtomicBool>,
    }

    impl Drop for Loopback {
        fn drop(&mut self) {
            self.stop.store(true, Ordering::Relaxed);
        }
    }

    impl Loopback {
        /// Starts capturing `source` into `pcm`: "" for what the default
        /// output device plays, else an input device's id.
        /// Returns: why WASAPI refused (no such device).
        pub fn start(pcm: Arc<Pcm>, source: &str) -> Result<Loopback, String> {
            let stop = Arc::new(AtomicBool::new(false));
            let stopped = stop.clone();
            let source = source.to_owned();
            let (tx, rx) = std::sync::mpsc::sync_channel(1);
            std::thread::spawn(move || {
                // SAFETY: COM is initialized for this thread and released
                // when it ends.
                unsafe {
                    let _ = CoInitializeEx(None, COINIT_MULTITHREADED);
                    match open(&source) {
                        Ok((client, capture, channels, rate, float)) => {
                            let _ = tx.send(Ok(()));
                            run(&capture, channels, rate, float, &pcm, &stopped);
                            let _ = client.Stop();
                        }
                        Err(e) => {
                            let _ = tx.send(Err(e));
                        }
                    }
                    CoUninitialize();
                }
            });
            rx.recv()
                .map_err(|_| "the sound capture did not start".to_string())??;
            Ok(Loopback { stop })
        }
    }

    fn e(what: &'static str) -> impl Fn(windows::core::Error) -> String {
        move |err| format!("{what}: {err}")
    }

    /// The text of a string the system allocated, which it frees.
    unsafe fn take_string(p: PWSTR) -> String {
        // SAFETY: a valid, NUL-terminated string from the system, freed
        // with CoTaskMemFree after it is read.
        unsafe {
            let s = p.to_string().unwrap_or_default();
            CoTaskMemFree(Some(p.0 as *const _));
            s
        }
    }

    /// The active input devices, by their endpoint ids.
    pub fn devices() -> Vec<Device> {
        // SAFETY: COM calls on objects this function owns, on a thread it
        // initializes for COM for the call.
        unsafe {
            let _ = CoInitializeEx(None, COINIT_MULTITHREADED);
            let list = (|| -> windows::core::Result<Vec<Device>> {
                let devices: IMMDeviceEnumerator =
                    CoCreateInstance(&MMDeviceEnumerator, None, CLSCTX_ALL)?;
                let all = devices.EnumAudioEndpoints(eCapture, DEVICE_STATE_ACTIVE)?;
                let mut out = Vec::new();
                for i in 0..all.GetCount()? {
                    let device = all.Item(i)?;
                    let id = take_string(device.GetId()?);
                    let name = device
                        .OpenPropertyStore(STGM_READ)
                        .and_then(|store| store.GetValue(&PKEY_Device_FriendlyName))
                        .and_then(|v| PropVariantToStringAlloc(&v))
                        .map(|p| take_string(p))
                        .unwrap_or_default();
                    out.push(Device { id, name });
                }
                Ok(out)
            })()
            .unwrap_or_default();
            CoUninitialize();
            list
        }
    }

    /// Opens `source`'s stream in its own format: the default output
    /// device's loopback for "", else the input device of that id.
    /// Returns: the client, its capture service, the channels, the rate and
    /// whether samples are 32-bit floats (else 16-bit integers).
    unsafe fn open(
        source: &str,
    ) -> Result<(IAudioClient, IAudioCaptureClient, usize, u32, bool), String> {
        // SAFETY: COM calls on objects this function owns; the mix format
        // is read and then freed with CoTaskMemFree.
        unsafe {
            let devices: IMMDeviceEnumerator =
                CoCreateInstance(&MMDeviceEnumerator, None, CLSCTX_ALL).map_err(e("devices"))?;
            let (device, flags): (IMMDevice, u32) = match source {
                "" => (
                    devices
                        .GetDefaultAudioEndpoint(eRender, eConsole)
                        .map_err(e("no output device"))?,
                    AUDCLNT_STREAMFLAGS_LOOPBACK,
                ),
                id => (
                    devices
                        .GetDevice(&HSTRING::from(id))
                        .map_err(e("no such sound input"))?,
                    0,
                ),
            };
            let client: IAudioClient = device.Activate(CLSCTX_ALL, None).map_err(e("client"))?;
            let mix = client.GetMixFormat().map_err(e("format"))?;
            let format: WAVEFORMATEX = std::ptr::read_unaligned(mix);
            let channels = format.nChannels as usize;
            let rate = format.nSamplesPerSec;
            let bits = format.wBitsPerSample;
            let started = client.Initialize(
                AUDCLNT_SHAREMODE_SHARED,
                flags,
                1_000_000, // 100 ms, in 100 ns units
                0,
                mix,
                None,
            );
            CoTaskMemFree(Some(mix as *const _));
            started.map_err(e("capture"))?;
            if channels == 0 || !(bits == 32 || bits == 16) {
                return Err(format!(
                    "unsupported sound format: {bits}-bit, {channels} channels"
                ));
            }
            let capture: IAudioCaptureClient = client.GetService().map_err(e("capture"))?;
            client.Start().map_err(e("start"))?;
            Ok((client, capture, channels, rate, bits == 32))
        }
    }

    /// Reads the stream every 5 ms until stopped.
    unsafe fn run(
        capture: &IAudioCaptureClient,
        channels: usize,
        rate: u32,
        float: bool,
        pcm: &Pcm,
        stop: &AtomicBool,
    ) {
        let mut samples: Vec<f32> = Vec::new();
        while !stop.load(Ordering::Relaxed) {
            std::thread::sleep(Duration::from_millis(5));
            // SAFETY: the capture client's buffers are read between
            // GetBuffer and ReleaseBuffer, for the frames it reported.
            unsafe {
                while capture.GetNextPacketSize().unwrap_or(0) > 0 {
                    let mut data = std::ptr::null_mut();
                    let (mut frames, mut flags) = (0u32, 0u32);
                    if capture
                        .GetBuffer(&mut data, &mut frames, &mut flags, None, None)
                        .is_err()
                    {
                        break;
                    }
                    let n = frames as usize * channels;
                    samples.clear();
                    if flags & AUDCLNT_BUFFERFLAGS_SILENT.0 as u32 != 0 || data.is_null() {
                        samples.resize(n, 0.0);
                    } else if float {
                        samples
                            .extend_from_slice(std::slice::from_raw_parts(data as *const f32, n));
                    } else {
                        let ints = std::slice::from_raw_parts(data as *const i16, n);
                        samples.extend(ints.iter().map(|s| *s as f32 / 32768.0));
                    }
                    let _ = capture.ReleaseBuffer(frames);
                    pcm.push(&samples, channels, rate);
                }
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Where PipeWire runs, the capture of what the computer plays connects
    /// and keeps running (nothing is played: it may be a person's computer).
    #[cfg(target_os = "linux")]
    #[test]
    fn pipewire_captures_what_the_computer_plays() {
        let Ok(pipe) = pipe::Pipe::start(Arc::new(Pcm::default()), "") else {
            return; // no PipeWire here (a build machine)
        };
        std::thread::sleep(Duration::from_millis(500));
        assert!(!pipe.stopped());
    }

    /// The input devices come with their system ids (a computer may have
    /// none, as a build machine).
    #[test]
    fn input_devices_are_listed_by_id() {
        let devices = devices();
        eprintln!("sound inputs here: {devices:?}");
        assert!(devices.iter().all(|d| !d.id.is_empty()));
    }

    /// Silence goes out for half a second, then nothing until sound
    /// returns.
    #[test]
    fn silence_stops_the_packets() {
        let (quiet, loud) = ([0.0f32; 8], [0.0, 0.2, 0.0, 0.0]);
        let mut silent = 0;
        let sent = (0..SILENT_AFTER + 20)
            .filter(|_| gate(&mut silent, &quiet))
            .count();
        assert_eq!(sent, SILENT_AFTER as usize);
        assert!(gate(&mut silent, &loud), "sound goes out at once");
        assert!(gate(&mut silent, &quiet), "and its tail after it");
    }

    /// Sound at another rate comes out at 48 kHz, as long as it went in;
    /// mono is doubled; a whole frame is taken once there is one.
    #[test]
    fn sound_becomes_48_khz_stereo() {
        let pcm = Pcm::default();
        // 100 ms of mono at 44.1 kHz, in uneven pieces.
        let tone: Vec<f32> = (0..4410).map(|i| (i as f32 * 0.01).sin()).collect();
        for piece in tone.chunks(441 * 3 + 7) {
            pcm.push(piece, 1, 44_100);
        }
        let got = pcm.inner.lock().unwrap().samples.len() / 2;
        assert!((4795..=4805).contains(&got), "{got} frames");
        let frame = pcm.take(Duration::ZERO).expect("a frame");
        assert_eq!(frame.len(), opus::FRAME * opus::CHANNELS);
        assert!(frame.chunks(2).all(|f| f[0] == f[1]), "mono is doubled");
        // 48 kHz stereo passes as it is.
        let pcm = Pcm::default();
        pcm.push(&[0.1, 0.2, 0.3, 0.4], 2, 48_000);
        assert_eq!(pcm.inner.lock().unwrap().samples, [0.1, 0.2, 0.3, 0.4]);
        assert!(pcm.take(Duration::ZERO).is_none(), "not a whole frame yet");
    }
}
