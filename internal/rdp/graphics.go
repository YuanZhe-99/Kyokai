// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/graphics.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package rdp

import (
	"encoding/binary"
	"image"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/YuanZhe-99/Kyokai/internal/bgra"
	"github.com/YuanZhe-99/Kyokai/internal/rdp/codec/bulk"
	"github.com/YuanZhe-99/Kyokai/internal/rdp/codec/interleaved"
	"github.com/YuanZhe-99/Kyokai/internal/rdp/codec/nsc"
	"github.com/YuanZhe-99/Kyokai/internal/rdp/codec/planar"
	"github.com/YuanZhe-99/Kyokai/internal/rdp/codec/rfx"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const (
	maxGraphicsDimension = 8192
	maxGraphicsPixels    = 32 << 20
	maxFastPathUpdate    = 16 << 20
	bitmapAlignment      = 64
	pointerCacheSize     = 96
	paletteSize          = 256
	paletteEntrySize     = 3
	opaqueAlpha          = 0xFF000000
)

const (
	fastPathUpdateOrders          = 0x0
	fastPathUpdateBitmap          = 0x1
	fastPathUpdatePalette         = 0x2
	fastPathUpdateSynchronize     = 0x3
	fastPathUpdateSurfaceCommands = 0x4
	fastPathUpdatePointerHidden   = 0x5
	fastPathUpdatePointerDefault  = 0x6
	fastPathUpdatePointerPosition = 0x8
	fastPathUpdateColorPointer    = 0x9
	fastPathUpdateCachedPointer   = 0xA
	fastPathUpdatePointer         = 0xB
	fastPathUpdateLargePointer    = 0xC

	fastPathUpdateCodeMask   = 0x0F
	fastPathFragmentShift    = 4
	fastPathFragmentMask     = 0x03
	fastPathCompressionShift = 6
	fastPathCompressionUsed  = 0x2

	fastPathFragmentSingle = 0x0
	fastPathFragmentLast   = 0x1
	fastPathFragmentFirst  = 0x2
	fastPathFragmentNext   = 0x3
)

const (
	updateTypeOrders      = 0x0
	updateTypeBitmap      = 0x1
	updateTypePalette     = 0x2
	updateTypeSynchronize = 0x3

	bitmapDataSize               = 18
	bitmapCompression            = 0x0001
	noBitmapCompressionHeader    = 0x0400
	compressedDataHeaderSize     = 8
	compressedDataMainBodyOffset = 2

	surfaceCommandSetSurfaceBits    = 0x0001
	surfaceCommandFrameMarker       = 0x0004
	surfaceCommandStreamSurfaceBits = 0x0006
	surfaceFrameBegin               = 0x0
	surfaceFrameEnd                 = 0x1
	bitmapDataExCompressedHeader    = 0x01
	compressedBitmapHeaderSize      = 24

	codecIDNone     = 0
	codecIDNSCodec  = 1
	codecIDRemoteFX = 3
)

type encoding uint32

const (
	encodingNone encoding = iota
	encodingBitmap
	encodingInterleaved
	encodingPlanar
	encodingNSCodec
	encodingRemoteFX
	encodingUncompressed
	encodingClearCodec
	encodingProgressive
	encodingAVC420
	encodingAVC444
	encodingAVC444v2
	encodingAlpha
)

var encodingNames = [...]string{
	encodingNone:         "",
	encodingBitmap:       "Bitmap",
	encodingInterleaved:  "Interleaved RLE",
	encodingPlanar:       "Planar",
	encodingNSCodec:      "NSCodec",
	encodingRemoteFX:     "RemoteFX",
	encodingUncompressed: "RDPGFX raw",
	encodingClearCodec:   "ClearCodec",
	encodingProgressive:  "RemoteFX Progressive",
	encodingAVC420:       "AVC420",
	encodingAVC444:       "AVC444",
	encodingAVC444v2:     "AVC444v2",
	encodingAlpha:        "Alpha",
}

type graphics struct {
	events   Events
	logger   *slog.Logger
	width    atomic.Int32
	height   atomic.Int32
	frames   atomic.Uint64
	encoding atomic.Uint32

	framebufferAccess sync.RWMutex
	framebuffer       *bgra.Image

	bulk         *bulk.Decompressor
	client       *Client
	pointerCache [pointerCacheSize]*bgra.Cursor
	palette      [paletteSize]uint32
	hasPalette   bool
	interleaved  interleaved.Decoder
	planar       planar.Decoder
	nsc          nsc.Decoder
	rfx          *rfx.Decoder
	fragment     []byte
	fragmentCode byte
	fragmenting  bool
	inFrame      bool
	dirty        image.Rectangle
	received     time.Time
	frameStart   time.Time
}

func newGraphics(events Events, logger *slog.Logger, width, height int, client *Client) *graphics {
	g := &graphics{events: events, logger: logger, bulk: new(bulk.Decompressor), client: client, rfx: rfx.NewDecoder()}
	g.width.Store(int32(width))
	g.height.Store(int32(height))
	return g
}

func validDesktopSize(width, height int) bool {
	return width > 0 && height > 0 && width <= maxGraphicsDimension && height <= maxGraphicsDimension && width*height <= maxGraphicsPixels
}

func validFramebuffer(framebuffer *bgra.Image, width, height int) bool {
	return framebuffer != nil && framebuffer.Width == width && framebuffer.Height == height && framebuffer.Validate() == nil
}

func setOpaque(img *bgra.Image) {
	for y := range img.Height {
		row := img.Pix[y*img.Stride:][:img.Width*4]
		for x := 3; x < len(row); x += 4 {
			row[x] = 0xFF
		}
	}
}

func (g *graphics) resize(width, height int) error {
	if !validDesktopSize(width, height) {
		return E.New("rdp: invalid desktop size ", width, "x", height)
	}
	g.framebufferAccess.RLock()
	unchanged := validFramebuffer(g.framebuffer, width, height)
	g.framebufferAccess.RUnlock()
	if unchanged {
		return nil
	}
	g.framebufferAccess.Lock()
	g.framebuffer = nil
	g.framebufferAccess.Unlock()
	g.width.Store(int32(width))
	g.height.Store(int32(height))
	framebuffer, err := g.events.ResizeFramebuffer(width, height)
	if err != nil {
		return err
	}
	g.client.refreshInfo()
	if !validFramebuffer(framebuffer, width, height) {
		return E.New("rdp: the application supplied an invalid framebuffer for the ", width, "x", height, " desktop")
	}
	g.framebufferAccess.Lock()
	g.framebuffer = framebuffer
	g.framebufferAccess.Unlock()
	g.dirty = image.Rectangle{}
	g.inFrame = false
	return nil
}

func (g *graphics) attach(framebuffer *bgra.Image) error {
	if !validFramebuffer(framebuffer, int(g.width.Load()), int(g.height.Load())) {
		return E.New("rdp: framebuffer does not match the ", g.width.Load(), "x", g.height.Load(), " desktop")
	}
	g.framebufferAccess.Lock()
	g.framebuffer = framebuffer
	g.framebufferAccess.Unlock()
	return nil
}

func (g *graphics) snapshot() *bgra.Image {
	g.framebufferAccess.Lock()
	defer g.framebufferAccess.Unlock()
	if g.framebuffer == nil {
		return nil
	}
	frame := bgra.New(g.framebuffer.Width, g.framebuffer.Height)
	frame.Copy(image.Point{}, g.framebuffer, g.framebuffer.Bounds())
	return frame
}

func (g *graphics) close() {
	g.framebufferAccess.Lock()
	g.framebuffer = nil
	g.framebufferAccess.Unlock()
	g.fragment = nil
	g.pointerCache = [pointerCacheSize]*bgra.Cursor{}
	g.rfx = nil
}

func (g *graphics) dropUpdate(err error) {
	g.logger.Debug("drop graphics update", "error", err)
	g.client.scheduleRefresh()
}

func (g *graphics) beginFrame() {
	g.endFrame()
	g.inFrame = true
	g.frameStart = g.received
}

func (g *graphics) endFrame() {
	dirty, start := g.dirty, g.received
	if g.inFrame {
		start = g.frameStart
	}
	g.dirty, g.inFrame = image.Rectangle{}, false
	if !dirty.Empty() {
		g.frames.Add(1)
		g.events.FramebufferUpdated(Update{Rectangle: dirty, Hold: time.Since(start)})
	}
}

func (g *graphics) updated(rect image.Rectangle) {
	g.dirty = g.dirty.Union(rect)
	if !g.inFrame {
		g.endFrame()
	}
}

func (g *graphics) drawBitmap(codec encoding, rect image.Rectangle, width, height int, opaque bool, decode func(dst *bgra.Image) error) error {
	maxWidth := (int(g.width.Load()) + bitmapAlignment - 1) &^ (bitmapAlignment - 1)
	maxHeight := (int(g.height.Load()) + bitmapAlignment - 1) &^ (bitmapAlignment - 1)
	if rect.Empty() || rect.Dx() > width || rect.Dy() > height || width > maxWidth || height > maxHeight {
		return E.New("rdp: invalid ", width, "x", height, " bitmap for destination ", rect)
	}
	g.framebufferAccess.RLock()
	if g.framebuffer == nil {
		g.framebufferAccess.RUnlock()
		return nil
	}
	dst := g.framebuffer.SubImage(rect)
	err := decode(dst)
	if err == nil && opaque {
		setOpaque(dst)
	}
	g.framebufferAccess.RUnlock()
	if err != nil {
		g.dropUpdate(E.Cause(err, "decode ", encodingNames[codec]))
		return nil
	}
	g.encoding.Store(uint32(codec))
	if dst.Width > 0 {
		g.updated(dst.Bounds().Add(rect.Min))
	}
	return nil
}

func (g *graphics) palettePointer() *[paletteSize]uint32 {
	if !g.hasPalette {
		return nil
	}
	return &g.palette
}

func (g *graphics) processFastPathUpdates(data []byte) error {
	g.received = time.Now()
	r := reader{data: data}
	for r.remaining() != 0 {
		header := r.u8()
		code := header & fastPathUpdateCodeMask
		fragment := header >> fastPathFragmentShift & fastPathFragmentMask
		var flags byte
		switch compression := header >> fastPathCompressionShift; compression {
		case 0:
		case fastPathCompressionUsed:
			flags = r.u8()
		default:
			return E.New("rdp: invalid fast-path compression indicator ", compression)
		}
		payload := r.take(int(r.u16()))
		if r.err != nil {
			return r.err
		}
		payload, err := g.bulk.Decompress(payload, flags)
		if err != nil {
			return E.Cause(err, "rdp: decompress fast-path update")
		}
		switch fragment {
		case fastPathFragmentSingle:
			if g.fragmenting {
				return E.New("rdp: unfragmented fast-path update ", code, " inside fragmented update ", g.fragmentCode)
			}
			err = g.processFastPathUpdate(code, payload)
			if err != nil {
				return err
			}
		case fastPathFragmentFirst:
			if g.fragmenting {
				return E.New("rdp: fast-path update ", code, " starts inside fragmented update ", g.fragmentCode)
			}
			if len(payload) > maxFastPathUpdate {
				return E.New("rdp: fast-path update ", code, " of ", len(payload), " bytes exceeds ", maxFastPathUpdate, " bytes")
			}
			g.fragmenting, g.fragmentCode = true, code
			g.fragment = append(g.fragment[:0], payload...)
		case fastPathFragmentLast, fastPathFragmentNext:
			if !g.fragmenting || g.fragmentCode != code {
				return E.New("rdp: unexpected continuation of fast-path update ", code)
			}
			if len(payload) > maxFastPathUpdate-len(g.fragment) {
				return E.New("rdp: fast-path update ", code, " of ", len(g.fragment)+len(payload), " bytes exceeds ", maxFastPathUpdate, " bytes")
			}
			g.fragment = append(g.fragment, payload...)
			if fragment == fastPathFragmentLast {
				g.fragmenting = false
				err = g.processFastPathUpdate(code, g.fragment)
				if err != nil {
					return err
				}
				g.fragment = g.fragment[:0]
			}
		}
	}
	return nil
}

func (g *graphics) processFastPathUpdate(code byte, data []byte) error {
	switch code {
	case fastPathUpdateOrders:
		r := reader{data: data}
		count := r.u16()
		if r.err != nil {
			return r.err
		}
		if count != 0 {
			return E.New("rdp: server sent ", count, " unnegotiated drawing orders")
		}
		return nil
	case fastPathUpdateBitmap, fastPathUpdatePalette:
		return g.processSlowPathUpdate(data)
	case fastPathUpdateSynchronize:
		return nil
	case fastPathUpdateSurfaceCommands:
		return g.processSurfaceCommands(data)
	case fastPathUpdatePointerHidden, fastPathUpdatePointerDefault, fastPathUpdatePointerPosition,
		fastPathUpdateColorPointer, fastPathUpdateCachedPointer, fastPathUpdatePointer, fastPathUpdateLargePointer:
		g.updatePointer(code, data)
		return nil
	default:
		return E.New("rdp: unknown fast-path update type ", code)
	}
}

func (g *graphics) processSlowPathUpdate(data []byte) error {
	g.received = time.Now()
	r := reader{data: data}
	updateType := r.u16()
	if r.err != nil {
		return r.err
	}
	switch updateType {
	case updateTypeOrders:
		r.take(2)
		count := r.u16()
		r.take(2)
		if r.err != nil {
			return r.err
		}
		if count != 0 {
			return E.New("rdp: server sent ", count, " unnegotiated drawing orders")
		}
		return nil
	case updateTypeBitmap:
		return g.processBitmapUpdate(&r)
	case updateTypePalette:
		return g.processPaletteUpdate(&r)
	case updateTypeSynchronize:
		return nil
	default:
		return E.New("rdp: unknown slow-path update type ", updateType)
	}
}

func (g *graphics) processPaletteUpdate(r *reader) error {
	r.take(2)
	count := int(r.u32())
	if count > len(g.palette) {
		return E.New("rdp: invalid palette entry count ", count)
	}
	entries := r.take(count * paletteEntrySize)
	if r.err != nil {
		return r.err
	}
	for i := range count {
		red, green, blue := entries[i*paletteEntrySize], entries[i*paletteEntrySize+1], entries[i*paletteEntrySize+2]
		g.palette[i] = uint32(blue) | uint32(green)<<8 | uint32(red)<<16 | opaqueAlpha
	}
	g.hasPalette = true
	return nil
}

func (g *graphics) processBitmapUpdate(r *reader) error {
	count := int(r.u16())
	if r.err != nil {
		return r.err
	}
	if count > r.remaining()/bitmapDataSize {
		return E.New("rdp: bitmap update lists ", count, " rectangles in ", r.remaining(), " bytes")
	}
	for range count {
		left, top := int(r.u16()), int(r.u16())
		right, bottom := int(r.u16())+1, int(r.u16())+1
		width, height := int(r.u16()), int(r.u16())
		bpp, flags, length := int(r.u16()), r.u16(), int(r.u16())
		if flags&bitmapCompression != 0 && flags&noBitmapCompressionHeader == 0 {
			r.take(compressedDataMainBodyOffset)
			length = int(r.u16())
			r.take(compressedDataHeaderSize - compressedDataMainBodyOffset - 2)
		}
		payload := r.take(length)
		if r.err != nil {
			return r.err
		}
		rect := image.Rect(left, top, right, bottom)
		palette := g.palettePointer()
		var err error
		switch {
		case flags&bitmapCompression == 0:
			err = g.drawBitmap(encodingBitmap, rect, width, height, false, func(dst *bgra.Image) error {
				return interleaved.DecodeUncompressed(payload, width, height, bpp, palette, dst)
			})
		case bpp == 32:
			err = g.drawBitmap(encodingPlanar, rect, width, height, true, func(dst *bgra.Image) error {
				return g.planar.Decode(payload, width, height, true, dst)
			})
		default:
			err = g.drawBitmap(encodingInterleaved, rect, width, height, false, func(dst *bgra.Image) error {
				return g.interleaved.Decode(payload, width, height, bpp, palette, dst)
			})
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (g *graphics) processSurfaceCommands(data []byte) error {
	r := reader{data: data}
	for r.remaining() != 0 {
		command := r.u16()
		if r.err != nil {
			return r.err
		}
		switch command {
		case surfaceCommandSetSurfaceBits, surfaceCommandStreamSurfaceBits:
			err := g.processSurfaceBits(&r)
			if err != nil {
				return err
			}
		case surfaceCommandFrameMarker:
			action, frameID := r.u16(), r.u32()
			if r.err != nil {
				return r.err
			}
			switch action {
			case surfaceFrameBegin:
				g.beginFrame()
			case surfaceFrameEnd:
				g.endFrame()
				err := g.client.session.sendData(pduFrameAcknowledge, binary.LittleEndian.AppendUint32(nil, frameID))
				if err != nil {
					return err
				}
			default:
				return E.New("rdp: unknown surface frame action ", action)
			}
		default:
			return E.New("rdp: unknown surface command ", command)
		}
	}
	return nil
}

func (g *graphics) processSurfaceBits(r *reader) error {
	left, top, right, bottom := int(r.u16()), int(r.u16()), int(r.u16()), int(r.u16())
	bpp, flags := int(r.u8()), r.u8()
	r.take(1)
	codecID := r.u8()
	width, height := int(r.u16()), int(r.u16())
	length := int(r.u32())
	if flags&bitmapDataExCompressedHeader != 0 {
		r.take(compressedBitmapHeaderSize)
	}
	payload := r.take(length)
	if r.err != nil {
		return r.err
	}
	if left >= right || top >= bottom {
		return E.New("rdp: invalid surface bits destination (", left, ",", top, ")-(", right, ",", bottom, ")")
	}
	rect := image.Rect(left, top, right, bottom)
	switch codecID {
	case codecIDNone:
		palette := g.palettePointer()
		return g.drawBitmap(encodingBitmap, rect, width, height, false, func(dst *bgra.Image) error {
			return interleaved.DecodeUncompressed(payload, width, height, bpp, palette, dst)
		})
	case codecIDNSCodec:
		return g.drawBitmap(encodingNSCodec, rect, width, height, true, func(dst *bgra.Image) error {
			return g.nsc.Decode(payload, width, height, true, dst)
		})
	case codecIDRemoteFX:
		g.framebufferAccess.RLock()
		if g.framebuffer == nil {
			g.framebufferAccess.RUnlock()
			return nil
		}
		updates, err := g.rfx.Decode(payload, g.framebuffer.SubImage(image.Rectangle{Min: rect.Min, Max: g.framebuffer.Bounds().Max}))
		g.framebufferAccess.RUnlock()
		if err != nil {
			g.dropUpdate(E.Cause(err, "decode ", encodingNames[encodingRemoteFX]))
			return nil
		}
		g.encoding.Store(uint32(encodingRemoteFX))
		for _, update := range updates {
			g.updated(update.Add(rect.Min))
		}
		return nil
	default:
		return E.New("rdp: unknown surface codec ", codecID)
	}
}
