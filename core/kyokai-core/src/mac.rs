//! macOS (doc/en-us/computer-use.md): the displays through Core Graphics
//! (in points, the unit of its global coordinates), the screen through
//! ScreenCaptureKit with Kyokai's own windows left out, the pointer, and
//! the privacy permissions capture and input need.

use crate::ai::desktop::{Desktop, Frame};
use crate::input::{Area, Event, Guard, INJECTED_TAG, LocalInput, Mods, PointerAction, lock_for};
use crate::video::{Display, Got, Grab, MAX_FPS, MAX_SIZE, Take, fit, order};
use block2::RcBlock;
use dispatch2::DispatchQueue;
use objc2::rc::Retained;
use objc2::runtime::ProtocolObject;
use objc2::{AllocAnyThread, DefinedClass, define_class, msg_send};
use objc2_application_services::{
    AXIsProcessTrusted, AXIsProcessTrustedWithOptions, kAXTrustedCheckOptionPrompt,
};
use objc2_core_foundation::{
    CFBoolean, CFDictionary, CFMachPort, CFNumber, CFRetained, CFRunLoop, CFString, CFType,
    CGPoint, CGRect, kCFRunLoopCommonModes,
};
use objc2_core_graphics::{
    CGDirectDisplayID, CGDisplayBounds, CGDisplayCopyDisplayMode, CGDisplayIsMain, CGDisplayMode,
    CGError, CGEvent, CGEventField, CGEventFlags, CGEventMask, CGEventSource, CGEventSourceStateID,
    CGEventTapLocation, CGEventTapOptions, CGEventTapPlacement, CGEventTapProxy, CGEventType,
    CGGetActiveDisplayList, CGKeyCode, CGMouseButton, CGPreflightScreenCaptureAccess,
    CGRectMakeWithDictionaryRepresentation, CGRequestScreenCaptureAccess, CGScrollEventUnit,
    CGWindowListCopyWindowInfo, CGWindowListOption, kCGWindowBounds, kCGWindowLayer,
    kCGWindowOwnerPID,
};
use objc2_core_media::{CMSampleBuffer, CMTime, CMTimeFlags};
use objc2_core_video::{
    CVPixelBufferGetBaseAddress, CVPixelBufferGetBytesPerRow, CVPixelBufferGetHeight,
    CVPixelBufferGetWidth, CVPixelBufferLockBaseAddress, CVPixelBufferLockFlags,
    CVPixelBufferUnlockBaseAddress, kCVPixelFormatType_32BGRA, kCVReturnSuccess,
};
use objc2_foundation::{NSArray, NSError, NSObject, NSObjectProtocol};
use objc2_screen_capture_kit::{
    SCContentFilter, SCRunningApplication, SCShareableContent, SCStream, SCStreamConfiguration,
    SCStreamDelegate, SCStreamOutput, SCStreamOutputType,
};
use serde::Serialize;
use std::collections::BTreeSet;
use std::ffi::c_void;
use std::ptr::NonNull;
use std::sync::atomic::{AtomicBool, AtomicPtr, Ordering};
use std::sync::mpsc::sync_channel;
use std::sync::{Arc, Condvar, Mutex};
use std::time::{Duration, Instant};

/// The app's bundle identifier (mygo.json): its windows are left out of
/// every capture.
const BUNDLE_ID: &str = "com.yuanzhe.kyokai";
/// How long ScreenCaptureKit may take to answer a request.
const WAIT: Duration = Duration::from_secs(5);
/// Displays listed at most.
const MAX_DISPLAYS: usize = 16;

/// The privacy permissions Kyokai needs here, as granted now.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
pub struct Permissions {
    /// Screen Recording: capture.
    pub screen: bool,
    /// Accessibility: injecting input.
    pub accessibility: bool,
    /// The Keychain item that keeps the secrets' key was not refused.
    pub keychain: bool,
}

/// What this process may do now; `keychain` says whether the secrets
/// store opened without a refusal.
/// Notes: a Screen Recording grant counts only after the app restarts.
pub fn permissions(keychain: bool) -> Permissions {
    Permissions {
        keychain,
        screen: CGPreflightScreenCaptureAccess(),
        // SAFETY: a plain query.
        accessibility: unsafe { AXIsProcessTrusted() },
    }
}

/// Asks macOS for a permission (`screen` or `accessibility`): it shows its
/// prompt, or for a permission asked before, nothing (System Settings
/// grants it then).
/// Returns: whether it is granted now.
pub fn request(name: &str) -> Result<bool, String> {
    match name {
        "screen" => Ok(CGRequestScreenCaptureAccess()),
        "accessibility" => {
            // SAFETY: an immutable constant of the framework.
            let key: &CFString = unsafe { kAXTrustedCheckOptionPrompt };
            let options = CFDictionary::from_slices(&[key], &[CFBoolean::new(true)]);
            // SAFETY: the options dictionary maps the prompt key to a boolean.
            Ok(unsafe { AXIsProcessTrustedWithOptions(Some(options.as_ref())) })
        }
        _ => Err("unknown permission".into()),
    }
}

/// The ids of the active displays.
fn active() -> Vec<CGDirectDisplayID> {
    let mut ids = [0 as CGDirectDisplayID; MAX_DISPLAYS];
    let mut n = 0u32;
    // SAFETY: `ids` has room for MAX_DISPLAYS ids and `n` receives how many
    // were written.
    let err = unsafe { CGGetActiveDisplayList(MAX_DISPLAYS as u32, ids.as_mut_ptr(), &mut n) };
    if err != CGError::Success {
        return Vec::new();
    }
    ids[..(n as usize).min(MAX_DISPLAYS)].to_vec()
}

