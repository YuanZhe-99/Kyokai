// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rfb/mvs/decoder.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package mvs

import (
	"github.com/YuanZhe-99/Kyokai/internal/bgra"
	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const (
	updatePartial      = 0
	updateFull         = 1
	updateQuantization = 2

	quantizationUpdateLength = 129
	cacheLastIndex           = 64999
	cacheChunkBits           = 10
)

type tileState struct {
	generation uint32
	copySource int32
}

type tileBaseline struct {
	lumaCount uint8
	cbDC      int8
	crDC      int8
	luma      [64]int8
}

// ScreenSharing caches 64 Y, 15 Cr and 20 Cb bytes per entry (99 bytes total)
// (ScreenSharing 776.27, macOS 27.0.1 26A434, UUID EC0B2C20-F816-3FCE-822D-F9403C09EB50,
// _DecodeMVSUpdate 0x1cc068da0..0x1cc068dcc, 0x1cc069428..0x1cc06951c, 0x1cc069568..0x1cc0695b4;
// ScreenSharing 786.3, macOS 27.2 26B5101f, UUID 21DF7A41-0D38-3DF3-B87A-8A6249686735,
// _DecodeMVSUpdate 0x22e4abe24..0x22e4abe50, 0x22e4ac4ac..0x22e4ac5a0, 0x22e4ac5ec..0x22e4ac638).
type cacheEntry struct {
	luma     [64]int8
	cb       [20]int8
	cr       [15]int8
	rendered uint32
	pixels   tilePixels
}

type tilePixels [8][32]byte

type tileScratch struct {
	lumaScan [64]int8
	luma     block
	cb       block
	cr       block
	lumaAC   bool
	cbAC     bool
	crAC     bool
	pixels   tilePixels
	lumaY    samples
	cbY      samples
	crY      samples
}

type Decoder struct {
	width      int
	height     int
	tilesWide  int
	tiles      []tileState
	baselines  []tileBaseline
	generation uint32

	luminanceQuantization   quantizationTable
	chrominanceQuantization quantizationTable
	quantizationGeneration  uint32

	cache              [][]cacheEntry
	cacheLength        int
	cacheWriteIndex    uint16
	lastCacheReference uint16

	scratch tileScratch
}

func NewDecoder() *Decoder {
	decoder := &Decoder{quantizationGeneration: 1}
	for i := range 64 {
		decoder.luminanceQuantization[i] = int32(defaultLuminanceQuantization[i])
		decoder.chrominanceQuantization[i] = int32(defaultChrominanceQuantization[i])
	}
	return decoder
}

func (d *Decoder) Reset(width, height int) {
	if width == d.width && height == d.height && d.tiles != nil {
		return
	}
	d.width = width
	d.height = height
	d.tilesWide = (width + 7) / 8
	count := d.tilesWide * ((height + 7) / 8)
	if cap(d.tiles) >= count && cap(d.tiles) <= count*2 {
		d.tiles = d.tiles[:count]
		d.baselines = d.baselines[:count]
		clear(d.baselines)
	} else {
		d.tiles = make([]tileState, count)
		d.baselines = make([]tileBaseline, count)
	}
	for i := range d.tiles {
		d.tiles[i] = tileState{copySource: -1}
	}
	d.generation = 0
}

func (d *Decoder) Decode(update []byte, x, y, width, height int, target *bgra.Image) error {
	if len(update) == 0 {
		return E.New("mvs: empty update")
	}
	switch update[0] {
	case updateQuantization:
		if len(update) != quantizationUpdateLength {
			return E.New("mvs: quantisation update has ", len(update), " bytes, expected ", quantizationUpdateLength)
		}
		for i := range 64 {
			d.luminanceQuantization[i] = int32(update[1+i])
			d.chrominanceQuantization[i] = int32(update[65+i])
		}
		d.quantizationGeneration++
		return nil
	case updatePartial, updateFull:
	default:
		return E.New("mvs: unknown update type ", update[0])
	}
	if width <= 0 || height <= 0 {
		return E.New("mvs: empty rectangle ", width, "x", height)
	}
	if x < 0 || y < 0 || x+width > target.Width || y+height > target.Height {
		return E.New("mvs: rectangle ", width, "x", height, " at ", x, ",", y, " outside ", target.Width, "x", target.Height, " framebuffer")
	}
	d.Reset(target.Width, target.Height)
	if update[0] == updatePartial {
		return d.decodePartial(update, x, y, width, height, target)
	}
	return d.decodeFull(update, x, y, width, height, target)
}

func nextCacheIndex(index uint16) uint16 {
	if index == cacheLastIndex {
		return 1
	}
	return index + 1
}

func (d *Decoder) cachedEntry(index uint16) *cacheEntry {
	if index == 0 || int(index) >= d.cacheLength {
		return nil
	}
	d.lastCacheReference = index
	entry := &d.cache[index>>cacheChunkBits][index&(1<<cacheChunkBits-1)]
	if entry.rendered != d.quantizationGeneration {
		d.renderCacheEntry(entry)
		entry.rendered = d.quantizationGeneration
	}
	return entry
}

func (d *Decoder) insertCache(lossless bool) {
	index := nextCacheIndex(d.cacheWriteIndex)
	d.cacheWriteIndex = index
	chunk := int(index >> cacheChunkBits)
	if chunk == len(d.cache) {
		d.cache = append(d.cache, make([]cacheEntry, 1<<cacheChunkBits))
	}
	d.cacheLength = max(d.cacheLength, int(index)+1)
	entry := &d.cache[chunk][index&(1<<cacheChunkBits-1)]
	scratch := &d.scratch
	entry.luma = scratch.lumaScan
	for scan := range len(entry.cb) {
		entry.cb[scan] = int8(scratch.cb[zigzag[scan]])
	}
	for scan := range len(entry.cr) {
		entry.cr[scan] = int8(scratch.cr[zigzag[scan]])
	}
	if lossless {
		entry.pixels = scratch.pixels
		entry.rendered = d.quantizationGeneration
	} else {
		entry.rendered = 0
	}
}
