//! H.264 and HEVC framing (doc/en-us/computer-use.md, "Video"). The wire
//! carries Annex B: units after start codes, parameter sets in the stream,
//! as Media Foundation's encoders give and its decoders take. VideoToolbox
//! works with length-prefixed units and keeps the parameter sets in a format
//! description. These helpers convert between the two and read unit types;
//! they run on every platform, so their tests do.
#![cfg_attr(not(target_os = "macos"), allow(dead_code))]

/// The unit syntax of a stream: H.264 and HEVC headers differ.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Syntax {
    H264,
    Hevc,
}

impl Syntax {
    /// The unit's type, from its header (0 for an empty unit).
    pub fn kind(self, unit: &[u8]) -> u8 {
        let Some(&first) = unit.first() else {
            return 0;
        };
        match self {
            Syntax::H264 => first & 0x1F,
            Syntax::Hevc => (first >> 1) & 0x3F,
        }
    }

    /// Whether the unit is a parameter set: H.264's SPS and PPS, HEVC's
    /// VPS, SPS and PPS.
    pub fn parameter_set(self, unit: &[u8]) -> bool {
        let kind = self.kind(unit);
        match self {
            Syntax::H264 => matches!(kind, 7 | 8),
            Syntax::Hevc => matches!(kind, 32..=34),
        }
    }

    /// Whether the unit starts a picture that decodes on its own: H.264's
    /// IDR slice, HEVC's random access points (BLA, IDR, CRA).
    pub fn key(self, unit: &[u8]) -> bool {
        let kind = self.kind(unit);
        match self {
            Syntax::H264 => kind == 5,
            Syntax::Hevc => (16..=21).contains(&kind),
        }
    }

    /// Whether the unit is an access unit delimiter, which the
    /// length-prefixed form leaves out.
    pub fn delimiter(self, unit: &[u8]) -> bool {
        let kind = self.kind(unit);
        match self {
            Syntax::H264 => kind == 9,
            Syntax::Hevc => kind == 35,
        }
    }
}

/// The units of an Annex B stream, without their start codes (3 or 4
/// bytes) or trailing zero bytes; bytes before the first start code are
/// skipped. It borrows the stream and allocates nothing.
pub fn units(stream: &[u8]) -> Units<'_> {
    let rest = match start_code(stream) {
        Some(at) => &stream[at + 3..],
        None => &[],
    };
    Units { rest }
}

/// The units of an Annex B stream (`units`).
pub struct Units<'a> {
    rest: &'a [u8],
}

impl<'a> Iterator for Units<'a> {
    type Item = &'a [u8];

    fn next(&mut self) -> Option<&'a [u8]> {
        while !self.rest.is_empty() {
            let (unit, rest) = match start_code(self.rest) {
                Some(at) => (&self.rest[..at], &self.rest[at + 3..]),
                None => (self.rest, &[][..]),
            };
            self.rest = rest;
            // A 4-byte start code's leading zero and any trailing zeros end
            // the unit before it; a unit never ends in a zero byte.
            let end = unit.iter().rposition(|&b| b != 0).map_or(0, |i| i + 1);
            if end > 0 {
                return Some(&unit[..end]);
            }
        }
        None
    }
}

/// Where the first `00 00 01` starts, if anywhere.
fn start_code(data: &[u8]) -> Option<usize> {
    let mut i = 0;
    while i + 3 <= data.len() {
        // A start code ends with 1 after two zeros: look at the third byte
        // first, which skips most positions at once.
        match data[i + 2] {
            1 if data[i] == 0 && data[i + 1] == 0 => return Some(i),
            0 => i += 1,
            _ => i += 3,
        }
    }
    None
}

/// The units of a length-prefixed buffer (`len_size` bytes, big-endian,
/// before each unit); it stops at the first length that runs past the end.
pub fn length_prefixed(data: &[u8], len_size: usize) -> impl Iterator<Item = &[u8]> {
    let mut rest = data;
    std::iter::from_fn(move || {
        if !(1..=4).contains(&len_size) || rest.len() < len_size {
            return None;
        }
        let len = rest[..len_size]
            .iter()
            .fold(0usize, |n, &b| (n << 8) | b as usize);
        let unit = rest.get(len_size..len_size + len)?;
        rest = &rest[len_size + len..];
        Some(unit)
    })
    .filter(|u| !u.is_empty())
}