/// A display's place in global coordinates (points, top left at the main
/// display's top left).
fn bounds(id: CGDirectDisplayID) -> Display {
    let b = CGDisplayBounds(id);
    Display {
        x: b.origin.x.round() as i32,
        y: b.origin.y.round() as i32,
        width: b.size.width.round() as u32,
        height: b.size.height.round() as u32,
        primary: CGDisplayIsMain(id),
    }
}

/// The active displays, in the viewer's order.
pub fn displays() -> Vec<Display> {
    let mut list: Vec<Display> = active().into_iter().map(bounds).collect();
    order(&mut list);
    list
}

/// The id of the display at `d`'s place.
fn display_id(d: Display) -> Option<CGDirectDisplayID> {
    active().into_iter().find(|&id| {
        let b = bounds(id);
        (b.x, b.y) == (d.x, d.y)
    })
}

/// A display's size in pixels (twice its points on a Retina display).
fn pixel_size(id: CGDirectDisplayID, points: Display) -> (u32, u32) {
    let mode = CGDisplayCopyDisplayMode(id);
    let (w, h) = (
        CGDisplayMode::pixel_width(mode.as_deref()),
        CGDisplayMode::pixel_height(mode.as_deref()),
    );
    if w == 0 || h == 0 {
        (points.width, points.height)
    } else {
        (w as u32, h as u32)
    }
}

/// Where the pointer is, as fractions of the main display (outside 0–1 on
/// another display), and whether it is shown.
pub fn pointer() -> Option<(f32, f32, bool)> {
    let main = displays().into_iter().find(|d| d.primary)?;
    pointer_in(Area {
        x: main.x,
        y: main.y,
        width: main.width,
        height: main.height,
    })
}

/// Where the pointer is, as fractions of `area` (points; outside 0–1
/// elsewhere), and whether it is shown.
pub fn pointer_in(area: Area) -> Option<(f32, f32, bool)> {
    let event = CGEvent::new(None)?;
    let at = CGEvent::location(Some(&event));
    Some((
        ((at.x - area.x as f64) / area.width.max(1) as f64) as f32,
        ((at.y - area.y as f64) / area.height.max(1) as f64) as f32,
        true,
    ))
}

/// An error a framework call handed back, in words.
fn error_text(error: *mut NSError, otherwise: &str) -> String {
    // SAFETY: the error, when not null, is valid for the callback's
    // duration.
    match unsafe { error.as_ref() } {
        Some(e) => e.localizedDescription().to_string(),
        None => otherwise.into(),
    }
}

/// A snapshot of what can be captured, handed from ScreenCaptureKit's
/// queue to the thread that waits for it.
struct Content(Retained<SCShareableContent>);

// SAFETY: the snapshot is immutable; the waiting thread only reads it after
// the callback handed it over.
unsafe impl Send for Content {}

/// The displays and applications that can be captured now.
/// Returns: an error without the Screen Recording permission.
fn shareable() -> Result<Retained<SCShareableContent>, String> {
    let (tx, rx) = sync_channel(1);
    let done = RcBlock::new(
        move |content: *mut SCShareableContent, error: *mut NSError| {
            // SAFETY: `content` is null or valid for the callback; retaining it
            // keeps it past the call.
            let got = unsafe { Retained::retain(content) }
                .map(Content)
                .ok_or_else(|| error_text(error, "nothing can be captured"));
            let _ = tx.try_send(got);
        },
    );
    // SAFETY: the handler is a block of the expected signature.
    unsafe { SCShareableContent::getShareableContentWithCompletionHandler(&done) };
    rx.recv_timeout(WAIT)
        .map_err(|_| "ScreenCaptureKit did not answer".to_string())?
        .map(|c| c.0)
}

/// Runs a ScreenCaptureKit call that reports back through a completion
/// handler, and waits for it.
fn finished(call: impl FnOnce(&block2::DynBlock<dyn Fn(*mut NSError)>)) -> Result<(), String> {
    let (tx, rx) = sync_channel(1);
    let done = RcBlock::new(move |error: *mut NSError| {
        let result = if error.is_null() {
            Ok(())
        } else {
            Err(error_text(error, ""))
        };
        let _ = tx.try_send(result);
    });
    call(&done);
    rx.recv_timeout(WAIT)
        .map_err(|_| "ScreenCaptureKit did not answer".to_string())?
}

/// The newest picture of a stream.
#[derive(Default)]
struct Latest {
    bgra: Vec<u8>,
    width: usize,
    height: usize,
    stride: usize,
    /// Counts the pictures; the reader knows which it took.
    seq: u64,
    /// The stream stopped (the display went away, the permission was
    /// taken back).
    stopped: bool,
}

/// What the stream's callbacks and the capture thread share.
#[derive(Default)]
struct Shared {
    latest: Mutex<Latest>,
    ready: Condvar,
}

impl Shared {
    /// Copies a sample buffer's picture as the newest one.
    /// Notes: idle frames (an unchanged screen) carry no picture.
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
            // Reuses the buffer: nothing allocates once the size settled.
            l.bgra.clear();
            l.bgra.extend_from_slice(rows);
            (l.width, l.height, l.stride) = (width, height, stride);
            l.seq += 1;
        }
        // SAFETY: balances the lock above.
        unsafe { CVPixelBufferUnlockBaseAddress(&pixels, lock) };
        self.ready.notify_all();
    }

    /// Marks the stream as stopped.
    fn stop(&self) {
        if let Ok(mut l) = self.latest.lock() {
            l.stopped = true;
        }
        self.ready.notify_all();
    }
}

