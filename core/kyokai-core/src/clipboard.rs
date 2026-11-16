//! Clipboard sharing in a session (doc/en-us/computer-use.md,
//! "Clipboard"): while a session may share the clipboard, text or an image
//! copied on either side is offered to the other, and with `files` copied
//! files (crate::files moves them). What Kyokai itself put there (the
//! Device ID, a password) is never offered: on Windows its owner window is
//! Kyokai's, and on every system its text was recorded with [`mine`].

use base64::Engine;
use std::collections::VecDeque;
use std::hash::{DefaultHasher, Hash, Hasher};
use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Duration;
use tokio::sync::mpsc;

/// Most text one side offers.
pub const MAX_TEXT: usize = 1 << 20;
/// Most pixels of an image one side offers (a 4K screenshot).
pub const MAX_PIXELS: u64 = 3840 * 2160;
/// Most bytes of an image's PNG, so it fits one message in base64.
pub const MAX_PNG: usize = 3 << 20;
/// Room kept below `MAX_PNG` so the base64 PNG and its JSON envelope fit
/// within `session::MAX_MESSAGE` (4 MiB).
const MAX_PNG_WIRE: usize = MAX_PNG - 64;
/// How often the clipboard is looked at for changes (it announces none).
const POLL: Duration = Duration::from_millis(500);
/// Texts of Kyokai's own that are remembered (see [`mine`]).
const MINE: usize = 8;

/// SHA-256 digests of the latest texts Kyokai put on the clipboard itself,
/// oldest first.
static MINE_DIGESTS: Mutex<VecDeque<[u8; 32]>> = Mutex::new(VecDeque::new());

/// Records a text Kyokai is about to put on the clipboard (a password, the
/// Device ID), so that no session of this process ever offers it.
/// Inputs: `digest` — SHA-256 of the text's UTF-8 bytes.
/// Side effects: remembers the digest, in place of the oldest of the last 8.
/// Notes: call it before the text is written, so the clipboard watch never
/// sees it unrecorded.
pub fn mine(digest: [u8; 32]) {
    let mut d = MINE_DIGESTS.lock().expect("clipboard digests");
    if d.contains(&digest) {
        return;
    }
    if d.len() == MINE {
        d.pop_front();
    }
    d.push_back(digest);
}

/// Whether an item is a text recorded with [`mine`].
fn is_mine(item: &Item) -> bool {
    let Item::Text(t) = item else {
        return false;
    };
    let digest: [u8; 32] = <sha2::Sha256 as sha2::Digest>::digest(t.as_bytes()).into();
    MINE_DIGESTS
        .lock()
        .expect("clipboard digests")
        .contains(&digest)
}

/// An image on a clipboard: RGBA pixels, row after row.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Image {
    pub width: u32,
    pub height: u32,
    pub rgba: Vec<u8>,
}

/// What one side offers the other.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Item {
    Text(String),
    Image(Image),
    /// An image as base64 PNG, as the session agent hands this computer's
    /// clipboard to the helper: forwarded, never decoded there.
    Png(String),
    /// Copied files and folders (on Windows `CF_HDROP`).
    Files(Vec<PathBuf>),
}

/// A clipboard: the system's, or a test double.
pub trait Clipboard: Send {
    /// A number that changes whenever the clipboard does, where the system
    /// keeps one; reading is skipped while it stays the same.
    fn serial(&mut self) -> Option<u32>;
    /// What the clipboard holds (files, then an image, then text), unless it
    /// is empty, something else, or Kyokai put it there.
    fn item(&mut self) -> Option<Item>;
    /// Puts an item on the clipboard. Returns: whether it worked.
    fn set(&mut self, item: &Item) -> bool;
}

/// How a session opens its side's clipboard: the system's, or a test
/// double.
pub type Open = Arc<dyn Fn() -> Option<Box<dyn Clipboard>> + Send + Sync>;

/// Opens this computer's clipboard.
pub fn system() -> Option<Box<dyn Clipboard>> {
    if let Some(link) = crate::agent::link() {
        return Some(link.clipboard());
    }
    #[cfg(target_os = "android")]
    return Some(Box::new(crate::android::Board));
    #[cfg(target_os = "linux")]
    return linux_system();
    #[cfg(not(any(target_os = "android", target_os = "linux")))]
    {
        let c = arboard::Clipboard::new().ok()?;
        Some(Box::new(System(c)))
    }
}

/// Opens the clipboard on Linux: Wayland's data-control protocol where the
/// compositor has it (KDE, wlroots), else X11 (also GNOME Wayland through
/// Xwayland).
#[cfg(target_os = "linux")]
fn linux_system() -> Option<Box<dyn Clipboard>> {
    use arboard::{GetExtLinux as _, LinuxClipboardKind};
    if std::env::var_os("WAYLAND_DISPLAY").is_some()
        && let Ok(mut c) = arboard::Clipboard::new()
        && matches!(
            c.get().clipboard(LinuxClipboardKind::Secondary).text(),
            Err(arboard::Error::ClipboardNotSupported)
        )
    {
        return Some(Box::new(System(c)));
    }
    x11::Board::new().map(|b| Box::new(b) as Box<dyn Clipboard>)
}

#[cfg(not(target_os = "android"))]
struct System(arboard::Clipboard);

#[cfg(not(target_os = "android"))]
impl System {
    /// Reads an image from the system clipboard: through `arboard`, with a
    /// platform fallback where `arboard` misses valid formats (`CF_DIBV5`
    /// and `CF_DIB` on Windows when `image` 0.25.10 shifts the pixel offset
    /// by 12 bytes; `public.png` on macOS when `public.tiff` is absent).
    fn image(&mut self) -> Option<Image> {
        if let Ok(img) = self.0.get_image()
            && let (Ok(width), Ok(height)) = (u32::try_from(img.width), u32::try_from(img.height))
        {
            let image = Image {
                width,
                height,
                rgba: img.bytes.into_owned(),
            };
            if fits(&Item::Image(image.clone())) {
                return Some(image);
            }
        }
        #[cfg(windows)]
        if let Some(img) = win_dib_image() {
            return Some(img);
        }
        #[cfg(target_os = "macos")]
        if let Some(img) = mac_png_image() {
            return Some(img);
        }
        None
    }
}

#[cfg(not(target_os = "android"))]
impl Clipboard for System {
    fn serial(&mut self) -> Option<u32> {
        #[cfg(windows)]
        {
            // SAFETY: a plain query.
            Some(unsafe { windows::Win32::System::DataExchange::GetClipboardSequenceNumber() })
        }
        #[cfg(target_os = "macos")]
        {
            mac_serial()
        }
        #[cfg(not(any(windows, target_os = "macos")))]
        {
            None
        }
    }

    fn item(&mut self) -> Option<Item> {
        if owned_by_kyokai() {
            return None;
        }
        if let Ok(files) = self.0.get().file_list()
            && !files.is_empty()
        {
            return Some(Item::Files(files));
        }
        if let Some(img) = self.image() {
            return Some(Item::Image(img));
        }
        if let Ok(text) = self.0.get_text()
            && !text.is_empty()
        {
            return Some(Item::Text(text));
        }
        None
    }

    fn set(&mut self, item: &Item) -> bool {
        match item {
            Item::Text(t) => self.0.set_text(t).is_ok(),
            Item::Png(_) => false,
            Item::Files(f) => self.0.set().file_list(f).is_ok(),
            Item::Image(i) => self
                .0
                .set_image(arboard::ImageData {
                    width: i.width as usize,
                    height: i.height as usize,
                    bytes: std::borrow::Cow::Borrowed(&i.rgba),
                })
                .is_ok(),
        }
    }
}

