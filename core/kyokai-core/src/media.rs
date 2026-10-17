//! Media inside a session (doc/en-us/protocol.md, "Media"): frames split
//! into binary chunks for the Noise channel and reassembled on the other
//! side, the controller's queue that the engine drains for its GUI, and the
//! host's test-pattern source.

use serde::{Deserialize, Serialize};
use std::collections::VecDeque;
use std::sync::{Condvar, Mutex};
use std::time::{Duration, Instant};

/// First plaintext byte of a media chunk (JSON control messages start with
/// `{`).
pub const CHUNK_TAG: u8 = 0x01;
/// Chunk header: tag, frame number (u32), chunk index and count (u16 each),
/// flags (u8), presentation time in microseconds (u64).
const HEADER: usize = 1 + 4 + 2 + 2 + 1 + 8;
/// The frame is a key frame (decodable on its own).
pub const KEY_FRAME: u8 = 1;
/// Largest frame accepted (a 4K frame of raw test pattern fits).
pub const MAX_FRAME_BYTES: usize = 64 << 20;

/// Packet kinds between the engine and its GUI (one per packet, first byte).
pub mod packet {
    /// engine → GUI: `{codec, width, height}` JSON, before the first frame
    /// and whenever the stream changes.
    pub const VIDEO_START: u8 = 1;
    /// engine → GUI: flags (u8), presentation time (u64 µs), frame bytes.
    pub const FRAME: u8 = 2;
    /// GUI → engine: a JSON control message for the host (input, key frame).
    pub const CONTROL: u8 = 3;
    /// engine → GUI: the host's `{delay_ms, fps, bitrate}` JSON, every second.
    pub const STATS: u8 = 4;
    /// engine → GUI: the host's AI task this controller started, as JSON.
    pub const AI_TASK: u8 = 5;
    /// engine → GUI: `{error}` JSON, why the host refused an AI request.
    pub const AI_ERROR: u8 = 6;
    /// engine → GUI: the host's pointer `{x, y, visible}` JSON.
    pub const CURSOR: u8 = 7;
    /// engine → GUI: the host's displays `{list, current}` JSON.
    pub const DISPLAYS: u8 = 8;
    /// engine → GUI: the session actions the host offers
    /// `{ctrl_alt_del, blocked, privacy}` JSON.
    pub const ACTIONS: u8 = 9;
}

/// What a video stream carries, sent before its first frame.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct VideoStart {
    /// `raw-rgba` (the test pattern) or a codec name.
    pub codec: String,
    pub width: u32,
    pub height: u32,
    /// Full colour (4:4:4); left out when false.
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub i444: bool,
}

/// Splits one frame into chunks that each fit one Noise message.
pub fn chunks(
    frame: u32,
    flags: u8,
    pts_us: u64,
    data: &[u8],
    max_plaintext: usize,
) -> Vec<Vec<u8>> {
    let room = max_plaintext - HEADER;
    let count = data.len().div_ceil(room).max(1);
    (0..count)
        .map(|i| {
            let part = &data[(i * room).min(data.len())..((i + 1) * room).min(data.len())];
            let mut c = Vec::with_capacity(HEADER + part.len());
            c.push(CHUNK_TAG);
            c.extend_from_slice(&frame.to_be_bytes());
            c.extend_from_slice(&(i as u16).to_be_bytes());
            c.extend_from_slice(&(count as u16).to_be_bytes());
            c.push(flags);
            c.extend_from_slice(&pts_us.to_be_bytes());
            c.extend_from_slice(part);
            c
        })
        .collect()
}

/// A frame put back together from its chunks.
pub struct Frame {
    pub flags: u8,
    pub pts_us: u64,
    pub data: Vec<u8>,
}

/// Collects one frame at a time; a chunk of a newer frame drops the
/// unfinished older one (latest frame wins).
#[derive(Default)]
pub struct Reassembler {
    frame: Option<u32>,
    next: u16,
    flags: u8,
    pts_us: u64,
    data: Vec<u8>,
}

