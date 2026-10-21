// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/codec/rfx/grid.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package rfx

import (
	"image"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

func appendTileParts(parts []image.Rectangle, tile image.Rectangle, clips []image.Rectangle, remaining *int) ([]image.Rectangle, error) {
	start := len(parts)
	for _, clip := range clips {
		if *remaining == 0 {
			return parts, E.New("rfx: region exceeds ", maxRegionIntersections, " tile intersections")
		}
		*remaining--
		if part := tile.Intersect(clip); !part.Empty() {
			if part == tile {
				if start >= maxRegionParts {
					return parts, E.New("rfx: region exceeds ", maxRegionParts, " tile parts")
				}
				return append(parts[:start], part), nil
			}
			if len(parts) >= maxRegionParts {
				return parts, E.New("rfx: region exceeds ", maxRegionParts, " tile parts")
			}
			parts = append(parts, part)
		}
	}
	return parts, nil
}

type tileGrid struct {
	width  int
	height int
	bits   []uint64
}

func (g *tileGrid) reset(width, height int) {
	g.width = max(width, 0)
	g.height = max(height, 0)
	words := (g.width*g.height + 63) / 64
	if cap(g.bits) < words {
		g.bits = make([]uint64, words)
	} else {
		g.bits = g.bits[:words]
		clear(g.bits)
	}
}

func (g *tileGrid) contains(x, y int) bool {
	return x >= 0 && y >= 0 && x < g.width && y < g.height
}

func (g *tileGrid) set(x, y int) bool {
	i := y*g.width + x
	word, bit := i/64, uint64(1)<<(i%64)
	old := g.bits[word]&bit != 0
	g.bits[word] |= bit
	return old
}

func (g *tileGrid) unset(x, y int) {
	i := y*g.width + x
	g.bits[i/64] &^= 1 << (i % 64)
}

func (g *tileGrid) test(x, y int) bool {
	i := y*g.width + x
	return g.bits[i/64]&(1<<(i%64)) != 0
}

func appendUpdated(updated []image.Rectangle, clips []image.Rectangle, grid *tileGrid, remaining *int) ([]image.Rectangle, error) {
	for _, clip := range clips {
		x0, y0 := clip.Min.X/tileSize, clip.Min.Y/tileSize
		x1, y1 := (clip.Max.X-1)/tileSize, (clip.Max.Y-1)/tileSize
		complete := true
		for y := y0; y <= y1 && complete; y++ {
			for x := x0; x <= x1; x++ {
				if *remaining == 0 {
					return updated, E.New("rfx: region exceeds ", maxRegionIntersections, " tile intersections")
				}
				*remaining--
				if !grid.test(x, y) {
					complete = false
					break
				}
			}
		}
		if complete {
			if len(updated) >= maxRegionParts {
				return updated, E.New("rfx: region exceeds ", maxRegionParts, " updated rectangles")
			}
			updated = append(updated, clip)
			continue
		}
		for y := y0; y <= y1; y++ {
			for x := x0; x <= x1; x++ {
				if *remaining == 0 {
					return updated, E.New("rfx: region exceeds ", maxRegionIntersections, " tile intersections")
				}
				*remaining--
				if grid.test(x, y) {
					if len(updated) >= maxRegionParts {
						return updated, E.New("rfx: region exceeds ", maxRegionParts, " updated rectangles")
					}
					tile := image.Rect(x*tileSize, y*tileSize, (x+1)*tileSize, (y+1)*tileSize)
					updated = append(updated, tile.Intersect(clip))
				}
			}
		}
	}
	return updated, nil
}
