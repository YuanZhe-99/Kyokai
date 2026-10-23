package gui

import (
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestWordsContrast: words in Kyokai's colours reach WCAG AA's 4.5:1 in
// both appearances — muted text on the page, on a card's surface and on
// the tints of the cards above the panes, and a status colour or the
// accent as words (textTone) on the page, on the surface and on its own
// tint, as pills and chosen tiles show it.
func TestWordsContrast(t *testing.T) {
	for _, th := range []*ui.Theme{readableTheme(ui.LightTheme()), readableTheme(ui.DarkTheme())} {
		check := func(name string, fg, bg ui.Color) {
			t.Helper()
			if r := contrast(fg, bg); r < 4.5 {
				t.Errorf("dark %v: %s: %.2f:1, under 4.5:1", th.Dark, name, r)
			}
		}
		for g, bg := range map[string]ui.Color{"page": th.Background, "surface": th.Surface} {
			check("muted on the "+g, th.TextMuted, bg)
			for name, col := range map[string]ui.Color{"warning": th.Warning, "success": th.Success, "danger": th.Danger, "accent": th.Accent} {
				check(name+" on the "+g, textTone(th, col), bg)
				check(name+" on its tint over the "+g, textTone(th, col), over(col.Alpha(0.12), bg))
			}
		}
		check("muted on a decision's card", th.TextMuted, over(th.Accent.Alpha(0.08), th.Background))
		check("muted on a card of someone at work", th.TextMuted, over(th.Warning.Alpha(0.08), th.Background))
	}
}

// over is fg, with its alpha, laid over an opaque bg.
func over(fg, bg ui.Color) ui.Color {
	a := float64(fg.A) / 255
	mix := func(f, b uint8) uint8 { return uint8(math.Round(float64(f)*a + float64(b)*(1-a))) }
	return ui.Color{R: mix(fg.R, bg.R), G: mix(fg.G, bg.G), B: mix(fg.B, bg.B), A: 255}
}

// contrast is WCAG's contrast ratio of two opaque colours.
func contrast(a, b ui.Color) float64 {
	la, lb := luminance(a), luminance(b)
	return (max(la, lb) + 0.05) / (min(la, lb) + 0.05)
}

// luminance is WCAG's relative luminance of an sRGB colour.
func luminance(c ui.Color) float64 {
	lin := func(v uint8) float64 {
		s := float64(v) / 255
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(c.R) + 0.7152*lin(c.G) + 0.0722*lin(c.B)
}