impl Reassembler {
    /// Adds one chunk (starting with CHUNK_TAG).
    /// Returns: a finished frame, or None while it is incomplete.
    /// Notes: malformed, out-of-order or oversized chunks reset the frame
    /// and are otherwise ignored; they never panic.
    pub fn push(&mut self, chunk: &[u8]) -> Option<Frame> {
        if chunk.len() < HEADER || chunk[0] != CHUNK_TAG {
            return None;
        }
        let frame = u32::from_be_bytes(chunk[1..5].try_into().ok()?);
        let index = u16::from_be_bytes(chunk[5..7].try_into().ok()?);
        let count = u16::from_be_bytes(chunk[7..9].try_into().ok()?);
        let flags = chunk[9];
        let pts_us = u64::from_be_bytes(chunk[10..18].try_into().ok()?);
        let part = &chunk[HEADER..];
        if index == 0 {
            self.frame = Some(frame);
            self.next = 0;
            self.flags = flags;
            self.pts_us = pts_us;
            self.data.clear();
        }
        if self.frame != Some(frame) || index != self.next || index >= count {
            self.frame = None;
            return None;
        }
        if self.data.len() + part.len() > MAX_FRAME_BYTES {
            self.frame = None;
            return None;
        }
        self.data.extend_from_slice(part);
        self.next += 1;
        if self.next < count {
            return None;
        }
        self.frame = None;
        Some(Frame {
            flags: self.flags,
            pts_us: self.pts_us,
            data: std::mem::take(&mut self.data),
        })
    }
}

/// The controller's media for its GUI: packets waiting to be read, newest
/// frames kept when the reader falls behind.
pub struct Queue {
    inner: Mutex<(VecDeque<Vec<u8>>, bool)>,
    ready: Condvar,
}

/// Frames kept for a slow reader; older ones are dropped first.
const KEEP_FRAMES: usize = 3;

/// Why `Queue::pop` returned nothing.
#[derive(Debug, PartialEq, Eq)]
pub enum Empty {
    Timeout,
    Closed,
}

impl Default for Queue {
    fn default() -> Self {
        Queue {
            inner: Mutex::new((VecDeque::new(), false)),
            ready: Condvar::new(),
        }
    }
}

impl Queue {
    /// Adds a packet; frames beyond KEEP_FRAMES push out the oldest frame,
    /// and a newer stats, AI task or pointer packet replaces one still
    /// waiting.
    pub fn push(&self, packet: Vec<u8>) {
        let Ok(mut q) = self.inner.lock() else {
            return;
        };
        if q.1 {
            return;
        }
        if let Some(
            &kind @ (packet::STATS
            | packet::AI_TASK
            | packet::CURSOR
            | packet::DISPLAYS
            | packet::ACTIONS),
        ) = packet.first()
        {
            q.0.retain(|p| p.first() != Some(&kind));
        }
        if packet.first() == Some(&packet::FRAME) {
            let frames =
                q.0.iter()
                    .filter(|p| p.first() == Some(&packet::FRAME))
                    .count();
            if frames >= KEEP_FRAMES
                && let Some(i) = q.0.iter().position(|p| p.first() == Some(&packet::FRAME))
            {
                q.0.remove(i);
            }
        }
        q.0.push_back(packet);
        self.ready.notify_one();
    }

    /// Waits up to `timeout` for the next packet.
    pub fn pop(&self, timeout: Duration) -> Result<Vec<u8>, Empty> {
        let deadline = Instant::now() + timeout;
        let mut q = self.inner.lock().map_err(|_| Empty::Closed)?;
        loop {
            if let Some(p) = q.0.pop_front() {
                return Ok(p);
            }
            if q.1 {
                return Err(Empty::Closed);
            }
            let now = Instant::now();
            if now >= deadline {
                return Err(Empty::Timeout);
            }
            q = self
                .ready
                .wait_timeout(q, deadline - now)
                .map_err(|_| Empty::Closed)?
                .0;
        }
    }

    /// Ends the stream: readers see Closed once the queue is drained.
    pub fn close(&self) {
        if let Ok(mut q) = self.inner.lock() {
            q.1 = true;
        }
        self.ready.notify_all();
    }
}

/// The host's test-pattern source: moving colour bars, raw RGBA.
pub struct TestPattern {
    pub width: u32,
    pub height: u32,
    n: u32,
}

impl TestPattern {
    pub const FPS: u32 = 15;

    pub fn new() -> Self {
        TestPattern {
            width: 320,
            height: 180,
            n: 0,
        }
    }

