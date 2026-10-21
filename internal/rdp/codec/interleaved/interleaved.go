// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/codec/interleaved/interleaved.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package interleaved

import (
	"encoding/binary"
	"strconv"

	"github.com/YuanZhe-99/Kyokai/internal/bgra"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const (
	opaqueAlpha = 0xFF000000

	regularOrderMask  = 0xC0
	liteOrderMask     = 0xF0
	regularOrderShift = 5
	liteOrderShift    = 4
	regularRunMask    = 0x1F
	liteRunMask       = 0x0F
	maskBits          = 8
)

const (
	regularBGRun         = 0x00
	regularFGRun         = 0x01
	regularFGBGImage     = 0x02
	regularColorRun      = 0x03
	regularColorImage    = 0x04
	liteSetFGFGRun       = 0x0C
	liteSetFGFGBGImage   = 0x0D
	liteDitheredRun      = 0x0E
	megaMegaBGRun        = 0xF0
	megaMegaFGRun        = 0xF1
	megaMegaFGBGImage    = 0xF2
	megaMegaColorRun     = 0xF3
	megaMegaColorImage   = 0xF4
	megaMegaSetFGRun     = 0xF6
	megaMegaSetFGBGImage = 0xF7
	megaMegaDitheredRun  = 0xF8
	specialFGBG1         = 0xF9
	specialFGBG2         = 0xFA
	specialWhite         = 0xFD
	specialBlack         = 0xFE
)

const (
	maskSpecialFGBG1 = 0x03
	maskSpecialFGBG2 = 0x05
)

type Decoder struct {
	rows []uint32
}

func (d *Decoder) Decode(src []byte, width, height, bpp int, palette *[256]uint32, dst *bgra.Image) error {
	if bpp == 32 {
		return E.New("interleaved: unsupported color depth ", bpp)
	}
	size, err := pixelSize(bpp, palette)
	if err != nil {
		return err
	}
	if cap(d.rows) < 2*width {
		d.rows = make([]uint32, 2*width)
	}
	outWidth, outHeight := min(width, dst.Width), min(height, dst.Height)
	return decodeRLE(src, d.rows[:2*width], width, height, size, func(line int, row []uint32) {
		y := height - 1 - line
		if y >= outHeight || outWidth <= 0 {
			return
		}
		row = row[:outWidth]
		out := dst.Pix[y*dst.Stride:][:outWidth*4]
		switch bpp {
		case 8:
			for x, p := range row {
				binary.LittleEndian.PutUint32(out[x*4:x*4+4], palette[uint8(p)]|opaqueAlpha)
			}
		case 15:
			for x, p := range row {
				binary.LittleEndian.PutUint32(out[x*4:x*4+4], expand15(p)|opaqueAlpha)
			}
		case 16:
			for x, p := range row {
				binary.LittleEndian.PutUint32(out[x*4:x*4+4], expand16(p)|opaqueAlpha)
			}
		default:
			for x, p := range row {
				binary.LittleEndian.PutUint32(out[x*4:x*4+4], p|opaqueAlpha)
			}
		}
	})
}

func DecodeUncompressed(src []byte, width, height, bpp int, palette *[256]uint32, dst *bgra.Image) error {
	size, err := pixelSize(bpp, palette)
	if err != nil {
		return err
	}
	rowSize := (width*size + 3) &^ 3
	if len(src) < rowSize*(height-1)+width*size {
		return E.New("interleaved: truncated bitmap: ", len(src), " of ", rowSize*(height-1)+width*size, " bytes")
	}
	outWidth, outHeight := min(width, dst.Width), min(height, dst.Height)
	if outWidth <= 0 {
		return nil
	}
	for y := range outHeight {
		row := src[(height-1-y)*rowSize:][:outWidth*size]
		out := dst.Pix[y*dst.Stride:][:outWidth*4]
		switch bpp {
		case 8:
			for x, p := range row {
				binary.LittleEndian.PutUint32(out[x*4:x*4+4], palette[p]|opaqueAlpha)
			}
		case 15:
			for len(row) >= 2 && len(out) >= 4 {
				binary.LittleEndian.PutUint32(out, expand15(uint32(binary.LittleEndian.Uint16(row)))|opaqueAlpha)
				row, out = row[2:], out[4:]
			}
		case 16:
			for len(row) >= 2 && len(out) >= 4 {
				binary.LittleEndian.PutUint32(out, expand16(uint32(binary.LittleEndian.Uint16(row)))|opaqueAlpha)
				row, out = row[2:], out[4:]
			}
		case 24:
			for len(row) >= 3 && len(out) >= 4 {
				binary.LittleEndian.PutUint32(out, uint32(row[0])|uint32(row[1])<<8|uint32(row[2])<<16|opaqueAlpha)
				row, out = row[3:], out[4:]
			}
		default:
			for len(row) >= 4 && len(out) >= 4 {
				binary.LittleEndian.PutUint32(out, binary.LittleEndian.Uint32(row)|opaqueAlpha)
				row, out = row[4:], out[4:]
			}
		}
	}
	return nil
}

func pixelSize(bpp int, palette *[256]uint32) (int, error) {
	switch bpp {
	case 8:
		if palette == nil {
			return 0, E.New("interleaved: 8 bpp bitmap without palette")
		}
		return 1, nil
	case 15, 16:
		return 2, nil
	case 24:
		return 3, nil
	case 32:
		return 4, nil
	default:
		return 0, E.New("interleaved: unsupported color depth ", bpp)
	}
}

func expand15(p uint32) uint32 {
	r, g, b := p>>10&0x1F, p>>5&0x1F, p&0x1F
	return b<<3 | b>>2 | (g<<3|g>>2)<<8 | (r<<3|r>>2)<<16
}

func expand16(p uint32) uint32 {
	r, g, b := p>>11&0x1F, p>>5&0x3F, p&0x1F
	return b<<3 | b>>2 | (g<<2|g>>4)<<8 | (r<<3|r>>2)<<16
}

func readPixel(src []byte, size int) uint32 {
	switch size {
	case 1:
		return uint32(src[0])
	case 2:
		return uint32(src[0]) | uint32(src[1])<<8
	default:
		return uint32(src[0]) | uint32(src[1])<<8 | uint32(src[2])<<16
	}
}

func decodeRLE(src []byte, rows []uint32, width, height, size int, emit func(line int, row []uint32)) error {
	white := uint32(1)<<(size*8) - 1
	fg := white
	insertFG := false
	firstLine := true
	total := width * height
	pos := 0
	for i := 0; i < len(src); {
		if firstLine && pos >= width {
			firstLine = false
			insertFG = false
		}
		header := src[i]
		i++
		code := header
		switch {
		case header&regularOrderMask != regularOrderMask:
			code = header >> regularOrderShift
		case header&liteOrderMask != liteOrderMask:
			code = header >> liteOrderShift
		}

		var run int
		switch code {
		case regularBGRun, regularFGRun, regularColorRun, regularColorImage:
			run = int(header & regularRunMask)
			if run == 0 {
				if i >= len(src) {
					return E.New("interleaved: truncated run length at byte ", i)
				}
				run = int(src[i]) + 32
				i++
			}
		case regularFGBGImage:
			run = int(header&regularRunMask) * maskBits
			if run == 0 {
				if i >= len(src) {
					return E.New("interleaved: truncated run length at byte ", i)
				}
				run = int(src[i]) + 1
				i++
			}
		case liteSetFGFGRun, liteDitheredRun:
			run = int(header & liteRunMask)
			if run == 0 {
				if i >= len(src) {
					return E.New("interleaved: truncated run length at byte ", i)
				}
				run = int(src[i]) + 16
				i++
			}
		case liteSetFGFGBGImage:
			run = int(header&liteRunMask) * maskBits
			if run == 0 {
				if i >= len(src) {
					return E.New("interleaved: truncated run length at byte ", i)
				}
				run = int(src[i]) + 1
				i++
			}
		case megaMegaBGRun, megaMegaFGRun, megaMegaFGBGImage, megaMegaColorRun, megaMegaColorImage,
			megaMegaSetFGRun, megaMegaSetFGBGImage, megaMegaDitheredRun:
			if len(src)-i < 2 {
				return E.New("interleaved: truncated run length at byte ", i)
			}
			run = int(binary.LittleEndian.Uint16(src[i:]))
			i += 2
		case specialFGBG1, specialFGBG2:
			run = maskBits
		case specialWhite, specialBlack:
			run = 1
		default:
			return E.New("interleaved: invalid order header 0x", strconv.FormatUint(uint64(header), 16), " at byte ", i-1)
		}

		switch code {
		case liteSetFGFGRun, liteSetFGFGBGImage, megaMegaSetFGRun, megaMegaSetFGBGImage:
			if len(src)-i < size {
				return E.New("interleaved: truncated foreground color at byte ", i)
			}
			fg = readPixel(src[i:], size)
			i += size
		}
		background := code == regularBGRun || code == megaMegaBGRun
		count := run
		if code == liteDitheredRun || code == megaMegaDitheredRun {
			count = 2 * run
		}
		if count > total-pos {
			return E.New("interleaved: order 0x", strconv.FormatUint(uint64(header), 16), " of ", count, " pixels at pixel ", pos, " overruns the ", width, "x", height, " bitmap")
		}
		if background && insertFG && run == 0 {
			return E.New("interleaved: empty background run after a background run at byte ", i)
		}
		insert := background && insertFG
		insertFG = background

		var (
			first, second uint32
			masks, data   []byte
		)
		switch code {
		case liteDitheredRun, megaMegaDitheredRun:
			if len(src)-i < 2*size {
				return E.New("interleaved: truncated dithered colors at byte ", i)
			}
			first, second = readPixel(src[i:], size), readPixel(src[i+size:], size)
			i += 2 * size
		case regularColorRun, megaMegaColorRun:
			if len(src)-i < size {
				return E.New("interleaved: truncated run color at byte ", i)
			}
			first = readPixel(src[i:], size)
			i += size
		case specialFGBG1:
			masks = []byte{maskSpecialFGBG1}
		case specialFGBG2:
			masks = []byte{maskSpecialFGBG2}
		case regularFGBGImage, megaMegaFGBGImage, liteSetFGFGBGImage, megaMegaSetFGBGImage:
			n := (run + maskBits - 1) / maskBits
			if len(src)-i < n {
				return E.New("interleaved: truncated bitmask at byte ", i, ": ", len(src)-i, " of ", n, " bytes")
			}
			masks = src[i : i+n]
			i += n
		case regularColorImage, megaMegaColorImage:
			if (len(src)-i)/size < run {
				return E.New("interleaved: truncated color image at byte ", i, ": ", len(src)-i, " of ", run*size, " bytes")
			}
			data = src[i : i+run*size]
			i += run * size
		}

		for k := 0; k < count; {
			line := pos / width
			x := pos - line*width
			n := min(count-k, width-x)
			current := rows[(line&1)*width:][:width]
			row := current[x : x+n]
			above := rows[(line&1^1)*width:][x : x+n]
			switch code {
			case regularBGRun, megaMegaBGRun:
				if firstLine {
					clear(row)
				} else {
					copy(row, above)
				}
				if insert && k == 0 {
					row[0] ^= fg
				}
			case regularFGRun, megaMegaFGRun, liteSetFGFGRun, megaMegaSetFGRun:
				if firstLine {
					for j := range row {
						row[j] = fg
					}
				} else {
					for j, p := range above {
						row[j] = p ^ fg
					}
				}
			case liteDitheredRun, megaMegaDitheredRun:
				for j := range row {
					if (k+j)&1 == 0 {
						row[j] = first
					} else {
						row[j] = second
					}
				}
			case regularColorRun, megaMegaColorRun:
				for j := range row {
					row[j] = first
				}
			case regularFGBGImage, megaMegaFGBGImage, liteSetFGFGBGImage, megaMegaSetFGBGImage,
				specialFGBG1, specialFGBG2:
				for j := range row {
					bit := k + j
					mask := fg & -(uint32(masks[bit>>3]>>(bit&7)) & 1)
					if firstLine {
						row[j] = mask
					} else {
						row[j] = above[j] ^ mask
					}
				}
			case regularColorImage, megaMegaColorImage:
				pixels := data[k*size : (k+n)*size]
				switch size {
				case 1:
					for j, value := range pixels {
						row[j] = uint32(value)
					}
				case 2:
					for j := range row {
						row[j] = uint32(binary.LittleEndian.Uint16(pixels[j*2 : j*2+2]))
					}
				default:
					for j := range row {
						pixel := pixels[j*3 : j*3+3]
						row[j] = uint32(pixel[0]) | uint32(pixel[1])<<8 | uint32(pixel[2])<<16
					}
				}
			case specialWhite:
				row[0] = white
			case specialBlack:
				row[0] = 0
			}
			k += n
			pos += n
			if x+n == width {
				emit(line, current)
			}
		}
	}
	for pos < total {
		line := pos / width
		current := rows[(line&1)*width:][:width]
		clear(current[pos-line*width:])
		emit(line, current)
		pos = (line + 1) * width
	}
	return nil
}
