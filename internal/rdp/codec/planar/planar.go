// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/codec/planar/planar.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package planar

import (
	"encoding/binary"

	"github.com/YuanZhe-99/Kyokai/internal/bgra"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const (
	formatHeaderCLLMask = 0x07
	formatHeaderCS      = 0x08
	formatHeaderRLE     = 0x10
	formatHeaderNA      = 0x20

	controlRunMask  = 0x0F
	controlRawShift = 4
	runExtended16   = 1
	runExtended32   = 2
)

const (
	planeAlpha = iota
	planeLuma
	planeOrange
	planeGreen
)

type Decoder struct {
	rows   [8][]byte
	opaque []byte
}

type plane struct {
	data      []byte
	width     int
	rle       bool
	started   bool
	cur, prev []byte
}

func (p *plane) next() error {
	if !p.rle {
		if len(p.data) < p.width {
			return E.New("truncated raw scanline: ", len(p.data), " of ", p.width, " bytes")
		}
		p.cur = p.data[:p.width]
		p.data = p.data[p.width:]
		return nil
	}
	p.cur, p.prev = p.prev, p.cur
	var previous []byte
	if p.started {
		previous = p.prev
	}
	p.started = true
	n, err := decodeRow(p.data, p.cur, previous)
	if err != nil {
		return err
	}
	p.data = p.data[n:]
	return nil
}

func (d *Decoder) Decode(src []byte, width, height int, bottomUp bool, dst *bgra.Image) error {
	if len(src) == 0 {
		return E.New("planar: missing format header")
	}
	header := src[0]
	data := src[1:]
	colorLossLevel := header & formatHeaderCLLMask
	subsampled := header&formatHeaderCS != 0
	rle := header&formatHeaderRLE != 0
	hasAlpha := header&formatHeaderNA == 0
	if subsampled && colorLossLevel == 0 {
		return E.New("planar: chroma subsampling without color loss reduction")
	}
	chromaWidth, chromaHeight := width, height
	if subsampled {
		chromaWidth, chromaHeight = (width+1)/2, (height+1)/2
	}
	sizes := [4][2]int{{width, height}, {width, height}, {chromaWidth, chromaHeight}, {chromaWidth, chromaHeight}}

	var planes [4]plane
	stored := planeAlpha
	if !hasAlpha {
		stored = planeLuma
	}
	for i := stored; i < len(planes); i++ {
		planeWidth, planeHeight := sizes[i][0], sizes[i][1]
		size := planeWidth * planeHeight
		if rle {
			n, err := rleSize(data, planeWidth, planeHeight)
			if err != nil {
				return E.Cause(err, "planar: measure RLE plane ", i)
			}
			size = n
		} else if len(data) < size {
			return E.New("planar: truncated plane ", i, ": ", len(data), " of ", size, " bytes")
		}
		planes[i] = plane{data: data[:size], width: planeWidth, rle: rle}
		data = data[size:]
	}

	decoded := planeAlpha
	if !hasAlpha {
		decoded = planeLuma
		if len(d.opaque) < width {
			d.opaque = make([]byte, width)
			for i := range d.opaque {
				d.opaque[i] = 0xFF
			}
		}
	}
	if rle {
		for i := decoded; i < len(planes); i++ {
			planeWidth := sizes[i][0]
			for j := 2 * i; j < 2*i+2; j++ {
				if cap(d.rows[j]) < planeWidth {
					d.rows[j] = make([]byte, planeWidth)
				}
			}
			planes[i].cur = d.rows[2*i][:planeWidth]
			planes[i].prev = d.rows[2*i+1][:planeWidth]
		}
	}

	outWidth, outHeight := min(width, dst.Width), min(height, dst.Height)
	shift := colorLossLevel - 1
	var chromaShift uint
	if subsampled {
		chromaShift = 1
	}
	var negate int32
	if !hasAlpha {
		negate = -1
	}
	for row := range height {
		for i := decoded; i < len(planes); i++ {
			if i >= planeOrange && subsampled && row&1 != 0 {
				continue
			}
			err := planes[i].next()
			if err != nil {
				return E.Cause(err, "planar: decode plane ", i, " scanline ", row)
			}
		}
		y := row
		if bottomUp {
			y = height - 1 - row
		}
		if y >= outHeight || outWidth <= 0 {
			continue
		}
		alpha := d.opaque
		if hasAlpha {
			alpha = planes[planeAlpha].cur
		}
		out := dst.Pix[y*dst.Stride:][:outWidth*4]
		if colorLossLevel == 0 {
			writeRGB(out, planes[planeLuma].cur, planes[planeOrange].cur, planes[planeGreen].cur, alpha)
		} else {
			writeYCoCg(out, planes[planeLuma].cur, planes[planeOrange].cur, planes[planeGreen].cur, alpha, shift, chromaShift, negate)
		}
	}
	return nil
}

func rleSize(src []byte, width, height int) (int, error) {
	pos := 0
	for y := range height {
		for x := 0; x < width; {
			if pos >= len(src) {
				return 0, E.New("truncated control byte at scanline ", y, " column ", x)
			}
			control := src[pos]
			run, raw := int(control&controlRunMask), int(control>>controlRawShift)
			switch run {
			case runExtended16:
				run, raw = raw+16, 0
			case runExtended32:
				run, raw = raw+32, 0
			}
			pos += 1 + raw
			x += raw + run
			if x > width {
				return 0, E.New("segment at scanline ", y, " ends at column ", x, " past the width of ", width)
			}
		}
	}
	if pos > len(src) {
		return 0, E.New("truncated raw values: ", len(src), " of ", pos, " bytes")
	}
	return pos, nil
}

func decodeRow(src []byte, cur []byte, previous []byte) (int, error) {
	width := len(cur)
	pos := 0
	var last byte
	for x := 0; x < width; {
		if pos >= len(src) {
			return 0, E.New("truncated control byte at column ", x)
		}
		control := src[pos]
		pos++
		run, raw := int(control&controlRunMask), int(control>>controlRawShift)
		switch run {
		case runExtended16:
			run, raw = raw+16, 0
		case runExtended32:
			run, raw = raw+32, 0
		}
		end := x + raw + run
		if end > width {
			return 0, E.New("segment ends at column ", end, " past the width of ", width)
		}
		if raw > len(src)-pos {
			return 0, E.New("truncated raw values at column ", x, ": ", len(src)-pos, " of ", raw, " bytes")
		}
		values := src[pos : pos+raw]
		pos += raw
		segment := cur[x:end]
		if previous == nil {
			copy(segment, values)
			if raw > 0 {
				last = values[raw-1]
			}
			for i := raw; i < len(segment); i++ {
				segment[i] = last
			}
		} else {
			above := previous[x:end]
			head, headAbove := segment[:raw], above[:raw]
			for i, value := range values {
				last = value>>1 ^ -(value & 1)
				head[i] = headAbove[i] + last
			}
			tail, tailAbove := segment[raw:], above[raw:]
			tailAbove = tailAbove[:len(tail)]
			for i := range tail {
				tail[i] = tailAbove[i] + last
			}
		}
		x = end
	}
	return pos, nil
}

func writeRGB(out []byte, red []byte, green []byte, blue []byte, alpha []byte) {
	red = red[:len(out)/4]
	green = green[:len(red)]
	blue = blue[:len(red)]
	alpha = alpha[:len(red)]
	for x, r := range red {
		binary.LittleEndian.PutUint32(out[x*4:x*4+4], uint32(blue[x])|uint32(green[x])<<8|uint32(r)<<16|uint32(alpha[x])<<24)
	}
}

func writeYCoCg(out []byte, luma []byte, orange []byte, green []byte, alpha []byte, shift uint8, chromaShift uint, negate int32) {
	luma = luma[:len(out)/4]
	alpha = alpha[:len(luma)]
	for x, l := range luma {
		cx := x >> chromaShift
		y := int32(l)
		co := int32(int8(orange[cx] << shift))
		cg := int32(int8(green[cx] << shift))
		co = co ^ negate - negate
		t := y - cg
		r := uint32(min(max(t+co, 0), 255))
		g := uint32(min(max(y+cg, 0), 255))
		b := uint32(min(max(t-co, 0), 255))
		binary.LittleEndian.PutUint32(out[x*4:x*4+4], b|g<<8|r<<16|uint32(alpha[x])<<24)
	}
}