define_class!(
    // SAFETY: NSObject has no subclassing requirements, and `Output` has
    // no Drop logic beyond its ivars.
    #[unsafe(super(NSObject))]
    #[name = "KyokaiStreamOutput"]
    #[ivars = Arc<Shared>]
    /// Receives a stream's pictures and its end.
    struct Output;

    unsafe impl NSObjectProtocol for Output {}

    unsafe impl SCStreamOutput for Output {
        #[unsafe(method(stream:didOutputSampleBuffer:ofType:))]
        fn stream_did_output(
            &self,
            _stream: &SCStream,
            sample: &CMSampleBuffer,
            kind: SCStreamOutputType,
        ) {
            if kind == SCStreamOutputType::Screen {
                self.ivars().take(sample);
            }
        }
    }

    unsafe impl SCStreamDelegate for Output {
        #[unsafe(method(stream:didStopWithError:))]
        fn stream_did_stop(&self, _stream: &SCStream, _error: &NSError) {
            self.ivars().stop();
        }
    }
);

impl Output {
    fn new(shared: Arc<Shared>) -> Retained<Self> {
        let this = Self::alloc().set_ivars(shared);
        // SAFETY: NSObject's init on a freshly allocated object.
        unsafe { msg_send![super(this), init] }
    }
}

/// A display through a ScreenCaptureKit stream, without Kyokai's windows
/// and without the pointer (the viewer draws it).
pub struct Capture {
    stream: Retained<SCStream>,
    _output: Retained<Output>,
    shared: Arc<Shared>,
    /// The last picture taken.
    seen: u64,
}

impl Grab for Capture {
    fn open(want: Display) -> Result<(Self, u32, u32), String> {
        let id = display_id(want).ok_or("the display is gone")?;
        let (pw, ph) = pixel_size(id, want);
        let (w, h) = fit(pw, ph, MAX_SIZE.0, MAX_SIZE.1);
        Ok((Capture::start(want, w, h)?, w, h))
    }

    fn frame(&mut self, timeout: Duration, take: &mut Take<'_>) -> Got {
        self.next(timeout, take)
    }
}

impl Capture {
    /// Starts a stream of `want` scaled to `w`×`h` pixels.
    /// Returns: an error without the Screen Recording permission.
    fn start(want: Display, w: u32, h: u32) -> Result<Capture, String> {
        let id = display_id(want).ok_or("the display is gone")?;
        let content = shareable()?;
        // SAFETY: plain reads of the snapshot.
        let display = unsafe { content.displays() }
            .iter()
            .find(|d| unsafe { d.displayID() } == id)
            .ok_or("the display cannot be captured")?;
        let own = std::process::id() as i32;
        // SAFETY: plain reads of the snapshot.
        let kyokai: Vec<Retained<SCRunningApplication>> = unsafe { content.applications() }
            .iter()
            .filter(|a| unsafe {
                a.processID() == own || a.bundleIdentifier().to_string() == BUNDLE_ID
            })
            .collect();
        // SAFETY: a filter of a display of the snapshot, without those
        // applications' windows.
        let filter = unsafe {
            SCContentFilter::initWithDisplay_excludingApplications_exceptingWindows(
                SCContentFilter::alloc(),
                &display,
                &NSArray::from_retained_slice(&kyokai),
                &NSArray::new(),
            )
        };
        // SAFETY: plain setters of a new configuration.
        let config = unsafe {
            let config = SCStreamConfiguration::new();
            config.setWidth(w as usize);
            config.setHeight(h as usize);
            config.setPixelFormat(kCVPixelFormatType_32BGRA);
            config.setMinimumFrameInterval(CMTime {
                value: 1,
                timescale: MAX_FPS as i32,
                flags: CMTimeFlags::Valid,
                epoch: 0,
            });
            config.setShowsCursor(false);
            config.setQueueDepth(3);
            config
        };
        let shared = Arc::new(Shared::default());
        let output = Output::new(shared.clone());
        // SAFETY: the stream keeps the delegate weakly; `Capture` holds it
        // for the stream's life.
        let stream = unsafe {
            SCStream::initWithFilter_configuration_delegate(
                SCStream::alloc(),
                &filter,
                &config,
                Some(ProtocolObject::from_ref(&*output)),
            )
        };
        let queue = DispatchQueue::new("com.yuanzhe.kyokai.capture", None);
        // SAFETY: the output lives as long as the stream (both in `Capture`);
        // the serial queue runs its callbacks.
        unsafe {
            stream.addStreamOutput_type_sampleHandlerQueue_error(
                ProtocolObject::from_ref(&*output),
                SCStreamOutputType::Screen,
                Some(&queue),
            )
        }
        .map_err(|e| e.localizedDescription().to_string())?;
        // SAFETY: the handler is a block of the expected signature.
        finished(|done| unsafe { stream.startCaptureWithCompletionHandler(Some(done)) })?;
        Ok(Capture {
            stream,
            _output: output,
            shared,
            seen: 0,
        })
    }

    /// Waits up to `timeout` for a picture newer than the last one taken.
    fn next(&mut self, timeout: Duration, take: &mut Take<'_>) -> Got {
        let seen = self.seen;
        let Ok(latest) = self.shared.latest.lock() else {
            return Got::Lost;
        };
        let Ok((l, _)) = self
            .shared
            .ready
            .wait_timeout_while(latest, timeout, |l| l.seq == seen && !l.stopped)
        else {
            return Got::Lost;
        };
        if l.stopped {
            return Got::Lost;
        }
        if l.seq == seen {
            return Got::Nothing;
        }
        self.seen = l.seq;
        take(&l.bgra, l.width, l.height, l.stride);
        Got::Picture
    }
}

impl Drop for Capture {
    fn drop(&mut self) {
        // SAFETY: stops the stream this capture started; nothing waits for
        // the answer.
        unsafe { self.stream.stopCaptureWithCompletionHandler(None) };
    }
}

/// Where a fraction of `area` lies in global coordinates (points).
fn point_in(area: Area, fx: f32, fy: f32) -> CGPoint {
    CGPoint {
        x: area.x as f64 + fx.clamp(0.0, 1.0) as f64 * (area.width.max(1) - 1) as f64,
        y: area.y as f64 + fy.clamp(0.0, 1.0) as f64 * (area.height.max(1) - 1) as f64,
    }
}

