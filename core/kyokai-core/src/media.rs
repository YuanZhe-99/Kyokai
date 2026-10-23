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

/// A plaintext that starts with this byte is one Opus packet of the
/// host's sound (the rest of it).
pub const AUDIO_TAG: u8 = 0x04;
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
    /// engine → GUI: `{text}` JSON, the host's shared AI draft.
    pub const AI_DRAFT: u8 = 10;
    /// engine → GUI: `{codecs}` JSON, what the host can send that this
    /// controller decodes, for the viewer's codec choice.
    pub const CODECS: u8 = 11;
    /// engine → GUI: one Opus packet of the host's sound.
    pub const AUDIO: u8 = 12;
    /// engine → GUI: what crossed the session's clipboard, for the viewer's
    /// history: `{from, at, text | png | files}` JSON (`from`: "controller"
    /// for what this side sent, "host" for what the host's side put here;
    /// `at`: Unix milliseconds).
    pub const CLIPBOARD: u8 = 13;
    /// Either way, for a tunnel's session: its bytes (doc/en-us/remote-services.md).
    pub const TUNNEL: u8 = 14;
    /// engine → GUI: `{paused}` JSON, whether the session refuses input as
    /// behind now (`session::InputGate`); a newer one replaces one waiting.
    pub const INPUT: u8 = 15;
}

/// What a video stream carries, sent before its first frame.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct VideoStart {
    /// `raw-rgba` (the test pattern) or a codec name.
    pub codec: String,
    pub width: u32,
    pub height: u32,
    /// Full colour (4:4:4); left out when false.
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub i444: bool,
    /// The host's encoder: `videotoolbox`, `media-foundation`, `libvpx` or
    /// `libaom` (empty for the test pattern), and whether it runs in
    /// hardware, as the encoder itself reports.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub encoder: String,
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub hardware: bool,
    /// Why the host sends a placeholder (the test pattern) instead of its
    /// screen, while it keeps trying the screen; left out for the screen.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub placeholder: Option<String>,
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