/// Whether one of Kyokai's processes put the clipboard's contents there
/// (its owner window belongs to the GUI or the engine).
#[cfg(windows)]
fn owned_by_kyokai() -> bool {
    use windows::Win32::System::DataExchange::GetClipboardOwner;
    use windows::Win32::UI::WindowsAndMessaging::GetWindowThreadProcessId;
    // SAFETY: plain queries; the owner window is only read.
    unsafe {
        let Ok(owner) = GetClipboardOwner() else {
            return false;
        };
        let mut pid = 0u32;
        GetWindowThreadProcessId(owner, Some(&mut pid));
        pid != 0 && crate::input::kyokai_process(pid)
    }
}

#[cfg(not(windows))]
fn owned_by_kyokai() -> bool {
    false
}

/// Reads `CF_DIBV5` or `CF_DIB` from the Windows clipboard when `arboard`'s
/// own reader failed (see [`dib_to_image`]).
#[cfg(windows)]
fn win_dib_image() -> Option<Image> {
    use windows::Win32::Foundation::HGLOBAL;
    use windows::Win32::System::DataExchange::{
        CloseClipboard, GetClipboardData, IsClipboardFormatAvailable, OpenClipboard,
    };
    use windows::Win32::System::Memory::{GlobalLock, GlobalSize, GlobalUnlock};

    const CF_DIB: u32 = 8;
    const CF_DIBV5: u32 = 17;

    // SAFETY: opens the clipboard on this thread, copies the global memory
    // block while locked, and closes the clipboard before returning.
    unsafe {
        OpenClipboard(None).ok()?;
        struct Guard;
        impl Drop for Guard {
            fn drop(&mut self) {
                // SAFETY: paired with the successful OpenClipboard above.
                let _ = unsafe { CloseClipboard() };
            }
        }
        let _guard = Guard;
        for fmt in [CF_DIBV5, CF_DIB] {
            if IsClipboardFormatAvailable(fmt).is_err() {
                continue;
            }
            let Ok(handle) = GetClipboardData(fmt) else {
                continue;
            };
            if handle.is_invalid() {
                continue;
            }
            let hglobal = HGLOBAL(handle.0);
            let len = GlobalSize(hglobal);
            if len == 0 || len > (MAX_PIXELS as usize) * 4 + 4096 {
                continue;
            }
            let ptr = GlobalLock(hglobal);
            if ptr.is_null() {
                continue;
            }
            let bytes = std::slice::from_raw_parts(ptr.cast::<u8>(), len).to_vec();
            let _ = GlobalUnlock(hglobal);
            if let Some(img) = dib_to_image(&bytes) {
                return Some(img);
            }
        }
        None
    }
}

/// The pasteboard's `changeCount` on macOS, so the clipboard is read only
/// when it changed.
#[cfg(target_os = "macos")]
fn mac_serial() -> Option<u32> {
    use objc2::msg_send;
    use objc2::rc::{Retained, autoreleasepool};
    use objc2::runtime::AnyClass;
    use objc2_foundation::NSObject;
    let class = AnyClass::get(c"NSPasteboard")?;
    autoreleasepool(|_| {
        // SAFETY: `generalPasteboard` and `changeCount` are standard
        // NSPasteboard selectors taking no arguments.
        unsafe {
            let pb: Option<Retained<NSObject>> = msg_send![class, generalPasteboard];
            let count: isize = msg_send![&*pb?, changeCount];
            Some(count as u32)
        }
    })
}

/// Reads `public.png` from `NSPasteboard` on macOS when `public.tiff` is
/// absent.
#[cfg(target_os = "macos")]
fn mac_png_image() -> Option<Image> {
    use objc2::msg_send;
    use objc2::rc::{Retained, autoreleasepool};
    use objc2::runtime::AnyClass;
    use objc2_foundation::{NSData, NSObject, NSString};
    let class = AnyClass::get(c"NSPasteboard")?;
    let bytes = autoreleasepool(|_| {
        // SAFETY: `generalPasteboard` and `dataForType:` are standard
        // NSPasteboard selectors; the bytes are copied before the pool drains.
        unsafe {
            let pb: Option<Retained<NSObject>> = msg_send![class, generalPasteboard];
            let uti = NSString::from_str("public.png");
            let data: Option<Retained<NSData>> = msg_send![&*pb?, dataForType: &*uti];
            Some(data?.to_vec())
        }
    })?;
    from_png_bytes(&bytes)
}

/// Decodes a Windows `CF_DIB` (`BITMAPINFOHEADER`) or `CF_DIBV5`
/// (`BITMAPV4HEADER` / `BITMAPV5HEADER`) buffer (or a BMP file with its
/// 14-byte `BM` header) into an RGBA [`Image`].
/// Notes: handles both 124-byte `BITMAPV5HEADER`s whose pixels start
/// immediately after the header (Chrome, Firefox, `arboard`) and those
/// followed by 12 bytes of RGB bitfield masks (GDI), as well as 32-bit GDI
/// bitmaps whose alpha channel is all zero.
#[cfg_attr(not(any(windows, target_os = "linux", test)), allow(dead_code))]
pub(crate) fn dib_to_image(dib: &[u8]) -> Option<Image> {
    let dib = if dib.len() > 54 && &dib[0..2] == b"BM" {
        &dib[14..]
    } else {
        dib
    };
    if dib.len() < 40 {
        return None;
    }
    let u32_at = |off: usize| -> Option<u32> {
        Some(u32::from_le_bytes(dib.get(off..off + 4)?.try_into().ok()?))
    };
    let i32_at = |off: usize| -> Option<i32> {
        Some(i32::from_le_bytes(dib.get(off..off + 4)?.try_into().ok()?))
    };
    let u16_at = |off: usize| -> Option<u16> {
        Some(u16::from_le_bytes(dib.get(off..off + 2)?.try_into().ok()?))
    };
    let header_size = u32_at(0)? as usize;
    if header_size < 40 || dib.len() < header_size {
        return None;
    }
    let width = u32::try_from(i32_at(4)?).ok()?;
    let raw_height = i32_at(8)?;
    let top_down = raw_height < 0;
    let height = raw_height.unsigned_abs();
    let planes = u16_at(12)?;
    let bpp = u16_at(14)?;
    let compression = u32_at(16)?;
    let pixels_count = width as u64 * height as u64;
    if width == 0 || height == 0 || planes != 1 || pixels_count > MAX_PIXELS {
        return None;
    }
    // BI_RGB = 0, BI_BITFIELDS = 3.
    if compression != 0 && compression != 3 {
        return None;
    }
    let (w, h) = (width as usize, height as usize);
    let mut rgba = vec![0u8; w * h * 4];
    match bpp {
        24 if compression == 0 => {
            let stride = (w * 3 + 3) & !3;
            let needed = stride.checked_mul(h)?;
            let src = dib.get(header_size..header_size.checked_add(needed)?)?;
            for y in 0..h {
                let sy = if top_down { y } else { h - 1 - y };
                let row = &src[sy * stride..sy * stride + w * 3];
                let dst = &mut rgba[y * w * 4..(y + 1) * w * 4];
                for (px, bgr) in dst
                    .as_chunks_mut::<4>()
                    .0
                    .iter_mut()
                    .zip(row.as_chunks::<3>().0)
                {
                    *px = [bgr[2], bgr[1], bgr[0], 0xFF];
                }
            }
        }
        32 => {
            let stride = w * 4;
            let needed = stride.checked_mul(h)?;
            let offset = if dib.len() == header_size + needed {
                header_size
            } else if compression == 3 && dib.len() >= header_size + 12 + needed {
                header_size + 12
            } else if dib.len() >= header_size + needed {
                header_size
            } else {
                return None;
            };
            let (mut rm, mut gm, mut bm, mut am) =
                (0x00FF_0000u32, 0x0000_FF00u32, 0x0000_00FFu32, 0u32);
            if compression == 3 || header_size >= 56 {
                let (r, g, b) = (u32_at(40)?, u32_at(44)?, u32_at(48)?);
                if r != 0 || g != 0 || b != 0 {
                    (rm, gm, bm) = (r, g, b);
                }
                if header_size >= 56 {
                    am = u32_at(52)?;
                }
            }
            let src = dib.get(offset..offset + needed)?;
            let mut any_alpha = false;
            for y in 0..h {
                let sy = if top_down { y } else { h - 1 - y };
                let row = &src[sy * stride..(sy + 1) * stride];
                let dst = &mut rgba[y * w * 4..(y + 1) * w * 4];
                for (px, raw) in dst
                    .as_chunks_mut::<4>()
                    .0
                    .iter_mut()
                    .zip(row.as_chunks::<4>().0)
                {
                    let v = u32::from_le_bytes(*raw);
                    px[0] = extract_channel(v, rm);
                    px[1] = extract_channel(v, gm);
                    px[2] = extract_channel(v, bm);
                    px[3] = if am != 0 {
                        let a = extract_channel(v, am);
                        any_alpha |= a != 0;
                        a
                    } else {
                        let a = raw[3];
                        any_alpha |= a != 0;
                        a
                    };
                }
            }
            if !any_alpha {
                for px in rgba.as_chunks_mut::<4>().0 {
                    px[3] = 0xFF;
                }
            }
        }
        _ => return None,
    }
    Some(Image {
        width,
        height,
        rgba,
    })
}

