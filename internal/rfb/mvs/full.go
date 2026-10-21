// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rfb/mvs/full.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package mvs

import (
	"strconv"

	"github.com/YuanZhe-99/Kyokai/internal/bgra"
	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const (
	fullUnchanged = iota
	fullRefine
	fullCopy
	fullCache
)

func (d *Decoder) decodeFull(update []byte, x, y, width, height int, target *bgra.Image) error {
	if len(update) < 3 {
		return E.New("mvs: full update of ", len(update), " bytes, shorter than its 3-byte header")
	}
	reader := bitReader{data: update[3:]}
	luminanceLimit := min(int(update[1]), 64)
	chrominanceLimit := min(int(update[2]), 64)
	scratch := &d.scratch
	for localY := 0; localY < height; localY += 8 {
		tileHeight := min(8, height-localY)
		pixelY := y + localY
		rowTile := (pixelY / 8) * d.tilesWide
		for localX := 0; localX < width; localX += 8 {
			tileWidth := min(8, width-localX)
			pixelX := x + localX
			globalTile := rowTile + pixelX/8
			switch reader.read(2) {
			case fullUnchanged:
			case fullRefine:
				lossless := d.decodeRefinement(&reader, &d.baselines[globalTile], luminanceLimit, chrominanceLimit)
				d.renderDCT(&scratch.pixels)
				blitTile(target, pixelX, pixelY, tileWidth, tileHeight, &scratch.pixels)
				d.insertCache(lossless)
			case fullCopy:
				state := &d.tiles[globalTile]
				source := state.copySource
				if source < 0 || d.tiles[source].generation != state.generation {
					continue
				}
				copyTile(target, pixelX, pixelY, int(source)%d.tilesWide*8, int(source)/d.tilesWide*8, tileWidth, tileHeight)
			case fullCache:
				var index uint16
				if reader.readBit() {
					index = nextCacheIndex(d.lastCacheReference)
				} else {
					index = uint16(reader.read(16))
				}
				entry := d.cachedEntry(index)
				if entry == nil {
					continue
				}
				blitTile(target, pixelX, pixelY, tileWidth, tileHeight, &entry.pixels)
			}
		}
		if reader.failed() {
			return E.New("mvs: invalid full update of ", len(update), " bytes at tile row ", localY/8)
		}
	}
	marker := reader.read(24)
	if marker != 0x6d7673 || reader.failed() {
		return E.New("mvs: full update of ", len(update), " bytes ends with marker 0x", strconv.FormatUint(uint64(marker), 16), ", expected 0x6d7673")
	}
	return nil
}

func (d *Decoder) decodeRefinement(r *bitReader, baseline *tileBaseline, luminanceLimit, chrominanceLimit int) (lossless bool) {
	oldCount := int(baseline.lumaCount)
	if oldCount > 64 {
		r.invalid = true
		return false
	}
	newCount := int(r.read(6)) + 1
	scratch := &d.scratch
	values := &scratch.lumaScan
	clear(values[:])
	values[0] = baseline.luma[0]
	sharedEnd := min(oldCount, newCount)
	extensionBits := uint(3)
	if oldCount < 15 {
		extensionBits = 4
		for scan := 1; scan < sharedEnd; scan++ {
			old := baseline.luma[zigzag[scan]]
			if old == 0 {
				values[scan] = int8(readDCRice(r))
			} else {
				values[scan] = signedDelta(old, r.read(3))
			}
		}
	} else {
		for scan := 1; scan < sharedEnd; scan++ {
			old := baseline.luma[zigzag[scan]]
			if old != 0 {
				values[scan] = signedDelta(old, r.read(1))
			} else if r.readBit() {
				values[scan] = 1 - 2*int8(r.read(1))
			}
		}
	}
	for scan := oldCount; scan < newCount; scan++ {
		old := baseline.luma[zigzag[scan]]
		if old == 0 {
			values[scan] = int8(readDCRice(r))
		} else {
			values[scan] = signedDelta(old, r.read(extensionBits))
		}
	}
	clear(scratch.luma[:])
	clear(scratch.cb[:])
	clear(scratch.cr[:])
	scratch.lumaAC = false
	for scan := range newCount {
		scratch.luma[zigzag[scan]] = int16(values[scan])
		scratch.lumaAC = scratch.lumaAC || scan > 0 && values[scan] != 0
	}
	// ScreenSharing decodes Cr before Cb, using header byte 1 for Cr and byte 2 for Cb
	// (ScreenSharing 776.27, macOS 27.0.1 26A434, UUID EC0B2C20-F816-3FCE-822D-F9403C09EB50,
	// _DecodeMVSUpdate 0x1cc068b78..0x1cc068c9c, 0x1cc069404..0x1cc0694ec,
	// _InitializeIDCT 0x1cc06b1a0..0x1cc06b1d8, _ycc_xrgb_convert32to32 0x1cc16b1c4..0x1cc16b1f4;
	// ScreenSharing 786.3, macOS 27.2 26B5101f, UUID 21DF7A41-0D38-3DF3-B87A-8A6249686735,
	// _DecodeMVSUpdate 0x22e4abbfc..0x22e4abd20, 0x22e4ac488..0x22e4ac570,
	// _InitializeIDCT 0x22e4ae224..0x22e4ae25c, _ycc_xrgb_convert32to32 0x22e5aef18..0x22e5aef48).
	scratch.cr[0] = int16(oneStepDC(r, baseline.crDC))
	var crLossless, cbLossless bool
	scratch.crAC, crLossless = readHuffmanAC(r, &scratch.cr, luminanceLimit, len(cacheEntry{}.cr))
	scratch.cb[0] = int16(oneStepDC(r, baseline.cbDC))
	scratch.cbAC, cbLossless = readHuffmanAC(r, &scratch.cb, chrominanceLimit, len(cacheEntry{}.cb))
	return crLossless && cbLossless
}

func signedDelta(old int8, magnitude uint32) int8 {
	if old < 0 {
		return old - int8(magnitude)
	}
	return old + int8(magnitude)
}

func oneStepDC(r *bitReader, old int8) int8 {
	changes := r.readBit()
	switch {
	case old == 0 && !changes:
		return 0
	case old == 0:
		if r.readBit() {
			return -1
		}
		return 1
	case !changes:
		return old
	case old < 0:
		return old - 1
	default:
		return old + 1
	}
}

func readHuffmanAC(r *bitReader, coefficients *block, limit, cached int) (present, lossless bool) {
	lossless = true
	for scan := 1; scan <= limit; {
		symbol := readHuffmanSymbol(r)
		run := int(symbol >> 4)
		size := uint(symbol & 0x0f)
		if size == 0 {
			if run != 15 {
				break
			}
			scan += 16
			continue
		}
		scan += run
		if scan >= 80 {
			r.invalid = true
			break
		}
		encoded := int32(r.read(size))
		if encoded < 1<<(size-1) {
			encoded += 1 - 1<<size
		}
		coefficients[zigzag[min(scan, 63)]] = int16(encoded)
		present = true
		if scan >= cached || encoded < -128 || encoded > 127 {
			lossless = false
		}
		scan++
	}
	return present, lossless
}
