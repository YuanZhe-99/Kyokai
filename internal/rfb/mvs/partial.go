// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rfb/mvs/partial.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package mvs

import (
	"encoding/binary"
	"math/bits"
	"strconv"

	"github.com/YuanZhe-99/Kyokai/internal/bgra"
	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const (
	tileWhite = iota
	tileCopyLeft
	tileCopyAbove
	tileBilevel
	tileColors
	tileRice
	tileCacheIndex
	tileCacheNext
)

var (
	colorWhite = pixelFromRGB(0xff, 0xff, 0xff)
	colorBlack = pixelFromRGB(0, 0, 0)
	// ScreenSharing initializes the second partial-update colour to 0x00B5D5FE
	// (ScreenSharing 776.27, macOS 27.0.1 26A434, UUID EC0B2C20-F816-3FCE-822D-F9403C09EB50,
	// _DecodeMVSUpdate 0x1cc06a1f0..0x1cc06a1f8;
	// ScreenSharing 786.3, macOS 27.2 26B5101f, UUID 21DF7A41-0D38-3DF3-B87A-8A6249686735,
	// _DecodeMVSUpdate 0x22e4ad274..0x22e4ad27c).
	colorSecondDefault = pixelFromRGB(0xb5, 0xd5, 0xfe)
)

type riceBlock struct {
	coefficients block
	lumaAC       bool
	cbDC         int16
	crDC         int16
	baseline     tileBaseline
	pixels       tilePixels
}

func (d *Decoder) decodePartial(update []byte, x, y, width, height int, target *bgra.Image) error {
	if len(update) < 6 {
		return E.New("mvs: partial update of ", len(update), " bytes, shorter than its 6-byte header")
	}
	secondaryOffset := int(update[3])<<16 | int(update[4])<<8 | int(update[5])
	if secondaryOffset < 6 || secondaryOffset > len(update) {
		return E.New("mvs: invalid secondary stream offset ", secondaryOffset, " in partial update of ", len(update), " bytes")
	}
	primary := bitReader{data: update[6:secondaryOffset]}
	secondary := bitReader{data: update[secondaryOffset:]}
	luminanceLimit := update[1]
	chrominanceLimit := update[2]
	primary.read(1)

	tilesWide := (width + 7) / 8
	total := tilesWide * ((height + 7) / 8)
	firstColor := colorWhite
	secondColor := colorSecondDefault
	solidColor := colorBlack
	var previous riceBlock
	hasPrevious := false
	pixels := &d.scratch.pixels

	d.generation++
	generation := d.generation
	tileIndex := 0
	column := 0
	row := 0
	for tileIndex < total {
		tileType := primary.read(3)
		run := readRepeatCount(&primary)
		if run > total-tileIndex {
			return E.New("mvs: tile run of ", run, " at tile ", tileIndex, " exceeds the ", total, " tiles of the rectangle")
		}
		for end := tileIndex + run; tileIndex < end; tileIndex++ {
			localX := column * 8
			localY := row * 8
			tileWidth := min(8, width-localX)
			tileHeight := min(8, height-localY)
			pixelX := x + localX
			pixelY := y + localY
			globalTile := (pixelY/8)*d.tilesWide + pixelX/8
			state := &d.tiles[globalTile]
			state.generation = generation
			state.copySource = -1
			column++
			if column == tilesWide {
				column = 0
				row++
			}
			switch tileType {
			case tileWhite:
				fillTile(target, pixelX, pixelY, tileWidth, tileHeight, colorWhite)
			case tileCopyLeft, tileCopyAbove:
				source := globalTile - 1
				if tileType == tileCopyAbove {
					source = globalTile - d.tilesWide
				}
				if source < 0 {
					continue
				}
				state.copySource = int32(source)
				copyTile(target, pixelX, pixelY, source%d.tilesWide*8, source/d.tilesWide*8, tileWidth, tileHeight)
			case tileBilevel:
				decodeBilevel(&secondary, colorWhite, colorBlack, colorWhite, pixels)
				blitTile(target, pixelX, pixelY, tileWidth, tileHeight, pixels)
			case tileColors:
				if secondary.readBit() {
					if !secondary.readBit() {
						firstColor = readColor(&secondary)
						secondColor = readColor(&secondary)
					}
					decodeBilevel(&secondary, firstColor, secondColor, firstColor, pixels)
					blitTile(target, pixelX, pixelY, tileWidth, tileHeight, pixels)
				} else {
					if !secondary.readBit() {
						solidColor = readColor(&secondary)
					}
					fillTile(target, pixelX, pixelY, tileWidth, tileHeight, solidColor)
				}
			case tileRice:
				if secondary.readBit() {
					if !hasPrevious {
						return E.New("mvs: Rice tile ", tileIndex, " repeats a missing block")
					}
				} else {
					decodeRice(&secondary, &previous, hasPrevious, luminanceLimit, chrominanceLimit)
					hasPrevious = true
					quantization := d.chrominanceQuantization[0]
					d.renderLuma(&previous.coefficients, previous.lumaAC, dcSample(previous.cbDC, quantization), dcSample(previous.crDC, quantization), &previous.pixels)
				}
				d.baselines[globalTile] = previous.baseline
				blitTile(target, pixelX, pixelY, tileWidth, tileHeight, &previous.pixels)
			case tileCacheIndex, tileCacheNext:
				var index uint16
				if tileType == tileCacheIndex {
					index = uint16(secondary.read(16))
				} else {
					index = nextCacheIndex(d.lastCacheReference)
				}
				entry := d.cachedEntry(index)
				if entry == nil {
					continue
				}
				blitTile(target, pixelX, pixelY, tileWidth, tileHeight, &entry.pixels)
			}
		}
		if primary.failed() || secondary.failed() {
			return E.New("mvs: invalid partial update of ", len(update), " bytes after ", tileIndex, " of ", total, " tiles")
		}
	}
	primaryMarker, secondaryMarker := primary.read(8), secondary.read(8)
	if primaryMarker != 0x6d || secondaryMarker != 0x6d || primary.failed() || secondary.failed() {
		return E.New("mvs: partial update of ", len(update), " bytes ends with markers 0x", strconv.FormatUint(uint64(primaryMarker), 16),
			" and 0x", strconv.FormatUint(uint64(secondaryMarker), 16), ", expected 0x6d")
	}
	return nil
}

func readRepeatCount(r *bitReader) int {
	if !r.readBit() {
		return 1
	}
	short := r.read(4)
	if short != 15 {
		return int(short) + 2
	}
	var value uint32
	for shift := uint(0); shift < 21; shift += 7 {
		group := r.read(8)
		value |= (group & 0x7f) << shift
		if group&0x80 == 0 {
			break
		}
	}
	return int(value) + 17
}

func readColor(r *bitReader) uint32 {
	fields := r.read(20)
	return pixelFromYCbCr(uint8(fields>>12), uint8(fields>>6&0x3f)<<2, uint8(fields&0x3f)<<2)
}

func decodeBilevel(r *bitReader, uniform, zero, one uint32, pixels *tilePixels) {
	uniformRows := r.read(8)
	for row := range pixels {
		line := &pixels[row]
		if uniformRows&(0x80>>row) != 0 {
			for column := range 8 {
				binary.LittleEndian.PutUint32(line[column*4:], uniform)
			}
			continue
		}
		mask := r.read(8)
		for column := range 8 {
			color := zero
			if mask&(0x80>>column) != 0 {
				color = one
			}
			binary.LittleEndian.PutUint32(line[column*4:], color)
		}
	}
}

func decodeRice(r *bitReader, previous *riceBlock, hasPrevious bool, luminanceLimit, chrominanceLimit uint8) {
	predictorMode := r.read(2)
	var previousLuma, previousCb, previousCr int16
	if hasPrevious {
		previousLuma = previous.coefficients[0]
		previousCb = previous.cbDC
		previousCr = previous.crDC
	}
	cbDC := previousCb
	crDC := previousCr
	if predictorMode&2 == 0 {
		cbDC = (previousCb/2 - readDCRice(r)) * 2
		crDC = (previousCr/2 - readDCRice(r)) * 2
	}
	lumaDC := previousLuma - readDCRice(r)
	limit := luminanceLimit
	if predictorMode&1 != 0 {
		limit = chrominanceLimit
	}
	coefficients := &previous.coefficients
	clear(coefficients[:])
	coefficients[0] = lumaDC
	previous.cbDC = cbDC
	previous.crDC = crDC
	previous.lumaAC = readACRice(r, coefficients, limit)
	baseline := &previous.baseline
	baseline.lumaCount = limit
	baseline.cbDC = int8(cbDC)
	baseline.crDC = int8(crDC)
	for i, value := range coefficients {
		baseline.luma[i] = int8(value)
	}
}

func readDCRice(r *bitReader) int16 {
	if r.count < 44 {
		r.refill()
	}
	prefix := r.leadingOnes()
	suffixes := r.value << (prefix + 1)
	var magnitude, sign uint32
	switch {
	case prefix == 0:
		if suffixes>>63 == 0 {
			r.skip(2)
			return 0
		}
		magnitude = 1
		sign = uint32(suffixes>>62) & 1
		r.skip(3)
	case prefix == 1:
		suffix := uint32(suffixes >> 62)
		magnitude = 2 + suffix>>1
		sign = suffix & 1
		r.skip(4)
	case prefix == 2:
		suffix := uint32(suffixes >> 61)
		magnitude = 4 + suffix>>1
		sign = suffix & 1
		r.skip(6)
	case prefix < 40:
		suffix := uint32(suffixes >> 60)
		magnitude = uint32(prefix)*8 - 16 + suffix>>1
		sign = suffix & 1
		r.skip(prefix + 5)
	default:
		r.invalid = true
		return 0
	}
	if sign != 0 {
		return -int16(magnitude)
	}
	return int16(magnitude)
}

func readACRice(r *bitReader, coefficients *block, limit uint8) (present bool) {
	shiftBelowLimit, shiftFromLimit := uint(3), uint(4)
	if limit > 14 {
		shiftBelowLimit, shiftFromLimit = 1, 3
	}
	scan := 1
	for scan < 64 {
		shift := shiftFromLimit
		if scan < int(limit) {
			shift = shiftBelowLimit
		}
		if r.count < 40 {
			r.refill()
		}
		window := r.value
		if window>>63 != 0 {
			prefix := uint(bits.LeadingZeros64(^(window << 1)))
			suffixes := window << (prefix + 2)
			var magnitude, sign uint32
			switch {
			case scan < 6 && prefix < 4:
				suffix := uint32(suffixes >> 61)
				magnitude = uint32(prefix)*4 + 2 + suffix>>1
				sign = suffix & 1
				r.skip(prefix + 5)
			case scan < 6 && prefix <= 33:
				suffix := uint32(suffixes >> 60)
				magnitude = uint32(prefix)*8 - 14 + suffix>>1
				sign = suffix & 1
				r.skip(prefix + 6)
			case scan < 6:
				r.invalid = true
				return present
			case prefix == 0:
				suffix := uint32(suffixes >> 62)
				magnitude = 2 + suffix>>1
				sign = suffix & 1
				r.skip(4)
			case prefix == 1:
				suffix := uint32(suffixes >> 61)
				magnitude = 4 + suffix>>1
				sign = suffix & 1
				r.skip(6)
			case prefix <= 19:
				suffix := uint32(suffixes >> 60)
				magnitude = uint32(prefix)*8 - 8 + suffix>>1
				sign = suffix & 1
				r.skip(prefix + 6)
			default:
				r.invalid = true
				return present
			}
			value := int16(magnitude << shift)
			if sign != 0 {
				value = -value
			}
			coefficients[zigzag[scan]] = value
			present = true
			scan++
			continue
		}
		switch window >> 61 & 3 {
		case 0:
			r.skip(3)
			scan++
		case 2:
			r.skip(3)
			coefficients[zigzag[scan]] = 1 << shift
			present = true
			scan++
		case 3:
			r.skip(3)
			coefficients[zigzag[scan]] = -(1 << shift)
			present = true
			scan++
		case 1:
			if window>>60&1 == 0 {
				r.skip(4)
				return present
			}
			first := uint32(window >> 58 & 3)
			r.skip(6)
			run := int(first) + 3
			if first == 3 {
				for {
					extension := r.read(3)
					run += int(extension)
					if extension != 7 {
						break
					}
				}
			}
			scan += run
			if scan > 64 {
				r.invalid = true
				return present
			}
		}
	}
	return present
}
