// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/codec/h264/convert.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package h264

import (
	"encoding/binary"
	"image"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/YuanZhe-99/Kyokai/internal/bgra"
)

const bandPixels = 64 * 1024

type frame444 struct {
	y      []byte
	u      []byte
	v      []byte
	width  int
	height int
}

func (f *frame444) ensure(width, height int) {
	if f.width == width && f.height == height {
		return
	}
	f.y = make([]byte, width*height)
	f.u = make([]byte, width*height)
	f.v = make([]byte, width*height)
	f.width, f.height = width, height
}

func forEachBand(rects []image.Rectangle, work func(image.Rectangle)) {
	total := 0
	for _, r := range rects {
		total += r.Dx() * r.Dy()
	}
	workers := min(runtime.GOMAXPROCS(0), total/bandPixels)
	if workers <= 1 {
		for _, r := range rects {
			work(r)
		}
		return
	}
	for i, r := range rects {
		if slices.ContainsFunc(rects[i+1:], r.Overlaps) {
			for i := range rects {
				forEachBand(rects[i:i+1], work)
			}
			return
		}
	}
	var bands []image.Rectangle
	for _, r := range rects {
		rows := max(2, bandPixels/r.Dx()&^1)
		for y := r.Min.Y; y < r.Max.Y; y += rows {
			bands = append(bands, image.Rect(r.Min.X, y, r.Max.X, min(y+rows, r.Max.Y)))
		}
	}
	var next atomic.Int64
	var group sync.WaitGroup
	for range workers {
		group.Go(func() {
			for {
				index := int(next.Add(1) - 1)
				if index >= len(bands) {
					return
				}
				work(bands[index])
			}
		})
	}
	group.Wait()
}

var clampBlue, clampGreen, clampRed = func() (blue, green, red [1024]uint32) {
	for i := range blue {
		value := uint32(min(max(i-256, 0), 255))
		blue[i], green[i], red[i] = value, value<<8, value<<16|0xFF000000
	}
	return
}()

func chromaOffsets(u, v byte) (red, green, blue int32) {
	cb, cr := int32(u)-128, int32(v)-128
	return (403*cr)>>8 + 256, (-48*cb-120*cr)>>8 + 256, (475*cb)>>8 + 256
}

func pixel(y byte, red, green, blue int32) uint32 {
	luma := int32(y)
	return clampBlue[(luma+blue)&1023] | clampGreen[(luma+green)&1023] | clampRed[(luma+red)&1023]
}

func reverseFilter(a, b, c, d byte) byte {
	filtered := byte(min(max(4*int32(a)-int32(b)-int32(c)-int32(d), 0), 255))
	if max(filtered, a)-min(filtered, a) < 30 {
		return a
	}
	return filtered
}

func convertNV12(dst *bgra.Image, r image.Rectangle, p *picture) {
	width := r.Dx()
	chromaStart := r.Min.X &^ 1
	chromaEnd := (r.Min.X + width + 1) &^ 1
	for row := range r.Dy() {
		y := r.Min.Y + row
		luma := p.luma[y*p.lumaStride+r.Min.X:][:width]
		chroma := p.chroma[y/2*p.chromaStride+chromaStart:][:chromaEnd-chromaStart]
		out := dst.Pix[dst.Offset(r.Min.X, r.Min.Y+row):][:width*4]
		i, c := 0, 0
		if r.Min.X&1 == 1 {
			red, green, blue := chromaOffsets(chroma[0], chroma[1])
			binary.LittleEndian.PutUint32(out, pixel(luma[0], red, green, blue))
			i, c = 1, 2
		}
		for ; i+1 < width; i, c = i+2, c+2 {
			pair := chroma[c : c+2 : c+2]
			red, green, blue := chromaOffsets(pair[0], pair[1])
			pixels := luma[i : i+2 : i+2]
			binary.LittleEndian.PutUint64(out[i*4:i*4+8:i*4+8], uint64(pixel(pixels[0], red, green, blue))|uint64(pixel(pixels[1], red, green, blue))<<32)
		}
		if i < width {
			red, green, blue := chromaOffsets(chroma[c], chroma[c+1])
			binary.LittleEndian.PutUint32(out[i*4:], pixel(luma[i], red, green, blue))
		}
	}
}

