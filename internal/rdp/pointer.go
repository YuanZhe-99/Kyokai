// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/pointer.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package rdp

import (
	"encoding/binary"

	"github.com/YuanZhe-99/Kyokai/internal/bgra"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const (
	pointerMessageSystem   = 0x0001
	pointerMessagePosition = 0x0003
	pointerMessageColor    = 0x0006
	pointerMessageCached   = 0x0007
	pointerMessagePointer  = 0x0008
	pointerMessageLarge    = 0x0009

	systemPointerNull    = 0x00000000
	systemPointerDefault = 0x00007F00

	maxPointerSize       = 96
	colorPointerBitDepth = 24
)

func (g *graphics) processPointer(data []byte) {
	r := reader{data: data}
	messageType := r.u16()
	r.take(2)
	var code byte
	switch messageType {
	case pointerMessageSystem:
		switch r.u32() {
		case systemPointerNull:
			code = fastPathUpdatePointerHidden
		case systemPointerDefault:
			code = fastPathUpdatePointerDefault
		}
	case pointerMessagePosition:
		code = fastPathUpdatePointerPosition
	case pointerMessageColor:
		code = fastPathUpdateColorPointer
	case pointerMessageCached:
		code = fastPathUpdateCachedPointer
	case pointerMessagePointer:
		code = fastPathUpdatePointer
	case pointerMessageLarge:
		code = fastPathUpdateLargePointer
	}
	if r.err != nil || code == 0 {
		g.logger.Debug("ignore pointer update", "type", messageType)
		return
	}
	g.updatePointer(code, r.rest())
}

func (g *graphics) updatePointer(code byte, data []byte) {
	r := reader{data: data}
	switch code {
	case fastPathUpdatePointerHidden:
		g.events.CursorChanged(&bgra.Cursor{})
	case fastPathUpdatePointerDefault:
		g.events.CursorChanged(nil)
	case fastPathUpdatePointerPosition:
		x, y := int(r.u16()), int(r.u16())
		if r.err == nil {
			g.events.CursorMoved(x, y)
		}
	case fastPathUpdateCachedPointer:
		index := int(r.u16())
		if r.err != nil || index >= pointerCacheSize || g.pointerCache[index] == nil {
			g.logger.Debug("missing cached pointer", "index", index)
			return
		}
		g.events.CursorChanged(g.pointerCache[index])
	case fastPathUpdateColorPointer, fastPathUpdatePointer, fastPathUpdateLargePointer:
		bpp := colorPointerBitDepth
		if code != fastPathUpdateColorPointer {
			bpp = int(r.u16())
		}
		index, cursor, err := g.readPointer(&r, bpp, code == fastPathUpdateLargePointer)
		if err != nil {
			g.logger.Debug("ignore pointer update", "error", err)
			return
		}
		g.pointerCache[index] = cursor
		g.events.CursorChanged(cursor)
	}
}

func (g *graphics) readPointer(r *reader, bpp int, large bool) (uint16, *bgra.Cursor, error) {
	index := r.u16()
	hotX, hotY := int(r.u16()), int(r.u16())
	width, height := int(r.u16()), int(r.u16())
	var andLength, xorLength uint32
	if large {
		andLength, xorLength = r.u32(), r.u32()
	} else {
		andLength, xorLength = uint32(r.u16()), uint32(r.u16())
	}
	if r.err != nil {
		return 0, nil, r.err
	}
	if width > maxPointerSize || height > maxPointerSize {
		return 0, nil, E.New("rdp: ", width, "x", height, " pointer is larger than ", maxPointerSize, "x", maxPointerSize)
	}
	if index >= pointerCacheSize {
		return 0, nil, E.New("rdp: pointer cache index ", index, " exceeds capacity ", pointerCacheSize)
	}
	switch bpp {
	case 1, 16, 24, 32:
	case 4, 8:
		if !g.hasPalette {
			return 0, nil, E.New("rdp: ", bpp, " bpp pointer without a palette")
		}
	default:
		return 0, nil, E.New("rdp: unsupported pointer depth ", bpp)
	}
	xorStride := (width*bpp + 15) / 16 * 2
	andStride := (width + 15) / 16 * 2
	if xorLength != uint32(xorStride*height) {
		return 0, nil, E.New("rdp: pointer XOR mask of ", xorLength, " bytes, expected ", xorStride*height)
	}
	if andLength != 0 && andLength != uint32(andStride*height) {
		return 0, nil, E.New("rdp: pointer AND mask of ", andLength, " bytes, expected ", andStride*height)
	}
	xorMask := r.take(int(xorLength))
	andMask := r.take(int(andLength))
	if r.err != nil {
		return 0, nil, r.err
	}
	if r.remaining() > 1 {
		return 0, nil, E.New("rdp: ", r.remaining(), " trailing bytes after pointer masks")
	}
	if hotX >= width {
		hotX = 0
	}
	if hotY >= height {
		hotY = 0
	}
	cursor := &bgra.Cursor{Width: width, Height: height, HotspotX: hotX, HotspotY: hotY, Pixels: make([]byte, width*height*4)}
	for y := 0; y < height; y++ {
		row := height - y - 1
		if bpp == 1 {
			// FreeRDP reads 1 bpp XOR masks top-down and deeper ones bottom-up.
			row = y
		}
		xorRow := xorMask[row*xorStride : (row+1)*xorStride]
		for x := 0; x < width; x++ {
			pixel := cursor.Pixels[(y*width+x)*4:][:4]
			pixel[3] = 255
			switch bpp {
			case 1:
				if xorRow[x/8]&(0x80>>uint(x%8)) != 0 {
					pixel[0], pixel[1], pixel[2] = 255, 255, 255
				}
			case 4, 8:
				var paletteIndex byte
				if bpp == 4 {
					paletteIndex = (xorRow[x/2] >> uint(4*(1-x%2))) & 15
				} else {
					paletteIndex = xorRow[x]
				}
				color := g.palette[paletteIndex]
				pixel[0], pixel[1], pixel[2] = byte(color), byte(color>>8), byte(color>>16)
			case 16:
				color := binary.LittleEndian.Uint16(xorRow[x*2:])
				blue, green, red := byte(color&31), byte((color>>5)&31), byte((color>>10)&31)
				pixel[0], pixel[1], pixel[2] = blue<<3|blue>>2, green<<3|green>>2, red<<3|red>>2
			case 24:
				copy(pixel[:3], xorRow[x*3:x*3+3])
			case 32:
				copy(pixel, xorRow[x*4:x*4+4])
			}
			if len(andMask) == 0 || andMask[row*andStride+x/8]&(0x80>>uint(x%8)) == 0 {
				continue
			}
			if pixel[0] == 0 && pixel[1] == 0 && pixel[2] == 0 && pixel[3] == 255 {
				pixel[3] = 0
			} else if pixel[0] == 255 && pixel[1] == 255 && pixel[2] == 255 && pixel[3] == 255 {
				if cursor.Invert == nil {
					cursor.Invert = make([]byte, width*height)
				}
				cursor.Invert[y*width+x] = 255
				shade := byte(255)
				if (x+y)%2 != 0 {
					shade = 0
				}
				pixel[0], pixel[1], pixel[2] = shade, shade, shade
			}
		}
	}
	return index, cursor, nil
}
