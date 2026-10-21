// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rfb/mvs/bitreader.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package mvs

import (
	"encoding/binary"
	"math/bits"
)

type bitReader struct {
	data     []byte
	position int
	value    uint64
	count    uint
	invalid  bool
}

func (r *bitReader) refill() {
	if r.position+8 <= len(r.data) {
		r.value |= binary.BigEndian.Uint64(r.data[r.position:]) >> r.count
		r.position += int((63 - r.count) >> 3)
		r.count |= 56
		return
	}
	for r.count <= 56 {
		var next uint64
		if r.position < len(r.data) {
			next = uint64(r.data[r.position])
		}
		r.value |= next << (56 - r.count)
		r.position++
		r.count += 8
	}
}

func (r *bitReader) read(n uint) uint32 {
	if r.count < n {
		r.refill()
	}
	result := uint32(r.value >> (64 - n))
	r.value <<= n
	r.count -= n
	return result
}

func (r *bitReader) readBit() bool {
	if r.count == 0 {
		r.refill()
	}
	result := r.value>>63 != 0
	r.value <<= 1
	r.count--
	return result
}

func (r *bitReader) peek(n uint) uint32 {
	if r.count < n {
		r.refill()
	}
	return uint32(r.value >> (64 - n))
}

func (r *bitReader) skip(n uint) {
	r.value <<= n
	r.count -= n
}

func (r *bitReader) leadingOnes() uint {
	return uint(bits.LeadingZeros64(^r.value))
}

func (r *bitReader) failed() bool {
	return r.invalid || r.position*8-int(r.count) > len(r.data)*8
}
