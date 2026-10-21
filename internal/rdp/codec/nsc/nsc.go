// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/codec/nsc/nsc.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package nsc

import (
	"encoding/binary"

	"github.com/YuanZhe-99/Kyokai/internal/bgra"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const (
	headerSize        = 20
	colorLossOffset   = 16
	subsamplingOffset = 17
	planeCount        = 4
	minColorLossLevel = 1
	maxColorLossLevel = 7
	rawTailSize       = 4
	runHeaderSize     = 3
	longRunHeaderSize = 7
	longRunMarker     = 0xFF
)

type Decoder struct {
	rows [planeCount][]byte
}

type planeReader struct {
	src   []byte
	left  int
	value byte
	run   int
	raw   int
}

func (d *Decoder) Decode(src []byte, width, height int, bottomUp bool, dst *bgra.Image) error {
	if len(src) < headerSize {
		return E.New("nsc: stream of ", len(src), " bytes is shorter than its header")
	}
	colorLossLevel := int(src[colorLossOffset])
	if colorLossLevel < minColorLossLevel || colorLossLevel > maxColorLossLevel {
		return E.New("nsc: color loss level ", colorLossLevel, " out of range")
	}
	subsampled := src[subsamplingOffset] != 0
	lumaWidth, chromaWidth, chromaHeight := width, width, height
	if subsampled {
		lumaWidth = (width + 7) &^ 7
		chromaWidth = lumaWidth / 2
		chromaHeight = (height + 1) / 2
	}
	planeSizes := [planeCount]int{lumaWidth * height, chromaWidth * chromaHeight, chromaWidth * chromaHeight, width * height}
	payload := src[headerSize:]
	var readers [planeCount]planeReader
	for i := range readers {
		byteCount := binary.LittleEndian.Uint32(src[i*4:])
		if uint64(byteCount) > uint64(len(payload)) {
			return E.New("nsc: plane ", i, " needs ", byteCount, " bytes, ", len(payload), " remain")
		}
		plane := payload[:byteCount]
		payload = payload[byteCount:]
		reader := &readers[i]
		switch size := planeSizes[i]; {
		case len(plane) == 0:
			reader.value, reader.run = 0xFF, size
		case len(plane) >= size:
			reader.src, reader.raw = plane, size
		default:
			reader.src, reader.left = plane, size
		}
	}
	visibleWidth, visibleHeight := min(width, dst.Width), min(height, dst.Height)
	if visibleWidth <= 0 || visibleHeight <= 0 {
		return nil
	}
	rowSizes := [planeCount]int{lumaWidth, chromaWidth, chromaWidth, width}
	for i, size := range rowSizes {
		if cap(d.rows[i]) < size {
			d.rows[i] = make([]byte, size)
		}
		d.rows[i] = d.rows[i][:size]
	}
	lumaRow, coRow, cgRow, alphaRow := d.rows[0], d.rows[1], d.rows[2], d.rows[3]
	shift := uint(colorLossLevel - 1)
	var chromaShift uint
	if subsampled {
		chromaShift = 1
	}
	for row := range height {
		y := row
		if bottomUp {
			y = height - 1 - row
		} else if y >= visibleHeight {
			break
		}
		err := readers[0].read(lumaRow)
		if err != nil {
			return E.Cause(err, "nsc: decode luma plane")
		}
		if !subsampled || row&1 == 0 {
			err = readers[1].read(coRow)
			if err != nil {
				return E.Cause(err, "nsc: decode orange chroma plane")
			}
			err = readers[2].read(cgRow)
			if err != nil {
				return E.Cause(err, "nsc: decode green chroma plane")
			}
		}
		err = readers[3].read(alphaRow)
		if err != nil {
			return E.Cause(err, "nsc: decode alpha plane")
		}
		if y >= visibleHeight {
			continue
		}
		out := dst.Pix[y*dst.Stride:][:visibleWidth*4]
		alpha := alphaRow[:visibleWidth]
		for x, value := range lumaRow[:visibleWidth] {
			luma := int(value)
			co := int(int8(coRow[x>>chromaShift] << shift))
			cg := int(int8(cgRow[x>>chromaShift] << shift))
			pixel := out[x*4 : x*4+4 : x*4+4]
			pixel[0] = byte(min(max(luma-co-cg, 0), 0xFF))
			pixel[1] = byte(min(max(luma+cg, 0), 0xFF))
			pixel[2] = byte(min(max(luma+co-cg, 0), 0xFF))
			pixel[3] = alpha[x]
		}
	}
	return nil
}

func (r *planeReader) read(out []byte) error {
	for len(out) > 0 {
		if r.run > 0 {
			n := min(r.run, len(out))
			out[0] = r.value
			for filled := 1; filled < n; filled *= 2 {
				copy(out[filled:n], out[:filled])
			}
			out = out[n:]
			r.run -= n
			continue
		}
		if r.raw > 0 {
			n := copy(out[:min(r.raw, len(out))], r.src)
			out = out[n:]
			r.src = r.src[n:]
			r.raw -= n
			continue
		}
		literals := min(len(out), r.left-rawTailSize-1, len(r.src)-1)
		i := 0
		for i < literals && r.src[i] != r.src[i+1] {
			out[i] = r.src[i]
			i++
		}
		if i > 0 {
			out = out[i:]
			r.src = r.src[i:]
			r.left -= i
			continue
		}
		switch {
		case r.left > rawTailSize:
			if len(r.src) == 0 {
				return E.New("truncated RLE data with ", r.left, " bytes to decode")
			}
			value := r.src[0]
			if r.left == rawTailSize+1 {
				out[0] = value
				out = out[1:]
				r.src = r.src[1:]
				r.left--
				continue
			}
			if len(r.src) < 2 {
				return E.New("truncated RLE data with ", r.left, " bytes to decode")
			}
			if r.src[1] != value {
				out[0] = value
				out = out[1:]
				r.src = r.src[1:]
				r.left--
				continue
			}
			if len(r.src) < runHeaderSize {
				return E.New("truncated run header: ", len(r.src), " of 3 bytes")
			}
			var length int
			if r.src[2] < longRunMarker {
				length = int(r.src[2]) + 2
				r.src = r.src[runHeaderSize:]
			} else {
				if len(r.src) < longRunHeaderSize {
					return E.New("truncated long run header: ", len(r.src), " of 7 bytes")
				}
				length = int(binary.LittleEndian.Uint32(r.src[runHeaderSize:]))
				r.src = r.src[longRunHeaderSize:]
			}
			if length > r.left-rawTailSize {
				return E.New("run of ", length, " bytes exceeds the ", r.left-rawTailSize, " bytes left before the raw tail")
			}
			r.left -= length
			r.value, r.run = value, length
		case r.left == rawTailSize:
			if len(r.src) < rawTailSize {
				return E.New("truncated raw tail: ", len(r.src), " of 4 bytes")
			}
			r.left, r.raw = 0, rawTailSize
		default:
			return E.New("RLE plane has ", r.left, " bytes left, short of its 4-byte raw tail")
		}
	}
	return nil
}
