//! The viewer's conversion of decoded pictures (NV12, planar I420 and I444,
//! RGBA) into the GUI's own buffer, at full size or at half
//! (doc/en-us/computer-use.md, "Video"). BT.601 limited range with the
//! integer arithmetic of `video::nv12_to_rgba`, so the bytes are the same;
//! sixteen pixels at a time in fixed-size arrays, which the compiler turns
//! into vector instructions (NEON, SSE2 or AVX2) without `unsafe`.

/// Pixels converted together, in fixed-size arrays the compiler vectorizes.
const L: usize = 16;

/// The widest picture converted, as the decoders open at most.
#[cfg(any(windows, target_os = "macos", test))]
const WIDEST: usize = 16384;

/// What a decoder makes of its newest picture.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Want {
    /// RGBA at the picture's size.
    Full,
    /// RGBA at half its size, each side rounded up: each pixel one chroma
    /// sample (4:2:0; the 2×2 average for 4:4:4) with the average luma of
    /// the 2×2 pixels it covers (the edge pixel twice on an odd side).
    Half,
    /// Nothing: the picture is decoded but not converted (a newer frame
    /// waits behind it).
    Skip,
}

impl Want {
    /// The bytes a w×h picture takes as wanted.
    pub fn bytes(self, w: usize, h: usize) -> usize {
        match self {
            Want::Full => w * h * 4,
            Want::Half => w.div_ceil(2) * h.div_ceil(2) * 4,
            Want::Skip => 0,
        }
    }
}

/// Where a decoder's picture goes.
enum Out<'a> {
    /// The caller's buffer (the GUI's picture), written from its start.
    Slice(&'a mut [u8]),
    /// A buffer resized to the picture (the decoders' tests).
    #[cfg(test)]
    Vec(&'a mut Vec<u8>),
}

/// A decoder's output: the buffer its newest picture is converted into,
/// as `want` says.
pub struct Sink<'a> {
    want: Want,
    out: Out<'a>,
    /// A picture did not fit the buffer.
    short: bool,
}