/// Extracts an 8-bit channel from a 32-bit pixel word using a contiguous bitmask.
#[cfg_attr(not(any(windows, target_os = "linux", test)), allow(dead_code))]
fn extract_channel(pixel: u32, mask: u32) -> u8 {
    if mask == 0 {
        return 0;
    }
    let shift = mask.trailing_zeros();
    let bits = (mask >> shift).count_ones();
    let val = (pixel & mask) >> shift;
    if bits >= 8 {
        (val >> (bits - 8)) as u8
    } else {
        ((val * 255 + ((1 << bits) - 1) / 2) / ((1 << bits) - 1)) as u8
    }
}

/// Whether an item may be offered: some text up to MAX_TEXT, an image up
/// to MAX_PIXELS whose pixels match its size, some files (their limits are
/// checked when they are listed).
fn fits(item: &Item) -> bool {
    match item {
        Item::Text(t) => !t.is_empty() && t.len() <= MAX_TEXT,
        Item::Files(f) => !f.is_empty() && f.len() <= crate::files::MAX_ENTRIES,
        Item::Png(p) => !p.is_empty() && p.len() <= MAX_PNG_WIRE * 4 / 3 + 4,
        Item::Image(i) => {
            let pixels = i.width as u64 * i.height as u64;
            pixels > 0 && pixels <= MAX_PIXELS && i.rgba.len() as u64 == pixels * 4
        }
    }
}

/// Tells a local copy from what this side last saw or wrote, so a copy is
/// offered once and an item from the other side never echoes back.
pub struct Watch {
    last: Option<u64>,
}

impl Watch {
    /// Starts from what the clipboard holds now, which is not offered: only
    /// later copies are.
    pub fn new(now: Option<&Item>) -> Self {
        Watch {
            last: now.map(fingerprint),
        }
    }

    /// Returns the local item to offer, when it changed, fits and is not a
    /// text Kyokai put there itself ([`mine`]).
    pub fn changed(&mut self, now: Option<Item>) -> Option<Item> {
        let item = now.filter(fits)?;
        let f = fingerprint(&item);
        if self.last == Some(f) {
            return None;
        }
        self.last = Some(f);
        (!is_mine(&item)).then_some(item)
    }

    /// Notes an item written from the other side.
    pub fn wrote(&mut self, item: &Item) {
        self.last = Some(fingerprint(item));
    }
}

fn fingerprint(item: &Item) -> u64 {
    let mut h = DefaultHasher::new();
    item.hash(&mut h);
    h.finish()
}

impl Hash for Item {
    fn hash<H: Hasher>(&self, h: &mut H) {
        match self {
            Item::Text(t) => (0u8, t).hash(h),
            Item::Image(i) => (1u8, i.width, i.height, &i.rgba).hash(h),
            Item::Png(p) => (2u8, p).hash(h),
            Item::Files(f) => (3u8, f).hash(h),
        }
    }
}

/// Encodes an image as an 8-bit RGBA PNG within [`MAX_PNG_WIRE`].
/// Notes: tries fast compression first, and falls back to default
/// compression with Paeth filtering when fast compression exceeds the wire
/// limit.
pub(crate) fn to_png_bytes(image: &Image) -> Option<Vec<u8>> {
    if !fits(&Item::Image(image.clone())) {
        return None;
    }
    for comp in [png::Compression::Fast, png::Compression::Balanced] {
        let mut png_bytes = Vec::new();
        let ok = (|| -> Option<()> {
            let mut enc = png::Encoder::new(&mut png_bytes, image.width, image.height);
            enc.set_color(png::ColorType::Rgba);
            enc.set_depth(png::BitDepth::Eight);
            enc.set_compression(comp);
            let mut w = enc.write_header().ok()?;
            w.write_image_data(&image.rgba).ok()
        })()
        .is_some();
        if ok && png_bytes.len() <= MAX_PNG_WIRE {
            return Some(png_bytes);
        }
    }
    None
}

/// An image as the wire carries it: base64 PNG.
/// Returns: None when the PNG would be too large for one message.
pub fn to_png(image: &Image) -> Option<String> {
    to_png_bytes(image).map(|b| base64::engine::general_purpose::STANDARD.encode(&b))
}

/// Decodes PNG bytes (RGB, RGBA, palette or grayscale, 8-bit or 16-bit)
/// into an 8-bit RGBA [`Image`] within [`MAX_PIXELS`].
pub(crate) fn from_png_bytes(bytes: &[u8]) -> Option<Image> {
    if bytes.is_empty() || bytes.len() > MAX_PNG {
        return None;
    }
    let mut dec = png::Decoder::new(std::io::Cursor::new(bytes));
    dec.set_transformations(
        png::Transformations::EXPAND | png::Transformations::STRIP_16 | png::Transformations::ALPHA,
    );
    let mut reader = dec.read_info().ok()?;
    let (width, height) = (reader.info().width, reader.info().height);
    if width == 0 || height == 0 || width as u64 * height as u64 > MAX_PIXELS {
        return None;
    }
    let (color, depth) = reader.output_color_type();
    if depth != png::BitDepth::Eight {
        return None;
    }
    let mut buf = vec![0; reader.output_buffer_size()?];
    let frame = reader.next_frame(&mut buf).ok()?;
    buf.truncate(frame.buffer_size());
    let rgba = match color {
        png::ColorType::Rgba => buf,
        png::ColorType::GrayscaleAlpha => {
            let mut out = Vec::with_capacity((width as usize) * (height as usize) * 4);
            for &[g, a] in buf.as_chunks::<2>().0 {
                out.extend_from_slice(&[g, g, g, a]);
            }
            out
        }
        _ => return None,
    };
    let image = Image {
        width,
        height,
        rgba,
    };
    fits(&Item::Image(image.clone())).then_some(image)
}