    /// The stream description sent before the first frame.
    pub fn start(&self) -> VideoStart {
        VideoStart {
            codec: "raw-rgba".into(),
            width: self.width,
            height: self.height,
            i444: false,
        }
    }

    /// Draws the next frame (every frame is a key frame).
    pub fn next_frame(&mut self) -> Vec<u8> {
        let (w, h) = (self.width as usize, self.height as usize);
        let mut px = vec![0u8; w * h * 4];
        let shift = self.n as usize * 4;
        for y in 0..h {
            for x in 0..w {
                let band = ((x + shift) / 40) % 6;
                let rgb: [u8; 3] = match band {
                    0 => [229, 75, 74],
                    1 => [239, 159, 39],
                    2 => [99, 153, 34],
                    3 => [29, 158, 117],
                    4 => [55, 138, 221],
                    _ => [127, 119, 221],
                };
                let shade = 160 + (y * 95 / h) as u8;
                let i = (y * w + x) * 4;
                px[i] = (rgb[0] as u16 * shade as u16 / 255) as u8;
                px[i + 1] = (rgb[1] as u16 * shade as u16 / 255) as u8;
                px[i + 2] = (rgb[2] as u16 * shade as u16 / 255) as u8;
                px[i + 3] = 255;
            }
        }
        self.n = self.n.wrapping_add(1);
        px
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn frames_survive_chunking_and_newer_frames_win() {
        let data: Vec<u8> = (0..200_000u32).map(|i| i as u8).collect();
        let mut r = Reassembler::default();
        let a = chunks(1, KEY_FRAME, 42, &data, 65_000);
        assert!(a.len() > 1);
        let b = chunks(2, 0, 43, &data[..10], 65_000);
        // Frame 1 is cut off by frame 2: only frame 2 arrives.
        assert!(r.push(&a[0]).is_none());
        let f = r.push(&b[0]).expect("frame 2");
        assert_eq!(f.data, &data[..10]);
        for c in &a[..a.len() - 1] {
            assert!(r.push(c).is_none());
        }
        let f = r.push(a.last().unwrap()).expect("frame 1");
        assert_eq!(
            (f.flags, f.pts_us, f.data.len()),
            (KEY_FRAME, 42, data.len())
        );
        assert!(
            r.push(&[CHUNK_TAG, 0, 1]).is_none(),
            "short chunks are ignored"
        );
    }

    #[test]
    fn the_queue_keeps_the_newest_frames_and_reports_closing() {
        let q = Queue::default();
        q.push(vec![packet::VIDEO_START]);
        for i in 0..5u8 {
            q.push(vec![packet::FRAME, i]);
        }
        assert_eq!(q.pop(Duration::ZERO).unwrap(), vec![packet::VIDEO_START]);
        assert_eq!(q.pop(Duration::ZERO).unwrap(), vec![packet::FRAME, 2]);
        q.close();
        assert_eq!(q.pop(Duration::ZERO).unwrap(), vec![packet::FRAME, 3]);
        assert_eq!(q.pop(Duration::ZERO).unwrap(), vec![packet::FRAME, 4]);
        assert_eq!(q.pop(Duration::ZERO), Err(Empty::Closed));
        let open = Queue::default();
        assert_eq!(open.pop(Duration::from_millis(10)), Err(Empty::Timeout));
    }

    /// Regression: with no reader, a stats report a second piled up for as
    /// long as the session lived.
    #[test]
    fn stats_ai_tasks_and_the_pointer_keep_only_the_newest_but_errors_all_stay() {
        let q = Queue::default();
        for i in 0..100u8 {
            q.push(vec![packet::STATS, i]);
            q.push(vec![packet::AI_TASK, i]);
            q.push(vec![packet::CURSOR, i]);
        }
        q.push(vec![packet::AI_ERROR, 1]);
        q.push(vec![packet::AI_ERROR, 2]);
        let got: Vec<Vec<u8>> = std::iter::from_fn(|| q.pop(Duration::ZERO).ok()).collect();
        assert_eq!(
            got,
            vec![
                vec![packet::STATS, 99],
                vec![packet::AI_TASK, 99],
                vec![packet::CURSOR, 99],
                vec![packet::AI_ERROR, 1],
                vec![packet::AI_ERROR, 2],
            ]
        );
    }
}
