// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/gfx.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: the on-disk graphics cache removed, import paths.

package rdp

import (
	"encoding/binary"
	"image"
	"slices"
	"strconv"
	"time"

	"github.com/YuanZhe-99/Kyokai/internal/bgra"
	"github.com/YuanZhe-99/Kyokai/internal/rdp/codec/clear"
	"github.com/YuanZhe-99/Kyokai/internal/rdp/codec/h264"
	"github.com/YuanZhe-99/Kyokai/internal/rdp/codec/rfx"
	"github.com/YuanZhe-99/Kyokai/internal/rdp/codec/zgfx"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const (
	gfxWireToSurface1        = 0x0001
	gfxWireToSurface2        = 0x0002
	gfxDeleteEncodingContext = 0x0003
	gfxSolidFill             = 0x0004
	gfxSurfaceToSurface      = 0x0005
	gfxSurfaceToCache        = 0x0006
	gfxCacheToSurface        = 0x0007
	gfxEvictCacheEntry       = 0x0008
	gfxCreateSurface         = 0x0009
	gfxDeleteSurface         = 0x000a
	gfxStartFrame            = 0x000b
	gfxEndFrame              = 0x000c
	gfxFrameAcknowledge      = 0x000d
	gfxResetGraphics         = 0x000e
	gfxMapSurfaceToOutput    = 0x000f
	gfxCapsAdvertise         = 0x0012
	gfxCapsConfirm           = 0x0013

	gfxCapsVersion8   = 0x00080004
	gfxCapsVersion81  = 0x00080105
	gfxCapsVersion10  = 0x000a0002
	gfxCapsVersion102 = 0x000a0200
	gfxCapsVersion104 = 0x000a0400
	gfxCapsVersion107 = 0x000a0701

	gfxCapsSmallCache       = 0x02
	gfxCapsAVC420Enabled    = 0x10
	gfxCapsAVCDisabled      = 0x20
	gfxCapsScaledMapDisable = 0x80
	gfxCapsDataSize         = 4

	gfxHeaderSize            = 8
	gfxRectangleSize         = 8
	gfxPointSize             = 4
	gfxMaxMonitors           = 16
	gfxMonitorSize           = 20
	gfxQueueDepthUnavailable = 0
	gfxResetFill             = 0xFFFFFFFF

	gfxXRGB            = 0x20
	gfxARGB            = 0x21
	gfxMaxSurfaces     = 64
	gfxMaxCacheBytes   = 100 << 20
	gfxMaxCacheEntries = 25600
)

const (
	gfxBytesPerPixel            = 4
	gfxMemoryPerDesktopPixel    = 16
	gfxMemoryOverhead           = 64 << 20
	gfxProgressiveBytesPerPixel = 10
	gfxH264BytesPerPixel        = 6
	gfxH264MinBytes             = 16 << 20
	gfxClearCodecBytes          = 24 << 20
	gfxRemoteFXBytes            = 4 << 20
)

var gfxCapsVersions = [...]uint32{gfxCapsVersion8, gfxCapsVersion81, gfxCapsVersion10, gfxCapsVersion102, gfxCapsVersion104, gfxCapsVersion107}

type gfxBudget struct {
	used  int
	limit int
}

func (b *gfxBudget) reserve(bytes int) error {
	if bytes > b.limit-b.used {
		return E.New(bytes, " bytes exceed the memory limit (", b.used, " of ", b.limit, " in use)")
	}
	b.used += bytes
	return nil
}

func (b *gfxBudget) release(bytes int) {
	b.used -= bytes
}

type rdpgfx struct {
	g             *graphics
	send          func([]byte) error
	zgfx          *zgfx.Decompressor
	surfaces      map[uint16]*gfxSurface
	cache         map[uint16]*gfxCacheEntry
	memory        gfxBudget
	cacheMemory   gfxBudget
	clear         *clear.Decoder
	rfx           *rfx.Decoder
	frameID       uint32
	frame         uint64
	framesDecoded uint32
	inFrame       bool
}

func (p *rdpgfx) OnOpen(send func([]byte) error) error {
	p.send = send
	p.zgfx = zgfx.NewDecompressor()
	p.surfaces = make(map[uint16]*gfxSurface)
	p.cache = make(map[uint16]*gfxCacheEntry)
	p.memory = gfxBudget{limit: gfxMemoryLimit(int(p.g.width.Load()), int(p.g.height.Load()))}
	p.cacheMemory = gfxBudget{limit: gfxMaxCacheBytes}
	return p.send(gfxPacket(gfxCapsAdvertise, gfxCapabilities()))
}

func gfxMemoryLimit(width, height int) int {
	return width*height*gfxMemoryPerDesktopPixel + gfxMemoryOverhead
}

func gfxCapabilities() []byte {
	avc := h264.Available()
	b := binary.LittleEndian.AppendUint16(nil, uint16(len(gfxCapsVersions)))
	for _, version := range gfxCapsVersions {
		var flags uint32
		if version == gfxCapsVersion81 && avc {
			// rdpbase.dll of Windows 11 26200 treats a version 8.1 set that enables AVC420 without
			// SMALL_CACHE or THINCLIENT as invalid, and the server skips it.
			flags |= gfxCapsSmallCache | gfxCapsAVC420Enabled
		}
		if version >= gfxCapsVersion10 && !avc {
			flags |= gfxCapsAVCDisabled
		}
		if version == gfxCapsVersion107 {
			flags |= gfxCapsScaledMapDisable
		}
		b = binary.LittleEndian.AppendUint32(b, version)
		b = binary.LittleEndian.AppendUint32(b, gfxCapsDataSize)
		b = binary.LittleEndian.AppendUint32(b, flags)
	}
	return b
}

func (p *rdpgfx) OnClose() {
	for _, s := range p.surfaces {
		s.close()
	}
	if p.inFrame {
		p.g.endFrame()
	}
	*p = rdpgfx{g: p.g}
}

func gfxPacket(command uint16, body []byte) []byte {
	b := binary.LittleEndian.AppendUint16(nil, command)
	b = binary.LittleEndian.AppendUint16(b, 0)
	b = binary.LittleEndian.AppendUint32(b, uint32(gfxHeaderSize+len(body)))
	return append(b, body...)
}

func (p *rdpgfx) OnData(data []byte) error {
	p.g.received = time.Now()
	data, err := p.zgfx.Decompress(data)
	if err != nil {
		return E.Cause(err, "rdpgfx: decompress channel data")
	}
	r := &reader{data: data}
	for r.remaining() > 0 {
		command := r.u16()
		r.take(2)
		length := r.u32()
		if r.err != nil {
			return r.err
		}
		if length < gfxHeaderSize || uint64(length-gfxHeaderSize) > uint64(r.remaining()) {
			return E.New("rdpgfx: invalid PDU length ", length, " with ", r.remaining(), " bytes left")
		}
		body := r.sub(int(length) - gfxHeaderSize)
		err = p.process(command, body)
		if err != nil {
			return E.Cause(err, "rdpgfx: process command 0x", strconv.FormatUint(uint64(command), 16))
		}
		if body.err != nil {
			return body.err
		}
		if body.remaining() != 0 {
			return E.New("rdpgfx: ", body.remaining(), " trailing bytes after command 0x", strconv.FormatUint(uint64(command), 16))
		}
	}
	return r.err
}

func (p *rdpgfx) process(command uint16, r *reader) error {
	switch command {
	case gfxCapsConfirm:
		version, length := r.u32(), r.u32()
		body := r.take(int(length))
		if r.err != nil {
			return r.err
		}
		if !slices.Contains(gfxCapsVersions[:], version) {
			return E.New("unsupported graphics version 0x", strconv.FormatUint(uint64(version), 16))
		}
		if len(body) != gfxCapsDataSize {
			return E.New("invalid capability length ", length)
		}
		return nil
	case gfxResetGraphics:
		width, height, count := int(r.u32()), int(r.u32()), int(r.u32())
		if r.err != nil {
			return r.err
		}
		if !validDesktopSize(width, height) || count < 1 || count > gfxMaxMonitors {
			return E.New("invalid reset to ", width, "x", height, " with ", count, " monitors")
		}
		for range count {
			left, top, right, bottom := int32(r.u32()), int32(r.u32()), int32(r.u32()), int32(r.u32())
			r.u32()
			if r.err == nil && (left > right || top > bottom) {
				return E.New("invalid monitor rectangle (", left, ",", top, ")-(", right, ",", bottom, ")")
			}
		}
		r.take((gfxMaxMonitors - count) * gfxMonitorSize)
		if r.err != nil {
			return r.err
		}
		err := p.g.resize(width, height)
		if err != nil {
			return err
		}
		p.memory.limit = gfxMemoryLimit(width, height)
		// FreeRDP fills every surface with 0xFF bytes when the graphics are reset.
		for _, surface := range p.surfaces {
			surface.image.Fill(surface.image.Bounds(), gfxResetFill)
		}
	case gfxCreateSurface:
		id, width, height, format := r.u16(), int(r.u16()), int(r.u16()), r.u8()
		if r.err != nil {
			return r.err
		}
		if !validDesktopSize(width, height) || !gfxValidFormat(format) {
			return E.New("invalid ", width, "x", height, " surface in pixel format 0x", strconv.FormatUint(uint64(format), 16))
		}
		if p.surfaces[id] != nil {
			return E.New("duplicate surface ", id)
		}
		if len(p.surfaces) >= gfxMaxSurfaces {
			return E.New("surface ", id, " exceeds the limit of ", gfxMaxSurfaces, " surfaces")
		}
		surface := &gfxSurface{format: format}
		err := p.reserve(surface, width*height*gfxBytesPerPixel)
		if err != nil {
			return err
		}
		surface.image = bgra.New(width, height)
		if format == gfxXRGB {
			surface.image.Fill(surface.image.Bounds(), opaqueAlpha)
		}
		p.surfaces[id] = surface
	case gfxDeleteSurface:
		id := r.u16()
		if r.err != nil {
			return r.err
		}
		if s := p.surfaces[id]; s != nil {
			s.close()
			p.memory.release(s.reserved)
			delete(p.surfaces, id)
		}
	case gfxMapSurfaceToOutput:
		id := r.u16()
		r.take(2)
		x, y := int(r.u32()), int(r.u32())
		if r.err != nil {
			return r.err
		}
		s, err := p.surface(id)
		if err != nil {
			return err
		}
		if x > maxGraphicsDimension || y > maxGraphicsDimension {
			return E.New("invalid output origin ", x, ",", y, " for surface ", id)
		}
		s.origin, s.mapped = image.Pt(x, y), true
		p.updated(s, s.image.Bounds(), false)
	case gfxWireToSurface1, gfxWireToSurface2:
		return p.wireToSurface(command, r)
	case gfxDeleteEncodingContext:
		id := r.u16()
		r.take(4)
		if r.err != nil {
			return r.err
		}
		_, err := p.surface(id)
		return err
	case gfxSolidFill:
		return p.solidFill(r)
	case gfxSurfaceToSurface:
		return p.surfaceToSurface(r)
	case gfxSurfaceToCache:
		return p.surfaceToCache(r)
	case gfxCacheToSurface:
		return p.cacheToSurface(r)
	case gfxEvictCacheEntry:
		slot := r.u16()
		if r.err != nil {
			return r.err
		}
		p.evictCache(slot)
	case gfxStartFrame:
		r.take(4)
		frameID := r.u32()
		if r.err != nil {
			return r.err
		}
		if p.inFrame {
			return E.New("frame ", frameID, " starts inside frame ", p.frameID)
		}
		p.frameID, p.inFrame = frameID, true
		p.frame++
		p.g.beginFrame()
	case gfxEndFrame:
		frameID := r.u32()
		if r.err != nil {
			return r.err
		}
		if !p.inFrame || p.frameID != frameID {
			return E.New("unmatched end frame ", frameID)
		}
		p.inFrame = false
		p.g.endFrame()
		p.framesDecoded++
		ack := binary.LittleEndian.AppendUint32(nil, gfxQueueDepthUnavailable)
		ack = binary.LittleEndian.AppendUint32(ack, frameID)
		ack = binary.LittleEndian.AppendUint32(ack, p.framesDecoded)
		return p.send(gfxPacket(gfxFrameAcknowledge, ack))
	default:
		return E.New("unsupported command")
	}
	return r.err
}

func gfxValidFormat(format byte) bool { return format == gfxXRGB || format == gfxARGB }

func gfxReadRect(r *reader) image.Rectangle {
	left, top, right, bottom := int(r.u16()), int(r.u16()), int(r.u16()), int(r.u16())
	rect := image.Rectangle{Min: image.Pt(left, top), Max: image.Pt(right, bottom)}
	if r.err == nil && (left > right || top > bottom) {
		r.err = E.New("invalid rectangle ", rect)
	}
	return rect
}

func (p *rdpgfx) surface(id uint16) (*gfxSurface, error) {
	s := p.surfaces[id]
	if s == nil {
		return nil, E.New("unknown surface ", id)
	}
	return s, nil
}

func (p *rdpgfx) reserve(s *gfxSurface, bytes int) error {
	err := p.memory.reserve(bytes)
	if err != nil {
		return err
	}
	s.reserved += bytes
	return nil
}

func (p *rdpgfx) updated(s *gfxSurface, rect image.Rectangle, opaque bool) {
	rect = rect.Intersect(s.image.Bounds())
	if rect.Empty() {
		return
	}
	if opaque {
		setOpaque(s.image.SubImage(rect))
	}
	if !s.mapped {
		return
	}
	g := p.g
	g.framebufferAccess.RLock()
	if g.framebuffer == nil {
		g.framebufferAccess.RUnlock()
		return
	}
	target := rect.Add(s.origin).Intersect(g.framebuffer.Bounds())
	g.framebuffer.Copy(target.Min, s.image, target.Sub(s.origin))
	g.framebufferAccess.RUnlock()
	if !target.Empty() {
		g.updated(target)
	}
}

func (p *rdpgfx) blit(dst *gfxSurface, at image.Point, src *bgra.Image, format byte, rect image.Rectangle) {
	dst.image.Copy(at, src, rect)
	p.updated(dst, rect.Sub(rect.Min).Add(at), format == gfxARGB && dst.format == gfxXRGB)
}

func (p *rdpgfx) solidFill(r *reader) error {
	id := r.u16()
	blue, green, red := r.u8(), r.u8(), r.u8()
	r.take(1)
	count := int(r.u16())
	if r.err != nil {
		return r.err
	}
	if count > r.remaining()/gfxRectangleSize {
		return E.New("SolidFill lists ", count, " rectangles in ", r.remaining(), " bytes")
	}
	s, err := p.surface(id)
	if err != nil {
		return err
	}
	// FreeRDP and IronRDP fill with an opaque color and ignore the XA byte.
	color := uint32(blue) | uint32(green)<<8 | uint32(red)<<16 | opaqueAlpha
	for range count {
		rect := gfxReadRect(r)
		if r.err != nil {
			return r.err
		}
		s.image.Fill(rect, color)
		p.updated(s, rect, false)
	}
	return nil
}

func (p *rdpgfx) surfaceToSurface(r *reader) error {
	sourceID, destID := r.u16(), r.u16()
	rect := gfxReadRect(r)
	count := int(r.u16())
	if r.err != nil {
		return r.err
	}
	if count > r.remaining()/gfxPointSize {
		return E.New("SurfaceToSurface lists ", count, " destination points in ", r.remaining(), " bytes")
	}
	src, err := p.surface(sourceID)
	if err != nil {
		return err
	}
	dst, err := p.surface(destID)
	if err != nil {
		return err
	}
	if !rect.In(src.image.Bounds()) {
		return E.New("copy rectangle ", rect, " outside source surface ", src.image.Bounds())
	}
	for range count {
		p.blit(dst, image.Pt(int(r.u16()), int(r.u16())), src.image, src.format, rect)
	}
	return r.err
}

func (p *rdpgfx) surfaceToCache(r *reader) error {
	id := r.u16()
	key := r.u64()
	slot := r.u16()
	rect := gfxReadRect(r)
	if r.err != nil {
		return r.err
	}
	s, err := p.surface(id)
	if err != nil {
		return err
	}
	if rect.Empty() || !rect.In(s.image.Bounds()) || slot == 0 || slot > gfxMaxCacheEntries {
		return E.New("invalid cache source rectangle ", rect, " or slot ", slot)
	}
	cached := bgra.New(rect.Dx(), rect.Dy())
	cached.Copy(image.Point{}, s.image, rect)
	return p.storeCache(slot, &gfxCacheEntry{image: cached, format: s.format, key: key, used: p.frame})
}

func (p *rdpgfx) cacheToSurface(r *reader) error {
	slot, id, count := r.u16(), r.u16(), int(r.u16())
	if r.err != nil {
		return r.err
	}
	if count > r.remaining()/gfxPointSize {
		return E.New("CacheToSurface lists ", count, " destination points in ", r.remaining(), " bytes")
	}
	cached, found := p.cache[slot]
	if !found {
		return E.New("unknown cache slot ", slot)
	}
	s, err := p.surface(id)
	if err != nil {
		return err
	}
	cached.used = p.frame
	for range count {
		p.blit(s, image.Pt(int(r.u16()), int(r.u16())), cached.image, cached.format, cached.image.Bounds())
	}
	return r.err
}
