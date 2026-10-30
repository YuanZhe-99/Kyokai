//! NanoKVM USB on macOS: the capture card through AVFoundation, which
//! decodes its MJPEG into BGRA (so a Mac host sends it encoded only, never
//! Raw), the serial port through termios (`/dev/cu.*`). macOS asks the
//! host's user for the camera the first time a card opens.

pub(super) use super::termios::Serial;
use super::{Picture, Port};
use crate::video::Got;
use block2::RcBlock;
use dispatch2::DispatchQueue;
use objc2::rc::Retained;
use objc2::runtime::{AnyObject, Bool, NSObject, NSObjectProtocol, ProtocolObject};
use objc2::{AllocAnyThread, DefinedClass, define_class, msg_send};
use objc2_av_foundation::{
    AVAuthorizationStatus, AVCaptureConnection, AVCaptureDevice, AVCaptureDeviceDiscoverySession,
    AVCaptureDeviceInput, AVCaptureDevicePosition, AVCaptureDeviceTypeExternal, AVCaptureOutput,
    AVCaptureSession, AVCaptureSessionPreset1920x1080, AVCaptureVideoDataOutput,
    AVCaptureVideoDataOutputSampleBufferDelegate, AVMediaTypeVideo,
};
use objc2_core_media::CMSampleBuffer;
use objc2_core_video::{
    CVPixelBufferGetBaseAddress, CVPixelBufferGetBytesPerRow, CVPixelBufferGetHeight,
    CVPixelBufferGetWidth, CVPixelBufferLockBaseAddress, CVPixelBufferLockFlags,
    CVPixelBufferUnlockBaseAddress, kCVPixelBufferPixelFormatTypeKey, kCVPixelFormatType_32BGRA,
    kCVReturnSuccess,
};
use objc2_foundation::{NSArray, NSDictionary, NSNumber, NSString};
use std::sync::{Arc, Condvar, Mutex};
use std::time::Duration;

/// AVFoundation gives decoded pictures, so Raw is not offered.
pub(super) const RAW: bool = false;

/// The NanoKVM USB's capture chip (MacroSilicon) as a UVC model ID names
/// its vendor, in decimal (0x345F).
const MACROSILICON: &str = "VendorID_13407";

/// The external video devices (UVC capture cards and cameras).
fn external() -> Vec<Retained<AVCaptureDevice>> {
    // SAFETY: AVFoundation's constants are valid for the process; the
    // discovery session is only read.
    unsafe {
        let types = NSArray::from_slice(&[AVCaptureDeviceTypeExternal]);
        let session =
            AVCaptureDeviceDiscoverySession::discoverySessionWithDeviceTypes_mediaType_position(
                &types,
                AVMediaTypeVideo,
                AVCaptureDevicePosition::Unspecified,
            );
        session.devices().to_vec()
    }
}

/// The capture cards, by their unique IDs.
pub(super) fn videos() -> Vec<Port> {
    external()
        .iter()
        .map(|d| {
            // SAFETY: plain reads of a device.
            let (id, name, model) = unsafe {
                (
                    d.uniqueID().to_string(),
                    d.localizedName().to_string(),
                    d.modelID().to_string(),
                )
            };
            Port {
                id,
                name,
                nanokvm: model.contains(MACROSILICON),
                hub: String::new(),
            }
        })
        .collect()
}

/// The USB serial ports: `/dev/cu.usbserial-*` (macOS's own CH340
/// driver), `/dev/cu.wchusbserial*` (WCH's) and `/dev/cu.usbmodem*`.
pub(super) fn serials() -> Vec<Port> {
    let Ok(dir) = std::fs::read_dir("/dev") else {
        return Vec::new();
    };
    let mut out: Vec<Port> = dir
        .flatten()
        .filter_map(|e| {
            let node = e.file_name().to_string_lossy().into_owned();
            let ch340 = node.starts_with("cu.usbserial") || node.starts_with("cu.wchusbserial");
            if !(ch340 || node.starts_with("cu.usbmodem")) {
                return None;
            }
            let path = format!("/dev/{node}");
            Some(Port {
                name: path.clone(),
                id: path,
                nanokvm: ch340,
                hub: String::new(),
            })
        })
        .collect();
    out.sort_by(|a, b| a.id.cmp(&b.id));
    out
}

