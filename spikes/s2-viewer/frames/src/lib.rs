//! S2 spike: a producer thread renders a moving RGBA test pattern into a
//! small pool of slots that the Go viewer shows without copying.
//!
//! A slot is free, being drawn, ready (newest finished frame) or held by
//! the viewer (the one on screen and the one being uploaded). Four slots
//! keep one free for the producer at all times.

use std::sync::{Mutex, OnceLock};
use std::thread;
use std::time::{Duration, Instant};

const SLOTS: usize = 4;

#[derive(Clone, Copy, PartialEq)]
enum Slot {
    Free,
    Drawing,
    Ready,
    Held,
}

/// A slot's pixels, leaked for the life of the process.
struct Buf(*mut u8);

// SAFETY: access to each buffer is serialized by the slot states in Pool.
unsafe impl Send for Buf {}
// SAFETY: as above; the pointer itself never changes.
unsafe impl Sync for Buf {}

struct Pool {
    w: usize,
    h: usize,
    bufs: Vec<Buf>,
    state: Mutex<[Slot; SLOTS]>,
    produced: Mutex<u64>,
    draw_ns: Mutex<u64>,
}

static POOL: OnceLock<Pool> = OnceLock::new();

/// Starts the producer for frames of w×h pixels at fps frames a second.
/// Returns 0, or -1 when already started.
#[unsafe(no_mangle)]
pub extern "C" fn s2_start(w: u32, h: u32, fps: u32) -> i32 {
    let (w, h) = (w as usize, h as usize);
    let pool = Pool {
        w,
        h,
        bufs: (0..SLOTS)
            .map(|_| Buf(Box::leak(vec![255u8; w * h * 4].into_boxed_slice()).as_mut_ptr()))
            .collect(),
        state: Mutex::new([Slot::Free; SLOTS]),
        produced: Mutex::new(0),
        draw_ns: Mutex::new(0),
    };
    if POOL.set(pool).is_err() {
        return -1;
    }
    let period = Duration::from_secs_f64(1.0 / fps.max(1) as f64);
    thread::spawn(move || {
        let pool = POOL.get().expect("started");
        let start = Instant::now();
        let mut next = Instant::now();
        loop {
            let slot = {
                let mut st = pool.state.lock().unwrap();
                match st.iter().position(|s| *s == Slot::Free) {
                    Some(i) => {
                        st[i] = Slot::Drawing;
                        i
                    }
                    None => {
                        drop(st);
                        thread::sleep(Duration::from_millis(1));
                        continue;
                    }
                }
            };
            let t = start.elapsed().as_secs_f32();
            // SAFETY: the slot is Drawing, so no other party reads or
            // writes this buffer until it is published below.
            let buf = unsafe { std::slice::from_raw_parts_mut(pool.bufs[slot].0, pool.w * pool.h * 4) };
            let t0 = Instant::now();
            draw(buf, pool.w, pool.h, t);
            *pool.draw_ns.lock().unwrap() += t0.elapsed().as_nanos() as u64;
            {
                let mut st = pool.state.lock().unwrap();
                for s in st.iter_mut() {
                    if *s == Slot::Ready {
                        *s = Slot::Free; // an unseen frame is dropped: latest wins
                    }
                }
                st[slot] = Slot::Ready;
            }
            *pool.produced.lock().unwrap() += 1;
            next += period;
            let now = Instant::now();
            if next > now {
                thread::sleep(next - now);
            } else {
                next = now;
            }
        }
    });
    0
}

/// A moving diagonal gradient with a white bar sweeping across, so torn or
/// stale frames are visible.
fn draw(buf: &mut [u8], w: usize, h: usize, t: f32) {
    let shift = (t * 120.0) as usize;
    let bar = ((t * 0.5).fract() * w as f32) as usize;
    for y in 0..h {
        let row = &mut buf[y * w * 4..(y + 1) * w * 4];
        for x in 0..w {
            let p = &mut row[x * 4..x * 4 + 4];
            if x.abs_diff(bar) < 8 {
                p.copy_from_slice(&[255, 255, 255, 255]);
            } else {
                let v = (x + y + shift) as u8;
                p.copy_from_slice(&[v, (y / 4) as u8, 255 - v, 255]);
            }
        }
    }
}

/// Returns the pixels of slot i (w×h RGBA, stride 4w), owned by the pool
/// for the life of the process; null before s2_start or for a bad index.
#[unsafe(no_mangle)]
pub extern "C" fn s2_slot_pixels(i: u32) -> *const u8 {
    match POOL.get() {
        Some(p) if (i as usize) < SLOTS => p.bufs[i as usize].0,
        _ => std::ptr::null(),
    }
}

/// Takes the newest finished frame and returns its slot, or -1 when none is
/// new. The slot stays held until s2_release.
#[unsafe(no_mangle)]
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

/// Gives a held slot back to the producer.
#[unsafe(no_mangle)]
pub extern "C" fn s2_release(i: u32) {
    if let Some(p) = POOL.get() {
        let mut st = p.state.lock().unwrap();
        if let Some(s) = st.get_mut(i as usize) {
            if *s == Slot::Held {
                *s = Slot::Free;
            }
        }
    }
}

/// Returns how many frames the producer has finished.
#[unsafe(no_mangle)]
pub extern "C" fn s2_produced() -> u64 {
    POOL.get().map_or(0, |p| *p.produced.lock().unwrap())
}

/// Returns the producer's total drawing time in nanoseconds, so a
/// measurement can subtract the test pattern's own cost.
#[unsafe(no_mangle)]
pub extern "C" fn s2_draw_ns() -> u64 {
    POOL.get().map_or(0, |p| *p.draw_ns.lock().unwrap())
}
