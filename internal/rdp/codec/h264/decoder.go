// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/codec/h264/decoder.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package h264

import (
	"encoding/binary"
	"image"
	"slices"

	"github.com/YuanZhe-99/Kyokai/internal/bgra"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const (
	metablockCountSize   = 4
	metablockRectSize    = 8
	metablockQualitySize = 2

	avc444InfoSize  = 4
	avc444SizeMask  = 0x3FFFFFFF
	avc444LCShift   = 30
	avc444LCBoth    = 0
	avc444LCLuma    = 1
	avc444LCChroma  = 2
	avc444LCInvalid = 3
)

type picture struct {
	luma         []byte
	chroma       []byte
	lumaStride   int
	chromaStride int
	width        int
	height       int
}

type Decoder struct {
	session  videoSession
	units    [][]byte
	rects    []image.Rectangle
	auxRects []image.Rectangle
	combined []image.Rectangle
	frame    frame444
}

func (d *Decoder) Close() {
	d.session.close()
	d.frame = frame444{}
}

func (d *Decoder) DecodeAVC420(src []byte, dst *bgra.Image) ([]image.Rectangle, error) {
	rects, bitstream, err := parseMetablock(src, d.rects)
	d.rects = rects
	if err != nil {
		return nil, err
	}
	defer d.session.release()
	decoded, ok, err := d.decodePicture(bitstream, dst)
	if err != nil || !ok {
		return nil, err
	}
	area := dst.Bounds().Intersect(image.Rect(0, 0, decoded.width, decoded.height))
	var updated []image.Rectangle
	for _, r := range rects {
		r = r.Intersect(area)
		if !r.Empty() {
			updated = append(updated, r)
		}
	}
	forEachBand(updated, func(r image.Rectangle) {
		convertNV12(dst, r, &decoded)
	})
	return updated, nil
}

func (d *Decoder) DecodeAVC444(src []byte, version2 bool, dst *bgra.Image) ([]image.Rectangle, error) {
	if len(src) < avc444InfoSize {
		return nil, E.New("h264: truncated AVC444 header: ", len(src), " of ", avc444InfoSize, " bytes")
	}
	info := binary.LittleEndian.Uint32(src)
	size := int(info & avc444SizeMask)
	src = src[avc444InfoSize:]
	var mainStream, auxStream []byte
	switch info >> avc444LCShift {
	case avc444LCBoth:
		if size > len(src) {
			return nil, E.New("h264: AVC444 main stream declares ", size, " bytes, ", len(src), " remain")
		}
		mainStream, auxStream = src[:size], src[size:]
	case avc444LCLuma:
		mainStream = src
	case avc444LCChroma:
		auxStream = src
	case avc444LCInvalid:
		return nil, E.New("h264: invalid AVC444 LC value ", avc444LCInvalid)
	}
	var (
		mainRects, auxRects         []image.Rectangle
		mainBitstream, auxBitstream []byte
		err                         error
	)
	if mainStream != nil {
		mainRects, mainBitstream, err = parseMetablock(mainStream, d.rects)
		d.rects = mainRects
		if err != nil {
			return nil, err
		}
	}
	if auxStream != nil {
		auxRects, auxBitstream, err = parseMetablock(auxStream, d.auxRects)
		d.auxRects = auxRects
		if err != nil {
			return nil, err
		}
	}
	defer d.session.release()
	var updated []image.Rectangle
	if mainStream != nil {
		updated, err = d.combine(updated, mainBitstream, mainRects, dst, true, combineMain)
		if err != nil {
			return nil, err
		}
	}
	if auxStream != nil {
		combineAux := combineAuxV1
		if version2 {
			combineAux = combineAuxV2
		}
		updated, err = d.combine(updated, auxBitstream, auxRects, dst, false, combineAux)
		if err != nil {
			return nil, err
		}
	}
	forEachBand(updated, func(r image.Rectangle) {
		convertYUV444(dst, r, &d.frame)
	})
	return updated, nil
}

func (d *Decoder) combine(updated []image.Rectangle, bitstream []byte, rects []image.Rectangle, dst *bgra.Image, main bool, combine func(*frame444, *picture, image.Rectangle)) ([]image.Rectangle, error) {
	decoded, ok, err := d.decodePicture(bitstream, dst)
	if err != nil || !ok {
		return updated, err
	}
	if decoded.width%2 != 0 || decoded.height%2 != 0 {
		return updated, E.New("h264: odd AVC444 picture size ", decoded.width, "x", decoded.height)
	}
	if main {
		d.frame.ensure(decoded.width, decoded.height)
	} else if decoded.width != d.frame.width || decoded.height != d.frame.height {
		return updated, E.New("h264: AVC444 auxiliary picture size ", decoded.width, "x", decoded.height, " does not match main picture ", d.frame.width, "x", d.frame.height)
	}
	area := dst.Bounds().Intersect(image.Rect(0, 0, decoded.width, decoded.height))
	d.combined = d.combined[:0]
	for _, r := range rects {
		r = r.Intersect(area)
		if r.Empty() {
			continue
		}
		if !slices.Contains(updated, r) {
			updated = append(updated, r)
		}
		r.Min.X &^= 1
		r.Min.Y &^= 1
		r.Max.X += r.Max.X & 1
		r.Max.Y += r.Max.Y & 1
		if !slices.Contains(d.combined, r) {
			d.combined = append(d.combined, r)
		}
	}
	forEachBand(d.combined, func(r image.Rectangle) {
		combine(&d.frame, &decoded, r)
	})
	return updated, nil
}

func (d *Decoder) decodePicture(bitstream []byte, dst *bgra.Image) (picture, bool, error) {
	units, err := splitAnnexB(bitstream, d.units)
	if err != nil {
		return picture{}, false, err
	}
	d.units = units
	decoded, ok, err := d.session.decode(units, dst.Width, dst.Height)
	clear(units)
	return decoded, ok, err
}

func parseMetablock(data []byte, rects []image.Rectangle) ([]image.Rectangle, []byte, error) {
	rects = rects[:0]
	if len(data) < metablockCountSize {
		return rects, nil, E.New("h264: truncated metablock header: ", len(data), " of ", metablockCountSize, " bytes")
	}
	count := uint64(binary.LittleEndian.Uint32(data))
	data = data[metablockCountSize:]
	if count*(metablockRectSize+metablockQualitySize) > uint64(len(data)) {
		return rects, nil, E.New("h264: metablock declares ", count, " rectangles, ", len(data), " bytes remain")
	}
	for i := range int(count) {
		field := data[i*metablockRectSize:]
		left := int(binary.LittleEndian.Uint16(field))
		top := int(binary.LittleEndian.Uint16(field[2:]))
		right := int(binary.LittleEndian.Uint16(field[4:]))
		bottom := int(binary.LittleEndian.Uint16(field[6:]))
		if left > right || top > bottom {
			return rects, nil, E.New("h264: invalid region rectangle ", left, ",", top, ",", right, ",", bottom)
		}
		rects = append(rects, image.Rectangle{Min: image.Pt(left, top), Max: image.Pt(right, bottom)})
	}
	return rects, data[count*(metablockRectSize+metablockQualitySize):], nil
}
