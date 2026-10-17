//! Two synthetic 1280×720 screens for the tests: a dialog with a "SAVE"
//! button, then the same dialog with the button pressed and a "SAVED"
//! status line. Nothing here touches a real desktop.

use super::desktop::Frame;

pub const WIDTH: u32 = 1280;
pub const HEIGHT: u32 = 720;
/// The button's rectangle `[x0, y0, x1, y1]`; its centre is (640, 425).
pub const BUTTON: [u32; 4] = [560, 400, 720, 450];

/// An RGB picture.
pub struct Rgb {
    pub width: u32,
    pub height: u32,
    pub pixels: Vec<u8>,
}

impl Rgb {
    fn new(width: u32, height: u32, color: [u8; 3]) -> Self {
        Rgb {
            width,
            height,
            pixels: color.repeat((width * height) as usize),
        }
    }

    fn fill(&mut self, [x0, y0, x1, y1]: [u32; 4], color: [u8; 3]) {
        for y in y0..y1.min(self.height) {
            for x in x0..x1.min(self.width) {
                let i = ((y * self.width + x) * 3) as usize;
                self.pixels[i..i + 3].copy_from_slice(&color);
            }
        }
    }

    fn text(&mut self, s: &str, x: u32, y: u32, scale: u32, color: [u8; 3]) {
        for (n, ch) in s.chars().enumerate() {
            let gx = x + n as u32 * 6 * scale;
            for (row, bits) in glyph(ch).iter().enumerate() {
                for col in 0..5 {
                    if bits & (0b10000 >> col) != 0 {
                        let px = gx + col * scale;
                        let py = y + row as u32 * scale;
                        self.fill([px, py, px + scale, py + scale], color);
                    }
                }
            }
        }
    }

    /// The picture as a captured frame (BGRA).
    pub fn frame(&self) -> Frame {
        let mut bgra = Vec::with_capacity(self.pixels.len() / 3 * 4);
        for p in self.pixels.as_chunks::<3>().0 {
            bgra.extend_from_slice(&[p[2], p[1], p[0], 255]);
        }
        Frame {
            bgra,
            width: self.width,
            height: self.height,
        }
    }
}

/// `pressed == false`: the first screen. `pressed == true`: after the click.
pub fn screen(pressed: bool) -> Rgb {
    let mut img = Rgb::new(WIDTH, HEIGHT, [40, 60, 90]); // desktop
    img.fill([0, 680, WIDTH, HEIGHT], [25, 25, 30]); // taskbar
    img.fill([340, 180, 940, 520], [235, 235, 235]); // window
    img.fill([340, 180, 940, 220], [60, 90, 160]); // title bar
    img.text("SAVE FILE", 356, 190, 3, [255, 255, 255]);
    img.text("SAVE CHANGES TO NOTES.TXT", 380, 270, 3, [30, 30, 30]);
    let [x0, y0, x1, y1] = BUTTON;
    if pressed {
        img.fill([x0, y0, x1, y1], [20, 60, 140]);
        img.text("SAVE", x0 + 40 + 2, y0 + 12 + 2, 4, [200, 200, 200]);
        img.text("SAVED", 380, 330, 3, [20, 130, 40]);
    } else {
        img.fill([x0 + 4, y0 + 4, x1 + 4, y1 + 4], [150, 150, 150]); // shadow
        img.fill([x0, y0, x1, y1], [40, 110, 220]);
        img.text("SAVE", x0 + 40, y0 + 12, 4, [255, 255, 255]);
    }
    img
}

/// 5×7 glyphs, one row per entry, high bit = leftmost column.
fn glyph(c: char) -> [u8; 7] {
    match c {
        'A' => [0x0E, 0x11, 0x11, 0x1F, 0x11, 0x11, 0x11],
        'C' => [0x0E, 0x11, 0x10, 0x10, 0x10, 0x11, 0x0E],
        'D' => [0x1E, 0x11, 0x11, 0x11, 0x11, 0x11, 0x1E],
        'E' => [0x1F, 0x10, 0x10, 0x1E, 0x10, 0x10, 0x1F],
        'F' => [0x1F, 0x10, 0x10, 0x1E, 0x10, 0x10, 0x10],
        'G' => [0x0E, 0x11, 0x10, 0x17, 0x11, 0x11, 0x0F],
        'H' => [0x11, 0x11, 0x11, 0x1F, 0x11, 0x11, 0x11],
        'I' => [0x0E, 0x04, 0x04, 0x04, 0x04, 0x04, 0x0E],
        'L' => [0x10, 0x10, 0x10, 0x10, 0x10, 0x10, 0x1F],
        'N' => [0x11, 0x19, 0x15, 0x13, 0x11, 0x11, 0x11],
        'O' => [0x0E, 0x11, 0x11, 0x11, 0x11, 0x11, 0x0E],
        'S' => [0x0F, 0x10, 0x10, 0x0E, 0x01, 0x01, 0x1E],
        'T' => [0x1F, 0x04, 0x04, 0x04, 0x04, 0x04, 0x04],
        'V' => [0x11, 0x11, 0x11, 0x11, 0x11, 0x0A, 0x04],
        'X' => [0x11, 0x11, 0x0A, 0x04, 0x0A, 0x11, 0x11],
        '.' => [0x00, 0x00, 0x00, 0x00, 0x00, 0x0C, 0x0C],
        _ => [0; 7],
    }
}