/// An image from the wire, checked: PNG decoded to 8-bit RGBA within the
/// limits.
pub fn from_png(b64: &str) -> Option<Image> {
    if b64.len() > MAX_PNG * 4 / 3 + 4 {
        return None;
    }
    let bytes = base64::engine::general_purpose::STANDARD.decode(b64).ok()?;
    from_png_bytes(&bytes)
}

/// One session's clipboard sharing, until dropped.
pub struct Share {
    /// Local copies to offer to the other side.
    pub offers: mpsc::Receiver<Item>,
    /// Items from the other side (or files received), to put on the local
    /// clipboard.
    pub writes: std::sync::mpsc::Sender<Item>,
    stop: Arc<AtomicBool>,
}

impl Drop for Share {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::Relaxed);
    }
}

/// Starts sharing a clipboard on a thread of its own (the system
/// clipboard's calls block and may wait for other programs).
pub fn share(mut clipboard: Box<dyn Clipboard>) -> Share {
    let (offer_tx, offers) = mpsc::channel(4);
    let (writes, write_rx) = std::sync::mpsc::channel::<Item>();
    let stop = Arc::new(AtomicBool::new(false));
    let stopped = stop.clone();
    std::thread::spawn(move || {
        let mut watch = Watch::new(clipboard.item().as_ref());
        let mut seen = clipboard.serial();
        while !stopped.load(Ordering::Relaxed) {
            while let Ok(item) = write_rx.try_recv() {
                if clipboard.set(&item) {
                    watch.wrote(&item);
                    seen = clipboard.serial();
                }
            }
            // Read only when the clipboard changed (where the system says).
            let serial = clipboard.serial();
            if serial.is_none() || serial != seen {
                seen = serial;
                if let Some(item) = watch.changed(clipboard.item())
                    && offer_tx.try_send(item).is_err()
                    && offer_tx.is_closed()
                {
                    break;
                }
            }
            std::thread::sleep(POLL);
        }
    });
    Share {
        offers,
        writes,
        stop,
    }
}

#[cfg(target_os = "linux")]
mod x11 {
    //! The X11 clipboard (also used on GNOME Wayland through Xwayland): a
    //! persistent selection server that serves all advertised UTF-8 text,
    //! PNG image (with `INCR` chunking) and file-list targets (`text/uri-list`
    //! and `x-special/gnome-copied-files`), and a persistent reader that
    //! queries `TARGETS` first and caches images by owner and timestamp.

    use super::{
        Clipboard, Image, Item, MAX_PNG, dib_to_image, fits, from_png_bytes, to_png_bytes,
    };
    use std::ffi::OsString;
    use std::os::unix::ffi::{OsStrExt as _, OsStringExt as _};
    use std::path::PathBuf;
    use std::sync::atomic::{AtomicBool, Ordering};
    use std::sync::{Arc, Mutex};
    use std::time::{Duration, Instant};
    use x11rb::connection::Connection as _;
    use x11rb::protocol::Event;
    use x11rb::protocol::xproto::{
        Atom, AtomEnum, ChangeWindowAttributesAux, ConnectionExt as _, CreateWindowAux, EventMask,
        PropMode, Property, SELECTION_NOTIFY_EVENT, SelectionNotifyEvent, SelectionRequestEvent,
        WindowClass,
    };
    use x11rb::rust_connection::RustConnection;
    use x11rb::wrapper::ConnectionExt as _;
    use x11rb::{COPY_DEPTH_FROM_PARENT, COPY_FROM_PARENT, CURRENT_TIME, NONE};

    /// Largest property written in one piece before switching to `INCR`.
    const INCR_CHUNK: usize = 256 * 1024;
    /// How long one selection read waits for the owner.
    const READ_TIMEOUT: Duration = Duration::from_millis(2000);
    /// How long an unfinished outbound `INCR` transfer is kept.
    const INCR_TTL: Duration = Duration::from_secs(10);

    x11rb::atom_manager! {
        pub Atoms: AtomCookies {
            CLIPBOARD,
            TARGETS,
            TIMESTAMP,
            SAVE_TARGETS,
            ATOM,
            INTEGER,
            INCR,
            UTF8_STRING,
            UTF8_MIME_0: b"text/plain;charset=utf-8",
            UTF8_MIME_1: b"text/plain;charset=UTF-8",
            TEXT_PLAIN: b"text/plain",
            STRING,
            TEXT,
            URI_LIST: b"text/uri-list",
            GNOME_FILES: b"x-special/gnome-copied-files",
            MATE_FILES: b"x-special/mate-copied-files",
            IMAGE_PNG: b"image/png",
            IMAGE_BMP: b"image/bmp",
            IMAGE_X_BMP: b"image/x-bmp",
            IMAGE_X_MS_BMP: b"image/x-MS-bmp",
            PROP: b"KYOKAI_CLIPBOARD",
            TEST_SEL: b"KYOKAI_TEST_SELECTION",
        }
    }

    /// Opens an X11 connection and creates a 1x1 unmapped window for
    /// selection events.
    fn open_context() -> Option<(RustConnection, u32, Atoms)> {
        let (conn, screen_num) = RustConnection::connect(None).ok()?;
        let screen = conn.setup().roots.get(screen_num)?;
        let win_id = conn.generate_id().ok()?;
        let mask = EventMask::PROPERTY_CHANGE | EventMask::STRUCTURE_NOTIFY;
        conn.create_window(
            COPY_DEPTH_FROM_PARENT,
            win_id,
            screen.root,
            0,
            0,
            1,
            1,
            0,
            WindowClass::COPY_FROM_PARENT,
            COPY_FROM_PARENT,
            &CreateWindowAux::new().event_mask(mask),
        )
        .ok()?;
        conn.flush().ok()?;
        let atoms = Atoms::new(&conn).ok()?.reply().ok()?;
        Some((conn, win_id, atoms))
    }

    #[derive(Default)]
    struct ServerState {
        /// The item Kyokai set, while `Server::win_id` still owns `CLIPBOARD`.
        current: Option<Item>,
        /// Target atoms and their payloads for `SelectionRequest`.
        targets: Vec<(Atom, Arc<[u8]>)>,
    }

    struct IncrSend {
        requestor: u32,
        property: Atom,
        target: Atom,
        bytes: Arc<[u8]>,
        offset: usize,
        updated: Instant,
    }

    /// Process-wide X11 selection owner, kept alive across sessions so
    /// closing a session never destroys the clipboard window.
    pub(super) struct Server {
        conn: RustConnection,
        win_id: u32,
        selection: Atom,
        atoms: Atoms,
        state: Mutex<ServerState>,
        alive: AtomicBool,
    }

    static SERVER: Mutex<Option<Arc<Server>>> = Mutex::new(None);

    impl Server {
        fn get() -> Option<Arc<Server>> {
            let mut guard = SERVER.lock().ok()?;
            if let Some(s) = guard.as_ref()
                && s.alive.load(Ordering::Acquire)
            {
                return Some(s.clone());
            }
            let server = Self::spawn(false)?;
            *guard = Some(server.clone());
            Some(server)
        }

