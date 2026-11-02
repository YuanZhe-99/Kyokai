//! The element list's second layer (doc/en-us/computer-use.md, "Element
//! list"): where the system's accessibility tree gives little or nothing (a
//! Remote Desktop or VNC window, a lent pane, a game), the text lines the
//! system's own OCR reads in the screenshot's frame, listed as `text`
//! elements. Windows uses Windows.Media.Ocr in the user's languages; macOS
//! uses Vision; Linux has none.

use super::desktop::Frame;
use super::elements::Element;

/// Fewer listed elements than this and the OCR lines are added.
pub const FEW: usize = 3;

/// The role OCR lines take in the list.
pub const ROLE: &str = "text";

/// The text lines the system reads in a frame, each with its bounds in the
/// frame's pixels, top to bottom.
/// Returns: None where the system has no OCR or it failed.
/// Notes: takes a few hundred milliseconds on a full screen; call it off the
/// async threads.
pub fn lines(frame: &Frame) -> Option<Vec<Element>> {
    #[cfg(windows)]
    return windows_ocr::lines(frame);
    #[cfg(target_os = "macos")]
    return crate::mac::ocr_lines(frame);
    #[cfg(not(any(windows, target_os = "macos")))]
    {
        let _ = frame;
        None
    }
}

#[cfg(windows)]
mod windows_ocr {
    use super::*;
    use windows::Graphics::Imaging::{BitmapAlphaMode, BitmapPixelFormat, SoftwareBitmap};
    use windows::Media::Ocr::OcrEngine;
    use windows::Security::Cryptography::CryptographicBuffer;
    use windows::Win32::System::Com::{COINIT_MULTITHREADED, CoInitializeEx};

    /// Windows.Media.Ocr over the frame, in the user's languages.
    pub fn lines(frame: &Frame) -> Option<Vec<Element>> {
        // SAFETY: joins this thread to the multithreaded apartment, as the
        // crate's other COM callers do; WinRT needs one.
        let _ = unsafe { CoInitializeEx(None, COINIT_MULTITHREADED) };
        let engine = OcrEngine::TryCreateFromUserProfileLanguages().ok()?;
        let max = OcrEngine::MaxImageDimension().ok()?;
        if frame.width > max || frame.height > max || frame.width == 0 || frame.height == 0 {
            return None;
        }
        let buffer = CryptographicBuffer::CreateFromByteArray(&frame.bgra).ok()?;
        let bitmap = SoftwareBitmap::CreateCopyWithAlphaFromBuffer(
            &buffer,
            BitmapPixelFormat::Bgra8,
            frame.width as i32,
            frame.height as i32,
            BitmapAlphaMode::Ignore,
        )
        .ok()?;
        let result = engine.RecognizeAsync(&bitmap).ok()?.join().ok()?;
        let mut out = Vec::new();
        for line in result.Lines().ok()? {
            let text = line.Text().map(|t| t.to_string()).unwrap_or_default();
            if text.trim().is_empty() {
                continue;
            }
            let mut rect: Option<[f32; 4]> = None;
            for word in line.Words().ok()? {
                let Ok(r) = word.BoundingRect() else { continue };
                let w = [r.X, r.Y, r.X + r.Width, r.Y + r.Height];
                rect = Some(match rect {
                    None => w,
                    Some(a) => [
                        a[0].min(w[0]),
                        a[1].min(w[1]),
                        a[2].max(w[2]),
                        a[3].max(w[3]),
                    ],
                });
            }
            let Some(r) = rect else { continue };
            out.push(Element {
                role: ROLE.into(),
                name: text,
                text: String::new(),
                rect: [
                    r[0] as i32,
                    r[1] as i32,
                    r[2].ceil() as i32,
                    r[3].ceil() as i32,
                ],
            });
        }
        Some(out)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// The system reads the test screen's lines, where it can, with bounds
    /// where they were drawn: one lies on the SAVE button (its blocky pixel
    /// font may read as other letters; the bounds are what the list needs).
    #[test]
    #[cfg(windows)]
    fn ocr_reads_a_drawn_line() {
        let frame = super::super::screens::screen(false).frame();
        let Some(lines) = lines(&frame) else {
            eprintln!("no OCR on this system");
            return;
        };
        let b = super::super::screens::BUTTON;
        let on_button = lines.iter().any(|l| {
            let (cx, cy) = ((l.rect[0] + l.rect[2]) / 2, (l.rect[1] + l.rect[3]) / 2);
            !l.name.trim().is_empty()
                && (b[0] as i32..b[2] as i32).contains(&cx)
                && (b[1] as i32..b[3] as i32).contains(&cy)
        });
        assert!(on_button, "no line on the button {b:?}: {lines:?}");
    }
}