func convertYUV444(dst *bgra.Image, r image.Rectangle, f *frame444) {
	width := r.Dx()
	stride := f.width
	end := min(width+1, stride-r.Min.X)
	for row := range r.Dy() {
		y := r.Min.Y + row
		offset := y*stride + r.Min.X
		lumaRow := f.y[offset:][:width]
		out := dst.Pix[dst.Offset(r.Min.X, r.Min.Y+row):][:width*4]
		if y&1 == 1 {
			uRow := f.u[offset:][:width]
			vRow := f.v[offset:][:width]
			for i, luma := range lumaRow {
				red, green, blue := chromaOffsets(uRow[i], vRow[i])
				binary.LittleEndian.PutUint32(out[i*4:i*4+4:i*4+4], pixel(luma, red, green, blue))
			}
			continue
		}
		uRow := f.u[offset:][:end]
		vRow := f.v[offset:][:end]
		uNext := f.u[offset+stride:][:end]
		vNext := f.v[offset+stride:][:end]
		i := 0
		if r.Min.X&1 == 1 {
			red, green, blue := chromaOffsets(uRow[0], vRow[0])
			binary.LittleEndian.PutUint32(out, pixel(lumaRow[0], red, green, blue))
			i++
		}
		for ; i+1 < width; i += 2 {
			u, v := uRow[i:i+2:i+2], vRow[i:i+2:i+2]
			uBelow, vBelow := uNext[i:i+2:i+2], vNext[i:i+2:i+2]
			evenRed, evenGreen, evenBlue := chromaOffsets(reverseFilter(u[0], u[1], uBelow[0], uBelow[1]), reverseFilter(v[0], v[1], vBelow[0], vBelow[1]))
			oddRed, oddGreen, oddBlue := chromaOffsets(u[1], v[1])
			pixels := lumaRow[i : i+2 : i+2]
			binary.LittleEndian.PutUint64(out[i*4:i*4+8:i*4+8], uint64(pixel(pixels[0], evenRed, evenGreen, evenBlue))|uint64(pixel(pixels[1], oddRed, oddGreen, oddBlue))<<32)
		}
		if i < width {
			red, green, blue := chromaOffsets(reverseFilter(uRow[i], uRow[i+1], uNext[i], uNext[i+1]), reverseFilter(vRow[i], vRow[i+1], vNext[i], vNext[i+1]))
			binary.LittleEndian.PutUint32(out[i*4:], pixel(lumaRow[i], red, green, blue))
		}
	}
}

func combineMain(f *frame444, p *picture, r image.Rectangle) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		offset := y * f.width
		copy(f.y[offset+r.Min.X:offset+r.Max.X], p.luma[y*p.lumaStride+r.Min.X:])
		chroma := p.chroma[y/2*p.chromaStride:][:r.Max.X]
		uRow := f.u[offset:][:r.Max.X]
		vRow := f.v[offset:][:r.Max.X]
		for x := r.Min.X; x+1 < r.Max.X; x += 2 {
			u, v := chroma[x], chroma[x+1]
			uRow[x], uRow[x+1] = u, u
			vRow[x], vRow[x+1] = v, v
		}
	}
}

func combineAuxV1(f *frame444, p *picture, r image.Rectangle) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		offset := y * f.width
		if y&1 == 1 {
			half := y / 2
			source := half/8*16 + half%8
			if source < p.height {
				copy(f.u[offset+r.Min.X:offset+r.Max.X], p.luma[source*p.lumaStride+r.Min.X:])
			}
			if source+8 < p.height {
				copy(f.v[offset+r.Min.X:offset+r.Max.X], p.luma[(source+8)*p.lumaStride+r.Min.X:])
			}
			continue
		}
		chroma := p.chroma[y/2*p.chromaStride:][:r.Max.X]
		uRow := f.u[offset:][:r.Max.X]
		vRow := f.v[offset:][:r.Max.X]
		for x := r.Min.X + 1; x < r.Max.X; x += 2 {
			uRow[x] = chroma[x-1]
			vRow[x] = chroma[x]
		}
	}
}

func combineAuxV2(f *frame444, p *picture, r image.Rectangle) {
	total := min((f.width+31)&^31, p.lumaStride, p.chromaStride)
	half := total / 2
	quarter := total / 4
	for y := r.Min.Y; y < r.Max.Y; y++ {
		offset := y * f.width
		luma := p.luma[y*p.lumaStride:][:total]
		uRow := f.u[offset:][:r.Max.X]
		vRow := f.v[offset:][:r.Max.X]
		for x := r.Min.X + 1; x < r.Max.X; x += 2 {
			uRow[x] = luma[x/2]
			vRow[x] = luma[half+x/2]
		}
		if y&1 == 0 {
			continue
		}
		chroma := p.chroma[y/2*p.chromaStride:][:total]
		for x := r.Min.X; x < r.Max.X; x += 2 {
			column := x / 4 * 2
			if x&2 != 0 {
				column++
			}
			uRow[x] = chroma[column]
			vRow[x] = chroma[quarter*2+column]
		}
	}
}
