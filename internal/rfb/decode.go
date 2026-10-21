// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rfb/decode.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package rfb

import (
	"encoding/binary"
	"image"

	"github.com/YuanZhe-99/Kyokai/internal/bgra"
	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
	"github.com/YuanZhe-99/Kyokai/internal/rfb/mvs"
)

const (
	maximumCompressedRectangle = 128 << 20
	maximumInflatedRectangle   = 512 << 20
)

type decoders struct {
	zlib      zlibStream
	zrle      zlibStream
	halftone  zlibStream
	gray      zlibStream
	thousands zlibStream
	mvs       *mvs.Decoder
	payload   []byte
	tile      []byte
}

func (d *decoders) resetMVS(width, height int) {
	if d.mvs == nil {
		d.mvs = mvs.NewDecoder()
	}
	d.mvs.Reset(width, height)
}

func (d *decoders) readPayload(input *stream) ([]byte, error) {
	length, err := input.u32()
	if err != nil {
		return nil, err
	}
	if length > maximumCompressedRectangle {
		return nil, E.New("rfb: compressed payload of ", length, " bytes is over the ", maximumCompressedRectangle, "-byte limit")
	}
	if cap(d.payload) < int(length) {
		d.payload = make([]byte, length)
	}
	payload := d.payload[:length]
	return payload, input.readFull(payload)
}

func (d *decoders) decode(input *stream, framebuffer *bgra.Image, rectangle Rectangle) error {
	x, y, width, height := rectangle.X, rectangle.Y, rectangle.Width, rectangle.Height
	switch rectangle.Encoding {
	case EncodingRaw:
		return decodeRaw(input, framebuffer, x, y, width, height)
	case EncodingCopyRect:
		source, err := input.u32()
		if err != nil {
			return err
		}
		framebuffer.Copy(image.Pt(x, y), framebuffer, image.Rect(int(source>>16), int(source&0xffff), int(source>>16)+width, int(source&0xffff)+height))
		return nil
	case EncodingHextile:
		return d.decodeHextile(input, framebuffer, x, y, width, height)
	case EncodingZlib, EncodingAppleHalftone, EncodingAppleGray, EncodingAppleThousands:
		payload, err := d.readPayload(input)
		if err != nil {
			return err
		}
		var zlib *zlibStream
		var rowBytes int
		switch rectangle.Encoding {
		case EncodingZlib:
			zlib, rowBytes = &d.zlib, width*4
		case EncodingAppleHalftone:
			zlib, rowBytes = &d.halftone, (width+7)/8
		case EncodingAppleGray:
			zlib, rowBytes = &d.gray, (width+1)/2
		default:
			zlib, rowBytes = &d.thousands, width*2
		}
		data, err := zlib.inflate(payload, rowBytes*height)
		if err != nil {
			return err
		}
		if len(data) != rowBytes*height {
			return E.New("rfb: inflated ", len(data), " bytes, want ", rowBytes*height)
		}
		switch rectangle.Encoding {
		case EncodingZlib:
			blitPixels(framebuffer, x, y, width, height, data)
		case EncodingAppleHalftone:
			blitHalftone(framebuffer, x, y, width, height, data, rowBytes)
		case EncodingAppleGray:
			blitGray(framebuffer, x, y, width, height, data, rowBytes)
		default:
			blitThousands(framebuffer, x, y, width, height, data)
		}
		return nil
	case EncodingZRLE:
		payload, err := d.readPayload(input)
		if err != nil {
			return err
		}
		data, err := d.zrle.inflate(payload, maximumInflatedRectangle)
		if err != nil {
			return err
		}
		return d.decodeZRLE(framebuffer, x, y, width, height, data)
	case EncodingMVS:
		payload, err := d.readPayload(input)
		if err != nil {
			return err
		}
		return d.mvs.Decode(payload, x, y, width, height, framebuffer)
	default:
		return E.New("rfb: not a pixel encoding")
	}
}

func decodeRaw(input *stream, framebuffer *bgra.Image, x, y, width, height int) error {
	if image.Rect(x, y, x+width, y+height).In(framebuffer.Bounds()) {
		for line := 0; line < height; line++ {
			offset := (y+line)*framebuffer.Stride + x*4
			row := framebuffer.Pix[offset : offset+width*4]
			err := input.readFull(row)
			if err != nil {
				return err
			}
			for i := 3; i < len(row); i += 4 {
				row[i] = 0xff
			}
		}
		return nil
	}
	data, err := input.bytes(width * height * 4)
	if err != nil {
		return err
	}
	blitPixels(framebuffer, x, y, width, height, data)
	return nil
}

func blitPixels(framebuffer *bgra.Image, x, y, width, height int, data []byte) {
	clipped := image.Rect(x, y, x+width, y+height).Intersect(framebuffer.Bounds())
	clippedX, clippedY, clippedWidth, clippedHeight := clipped.Min.X, clipped.Min.Y, clipped.Dx(), clipped.Dy()
	for line := 0; line < clippedHeight; line++ {
		sourceOffset := ((clippedY-y+line)*width + clippedX - x) * 4
		source := data[sourceOffset : sourceOffset+clippedWidth*4]
		targetOffset := (clippedY+line)*framebuffer.Stride + clippedX*4
		target := framebuffer.Pix[targetOffset : targetOffset+clippedWidth*4]
		copy(target, source)
		for i := 3; i < len(target); i += 4 {
			target[i] = 0xff
		}
	}
}

