// Vendored from RustDesk (https://github.com/rustdesk/rustdesk), libs/scrap/src/dxgi/gdi.rs,
// commit 23e5bb471a040d62f0a6ad52591fb30784265b73. License: AGPL-3.0 (RustDesk); the original
// scrap code by Ram (quadrupleslap) is MIT.
// Modified by Kyokai: no Magnification (privacy mode) module; logging through the `log` crate
// instead of hbb_common; scrap's shared types and libyuv calls come from crate::shim. The bitmap
// is read top-down (a negative height), so a frame needs neither a buffer of its own nor the
// mirror and rotation passes that turned the bottom-up rows around.

use std::mem::size_of;
use winapi::{
    shared::windef::{HBITMAP, HDC},
    um::wingdi::{
        BitBlt,
        CreateCompatibleBitmap,
        CreateCompatibleDC,
        CreateDCW,
        DeleteDC,
        DeleteObject,
        GetDIBits,
        SelectObject,
        BITMAPINFO,
        BITMAPINFOHEADER,
        BI_RGB,
        CAPTUREBLT,
        DIB_RGB_COLORS, //CAPTUREBLT,
        HGDI_ERROR,
        RGBQUAD,
        SRCCOPY,
    },
};

const PIXEL_WIDTH: i32 = 4;

pub struct CapturerGDI {
    screen_dc: HDC,
    dc: HDC,
    bmp: HBITMAP,
    width: i32,
    height: i32,
}

impl CapturerGDI {
    pub fn new(name: &[u16], width: i32, height: i32) -> Result<Self, Box<dyn std::error::Error>> {
        /* or Enumerate monitors with EnumDisplayMonitors,
        https://stackoverflow.com/questions/34987695/how-can-i-get-an-hmonitor-handle-from-a-display-device-name
            #[no_mangle]
            pub extern "C" fn callback(m: HMONITOR, dc: HDC, rect: LPRECT, lp: LPARAM) -> BOOL {}
        */
        /*
        shared::windef::HMONITOR,
        winuser::{GetMonitorInfoW, GetSystemMetrics, MONITORINFOEXW},
        let mut mi: MONITORINFOEXW = std::mem::MaybeUninit::uninit().assume_init();
        mi.cbSize = size_of::<MONITORINFOEXW>() as _;
        if GetMonitorInfoW(m, &mut mi as *mut MONITORINFOEXW as _) == 0 {
            return Err(format!("Failed to get monitor information of: {:?}", m).into());
        }
        */
        unsafe {
            if name.is_empty() {
                return Err("Empty display name".into());
            }
            let screen_dc = CreateDCW(&name[0], 0 as _, 0 as _, 0 as _);
            if screen_dc.is_null() {
                return Err("Failed to create dc from monitor name".into());
            }

            // Create a Windows Bitmap, and copy the bits into it
            let dc = CreateCompatibleDC(screen_dc);
            if dc.is_null() {
                DeleteDC(screen_dc);
                return Err("Can't get a Windows display".into());
            }

            let bmp = CreateCompatibleBitmap(screen_dc, width, height);
            if bmp.is_null() {
                DeleteDC(screen_dc);
                DeleteDC(dc);
                return Err("Can't create a Windows buffer".into());
            }

            let res = SelectObject(dc, bmp as _);
            if res.is_null() || res == HGDI_ERROR {
                DeleteDC(screen_dc);
                DeleteDC(dc);
                DeleteObject(bmp as _);
                return Err("Can't select Windows buffer".into());
            }
            Ok(Self {
                screen_dc,
                dc,
                bmp,
                width,
                height,
            })
        }
    }

    pub fn frame(&self, data: &mut Vec<u8>) -> Result<(), Box<dyn std::error::Error>> {
        unsafe {
            let res = BitBlt(
                self.dc,
                0,
                0,
                self.width,
                self.height,
                self.screen_dc,
                0,
                0,
                SRCCOPY | CAPTUREBLT, // CAPTUREBLT enable layered window but also make cursor blinking
            );
            if res == 0 {
                return Err("Failed to copy screen to Windows buffer".into());
            }

            let size: usize = (self.width * PIXEL_WIDTH * self.height) as usize;
            data.resize(size, 0);

            let mut bmi = BITMAPINFO {
                bmiHeader: BITMAPINFOHEADER {
                    biSize: size_of::<BITMAPINFOHEADER>() as _,
                    biWidth: self.width as _,
                    // Top-down rows (Kyokai).
                    biHeight: -self.height as _,
                    biPlanes: 1,
                    biBitCount: (8 * PIXEL_WIDTH) as _,
                    biCompression: BI_RGB,
                    biSizeImage: (self.width * self.height * PIXEL_WIDTH) as _,
                    biXPelsPerMeter: 0,
                    biYPelsPerMeter: 0,
                    biClrUsed: 0,
                    biClrImportant: 0,
                },
                bmiColors: [RGBQUAD {
                    rgbBlue: 0,
                    rgbGreen: 0,
                    rgbRed: 0,
                    rgbReserved: 0,
                }],
            };

            // copy bits into Vec
            let res = GetDIBits(
                self.dc,
                self.bmp,
                0,
                self.height as _,
                &mut data[0] as *mut u8 as _,
                &mut bmi as _,
                DIB_RGB_COLORS,
            );
            if res == 0 {
                return Err("GetDIBits failed".into());
            }
            Ok(())
        }
    }
}

impl Drop for CapturerGDI {
    fn drop(&mut self) {
        unsafe {
            DeleteDC(self.screen_dc);
            DeleteDC(self.dc);
            DeleteObject(self.bmp as _);
        }
    }
}
