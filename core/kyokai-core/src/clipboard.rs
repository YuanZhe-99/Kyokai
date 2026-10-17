//! Clipboard sharing in a session (doc/en-us/computer-use.md,
//! "Clipboard"): while a session may share the clipboard, text or an image
//! copied on either side is offered to the other. What Kyokai itself put
//! there (the Device ID, a password) is never offered.

use base64::Engine;
use std::hash::{DefaultHasher, Hash, Hasher};
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::Duration;
use tokio::sync::mpsc;

/// Most text one side offers.
pub const MAX_TEXT: usize = 1 << 20;
/// Most pixels of an image one side offers (a 4K screenshot).
pub const MAX_PIXELS: u64 = 3840 * 2160;
/// Most bytes of an image's PNG, so it fits one message in base64.
pub const MAX_PNG: usize = 3 << 20;
/// How often the clipboard is looked at for changes (it announces none).
const POLL: Duration = Duration::from_millis(500);

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
}

/// A clipboard: the system's, or a test double.
pub trait Clipboard: Send {
    /// A number that changes whenever the clipboard does, where the system
    /// keeps one; reading is skipped while it stays the same.
    fn serial(&mut self) -> Option<u32>;
    /// What the clipboard holds (text before an image), unless it is empty,
    /// something else, or Kyokai put it there.
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
    let c = arboard::Clipboard::new().ok()?;
    Some(Box::new(System(c)))
}

struct System(arboard::Clipboard);

impl Clipboard for System {
    fn serial(&mut self) -> Option<u32> {
        #[cfg(windows)]
        {
            // SAFETY: a plain query.
            Some(unsafe { windows::Win32::System::DataExchange::GetClipboardSequenceNumber() })
        }
        #[cfg(not(windows))]
        {
            None
        }
    }

    fn item(&mut self) -> Option<Item> {
        if owned_by_kyokai() {
            return None;
        }
        if let Ok(text) = self.0.get_text() {
            return Some(Item::Text(text));
        }
        let img = self.0.get_image().ok()?;
        let (width, height) = (
            u32::try_from(img.width).ok()?,
            u32::try_from(img.height).ok()?,
        );
        Some(Item::Image(Image {
            width,
            height,
            rgba: img.bytes.into_owned(),
        }))
    }

    fn set(&mut self, item: &Item) -> bool {
        match item {
            Item::Text(t) => self.0.set_text(t).is_ok(),
            Item::Png(_) => false,
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

/// Whether an item may be offered: some text up to MAX_TEXT, an image up
/// to MAX_PIXELS whose pixels match its size.
fn fits(item: &Item) -> bool {
    match item {
        Item::Text(t) => !t.is_empty() && t.len() <= MAX_TEXT,
        Item::Png(p) => !p.is_empty() && p.len() <= MAX_PNG * 4 / 3 + 4,
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

    /// Returns the local item to offer, when it changed and fits.
    pub fn changed(&mut self, now: Option<Item>) -> Option<Item> {
        let item = now.filter(fits)?;
        let f = fingerprint(&item);
        if self.last == Some(f) {
            return None;
        }
        self.last = Some(f);
        Some(item)
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
        }
    }
}

/// An image as the wire carries it: base64 PNG.
/// Returns: None when the PNG would be too large for one message.
pub fn to_png(image: &Image) -> Option<String> {
    let mut png_bytes = Vec::new();
    {
        let mut enc = png::Encoder::new(&mut png_bytes, image.width, image.height);
        enc.set_color(png::ColorType::Rgba);
        enc.set_depth(png::BitDepth::Eight);
        enc.set_compression(png::Compression::Fast);
        let mut w = enc.write_header().ok()?;
        w.write_image_data(&image.rgba).ok()?;
    }
    (png_bytes.len() <= MAX_PNG)
        .then(|| base64::engine::general_purpose::STANDARD.encode(&png_bytes))
}

/// An image from the wire, checked: 8-bit RGBA PNG within the limits.
pub fn from_png(b64: &str) -> Option<Image> {
    if b64.len() > MAX_PNG * 4 / 3 + 4 {
        return None;
    }
    let bytes = base64::engine::general_purpose::STANDARD.decode(b64).ok()?;
    let dec = png::Decoder::new(std::io::Cursor::new(bytes));
    let mut reader = dec.read_info().ok()?;
    let info = reader.info();
    let (width, height) = (info.width, info.height);
    if info.color_type != png::ColorType::Rgba
        || info.bit_depth != png::BitDepth::Eight
        || width as u64 * height as u64 > MAX_PIXELS
    {
        return None;
    }
    let mut rgba = vec![0; reader.output_buffer_size()?];
    let frame = reader.next_frame(&mut rgba).ok()?;
    rgba.truncate(frame.buffer_size());
    let image = Image {
        width,
        height,
        rgba,
    };
    fits(&Item::Image(image.clone())).then_some(image)
}

/// One session's clipboard sharing, until dropped.
pub struct Share {
    /// Local copies to offer to the other side.
    pub offers: mpsc::Receiver<Item>,
    /// Items from the other side, to put on the local clipboard.
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

    #[test]
    fn images_cross_as_png_and_come_back_checked() {
        let Item::Image(img) = image(64, 48, 200) else {
            unreachable!()
        };
        let wire = to_png(&img).unwrap();
        assert_eq!(from_png(&wire), Some(img));
        assert_eq!(from_png("not base64!"), None);
        assert_eq!(
            from_png(&base64::engine::general_purpose::STANDARD.encode(b"not a png")),
            None
        );
    }
}