/// Appends a unit in Annex B form (a 4-byte start code before it).
pub fn append_annexb(unit: &[u8], out: &mut Vec<u8>) {
    out.extend_from_slice(&[0, 0, 0, 1]);
    out.extend_from_slice(unit);
}

/// Appends a unit with its length before it (4 bytes, big-endian).
pub fn append_length_prefixed(unit: &[u8], out: &mut Vec<u8>) {
    out.extend_from_slice(&(unit.len() as u32).to_be_bytes());
    out.extend_from_slice(unit);
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn annexb_streams_split_into_units() {
        let cases: &[(&[u8], &[&[u8]])] = &[
            // 4- and 3-byte start codes.
            (
                &[0, 0, 0, 1, 0x67, 1, 2, 0, 0, 1, 0x68, 3],
                &[&[0x67, 1, 2], &[0x68, 3]],
            ),
            // Trailing zeros belong to no unit; junk before the first start
            // code is skipped.
            (
                &[9, 9, 0, 0, 1, 0x65, 7, 0, 0, 0, 0, 1, 0x41, 8, 0, 0],
                &[&[0x65, 7], &[0x41, 8]],
            ),
            // Zeros inside a unit stay (00 00 03 is emulation prevention).
            (&[0, 0, 1, 0x41, 0, 0, 3, 0, 5], &[&[0x41, 0, 0, 3, 0, 5]]),
            // Empty units and streams without a start code give nothing.
            (&[0, 0, 1, 0, 0, 1, 0x09, 0xF0], &[&[0x09, 0xF0]]),
            (&[1, 2, 3], &[]),
            (&[], &[]),
        ];
        for (stream, want) in cases {
            let got: Vec<&[u8]> = units(stream).collect();
            assert_eq!(&got, want, "{stream:02x?}");
        }
    }

    #[test]
    fn length_prefixed_round_trips_and_stops_at_a_bad_length() {
        let units_in: [&[u8]; 3] = [&[0x67, 1], &[0x68, 2, 3], &[0x65, 4, 5, 6]];
        let mut avcc = Vec::new();
        for u in units_in {
            append_length_prefixed(u, &mut avcc);
        }
        assert_eq!(length_prefixed(&avcc, 4).collect::<Vec<_>>(), units_in);
        let mut annexb = Vec::new();
        for u in length_prefixed(&avcc, 4) {
            append_annexb(u, &mut annexb);
        }
        assert_eq!(units(&annexb).collect::<Vec<_>>(), units_in);
        // A length past the end ends the list there.
        avcc.extend_from_slice(&[0, 0, 0, 9, 1]);
        assert_eq!(length_prefixed(&avcc, 4).count(), 3);
        // Two-byte lengths, as some streams use.
        assert_eq!(
            length_prefixed(&[0, 2, 0x41, 1, 0, 1, 0x41], 2).collect::<Vec<_>>(),
            [&[0x41, 1][..], &[0x41]]
        );
    }

    #[test]
    fn unit_types_by_syntax() {
        use Syntax::*;
        // (syntax, first header byte, parameter set, key, delimiter)
        let cases = [
            (H264, 0x67, true, false, false),  // SPS
            (H264, 0x68, true, false, false),  // PPS
            (H264, 0x65, false, true, false),  // IDR slice
            (H264, 0x41, false, false, false), // other slice
            (H264, 0x09, false, false, true),  // delimiter
            (Hevc, 0x40, true, false, false),  // VPS (32)
            (Hevc, 0x42, true, false, false),  // SPS (33)
            (Hevc, 0x44, true, false, false),  // PPS (34)
            (Hevc, 0x26, false, true, false),  // IDR_W_RADL (19)
            (Hevc, 0x2A, false, true, false),  // CRA (21)
            (Hevc, 0x02, false, false, false), // TRAIL_R (1)
            (Hevc, 0x46, false, false, true),  // delimiter (35)
        ];
        for (syntax, header, set, key, delimiter) in cases {
            let unit = [header, 1];
            assert_eq!(syntax.parameter_set(&unit), set, "{syntax:?} {header:#x}");
            assert_eq!(syntax.key(&unit), key, "{syntax:?} {header:#x}");
            assert_eq!(syntax.delimiter(&unit), delimiter, "{syntax:?} {header:#x}");
        }
        assert_eq!(H264.kind(&[]), 0);
    }
}
