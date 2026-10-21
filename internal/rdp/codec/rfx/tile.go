// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/codec/rfx/tile.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package rfx

import (
	"encoding/binary"
	"image"
	"strconv"

	"github.com/YuanZhe-99/Kyokai/internal/bgra"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const (
	tileSize               = 64
	tileCoefficients       = tileSize * tileSize
	bandCount              = 10
	bandLevel2             = 3
	bandLevel3             = 6
	bandLL3                = 9
	quantSize              = 5
	blockHeaderSize        = 6
	syncMagic              = 0xCACCACCA
	syncVersion            = 0x0100
	maxRegionIntersections = 1 << 26
	maxRegionParts         = 1 << 20
)

type bandLayout struct {
	offset [bandCount]int
	length [bandCount]int
}

var (
	linearLayout = bandLayout{
		offset: [bandCount]int{0, 1024, 2048, 3072, 3328, 3584, 3840, 3904, 3968, 4032},
		length: [bandCount]int{1024, 1024, 1024, 256, 256, 256, 64, 64, 64, 64},
	}
	extrapolateLayout = bandLayout{
		offset: [bandCount]int{0, 1023, 2046, 3007, 3279, 3551, 3807, 3879, 3951, 4015},
		length: [bandCount]int{1023, 1023, 961, 272, 272, 256, 72, 72, 64, 81},
	}
)

type quantValues [bandCount]uint8

var (
	linearQuantNibbles      = [bandCount]int{8, 7, 9, 5, 4, 6, 2, 1, 3, 0}
	progressiveQuantNibbles = [bandCount]int{7, 8, 9, 4, 5, 6, 1, 2, 3, 0}
)

func nextBlock(src []byte) (blockType uint16, body, rest []byte, err error) {
	if len(src) < blockHeaderSize {
		return 0, nil, nil, E.New("rfx: truncated block header")
	}
	blockType = binary.LittleEndian.Uint16(src)
	length := binary.LittleEndian.Uint32(src[2:])
	if length < blockHeaderSize || uint64(length) > uint64(len(src)) {
		return 0, nil, nil, E.New("rfx: block 0x", strconv.FormatUint(uint64(blockType), 16), " declares ", length, " bytes, ", len(src), " remain")
	}
	return blockType, src[blockHeaderSize:length], src[length:], nil
}

func readQuant(src []byte, nibbles *[bandCount]int) quantValues {
	var q quantValues
	for band, nibble := range nibbles {
		q[band] = src[nibble/2] >> (4 * (nibble % 2)) & 0x0F
	}
	return q
}

func shiftBand(band []int16, shift uint8) {
	if shift == 0 {
		return
	}
	for i := range band {
		band[i] <<= shift
	}
}

func differentialDecode(band []int16) {
	for i := 1; i < len(band); i++ {
		band[i] += band[i-1]
	}
}

type scratch struct {
	component [3][tileCoefficients]int16
	tmp       [tileCoefficients]int16
}

var clampTable = func() (table [2048]uint8) {
	for i := range table {
		table[i] = uint8(min(max(i-1024, 0), 255))
	}
	return
}()

func writeTile(dst *bgra.Image, s *scratch, origin image.Point, r image.Rectangle) {
	width := r.Dx()
	sx := r.Min.X - origin.X
	for row := r.Min.Y; row < r.Max.Y; row++ {
		i := (row-origin.Y)*tileSize + sx
		yRow := s.component[0][i:][:width]
		cbRow := s.component[1][i:][:width]
		crRow := s.component[2][i:][:width]
		out := dst.Pix[dst.Offset(r.Min.X, row):][:width*4]
		for x := range yRow {
			y := int32(uint32(int32(yRow[x])+4096) << 16)
			cb := int32(cbRow[x])
			cr := int32(crRow[x])
			red := clampTable[((cr*91916+y)>>21+1024)&2047]
			green := clampTable[((y-cb*22527-cr*46819)>>21+1024)&2047]
			blue := clampTable[((cb*115992+y)>>21+1024)&2047]
			binary.LittleEndian.PutUint32(out[x*4:], uint32(blue)|uint32(green)<<8|uint32(red)<<16|0xFF000000)
		}
	}
}
