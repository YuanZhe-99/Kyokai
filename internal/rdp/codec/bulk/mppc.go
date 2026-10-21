// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/codec/bulk/mppc.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package bulk

import (
	"math/bits"

	"github.com/YuanZhe-99/Kyokai/internal/rdp/codec/internal/bitio"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const (
	mppc8KHistorySize    = 8192
	mppc64KHistorySize   = 65536
	mppc8KMaxLengthBits  = 11
	mppc64KMaxLengthBits = 14
)

type mppcDecompressor struct {
	history [mppc64KHistorySize]byte
	offset  int
}

func (m *mppcDecompressor) decompress(data []byte, flags uint8, rdp5 bool) ([]byte, error) {
	size := mppc8KHistorySize
	maxLengthOnes := mppc8KMaxLengthBits
	if rdp5 {
		size = mppc64KHistorySize
		maxLengthOnes = mppc64KMaxLengthBits
	}
	history := m.history[:size]
	if flags&packetAtFront != 0 {
		m.offset = 0
	}
	if flags&packetFlushed != 0 {
		m.offset = 0
		clear(history)
	}
	if flags&packetCompressed == 0 {
		return data, nil
	}
	if m.offset > size {
		return nil, E.New("bulk: MPPC history offset ", m.offset, " is past the ", size, "-byte history")
	}
	var (
		accumulator uint64
		available   uint
		next        int
		left        = len(data) * 8
		start       = m.offset
		position    = start
		mask        = size - 1
	)
	for left >= 8 {
		if available < 56 {
			accumulator, available, next = bitio.Refill(data, next, accumulator, available)
		}
		if accumulator>>63 == 0 {
			if position == size {
				return nil, E.New("bulk: MPPC literal overflows the ", size, "-byte history")
			}
			history[position] = byte(accumulator >> 56)
			position++
			accumulator <<= 8
			available -= 8
			left -= 8
			continue
		}
		if accumulator>>62 == 2 {
			if left < 9 {
				return nil, E.New("bulk: truncated MPPC literal: ", left, " of 9 bits")
			}
			if position == size {
				return nil, E.New("bulk: MPPC literal overflows the ", size, "-byte history")
			}
			history[position] = byte(accumulator>>55) | 0x80
			position++
			accumulator <<= 9
			available -= 9
			left -= 9
			continue
		}
		var (
			copyOffset int
			consumed   uint
		)
		if rdp5 {
			switch bits.LeadingZeros64(^accumulator) {
			case 2:
				copyOffset = int(accumulator>>45&0xFFFF) + 2368
				consumed = 19
			case 3:
				copyOffset = int(accumulator>>49&0x7FF) + 320
				consumed = 15
			case 4:
				copyOffset = int(accumulator>>51&0xFF) + 64
				consumed = 13
			default:
				copyOffset = int(accumulator >> 53 & 0x3F)
				consumed = 11
			}
		} else {
			switch bits.LeadingZeros64(^accumulator) {
			case 2:
				copyOffset = int(accumulator>>48&0x1FFF) + 320
				consumed = 16
			case 3:
				copyOffset = int(accumulator>>52&0xFF) + 64
				consumed = 12
			default:
				copyOffset = int(accumulator >> 54 & 0x3F)
				consumed = 10
			}
		}
		accumulator <<= consumed
		var count int
		if accumulator>>63 == 0 {
			count = 3
			accumulator <<= 1
			consumed++
		} else {
			ones := uint(bits.LeadingZeros64(^accumulator))
			if ones > uint(maxLengthOnes) {
				return nil, E.New("bulk: MPPC length-of-match prefix of ", ones, " bits exceeds ", maxLengthOnes)
			}
			accumulator <<= ones + 1
			count = 1<<(ones+1) + int(accumulator>>(63-ones))
			accumulator <<= ones + 1
			consumed += 2 * (ones + 1)
		}
		if int(consumed) > left {
			return nil, E.New("bulk: truncated MPPC copy tuple: ", left, " of ", consumed, " bits")
		}
		available -= consumed
		left -= int(consumed)
		if count > size-position {
			return nil, E.New("bulk: MPPC match of ", count, " bytes at ", position, " overflows the ", size, "-byte history")
		}
		source := (position - copyOffset) & mask
		if source+count <= size {
			bitio.CopyMatch(history, position, source, count)
		} else {
			for index := range count {
				history[position+index] = history[(source+index)&mask]
			}
		}
		position += count
	}
	m.offset = position
	return history[start:position:position], nil
}
