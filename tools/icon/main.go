// icon renders Kyokai's icon, a ring split by the boundary with a pointer crossing it, as PNGs:
// the app icon (full colour) and the tray glyph (one colour, template).
// Usage: go run ./tools/icon resources/icon.png internal/gui/assets/tray.png
package main

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
)

type pt struct{ x, y float64 }

// The pointer, in the 100-unit design space.
var cursor = []pt{{44, 38}, {44, 60}, {49.5, 55}, {53.5, 63.5}, {57.5, 61.6}, {53.5, 53.3}, {60.5, 53.3}}

// inPoly reports whether p lies inside poly (even-odd rule).
func inPoly(p pt, poly []pt) bool {
	in := false
	for i, j := 0, len(poly)-1; i < len(poly); j, i = i, i+1 {
		a, b := poly[i], poly[j]
		if (a.y > p.y) != (b.y > p.y) && p.x < (b.x-a.x)*(p.y-a.y)/(b.y-a.y)+a.x {
			in = !in
		}
	}
	return in
}

// segDist is the distance from p to the segment ab.
func segDist(p, a, b pt) float64 {
	dx, dy := b.x-a.x, b.y-a.y
	t := math.Max(0, math.Min(1, ((p.x-a.x)*dx+(p.y-a.y)*dy)/(dx*dx+dy*dy)))
	return math.Hypot(p.x-(a.x+t*dx), p.y-(a.y+t*dy))
}

// polyDist is the distance from p to the nearest edge of poly.
func polyDist(p pt, poly []pt) float64 {
	d := math.Inf(1)
	for i, j := 0, len(poly)-1; i < len(poly); j, i = i, i+1 {
		d = math.Min(d, segDist(p, poly[i], poly[j]))
	}
	return d
}

// inRoundRect reports whether p lies in the rectangle with corner radius r.
func inRoundRect(p pt, x0, y0, x1, y1, r float64) bool {
	cx := math.Max(x0+r, math.Min(p.x, x1-r))
	cy := math.Max(y0+r, math.Min(p.y, y1-r))
	return p.x >= x0 && p.x <= x1 && p.y >= y0 && p.y <= y1 && math.Hypot(p.x-cx, p.y-cy) <= r
}

var (
	bg          = color.NRGBA{0x04, 0x34, 0x2C, 255}
	teal        = color.NRGBA{0x1D, 0x9E, 0x75, 255}
	mint        = color.NRGBA{0x9F, 0xE1, 0xCB, 255}
	foam        = color.NRGBA{0xE1, 0xF5, 0xEE, 255}
	white       = color.NRGBA{255, 255, 255, 255}
	transparent = color.NRGBA{}
)

// app is the colour at a design-space point (painter's order).
func app(p pt) color.NRGBA {
	c := transparent
	if !inRoundRect(p, 0, 0, 100, 100, 22) {
		return c
	}
	c = bg
	r := math.Hypot(p.x-50, p.y-50)
	if p.x <= 50 && r <= 31 {
		c = teal
	}
	if p.x >= 50 && r >= 26 && r <= 31 {
		c = mint
	}
	if segDist(p, pt{50, 15}, pt{50, 85}) <= 0.75 {
		c = foam
	}
	if inPoly(p, cursor) || polyDist(p, cursor) <= 0.8 {
		c = bg
	}
	if inPoly(p, cursor) && polyDist(p, cursor) > 0.8 {
		c = white
	}
	return c
}

// tray is the glyph's coverage: ring and half disc, the pointer cut out
// with a gap around it, then the pointer itself.
func tray(p pt) bool {
	// The glyph fills the canvas: scale the 19..81 circle up.
	q := pt{50 + (p.x-50)*0.66, 50 + (p.y-50)*0.66}
	r := math.Hypot(q.x-50, q.y-50)
	on := (q.x <= 50 && r <= 31) || (q.x >= 50 && r >= 25.5 && r <= 31)
	if inPoly(q, cursor) || polyDist(q, cursor) <= 2.4 {
		on = false
	}
	if inPoly(q, cursor) {
		on = true
	}
	return on
}

// render draws size×size pixels, the 100-unit design inset by margin, with
// ss×ss samples per pixel.
func render(size, margin int, ss int, at func(pt) (color.NRGBA, float64)) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	span := float64(size - 2*margin)
	for y := range size {
		for x := range size {
			var r, g, b, a float64
			for sy := range ss {
				for sx := range ss {
					px := (float64(x-margin) + (float64(sx)+0.5)/float64(ss)) / span * 100
					py := (float64(y-margin) + (float64(sy)+0.5)/float64(ss)) / span * 100
					c, cov := at(pt{px, py})
					w := cov * float64(c.A) / 255
					r += float64(c.R) * w
					g += float64(c.G) * w
					b += float64(c.B) * w
					a += w
				}
			}
			n := float64(ss * ss)
			if a > 0 {
				img.SetNRGBA(x, y, color.NRGBA{uint8(r / a), uint8(g / a), uint8(b / a), uint8(math.Round(a / n * 255))})
			}
		}
	}
	return img
}

// save writes img as a PNG.
func save(path string, img image.Image) {
	f, err := os.Create(path)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		panic(err)
	}
}

func main() {
	save(os.Args[1], render(1024, 40, 4, func(p pt) (color.NRGBA, float64) { return app(p), 1 }))
	save(os.Args[2], render(64, 0, 8, func(p pt) (color.NRGBA, float64) {
		if tray(p) {
			return white, 1
		}
		return transparent, 0
	}))
}
