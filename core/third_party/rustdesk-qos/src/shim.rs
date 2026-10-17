// Kyokai shim for vendored RustDesk code (AGPL-3.0, like RustDesk): what
// video_qos.rs takes from RustDesk's server module, scrap's codec
// (libs/scrap/src/common/codec.rs, commit fada664df7a294d1d1a9ca3e7cd3637069122f17,
// copied below unchanged), hbb_common's clock and RustDesk's configuration.

pub use std::collections::HashMap;

/// RustDesk's protobuf image quality (hbb_common::message_proto).
#[allow(dead_code)]
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum ImageQuality {
    NotSet = 0,
    Low = 2,
    Balanced = 3,
    Best = 4,
}

impl ImageQuality {
    pub fn value(&self) -> i32 {
        *self as i32
    }
}

/// hbb_common::get_time: milliseconds since the Unix epoch.
pub fn get_time() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis() as i64)
        .unwrap_or(0)
}

/// RustDesk's configuration: Kyokai keeps RustDesk's defaults (ABR on).
pub struct Config;

impl Config {
    pub fn get_option(_name: &str) -> String {
        String::new()
    }
}

// From scrap's codec.rs, unchanged:
pub const BR_BEST: f32 = 1.5;
pub const BR_BALANCED: f32 = 0.67;
pub const BR_SPEED: f32 = 0.5;

#[derive(Debug, Clone, Copy, PartialEq)]
pub enum Quality {
    Best,
    Balanced,
    Low,
    Custom(f32),
}

impl Default for Quality {
    fn default() -> Self {
        Self::Balanced
    }
}

impl Quality {
    pub fn is_custom(&self) -> bool {
        match self {
            Quality::Custom(_) => true,
            _ => false,
        }
    }

    pub fn ratio(&self) -> f32 {
        match self {
            Quality::Best => BR_BEST,
            Quality::Balanced => BR_BALANCED,
            Quality::Low => BR_SPEED,
            Quality::Custom(v) => *v,
        }
    }
}

pub fn base_bitrate(width: u32, height: u32) -> u32 {
    const RESOLUTION_PRESETS: &[(u32, u32, u32)] = &[
        (640, 480, 400),     // VGA, 307k pixels
        (800, 600, 500),     // SVGA, 480k pixels
        (1024, 768, 800),    // XGA, 786k pixels
        (1280, 720, 1000),   // 720p, 921k pixels
        (1366, 768, 1100),   // HD, 1049k pixels
        (1440, 900, 1300),   // WXGA+, 1296k pixels
        (1600, 900, 1500),   // HD+, 1440k pixels
        (1920, 1080, 2073),  // 1080p, 2073k pixels
        (2048, 1080, 2200),  // 2K DCI, 2211k pixels
        (2560, 1440, 3000),  // 2K QHD, 3686k pixels
        (3440, 1440, 4000),  // UWQHD, 4953k pixels
        (3840, 2160, 5000),  // 4K UHD, 8294k pixels
        (7680, 4320, 12000), // 8K UHD, 33177k pixels
    ];
    let pixels = width * height;

    let (preset_pixels, preset_bitrate) = RESOLUTION_PRESETS
        .iter()
        .map(|(w, h, bitrate)| (w * h, bitrate))
        .min_by_key(|(preset_pixels, _)| {
            if *preset_pixels >= pixels {
                preset_pixels - pixels
            } else {
                pixels - preset_pixels
            }
        })
        .unwrap_or(((1920 * 1080) as u32, &2073)); // default 1080p

    let bitrate = (*preset_bitrate as f32 * (pixels as f32 / preset_pixels as f32)).round() as u32;

    #[cfg(target_os = "android")]
    {
        let fix = crate::Display::fix_quality() as u32;
        log::debug!("Android screen, fix quality:{}", fix);
        bitrate * fix
    }
    #[cfg(not(target_os = "android"))]
    {
        bitrate
    }
}