/// The main display as an input area.
fn main_area() -> Area {
    let d = displays()
        .into_iter()
        .find(|d| d.primary)
        .unwrap_or(Display {
            x: 0,
            y: 0,
            width: 1,
            height: 1,
            primary: true,
        });
    Area {
        x: d.x,
        y: d.y,
        width: d.width,
        height: d.height,
    }
}

/// The Mac key code of a key name (`input::vk`'s names).
/// Returns: None for a key a Mac keyboard lacks (F21–F24).
pub fn keycode(name: &str) -> Option<CGKeyCode> {
    const LETTERS: [CGKeyCode; 26] = [
        0x00, 0x0B, 0x08, 0x02, 0x0E, 0x03, 0x05, 0x04, 0x22, 0x26, 0x28, 0x25, 0x2E, 0x2D, 0x1F,
        0x23, 0x0C, 0x0F, 0x01, 0x11, 0x20, 0x09, 0x0D, 0x07, 0x10, 0x06,
    ];
    const DIGITS: [CGKeyCode; 10] = [0x1D, 0x12, 0x13, 0x14, 0x15, 0x17, 0x16, 0x1A, 0x1C, 0x19];
    const FKEYS: [CGKeyCode; 20] = [
        0x7A, 0x78, 0x63, 0x76, 0x60, 0x61, 0x62, 0x64, 0x65, 0x6D, 0x67, 0x6F, 0x69, 0x6B, 0x71,
        0x6A, 0x40, 0x4F, 0x50, 0x5A,
    ];
    let b = name.as_bytes();
    if b.len() == 1 && b[0].is_ascii_lowercase() {
        return Some(LETTERS[(b[0] - b'a') as usize]);
    }
    if b.len() == 1 && b[0].is_ascii_digit() {
        return Some(DIGITS[(b[0] - b'0') as usize]);
    }
    if let Some(n) = name.strip_prefix('f').and_then(|n| n.parse::<usize>().ok()) {
        return (1..=FKEYS.len()).contains(&n).then(|| FKEYS[n - 1]);
    }
    Some(match name {
        "enter" => 0x24,
        "escape" => ESCAPE,
        "backspace" => 0x33,
        "tab" => 0x30,
        "space" => 0x31,
        "delete" => 0x75,
        "insert" => 0x72,
        "home" => 0x73,
        "end" => 0x77,
        "page-up" => 0x74,
        "page-down" => 0x79,
        "left" => 0x7B,
        "up" => 0x7E,
        "right" => 0x7C,
        "down" => 0x7D,
        "minus" => 0x1B,
        "equal" => 0x18,
        "comma" => 0x2B,
        "period" => 0x2F,
        "slash" => 0x2C,
        "semicolon" => 0x29,
        "quote" => 0x27,
        "bracket-left" => 0x21,
        "bracket-right" => 0x1E,
        "backslash" => 0x2A,
        "backquote" => 0x32,
        "context-menu" => 0x6E,
        "caps-lock" => CAPS_LOCK,
        "print-screen" => 0x69,
        "shift" => 0x38,
        "ctrl" => 0x3B,
        "alt" => 0x3A,
        "meta" => 0x37,
        _ => return None,
    })
}

/// The Mac key code of a PC scan code (set 1, `0xE0..` extended), as the
/// viewer's Map mode sends them.
/// Returns: None for a key a Mac keyboard lacks (NumLock, ScrollLock).
pub fn scan_keycode(code: u32) -> Option<CGKeyCode> {
    const DIGITS: [CGKeyCode; 10] = [0x12, 0x13, 0x14, 0x15, 0x17, 0x16, 0x1A, 0x1C, 0x19, 0x1D];
    const TOP: [CGKeyCode; 10] = [0x0C, 0x0D, 0x0E, 0x0F, 0x11, 0x10, 0x20, 0x22, 0x1F, 0x23];
    const HOME: [CGKeyCode; 9] = [0x00, 0x01, 0x02, 0x03, 0x05, 0x04, 0x26, 0x28, 0x25];
    const BOTTOM: [CGKeyCode; 7] = [0x06, 0x07, 0x08, 0x09, 0x0B, 0x2D, 0x2E];
    const FKEYS: [CGKeyCode; 10] = [0x7A, 0x78, 0x63, 0x76, 0x60, 0x61, 0x62, 0x64, 0x65, 0x6D];
    const KEYPAD: [CGKeyCode; 13] = [
        0x59, 0x5B, 0x5C, 0x4E, 0x56, 0x57, 0x58, 0x45, 0x53, 0x54, 0x55, 0x52, 0x41,
    ];
    let i = code as usize;
    Some(match code {
        0x01 => ESCAPE,
        0x02..=0x0B => DIGITS[i - 0x02],
        0x0C => 0x1B,
        0x0D => 0x18,
        0x0E => 0x33,
        0x0F => 0x30,
        0x10..=0x19 => TOP[i - 0x10],
        0x1A => 0x21,
        0x1B => 0x1E,
        0x1C => 0x24,
        0x1D => 0x3B,
        0x1E..=0x26 => HOME[i - 0x1E],
        0x27 => 0x29,
        0x28 => 0x27,
        0x29 => 0x32,
        0x2A => 0x38,
        0x2B => 0x2A,
        0x2C..=0x32 => BOTTOM[i - 0x2C],
        0x33 => 0x2B,
        0x34 => 0x2F,
        0x35 => 0x2C,
        0x36 => 0x3C,
        0x37 => 0x43,
        0x38 => 0x3A,
        0x39 => 0x31,
        0x3A => CAPS_LOCK,
        0x3B..=0x44 => FKEYS[i - 0x3B],
        0x47..=0x53 => KEYPAD[i - 0x47],
        0x56 => 0x0A,
        0x57 => 0x67,
        0x58 => 0x6F,
        0xE01C => 0x4C,
        0xE01D => 0x3E,
        0xE035 => 0x4B,
        0xE037 => 0x69,
        0xE038 => 0x3D,
        0xE047 => 0x73,
        0xE048 => 0x7E,
        0xE049 => 0x74,
        0xE04B => 0x7B,
        0xE04D => 0x7C,
        0xE04F => 0x77,
        0xE050 => 0x7D,
        0xE051 => 0x79,
        0xE052 => 0x72,
        0xE053 => 0x75,
        0xE05B => 0x37,
        0xE05C => 0x36,
        0xE05D => 0x6E,
        0xE11D => 0x71,
        _ => return None,
    })
}

