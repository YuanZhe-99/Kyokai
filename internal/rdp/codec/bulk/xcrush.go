// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/codec/bulk/xcrush.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package bulk

import (
	"encoding/binary"
	"strconv"

	"github.com/YuanZhe-99/Kyokai/internal/rdp/codec/internal/bitio"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const (
	xcrushHistorySize = 2000000

	level1Compressed    = 0x01
	level1NoCompression = 0x02
	level1PacketAtFront = 0x04
)

type xcrushDecompressor struct {
	history [xcrushHistorySize]byte
	offset  int
	mppc    mppcDecompressor
}

func (x *xcrushDecompressor) decompress(data []byte, flags uint8) ([]byte, error) {
	history := x.history[:]
	if flags&packetFlushed != 0 {
		clear(history)
		x.offset = 0
	}
	if flags&packetCompressed == 0 {
		return data, nil
	}
	if len(data) < 2 {
		return nil, E.New("bulk: truncated XCRUSH header: ", len(data), " of 2 bytes")
	}
	level1Flags := data[0]
	level2Flags := data[1]
	data = data[2:]
	if level2Flags&flagsMask != 0 {
		var err error
		data, err = x.mppc.decompress(data, level2Flags, true)
		if err != nil {
			return nil, err
		}
	}
	if len(data) == 0 {
		return nil, E.New("bulk: empty XCRUSH level-1 data")
	}
	if level1Flags&level1PacketAtFront != 0 {
		x.offset = 0
	}
	start := x.offset
	position := start
	literals := data
	if level1Flags&level1NoCompression == 0 {
		if level1Flags&level1Compressed == 0 {
			return nil, E.New("bulk: XCRUSH level-1 flags 0x", strconv.FormatUint(uint64(level1Flags), 16), " mark the data neither compressed nor uncompressed")
		}
		if len(data) < 2 {
			return nil, E.New("bulk: truncated XCRUSH match count")
		}
		detailsEnd := 2 + 8*int(binary.LittleEndian.Uint16(data))
		if detailsEnd > len(data) {
			return nil, E.New("bulk: truncated XCRUSH match details: ", len(data), " of ", detailsEnd, " bytes")
		}
		details := data[2:detailsEnd]
		literals = data[detailsEnd:]
		outputOffset := 0
		for ; len(details) >= 8; details = details[8:] {
			matchLength := int(binary.LittleEndian.Uint16(details))
			matchOutputOffset := int(binary.LittleEndian.Uint16(details[2:]))
			matchHistoryOffset := binary.LittleEndian.Uint32(details[4:])
			if matchOutputOffset < outputOffset || matchHistoryOffset > xcrushHistorySize {
				return nil, E.New("bulk: invalid XCRUSH match at output offset ", matchOutputOffset, " after ", outputOffset, " from history offset ", matchHistoryOffset)
			}
			literalLength := matchOutputOffset - outputOffset
			if literalLength > 0 {
				if position+literalLength >= xcrushHistorySize {
					return nil, E.New("bulk: XCRUSH literals of ", literalLength, " bytes at ", position, " overflow the history")
				}
				if literalLength > len(literals) {
					return nil, E.New("bulk: truncated XCRUSH literals: ", len(literals), " of ", literalLength, " bytes")
				}
				position += copy(history[position:], literals[:literalLength])
				literals = literals[literalLength:]
			}
			source := int(matchHistoryOffset)
			if position+matchLength >= xcrushHistorySize || source+matchLength >= xcrushHistorySize {
				return nil, E.New("bulk: XCRUSH match of ", matchLength, " bytes from ", source, " to ", position, " overflows the history")
			}
			bitio.CopyMatch(history, position, source, matchLength)
			position += matchLength
			outputOffset = matchOutputOffset + matchLength
		}
	}
	if len(literals) > 0 {
		if position+len(literals) >= xcrushHistorySize {
			return nil, E.New("bulk: XCRUSH trailing literals of ", len(literals), " bytes at ", position, " overflow the history")
		}
		position += copy(history[position:], literals)
	}
	x.offset = position
	return history[start:position:position], nil
}
