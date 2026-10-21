// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/codec/rfx/progressive.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package rfx

import (
	"encoding/binary"
	"image"
	"math/bits"
	"strconv"

	"github.com/YuanZhe-99/Kyokai/internal/bgra"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const (
	progressiveSync        = 0xCCC0
	progressiveFrameBegin  = 0xCCC1
	progressiveFrameEnd    = 0xCCC2
	progressiveContext     = 0xCCC3
	progressiveRegion      = 0xCCC4
	progressiveTileSimple  = 0xCCC5
	progressiveTileFirst   = 0xCCC6
	progressiveTileUpgrade = 0xCCC7

	progressiveSyncSize        = 6
	progressiveFrameBeginSize  = 6
	progressiveContextSize     = 4
	progressiveRegionSize      = 12
	progressiveQualitySize     = 16
	progressiveTileSimpleSize  = 16
	progressiveTileFirstSize   = 17
	progressiveTileUpgradeSize = 20
	progressiveFirstLengths    = 4
	progressiveUpgradeLengths  = 6

	progressiveExtrapolate   = 0x01
	progressiveDifference    = 0x01
	progressiveFullQuality   = 0xFF
	progressiveMaxQuant      = 7
	progressiveMinQuantValue = 6
	progressiveMaxQuantValue = 15
)

const (
	progressiveFrameBegun = 1 << iota
	progressiveFrameEnded
)

var progressiveFullQuant [3]quantValues

type ProgressiveSurface struct {
	width      int
	height     int
	gridWidth  int
	gridHeight int
	tiles      []*progressiveTile
	frame      uint64
	frameTiles tileGrid
	state      uint8
	flags      uint8
	rects      []image.Rectangle
	quants     []quantValues
	progQuants [][3]quantValues
	jobs       []progressiveJob
	clips      []image.Rectangle
	parts      []image.Rectangle
	grid       tileGrid
	pool       workerPool
}

type progressiveTile struct {
	coefficients [3][tileCoefficients]int16
	sign         [3][tileCoefficients]int8
	bitPos       [3]quantValues
	extrapolate  bool
	valid        bool
}

type progressiveJob struct {
	blockType uint16
	tile      *progressiveTile
	reuse     bool
	quant     [3]uint8
	flags     uint8
	quality   uint8
	data      [progressiveUpgradeLengths][]byte
	origin    image.Point
	parts     [2]int
	err       error
}

func NewProgressiveSurface(width, height int) *ProgressiveSurface {
	gridWidth, gridHeight := (width+tileSize-1)/tileSize, (height+tileSize-1)/tileSize
	s := &ProgressiveSurface{
		width: width, height: height,
		gridWidth: gridWidth, gridHeight: gridHeight,
		tiles: make([]*progressiveTile, gridWidth*gridHeight),
	}
	s.frameTiles.reset(gridWidth, gridHeight)
	return s
}

func (s *ProgressiveSurface) Decode(src []byte, frame uint64, dst *bgra.Image) (updated []image.Rectangle, err error) {
	if frame != s.frame {
		clear(s.frameTiles.bits)
		s.frame = frame
	}
	s.state = 0
	for len(src) > 0 {
		var (
			blockType uint16
			body      []byte
		)
		blockType, body, src, err = nextBlock(src)
		if err != nil {
			return updated, err
		}
		switch blockType {
		case progressiveSync:
			if len(body) != progressiveSyncSize {
				return updated, E.New("rfx: invalid progressive sync length ", len(body), ", want ", progressiveSyncSize)
			}
			if magic := binary.LittleEndian.Uint32(body); magic != syncMagic {
				return updated, E.New("rfx: invalid progressive sync magic 0x", strconv.FormatUint(uint64(magic), 16))
			}
			if version := binary.LittleEndian.Uint16(body[4:]); version != syncVersion {
				return updated, E.New("rfx: unsupported progressive version 0x", strconv.FormatUint(uint64(version), 16))
			}
		case progressiveFrameBegin:
			if len(body) != progressiveFrameBeginSize {
				return updated, E.New("rfx: invalid progressive frame begin length ", len(body), ", want ", progressiveFrameBeginSize)
			}
			if s.state != 0 {
				return updated, E.New("rfx: unexpected progressive frame begin")
			}
			s.state |= progressiveFrameBegun
		case progressiveFrameEnd:
			if len(body) != 0 {
				return updated, E.New("rfx: invalid progressive frame end length ", len(body), ", want 0")
			}
			s.state |= progressiveFrameEnded
		case progressiveContext:
			if len(body) != progressiveContextSize {
				return updated, E.New("rfx: invalid progressive context length ", len(body), ", want ", progressiveContextSize)
			}
			if size := binary.LittleEndian.Uint16(body[1:]); size != tileSize {
				return updated, E.New("rfx: unsupported progressive tile size ", size)
			}
		case progressiveRegion:
			updated, err = s.decodeRegion(body, dst, updated)
			if err != nil {
				return updated, err
			}
		default:
			return updated, E.New("rfx: unexpected progressive block 0x", strconv.FormatUint(uint64(blockType), 16))
		}
	}
	return updated, nil
}

func (s *ProgressiveSurface) decodeRegion(body []byte, dst *bgra.Image, updated []image.Rectangle) ([]image.Rectangle, error) {
	if len(body) < progressiveRegionSize {
		return updated, E.New("rfx: truncated progressive region header: ", len(body), " of ", progressiveRegionSize, " bytes")
	}
	if body[0] != tileSize {
		return updated, E.New("rfx: unsupported progressive region tile size ", body[0])
	}
	rectCount := int(binary.LittleEndian.Uint16(body[1:]))
	quantCount := int(body[3])
	progQuantCount := int(body[4])
	flags := body[5]
	tileCount := int(binary.LittleEndian.Uint16(body[6:]))
	tileDataSize := uint64(binary.LittleEndian.Uint32(body[8:]))
	if rectCount < 1 {
		return updated, E.New("rfx: progressive region without rectangles")
	}
	if quantCount > progressiveMaxQuant {
		return updated, E.New("rfx: progressive region has ", quantCount, " quantization values, more than ", progressiveMaxQuant)
	}
	body = body[progressiveRegionSize:]
	headerSize := rectCount*rectangleSize + quantCount*quantSize + progQuantCount*progressiveQualitySize
	if uint64(len(body)) < uint64(headerSize)+tileDataSize {
		return updated, E.New("rfx: truncated progressive region: ", len(body), " of ", uint64(headerSize)+tileDataSize, " bytes")
	}
	if s.state != progressiveFrameBegun {
		return updated, nil
	}
	if uint64(len(body)) != uint64(headerSize)+tileDataSize {
		return updated, E.New("rfx: progressive region has ", uint64(len(body))-uint64(headerSize)-tileDataSize, " trailing bytes")
	}
	s.flags = flags
	s.rects = appendRectangles(s.rects[:0], body, rectCount)
	body = body[rectCount*rectangleSize:]
	s.quants = s.quants[:0]
	for i := range quantCount {
		q := readQuant(body[i*quantSize:], &progressiveQuantNibbles)
		for _, v := range q {
			if v < progressiveMinQuantValue || v > progressiveMaxQuantValue {
				return updated, E.New("rfx: progressive quantization value ", v, " out of range ", progressiveMinQuantValue, " to ", progressiveMaxQuantValue)
			}
		}
		s.quants = append(s.quants, q)
	}
	body = body[quantCount*quantSize:]
	s.progQuants = s.progQuants[:0]
	for i := range progQuantCount {
		entry := body[i*progressiveQualitySize+1:]
		s.progQuants = append(s.progQuants, [3]quantValues{
			readQuant(entry, &progressiveQuantNibbles),
			readQuant(entry[quantSize:], &progressiveQuantNibbles),
			readQuant(entry[2*quantSize:], &progressiveQuantNibbles),
		})
	}
	body = body[progQuantCount*progressiveQualitySize:]

	bounds := dst.Bounds().Intersect(image.Rect(0, 0, s.width, s.height))
	s.clips = s.clips[:0]
	for _, r := range s.rects {
		if clip := r.Intersect(bounds); !clip.Empty() {
			s.clips = append(s.clips, clip)
		}
	}
	s.grid.reset(s.gridWidth, s.gridHeight)
	clear(s.jobs)
	s.jobs = s.jobs[:0]
	s.parts = s.parts[:0]
	duplicate := false
	remaining := maxRegionIntersections
	count := 0
	for len(body) > 0 {
		var (
			blockType uint16
			block     []byte
			err       error
		)
		blockType, block, body, err = nextBlock(body)
		if err != nil {
			return updated, err
		}
		count++
		job := progressiveJob{blockType: blockType, quality: progressiveFullQuality}
		var headerSize, lengths int
		switch blockType {
		case progressiveTileSimple:
			headerSize, lengths = progressiveTileSimpleSize, progressiveFirstLengths
		case progressiveTileFirst:
			headerSize, lengths = progressiveTileFirstSize, progressiveFirstLengths
		case progressiveTileUpgrade:
			headerSize, lengths = progressiveTileUpgradeSize, progressiveUpgradeLengths
		default:
			return updated, E.New("rfx: invalid progressive tile block type 0x", strconv.FormatUint(uint64(blockType), 16))
		}
		if len(block) < headerSize {
			return updated, E.New("rfx: truncated progressive tile header: ", len(block), " of ", headerSize, " bytes")
		}
		copy(job.quant[:], block[:3])
		xIdx := int(binary.LittleEndian.Uint16(block[3:]))
		yIdx := int(binary.LittleEndian.Uint16(block[5:]))
		switch blockType {
		case progressiveTileSimple:
			job.flags = block[7]
		case progressiveTileFirst:
			job.flags = block[7]
			job.quality = block[8]
		case progressiveTileUpgrade:
			job.quality = block[7]
		}
		data := block[headerSize:]
		for i := range lengths {
			n := int(binary.LittleEndian.Uint16(block[headerSize-2*lengths+2*i:]))
			if len(data) < n {
				return updated, E.New("rfx: progressive tile data ", i, " declares ", n, " bytes, ", len(data), " remain")
			}
			job.data[i] = data[:n]
			data = data[n:]
		}
		if len(data) != 0 {
			return updated, E.New("rfx: progressive tile has ", len(data), " trailing bytes")
		}
		if xIdx >= s.gridWidth || yIdx >= s.gridHeight {
			continue
		}
		job.origin = image.Pt(xIdx*tileSize, yIdx*tileSize)
		job.parts[0] = len(s.parts)
		s.parts, err = appendTileParts(s.parts, image.Rect(0, 0, tileSize, tileSize).Add(job.origin), s.clips, &remaining)
		if err != nil {
			return updated, err
		}
		job.parts[1] = len(s.parts)
		if s.grid.set(xIdx, yIdx) {
			duplicate = true
		}
		index := yIdx*s.gridWidth + xIdx
		job.tile = s.tiles[index]
		if job.tile == nil && blockType != progressiveTileUpgrade {
			job.tile = new(progressiveTile)
			s.tiles[index] = job.tile
		}
		s.jobs = append(s.jobs, job)
	}
	if count != tileCount {
		return updated, E.New("rfx: progressive region has ", count, " tiles, expected ", tileCount)
	}
	err := s.reuseFrameTiles(&remaining)
	if err != nil {
		return updated, err
	}

	extrapolate := s.flags&progressiveExtrapolate != 0
	s.pool.run(len(s.jobs), duplicate, func(sc *scratch, i int) {
		job := &s.jobs[i]
		tileExtrapolate := extrapolate
		switch {
		case job.reuse:
			sc.component = job.tile.coefficients
			tileExtrapolate = job.tile.extrapolate
		case job.blockType == progressiveTileUpgrade:
			job.err = s.upgradeTile(job, sc)
		default:
			job.err = s.decodeFirstTile(job, sc)
		}
		if job.err != nil || job.parts[0] == job.parts[1] {
			return
		}
		for c := range 3 {
			if tileExtrapolate {
				inverseDWTExtrapolate(&sc.component[c], &sc.tmp)
			} else {
				inverseDWT(&sc.component[c], &sc.tmp)
			}
		}
		for _, part := range s.parts[job.parts[0]:job.parts[1]] {
			writeTile(dst, sc, job.origin, part)
		}
	})

	var tileErr error
	s.grid.reset(s.gridWidth, s.gridHeight)
	for i := range s.jobs {
		job := &s.jobs[i]
		x, y := job.origin.X/tileSize, job.origin.Y/tileSize
		if job.err != nil {
			s.frameTiles.unset(x, y)
			if tileErr == nil {
				tileErr = E.Cause(job.err, "rfx: decode progressive tile at ", job.origin.X, ",", job.origin.Y)
			}
			continue
		}
		s.frameTiles.set(x, y)
		if job.parts[0] != job.parts[1] {
			s.grid.set(x, y)
		}
	}
	updated, err = appendUpdated(updated, s.clips, &s.grid, &remaining)
	if err != nil {
		return updated, err
	}
	return updated, tileErr
}

func (s *ProgressiveSurface) reuseFrameTiles(remaining *int) error {
	if len(s.clips) == 0 {
		return nil
	}
	for wordIndex, word := range s.frameTiles.bits {
		for word != 0 {
			index := wordIndex*64 + bits.TrailingZeros64(word)
			word &= word - 1
			x, y := index%s.gridWidth, index/s.gridWidth
			if s.grid.test(x, y) {
				continue
			}
			job := progressiveJob{tile: s.tiles[index], reuse: true, origin: image.Pt(x*tileSize, y*tileSize)}
			job.parts[0] = len(s.parts)
			var err error
			s.parts, err = appendTileParts(s.parts, image.Rect(0, 0, tileSize, tileSize).Add(job.origin), s.clips, remaining)
			if err != nil {
				return err
			}
			job.parts[1] = len(s.parts)
			if job.parts[0] != job.parts[1] {
				s.jobs = append(s.jobs, job)
			}
		}
	}
	return nil
}

func (s *ProgressiveSurface) jobQuants(job *progressiveJob) (quants [3]*quantValues, prog *[3]quantValues, err error) {
	for c := range 3 {
		if int(job.quant[c]) >= len(s.quants) {
			return quants, nil, E.New("quantization index ", job.quant[c], " out of ", len(s.quants))
		}
		quants[c] = &s.quants[job.quant[c]]
	}
	if job.quality == progressiveFullQuality {
		return quants, &progressiveFullQuant, nil
	}
	if int(job.quality) >= len(s.progQuants) {
		return quants, nil, E.New("quality ", job.quality, " out of ", len(s.progQuants))
	}
	return quants, &s.progQuants[job.quality], nil
}

func (s *ProgressiveSurface) decodeFirstTile(job *progressiveJob, sc *scratch) error {
	quants, prog, err := s.jobQuants(job)
	if err != nil {
		return err
	}
	tile := job.tile
	tile.valid = false
	for c := range 3 {
		for band := range bandCount {
			tile.bitPos[c][band] = quants[c][band] + prog[c][band]
		}
	}
	extrapolate := s.flags&progressiveExtrapolate != 0
	layout := &linearLayout
	if extrapolate {
		layout = &extrapolateLayout
	}
	for c := range 3 {
		buf := &sc.component[c]
		current := &tile.coefficients[c]
		err = rlgrDecode(job.data[c], buf, false)
		if err != nil {
			return err
		}
		sign := &tile.sign[c]
		for i, v := range buf {
			sign[i] = int8(min(max(v, -1), 1))
		}
		var shifts quantValues
		for band := range bandCount {
			shifts[band] = tile.bitPos[c][band] - 1
			if shifts[band] >= 16 {
				return E.New("shift ", shifts[band], " of band ", band, " out of range")
			}
		}
		if extrapolate {
			for band := range bandCount - 1 {
				shiftBand(buf[layout.offset[band]:][:layout.length[band]], shifts[band])
			}
			differentialDecode(buf[layout.offset[bandLL3]:])
			shiftBand(buf[layout.offset[bandLL3]:], shifts[bandLL3])
		} else {
			differentialDecode(buf[layout.offset[bandLL3]:])
			for band := range bandCount {
				shiftBand(buf[layout.offset[band]:][:layout.length[band]], shifts[band])
			}
		}
		if job.flags&progressiveDifference == 0 {
			*current = *buf
			continue
		}
		for i, v := range buf {
			sum := int16(min(max(int32(v)+int32(current[i]), -32768), 32767))
			buf[i] = sum
			current[i] = sum
		}
	}
	tile.extrapolate = extrapolate
	tile.valid = true
	return nil
}

func (s *ProgressiveSurface) upgradeTile(job *progressiveJob, sc *scratch) error {
	quants, prog, err := s.jobQuants(job)
	if err != nil {
		return err
	}
	tile := job.tile
	if tile == nil || !tile.valid {
		return E.New("upgrade without a first pass")
	}
	tile.valid = false
	var bitPos, numBits [3]quantValues
	for c := range 3 {
		for band := range bandCount {
			bitPos[c][band] = quants[c][band] + prog[c][band]
			if tile.bitPos[c][band] < bitPos[c][band] {
				return E.New("upgrade raises bit position of band ", band, " from ", tile.bitPos[c][band], " to ", bitPos[c][band])
			}
			numBits[c][band] = tile.bitPos[c][band] - bitPos[c][band]
		}
	}
	tile.bitPos = bitPos
	for c := range 3 {
		var srl srlReader
		srl.reset(job.data[2*c])
		var raw bitReader
		raw.reset(job.data[2*c+1])
		current := &tile.coefficients[c]
		sign := &tile.sign[c]
		// FreeRDP walks upgrade bands with the reduce-extrapolate layout even when the region
		// uses the linear one.
		for band := range bandCount - 1 {
			n := uint(numBits[c][band])
			if n == 0 {
				continue
			}
			shift := bitPos[c][band] - 1
			start := extrapolateLayout.offset[band]
			end := start + extrapolateLayout.length[band]
			for i := start; i < end; i++ {
				var input int32
				switch {
				case sign[i] > 0:
					raw.refill()
					input = int32(int16(raw.read(n)))
				case sign[i] < 0:
					raw.refill()
					input = -int32(int16(raw.read(n)))
				case srl.nz > 0:
					srl.nz--
					continue
				default:
					input = int32(srl.read(n))
					sign[i] = int8(min(max(input, -1), 1))
				}
				current[i] = int16(int32(current[i]) + input<<shift)
			}
		}
		if n := uint(numBits[c][bandLL3]); n > 0 {
			shift := bitPos[c][bandLL3] - 1
			for i := extrapolateLayout.offset[bandLL3]; i < tileCoefficients; i++ {
				raw.refill()
				current[i] = int16(int32(current[i]) + int32(int16(raw.read(n)))<<shift)
			}
		}
		sc.component[c] = *current
	}
	tile.extrapolate = s.flags&progressiveExtrapolate != 0
	tile.valid = true
	return nil
}

type srlReader struct {
	bitReader
	kp   uint
	nz   int
	mode bool
}

func (r *srlReader) reset(src []byte) {
	r.bitReader.reset(src)
	r.kp = 1 << lsgr
	r.nz = 0
	r.mode = false
}

func (r *srlReader) read(numBits uint) int16 {
	k := r.kp >> lsgr
	r.refill()
	if !r.mode {
		if r.bitReader.read(1) == 0 {
			r.nz = 1<<k - 1
			r.kp = min(r.kp+upGR, kpMax)
			return 0
		}
		r.mode = true
		r.nz = int(r.bitReader.read(k))
		if r.nz > 0 {
			r.nz--
			return 0
		}
	}
	r.mode = false
	sign := r.bitReader.read(1)
	r.kp = max(r.kp, dnGR) - dnGR
	if numBits == 1 {
		if sign != 0 {
			return -1
		}
		return 1
	}
	magnitude := uint32(1)
	limit := uint32(1)<<numBits - 1
	for magnitude < limit {
		if r.left <= 0 {
			magnitude = limit
			break
		}
		r.refill()
		zeros := uint32(min(uint(bits.LeadingZeros64(r.acc)), r.have))
		if need := limit - magnitude; zeros >= need {
			r.skip(uint(need))
			magnitude = limit
			break
		}
		if zeros < uint32(r.have) {
			r.skip(uint(zeros) + 1)
			magnitude += zeros
			break
		}
		r.skip(uint(zeros))
		magnitude += zeros
	}
	magnitude = min(magnitude, 32767)
	if sign != 0 {
		return -int16(magnitude)
	}
	return int16(magnitude)
}
