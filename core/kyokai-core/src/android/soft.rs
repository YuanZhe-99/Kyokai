//! `soft` on Android: no libvpx or libaom in the core. An Android device is
//! only a controller (Kyokai-Mobile), whose app decodes every codec through
//! the system's MediaCodec (`video::decodable`), so nothing here encodes or
//! decodes: each opens with an error, as a missing codec does elsewhere.

use crate::convert::Sink;

/// A software codec, by the name the wire and the stream description use.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Codec {
    Vp8,
    Vp9,
    Av1,
}

impl Codec {
    pub const ALL: [Codec; 3] = [Codec::Vp9, Codec::Av1, Codec::Vp8];

    pub fn name(self) -> &'static str {
        match self {
            Codec::Vp8 => "vp8",
            Codec::Vp9 => "vp9-sw",
            Codec::Av1 => "av1",
        }
    }

    pub fn from_name(name: &str) -> Option<Codec> {
        Codec::ALL.into_iter().find(|c| c.name() == name)
    }

    pub fn full_colour(self) -> bool {
        matches!(self, Codec::Vp9 | Codec::Av1)
    }
}

/// An encoded frame and whether it is a key frame.
pub type Unit = (Vec<u8>, bool);

/// Never made: `new` always fails.
pub enum Encoder {}

impl Encoder {
    pub fn new(
        _codec: Codec,
        _width: u32,
        _height: u32,
        _fps: u32,
        _bitrate: u32,
        _i444: bool,
    ) -> Result<Encoder, String> {
        Err("no software encoders on Android".into())
    }

    pub fn i444(&self) -> bool {
        match *self {}
    }

    pub fn library(&self) -> &'static str {
        match *self {}
    }

    pub fn picture(&mut self, _bgra: &[u8], _width: usize, _height: usize, _stride: usize) {
        match *self {}
    }

    pub fn has_picture(&self) -> bool {
        match *self {}
    }

    pub fn encode(&mut self, _key: bool, _at: std::time::Duration) -> Result<Vec<Unit>, String> {
        match *self {}
    }

    pub fn set_bitrate(&mut self, _bitrate: u32) {
        match *self {}
    }
}

/// Never made: `new` always fails.
pub enum Decoder {}

impl Decoder {
    pub fn library(&self) -> &'static str {
        match *self {}
    }

    pub fn new(_codec: Codec) -> Result<Decoder, String> {
        Err("no software decoders in the core on Android: the app decodes".into())
    }

    pub fn decode(&mut self, _frame: &[u8], _out: &mut Sink) -> Result<Option<(u32, u32)>, String> {
        match *self {}
    }
}
