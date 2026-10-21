// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/codec/rfx/bits.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package rfx

import (
	"math/bits"

	"github.com/YuanZhe-99/Kyokai/internal/rdp/codec/internal/bitio"
)

func countRun(src []byte, pos int, acc uint64, have uint, invert uint64) (uint, uint64, uint, int) {
	var n uint
	for {
		if z := uint(bits.LeadingZeros64(acc ^ invert)); z < have {
			return n + z, acc << z, have - z, pos
		}
		n += have
		if pos >= len(src) {
			return n, 0, 0, pos
		}
		acc, have, pos = bitio.Refill(src, pos, 0, 0)
	}
}

type bitReader struct {
	src  []byte
	pos  int
	acc  uint64
	have uint
	left int
}

func (r *bitReader) reset(src []byte) {
	r.src = src
	r.left = len(src) * 8
	r.acc, r.have, r.pos = bitio.Refill(src, 0, 0, 0)
}

func (r *bitReader) refill() {
	if r.have < 56 {
		r.acc, r.have, r.pos = bitio.Refill(r.src, r.pos, r.acc, r.have)
	}
}

func (r *bitReader) read(n uint) uint32 {
	v := uint32(r.acc >> (64 - n))
	r.acc <<= n
	r.have -= n
	r.left -= int(n)
	return v
}

func (r *bitReader) skip(n uint) {
	r.acc <<= n
	r.have -= n
	r.left -= int(n)
}