        pub(super) fn spawn(test_selection: bool) -> Option<Arc<Server>> {
            let (conn, win_id, atoms) = open_context()?;
            let selection = if test_selection {
                atoms.TEST_SEL
            } else {
                atoms.CLIPBOARD
            };
            let server = Arc::new(Server {
                conn,
                win_id,
                selection,
                atoms,
                state: Mutex::new(ServerState::default()),
                alive: AtomicBool::new(true),
            });
            let bg = server.clone();
            std::thread::Builder::new()
                .name("kyokai-x11-clip".into())
                .spawn(move || bg.serve())
                .ok()?;
            Some(server)
        }

        fn current(&self) -> Option<Item> {
            self.state.lock().ok()?.current.clone()
        }

        fn set(&self, item: &Item) -> bool {
            if !self.alive.load(Ordering::Acquire) {
                return false;
            }
            let targets = match item {
                Item::Text(text) => {
                    let b: Arc<[u8]> = Arc::from(text.as_bytes());
                    vec![
                        (self.atoms.UTF8_STRING, b.clone()),
                        (self.atoms.UTF8_MIME_0, b.clone()),
                        (self.atoms.UTF8_MIME_1, b.clone()),
                        (self.atoms.TEXT_PLAIN, b.clone()),
                        (self.atoms.STRING, b.clone()),
                        (self.atoms.TEXT, b),
                    ]
                }
                Item::Image(img) => {
                    let Some(png) = to_png_bytes(img) else {
                        return false;
                    };
                    vec![(self.atoms.IMAGE_PNG, Arc::from(png))]
                }
                Item::Files(paths) => {
                    let Some((uri_list, gnome, plain)) = encode_file_list(paths) else {
                        return false;
                    };
                    let uri_b: Arc<[u8]> = Arc::from(uri_list.into_bytes());
                    let gnome_b: Arc<[u8]> = Arc::from(gnome.into_bytes());
                    let plain_b: Arc<[u8]> = Arc::from(plain.into_bytes());
                    vec![
                        (self.atoms.URI_LIST, uri_b),
                        (self.atoms.GNOME_FILES, gnome_b.clone()),
                        (self.atoms.MATE_FILES, gnome_b),
                        (self.atoms.UTF8_STRING, plain_b.clone()),
                        (self.atoms.UTF8_MIME_0, plain_b),
                    ]
                }
                Item::Png(_) => return false,
            };
            {
                let Ok(mut st) = self.state.lock() else {
                    return false;
                };
                st.current = Some(item.clone());
                st.targets = targets;
            }
            self.conn
                .set_selection_owner(self.win_id, self.selection, CURRENT_TIME)
                .is_ok()
                && self.conn.flush().is_ok()
        }

        fn serve(&self) {
            let mut incrs: Vec<IncrSend> = Vec::new();
            while let Ok(event) = self.conn.wait_for_event() {
                incrs.retain(|i| i.updated.elapsed() < INCR_TTL);
                match event {
                    Event::DestroyNotify(ev) if ev.window == self.win_id => break,
                    Event::SelectionClear(ev) if ev.selection == self.selection => {
                        let still_ours = self
                            .conn
                            .get_selection_owner(self.selection)
                            .ok()
                            .and_then(|c| c.reply().ok())
                            .is_some_and(|r| r.owner == self.win_id);
                        if !still_ours && let Ok(mut st) = self.state.lock() {
                            st.current = None;
                            st.targets.clear();
                        }
                    }
                    Event::SelectionRequest(req) => {
                        self.handle_request(req, &mut incrs);
                    }
                    Event::PropertyNotify(ev) if ev.state == Property::DELETE => {
                        if let Some(idx) = incrs
                            .iter()
                            .position(|i| i.requestor == ev.window && i.property == ev.atom)
                        {
                            let done = {
                                let incr = &mut incrs[idx];
                                if incr.offset < incr.bytes.len() {
                                    let end = (incr.offset + INCR_CHUNK).min(incr.bytes.len());
                                    let _ = self.conn.change_property8(
                                        PropMode::REPLACE,
                                        incr.requestor,
                                        incr.property,
                                        incr.target,
                                        &incr.bytes[incr.offset..end],
                                    );
                                    let _ = self.conn.flush();
                                    incr.offset = end;
                                    incr.updated = Instant::now();
                                    false
                                } else {
                                    let _ = self.conn.change_property8(
                                        PropMode::REPLACE,
                                        incr.requestor,
                                        incr.property,
                                        incr.target,
                                        &[],
                                    );
                                    let _ = self.conn.change_window_attributes(
                                        incr.requestor,
                                        &ChangeWindowAttributesAux::new()
                                            .event_mask(EventMask::NO_EVENT),
                                    );
                                    let _ = self.conn.flush();
                                    true
                                }
                            };
                            if done {
                                incrs.swap_remove(idx);
                            }
                        }
                    }
                    _ => {}
                }
            }
            self.alive.store(false, Ordering::Release);
        }

        fn handle_request(&self, req: SelectionRequestEvent, incrs: &mut Vec<IncrSend>) {
            let prop = if req.property == NONE {
                req.target
            } else {
                req.property
            };
            let ok = if req.selection != self.selection || prop == NONE {
                false
            } else if req.target == self.atoms.TARGETS {
                let mut list = vec![
                    self.atoms.TARGETS,
                    self.atoms.TIMESTAMP,
                    self.atoms.SAVE_TARGETS,
                ];
                if let Ok(st) = self.state.lock() {
                    for &(atom, _) in &st.targets {
                        if !list.contains(&atom) {
                            list.push(atom);
                        }
                    }
                }
                self.conn
                    .change_property32(
                        PropMode::REPLACE,
                        req.requestor,
                        prop,
                        AtomEnum::ATOM,
                        &list,
                    )
                    .is_ok()
            } else if req.target == self.atoms.TIMESTAMP {
                self.conn
                    .change_property32(
                        PropMode::REPLACE,
                        req.requestor,
                        prop,
                        AtomEnum::INTEGER,
                        &[0u32],
                    )
                    .is_ok()
            } else if req.target == self.atoms.SAVE_TARGETS {
                self.conn
                    .change_property8(PropMode::REPLACE, req.requestor, prop, AtomEnum::ATOM, &[])
                    .is_ok()
            } else {
                let found = self
                    .state
                    .lock()
                    .ok()
                    .and_then(|st| st.targets.iter().find(|(a, _)| *a == req.target).cloned());
                match found {
                    Some((format_atom, bytes)) if bytes.len() <= INCR_CHUNK => self
                        .conn
                        .change_property8(
                            PropMode::REPLACE,
                            req.requestor,
                            prop,
                            format_atom,
                            &bytes,
                        )
                        .is_ok(),
                    Some((format_atom, bytes)) => {
                        let _ = self.conn.change_window_attributes(
                            req.requestor,
                            &ChangeWindowAttributesAux::new()
                                .event_mask(EventMask::PROPERTY_CHANGE),
                        );
                        let ok = self
                            .conn
                            .change_property32(
                                PropMode::REPLACE,
                                req.requestor,
                                prop,
                                self.atoms.INCR,
                                &[bytes.len().min(u32::MAX as usize) as u32],
                            )
                            .is_ok();
                        if ok {
                            incrs.push(IncrSend {
                                requestor: req.requestor,
                                property: prop,
                                target: format_atom,
                                bytes,
                                offset: 0,
                                updated: Instant::now(),
                            });
                        }
                        ok
                    }
                    None => false,
                }
            };
            let notify = SelectionNotifyEvent {
                response_type: SELECTION_NOTIFY_EVENT,
                sequence: req.sequence,
                time: req.time,
                requestor: req.requestor,
                selection: req.selection,
                target: req.target,
                property: if ok { prop } else { NONE },
            };
            let _ = self
                .conn
                .send_event(false, req.requestor, EventMask::NO_EVENT, notify);
            let _ = self.conn.flush();
        }
    }

