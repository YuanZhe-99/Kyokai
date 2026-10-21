// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/codec/zgfx/zgfx.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package zgfx

import (
	"encoding/binary"
	"math/bits"
	"strconv"

	"github.com/YuanZhe-99/Kyokai/internal/rdp/codec/internal/bitio"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const (
	descriptorSingle    = 0xE0
	descriptorMultipart = 0xE1

	compressionTypeMask = 0x0F
	compressionTypeRDP8 = 0x04
	packetCompressed    = 0x20

	historySize         = 2500000
	windowSize          = 2 * historySize
	maxSegmentSize      = 65536
	maxOutputSize       = 32 << 20
	maxRetainedOutput   = 4 << 20
	multipartHeaderSize = 7
	segmentHeaderSize   = 4
	paddingMask         = 0x07
	unencodedLengthBits = 15
	maxMatchLengthBits  = 15
	tokenPrefixBits     = 9
)

type token struct {
	prefixLength uint8
	valueBits    uint8
	match        bool
	base         uint32
}

var tokenTable = func() (table [512]token) {
	for _, spec := range []struct {
		prefixLength uint8
		prefixCode   int
		valueBits    uint8
		match        bool
		base         uint32
	}{
		{1, 0, 8, false, 0},
		{5, 17, 5, true, 0},
		{5, 18, 7, true, 32},
		{5, 19, 9, true, 160},
		{5, 20, 10, true, 672},
		{5, 21, 12, true, 1696},
		{5, 24, 0, false, 0x00},
		{5, 25, 0, false, 0x01},
		{6, 44, 14, true, 5792},
		{6, 45, 15, true, 22176},
		{6, 52, 0, false, 0x02},
		{6, 53, 0, false, 0x03},
		{6, 54, 0, false, 0xFF},
		{7, 92, 18, true, 54944},
		{7, 93, 20, true, 317088},
		{7, 110, 0, false, 0x04},
		{7, 111, 0, false, 0x05},
		{7, 112, 0, false, 0x06},
		{7, 113, 0, false, 0x07},
		{7, 114, 0, false, 0x08},
		{7, 115, 0, false, 0x09},
		{7, 116, 0, false, 0x0A},
		{7, 117, 0, false, 0x0B},
		{7, 118, 0, false, 0x3A},
		{7, 119, 0, false, 0x3B},
		{7, 120, 0, false, 0x3C},
		{7, 121, 0, false, 0x3D},
		{7, 122, 0, false, 0x3E},
		{7, 123, 0, false, 0x3F},
		{7, 124, 0, false, 0x40},
		{7, 125, 0, false, 0x80},
		{8, 188, 20, true, 1365664},
		{8, 189, 21, true, 2414240},
		{8, 252, 0, false, 0x0C},
		{8, 253, 0, false, 0x38},
		{8, 254, 0, false, 0x39},
		{8, 255, 0, false, 0x66},
		{9, 380, 22, true, 4511392},
		{9, 381, 23, true, 8705696},
		{9, 382, 24, true, 17094304},
	} {
		shift := tokenPrefixBits - spec.prefixLength
		for index := spec.prefixCode << shift; index < (spec.prefixCode+1)<<shift; index++ {
			table[index] = token{spec.prefixLength, spec.valueBits, spec.match, spec.base}
		}
	}
	return
}()

type Decompressor struct {
	window []byte
	pos    int
	output []byte
}

func NewDecompressor() *Decompressor {
	return &Decompressor{
		window: make([]byte, windowSize),
		pos:    historySize,
	}
}

func (d *Decompressor) Decompress(segmented []byte) ([]byte, error) {
	if len(segmented) == 0 {
		return nil, E.New("zgfx: empty segmented data")
	}
	switch segmented[0] {
	case descriptorSingle:
		return d.decompressSegment(segmented[1:])
	case descriptorMultipart:
		if len(segmented) < multipartHeaderSize {
			return nil, E.New("zgfx: truncated multipart header: ", len(segmented), " of 7 bytes")
		}
		segmentCount := int(binary.LittleEndian.Uint16(segmented[1:]))
		uncompressedSize := binary.LittleEndian.Uint32(segmented[3:])
		if uint64(uncompressedSize) > uint64(segmentCount)*maxSegmentSize || uncompressedSize > maxOutputSize {
			return nil, E.New("zgfx: declared size of ", uncompressedSize, " bytes is over the limit for ", segmentCount, " segments")
		}
		data := segmented[multipartHeaderSize:]
		if segmentCount*(segmentHeaderSize+1) > len(data) {
			return nil, E.New("zgfx: truncated multipart data: ", len(data), " bytes for ", segmentCount, " segments")
		}
		size := int(uncompressedSize)
		output := d.output[:0]
		for index := range segmentCount {
			if len(data) < segmentHeaderSize {
				return nil, E.New("zgfx: truncated header of segment ", index, ": ", len(data), " of 4 bytes")
			}
			segmentSize := binary.LittleEndian.Uint32(data)
			data = data[segmentHeaderSize:]
			if segmentSize > uint32(len(data)) {
				return nil, E.New("zgfx: truncated segment ", index, ": ", len(data), " of ", segmentSize, " bytes")
			}
			segment, err := d.decompressSegment(data[:segmentSize])
			if err != nil {
				return nil, err
			}
			if len(segment) > size-len(output) {
				return nil, E.New("zgfx: segments exceed the declared size of ", size, " bytes")
			}
			output = append(output, segment...)
			data = data[segmentSize:]
		}
		if len(output) != size {
			return nil, E.New("zgfx: segments total ", len(output), " of ", size, " declared bytes")
		}
		if cap(output) <= maxRetainedOutput {
			d.output = output
		}
		return output, nil
	default:
		return nil, E.New("zgfx: unknown segmented data descriptor 0x", strconv.FormatUint(uint64(segmented[0]), 16))
	}
}

func (d *Decompressor) decompressSegment(segment []byte) ([]byte, error) {
	if len(segment) == 0 {
		return nil, E.New("zgfx: empty segment")
	}
	header := segment[0]
	if header&compressionTypeMask != compressionTypeRDP8 {
		return nil, E.New("zgfx: unsupported compression type ", header&compressionTypeMask)
	}
	data := segment[1:]
	if d.pos+maxSegmentSize > len(d.window) {
		copy(d.window, d.window[d.pos-historySize:d.pos])
		d.pos = historySize
	}
	window := d.window
	start := d.pos
	end := start + maxSegmentSize
	if header&packetCompressed == 0 {
		if len(data) > maxSegmentSize {
			return nil, E.New("zgfx: uncompressed segment of ", len(data), " bytes exceeds 65536 bytes")
		}
		d.pos += copy(window[start:], data)
		return window[start:d.pos:d.pos], nil
	}
	if len(data) == 0 {
		return nil, E.New("zgfx: empty compressed segment")
	}
	padding := int(data[len(data)-1] & paddingMask)
	data = data[:len(data)-1]
	totalBits := len(data)*8 - padding
	if totalBits < 0 {
		return nil, E.New("zgfx: padding of ", padding, " bits exceeds ", len(data), " data bytes")
	}
	var (
		accumulator uint64
		available   uint
		next        int
		left        = totalBits
		pos         = start
	)
	for left > 0 {
		if available < 56 {
			accumulator, available, next = bitio.Refill(data, next, accumulator, available)
		}
		entry := tokenTable[accumulator>>(64-tokenPrefixBits)]
		if entry.prefixLength == 0 {
			return nil, E.New("zgfx: invalid token prefix at bit ", totalBits-left)
		}
		accumulator <<= entry.prefixLength
		value := entry.base + uint32(accumulator>>(64-uint(entry.valueBits)))
		accumulator <<= entry.valueBits
		consumed := uint(entry.prefixLength + entry.valueBits)
		available -= consumed
		left -= int(consumed)
		if !entry.match {
			if pos == end {
				return nil, E.New("zgfx: literal overflows the 65536-byte segment output")
			}
			window[pos] = byte(value)
			pos++
			continue
		}
		if value == 0 {
			count := int(accumulator >> (64 - unencodedLengthBits))
			left -= unencodedLengthBits
			if left < 0 {
				return nil, E.New("zgfx: truncated unencoded run length")
			}
			offset := (totalBits - left + 7) >> 3
			if count > end-pos {
				return nil, E.New("zgfx: unencoded run of ", count, " bytes at ", pos-start, " overflows the 65536-byte segment output")
			}
			if count*8 > totalBits-offset*8 {
				return nil, E.New("zgfx: truncated unencoded run: ", totalBits-offset*8, " of ", count*8, " bits")
			}
			pos += copy(window[pos:], data[offset:offset+count])
			next = offset + count
			accumulator = 0
			available = 0
			left = totalBits - next*8
			continue
		}
		if available < 2*maxMatchLengthBits+2 {
			accumulator, available, next = bitio.Refill(data, next, accumulator, available)
		}
		var count int
		if accumulator>>63 == 0 {
			count = 3
			accumulator <<= 1
			available--
			left--
		} else {
			ones := uint(bits.LeadingZeros64(^accumulator))
			if ones > maxMatchLengthBits {
				return nil, E.New("zgfx: match length prefix of ", ones, " bits exceeds 15")
			}
			accumulator <<= ones + 1
			count = 1<<(ones+1) + int(accumulator>>(63-ones))
			accumulator <<= ones + 1
			available -= 2 * (ones + 1)
			left -= int(2 * (ones + 1))
		}
		if left < 0 {
			return nil, E.New("zgfx: truncated match")
		}
		if value > historySize {
			return nil, E.New("zgfx: match distance ", value, " exceeds the ", historySize, "-byte history")
		}
		if count > end-pos {
			return nil, E.New("zgfx: match of ", count, " bytes at ", pos-start, " overflows the 65536-byte segment output")
		}
		bitio.CopyMatch(window, pos, pos-int(value), count)
		pos += count
	}
	if left < 0 {
		return nil, E.New("zgfx: truncated final token")
	}
	d.pos = pos
	return window[start:pos:pos], nil
}
