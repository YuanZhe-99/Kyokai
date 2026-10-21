// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/codec/rfx/decoder.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package rfx

import (
	"encoding/binary"
	"image"
	"strconv"

	"github.com/YuanZhe-99/Kyokai/internal/bgra"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const (
	blockSync          = 0xCCC0
	blockCodecVersions = 0xCCC1
	blockChannels      = 0xCCC2
	blockContext       = 0xCCC3
	blockFrameBegin    = 0xCCC4
	blockFrameEnd      = 0xCCC5
	blockRegion        = 0xCCC6
	blockExtension     = 0xCCC7

	blockRegionType = 0xCAC1
	blockTileset    = 0xCAC2
	blockTile       = 0xCAC3

	codecID          = 0x01
	contextChannelID = 0xFF
	channelSize      = 5
	frameBeginSize   = 6
	regionHeaderSize = 3
	rectangleSize    = 8
	tilesetSize      = 14
	tileHeaderSize   = 13

	propertiesEntropyMask  = 0x1E00
	propertiesEntropyShift = 9
	entropyRLGR1           = 1
	entropyRLGR3           = 4
)

const (
	headerSync = 1 << iota
	headerContext
	headerVersions
	headerChannels
	headerAll = headerSync | headerContext | headerVersions | headerChannels
)

type Decoder struct {
	headers  uint8
	width    int
	height   int
	rlgr3    bool
	expected uint16
	rects    []image.Rectangle
	quants   []quantValues
	clips    []image.Rectangle
	parts    []image.Rectangle
	tiles    []linearTile
	grid     tileGrid
	pool     workerPool
}

type linearTile struct {
	data   [3][]byte
	quant  [3]uint8
	origin image.Point
	parts  [2]int
	err    error
}

func NewDecoder() *Decoder {
	return &Decoder{expected: blockFrameBegin}
}

func (d *Decoder) Decode(src []byte, dst *bgra.Image) (updated []image.Rectangle, err error) {
	for len(src) >= blockHeaderSize {
		var (
			blockType uint16
			body      []byte
		)
		blockType, body, src, err = nextBlock(src)
		if err != nil {
			return updated, d.fail(err)
		}
		if blockType > blockContext && d.headers != headerAll {
			return updated, d.fail(E.New("rfx: data block 0x", strconv.FormatUint(uint64(blockType), 16), " before the header blocks"))
		}
		if blockType >= blockContext && blockType <= blockExtension {
			if len(body) < 2 {
				return updated, d.fail(E.New("rfx: truncated channel of block 0x", strconv.FormatUint(uint64(blockType), 16), ": ", len(body), " of 2 bytes"))
			}
			if body[0] != codecID {
				return updated, d.fail(E.New("rfx: invalid codec id ", body[0]))
			}
			if channelID := body[1]; blockType == blockContext && channelID != contextChannelID || blockType != blockContext && channelID != 0 {
				return updated, d.fail(E.New("rfx: invalid channel id ", channelID, " for block 0x", strconv.FormatUint(uint64(blockType), 16)))
			}
			body = body[2:]
		}
		switch blockType {
		case blockSync:
			err = d.readSync(body)
		case blockCodecVersions:
			err = d.readCodecVersions(body)
		case blockChannels:
			err = d.readChannels(body)
		case blockContext:
			err = d.readContext(body)
		case blockFrameBegin:
			err = d.advance(blockFrameBegin, blockRegion)
			if err == nil && len(body) < frameBeginSize {
				err = E.New("rfx: truncated frame begin: ", len(body), " of ", frameBeginSize, " bytes")
			}
		case blockRegion:
			err = d.advance(blockRegion, blockExtension)
			if err == nil {
				err = d.readRegion(body)
			}
		case blockExtension:
			err = d.advance(blockExtension, blockFrameEnd)
			if err == nil {
				updated, err = d.decodeTileset(body, dst, updated)
			}
		case blockFrameEnd:
			err = d.advance(blockFrameEnd, blockFrameBegin)
		default:
			err = E.New("rfx: unexpected block 0x", strconv.FormatUint(uint64(blockType), 16))
		}
		if err != nil {
			return updated, d.fail(err)
		}
	}
	return updated, nil
}

func (d *Decoder) fail(err error) error {
	d.expected = blockFrameBegin
	return err
}

func (d *Decoder) advance(current, next uint16) error {
	if d.expected != current {
		return E.New("rfx: unexpected block 0x", strconv.FormatUint(uint64(current), 16), ", expected 0x", strconv.FormatUint(uint64(d.expected), 16))
	}
	d.expected = next
	return nil
}

func (d *Decoder) readSync(body []byte) error {
	d.headers &^= headerSync
	if len(body) < 6 {
		return E.New("rfx: truncated sync: ", len(body), " of 6 bytes")
	}
	magic := binary.LittleEndian.Uint32(body)
	version := binary.LittleEndian.Uint16(body[4:])
	if magic != syncMagic {
		return E.New("rfx: invalid sync magic 0x", strconv.FormatUint(uint64(magic), 16))
	}
	if version != syncVersion {
		return E.New("rfx: unsupported version 0x", strconv.FormatUint(uint64(version), 16))
	}
	d.headers |= headerSync
	return nil
}

func (d *Decoder) readCodecVersions(body []byte) error {
	d.headers &^= headerVersions
	if len(body) < 4 {
		return E.New("rfx: truncated codec versions: ", len(body), " of 4 bytes")
	}
	if body[0] != 1 {
		return E.New("rfx: invalid codec count ", body[0])
	}
	if body[1] != codecID {
		return E.New("rfx: invalid codec id ", body[1])
	}
	if version := binary.LittleEndian.Uint16(body[2:]); version != syncVersion {
		return E.New("rfx: unsupported codec version 0x", strconv.FormatUint(uint64(version), 16))
	}
	d.headers |= headerVersions
	return nil
}

func (d *Decoder) readChannels(body []byte) error {
	d.headers &^= headerChannels
	if len(body) < 1 {
		return E.New("rfx: empty channels block")
	}
	count := int(body[0])
	if count < 1 {
		return E.New("rfx: no channels")
	}
	if len(body) < 1+count*channelSize {
		return E.New("rfx: truncated channels: ", len(body), " of ", 1+count*channelSize, " bytes for ", count, " channels")
	}
	if body[1] != 0 {
		return E.New("rfx: invalid first channel id ", body[1])
	}
	width := int(binary.LittleEndian.Uint16(body[2:]))
	height := int(binary.LittleEndian.Uint16(body[4:]))
	if width == 0 || height == 0 {
		return E.New("rfx: invalid channel size ", width, "x", height)
	}
	d.width, d.height = width, height
	d.headers |= headerChannels
	return nil
}

func (d *Decoder) readContext(body []byte) error {
	d.headers &^= headerContext
	if len(body) < 5 {
		return E.New("rfx: truncated context: ", len(body), " of 5 bytes")
	}
	properties := binary.LittleEndian.Uint16(body[3:])
	switch properties & propertiesEntropyMask >> propertiesEntropyShift {
	case entropyRLGR1:
		d.rlgr3 = false
	case entropyRLGR3:
		d.rlgr3 = true
	default:
		return E.New("rfx: unsupported entropy algorithm in properties 0x", strconv.FormatUint(uint64(properties), 16))
	}
	d.headers |= headerContext
	return nil
}

func (d *Decoder) readRegion(body []byte) error {
	if len(body) < regionHeaderSize {
		return E.New("rfx: truncated region header: ", len(body), " of ", regionHeaderSize, " bytes")
	}
	count := int(binary.LittleEndian.Uint16(body[1:]))
	d.rects = d.rects[:0]
	if count == 0 {
		d.rects = append(d.rects, image.Rect(0, 0, d.width, d.height))
		return nil
	}
	body = body[regionHeaderSize:]
	if len(body) < count*rectangleSize {
		return E.New("rfx: truncated region rectangles: ", len(body), " of ", count*rectangleSize, " bytes for ", count, " rectangles")
	}
	d.rects = appendRectangles(d.rects, body, count)
	body = body[count*rectangleSize:]
	if len(body) < 4 {
		return E.New("rfx: truncated region type and tileset count: ", len(body), " of 4 bytes")
	}
	// FreeRDP accepts a region of the wrong type and skips the tileset count check.
	if binary.LittleEndian.Uint16(body) != blockRegionType {
		return nil
	}
	if tilesets := binary.LittleEndian.Uint16(body[2:]); tilesets != 1 {
		return E.New("rfx: invalid tileset count ", tilesets)
	}
	return nil
}

func appendRectangles(rects []image.Rectangle, src []byte, count int) []image.Rectangle {
	for i := range count {
		r := src[i*rectangleSize:]
		x := int(binary.LittleEndian.Uint16(r))
		y := int(binary.LittleEndian.Uint16(r[2:]))
		w := int(binary.LittleEndian.Uint16(r[4:]))
		h := int(binary.LittleEndian.Uint16(r[6:]))
		rects = append(rects, image.Rect(x, y, x+w, y+h))
	}
	return rects
}

func (d *Decoder) decodeTileset(body []byte, dst *bgra.Image, updated []image.Rectangle) ([]image.Rectangle, error) {
	if len(body) < tilesetSize {
		return updated, E.New("rfx: truncated tileset header: ", len(body), " of ", tilesetSize, " bytes")
	}
	if subtype := binary.LittleEndian.Uint16(body); subtype != blockTileset {
		return updated, E.New("rfx: invalid tileset subtype 0x", strconv.FormatUint(uint64(subtype), 16))
	}
	quantCount := int(body[6])
	if quantCount < 1 {
		return updated, E.New("rfx: tileset without quantization values")
	}
	tileCount := int(binary.LittleEndian.Uint16(body[8:]))
	if tileCount == 0 {
		return updated, nil
	}
	dataSize := binary.LittleEndian.Uint32(body[10:])
	body = body[tilesetSize:]
	if len(body) < quantCount*quantSize {
		return updated, E.New("rfx: truncated quantization values: ", len(body), " of ", quantCount*quantSize, " bytes for ", quantCount, " values")
	}
	d.quants = d.quants[:0]
	for i := range quantCount {
		d.quants = append(d.quants, readQuant(body[i*quantSize:], &linearQuantNibbles))
	}
	body = body[quantCount*quantSize:]
	if uint64(len(body)) < uint64(dataSize) {
		return updated, E.New("rfx: truncated tiles: ", len(body), " of ", dataSize, " bytes")
	}

	bounds := dst.Bounds()
	d.clips = d.clips[:0]
	for _, r := range d.rects {
		if clip := r.Intersect(bounds); !clip.Empty() {
			d.clips = append(d.clips, clip)
		}
	}
	d.grid.reset((bounds.Dx()+tileSize-1)/tileSize, (bounds.Dy()+tileSize-1)/tileSize)
	d.tiles = d.tiles[:0]
	d.parts = d.parts[:0]
	duplicate := false
	remaining := maxRegionIntersections
	for range tileCount {
		var (
			blockType uint16
			block     []byte
			err       error
		)
		blockType, block, body, err = nextBlock(body)
		if err != nil {
			return updated, err
		}
		if blockType != blockTile {
			return updated, E.New("rfx: invalid tile block type 0x", strconv.FormatUint(uint64(blockType), 16))
		}
		if len(block) < tileHeaderSize {
			return updated, E.New("rfx: truncated tile header: ", len(block), " of ", tileHeaderSize, " bytes")
		}
		var tile linearTile
		for c := range 3 {
			tile.quant[c] = block[c]
			if int(block[c]) >= quantCount {
				return updated, E.New("rfx: tile quantization index ", block[c], " out of ", quantCount)
			}
		}
		xIdx := int(binary.LittleEndian.Uint16(block[3:]))
		yIdx := int(binary.LittleEndian.Uint16(block[5:]))
		data := block[tileHeaderSize:]
		for c := range 3 {
			n := int(binary.LittleEndian.Uint16(block[7+2*c:]))
			if len(data) < n {
				return updated, E.New("rfx: tile component ", c, " declares ", n, " bytes, ", len(data), " remain")
			}
			tile.data[c] = data[:n]
			data = data[n:]
		}
		if !d.grid.contains(xIdx, yIdx) {
			continue
		}
		tile.origin = image.Pt(xIdx*tileSize, yIdx*tileSize)
		tile.parts[0] = len(d.parts)
		d.parts, err = appendTileParts(d.parts, image.Rect(0, 0, tileSize, tileSize).Add(tile.origin), d.clips, &remaining)
		if err != nil {
			return updated, err
		}
		tile.parts[1] = len(d.parts)
		if tile.parts[0] == tile.parts[1] {
			continue
		}
		if d.grid.set(xIdx, yIdx) {
			duplicate = true
		}
		d.tiles = append(d.tiles, tile)
	}

	d.pool.run(len(d.tiles), duplicate, func(s *scratch, i int) {
		tile := &d.tiles[i]
		for c := range 3 {
			tile.err = decodeLinearComponent(tile.data[c], &d.quants[tile.quant[c]], d.rlgr3, &s.component[c], &s.tmp)
			if tile.err != nil {
				return
			}
		}
		for _, part := range d.parts[tile.parts[0]:tile.parts[1]] {
			writeTile(dst, s, tile.origin, part)
		}
	})

	var tileErr error
	d.grid.reset(d.grid.width, d.grid.height)
	for i := range d.tiles {
		tile := &d.tiles[i]
		if tile.err != nil {
			if tileErr == nil {
				tileErr = E.Cause(tile.err, "rfx: decode tile at ", tile.origin.X, ",", tile.origin.Y)
			}
			continue
		}
		d.grid.set(tile.origin.X/tileSize, tile.origin.Y/tileSize)
	}
	updated, err := appendUpdated(updated, d.clips, &d.grid, &remaining)
	if err != nil {
		return updated, err
	}
	return updated, tileErr
}

func decodeLinearComponent(data []byte, quant *quantValues, rlgr3 bool, buf, tmp *[tileCoefficients]int16) error {
	err := rlgrDecode(data, buf, rlgr3)
	if err != nil {
		return err
	}
	differentialDecode(buf[linearLayout.offset[bandLL3]:])
	for band := range bandCount {
		// FreeRDP ignores the failed shift of a zero quantization value and leaves the band as is.
		if quant[band] > 0 {
			shiftBand(buf[linearLayout.offset[band]:][:linearLayout.length[band]], quant[band]-1)
		}
	}
	inverseDWT(buf, tmp)
	return nil
}