    /// A session's X11 clipboard handle: reads through its own window and
    /// writes through the process-wide [`Server`].
    pub(super) struct Board {
        conn: RustConnection,
        win_id: u32,
        selection: Atom,
        pub(super) atoms: Atoms,
        server: Arc<Server>,
        /// Last image read from another owner `(owner_win, timestamp, image)`,
        /// so a still image on the clipboard is not transferred and decoded
        /// again every 500 ms.
        cached_image: Option<(u32, u32, Image)>,
    }

    impl Board {
        pub(super) fn new() -> Option<Self> {
            Self::with_server(Server::get()?)
        }

        pub(super) fn with_server(server: Arc<Server>) -> Option<Self> {
            let (conn, win_id, atoms) = open_context()?;
            let selection = server.selection;
            Some(Board {
                conn,
                win_id,
                selection,
                atoms,
                server,
                cached_image: None,
            })
        }

        /// Reads one target format from `selection` (with `INCR` when
        /// chunked by the owner).
        pub(super) fn read_bytes(&self, target: Atom) -> Option<Vec<u8>> {
            while self.conn.poll_for_event().ok()?.is_some() {}
            self.conn
                .delete_property(self.win_id, self.atoms.PROP)
                .ok()?;
            self.conn
                .convert_selection(
                    self.win_id,
                    self.selection,
                    target,
                    self.atoms.PROP,
                    CURRENT_TIME,
                )
                .ok()?;
            self.conn.flush().ok()?;

            let mut deadline = Instant::now() + READ_TIMEOUT;
            let mut using_incr = false;
            let mut incr_data = Vec::new();
            while Instant::now() < deadline {
                let Some(event) = self.conn.poll_for_event().ok()? else {
                    std::thread::sleep(Duration::from_millis(1));
                    continue;
                };
                match event {
                    Event::SelectionNotify(ev) if ev.selection == self.selection => {
                        if ev.property == NONE || ev.target != target {
                            return None;
                        }
                        let reply = self
                            .conn
                            .get_property(
                                true,
                                self.win_id,
                                ev.property,
                                AtomEnum::ANY,
                                0,
                                u32::MAX / 4,
                            )
                            .ok()?
                            .reply()
                            .ok()?;
                        if reply.type_ == self.atoms.INCR {
                            using_incr = true;
                            if let Some(mut vals) = reply.value32()
                                && let Some(n) = vals.next()
                            {
                                incr_data.reserve((n as usize).min(MAX_PNG * 2));
                            }
                            deadline = Instant::now() + READ_TIMEOUT;
                        } else {
                            return Some(reply.value);
                        }
                    }
                    Event::PropertyNotify(ev)
                        if using_incr
                            && ev.window == self.win_id
                            && ev.atom == self.atoms.PROP
                            && ev.state == Property::NEW_VALUE =>
                    {
                        let reply = self
                            .conn
                            .get_property(
                                true,
                                self.win_id,
                                self.atoms.PROP,
                                AtomEnum::ANY,
                                0,
                                u32::MAX / 4,
                            )
                            .ok()?
                            .reply()
                            .ok()?;
                        if reply.value.is_empty() {
                            return Some(incr_data);
                        }
                        if incr_data.len() + reply.value.len() > MAX_PNG * 2 {
                            return None;
                        }
                        incr_data.extend_from_slice(&reply.value);
                        deadline = Instant::now() + READ_TIMEOUT;
                    }
                    _ => {}
                }
            }
            None
        }

        /// The target atoms the current `CLIPBOARD` owner advertises.
        fn read_targets(&self) -> Vec<Atom> {
            let Some(bytes) = self.read_bytes(self.atoms.TARGETS) else {
                return Vec::new();
            };
            bytes
                .as_chunks::<4>()
                .0
                .iter()
                .map(|c| u32::from_ne_bytes(*c))
                .collect()
        }

        /// The owner's selection timestamp when it reports one (`0` otherwise).
        fn read_timestamp(&self) -> u32 {
            let Some(bytes) = self.read_bytes(self.atoms.TIMESTAMP) else {
                return 0;
            };
            bytes
                .get(0..4)
                .map_or(0, |c| u32::from_ne_bytes([c[0], c[1], c[2], c[3]]))
        }
    }

    impl Drop for Board {
        fn drop(&mut self) {
            let _ = self.conn.destroy_window(self.win_id);
            let _ = self.conn.flush();
        }
    }

    impl Clipboard for Board {
        fn serial(&mut self) -> Option<u32> {
            None
        }

        fn item(&mut self) -> Option<Item> {
            let owner = self
                .conn
                .get_selection_owner(self.selection)
                .ok()?
                .reply()
                .ok()?
                .owner;
            if owner == NONE {
                return None;
            }
            if owner == self.server.win_id {
                return self.server.current();
            }
            let targets = self.read_targets();
            if targets.contains(&self.atoms.URI_LIST) || targets.contains(&self.atoms.GNOME_FILES) {
                let target = if targets.contains(&self.atoms.URI_LIST) {
                    self.atoms.URI_LIST
                } else {
                    self.atoms.GNOME_FILES
                };
                if let Some(bytes) = self.read_bytes(target) {
                    let files = paths_from_uri_list(&bytes);
                    if !files.is_empty() {
                        return Some(Item::Files(files));
                    }
                }
            }
            let img_target = [
                self.atoms.IMAGE_PNG,
                self.atoms.IMAGE_BMP,
                self.atoms.IMAGE_X_BMP,
                self.atoms.IMAGE_X_MS_BMP,
            ]
            .into_iter()
            .find(|a| targets.contains(a));
            if let Some(target) = img_target {
                let ts = if targets.contains(&self.atoms.TIMESTAMP) {
                    self.read_timestamp()
                } else {
                    0
                };
                if ts != 0
                    && let Some((cached_owner, cached_ts, ref img)) = self.cached_image
                    && cached_owner == owner
                    && cached_ts == ts
                {
                    return Some(Item::Image(img.clone()));
                }
                if let Some(bytes) = self.read_bytes(target) {
                    let img = if target == self.atoms.IMAGE_PNG {
                        from_png_bytes(&bytes)
                    } else {
                        dib_to_image(&bytes).filter(|i| fits(&Item::Image(i.clone())))
                    };
                    if let Some(img) = img {
                        if ts != 0 {
                            self.cached_image = Some((owner, ts, img.clone()));
                        }
                        return Some(Item::Image(img));
                    }
                }
            }
            let text_atoms = [
                self.atoms.UTF8_STRING,
                self.atoms.UTF8_MIME_0,
                self.atoms.UTF8_MIME_1,
                self.atoms.TEXT_PLAIN,
                self.atoms.STRING,
                self.atoms.TEXT,
            ];
            let text_target = if targets.is_empty() {
                Some(self.atoms.UTF8_STRING)
            } else {
                text_atoms.into_iter().find(|a| targets.contains(a))
            };
            if let Some(target) = text_target
                && let Some(bytes) = self.read_bytes(target)
            {
                let text = if target == self.atoms.STRING {
                    bytes.into_iter().map(|b| b as char).collect()
                } else {
                    String::from_utf8(bytes).ok()?
                };
                let text = text.trim_end_matches('\0').to_owned();
                if !text.is_empty() {
                    return Some(Item::Text(text));
                }
            }
            None
        }

