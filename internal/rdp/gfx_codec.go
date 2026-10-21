// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/gfx_codec.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package rdp

import (
	"image"
	"strconv"

	"github.com/YuanZhe-99/Kyokai/internal/bgra"
	"github.com/YuanZhe-99/Kyokai/internal/rdp/codec/alpha"
	"github.com/YuanZhe-99/Kyokai/internal/rdp/codec/clear"
	"github.com/YuanZhe-99/Kyokai/internal/rdp/codec/h264"
	"github.com/YuanZhe-99/Kyokai/internal/rdp/codec/planar"
	"github.com/YuanZhe-99/Kyokai/internal/rdp/codec/rfx"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const (
	gfxCodecUncompressed = 0x0000
	gfxCodecRemoteFX     = 0x0003
	gfxCodecClearCodec   = 0x0008
	gfxCodecProgressive  = 0x0009
	gfxCodecPlanar       = 0x000a
	gfxCodecAVC420       = 0x000b
	gfxCodecAlpha        = 0x000c
	gfxCodecAVC444       = 0x000e
	gfxCodecAVC444v2     = 0x000f
)

type gfxSurface struct {
	image       *bgra.Image
	format      byte
	origin      image.Point
	mapped      bool
	reserved    int
	planar      planar.Decoder
	progressive *rfx.ProgressiveSurface
	h264        *h264.Decoder
}

func (s *gfxSurface) close() {
	if s.h264 != nil {
		s.h264.Close()
	}
}

func (p *rdpgfx) wireToSurface(command uint16, r *reader) error {
	id, codecID := r.u16(), r.u16()
	var rect image.Rectangle
	var format byte
	if command == gfxWireToSurface1 {
		format = r.u8()
		rect = gfxReadRect(r)
	} else {
		r.take(4)
		format = r.u8()
	}
	data := r.take(int(r.u32()))
	if r.err != nil {
		return r.err
	}
	if !gfxValidFormat(format) {
		return E.New("invalid bitmap pixel format 0x", strconv.FormatUint(uint64(format), 16))
	}
	s, err := p.surface(id)
	if err != nil {
		return err
	}
	if command == gfxWireToSurface2 {
		if codecID != gfxCodecProgressive {
			return E.New("WireToSurface2 with codec 0x", strconv.FormatUint(uint64(codecID), 16), " instead of progressive")
		}
		rect = s.image.Bounds()
	}
	if !rect.In(s.image.Bounds()) {
		return E.New("bitmap destination ", rect, " outside surface ", s.image.Bounds())
	}
	avc := codecID == gfxCodecAVC420 || codecID == gfxCodecAVC444 || codecID == gfxCodecAVC444v2
	if rect.Empty() && !avc {
		return E.New("empty bitmap destination ", rect, " for codec 0x", strconv.FormatUint(uint64(codecID), 16))
	}
	target := s.image.SubImage(rect)
	single := [1]image.Rectangle{rect}
	updates := single[:]
	var (
		codec  encoding
		origin image.Point
		opaque bool
	)
	opaqueSource := format == gfxXRGB || s.format == gfxXRGB
	switch codecID {
	case gfxCodecUncompressed:
		codec, opaque = encodingUncompressed, opaqueSource
		if len(data) != rect.Dx()*rect.Dy()*gfxBytesPerPixel {
			return E.New("uncompressed ", rect.Dx(), "x", rect.Dy(), " bitmap of ", len(data), " bytes")
		}
		target.Copy(image.Point{}, &bgra.Image{Pix: data, Stride: rect.Dx() * gfxBytesPerPixel, Width: rect.Dx(), Height: rect.Dy()}, target.Bounds())
	case gfxCodecRemoteFX:
		codec = encodingRemoteFX
		if p.rfx == nil {
			err = p.memory.reserve(gfxRemoteFXBytes)
			if err != nil {
				return err
			}
			p.rfx = rfx.NewDecoder()
		}
		origin = rect.Min
		updates, err = p.rfx.Decode(data, s.image.SubImage(image.Rectangle{Min: rect.Min, Max: s.image.Bounds().Max}))
	case gfxCodecClearCodec:
		codec, opaque = encodingClearCodec, opaqueSource
		if p.clear == nil {
			err = p.memory.reserve(gfxClearCodecBytes)
			if err != nil {
				return err
			}
			p.clear = clear.NewDecoder()
		}
		err = p.clear.Decode(data, rect.Dx(), rect.Dy(), target)
	case gfxCodecProgressive:
		codec = encodingProgressive
		if s.progressive == nil {
			err = p.reserve(s, s.image.Width*s.image.Height*gfxProgressiveBytesPerPixel)
			if err != nil {
				return err
			}
			s.progressive = rfx.NewProgressiveSurface(s.image.Width, s.image.Height)
		}
		if !p.inFrame {
			p.frame++
		}
		updates, err = s.progressive.Decode(data, p.frame, s.image)
	case gfxCodecPlanar:
		codec, opaque = encodingPlanar, opaqueSource
		err = s.planar.Decode(data, rect.Dx(), rect.Dy(), false, target)
	case gfxCodecAVC420, gfxCodecAVC444, gfxCodecAVC444v2:
		if !h264.Available() {
			return E.New("AVC codec was not negotiated")
		}
		if s.h264 == nil {
			err = p.reserve(s, max(s.image.Width*s.image.Height*gfxH264BytesPerPixel, gfxH264MinBytes))
			if err != nil {
				return err
			}
			s.h264 = new(h264.Decoder)
		}
		switch codecID {
		case gfxCodecAVC420:
			codec = encodingAVC420
			updates, err = s.h264.DecodeAVC420(data, s.image)
		case gfxCodecAVC444:
			codec = encodingAVC444
			updates, err = s.h264.DecodeAVC444(data, false, s.image)
		default:
			codec = encodingAVC444v2
			updates, err = s.h264.DecodeAVC444(data, true, s.image)
		}
	case gfxCodecAlpha:
		codec, opaque = encodingAlpha, s.format == gfxXRGB
		err = alpha.Decode(data, rect.Dx(), rect.Dy(), target)
	default:
		return E.New("unsupported codec 0x", strconv.FormatUint(uint64(codecID), 16))
	}
	if err != nil {
		p.g.dropUpdate(E.Cause(err, "decode ", encodingNames[codec]))
		return nil
	}
	p.g.encoding.Store(uint32(codec))
	for _, update := range updates {
		p.updated(s, update.Add(origin), opaque)
	}
	return nil
}
