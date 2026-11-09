//! What the core takes from its app on Android (Kyokai-Mobile), where the
//! system's services it uses elsewhere are the app's: the secrets store's
//! key (the app keeps it under the Android Keystore), the clipboard (only
//! the app may read it, and only in front) and the codecs the app decodes
//! through MediaCodec. An Android device is only a controller.

use crate::clipboard::{Clipboard, Item};
use std::sync::Mutex;
use zeroize::Zeroizing;

/// The secrets store's key, from `kyokai_start`'s `master_key` (base64 of
/// 32 bytes), until the store opens with it.
static MASTER_KEY: Mutex<Option<Zeroizing<[u8; 32]>>> = Mutex::new(None);

/// What the app put on the clipboard, numbered by its changes.
static BOARD: Mutex<(u32, Option<Item>)> = Mutex::new((0, None));

/// The codecs the app decodes (`video::decodable`): set before a session.
static DECODE: Mutex<Vec<String>> = Mutex::new(Vec::new());

/// Keeps the store's key the app gave.
/// Returns: why it is unusable.
pub fn set_master_key(b64: &str) -> Result<(), String> {
    use base64::Engine;
    let bytes = Zeroizing::new(
        base64::engine::general_purpose::STANDARD
            .decode(b64)
            .map_err(|_| "bad master key")?,
    );
    let key: [u8; 32] = bytes.as_slice().try_into().map_err(|_| "bad master key")?;
    *MASTER_KEY.lock().map_err(|_| "master key poisoned")? = Some(Zeroizing::new(key));
    Ok(())
}

/// The store's key, for every time the store opens: at start and again
/// when `store.start_over` rebuilds it. It stays for the process, as the
/// open store holds it anyway.
pub fn master_key() -> Option<Zeroizing<[u8; 32]>> {
    MASTER_KEY.lock().ok()?.clone()
}

/// Puts what the person copied on this device where sessions offer it.
pub fn put_clipboard(text: String) {
    if let Ok(mut b) = BOARD.lock() {
        b.0 = b.0.wrapping_add(1);
        b.1 = Some(Item::Text(text));
    }
}

/// Sets the codecs the app decodes.
pub fn set_decoders(names: Vec<String>) {
    if let Ok(mut d) = DECODE.lock() {
        *d = names;
    }
}

/// The codecs the app decodes.
pub fn decoders() -> Vec<String> {
    DECODE.lock().map(|d| d.clone()).unwrap_or_default()
}

/// The app's clipboard: what the person copied here is offered to the
/// host; what the host's side copied is not written here (the app shows
/// it from the clipboard's media packet and copies it itself).
pub struct Board;

impl Clipboard for Board {
    fn serial(&mut self) -> Option<u32> {
        BOARD.lock().ok().map(|b| b.0)
    }

    fn item(&mut self) -> Option<Item> {
        BOARD.lock().ok()?.1.clone()
    }

    fn set(&mut self, item: &Item) -> bool {
        match item {
            Item::Text(_) => {
                if let Ok(mut b) = BOARD.lock() {
                    // Kept without a new number: it is not offered back.
                    b.1 = Some(item.clone());
                }
                true
            }
            Item::Image(_) | Item::Png(_) | Item::Files(_) => false,
        }
    }
}