/// Reads a media chunk's frame number when it is the frame's last chunk.
/// Returns: the frame number, or None for any other chunk or a malformed one.
pub fn last_chunk(chunk: &[u8]) -> Option<u32> {
    if chunk.len() < HEADER || chunk[0] != CHUNK_TAG {
        return None;
    }
    let index = u16::from_be_bytes([chunk[5], chunk[6]]);
    let count = u16::from_be_bytes([chunk[7], chunk[8]]);
    (index.checked_add(1) == Some(count))
        .then(|| u32::from_be_bytes([chunk[1], chunk[2], chunk[3], chunk[4]]))
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

/// The controller's media for its GUI: packets waiting to be read; a
/// reader that falls behind loses the stale frames and goes on from the
/// next key frame.
pub struct Queue {
    inner: Mutex<Inner>,
    ready: Condvar,
}

/// A queue's packets and state.
#[derive(Default)]
struct Inner {
    packets: VecDeque<Vec<u8>>,
    closed: bool,
    /// Frames were dropped for a slow reader: the frames up to the next
    /// key frame cannot be decoded without them, so they are left out too.
    skipping: bool,
    /// When a reader last took a packet: a session with no viewer open
    /// drops frames too, but asks for no key frames.
    read: Option<Instant>,
}

/// How long after its last packet a reader counts as gone.
const READER_GONE: Duration = Duration::from_secs(2);

/// Frames kept for a slow reader; older ones are dropped first.
const KEEP_FRAMES: usize = 3;
/// Sound packets kept for a reader that fell behind: 200 ms; older ones go.
const KEEP_SOUND: usize = 20;
/// Clipboard packets kept for a reader that is not reading (an image's
/// packet holds up to 4 MiB); older ones go.
const KEEP_CLIPBOARD: usize = 8;

/// Why `Queue::pop` returned nothing.
#[derive(Debug, PartialEq, Eq)]
pub enum Empty {
    Timeout,
    Closed,
}

impl Default for Queue {
    fn default() -> Self {
        Queue {
            inner: Mutex::default(),
            ready: Condvar::new(),
        }
    }
}

impl Queue {
    /// Adds a packet. A newer stats, AI task, pointer, displays, actions or
    /// codecs packet replaces one still waiting. A frame that finds
    /// KEEP_FRAMES waiting finds a reader that fell behind: they all go, and
    /// so does every frame up to the next key frame (`waiting_for_key`); a
    /// key frame is kept.
    pub fn push(&self, packet: Vec<u8>) {
        let Ok(mut q) = self.inner.lock() else {
            return;
        };
        if q.closed {
            return;
        }
        match packet.first() {
            // The same stream described again (a key frame was asked for)
            // adds nothing to the description still waiting.
            Some(&packet::VIDEO_START)
                if q.packets
                    .iter()
                    .rev()
                    .find(|p| p.first() == Some(&packet::VIDEO_START))
                    == Some(&packet) =>
            {
                return;
            }
            Some(
                &kind @ (packet::STATS
                | packet::AI_TASK
                | packet::CURSOR
                | packet::DISPLAYS
                | packet::ACTIONS
                | packet::CODECS
                | packet::INPUT),
            ) => q.packets.retain(|p| p.first() != Some(&kind)),
            Some(&kind @ (packet::AUDIO | packet::CLIPBOARD)) => {
                let keep = if kind == packet::AUDIO {
                    KEEP_SOUND
                } else {
                    KEEP_CLIPBOARD
                };
                let waiting = q
                    .packets
                    .iter()
                    .filter(|p| p.first() == Some(&kind))
                    .count();
                if waiting >= keep
                    && let Some(i) = q.packets.iter().position(|p| p.first() == Some(&kind))
                {
                    q.packets.remove(i);
                }
            }
            Some(&packet::FRAME) => {
                let key = packet.get(1).is_some_and(|f| f & KEY_FRAME != 0);
                if key {
                    q.skipping = false;
                } else if q.skipping {
                    return;
                }
                let frames = q
                    .packets
                    .iter()
                    .filter(|p| p.first() == Some(&packet::FRAME))
                    .count();
                if frames >= KEEP_FRAMES {
                    // Before a frame that is not a key frame, the newest key
                    // frame waiting stays, alone: a reader that has not
                    // caught up (a viewer still opening) gets one picture it
                    // can decode instead of none (regression: the stream's
                    // first key frame went before the viewer read it).
                    let keep = if key {
                        None
                    } else {
                        q.packets.iter().rposition(|p| {
                            p.first() == Some(&packet::FRAME)
                                && p.get(1).is_some_and(|f| f & KEY_FRAME != 0)
                        })
                    };
                    let mut at = 0;
                    q.packets.retain(|p| {
                        at += 1;
                        p.first() != Some(&packet::FRAME) || Some(at - 1) == keep
                    });
                    if !key {
                        q.skipping = true;
                        return;
                    }
                }
            }
            _ => {}
        }
        q.packets.push_back(packet);
        self.ready.notify_one();
    }

    /// Whether a reader that fell behind waits for the next key frame,
    /// which the session then asks the host for: frames are left out, and
    /// a reader took a packet within READER_GONE (no viewer, no request).
    pub fn waiting_for_key(&self) -> bool {
        self.inner
            .lock()
            .is_ok_and(|q| q.skipping && q.read.is_some_and(|t| t.elapsed() < READER_GONE))
    }

    /// Waits up to `timeout` for the next packet.
    pub fn pop(&self, timeout: Duration) -> Result<Vec<u8>, Empty> {
        let deadline = Instant::now() + timeout;
        let mut q = self.inner.lock().map_err(|_| Empty::Closed)?;
        loop {
            if let Some(p) = q.packets.pop_front() {
                q.read = Some(Instant::now());
                return Ok(p);
            }
            if q.closed {
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
            q.closed = true;
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
            ..VideoStart::default()
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

    /// Sound waits at most KEEP_SOUND packets for a slow reader, the oldest
    /// going first, and never makes video frames go.
    #[test]
    fn sound_waits_at_most_200_ms() {
        let q = Queue::default();
        q.push(vec![packet::FRAME, KEY_FRAME, 0]);
        for i in 0..(KEEP_SOUND as u8 + 5) {
            q.push(vec![packet::AUDIO, i]);
        }
        let mut got = Vec::new();
        while let Ok(p) = q.pop(Duration::ZERO) {
            got.push(p);
        }
        assert_eq!(got[0][0], packet::FRAME);
        let sound: Vec<u8> = got[1..].iter().map(|p| p[1]).collect();
        assert_eq!(sound, (5..KEEP_SOUND as u8 + 5).collect::<Vec<_>>());
    }

    #[test]
    fn only_a_frame_s_last_chunk_is_acknowledged() {
        let data = vec![7u8; 150_000];
        let parts = chunks(9, 0, 1, &data, 65_000);
        assert_eq!(parts.len(), 3);
        let seen: Vec<_> = parts.iter().map(|c| last_chunk(c)).collect();
        assert_eq!(seen, [None, None, Some(9)]);
        assert_eq!(last_chunk(&chunks(4, 0, 1, b"x", 65_000)[0]), Some(4));
        assert_eq!(last_chunk(&[CHUNK_TAG, 0, 0]), None);
        assert_eq!(last_chunk(b"{\"type\":\"ping\"}"), None);
    }

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

    /// Regression: a slow reader lost the oldest frame and decoded the next
    /// ones without it, which smeared the picture until a key frame; and a
    /// reader that had not read yet lost the stream's first key frame with
    /// the frames after it, and showed nothing.
    #[test]
    fn a_slow_reader_goes_on_from_the_next_key_frame() {
        let q = Queue::default();
        let frame = |n: u8, key: bool| vec![packet::FRAME, if key { KEY_FRAME } else { 0 }, n];
        // With no reader yet (a viewer still opening), the frames after the
        // first key frame go but the key frame stays; no key frame is asked
        // for.
        for n in 0..5 {
            q.push(frame(n, n == 0));
        }
        assert!(!q.waiting_for_key());
        q.push(frame(5, true));
        assert_eq!(q.pop(Duration::ZERO).unwrap(), frame(0, true));
        assert_eq!(q.pop(Duration::ZERO).unwrap(), frame(5, true));
        // A reader that took a packet and fell behind waits for a key frame.
        q.push(vec![packet::VIDEO_START]);
        q.push(frame(0, true));
        q.push(frame(1, false));
        q.push(frame(2, false));
        assert!(!q.waiting_for_key());
        // A fourth frame finds the reader behind: the frames after the key
        // frame go, and so do the frames until the next key frame.
        q.push(frame(3, false));
        assert!(q.waiting_for_key());
        q.push(frame(4, false));
        q.push(frame(5, true));
        assert!(!q.waiting_for_key());
        q.push(frame(6, false));
        // The newest key frame waiting is the one that stays.
        q.push(frame(7, false));
        // A full queue keeps a key frame coming (it decodes on its own).
        q.push(frame(8, true));
        let got: Vec<Vec<u8>> = std::iter::from_fn(|| q.pop(Duration::ZERO).ok()).collect();
        assert_eq!(
            got,
            vec![vec![packet::VIDEO_START], frame(5, true), frame(8, true)]
        );
        assert!(!q.waiting_for_key());
    }

    /// Regression: with no viewer open, the descriptions a key frame
    /// request brings piled up for the next one.
    #[test]
    fn the_same_stream_description_waits_once() {
        let q = Queue::default();
        let start = |codec: u8| vec![packet::VIDEO_START, codec];
        for _ in 0..10 {
            q.push(start(1));
        }
        q.push(vec![packet::FRAME, KEY_FRAME, 0]);
        q.push(start(1));
        q.push(start(2));
        q.push(start(2));
        let got: Vec<Vec<u8>> = std::iter::from_fn(|| q.pop(Duration::ZERO).ok()).collect();
        assert_eq!(
            got,
            vec![start(1), vec![packet::FRAME, KEY_FRAME, 0], start(2)]
        );
    }

    #[test]
    fn the_queue_reports_closing_after_it_drained() {
        let q = Queue::default();
        q.push(vec![packet::VIDEO_START]);
        q.push(vec![packet::FRAME, KEY_FRAME, 1]);
        q.close();
        q.push(vec![packet::FRAME, 0, 2]);
        assert_eq!(q.pop(Duration::ZERO).unwrap(), vec![packet::VIDEO_START]);
        assert_eq!(
            q.pop(Duration::ZERO).unwrap(),
            vec![packet::FRAME, KEY_FRAME, 1]
        );
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