func setPixel(framebuffer *bgra.Image, x, y int, pixel uint32) {
	if x < 0 || y < 0 || x >= framebuffer.Width || y >= framebuffer.Height {
		return
	}
	binary.LittleEndian.PutUint32(framebuffer.Pix[y*framebuffer.Stride+x*4:], pixel)
}

func grayPixel(value uint32) uint32 {
	return 0xff000000 | value<<16 | value<<8 | value
}

func blitHalftone(framebuffer *bgra.Image, x, y, width, height int, data []byte, rowBytes int) {
	for line := 0; line < height; line++ {
		row := data[line*rowBytes:]
		for column := 0; column < width; column++ {
			var value uint32
			if row[column/8]&(0x80>>(column%8)) != 0 {
				value = 0xff
			}
			setPixel(framebuffer, x+column, y+line, grayPixel(value))
		}
	}
}

func blitGray(framebuffer *bgra.Image, x, y, width, height int, data []byte, rowBytes int) {
	for line := 0; line < height; line++ {
		row := data[line*rowBytes:]
		for column := 0; column < width; column++ {
			nibble := uint32(row[column/2] >> 4)
			if column%2 == 1 {
				nibble = uint32(row[column/2] & 0x0f)
			}
			value := nibble * 16
			if nibble == 15 {
				value = 0xff
			}
			setPixel(framebuffer, x+column, y+line, grayPixel(value))
		}
	}
}

func expandFive(value uint32) uint32 {
	return value<<3 | value>>2
}

func blitThousands(framebuffer *bgra.Image, x, y, width, height int, data []byte) {
	for line := 0; line < height; line++ {
		for column := 0; column < width; column++ {
			value := uint32(binary.BigEndian.Uint16(data[(line*width+column)*2:]))
			red := expandFive(value >> 10 & 0x1f)
			green := expandFive(value >> 5 & 0x1f)
			blue := expandFive(value & 0x1f)
			setPixel(framebuffer, x+column, y+line, 0xff000000|red<<16|green<<8|blue)
		}
	}
}

const (
	hextileRaw                 = 1
	hextileBackgroundSpecified = 2
	hextileForegroundSpecified = 4
	hextileAnySubrects         = 8
	hextileSubrectsColoured    = 16
)

func (d *decoders) decodeHextile(input *stream, framebuffer *bgra.Image, x, y, width, height int) error {
	var background, foreground uint32
	for tileY := y; tileY < y+height; tileY += 16 {
		tileHeight := min(16, y+height-tileY)
		for tileX := x; tileX < x+width; tileX += 16 {
			tileWidth := min(16, x+width-tileX)
			subencoding, err := input.u8()
			if err != nil {
				return err
			}
			if subencoding&hextileRaw != 0 {
				if cap(d.tile) < 16*16*4 {
					d.tile = make([]byte, 64*64*4)
				}
				pixels := d.tile[:tileWidth*tileHeight*4]
				err = input.readFull(pixels)
				if err != nil {
					return err
				}
				blitPixels(framebuffer, tileX, tileY, tileWidth, tileHeight, pixels)
				continue
			}
			if subencoding&hextileBackgroundSpecified != 0 {
				background, err = input.u32()
				if err != nil {
					return err
				}
				background = pixelFromWire(background)
			}
			framebuffer.Fill(image.Rect(tileX, tileY, tileX+tileWidth, tileY+tileHeight), background)
			if subencoding&hextileForegroundSpecified != 0 {
				foreground, err = input.u32()
				if err != nil {
					return err
				}
				foreground = pixelFromWire(foreground)
			}
			if subencoding&hextileAnySubrects == 0 {
				continue
			}
			count, err := input.u8()
			if err != nil {
				return err
			}
			coloured := subencoding&hextileSubrectsColoured != 0
			for i := 0; i < int(count); i++ {
				color := foreground
				if coloured {
					color, err = input.u32()
					if err != nil {
						return err
					}
					color = pixelFromWire(color)
				}
				var geometry uint16
				geometry, err = input.u16()
				if err != nil {
					return err
				}
				subX, subY := int(geometry>>12), int(geometry>>8&0x0f)
				subWidth, subHeight := int(geometry>>4&0x0f)+1, int(geometry&0x0f)+1
				framebuffer.Fill(image.Rectangle{Min: image.Pt(tileX+subX, tileY+subY), Max: image.Pt(tileX+min(subX+subWidth, tileWidth), tileY+min(subY+subHeight, tileHeight))}, color)
			}
		}
	}
	return nil
}

func pixelFromWire(value uint32) uint32 {
	return (value>>24 | value>>8&0xff00 | value<<8&0xff0000) | 0xff000000
}

