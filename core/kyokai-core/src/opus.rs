//! Opus for the host's sound (doc/en-us/computer-use.md, "Sound"): 48 kHz
//! stereo in 10 ms frames in Opus' low-delay mode, through the static libopus that
//! scripts/codecs.sh builds with vcpkg (linked by rustdesk-codec's build
//! script with the video codecs).

use std::ffi::c_void;
use std::ptr::NonNull;

/// The sample rate of every stream, in frames per second.
pub const RATE: usize = 48_000;
/// The channels of every stream: left and right.
pub const CHANNELS: usize = 2;
/// Frames per packet: 10 ms.
pub const FRAME: usize = RATE / 100;
/// The bitrate the host encodes at, in bit/s.
const BITRATE: i32 = 96_000;
/// Largest packet the encoder may write.
const MAX_PACKET: usize = 4000;

/// Opus' low-delay mode: no speech mode, 5 ms less look-ahead (as
/// RustDesk's sound).
const OPUS_APPLICATION_RESTRICTED_LOWDELAY: i32 = 2051;
const OPUS_SET_BITRATE_REQUEST: i32 = 4002;

unsafe extern "C" {
    fn opus_encoder_create(
        fs: i32,
        channels: i32,
        application: i32,
        error: *mut i32,
    ) -> *mut c_void;
    fn opus_encoder_ctl(st: *mut c_void, request: i32, ...) -> i32;
    fn opus_encode_float(
        st: *mut c_void,
        pcm: *const f32,
        frame_size: i32,
        data: *mut u8,
        max_data_bytes: i32,
    ) -> i32;
    fn opus_encoder_destroy(st: *mut c_void);
    fn opus_decoder_create(fs: i32, channels: i32, error: *mut i32) -> *mut c_void;
    fn opus_decode_float(
        st: *mut c_void,
        data: *const u8,
        len: i32,
        pcm: *mut f32,
        frame_size: i32,
        decode_fec: i32,
    ) -> i32;
    fn opus_decoder_destroy(st: *mut c_void);
}

/// An Opus encoder of 10 ms stereo frames.
pub struct Encoder(NonNull<c_void>);

// SAFETY: an Opus encoder keeps no thread affinity; it is used by one
// thread at a time (`&mut self`).
unsafe impl Send for Encoder {}

impl Encoder {
    /// Opens a low-delay encoder at the host's bitrate.
    /// Returns: why libopus refused it.
    pub fn new() -> Result<Encoder, String> {
        let mut err = 0;
        // SAFETY: valid arguments; `err` receives the status.
        let st = unsafe {
            opus_encoder_create(
                RATE as i32,
                CHANNELS as i32,
                OPUS_APPLICATION_RESTRICTED_LOWDELAY,
                &mut err,
            )
        };
        let st = NonNull::new(st)
            .filter(|_| err == 0)
            .ok_or(format!("opus encoder: {err}"))?;
        // SAFETY: a request libopus defines, with its one i32 argument.
        unsafe { opus_encoder_ctl(st.as_ptr(), OPUS_SET_BITRATE_REQUEST, BITRATE) };
        Ok(Encoder(st))
    }

    /// Encodes one frame of `FRAME` interleaved stereo samples.
    /// Returns: the packet.
    pub fn encode(&mut self, pcm: &[f32]) -> Result<Vec<u8>, String> {
        if pcm.len() != FRAME * CHANNELS {
            return Err("an Opus frame is 10 ms of stereo".into());
        }
        let mut out = vec![0u8; MAX_PACKET];
        // SAFETY: `pcm` holds a whole frame and `out` MAX_PACKET bytes.
        let n = unsafe {
            opus_encode_float(
                self.0.as_ptr(),
                pcm.as_ptr(),
                FRAME as i32,
                out.as_mut_ptr(),
                MAX_PACKET as i32,
            )
        };
        if n < 0 {
            return Err(format!("opus encode: {n}"));
        }
        out.truncate(n as usize);
        Ok(out)
    }
}

impl Drop for Encoder {
    fn drop(&mut self) {
        // SAFETY: the encoder was created by opus_encoder_create.
        unsafe { opus_encoder_destroy(self.0.as_ptr()) };
    }
}

/// An Opus decoder of the host's stereo packets.
pub struct Decoder(NonNull<c_void>);

// SAFETY: an Opus decoder keeps no thread affinity; it is used by one
// thread at a time (`&mut self`).
unsafe impl Send for Decoder {}

impl Decoder {
    /// Opens a decoder.
    /// Returns: why libopus refused it.
    pub fn new() -> Result<Decoder, String> {
        let mut err = 0;
        // SAFETY: valid arguments; `err` receives the status.
        let st = unsafe { opus_decoder_create(RATE as i32, CHANNELS as i32, &mut err) };
        NonNull::new(st)
            .filter(|_| err == 0)
            .map(Decoder)
            .ok_or(format!("opus decoder: {err}"))
    }

    /// Decodes one packet into `pcm` (interleaved stereo; resized).
    /// Returns: the frames decoded.
    /// Notes: a malformed packet is an error, never a panic.
    pub fn decode(&mut self, packet: &[u8], pcm: &mut Vec<f32>) -> Result<usize, String> {
        // The longest packet Opus makes is 120 ms.
        let most = RATE / 1000 * 120;
        pcm.resize(most * CHANNELS, 0.0);
        // SAFETY: `packet` is readable for its length and `pcm` holds
        // `most` frames.
        let n = unsafe {
            opus_decode_float(
                self.0.as_ptr(),
                packet.as_ptr(),
                packet.len() as i32,
                pcm.as_mut_ptr(),
                most as i32,
                0,
            )
        };
        if n < 0 {
            pcm.clear();
            return Err(format!("opus decode: {n}"));
        }
        pcm.truncate(n as usize * CHANNELS);
        Ok(n as usize)
    }
}

impl Drop for Decoder {
    fn drop(&mut self) {
        // SAFETY: the decoder was created by opus_decoder_create.
        unsafe { opus_decoder_destroy(self.0.as_ptr()) };
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A tone survives encoding and decoding: as loud after as before,
    /// and the decoder refuses garbage without panicking.
    #[test]
    fn a_tone_round_trips() {
        let mut enc = Encoder::new().unwrap();
        let mut dec = Decoder::new().unwrap();
        let mut pcm = Vec::new();
        let mut heard = 0.0f32;
        for n in 0..25 {
            let frame: Vec<f32> = (0..FRAME)
                .flat_map(|i| {
                    let t = (n * FRAME + i) as f32 / RATE as f32;
                    let s = 0.5 * (t * 440.0 * std::f32::consts::TAU).sin();
                    [s, s]
                })
                .collect();
            let packet = enc.encode(&frame).unwrap();
            assert!(packet.len() < MAX_PACKET);
            assert_eq!(dec.decode(&packet, &mut pcm).unwrap(), FRAME);
            if n > 5 {
                heard = heard.max(pcm.iter().fold(0.0, |m, s| m.max(s.abs())));
            }
        }
        assert!((0.4..0.6).contains(&heard), "peak {heard}");
        assert!(enc.encode(&[0.0; 10]).is_err());
        assert!(dec.decode(&[0xFF; 3], &mut pcm).is_err());
    }
}