        fn set(&mut self, item: &Item) -> bool {
            self.server.set(item)
        }
    }

    /// Decodes a `text/uri-list` or `x-special/gnome-copied-files` payload
    /// into local file paths.
    pub(super) fn paths_from_uri_list(bytes: &[u8]) -> Vec<PathBuf> {
        bytes
            .split(|&b| b == b'\n')
            .map(|line| line.strip_suffix(b"\r").unwrap_or(line))
            .filter(|line| !line.is_empty() && !line.starts_with(b"#"))
            .filter_map(|line| {
                line.strip_prefix(b"file://localhost/")
                    .map(|rest| {
                        let mut p = Vec::with_capacity(1 + rest.len());
                        p.push(b'/');
                        p.extend_from_slice(rest);
                        p
                    })
                    .or_else(|| line.strip_prefix(b"file://").map(|s| s.to_vec()))
            })
            .map(|raw| PathBuf::from(OsString::from_vec(percent_decode(&raw))))
            .collect()
    }

    /// Encodes local file paths as `(text/uri-list, x-special/gnome-copied-files, plain)`.
    pub(super) fn encode_file_list(paths: &[PathBuf]) -> Option<(String, String, String)> {
        let mut uris = Vec::with_capacity(paths.len());
        let mut plain = Vec::with_capacity(paths.len());
        for p in paths {
            let abs = p.canonicalize().ok().unwrap_or_else(|| p.clone());
            if !abs.is_absolute() {
                continue;
            }
            uris.push(format!(
                "file://{}",
                percent_encode(abs.as_os_str().as_bytes())
            ));
            plain.push(abs.to_string_lossy().into_owned());
        }
        if uris.is_empty() {
            return None;
        }
        let uri_list = format!("{}\r\n", uris.join("\r\n"));
        let gnome = format!("copy\n{}", uris.join("\n"));
        Some((uri_list, gnome, plain.join("\n")))
    }

    fn percent_decode(input: &[u8]) -> Vec<u8> {
        let hex = |b: u8| -> Option<u8> {
            match b {
                b'0'..=b'9' => Some(b - b'0'),
                b'a'..=b'f' => Some(b - b'a' + 10),
                b'A'..=b'F' => Some(b - b'A' + 10),
                _ => None,
            }
        };
        let mut out = Vec::with_capacity(input.len());
        let mut i = 0;
        while i < input.len() {
            if input[i] == b'%'
                && i + 2 < input.len()
                && let (Some(hi), Some(lo)) = (hex(input[i + 1]), hex(input[i + 2]))
            {
                out.push((hi << 4) | lo);
                i += 3;
            } else {
                out.push(input[i]);
                i += 1;
            }
        }
        out
    }

    fn percent_encode(input: &[u8]) -> String {
        const HEX: &[u8; 16] = b"0123456789ABCDEF";
        let mut out = String::with_capacity(input.len());
        for &b in input {
            let safe = b.is_ascii_alphanumeric()
                || matches!(
                    b,
                    b'-' | b'.'
                        | b'_'
                        | b'~'
                        | b'/'
                        | b':'
                        | b'@'
                        | b'!'
                        | b'$'
                        | b'&'
                        | b'\''
                        | b'('
                        | b')'
                        | b'*'
                        | b'+'
                        | b','
                        | b'='
                );
            if safe {
                out.push(b as char);
            } else {
                out.push('%');
                out.push(HEX[(b >> 4) as usize] as char);
                out.push(HEX[(b & 0x0F) as usize] as char);
            }
        }
        out
    }
}

#[cfg(test)]
pub(crate) mod fake {
    //! A clipboard in memory for the session tests.
    use super::*;
    use std::sync::Mutex;

    /// What a fake clipboard holds; tests read and write it.
    pub type Board = Arc<Mutex<Option<Item>>>;

    pub struct Fake(pub Board);

    impl Clipboard for Fake {
        fn serial(&mut self) -> Option<u32> {
            None
        }

        fn item(&mut self) -> Option<Item> {
            self.0.lock().unwrap().clone()
        }

        fn set(&mut self, item: &Item) -> bool {
            *self.0.lock().unwrap() = Some(item.clone());
            true
        }
    }

