//! S3-lite spike: RustDesk's DXGI capturer (vendored in `dxgi`, decoupled
//! from the rest of RustDesk through `shim`) feeding the S2 viewer. It
//! exports the S2 ABI, so `spikes/s2-viewer` shows the live desktop:
//! captured BGRA is converted to RGBA and scaled (nearest) to w×h.
//! `s2_draw_ns` reports the time spent converting and scaling, and
//! `s3_capture_ns` the time inside the capturer.

mod dxgi;
mod shim;

pub mod common {
    pub use crate::shim::{ARGBMirror, ARGBRotate};
}
pub use shim::{would_block_if_equal, AdapterDevice, Frame, PixelBuffer, RotationMode};

use std::sync::{Mutex, OnceLock};
use std::thread;
use std::time::Instant;

const SLOTS: usize = 4;

#[derive(Clone, Copy, PartialEq)]
enum Slot {
    Free,
    Drawing,
    Ready,
    Held,
}

struct Buf(*mut u8);
// SAFETY: access to each buffer is serialized by the slot states.
unsafe impl Send for Buf {}
// SAFETY: as above; the pointer never changes.
unsafe impl Sync for Buf {}

#[derive(Default)]
struct Stats {
    produced: u64,
    convert_ns: u64,
    capture_ns: u64,
    source: (usize, usize),
}

struct Pool {
    w: usize,
    h: usize,
    bufs: Vec<Buf>,
    state: Mutex<[Slot; SLOTS]>,
    stats: Mutex<Stats>,
}

static POOL: OnceLock<Pool> = OnceLock::new();

/// Starts capturing the primary display, delivering w×h RGBA frames.
/// Returns 0, -1 when already started, -2 when no display can be captured.
#[no_mangle]
pub extern "C" fn s2_start(w: u32, h: u32, _fps: u32) -> i32 {
    let (w, h) = (w as usize, h as usize);
    let pool = Pool {
        w,
        h,
        bufs: (0..SLOTS)
            .map(|_| Buf(Box::leak(vec![255u8; w * h * 4].into_boxed_slice()).as_mut_ptr()))
            .collect(),
        state: Mutex::new([Slot::Free; SLOTS]),
        stats: Mutex::new(Stats::default()),
    };
    if POOL.set(pool).is_err() {
        return -1;
    }
    // DXGI objects stay on the thread that made them.
    let (ready, started) = std::sync::mpsc::channel();
    thread::spawn(move || {
        let pool = POOL.get().expect("started");
        let display = dxgi::Displays::new().ok().and_then(|mut d| d.next());
        let mut capturer = match display.map(dxgi::Capturer::new) {
            Some(Ok(c)) => c,
            other => {
                if let Some(Err(e)) = other {
                    eprintln!("capturer: {e}");
                }
                let _ = ready.send(false);
                return;
            }
        };
        let _ = ready.send(true);
        loop {
            let t0 = Instant::now();
            let frame = match capturer.frame(100) {
                Ok(Frame::PixelBuffer(pb)) => pb,
                Ok(_) => continue,
                Err(_) => continue, // timeout (nothing changed) or a transient error
            };
            let capture_ns = t0.elapsed().as_nanos() as u64;
            let slot = {
                let mut st = pool.state.lock().unwrap();
                match st.iter().position(|s| *s == Slot::Free) {
                    Some(i) => {
                        st[i] = Slot::Drawing;
                        i
                    }
                    None => continue,
                }
            };
            let t1 = Instant::now();
            // SAFETY: the slot is Drawing, so nobody else touches the buffer.
            let dst = unsafe { std::slice::from_raw_parts_mut(pool.bufs[slot].0, pool.w * pool.h * 4) };
            scale_bgra_to_rgba(&frame, dst, pool.w, pool.h);
            let convert_ns = t1.elapsed().as_nanos() as u64;
            {
                let mut st = pool.state.lock().unwrap();
                for s in st.iter_mut() {
                    if *s == Slot::Ready {
                        *s = Slot::Free;
                    }
                }
                st[slot] = Slot::Ready;
            }
            let mut s = pool.stats.lock().unwrap();
            s.produced += 1;
            s.convert_ns += convert_ns;
            s.capture_ns += capture_ns;
            s.source = (frame.width, frame.height);
        }
    });
    if started.recv().unwrap_or(false) {
        0
    } else {
        -2
    }
}

/// Nearest-neighbour scaling from the captured BGRA into w×h RGBA.
fn scale_bgra_to_rgba(src: &PixelBuffer, dst: &mut [u8], w: usize, h: usize) {
    let xs: Vec<usize> = (0..w).map(|x| x * src.width / w * 4).collect();
    for y in 0..h {
        let row = &src.data[(y * src.height / h) * src.stride..];
        let out = &mut dst[y * w * 4..(y + 1) * w * 4];
        for (x, &sx) in xs.iter().enumerate() {
            let p = &row[sx..sx + 4];
            out[x * 4..x * 4 + 4].copy_from_slice(&[p[2], p[1], p[0], 255]);
        }
    }
}

#[no_mangle]
pub extern "C" fn s2_slot_pixels(i: u32) -> *const u8 {
    match POOL.get() {
        Some(p) if (i as usize) < SLOTS => p.bufs[i as usize].0,
        _ => std::ptr::null(),
    }
}

#[no_mangle]
pub extern "C" fn s2_acquire() -> i32 {
    let Some(p) = POOL.get() else { return -1 };
    let mut st = p.state.lock().unwrap();
    match st.iter().position(|s| *s == Slot::Ready) {
        Some(i) => {
            st[i] = Slot::Held;
            i as i32
        }
        None => -1,
    }
}

#[no_mangle]
pub extern "C" fn s2_release(i: u32) {
    if let Some(p) = POOL.get() {
        if let Some(s) = p.state.lock().unwrap().get_mut(i as usize) {
            if *s == Slot::Held {
                *s = Slot::Free;
            }
        }
    }
}

#[no_mangle]
pub extern "C" fn s2_produced() -> u64 {
    POOL.get().map_or(0, |p| p.stats.lock().unwrap().produced)
}

/// Conversion and scaling time (the S2 viewer subtracts it as "drawing").
#[no_mangle]
pub extern "C" fn s2_draw_ns() -> u64 {
    POOL.get().map_or(0, |p| p.stats.lock().unwrap().convert_ns)
}

/// Time spent inside the capturer, including waits for the next frame.
#[no_mangle]
pub extern "C" fn s3_capture_ns() -> u64 {
    POOL.get().map_or(0, |p| p.stats.lock().unwrap().capture_ns)
}

/// Captured display size, width << 32 | height.
#[no_mangle]
pub extern "C" fn s3_source_size() -> u64 {
    POOL.get().map_or(0, |p| {
        let (w, h) = p.stats.lock().unwrap().source;
        (w as u64) << 32 | h as u64
    })
}