impl<'a> Sink<'a> {
    /// A sink writing into `out` as `want` says.
    pub fn new(want: Want, out: &'a mut [u8]) -> Sink<'a> {
        Sink {
            want,
            out: Out::Slice(out),
            short: false,
        }
    }

    /// A sink of full-size pictures into `out`, resized to each (tests).
    #[cfg(test)]
    pub fn vec(out: &'a mut Vec<u8>) -> Sink<'a> {
        Sink {
            want: Want::Full,
            out: Out::Vec(out),
            short: false,
        }
    }

    /// What is wanted of the picture (a decoder need not read one that is
    /// not converted).
    #[cfg(any(windows, target_os = "macos"))]
    pub fn want(&self) -> Want {
        self.want
    }

    /// Whether a picture did not fit the buffer (its error says so too).
    pub fn short(&self) -> bool {
        self.short
    }

    /// The room for a w×h picture as wanted.
    /// Returns: None when it is not converted (Skip), or an error when it
    /// does not fit.
    fn room(&mut self, w: usize, h: usize) -> Result<Option<&mut [u8]>, String> {
        let n = self.want.bytes(w, h);
        if self.want == Want::Skip {
            return Ok(None);
        }
        let fits = match &self.out {
            Out::Slice(s) => s.len() >= n,
            #[cfg(test)]
            Out::Vec(_) => true,
        };
        if !fits {
            self.short = true;
            return Err("the picture does not fit its buffer".into());
        }
        Ok(Some(match &mut self.out {
            Out::Slice(s) => &mut s[..n],
            #[cfg(test)]
            Out::Vec(v) => {
                v.resize(n, 0);
                &mut v[..]
            }
        }))
    }

    /// Converts NV12 planes (`y`, and `uv` with U and V interleaved, with
    /// their strides) of w×h.
    /// Returns: an error when the picture does not fit the buffer.
    #[cfg(any(windows, target_os = "macos", test))]
    pub fn nv12(
        &mut self,
        y: &[u8],
        ys: usize,
        uv: &[u8],
        uvs: usize,
        w: usize,
        h: usize,
    ) -> Result<(), String> {
        let want = self.want;
        let Some(out) = self.room(w, h)? else {
            return Ok(());
        };
        if w == 0 || h == 0 {
            return Ok(());
        }
        let cw = w.div_ceil(2);
        if cw > WIDEST / 2 {
            return Err("the picture is too wide".into());
        }
        // Each chroma row is taken apart once (a pass the compiler
        // vectorizes), then converted as I420's.
        let (mut u, mut v) = ([0u8; WIDEST / 2], [0u8; WIDEST / 2]);
        let (cu, cv) = (&mut u[..cw], &mut v[..cw]);
        let apart = |r: usize, cu: &mut [u8], cv: &mut [u8]| {
            let pairs = uv[r * uvs..][..cw * 2].as_chunks::<2>().0;
            for ((u, v), p) in cu.iter_mut().zip(cv.iter_mut()).zip(pairs) {
                (*u, *v) = (p[0], p[1]);
            }
        };
        match want {
            Want::Full => {
                for (r, lines) in out.chunks_mut(w * 8).enumerate() {
                    apart(r, cu, cv);
                    for (i, line) in lines.chunks_exact_mut(w * 4).enumerate() {
                        full_420(&y[(2 * r + i) * ys..][..w], cu, cv, line);
                    }
                }
            }
            Want::Half => {
                for (r, line) in out.chunks_exact_mut(cw * 4).enumerate() {
                    let (l0, l1) = luma_pair(y, ys, w, h, r);
                    apart(r, cu, cv);
                    half_420(l0, l1, cu, cv, w, line);
                }
            }
            Want::Skip => {}
        }
        Ok(())
    }

    /// Converts planar YUV (I420, or I444 with `i444`; planes Y, U, V with
    /// their strides) of w×h.
    /// Returns: an error when the picture does not fit the buffer.
    pub fn planar(
        &mut self,
        planes: [&[u8]; 3],
        strides: [usize; 3],
        w: usize,
        h: usize,
        i444: bool,
    ) -> Result<(), String> {
        let want = self.want;
        let Some(out) = self.room(w, h)? else {
            return Ok(());
        };
        if w == 0 || h == 0 {
            return Ok(());
        }
        let [py, pu, pv] = planes;
        let [ys, us, vs] = strides;
        let cw = if i444 { w } else { w.div_ceil(2) };
        match (want, i444) {
            (Want::Full, false) => {
                for (r, line) in out.chunks_exact_mut(w * 4).enumerate() {
                    let (cu, cv) = (row_of(pu, us, r / 2, cw), row_of(pv, vs, r / 2, cw));
                    full_420(row_of(py, ys, r, w), cu, cv, line);
                }
            }
            (Want::Full, true) => {
                for (r, line) in out.chunks_exact_mut(w * 4).enumerate() {
                    let (cu, cv) = (row_of(pu, us, r, cw), row_of(pv, vs, r, cw));
                    full_444(row_of(py, ys, r, w), cu, cv, line);
                }
            }
            (Want::Half, false) => {
                for (r, line) in out.chunks_exact_mut(w.div_ceil(2) * 4).enumerate() {
                    let (l0, l1) = luma_pair(py, ys, w, h, r);
                    let (cu, cv) = (row_of(pu, us, r, cw), row_of(pv, vs, r, cw));
                    half_420(l0, l1, cu, cv, w, line);
                }
            }
            (Want::Half, true) => {
                for (r, line) in out.chunks_exact_mut(w.div_ceil(2) * 4).enumerate() {
                    let (r0, r1) = (2 * r, (2 * r + 1).min(h - 1));
                    let rows = |p, s| (row_of(p, s, r0, w), row_of(p, s, r1, w));
                    let (l0, l1) = rows(py, ys);
                    half_444([l0, l1], [rows(pu, us), rows(pv, vs)], w, line);
                }
            }
            (Want::Skip, _) => {}
        }
        Ok(())
    }

    /// Takes a picture that is RGBA already (w×h, rows packed): copied, or
    /// each output pixel the rounded average of the 2×2 it covers.
    /// Returns: an error when the picture does not fit the buffer.
    pub fn rgba(&mut self, src: &[u8], w: usize, h: usize) -> Result<(), String> {
        let want = self.want;
        let Some(out) = self.room(w, h)? else {
            return Ok(());
        };
        match want {
            Want::Full => out.copy_from_slice(&src[..w * h * 4]),
            Want::Half if w > 0 && h > 0 => {
                let ow = w.div_ceil(2);
                for (r, line) in out.chunks_exact_mut(ow * 4).enumerate() {
                    let (r0, r1) = (2 * r, (2 * r + 1).min(h - 1));
                    let (a, b) = (&src[r0 * w * 4..][..w * 4], &src[r1 * w * 4..][..w * 4]);
                    for (x, px) in line.as_chunks_mut::<4>().0.iter_mut().enumerate() {
                        let (c0, c1) = (8 * x, 4 * (2 * x + 1).min(w - 1));
                        for (i, o) in px.iter_mut().enumerate() {
                            *o = avg4(a[c0 + i], a[c1 + i], b[c0 + i], b[c1 + i]);
                        }
                    }
                }
            }
            Want::Half | Want::Skip => {}
        }
        Ok(())
    }
}

/// Row `r` of a plane with stride `s`: its first `n` samples.
#[inline(always)]
fn row_of(p: &[u8], s: usize, r: usize, n: usize) -> &[u8] {
    &p[r * s..][..n]
}

/// The two luma rows output row `r` of a half-size picture covers (the
/// last row twice when the height is odd).
#[inline(always)]
fn luma_pair(y: &[u8], ys: usize, w: usize, h: usize, r: usize) -> (&[u8], &[u8]) {
    let (r0, r1) = (2 * r, (2 * r + 1).min(h - 1));
    (&y[r0 * ys..][..w], &y[r1 * ys..][..w])
}

/// The rounded average of four samples.
#[inline(always)]
fn avg4(a: u8, b: u8, c: u8, d: u8) -> u8 {
    ((a as u16 + b as u16 + c as u16 + d as u16 + 2) >> 2) as u8
}

/// The RGBA pixel of a luma sample and its chroma pair (BT.601 limited
/// range, as `video::nv12_to_rgba`).
#[inline(always)]
fn pixel(y: u8, u: u8, v: u8) -> [u8; 4] {
    let c = 298 * (y as i32 - 16) + 128;
    let (d, e) = (u as i32 - 128, v as i32 - 128);
    let clip = |x: i32| (x >> 8).clamp(0, 255) as u8;
    [
        clip(c + 409 * e),
        clip(c - 100 * d - 208 * e),
        clip(c + 516 * d),
        255,
    ]
}

/// Converts L pixels, each with its own chroma pair; fixed bounds, so the
/// loop becomes vector instructions.
#[inline(always)]
#[cfg(not(target_arch = "aarch64"))]
fn block(y: &[u8; L], u: &[u8; L], v: &[u8; L], out: &mut [u8; 4 * L]) {
    for (i, px) in out.as_chunks_mut::<4>().0.iter_mut().enumerate() {
        *px = pixel(y[i], u[i], v[i]);
    }
}

/// Converts L pixels with NEON, which every 64-bit ARM processor has: the
/// same integer arithmetic in 32-bit lanes (16-bit products widened), the
/// clamp as saturating narrowing, and RGBA interleaved by the store.
#[cfg(target_arch = "aarch64")]
#[inline(always)]
fn block(y: &[u8; L], u: &[u8; L], v: &[u8; L], out: &mut [u8; 4 * L]) {
    use std::arch::aarch64::*;
    // SAFETY: NEON is part of the aarch64 baseline; the loads read the
    // three L-byte arrays and the store writes the 4·L-byte array.
    unsafe {
        let (y, u, v) = (
            vld1q_u8(y.as_ptr()),
            vld1q_u8(u.as_ptr()),
            vld1q_u8(v.as_ptr()),
        );
        // Lanes as signed 16-bit: Y − 16, U − 128, V − 128 (the wrapped
        // unsigned difference read as signed is the true one).
        let wide = |a: uint8x16_t, k: u8| {
            let k = vdupq_n_u8(k);
            (
                vreinterpretq_s16_u16(vsubl_u8(vget_low_u8(a), vget_low_u8(k))),
                vreinterpretq_s16_u16(vsubl_high_u8(a, k)),
            )
        };
        let (y, d, e) = (wide(y, 16), wide(u, 128), wide(v, 128));
        let round = vdupq_n_s32(128);
        // Eight pixels: R, G, B from Y, D (U) and E (V), each clamped.
        let channels = |y: int16x8_t, d: int16x8_t, e: int16x8_t| {
            let (cl, ch) = (
                vmlal_n_s16(round, vget_low_s16(y), 298),
                vmlal_high_n_s16(round, y, 298),
            );
            let narrow = |lo: int32x4_t, hi: int32x4_t| {
                vqmovn_u16(vcombine_u16(vqshrun_n_s32::<8>(lo), vqshrun_n_s32::<8>(hi)))
            };
            let r = narrow(
                vmlal_n_s16(cl, vget_low_s16(e), 409),
                vmlal_high_n_s16(ch, e, 409),
            );
            let g = narrow(
                vmlsl_n_s16(vmlsl_n_s16(cl, vget_low_s16(d), 100), vget_low_s16(e), 208),
                vmlsl_high_n_s16(vmlsl_high_n_s16(ch, d, 100), e, 208),
            );
            let b = narrow(
                vmlal_n_s16(cl, vget_low_s16(d), 516),
                vmlal_high_n_s16(ch, d, 516),
            );
            (r, g, b)
        };
        let (r0, g0, b0) = channels(y.0, d.0, e.0);
        let (r1, g1, b1) = channels(y.1, d.1, e.1);
        let px = uint8x16x4_t(
            vcombine_u8(r0, r1),
            vcombine_u8(g0, g1),
            vcombine_u8(b0, b1),
            vdupq_n_u8(255),
        );
        vst4q_u8(out.as_mut_ptr(), px);
    }
}

/// Fills one output row: the first `whole` pixels L at a time from `blk`
/// (luma, U and V for the L pixels starting at its argument), the rest one
/// at a time from `at`.
#[inline(always)]
fn drive(
    out: &mut [u8],
    whole: usize,
    blk: impl Fn(usize) -> [[u8; L]; 3],
    at: impl Fn(usize) -> [u8; 3],
) {
    let n = whole / L;
    let (blocks, _) = out.as_chunks_mut::<{ 4 * L }>();
    for (i, o) in blocks[..n].iter_mut().enumerate() {
        let [y, u, v] = blk(i * L);
        block(&y, &u, &v, o);
    }
    for (i, px) in out[n * L * 4..]
        .as_chunks_mut::<4>()
        .0
        .iter_mut()
        .enumerate()
    {
        let [y, u, v] = at(n * L + i);
        *px = pixel(y, u, v);
    }
}

/// A fixed-size view of `s[at..at + N]`.
#[inline(always)]
fn arr<const N: usize>(s: &[u8], at: usize) -> &[u8; N] {
    s[at..at + N].try_into().expect("N bytes")
}

/// One full-size row of I420: luma `l` and the chroma rows `cu`, `cv`.
fn full_420(l: &[u8], cu: &[u8], cv: &[u8], out: &mut [u8]) {
    let blk = |x0: usize| {
        let (ys, us, vs) = (
            arr::<L>(l, x0),
            arr::<{ L / 2 }>(cu, x0 / 2),
            arr::<{ L / 2 }>(cv, x0 / 2),
        );
        let (mut u, mut v) = ([0; L], [0; L]);
        for i in 0..L {
            (u[i], v[i]) = (us[i / 2], vs[i / 2]);
        }
        [*ys, u, v]
    };
    let at = |x: usize| [l[x], cu[x / 2], cv[x / 2]];
    drive(out, l.len(), blk, at);
}

/// One full-size row of I444.
fn full_444(l: &[u8], cu: &[u8], cv: &[u8], out: &mut [u8]) {
    let blk = |x0: usize| [*arr::<L>(l, x0), *arr::<L>(cu, x0), *arr::<L>(cv, x0)];
    let at = |x: usize| [l[x], cu[x], cv[x]];
    drive(out, l.len(), blk, at);
}

/// The average luma of output pixels x0..x0+L of a half-size row, from the
/// two rows it covers (both columns inside the picture).
#[inline(always)]
fn half_luma(l0: &[u8], l1: &[u8], x0: usize) -> [u8; L] {
    let (a, b) = (arr::<{ 2 * L }>(l0, 2 * x0), arr::<{ 2 * L }>(l1, 2 * x0));
    let mut y = [0; L];
    for i in 0..L {
        y[i] = avg4(a[2 * i], a[2 * i + 1], b[2 * i], b[2 * i + 1]);
    }
    y
}

/// The average of the 2×2 samples output pixel x covers in rows `a`, `b`
/// of w samples (the last column twice when w is odd).
#[inline(always)]
fn quad(a: &[u8], b: &[u8], w: usize, x: usize) -> u8 {
    let (c0, c1) = (2 * x, (2 * x + 1).min(w - 1));
    avg4(a[c0], a[c1], b[c0], b[c1])
}

/// One half-size row of I420.
fn half_420(l0: &[u8], l1: &[u8], cu: &[u8], cv: &[u8], w: usize, out: &mut [u8]) {
    let blk = |x0: usize| [half_luma(l0, l1, x0), *arr::<L>(cu, x0), *arr::<L>(cv, x0)];
    let at = |x: usize| [quad(l0, l1, w, x), cu[x], cv[x]];
    drive(out, w / 2, blk, at);
}

/// One half-size row of I444: luma, U and V each averaged over the 2×2.
fn half_444(l: [&[u8]; 2], c: [(&[u8], &[u8]); 2], w: usize, out: &mut [u8]) {
    let [(u0, u1), (v0, v1)] = c;
    let blk = |x0: usize| {
        [
            half_luma(l[0], l[1], x0),
            half_luma(u0, u1, x0),
            half_luma(v0, v1, x0),
        ]
    };
    let at = |x: usize| {
        [
            quad(l[0], l[1], w, x),
            quad(u0, u1, w, x),
            quad(v0, v1, w, x),
        ]
    };
    drive(out, w / 2, blk, at);
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Bytes that look like nothing in particular, the same every run.
    fn noise(len: usize, seed: u32) -> Vec<u8> {
        let mut x = seed.wrapping_mul(2_654_435_761).max(1);
        (0..len)
            .map(|_| {
                x ^= x << 13;
                x ^= x >> 17;
                x ^= x << 5;
                x as u8
            })
            .collect()
    }

    /// The half-size picture by its definition, from a per-pixel sampler of
    /// (Y, U, V) at full size and whether chroma is subsampled.
    fn half_reference(
        w: usize,
        h: usize,
        at: impl Fn(usize, usize) -> [u8; 3],
        i444: bool,
    ) -> Vec<u8> {
        let (ow, oh) = (w.div_ceil(2), h.div_ceil(2));
        let mut out = Vec::new();
        for r in 0..oh {
            for x in 0..ow {
                let (r1, x1) = ((2 * r + 1).min(h - 1), (2 * x + 1).min(w - 1));
                let q = [at(2 * x, 2 * r), at(x1, 2 * r), at(2 * x, r1), at(x1, r1)];
                let avg = |i: usize| avg4(q[0][i], q[1][i], q[2][i], q[3][i]);
                let (u, v) = if i444 {
                    (avg(1), avg(2))
                } else {
                    (q[0][1], q[0][2])
                };
                out.extend_from_slice(&pixel(avg(0), u, v));
            }
        }
        out
    }

    #[test]
    fn nv12_matches_the_scalar_conversion_at_both_sizes() {
        // Odd widths and heights, below and above a block, padded strides.
        for (n, (w, h, pad)) in [
            (1usize, 1usize, 0usize),
            (7, 3, 1),
            (33, 17, 5),
            (64, 36, 0),
            (97, 55, 32),
        ]
        .into_iter()
        .enumerate()
        {
            let (stride, rows) = (w.div_ceil(2) * 2 + pad, h + 3);
            let nv12 = noise(stride * rows * 3 / 2, n as u32 + 1);
            let (y, uv) = nv12.split_at(stride * rows);
            let mut old = Vec::new();
            crate::video::nv12_to_rgba(&nv12, w, h, stride, rows, &mut old);
            let mut full = vec![0; Want::Full.bytes(w, h)];
            Sink::new(Want::Full, &mut full)
                .nv12(y, stride, uv, stride, w, h)
                .unwrap();
            assert_eq!(full, old, "{w}×{h}");
            let mut half = vec![0; Want::Half.bytes(w, h)];
            Sink::new(Want::Half, &mut half)
                .nv12(y, stride, uv, stride, w, h)
                .unwrap();
            let at = |x: usize, r: usize| {
                let c = r / 2 * stride + (x & !1);
                [y[r * stride + x], uv[c], uv[c + 1]]
            };
            assert_eq!(half, half_reference(w, h, at, false), "{w}×{h} half");
        }
    }

    #[test]
    fn planar_matches_the_scalar_conversion_at_both_sizes() {
        for i444 in [false, true] {
            for (n, (w, h, pad)) in [
                (1usize, 1usize, 0usize),
                (5, 5, 3),
                (35, 19, 0),
                (97, 55, 16),
            ]
            .into_iter()
            .enumerate()
            {
                let (cw, ch) = if i444 {
                    (w, h)
                } else {
                    (w.div_ceil(2), h.div_ceil(2))
                };
                let (ys, cs) = (w + pad, cw + pad);
                let seed = n as u32 * 3 + 10;
                let (y, u, v) = (
                    noise(ys * h, seed),
                    noise(cs * ch, seed + 1),
                    noise(cs * ch, seed + 2),
                );
                let mut old = Vec::new();
                crate::video::planar_to_rgba([&y, &u, &v], [ys, cs, cs], w, h, i444, &mut old);
                let mut full = vec![0; Want::Full.bytes(w, h)];
                Sink::new(Want::Full, &mut full)
                    .planar([&y, &u, &v], [ys, cs, cs], w, h, i444)
                    .unwrap();
                assert_eq!(full, old, "{w}×{h} i444 {i444}");
                let mut half = vec![0; Want::Half.bytes(w, h)];
                Sink::new(Want::Half, &mut half)
                    .planar([&y, &u, &v], [ys, cs, cs], w, h, i444)
                    .unwrap();
                let at = |x: usize, r: usize| {
                    let c = if i444 { r * cs + x } else { r / 2 * cs + x / 2 };
                    [y[r * ys + x], u[c], v[c]]
                };
                assert_eq!(
                    half,
                    half_reference(w, h, at, i444),
                    "{w}×{h} i444 {i444} half"
                );
            }
        }
    }

    #[test]
    fn rgba_halves_by_averaging_and_short_buffers_fail() {
        // 3×1: the odd last column counts twice.
        let src = [10, 20, 30, 255, 20, 40, 60, 255, 101, 0, 0, 255];
        let mut half = [0; 8];
        Sink::new(Want::Half, &mut half).rgba(&src, 3, 1).unwrap();
        assert_eq!(half, [15, 30, 45, 255, 101, 0, 0, 255]);
        let mut small = [0; 4];
        let mut sink = Sink::new(Want::Full, &mut small);
        assert!(sink.rgba(&src, 3, 1).is_err());
        assert!(sink.short());
        // A skipped picture needs no room and writes nothing.
        let mut none = [7u8; 0];
        let mut sink = Sink::new(Want::Skip, &mut none);
        assert!(sink.rgba(&src, 3, 1).is_ok() && !sink.short());
    }

    /// How long a 1080p picture takes, the scalar conversion against this
    /// one (run with --release --ignored --nocapture).
    #[test]
    #[ignore = "a timing, not a check"]
    fn timing_at_1080p() {
        let (w, h) = (1920, 1080);
        let nv12 = noise(w * h * 3 / 2, 3);
        let (y, uv) = nv12.split_at(w * h);
        let mut old = Vec::new();
        let mut out = vec![0; w * h * 4];
        let runs = 50;
        let t = std::time::Instant::now();
        for _ in 0..runs {
            crate::video::nv12_to_rgba(&nv12, w, h, w, h, &mut old);
        }
        let scalar = t.elapsed() / runs;
        let t = std::time::Instant::now();
        for _ in 0..runs {
            Sink::new(Want::Full, &mut out)
                .nv12(y, w, uv, w, w, h)
                .unwrap();
        }
        let full = t.elapsed() / runs;
        let t = std::time::Instant::now();
        for _ in 0..runs {
            Sink::new(Want::Half, &mut out)
                .nv12(y, w, uv, w, w, h)
                .unwrap();
        }
        let half = t.elapsed() / runs;
        eprintln!("1080p NV12: scalar {scalar:?}, full {full:?}, half {half:?}");
    }
}