    /// An opener that gives every session the same fake clipboard.
    pub fn opener(board: Board) -> Open {
        Arc::new(move || Some(Box::new(Fake(board.clone())) as Box<dyn Clipboard>))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn text(t: &str) -> Option<Item> {
        Some(Item::Text(t.into()))
    }

    fn image(w: u32, h: u32, v: u8) -> Item {
        Item::Image(Image {
            width: w,
            height: h,
            rgba: vec![v; (w * h * 4) as usize],
        })
    }

    #[test]
    fn copies_are_offered_once_and_never_echo() {
        let mut w = Watch::new(text("before the session").as_ref());
        assert_eq!(
            w.changed(text("before the session")),
            None,
            "what was there is not offered"
        );
        assert_eq!(w.changed(text("copied")), text("copied"));
        assert_eq!(w.changed(text("copied")), None, "offered once");
        w.wrote(&Item::Text("from the other side".into()));
        assert_eq!(w.changed(text("from the other side")), None, "no echo");
        assert_eq!(w.changed(None), None);
        assert_eq!(w.changed(text("")), None);
        let big = "x".repeat(MAX_TEXT + 1);
        assert_eq!(w.changed(text(&big)), None, "too large to offer");
        assert_eq!(
            w.changed(text("copied")),
            text("copied"),
            "a copy again after another"
        );
        assert_eq!(w.changed(Some(image(4, 3, 9))), Some(image(4, 3, 9)));
        assert_eq!(w.changed(Some(image(4, 3, 9))), None);
        let bad = Item::Image(Image {
            width: 4,
            height: 3,
            rgba: vec![0; 5],
        });
        assert_eq!(
            w.changed(Some(bad)),
            None,
            "pixels that do not match the size"
        );
    }

    /// Regression: on macOS and Linux text Kyokai copied itself (the
    /// temporary password) was offered to controllers, since only Windows
    /// tells who owns the clipboard.
    #[test]
    fn texts_kyokai_copied_are_never_offered() {
        let digest =
            |t: &str| -> [u8; 32] { <sha2::Sha256 as sha2::Digest>::digest(t.as_bytes()).into() };
        let password = "kyokai-test-password-7f3a";
        mine(digest(password));
        let mut w = Watch::new(None);
        assert_eq!(w.changed(text(password)), None, "Kyokai's own text");
        assert_eq!(w.changed(text("typed")), text("typed"));
        assert_eq!(w.changed(text(password)), None, "again after another copy");
        // Only the last 8 are kept: the oldest is forgotten.
        for i in 0..MINE {
            mine(digest(&format!("kyokai-test-later-{i}")));
        }
        assert_eq!(w.changed(text("typed")), text("typed"));
        assert_eq!(w.changed(text(password)), text(password));
    }

    #[test]
    fn images_cross_as_png_and_come_back_checked() {
        let Item::Image(img) = image(64, 48, 200) else {
            unreachable!()
        };
        let wire = to_png(&img).unwrap();
        assert_eq!(from_png(&wire), Some(img.clone()));
        assert_eq!(from_png("not base64!"), None);
        assert_eq!(
            from_png(&base64::engine::general_purpose::STANDARD.encode(b"not a png")),
            None
        );

        // 24-bit RGB PNGs (as Android or system tools may hand over) expand
        // to 8-bit RGBA on decode.
        let mut rgb_png = Vec::new();
        {
            let mut enc = png::Encoder::new(&mut rgb_png, 2, 2);
            enc.set_color(png::ColorType::Rgb);
            enc.set_depth(png::BitDepth::Eight);
            let mut w = enc.write_header().unwrap();
            w.write_image_data(&[10, 20, 30, 40, 50, 60, 70, 80, 90, 100, 110, 120])
                .unwrap();
        }
        let b64 = base64::engine::general_purpose::STANDARD.encode(&rgb_png);
        assert_eq!(
            from_png(&b64),
            Some(Image {
                width: 2,
                height: 2,
                rgba: vec![
                    10, 20, 30, 255, 40, 50, 60, 255, 70, 80, 90, 255, 100, 110, 120, 255,
                ],
            })
        );
    }

    /// Regression: `image` 0.25.10 shifts the pixel offset of a 124-byte
    /// `BITMAPV5HEADER` with `BI_BITFIELDS` by 12 bytes, so Chrome's and
    /// `arboard`'s own `CF_DIBV5` images failed with `UnexpectedEof` on
    /// Windows; GDI 32-bit `BI_RGB` bitmaps leave alpha at 0.
    #[test]
    fn windows_dib_and_dibv5_images_decode() {
        // Chrome's 5x5 32-bit CF_DIBV5 (124-byte header + 100 pixel bytes).
        let chrome = vec![
            124, 0, 0, 0, 5, 0, 0, 0, 5, 0, 0, 0, 1, 0, 32, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
            0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 255,
            32, 110, 105, 87, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
            0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 4, 0, 0, 0,
            0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 32, 38, 145, 192, 38, 65, 111, 158, 46, 73, 68,
            107, 50, 73, 50, 92, 55, 79, 55, 100, 31, 46, 139, 190, 41, 76, 100, 152, 49, 81, 60,
            110, 53, 83, 53, 108, 60, 91, 60, 118, 32, 59, 131, 187, 44, 86, 89, 150, 51, 89, 56,
            121, 57, 95, 57, 127, 63, 103, 63, 139, 35, 71, 122, 186, 46, 95, 76, 150, 52, 99, 54,
            136, 59, 105, 59, 146, 65, 113, 65, 156, 37, 86, 109, 184, 46, 103, 63, 155, 52, 107,
            53, 152, 60, 114, 60, 162, 68, 123, 68, 174,
        ];
        let img = dib_to_image(&chrome).expect("Chrome CF_DIBV5");
        assert_eq!((img.width, img.height), (5, 5));
        assert_eq!(&img.rgba[0..4], &[109, 86, 37, 184]);

        // The same with BI_BITFIELDS (3) and explicit masks in the 124-byte header,
        // both without and with 12 trailing mask bytes.
        let mut bitfields = chrome.clone();
        bitfields[16] = 3;
        bitfields[40..44].copy_from_slice(&0x00FF_0000u32.to_le_bytes());
        bitfields[44..48].copy_from_slice(&0x0000_FF00u32.to_le_bytes());
        bitfields[48..52].copy_from_slice(&0x0000_00FFu32.to_le_bytes());
        assert_eq!(dib_to_image(&bitfields), Some(img.clone()));
        let mut with_extra = Vec::new();
        with_extra.extend_from_slice(&bitfields[..124]);
        with_extra.extend_from_slice(&[0u8; 12]);
        with_extra.extend_from_slice(&bitfields[124..]);
        assert_eq!(dib_to_image(&with_extra), Some(img));

        // A GDI 32-bit BI_RGB BITMAPINFOHEADER (40 bytes) with 0 alpha bytes
        // becomes opaque RGBA.
        let mut gdi = vec![0u8; 40 + 4];
        gdi[0..4].copy_from_slice(&40u32.to_le_bytes());
        gdi[4..8].copy_from_slice(&1i32.to_le_bytes());
        gdi[8..12].copy_from_slice(&1i32.to_le_bytes());
        gdi[12..14].copy_from_slice(&1u16.to_le_bytes());
        gdi[14..16].copy_from_slice(&32u16.to_le_bytes());
        gdi[40..44].copy_from_slice(&[30, 20, 10, 0]);
        assert_eq!(
            dib_to_image(&gdi),
            Some(Image {
                width: 1,
                height: 1,
                rgba: vec![10, 20, 30, 255],
            })
        );
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn linux_uri_lists_round_trip() {
        let raw = b"copy\nfile:///tmp/bar.log\r\nfile:///tmp/white%20space.txt\n";
        let got = x11::paths_from_uri_list(raw);
        assert_eq!(
            got,
            vec![
                PathBuf::from("/tmp/bar.log"),
                PathBuf::from("/tmp/white space.txt"),
            ]
        );
        let dir = std::env::temp_dir();
        let (uri_list, gnome, plain) = x11::encode_file_list(std::slice::from_ref(&dir)).unwrap();
        assert!(uri_list.starts_with("file:///") && uri_list.ends_with("\r\n"));
        assert!(gnome.starts_with("copy\nfile:///"));
        assert!(!plain.is_empty());
        assert_eq!(
            x11::paths_from_uri_list(uri_list.as_bytes()),
            vec![dir.canonicalize().unwrap_or(dir)]
        );
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn x11_clipboard_serves_utf8_mimes_incr_images_and_gnome_files() {
        let (Some(srv_a), Some(srv_b)) = (x11::Server::spawn(true), x11::Server::spawn(true))
        else {
            return;
        };
        let (Some(mut a), Some(mut b)) = (
            x11::Board::with_server(srv_a),
            x11::Board::with_server(srv_b),
        ) else {
            return;
        };
        // Text: serves both `text/plain;charset=utf-8` (asked by Chromium,
        // Electron and Mutter) and `UTF8_STRING`.
        assert!(a.set(&Item::Text("剪贴板 Kyokai".into())));
        assert_eq!(
            b.read_bytes(b.atoms.UTF8_MIME_0).as_deref(),
            Some("剪贴板 Kyokai".as_bytes())
        );
        assert_eq!(b.item(), Some(Item::Text("剪贴板 Kyokai".into())));

        // A large image (> 256 KiB PNG) crosses X11 both ways through INCR.
        let (w, h) = (400u32, 300u32);
        let mut rgba = vec![0u8; (w * h * 4) as usize];
        let mut s = 0x1234_5678u32;
        for byte in &mut rgba {
            s = s.wrapping_mul(1664525).wrapping_add(1013904223);
            *byte = (s >> 24) as u8;
        }
        let big = Image {
            width: w,
            height: h,
            rgba,
        };
        assert!(to_png_bytes(&big).unwrap().len() > 256 * 1024);
        assert!(a.set(&Item::Image(big.clone())));
        assert_eq!(b.item(), Some(Item::Image(big)));

        // Files: serves both `text/uri-list` and `x-special/gnome-copied-files`.
        let dir = std::env::temp_dir().canonicalize().unwrap();
        assert!(a.set(&Item::Files(vec![dir.clone()])));
        let gnome = b.read_bytes(b.atoms.GNOME_FILES).unwrap();
        assert!(gnome.starts_with(b"copy\nfile:///"));
        assert_eq!(b.item(), Some(Item::Files(vec![dir])));
    }
}
