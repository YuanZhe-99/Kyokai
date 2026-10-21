// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/bgra/bgra.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package bgra

import (
	"encoding/binary"
	"image"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

type Image struct {
	Pix    []byte
	Stride int
	Width  int
	Height int
}

func New(width, height int) *Image {
	return &Image{Pix: make([]byte, width*height*4), Stride: width * 4, Width: width, Height: height}
}

func (m *Image) Validate() error {
	if m.Width <= 0 || m.Height <= 0 {
		return E.New("bgra: image size ", m.Width, "x", m.Height, " is empty")
	}
	if m.Stride < m.Width*4 {
		return E.New("bgra: image stride ", m.Stride, " is below ", m.Width*4, " for width ", m.Width)
	}
	required := int64(m.Stride)*int64(m.Height-1) + int64(m.Width)*4
	if int64(len(m.Pix)) < required {
		return E.New("bgra: image memory of ", len(m.Pix), " bytes, want ", required, " for ", m.Width, "x", m.Height, " with stride ", m.Stride)
	}
	return nil
}

func (m *Image) Bounds() image.Rectangle {
	return image.Rect(0, 0, m.Width, m.Height)
}

func (m *Image) Offset(x, y int) int {
	return y*m.Stride + x*4
}

func (m *Image) SubImage(r image.Rectangle) *Image {
	r = r.Intersect(m.Bounds())
	if r.Empty() {
		return &Image{}
	}
	start := m.Offset(r.Min.X, r.Min.Y)
	end := m.Offset(r.Max.X-1, r.Max.Y-1) + 4
	return &Image{Pix: m.Pix[start:end:end], Stride: m.Stride, Width: r.Dx(), Height: r.Dy()}
}

func (m *Image) Fill(r image.Rectangle, pixel uint32) {
	r = r.Intersect(m.Bounds())
	if r.Empty() {
		return
	}
	row := m.Pix[m.Offset(r.Min.X, r.Min.Y):][:r.Dx()*4]
	for i := 0; i < len(row); i += 4 {
		binary.LittleEndian.PutUint32(row[i:], pixel)
	}
	for y := r.Min.Y + 1; y < r.Max.Y; y++ {
		copy(m.Pix[m.Offset(r.Min.X, y):][:len(row)], row)
	}
}

func (m *Image) Copy(at image.Point, source *Image, r image.Rectangle) {
	clipped := r.Intersect(source.Bounds())
	at = at.Add(clipped.Min.Sub(r.Min))
	r = clipped
	target := r.Sub(r.Min).Add(at).Intersect(m.Bounds())
	if target.Empty() {
		return
	}
	r.Min = r.Min.Add(target.Min.Sub(at))
	width := target.Dx() * 4
	if m == source && target.Min.Y > r.Min.Y {
		for y := target.Dy() - 1; y >= 0; y-- {
			copy(m.Pix[m.Offset(target.Min.X, target.Min.Y+y):][:width], source.Pix[source.Offset(r.Min.X, r.Min.Y+y):][:width])
		}
		return
	}
	for y := 0; y < target.Dy(); y++ {
		copy(m.Pix[m.Offset(target.Min.X, target.Min.Y+y):][:width], source.Pix[source.Offset(r.Min.X, r.Min.Y+y):][:width])
	}
}

type Cursor struct {
	Width    int
	Height   int
	HotspotX int
	HotspotY int
	Pixels   []byte
	Invert   []byte
}