/// Caps Lock's key code.
const CAPS_LOCK: CGKeyCode = 0x39;
/// Escape's key code (the emergency stop is Ctrl+Option+Shift+Esc).
const ESCAPE: CGKeyCode = 0x35;
/// A press within this long of the last on the same button and place
/// continues its click count (a double or triple click).
const CLICK_INTERVAL: Duration = Duration::from_millis(500);
/// How far (points) a further click may land from the first.
const CLICK_SLOP: f64 = 4.0;
/// Most UTF-16 units one keyboard event carries.
const TEXT_CHUNK: usize = 20;
/// The mouse buttons by the protocol's number (left, right, middle).
const BUTTONS: [CGMouseButton; 3] = [
    CGMouseButton::Left,
    CGMouseButton::Right,
    CGMouseButton::Center,
];

/// The modifier flag a modifier key sets.
fn modifier_flag(code: CGKeyCode) -> Option<CGEventFlags> {
    Some(match code {
        0x38 | 0x3C => CGEventFlags::MaskShift,
        0x3B | 0x3E => CGEventFlags::MaskControl,
        0x3A | 0x3D => CGEventFlags::MaskAlternate,
        0x37 | 0x36 => CGEventFlags::MaskCommand,
        _ => return None,
    })
}

/// Whether a modifier set asks for one modifier.
type Wanted = fn(&Mods) -> bool;

/// The modifiers of a key event: their key codes (Control for ctrl,
/// Option for alt, Command for meta) and whether each is wanted.
const MODIFIERS: [(CGKeyCode, Wanted); 4] = [
    (0x38, |m| m.shift),
    (0x3B, |m| m.ctrl),
    (0x3A, |m| m.alt),
    (0x37, |m| m.meta),
];

/// Tags an event as Kyokai's and posts it at the HID level.
fn post(event: Option<CFRetained<CGEvent>>) {
    let Some(event) = event else {
        return;
    };
    CGEvent::set_integer_value_field(
        Some(&event),
        CGEventField::EventSourceUserData,
        INJECTED_TAG as i64,
    );
    CGEvent::post(CGEventTapLocation::HIDEventTap, Some(&event));
}

/// The owner of the frontmost on-screen window at `at` (points), or of the
/// frontmost normal window (the focused application's) when None.
fn window_owner(at: Option<CGPoint>) -> Option<i32> {
    let list = CGWindowListCopyWindowInfo(
        CGWindowListOption::OptionOnScreenOnly | CGWindowListOption::ExcludeDesktopElements,
        0,
    )?;
    // SAFETY: the window list holds dictionaries with string keys.
    let windows = unsafe { list.cast_unchecked::<CFDictionary<CFString, CFType>>() };
    // SAFETY: immutable constants of the framework.
    let (pid_key, layer_key, bounds_key) =
        unsafe { (kCGWindowOwnerPID, kCGWindowLayer, kCGWindowBounds) };
    for w in windows.iter() {
        let number = |key: &CFString| {
            w.get(key)
                .and_then(|v| v.downcast::<CFNumber>().ok())
                .and_then(|n| n.as_i64())
        };
        let Some(pid) = number(pid_key) else {
            continue;
        };
        let hit = match at {
            None => number(layer_key) == Some(0),
            Some(p) => {
                let mut r = CGRect::default();
                w.get(bounds_key)
                    .and_then(|v| v.downcast::<CFDictionary>().ok())
                    // SAFETY: a bounds dictionary and a rectangle to fill.
                    .is_some_and(|b| unsafe {
                        CGRectMakeWithDictionaryRepresentation(Some(&b), &mut r)
                    })
                    && p.x >= r.origin.x
                    && p.y >= r.origin.y
                    && p.x < r.origin.x + r.size.width
                    && p.y < r.origin.y + r.size.height
            }
        };
        if hit {
            return Some(pid as i32);
        }
    }
    None
}

/// Whether a process is Kyokai's: this engine or the app that started it.
fn kyokai_pid(pid: i32) -> bool {
    let pid = pid as u32;
    pid == std::process::id() || pid == std::os::unix::process::parent_id()
}

/// A mouse press: its button, place (points), time and click count.
#[derive(Clone, Copy)]
struct Press {
    button: u8,
    at: (f64, f64),
    when: Instant,
    clicks: i64,
}

/// The host's input on macOS: events posted through Core Graphics, tagged
/// as Kyokai's; nothing reaches Kyokai's own windows, and whatever is held
/// is released at the end. Needs the Accessibility permission.
#[derive(Default)]
pub struct Injector {
    area: Option<Area>,
    keys: BTreeSet<CGKeyCode>,
    buttons: BTreeSet<u8>,
    /// Where the pointer was last put (points).
    at: (f64, f64),
    /// The last press, for double and triple clicks.
    press: Option<Press>,
    /// Wheel movement not sent yet (less than a line).
    scroll: (f32, f32),
}

impl Injector {
    /// Sets the display pointer positions refer to (None: the main one).
    pub fn set_area(&mut self, area: Option<Area>) {
        self.area = area;
    }

