//! `opus` on Android: no libopus in the core. An Android device never
//! shares its sound (it is only a controller), and Kyokai-Mobile decodes
//! the host's through the system's MediaCodec.

pub const RATE: usize = 48_000;
pub const CHANNELS: usize = 2;
pub const FRAME: usize = RATE / 100;

/// Never made: `new` always fails.
pub enum Encoder {}

impl Encoder {
    pub fn new() -> Result<Encoder, String> {
        Err("no Opus encoder on Android".into())
    }

    pub fn encode(&mut self, _pcm: &[f32]) -> Result<Vec<u8>, String> {
        match *self {}
    }
}

/// Never made: `new` always fails.
pub enum Decoder {}

impl Decoder {
    pub fn new() -> Result<Decoder, String> {
        Err("no Opus decoder in the core on Android: the app decodes".into())
    }

    pub fn decode(&mut self, _packet: &[u8], _pcm: &mut Vec<f32>) -> Result<usize, String> {
        match *self {}
    }
}
