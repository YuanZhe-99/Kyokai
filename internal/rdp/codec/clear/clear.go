// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/codec/clear/clear.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package clear

import (
	"encoding/binary"
	"image"
	"math/bits"

	"github.com/YuanZhe-99/Kyokai/internal/bgra"
	"github.com/YuanZhe-99/Kyokai/internal/rdp/codec/nsc"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const (
	flagGlyphIndex = 0x01
	flagGlyphHit   = 0x02
	flagCacheReset = 0x04

	glyphCacheSize     = 4000
	glyphMaxPixels     = 1024
	vbarCacheSize      = 32768
	shortVBarCacheSize = 16384
	vbarMaxHeight      = 52
	vbarStride         = vbarMaxHeight * 3

	subcodecUncompressed = 0
	subcodecNSCodec      = 1
	subcodecRLEX         = 2

	headerSize          = 2
	glyphIndexSize      = 2
	compositeHeaderSize = 12
	bandHeaderSize      = 11
	vbarHeaderSize      = 2
	subcodecHeaderSize  = 13
	residualHeaderSize  = 4
	rlexSegmentSize     = 2
	maxPaletteCount     = 127

	runLength16 = 0xFF
	runLength32 = 0xFFFF

	vbarKindShift      = 14
	shortVBarCacheMiss = 0
	shortVBarCacheHit  = 1
	vbarIndexMask      = 0x7FFF
	shortVBarIndexMask = 0x3FFF
	yOnMask            = 0xFF
	yOffShift          = 8
	yOffMask           = 0x3F
)

type Decoder struct {
	nsc              nsc.Decoder
	glyphs           [glyphCacheSize][]byte
	glyph            bgra.Image
	vbarPixels       []byte
	vbarHeights      []uint8
	vbarCursor       int
	shortVBarPixels  []byte
	shortVBarHeights []uint8
	shortVBarCursor  int
}

type linearWriter struct {
	image *bgra.Image
	width int
	x     int
	y     int
}

func NewDecoder() *Decoder {
	return &Decoder{
		vbarPixels:       make([]byte, vbarCacheSize*vbarStride),
		vbarHeights:      make([]uint8, vbarCacheSize),
		shortVBarPixels:  make([]byte, shortVBarCacheSize*vbarStride),
		shortVBarHeights: make([]uint8, shortVBarCacheSize),
	}
}

func (d *Decoder) Decode(src []byte, width, height int, dst *bgra.Image) error {
	if len(src) < headerSize {
		return E.New("clear: truncated header: ", len(src), " of 2 bytes")
	}
	flags := src[0]
	src = src[headerSize:]
	if flags&flagGlyphHit != 0 && flags&flagGlyphIndex == 0 {
		return E.New("clear: glyph hit without glyph index")
	}
	if flags&flagCacheReset != 0 {
		d.vbarCursor = 0
		d.shortVBarCursor = 0
	}
	glyphIndex := -1
	if flags&flagGlyphIndex != 0 {
		if len(src) < glyphIndexSize {
			return E.New("clear: truncated glyph index: ", len(src), " of 2 bytes")
		}
		glyphIndex = int(binary.LittleEndian.Uint16(src))
		src = src[glyphIndexSize:]
		if glyphIndex >= glyphCacheSize {
			return E.New("clear: glyph index ", glyphIndex, " out of range")
		}
	}
	if flags&flagGlyphHit != 0 {
		glyph := d.glyphs[glyphIndex]
		if len(glyph) == 0 {
			return E.New("clear: glyph ", glyphIndex, " is not cached")
		}
		if width*height > len(glyph)/4 {
			return E.New("clear: glyph ", glyphIndex, " holds ", len(glyph)/4, " pixels, ", width, "x", height, " requested")
		}
		visibleWidth, visibleHeight := min(width, dst.Width), min(height, dst.Height)
		for y := range visibleHeight {
			copy(dst.Pix[y*dst.Stride:][:visibleWidth*4], glyph[y*width*4:])
		}
		return nil
	}
	if len(src) == 0 && glyphIndex < 0 {
		return nil
	}
	if len(src) < compositeHeaderSize {
		return E.New("clear: truncated composite header: ", len(src), " of 12 bytes")
	}
	residualByteCount := uint64(binary.LittleEndian.Uint32(src))
	bandsByteCount := uint64(binary.LittleEndian.Uint32(src[4:]))
	subcodecByteCount := uint64(binary.LittleEndian.Uint32(src[8:]))
	src = src[compositeHeaderSize:]
	if residualByteCount+bandsByteCount+subcodecByteCount > uint64(len(src)) {
		return E.New("clear: composite layers need ", residualByteCount+bandsByteCount+subcodecByteCount, " bytes, ", len(src), " remain")
	}
	target := dst
	var glyph []byte
	if glyphIndex >= 0 {
		glyph = d.glyphs[glyphIndex]
		d.glyphs[glyphIndex] = glyph[:0]
		if area := width * height; area > 0 && area <= glyphMaxPixels {
			if glyph == nil {
				glyph = make([]byte, glyphMaxPixels*4)
			}
			glyph = glyph[:area*4]
			clear(glyph)
			d.glyph = bgra.Image{Pix: glyph, Stride: width * 4, Width: width, Height: height}
			d.glyph.Copy(image.Point{}, dst, dst.Bounds())
			target = &d.glyph
		} else {
			glyph = nil
		}
	}
	if residualByteCount > 0 {
		err := decodeResidual(src[:residualByteCount], width, height, target)
		if err != nil {
			return E.Cause(err, "clear: decode residual layer")
		}
	}
	src = src[residualByteCount:]
	if bandsByteCount > 0 {
		err := d.decodeBands(src[:bandsByteCount], width, height, target)
		if err != nil {
			return E.Cause(err, "clear: decode bands layer")
		}
	}
	src = src[bandsByteCount:]
	if subcodecByteCount > 0 {
		err := d.decodeSubcodecs(src[:subcodecByteCount], width, height, target)
		if err != nil {
			return E.Cause(err, "clear: decode subcodec layer")
		}
	}
	if glyph != nil {
		d.glyphs[glyphIndex] = glyph
		dst.Copy(image.Point{}, target, target.Bounds())
	}
	return nil
}

func readRunLength(data []byte, offset int) (int, int, bool) {
	length := int(data[offset])
	offset++
	if length < runLength16 {
		return length, offset, true
	}
	if len(data) < offset+2 {
		return 0, 0, false
	}
	length = int(binary.LittleEndian.Uint16(data[offset:]))
	offset += 2
	if length < runLength32 {
		return length, offset, true
	}
	if len(data) < offset+4 {
		return 0, 0, false
	}
	return int(binary.LittleEndian.Uint32(data[offset:])), offset + 4, true
}

func decodeResidual(data []byte, width, height int, dst *bgra.Image) error {
	total := width * height
	writer := linearWriter{image: dst, width: width}
	written := 0
	for len(data) > 0 {
		if len(data) < residualHeaderSize {
			return E.New("truncated run after pixel ", written, ": ", len(data), " of 4 bytes")
		}
		color := uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16 | 0xFF000000
		length, offset, ok := readRunLength(data, 3)
		if !ok {
			return E.New("truncated run length after pixel ", written)
		}
		data = data[offset:]
		if length > total-written {
			return E.New("run of ", length, " pixels overflows ", width, "x", height)
		}
		writer.fill(length, color)
		written += length
	}
	if written != total {
		return E.New("runs cover ", written, " of ", total, " pixels")
	}
	return nil
}

func (d *Decoder) decodeBands(data []byte, width, height int, dst *bgra.Image) error {
	visibleWidth, visibleHeight := min(width, dst.Width), min(height, dst.Height)
	for len(data) > 0 {
		if len(data) < bandHeaderSize {
			return E.New("truncated band header: ", len(data), " of 11 bytes")
		}
		xStart := int(binary.LittleEndian.Uint16(data))
		xEnd := int(binary.LittleEndian.Uint16(data[2:]))
		yStart := int(binary.LittleEndian.Uint16(data[4:]))
		yEnd := int(binary.LittleEndian.Uint16(data[6:]))
		background := [3]byte{data[8], data[9], data[10]}
		data = data[bandHeaderSize:]
		if xEnd < xStart || yEnd < yStart {
			return E.New("invalid band ", xStart, ",", yStart, " to ", xEnd, ",", yEnd)
		}
		vbarHeight := yEnd - yStart + 1
		if vbarHeight > vbarMaxHeight {
			return E.New("band height ", vbarHeight, " exceeds ", vbarMaxHeight)
		}
		visibleRows := min(vbarHeight, visibleHeight-yStart)
		for x := xStart; x <= xEnd; x++ {
			if len(data) < vbarHeaderSize {
				return E.New("truncated vbar header at column ", x)
			}
			header := int(binary.LittleEndian.Uint16(data))
			data = data[vbarHeaderSize:]
			var vbar []byte
			switch header >> vbarKindShift {
			case shortVBarCacheMiss:
				yOn, yOff := header&yOnMask, header>>yOffShift&yOffMask
				if yOff < yOn {
					return E.New("short vbar ends at ", yOff, " before it starts at ", yOn)
				}
				count := yOff - yOn
				if count > vbarMaxHeight {
					return E.New("short vbar height ", count, " exceeds ", vbarMaxHeight)
				}
				if len(data) < count*3 {
					return E.New("truncated short vbar at column ", x, ": ", len(data), " of ", count*3, " bytes")
				}
				short := d.shortVBarPixels[d.shortVBarCursor*vbarStride:][:count*3]
				copy(short, data)
				data = data[count*3:]
				d.shortVBarHeights[d.shortVBarCursor] = uint8(count)
				d.shortVBarCursor = (d.shortVBarCursor + 1) % shortVBarCacheSize
				vbar = d.storeVBar(background, vbarHeight, yOn, short)
			case shortVBarCacheHit:
				if len(data) < 1 {
					return E.New("truncated short vbar cache hit at column ", x)
				}
				index := header & shortVBarIndexMask
				yOn := int(data[0])
				data = data[1:]
				short := d.shortVBarPixels[index*vbarStride:][:int(d.shortVBarHeights[index])*3]
				vbar = d.storeVBar(background, vbarHeight, yOn, short)
			default:
				index := header & vbarIndexMask
				vbar = d.vbarPixels[index*vbarStride:][:int(d.vbarHeights[index])*3]
			}
			if x >= visibleWidth {
				continue
			}
			offset := yStart*dst.Stride + x*4
			for row := range visibleRows {
				pixel := dst.Pix[offset : offset+4 : offset+4]
				if row*3 < len(vbar) {
					pixel[0], pixel[1], pixel[2] = vbar[row*3], vbar[row*3+1], vbar[row*3+2]
				} else {
					pixel[0], pixel[1], pixel[2] = 0, 0, 0
				}
				pixel[3] = 0xFF
				offset += dst.Stride
			}
		}
	}
	return nil
}

func (d *Decoder) storeVBar(background [3]byte, height, yOn int, short []byte) []byte {
	vbar := d.vbarPixels[d.vbarCursor*vbarStride:][:height*3]
	d.vbarHeights[d.vbarCursor] = uint8(height)
	d.vbarCursor = (d.vbarCursor + 1) % vbarCacheSize
	on := min(yOn, height) * 3
	for i := 0; i < on; i += 3 {
		vbar[i], vbar[i+1], vbar[i+2] = background[0], background[1], background[2]
	}
	for i := on + copy(vbar[on:], short); i < len(vbar); i += 3 {
		vbar[i], vbar[i+1], vbar[i+2] = background[0], background[1], background[2]
	}
	return vbar
}

func (d *Decoder) decodeSubcodecs(data []byte, width, height int, dst *bgra.Image) error {
	for len(data) > 0 {
		if len(data) < subcodecHeaderSize {
			return E.New("truncated subcodec header: ", len(data), " of 13 bytes")
		}
		x := int(binary.LittleEndian.Uint16(data))
		y := int(binary.LittleEndian.Uint16(data[2:]))
		subWidth := int(binary.LittleEndian.Uint16(data[4:]))
		subHeight := int(binary.LittleEndian.Uint16(data[6:]))
		byteCount := binary.LittleEndian.Uint32(data[8:])
		subcodecID := data[12]
		data = data[subcodecHeaderSize:]
		if uint64(byteCount) > uint64(len(data)) {
			return E.New("truncated subcodec payload: ", len(data), " of ", byteCount, " bytes")
		}
		payload := data[:byteCount]
		data = data[byteCount:]
		if x+subWidth > width || y+subHeight > height {
			return E.New("subcodec region ", subWidth, "x", subHeight, " at ", x, ",", y, " exceeds ", width, "x", height)
		}
		region := &bgra.Image{Stride: dst.Stride}
		if regionWidth, regionHeight := min(subWidth, dst.Width-x), min(subHeight, dst.Height-y); regionWidth > 0 && regionHeight > 0 {
			region.Pix, region.Width, region.Height = dst.Pix[dst.Offset(x, y):], regionWidth, regionHeight
		}
		switch subcodecID {
		case subcodecUncompressed:
			if len(payload) != subWidth*subHeight*3 {
				return E.New("uncompressed subcodec has ", len(payload), " bytes for ", subWidth, "x", subHeight)
			}
			for row := range region.Height {
				in := payload[row*subWidth*3:][:region.Width*3]
				out := region.Pix[row*region.Stride:][:region.Width*4]
				for i := range region.Width {
					pixel := out[i*4 : i*4+4 : i*4+4]
					pixel[0], pixel[1], pixel[2], pixel[3] = in[i*3], in[i*3+1], in[i*3+2], 0xFF
				}
			}
		case subcodecNSCodec:
			err := d.nsc.Decode(payload, subWidth, subHeight, false, region)
			if err != nil {
				return err
			}
		case subcodecRLEX:
			err := decodeRLEX(payload, subWidth, subHeight, region)
			if err != nil {
				return E.Cause(err, "decode rlex subcodec")
			}
		default:
			return E.New("unknown subcodec ", subcodecID)
		}
	}
	return nil
}

func decodeRLEX(data []byte, width, height int, dst *bgra.Image) error {
	if len(data) < 1 {
		return E.New("missing palette count")
	}
	paletteCount := int(data[0])
	if paletteCount < 1 || paletteCount > maxPaletteCount {
		return E.New("palette count ", paletteCount, " out of range")
	}
	if len(data) < 1+paletteCount*3 {
		return E.New("truncated palette: ", len(data)-1, " of ", paletteCount*3, " bytes")
	}
	var palette [maxPaletteCount]uint32
	for i := range paletteCount {
		entry := data[1+i*3:]
		palette[i] = uint32(entry[0]) | uint32(entry[1])<<8 | uint32(entry[2])<<16 | 0xFF000000
	}
	data = data[1+paletteCount*3:]
	indexBits := max(1, bits.Len(uint(paletteCount-1)))
	total := width * height
	writer := linearWriter{image: dst, width: width}
	written := 0
	for len(data) > 0 {
		if len(data) < rlexSegmentSize {
			return E.New("truncated segment after pixel ", written, ": ", len(data), " of 2 bytes")
		}
		stopIndex := int(data[0]) & (1<<indexBits - 1)
		suiteDepth := int(data[0]) >> indexBits
		length, offset, ok := readRunLength(data, 1)
		if !ok {
			return E.New("truncated run length after pixel ", written)
		}
		data = data[offset:]
		startIndex := stopIndex - suiteDepth
		if startIndex < 0 || stopIndex >= paletteCount {
			return E.New("palette suite ", startIndex, " to ", stopIndex, " outside ", paletteCount, " entries")
		}
		if length+suiteDepth+1 > total-written {
			return E.New("segment of ", length+suiteDepth+1, " pixels overflows ", width, "x", height)
		}
		writer.fill(length, palette[startIndex])
		for index := startIndex; index <= stopIndex; index++ {
			writer.fill(1, palette[index])
		}
		written += length + suiteDepth + 1
	}
	if written != total {
		return E.New("segments cover ", written, " of ", total, " pixels")
	}
	return nil
}

func (w *linearWriter) fill(count int, color uint32) {
	dst := w.image
	for count > 0 && w.y < dst.Height {
		n := min(count, w.width-w.x)
		if w.x < dst.Width {
			row := dst.Pix[w.y*dst.Stride+w.x*4 : w.y*dst.Stride+min(w.x+n, dst.Width)*4]
			binary.LittleEndian.PutUint32(row, color)
			for filled := 4; filled < len(row); filled *= 2 {
				copy(row[filled:], row[:filled])
			}
		}
		count -= n
		w.x += n
		if w.x == w.width {
			w.x = 0
			w.y++
		}
	}
}