    /// The flags the held modifier keys set.
    fn flags(&self) -> CGEventFlags {
        self.keys
            .iter()
            .filter_map(|k| modifier_flag(*k))
            .fold(CGEventFlags::empty(), |a, f| a | f)
    }

    /// Posts a key going down or up, as a modifier change for modifiers.
    fn key(&mut self, code: CGKeyCode, down: bool) {
        if down {
            self.keys.insert(code);
        } else {
            self.keys.remove(&code);
        }
        let event = CGEvent::new_keyboard_event(None, code, down);
        if let Some(e) = &event {
            if modifier_flag(code).is_some() {
                CGEvent::set_type(Some(e), CGEventType::FlagsChanged);
            }
            CGEvent::set_flags(Some(e), self.flags());
        }
        post(event);
    }

    /// Presses or releases modifiers so they match `mods`.
    fn sync_mods(&mut self, mods: &Mods) {
        for (code, wanted) in MODIFIERS {
            let held = self.keys.contains(&code);
            if wanted(mods) != held {
                self.key(code, !held);
            }
        }
    }

    /// Posts a mouse event at a place.
    fn mouse(&self, kind: CGEventType, button: CGMouseButton, at: (f64, f64), clicks: i64) {
        let event = CGEvent::new_mouse_event(None, kind, CGPoint { x: at.0, y: at.1 }, button);
        if let Some(e) = &event {
            CGEvent::set_integer_value_field(Some(e), CGEventField::MouseEventClickState, clicks);
            CGEvent::set_flags(Some(e), self.flags());
        }
        post(event);
    }

    /// Whether the window `guard` names is one of Kyokai's.
    fn blocked(&self, guard: Guard) -> bool {
        let owner = match guard {
            Guard::None => return false,
            Guard::At(x, y) => {
                window_owner(Some(point_in(self.area.unwrap_or_else(main_area), x, y)))
            }
            Guard::Pointer => {
                let Some(event) = CGEvent::new(None) else {
                    return true;
                };
                window_owner(Some(CGEvent::location(Some(&event))))
            }
            Guard::Focus => window_owner(None),
        };
        owner.is_some_and(kyokai_pid)
    }

    /// Injects an event, unless it would reach a Kyokai window.
    /// Returns: whether it was injected.
    pub fn inject(&mut self, e: &Event) -> bool {
        if !e.valid() || self.blocked(e.guard()) {
            return false;
        }
        match e {
            Event::Pointer {
                action,
                button,
                x,
                y,
            } => {
                let p = point_in(self.area.unwrap_or_else(main_area), *x, *y);
                let at = (p.x, p.y);
                let b = *button as usize;
                match action {
                    PointerAction::Move => {
                        let (kind, held) = match self.buttons.first() {
                            Some(0) => (CGEventType::LeftMouseDragged, BUTTONS[0]),
                            Some(1) => (CGEventType::RightMouseDragged, BUTTONS[1]),
                            Some(_) => (CGEventType::OtherMouseDragged, BUTTONS[2]),
                            None => (CGEventType::MouseMoved, BUTTONS[0]),
                        };
                        self.mouse(kind, held, at, 0);
                    }
                    PointerAction::Down => {
                        let clicks = match self.press {
                            Some(p)
                                if p.button == *button
                                    && p.when.elapsed() < CLICK_INTERVAL
                                    && (p.at.0 - at.0).abs() <= CLICK_SLOP
                                    && (p.at.1 - at.1).abs() <= CLICK_SLOP =>
                            {
                                p.clicks + 1
                            }
                            _ => 1,
                        };
                        self.press = Some(Press {
                            button: *button,
                            at,
                            when: Instant::now(),
                            clicks,
                        });
                        let kind = [
                            CGEventType::LeftMouseDown,
                            CGEventType::RightMouseDown,
                            CGEventType::OtherMouseDown,
                        ][b];
                        self.mouse(kind, BUTTONS[b], at, clicks);
                        self.buttons.insert(*button);
                    }
                    PointerAction::Up => {
                        let clicks = self
                            .press
                            .filter(|p| p.button == *button)
                            .map_or(1, |p| p.clicks);
                        let kind = [
                            CGEventType::LeftMouseUp,
                            CGEventType::RightMouseUp,
                            CGEventType::OtherMouseUp,
                        ][b];
                        self.mouse(kind, BUTTONS[b], at, clicks);
                        self.buttons.remove(button);
                    }
                }
                self.at = at;
            }
            Event::Scroll { dx, dy } => {
                // Wheel notches as lines; fractions add up to a line.
                self.scroll.0 += dx;
                self.scroll.1 += dy;
                let (lx, ly) = (self.scroll.0.trunc(), self.scroll.1.trunc());
                self.scroll.0 -= lx;
                self.scroll.1 -= ly;
                if lx != 0.0 || ly != 0.0 {
                    post(CGEvent::new_scroll_wheel_event2(
                        None,
                        CGScrollEventUnit::Line,
                        2,
                        -ly as i32,
                        -lx as i32,
                        0,
                    ));
                }
            }
            Event::Key { key, down, mods } => {
                let Some(code) = keycode(key) else {
                    return false;
                };
                if *down {
                    self.sync_mods(mods);
                }
                self.key(code, *down);
            }
            Event::Text { text } => {
                let units: Vec<u16> = text.encode_utf16().collect();
                for chunk in units.chunks(TEXT_CHUNK) {
                    for down in [true, false] {
                        let event = CGEvent::new_keyboard_event(None, 0, down);
                        if let Some(e) = &event {
                            // SAFETY: `chunk` holds `chunk.len()` UTF-16 units.
                            unsafe {
                                CGEvent::keyboard_set_unicode_string(
                                    Some(e),
                                    chunk.len() as _,
                                    chunk.as_ptr(),
                                )
                            };
                        }
                        post(event);
                    }
                }
            }
            Event::Scan {
                code, down, caps, ..
            } => {
                let Some(key) = scan_keycode(*code) else {
                    return false;
                };
                // The host's Caps Lock follows the controller's before a key
                // it changes (a Mac has no NumLock).
                if *down
                    && let Some((0x3A, on)) = lock_for(*code, *caps, false)
                    && caps_lock() != on
                {
                    self.key(CAPS_LOCK, true);
                    self.key(CAPS_LOCK, false);
                }
                self.key(key, *down);
            }
        }
        true
    }