/// The newest picture of the card.
#[derive(Default)]
struct Latest {
    bgra: Vec<u8>,
    width: usize,
    height: usize,
    stride: usize,
    seq: u64,
}

/// What the capture's callbacks and the capture thread share.
#[derive(Default)]
struct Shared {
    latest: Mutex<Latest>,
    ready: Condvar,
}

impl Shared {
    /// Copies a sample buffer's picture as the newest one.
    fn take(&self, sample: &CMSampleBuffer) {
        // SAFETY: the sample buffer is valid for the callback.
        let Some(pixels) = (unsafe { sample.image_buffer() }) else {
            return;
        };
        let lock = CVPixelBufferLockFlags::ReadOnly;
        // SAFETY: locks a valid pixel buffer for reading.
        if unsafe { CVPixelBufferLockBaseAddress(&pixels, lock) } != kCVReturnSuccess {
            return;
        }
        let (width, height) = (
            CVPixelBufferGetWidth(&pixels),
            CVPixelBufferGetHeight(&pixels),
        );
        let stride = CVPixelBufferGetBytesPerRow(&pixels);
        let base = CVPixelBufferGetBaseAddress(&pixels) as *const u8;
        if !base.is_null()
            && let Ok(mut l) = self.latest.lock()
        {
            // SAFETY: while the buffer is locked, `base` points at `height`
            // rows of `stride` bytes.
            let rows = unsafe { std::slice::from_raw_parts(base, stride * height) };
            l.bgra.clear();
            l.bgra.extend_from_slice(rows);
            (l.width, l.height, l.stride) = (width, height, stride);
            l.seq += 1;
        }
        // SAFETY: balances the lock above.
        unsafe { CVPixelBufferUnlockBaseAddress(&pixels, lock) };
        self.ready.notify_all();
    }
}

define_class!(
    // SAFETY: NSObject has no subclassing requirements, and `CardOutput`
    // has no Drop logic beyond its ivars.
    #[unsafe(super(NSObject))]
    #[name = "KyokaiNanoKvmOutput"]
    #[ivars = Arc<Shared>]
    /// Receives the capture card's pictures.
    struct CardOutput;

    unsafe impl NSObjectProtocol for CardOutput {}

    unsafe impl AVCaptureVideoDataOutputSampleBufferDelegate for CardOutput {
        #[unsafe(method(captureOutput:didOutputSampleBuffer:fromConnection:))]
        fn did_output(
            &self,
            _output: &AVCaptureOutput,
            sample: &CMSampleBuffer,
            _connection: &AVCaptureConnection,
        ) {
            crate::guarded(|| self.ivars().take(sample));
        }
    }
);

impl CardOutput {
    fn new(shared: Arc<Shared>) -> Retained<Self> {
        let this = Self::alloc().set_ivars(shared);
        // SAFETY: NSObject's init on a freshly allocated object.
        unsafe { msg_send![super(this), init] }
    }
}

/// Whether this app may use cameras, asking the host's user the first time
/// (macOS attributes the prompt to the app).
fn camera_allowed() -> Result<(), String> {
    // SAFETY: AVFoundation's constant, valid for the process.
    let Some(video) = (unsafe { AVMediaTypeVideo }) else {
        return Err("no video media type".into());
    };
    // SAFETY: a class method with a valid media type.
    match unsafe { AVCaptureDevice::authorizationStatusForMediaType(video) } {
        AVAuthorizationStatus::Authorized => Ok(()),
        AVAuthorizationStatus::NotDetermined => {
            let (tx, rx) = std::sync::mpsc::sync_channel(1);
            let done = RcBlock::new(move |granted: Bool| {
                let _ = tx.try_send(granted.as_bool());
            });
            // SAFETY: the handler is a block of the expected signature.
            unsafe { AVCaptureDevice::requestAccessForMediaType_completionHandler(video, &done) };
            match rx.recv_timeout(Duration::from_secs(120)) {
                Ok(true) => Ok(()),
                _ => Err("Camera access was not allowed".into()),
            }
        }
        _ => Err(
            "Camera access is off for Kyokai (System Settings, Privacy & Security, Camera)".into(),
        ),
    }
}

