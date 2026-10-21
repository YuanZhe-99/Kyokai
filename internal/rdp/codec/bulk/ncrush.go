// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/codec/bulk/ncrush.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package bulk

import (
	"encoding/binary"
	"math/bits"

	"github.com/YuanZhe-99/Kyokai/internal/rdp/codec/internal/bitio"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const (
	ncrushHistorySize = 65536
	ncrushHistoryEnd  = ncrushHistorySize - 1
	ncrushAtFrontKeep = 32768
)

var lecCodeLengths = [294]uint8{
	6, 6, 6, 7, 7, 7, 7, 7, 7, 7, 7, 8, 8, 8, 8, 8, 8, 8, 9, 8, 9, 9, 9, 9, 8, 8, 9, 9, 9, 9, 9, 9,
	8, 9, 9, 10, 9, 9, 9, 9, 9, 9, 9, 10, 9, 10, 10, 10, 9, 9, 10, 9, 10, 9, 10, 9, 9, 9, 10, 10, 9, 10, 9, 9,
	8, 9, 9, 9, 9, 10, 10, 10, 9, 9, 10, 10, 10, 10, 10, 10, 9, 9, 10, 10, 10, 10, 10, 10, 10, 9, 10, 10, 10, 10, 10, 10,
	8, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 9, 10, 10, 10, 10, 10, 10, 10, 9, 10, 10, 10, 10, 10, 10, 9,
	7, 9, 9, 10, 9, 10, 10, 10, 9, 10, 10, 10, 10, 10, 10, 10, 9, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10,
	10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 13, 10, 10, 10, 10, 10, 10, 11, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10,
	9, 10, 10, 10, 10, 10, 9, 10, 10, 10, 10, 10, 9, 10, 10, 10, 9, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10,
	9, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 9, 10, 8, 9, 9, 10, 9, 10, 10, 10, 9, 10, 10, 10, 9, 9, 8, 7,
	13, 13, 7, 7, 10, 7, 7, 6, 6, 6, 6, 5, 6, 6, 6, 5, 6, 5, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6,
	8, 5, 6, 7, 7, 13,
}

var lomCodeLengths = [32]uint8{
	4, 2, 3, 4, 3, 4, 4, 5, 4, 5, 5, 6, 6, 7, 7, 8, 7, 8, 8, 9, 9, 8, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9,
}

var copyOffsetBits = [32]uint8{
	0, 0, 0, 0, 1, 1, 2, 2, 3, 3, 4, 4, 5, 5, 6, 6, 7, 7, 8, 8, 9, 9, 10, 10, 11, 11, 12, 12, 13, 13, 14, 14,
}

var copyOffsetBase = [32]uint32{
	0x1, 0x2, 0x3, 0x4, 0x5, 0x7, 0x9, 0xD, 0x11, 0x19, 0x21, 0x31, 0x41, 0x61, 0x81, 0xC1,
	0x101, 0x181, 0x201, 0x301, 0x401, 0x601, 0x801, 0xC01, 0x1001, 0x1801, 0x2001, 0x3001, 0x4001, 0x6001, 0x8001, 0xC001,
}

var lengthOfMatchBits = [30]uint8{
	0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 2, 2, 2, 2, 3, 3, 3, 3, 4, 4, 4, 4, 6, 6, 8, 8, 14, 14,
}

var lengthOfMatchBase = [30]uint32{
	0x2, 0x3, 0x4, 0x5, 0x6, 0x7, 0x8, 0x9, 0xA, 0xC, 0xE, 0x10, 0x12, 0x16, 0x1A, 0x1E,
	0x22, 0x2A, 0x32, 0x3A, 0x42, 0x52, 0x62, 0x72, 0x82, 0xC2, 0x102, 0x202, 0x2, 0x2,
}

var lecTable, lomTable = func() (lec [8192]uint16, lom [512]uint16) {
	buildDecodeTable(lec[:], lecCodeLengths[:])
	buildDecodeTable(lom[:], lomCodeLengths[:])
	return
}()

func buildDecodeTable(table []uint16, codeLengths []uint8) {
	var lengthCount [16]int
	for _, length := range codeLengths {
		lengthCount[length]++
	}
	var nextCode [16]int
	code := 0
	for length := 1; length < len(nextCode); length++ {
		code = (code + lengthCount[length-1]) << 1
		nextCode[length] = code
	}
	for symbol, length := range codeLengths {
		reversed := int(bits.Reverse16(uint16(nextCode[length])) >> (16 - length))
		nextCode[length]++
		for index := reversed; index < len(table); index += 1 << length {
			table[index] = uint16(length)<<12 | uint16(symbol)
		}
	}
}

type ncrushDecompressor struct {
	history     [ncrushHistorySize]byte
	offset      int
	offsetCache [4]int
}

func (n *ncrushDecompressor) decompress(data []byte, flags uint8) ([]byte, error) {
	history := n.history[:]
	if flags&packetFlushed != 0 {
		n.offset = 0
		clear(history)
		n.offsetCache = [4]int{}
	} else if flags&packetAtFront != 0 {
		if n.offset < ncrushAtFrontKeep {
			return nil, E.New("bulk: NCRUSH packet at front with ", n.offset, " bytes of history, ", ncrushAtFrontKeep, " needed")
		}
		copy(history, history[n.offset-ncrushAtFrontKeep:n.offset])
		clear(history[ncrushAtFrontKeep:])
		n.offset = ncrushAtFrontKeep
	}
	if flags&packetCompressed == 0 {
		return data, nil
	}
	if len(data) < 4 {
		return nil, E.New("bulk: truncated NCRUSH data: ", len(data), " of at least 4 bytes")
	}
	var (
		accumulator uint64
		available   uint
		next        int
		left        = len(data) * 8
		start       = n.offset
		position    = start
	)
	for {
		if available < 56 {
			if next+8 <= len(data) {
				accumulator |= binary.LittleEndian.Uint64(data[next:]) << available
				next += int((63 - available) >> 3)
				available |= 56
			} else {
				next, accumulator, available = refillTailLSB(data, next, accumulator, available)
			}
		}
		entry := lecTable[accumulator&0x1FFF]
		consumed := uint(entry >> 12)
		symbol := int(entry & 0xFFF)
		accumulator >>= consumed
		if symbol < 256 {
			available -= consumed
			left -= int(consumed)
			if left < 0 {
				return nil, E.New("bulk: truncated NCRUSH literal")
			}
			if position >= ncrushHistoryEnd {
				return nil, E.New("bulk: NCRUSH literal overflows the 65536-byte history")
			}
			history[position] = byte(symbol)
			position++
			continue
		}
		if symbol == 256 {
			left -= int(consumed)
			break
		}
		var copyOffset int
		cacheIndex := symbol - 289
		if cacheIndex < 0 {
			index := symbol - 257
			extraBits := copyOffsetBits[index]
			copyOffset = int(copyOffsetBase[index]) - 1 + int(accumulator&(1<<extraBits-1))
			accumulator >>= extraBits
			consumed += uint(extraBits)
		} else {
			if cacheIndex >= len(n.offsetCache) {
				return nil, E.New("bulk: NCRUSH offset cache index ", cacheIndex, " out of range")
			}
			copyOffset = n.offsetCache[cacheIndex]
		}
		entry = lomTable[accumulator&0x1FF]
		accumulator >>= entry >> 12
		consumed += uint(entry >> 12)
		lengthSymbol := int(entry & 0xFFF)
		if lengthSymbol >= 30 {
			return nil, E.New("bulk: NCRUSH length-of-match symbol ", lengthSymbol, " out of range")
		}
		extraBits := lengthOfMatchBits[lengthSymbol]
		count := int(lengthOfMatchBase[lengthSymbol]) + int(accumulator&(1<<extraBits-1))
		accumulator >>= extraBits
		consumed += uint(extraBits)
		available -= consumed
		left -= int(consumed)
		if left < 0 {
			return nil, E.New("bulk: truncated NCRUSH match")
		}
		if cacheIndex < 0 {
			n.offsetCache = [4]int{copyOffset, n.offsetCache[0], n.offsetCache[1], n.offsetCache[2]}
		} else {
			n.offsetCache[cacheIndex] = n.offsetCache[0]
			n.offsetCache[0] = copyOffset
		}
		source := (position - copyOffset) & ncrushHistoryEnd
		if source >= ncrushHistoryEnd-count || position >= ncrushHistoryEnd-count {
			return nil, E.New("bulk: NCRUSH match of ", count, " bytes from ", source, " to ", position, " overflows the 65536-byte history")
		}
		if copyOffset == 0 {
			value := history[position]
			for index := range count {
				history[position+index] = value
			}
		} else {
			bitio.CopyMatch(history, position, source, count)
		}
		position += count
	}
	if left < 0 {
		return nil, E.New("bulk: truncated NCRUSH end-of-stream code")
	}
	n.offset = position
	return history[start:position:position], nil
}

func refillTailLSB(data []byte, next int, accumulator uint64, available uint) (int, uint64, uint) {
	for available <= 56 {
		if next < len(data) {
			accumulator |= uint64(data[next]) << available
		}
		next++
		available += 8
	}
	return next, accumulator, available
}