    /// Releases every key and button this session holds.
    pub fn release_all(&mut self) {
        let keys: Vec<CGKeyCode> = self.keys.iter().rev().copied().collect();
        for k in keys {
            self.key(k, false);
        }
        let buttons: Vec<u8> = std::mem::take(&mut self.buttons).into_iter().collect();
        for b in buttons {
            let kind = [
                CGEventType::LeftMouseUp,
                CGEventType::RightMouseUp,
                CGEventType::OtherMouseUp,
            ][b as usize];
            self.mouse(kind, BUTTONS[b as usize], self.at, 1);
        }
    }
}

impl Drop for Injector {
    fn drop(&mut self) {
        self.release_all();
    }
}

/// Whether Caps Lock is on.
fn caps_lock() -> bool {
    CGEventSource::flags_state(CGEventSourceStateID::HIDSystemState)
        .contains(CGEventFlags::MaskAlphaShift)
}

/// What the event tap and the AI task share.
#[derive(Default)]
struct Watched {
    press: Mutex<Option<Instant>>,
    moved: Mutex<Option<Instant>>,
    stop: AtomicBool,
    /// The tap, to switch it on again when macOS turned it off.
    tap: AtomicPtr<CFMachPort>,
}

/// The run loop of the watch's thread, stopped from the thread that drops
/// the watch.
struct Loop(CFRetained<CFRunLoop>);

// SAFETY: CFRunLoopStop may be called from any thread.
unsafe impl Send for Loop {}

/// Watches the host user's own keyboard and mouse through an event tap
/// that only listens (Kyokai's tagged events are skipped), and the
/// emergency-stop shortcut Ctrl+Option+Shift+Esc.
pub struct LocalWatch {
    watched: Arc<Watched>,
    run_loop: Loop,
}

/// The event tap's callback: notes local input and the stop shortcut.
unsafe extern "C-unwind" fn tapped(
    _proxy: CGEventTapProxy,
    kind: CGEventType,
    event: NonNull<CGEvent>,
    info: *mut c_void,
) -> *mut CGEvent {
    // SAFETY: `info` is the watch's `Watched`, kept alive by the tap's
    // thread while its run loop runs.
    let watched = unsafe { &*(info as *const Watched) };
    // SAFETY: the event is valid for the callback.
    let e = unsafe { event.as_ref() };
    if kind == CGEventType::TapDisabledByTimeout || kind == CGEventType::TapDisabledByUserInput {
        // SAFETY: the tap lives as long as its thread's run loop runs.
        if let Some(tap) = unsafe { watched.tap.load(Ordering::Acquire).as_ref() } {
            CGEvent::tap_enable(tap, true);
        }
        return event.as_ptr();
    }
    if CGEvent::integer_value_field(Some(e), CGEventField::EventSourceUserData)
        == INJECTED_TAG as i64
    {
        return event.as_ptr();
    }
    let now = Some(Instant::now());
    let moved = [
        CGEventType::MouseMoved,
        CGEventType::LeftMouseDragged,
        CGEventType::RightMouseDragged,
        CGEventType::OtherMouseDragged,
    ]
    .contains(&kind);
    let slot = if moved {
        &watched.moved
    } else {
        &watched.press
    };
    if let Ok(mut t) = slot.lock() {
        *t = now;
    }
    let stop = CGEventFlags::MaskControl | CGEventFlags::MaskAlternate | CGEventFlags::MaskShift;
    if kind == CGEventType::KeyDown
        && CGEvent::integer_value_field(Some(e), CGEventField::KeyboardEventKeycode)
            == ESCAPE as i64
        && CGEvent::flags(Some(e)).contains(stop)
    {
        watched.stop.store(true, Ordering::Relaxed);
    }
    event.as_ptr()
}

impl LocalWatch {
    /// Starts the tap on a thread of its own.
    /// Returns: an error without the Accessibility permission.
    pub fn start() -> Result<LocalWatch, String> {
        let watched = Arc::new(Watched::default());
        let (tx, rx) = sync_channel(1);
        let shared = watched.clone();
        std::thread::spawn(move || {
            let mask: CGEventMask = [
                CGEventType::KeyDown,
                CGEventType::FlagsChanged,
                CGEventType::LeftMouseDown,
                CGEventType::RightMouseDown,
                CGEventType::OtherMouseDown,
                CGEventType::MouseMoved,
                CGEventType::LeftMouseDragged,
                CGEventType::RightMouseDragged,
                CGEventType::OtherMouseDragged,
                CGEventType::ScrollWheel,
            ]
            .iter()
            .fold(0, |m, t| m | 1 << t.0);
            let info = Arc::as_ptr(&shared) as *mut c_void;
            // SAFETY: `tapped` matches the callback type; `info` stays valid
            // while this thread holds `shared`.
            let tap = unsafe {
                CGEvent::tap_create(
                    CGEventTapLocation::SessionEventTap,
                    CGEventTapPlacement::HeadInsertEventTap,
                    CGEventTapOptions::ListenOnly,
                    mask,
                    Some(tapped),
                    info,
                )
            };
            let Some(tap) = tap else {
                let _ = tx.send(Err(
                    "watching local input needs the Accessibility permission".to_string(),
                ));
                return;
            };
            shared
                .tap
                .store(CFRetained::as_ptr(&tap).as_ptr(), Ordering::Release);
            let (Some(source), Some(run_loop)) = (
                CFMachPort::new_run_loop_source(None, Some(&tap), 0),
                CFRunLoop::current(),
            ) else {
                let _ = tx.send(Err("no run loop for the input watch".to_string()));
                return;
            };
            // SAFETY: an immutable constant of the framework.
            run_loop.add_source(Some(&source), unsafe { kCFRunLoopCommonModes });
            CGEvent::tap_enable(&tap, true);
            let _ = tx.send(Ok(Loop(run_loop)));
            CFRunLoop::run();
            shared.tap.store(std::ptr::null_mut(), Ordering::Release);
        });
        let run_loop = rx
            .recv()
            .map_err(|_| "the input watch did not start".to_string())??;
        Ok(LocalWatch { watched, run_loop })
    }