func (d *decoders) decodeZRLE(framebuffer *bgra.Image, x, y, width, height int, data []byte) error {
	position := 0
	take := func(length int) ([]byte, error) {
		if position+length > len(data) {
			return nil, E.New("rfb: ZRLE data ends inside a tile: ", length, " bytes needed at offset ", position, " of ", len(data))
		}
		value := data[position : position+length]
		position += length
		return value, nil
	}
	compactPixel := func() (uint32, error) {
		value, err := take(3)
		if err != nil {
			return 0, err
		}
		return 0xff000000 | uint32(value[2])<<16 | uint32(value[1])<<8 | uint32(value[0]), nil
	}
	var palette [128]uint32
	for tileY := y; tileY < y+height; tileY += 64 {
		tileHeight := min(64, y+height-tileY)
		for tileX := x; tileX < x+width; tileX += 64 {
			tileWidth := min(64, x+width-tileX)
			header, err := take(1)
			if err != nil {
				return err
			}
			subencoding := int(header[0])
			rle := subencoding&0x80 != 0
			paletteSize := subencoding & 0x7f
			for i := 0; i < paletteSize; i++ {
				palette[i], err = compactPixel()
				if err != nil {
					return err
				}
			}
			if !rle && paletteSize == 1 {
				framebuffer.Fill(image.Rect(tileX, tileY, tileX+tileWidth, tileY+tileHeight), palette[0])
				continue
			}
			count := tileWidth * tileHeight
			if cap(d.tile) < count*4 {
				d.tile = make([]byte, 64*64*4)
			}
			pixels := d.tile[:count*4]
			switch {
			case !rle && paletteSize == 0:
				for i := 0; i < count; i++ {
					var pixel uint32
					pixel, err = compactPixel()
					if err != nil {
						return err
					}
					binary.LittleEndian.PutUint32(pixels[i*4:], pixel)
				}
			case !rle:
				if paletteSize > 16 {
					return E.New("rfb: ZRLE packed palette of ", paletteSize, " colours, want at most 16")
				}
				bitsPerIndex := 4
				if paletteSize <= 2 {
					bitsPerIndex = 1
				} else if paletteSize <= 4 {
					bitsPerIndex = 2
				}
				rowBytes := (tileWidth*bitsPerIndex + 7) / 8
				var packed []byte
				packed, err = take(rowBytes * tileHeight)
				if err != nil {
					return err
				}
				mask := byte(1<<bitsPerIndex - 1)
				for line := 0; line < tileHeight; line++ {
					row := packed[line*rowBytes:]
					for column := 0; column < tileWidth; column++ {
						bit := column * bitsPerIndex
						index := row[bit/8] >> (8 - bitsPerIndex - bit%8) & mask
						binary.LittleEndian.PutUint32(pixels[(line*tileWidth+column)*4:], palette[index])
					}
				}
			default:
				filled := 0
				for filled < count {
					var pixel uint32
					run := 1
					if paletteSize == 0 {
						pixel, err = compactPixel()
						if err != nil {
							return err
						}
						run, err = zrleRunLength(take)
						if err != nil {
							return err
						}
					} else {
						var indexByte []byte
						indexByte, err = take(1)
						if err != nil {
							return err
						}
						index := int(indexByte[0] & 0x7f)
						if index >= paletteSize {
							return E.New("rfb: ZRLE palette index ", index, " is outside a palette of ", paletteSize)
						}
						pixel = palette[index]
						if indexByte[0]&0x80 != 0 {
							run, err = zrleRunLength(take)
							if err != nil {
								return err
							}
						}
					}
					if filled+run > count {
						return E.New("rfb: ZRLE run of ", run, " pixels at pixel ", filled, " passes the ", count, "-pixel tile")
					}
					for i := filled; i < filled+run; i++ {
						binary.LittleEndian.PutUint32(pixels[i*4:], pixel)
					}
					filled += run
				}
			}
			blitPixelsWithAlpha(framebuffer, tileX, tileY, tileWidth, tileHeight, pixels)
		}
	}
	if position != len(data) {
		return E.New("rfb: ", len(data)-position, " bytes left after the ZRLE tiles")
	}
	return nil
}

func zrleRunLength(take func(int) ([]byte, error)) (int, error) {
	run := 1
	for {
		value, err := take(1)
		if err != nil {
			return 0, err
		}
		run += int(value[0])
		if value[0] != 255 {
			return run, nil
		}
		if run > 64*64 {
			return 0, E.New("rfb: ZRLE run of at least ", run, " pixels passes a 64x64 tile")
		}
	}
}

func blitPixelsWithAlpha(framebuffer *bgra.Image, x, y, width, height int, data []byte) {
	clipped := image.Rect(x, y, x+width, y+height).Intersect(framebuffer.Bounds())
	clippedX, clippedY, clippedWidth, clippedHeight := clipped.Min.X, clipped.Min.Y, clipped.Dx(), clipped.Dy()
	for line := 0; line < clippedHeight; line++ {
		sourceOffset := ((clippedY-y+line)*width + clippedX - x) * 4
		targetOffset := (clippedY+line)*framebuffer.Stride + clippedX*4
		copy(framebuffer.Pix[targetOffset:targetOffset+clippedWidth*4], data[sourceOffset:sourceOffset+clippedWidth*4])
	}
}
