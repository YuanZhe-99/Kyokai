//! The host's sound (doc/en-us/computer-use.md, "Sound"): what the
//! computer plays, captured (ScreenCaptureKit on macOS, WASAPI loopback on
//! Windows), turned into 48 kHz stereo and sent as 20 ms Opus packets. In
//! the helper's modes the session agent captures it on the user's desktop.

use crate::opus;
use std::collections::VecDeque;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Condvar, Mutex};
use std::time::Duration;
use tokio::sync::mpsc;

/// Packets waiting for the session: about half a second; newer ones are
/// dropped beyond it.
const QUEUE: usize = 25;
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
    /// Captured here: the capture and the encoder thread's stop flag.
    Local {
        stop: Arc<AtomicBool>,
        _capture: Capture,
    },
    /// Captured by the helper's session agent.
    Agent {
        link: Arc<crate::agent::Link>,
        stream: u32,
    },
}

impl Drop for Source {
    fn drop(&mut self) {
        match &self.keep {
            Keep::Local { stop, .. } => stop.store(true, Ordering::Relaxed),
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

    /// Captures and encodes the sound in this process.
    pub fn local() -> Result<Source, String> {
        let pcm = Arc::new(Pcm::default());
        let capture = Capture::start(pcm.clone())?;
        let mut encoder = opus::Encoder::new()?;
        let (tx, packets) = mpsc::channel(QUEUE);
        let stop = Arc::new(AtomicBool::new(false));
        let stopped = stop.clone();
        std::thread::spawn(move || {
            while !stopped.load(Ordering::Relaxed) {
                let Some(frame) = pcm.take(Duration::from_millis(100)) else {
                    continue;
                };
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
            keep: Keep::Local {
                stop,
                _capture: capture,
            },
        })
    }
}

#[cfg(target_os = "macos")]
type Capture = crate::mac::AudioCapture;

#[cfg(windows)]
type Capture = loopback::Loopback;

/// No capture elsewhere.
#[cfg(not(any(windows, target_os = "macos")))]
struct Capture;

#[cfg(not(any(windows, target_os = "macos")))]
impl Capture {
    fn start(_pcm: Arc<Pcm>) -> Result<Capture, String> {
        Err("sound is not captured on this system".into())
    }
}

/// What the default output device plays, through WASAPI loopback.
#[cfg(windows)]
mod loopback {
    use super::Pcm;
    use std::sync::Arc;
    use std::sync::atomic::{AtomicBool, Ordering};
    use std::time::Duration;
    use windows::Win32::Media::Audio::{
        AUDCLNT_BUFFERFLAGS_SILENT, AUDCLNT_SHAREMODE_SHARED, AUDCLNT_STREAMFLAGS_LOOPBACK,
        IAudioCaptureClient, IAudioClient, IMMDeviceEnumerator, MMDeviceEnumerator, WAVEFORMATEX,
        eConsole, eRender,
    };
    use windows::Win32::System::Com::{
        CLSCTX_ALL, COINIT_MULTITHREADED, CoCreateInstance, CoInitializeEx, CoTaskMemFree,
        CoUninitialize,
    };

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
        /// Starts capturing the default output device into `pcm`.
        /// Returns: why WASAPI refused (no output device).
        pub fn start(pcm: Arc<Pcm>) -> Result<Loopback, String> {
            let stop = Arc::new(AtomicBool::new(false));
            let stopped = stop.clone();
            let (tx, rx) = std::sync::mpsc::sync_channel(1);
            std::thread::spawn(move || {
                // SAFETY: COM is initialized for this thread and released
                // when it ends.
                unsafe {
                    let _ = CoInitializeEx(None, COINIT_MULTITHREADED);
                    match open() {
                        Ok((client, capture, channels, rate, float)) => {
                            let _ = tx.send(Ok(()));
                            run(&client, &capture, channels, rate, float, &pcm, &stopped);
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

    /// Opens the default output device's loopback stream in its own format.
    /// Returns: the client, its capture service, the channels, the rate and
    /// whether samples are 32-bit floats (else 16-bit integers).
    unsafe fn open() -> Result<(IAudioClient, IAudioCaptureClient, usize, u32, bool), String> {
        fn e(what: &'static str) -> impl Fn(windows::core::Error) -> String {
            move |err| format!("{what}: {err}")
        }
        // SAFETY: COM calls on objects this function owns; the mix format
        // is read and then freed with CoTaskMemFree.
        unsafe {
            let devices: IMMDeviceEnumerator =
                CoCreateInstance(&MMDeviceEnumerator, None, CLSCTX_ALL).map_err(e("devices"))?;
            let device = devices
                .GetDefaultAudioEndpoint(eRender, eConsole)
                .map_err(e("no output device"))?;
            let client: IAudioClient = device.Activate(CLSCTX_ALL, None).map_err(e("client"))?;
            let mix = client.GetMixFormat().map_err(e("format"))?;
            let format: WAVEFORMATEX = std::ptr::read_unaligned(mix);
            let channels = format.nChannels as usize;
            let rate = format.nSamplesPerSec;
            let bits = format.wBitsPerSample;
            let started = client.Initialize(
                AUDCLNT_SHAREMODE_SHARED,
                AUDCLNT_STREAMFLAGS_LOOPBACK,
                1_000_000, // 100 ms, in 100 ns units
                0,
                mix,
                None,
            );
            CoTaskMemFree(Some(mix as *const _));
            started.map_err(e("loopback"))?;
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

    /// Reads the loopback stream every 10 ms until stopped.
    unsafe fn run(
        _client: &IAudioClient,
        capture: &IAudioCaptureClient,
        channels: usize,
        rate: u32,
        float: bool,
        pcm: &Pcm,
        stop: &AtomicBool,
    ) {
        let mut samples: Vec<f32> = Vec::new();
        while !stop.load(Ordering::Relaxed) {
            std::thread::sleep(Duration::from_millis(10));
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