/// The capture card through an AVFoundation session giving BGRA.
pub(super) struct Camera {
    session: Retained<AVCaptureSession>,
    _output: Retained<CardOutput>,
    _data: Retained<AVCaptureVideoDataOutput>,
    shared: Arc<Shared>,
    seen: u64,
}

// SAFETY: the session and its objects are used by the one thread that
// owns the Camera; their callbacks run on their own serial queue and
// reach it only through `Shared`.
unsafe impl Send for Camera {}

impl Camera {
    /// Opens the card whose unique ID is `id` at 1920×1080 when it has it,
    /// and starts it.
    /// Returns: the camera and its pictures' size, once the first picture
    /// came (within 3 seconds).
    pub(super) fn open(id: &str) -> Result<(Camera, u32, u32), String> {
        camera_allowed()?;
        let shared = Arc::new(Shared::default());
        let output = CardOutput::new(shared.clone());
        // SAFETY: AVFoundation calls on objects this function owns; the
        // output keeps the delegate weakly and the Camera holds it.
        let (session, data) = unsafe {
            let device = AVCaptureDevice::deviceWithUniqueID(&NSString::from_str(id))
                .ok_or("the capture card is not there")?;
            let input = AVCaptureDeviceInput::deviceInputWithDevice_error(&device)
                .map_err(|e| e.localizedDescription().to_string())?;
            let session = AVCaptureSession::new();
            if !session.canAddInput(&input) {
                return Err("the capture card cannot be used".into());
            }
            session.addInput(&input);
            if session.canSetSessionPreset(AVCaptureSessionPreset1920x1080) {
                session.setSessionPreset(AVCaptureSessionPreset1920x1080);
            }
            let data = AVCaptureVideoDataOutput::new();
            // kCVPixelBufferPixelFormatTypeKey is a CFString, toll-free
            // bridged to NSString.
            let key: &NSString =
                &*(kCVPixelBufferPixelFormatTypeKey as *const _ as *const NSString);
            let format = NSNumber::new_u32(kCVPixelFormatType_32BGRA);
            let value: &AnyObject = &format;
            let settings = NSDictionary::<NSString, AnyObject>::from_slices(&[key], &[value]);
            data.setVideoSettings(Some(&settings));
            data.setAlwaysDiscardsLateVideoFrames(true);
            let queue = DispatchQueue::new("com.yuanzhe.kyokai.nanokvm", None);
            data.setSampleBufferDelegate_queue(
                Some(ProtocolObject::from_ref(&*output)),
                Some(&queue),
            );
            if !session.canAddOutput(&data) {
                return Err("the capture card gives no pictures".into());
            }
            session.addOutput(&data);
            session.startRunning();
            (session, data)
        };
        let mut cam = Camera {
            session,
            _output: output,
            _data: data,
            shared,
            seen: 0,
        };
        let size = {
            let l = cam.shared.latest.lock().map_err(|_| "poisoned")?;
            let (l, _) = cam
                .shared
                .ready
                .wait_timeout_while(l, Duration::from_secs(3), |l| l.seq == 0)
                .map_err(|_| "poisoned")?;
            (l.seq > 0).then_some((l.width as u32, l.height as u32))
        };
        let (w, h) = size.ok_or("the capture card gave no picture")?;
        cam.seen = 0;
        Ok((cam, w, h))
    }

    /// Waits up to `timeout` for a picture newer than the last one taken.
    pub(super) fn frame(&mut self, timeout: Duration, take: &mut dyn FnMut(Picture<'_>)) -> Got {
        let seen = self.seen;
        let Ok(latest) = self.shared.latest.lock() else {
            return Got::Lost;
        };
        let Ok((l, _)) = self
            .shared
            .ready
            .wait_timeout_while(latest, timeout, |l| l.seq == seen)
        else {
            return Got::Lost;
        };
        if l.seq == seen {
            return Got::Nothing;
        }
        self.seen = l.seq;
        take(Picture::Bgra(crate::video::Picture {
            bgra: &l.bgra,
            width: l.width,
            height: l.height,
            stride: l.stride,
        }));
        Got::Picture
    }
}

impl Drop for Camera {
    fn drop(&mut self) {
        // SAFETY: stops the session this Camera owns.
        unsafe { self.session.stopRunning() };
    }
}
