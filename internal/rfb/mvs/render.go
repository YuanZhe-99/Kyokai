// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rfb/mvs/render.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package mvs

import (
	"encoding/binary"

	"github.com/YuanZhe-99/Kyokai/internal/bgra"
)

func fillTile(target *bgra.Image, x, y, width, height int, color uint32) {
	var line [32]byte
	for column := 0; column < len(line); column += 4 {
		binary.LittleEndian.PutUint32(line[column:], color)
	}
	offset := y*target.Stride + x*4
	for range height {
		copy(target.Pix[offset:offset+width*4], line[:])
		offset += target.Stride
	}
}

func blitTile(target *bgra.Image, x, y, width, height int, pixels *tilePixels) {
	offset := y*target.Stride + x*4
	if width == 8 {
		for row := range height {
			*(*[32]byte)(target.Pix[offset : offset+32]) = pixels[row]
			offset += target.Stride
		}
		return
	}
	for row := range height {
		copy(target.Pix[offset:offset+width*4], pixels[row][:])
		offset += target.Stride
	}
}

func copyTile(target *bgra.Image, x, y, sourceX, sourceY, width, height int) {
	width = min(width, target.Width-sourceX)
	height = min(height, target.Height-sourceY)
	if width <= 0 || height <= 0 {
		return
	}
	offset := y*target.Stride + x*4
	sourceOffset := sourceY*target.Stride + sourceX*4
	if width == 8 {
		for range height {
			*(*[32]byte)(target.Pix[offset : offset+32]) = *(*[32]byte)(target.Pix[sourceOffset : sourceOffset+32])
			offset += target.Stride
			sourceOffset += target.Stride
		}
		return
	}
	for range height {
		copy(target.Pix[offset:offset+width*4], target.Pix[sourceOffset:sourceOffset+width*4])
		offset += target.Stride
		sourceOffset += target.Stride
	}
}

func fillPixels(pixels *tilePixels, color uint32) {
	line := &pixels[0]
	for column := 0; column < len(line); column += 4 {
		binary.LittleEndian.PutUint32(line[column:], color)
	}
	for row := 1; row < len(pixels); row++ {
		pixels[row] = *line
	}
}

func fillSamples(output *samples, value uint8) {
	for i := range output {
		output[i] = value
	}
}

func (d *Decoder) renderLuma(luma *block, lumaAC bool, cb, cr uint8, pixels *tilePixels) {
	if !lumaAC {
		fillPixels(pixels, pixelFromYCbCr(dcSample(luma[0], d.luminanceQuantization[0]), cb, cr))
		return
	}
	scratch := &d.scratch
	inverseDCT(luma, &d.luminanceQuantization, &scratch.lumaY)
	red := redFromCr[cr]
	green := (greenFromCb[cb] + greenFromCr[cr]) >> 16
	blue := blueFromCb[cb]
	for row := range pixels {
		line := &pixels[row]
		for column, sample := range (*[8]uint8)(scratch.lumaY[row*8:]) {
			y := int32(sample)
			binary.LittleEndian.PutUint32(line[column*4:], uint32(clampSample(y+blue))|uint32(clampSample(y+green))<<8|uint32(clampSample(y+red))<<16|0xff000000)
		}
	}
}

func (d *Decoder) renderDCT(pixels *tilePixels) {
	scratch := &d.scratch
	if !scratch.cbAC && !scratch.crAC {
		quantization := d.chrominanceQuantization[0]
		d.renderLuma(&scratch.luma, scratch.lumaAC, dcSample(scratch.cb[0], quantization), dcSample(scratch.cr[0], quantization), pixels)
		return
	}
	if scratch.lumaAC {
		inverseDCT(&scratch.luma, &d.luminanceQuantization, &scratch.lumaY)
	} else {
		fillSamples(&scratch.lumaY, dcSample(scratch.luma[0], d.luminanceQuantization[0]))
	}
	if scratch.cbAC {
		inverseDCT(&scratch.cb, &d.chrominanceQuantization, &scratch.cbY)
	} else {
		fillSamples(&scratch.cbY, dcSample(scratch.cb[0], d.chrominanceQuantization[0]))
	}
	if scratch.crAC {
		inverseDCT(&scratch.cr, &d.chrominanceQuantization, &scratch.crY)
	} else {
		fillSamples(&scratch.crY, dcSample(scratch.cr[0], d.chrominanceQuantization[0]))
	}
	for row := range pixels {
		line := &pixels[row]
		lumaRow := (*[8]uint8)(scratch.lumaY[row*8:])
		cbRow := (*[8]uint8)(scratch.cbY[row*8:])
		crRow := (*[8]uint8)(scratch.crY[row*8:])
		for column := range 8 {
			binary.LittleEndian.PutUint32(line[column*4:], pixelFromYCbCr(lumaRow[column], cbRow[column], crRow[column]))
		}
	}
}

func (d *Decoder) renderCacheEntry(entry *cacheEntry) {
	scratch := &d.scratch
	clear(scratch.luma[:])
	clear(scratch.cb[:])
	clear(scratch.cr[:])
	scratch.lumaAC = false
	scratch.cbAC = false
	scratch.crAC = false
	for scan, value := range &entry.luma {
		scratch.luma[zigzag[scan]] = int16(value)
		scratch.lumaAC = scratch.lumaAC || scan > 0 && value != 0
	}
	for scan, value := range &entry.cb {
		scratch.cb[zigzag[scan]] = int16(value)
		scratch.cbAC = scratch.cbAC || scan > 0 && value != 0
	}
	for scan, value := range &entry.cr {
		scratch.cr[zigzag[scan]] = int16(value)
		scratch.crAC = scratch.crAC || scan > 0 && value != 0
	}
	d.renderDCT(&entry.pixels)
}