    /// When the host user last pressed something and moved the pointer.
    pub fn input(&self) -> LocalInput {
        LocalInput {
            press: self.watched.press.lock().ok().and_then(|p| *p),
            moved: self.watched.moved.lock().ok().and_then(|m| *m),
        }
    }

    /// Whether the stop shortcut was pressed since the last call.
    pub fn stop_requested(&self) -> bool {
        self.watched.stop.swap(false, Ordering::Relaxed)
    }
}

impl Drop for LocalWatch {
    fn drop(&mut self) {
        self.run_loop.0.stop();
    }
}

/// The real desktop for AI tasks: the main display through
/// ScreenCaptureKit at full resolution, Core Graphics input, and a watch on
/// local input.
pub struct Screen {
    injector: Injector,
    watch: LocalWatch,
}

impl Screen {
    /// Opens the desktop and starts watching for local input.
    pub fn open() -> Result<Screen, String> {
        Ok(Screen {
            injector: Injector::default(),
            watch: LocalWatch::start()?,
        })
    }
}

/// The main display: its id and place.
fn main_display() -> Result<(CGDirectDisplayID, Display), String> {
    let id = active()
        .into_iter()
        .find(|&id| CGDisplayIsMain(id))
        .ok_or("no main display")?;
    Ok((id, bounds(id)))
}

impl Desktop for Screen {
    fn size(&mut self) -> Result<(u32, u32), String> {
        let (id, d) = main_display()?;
        Ok(pixel_size(id, d))
    }

    fn capture(&mut self) -> Result<Frame, String> {
        let (id, d) = main_display()?;
        let (w, h) = pixel_size(id, d);
        let mut cap = Capture::start(d, w, h)?;
        let mut frame = None;
        // The stream's first picture comes within a few frames.
        cap.next(WAIT, &mut |bgra, width, height, stride| {
            let mut tight = Vec::with_capacity(width * height * 4);
            for row in bgra.chunks(stride).take(height) {
                tight.extend_from_slice(&row[..width * 4]);
            }
            frame = Some(Frame {
                bgra: tight,
                width: width as u32,
                height: height as u32,
            });
        });
        frame.ok_or_else(|| "the screen gave no picture".into())
    }

    fn inject(&mut self, e: &Event) -> bool {
        self.injector.inject(e)
    }

    fn release(&mut self) {
        self.injector.release_all();
    }

    fn cursor(&mut self) -> Option<(f32, f32)> {
        let (x, y, _) = pointer()?;
        Some((x.clamp(0.0, 1.0), y.clamp(0.0, 1.0)))
    }

    fn local_input(&mut self) -> LocalInput {
        self.watch.input()
    }

    fn stop_requested(&mut self) -> bool {
        self.watch.stop_requested()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn named_keys_and_scan_codes_reach_the_same_mac_keys() {
        let pairs = [
            ("a", 0x1E),
            ("z", 0x2C),
            ("q", 0x10),
            ("m", 0x32),
            ("1", 0x02),
            ("0", 0x0B),
            ("escape", 0x01),
            ("enter", 0x1C),
            ("space", 0x39),
            ("tab", 0x0F),
            ("backspace", 0x0E),
            ("minus", 0x0C),
            ("equal", 0x0D),
            ("bracket-left", 0x1A),
            ("bracket-right", 0x1B),
            ("backslash", 0x2B),
            ("semicolon", 0x27),
            ("quote", 0x28),
            ("backquote", 0x29),
            ("comma", 0x33),
            ("period", 0x34),
            ("slash", 0x35),
            ("f1", 0x3B),
            ("f10", 0x44),
            ("f11", 0x57),
            ("f12", 0x58),
            ("left", 0xE04B),
            ("right", 0xE04D),
            ("up", 0xE048),
            ("down", 0xE050),
            ("home", 0xE047),
            ("end", 0xE04F),
            ("page-up", 0xE049),
            ("page-down", 0xE051),
            ("delete", 0xE053),
            ("insert", 0xE052),
            ("shift", 0x2A),
            ("ctrl", 0x1D),
            ("alt", 0x38),
            ("meta", 0xE05B),
            ("caps-lock", 0x3A),
            ("context-menu", 0xE05D),
            ("print-screen", 0xE037),
        ];
        for (name, scan) in pairs {
            assert!(keycode(name).is_some(), "{name}");
            assert_eq!(keycode(name), scan_keycode(scan), "{name}");
        }
        assert_eq!(keycode("f21"), None, "a Mac has no F21");
        // Distinct keys stay distinct.
        let mut seen = std::collections::HashMap::new();
        for code in (0x01..=0xFF).chain(0xE001..=0xE0FF).chain([0xE11D]) {
            if let Some(k) = scan_keycode(code)
                && let Some(other) = seen.insert(k, code)
            {
                panic!("{other:#x} and {code:#x} both reach {k:#x}");
            }
        }
    }
}
