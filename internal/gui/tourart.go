package gui

import (
	"cmp"
	"math"
	"runtime"
	"slices"
	"time"

	"github.com/egoist/mygo/ui"

	L "github.com/YuanZhe-99/Kyokai/locales"
)

// Layout and timing of the tour's illustrations (doc/en-us/gui.md, "First
// run"). Each is drawn in an artW×artH space and scaled to fit its column.
// The computers and their projection come from EasyTier MyGo's tour (same
// author, relicensed AGPL-3.0 here).
const (
	artW       float32 = 320
	artH       float32 = 580
	artPeerR   float32 = 22 // a device's round node
	artDash    float32 = 3  // a flow's dash
	artDashGap float32 = 7  // and the gap after it
	artFlowW   float32 = 2  // a flow's width
	artScreenR float32 = 10 // a screen's corners

	artFlowTime = 900 * time.Millisecond
	artPointer  = 6 * time.Second // a pointer's round on a screen
	artPause    = 8 * time.Second // the AI working, then paused
	artBob      = 5 * time.Second
	artSleep    = 8 * time.Second // awake, asleep and back
)

// The illustrations' fixed colors: the computers are real objects and keep
// their finish in both appearances; the light's; the check's; the input of
// someone let in (orange, as uploads are); the screens of other systems.
var (
	artLED      = ui.Hex("#34D399")
	artLEDSleep = ui.Hex("#F59E0B")
	artSunInk   = ui.Hex("#D97706")
	artWhite    = ui.Hex("#FFFFFF")
	artInput    = ui.Hex("#EA7A12")
	artTerminal = ui.Hex("#16171B")
	artPrompt   = ui.Hex("#3A4150")
	artSignIn   = ui.Hex("#1E3A6E")
	artRDP      = ui.Hex("#1F4E8C")
	artVNC      = ui.Hex("#3B3F57")
	artBar      = ui.Hex("#C9CDD3")
	artGreen    = ui.Hex("#7EE2A8")
)

// artPlatform is the system whose computer the illustrations show (and
// whose steps the tour has); tests set it to draw each one.
var artPlatform = runtime.GOOS

// artState is what an illustration shows. The view animates the values and
// the illustration only draws them.
type artState struct {
	step    tourStep
	direct  float32    // the network step: 1 direct, 0 through the server
	ask     float32    // letting others in: 1 without a password
	kind    int        // connecting: the Open as choice shown (openAs)
	reach   [3]float32 // when nobody is here: each screen others reach, 0 to 1
	granted [4]bool    // the privacy permissions allowed (macOS)
	pop     float32    // the ready step's check, 0 to 1
	mode    int        // the run mode chosen (modeUser…)
	device  string     // this computer's Device ID, grouped, or ""
	still   bool       // Reduce Motion: nothing moves by itself
}

// artColors are an illustration's theme colors.
type artColors struct {
	text, muted, card, edge, surface, flow, accent, good, warn ui.Color
}

// artPainter draws in the illustration's space.
type artPainter struct {
	p   *ui.Painter
	col artColors
	now time.Time
	st  artState
}

// drawArt draws the illustration of s into r, centred and scaled to fit.
// Side effects: asks for the next frame while something moves.
func drawArt(p *ui.Painter, r ui.Rect, s artState, col artColors) {
	scale := min(r.W/artW, r.H/artH)
	ox, oy := r.X+(r.W-artW*scale)/2, r.Y+(r.H-artH*scale)/2
	if !s.still {
		p.AnimationFrame()
	}
	d := artPainter{p: p, col: col, now: p.Now(), st: s}
	p.Transform(ui.Scaling(scale, scale).Then(ui.Translation(ox, oy)), 0, 0, func() {
		switch s.step {
		case tourNetwork:
			d.networkScene()
		case tourConnect:
			d.connectScene()
		case tourLetIn:
			d.letInScene()
		case tourAI:
			d.aiScene()
		case tourPrivacy:
			d.privacyScene()
		case tourAway:
			d.awayScene()
		case tourAwake:
			d.awakeScene()
		case tourReady:
			d.readyScene()
		default:
			d.welcomeScene()
		}
	})
}

// phase is how far a loop of period has come, from 0 to 1 (0 when still).
func (d artPainter) phase(period time.Duration) float32 {
	if d.st.still {
		return 0
	}
	return float32(d.now.UnixMilli()%period.Milliseconds()) / float32(period.Milliseconds())
}

// bob is how far this computer floats up and down, 0 when still.
func (d artPainter) bob() float32 {
	if d.st.still {
		return 0
	}
	return -2.5 + 2.5*float32(math.Cos(2*math.Pi*float64(d.phase(artBob))))
}

// text draws s with its top-left corner at x, y (align 0), centred on x
// (align 1) or ending at x (align 2), wrapping at width when it is not 0.
func (d artPainter) text(x, y float32, s string, size float32, weight int, c ui.Color, align int, width float32) {
	if c.A == 0 { // a span without a color would take the default one
		return
	}
	span := ui.Span{Text: s, Size: size, Weight: weight, Color: c}
	w, _ := d.p.MeasureText(width, span)
	switch align {
	case 1:
		x -= w / 2
	case 2:
		x -= w
	}
	d.p.RichText(x, y, width, span)
}

// flow draws a dashed line from (x0, y0) to (x1, y1) whose dashes move
// toward its end, as data going that way.
func (d artPainter) flow(x0, y0, x1, y1 float32, c ui.Color, moving bool) {
	dx, dy := x1-x0, y1-y0
	n := float32(math.Hypot(float64(dx), float64(dy)))
	if n == 0 || c.A == 0 {
		return
	}
	period := artDash + artDashGap
	off := float32(0)
	if moving {
		off = d.phase(artFlowTime) * period
	}
	var path ui.Path
	for s := off - period; s < n; s += period {
		a, b := max(s, 0), min(s+artDash, n)
		if b <= a {
			continue
		}
		path.MoveTo(x0+dx*a/n, y0+dy*a/n).LineTo(x0+dx*b/n, y0+dy*b/n)
	}
	d.p.StrokePath(&path, artFlowW, c)
}

// peer draws a device or a server: a round node with its icon.
func (d artPainter) peer(x, y float32, icon *ui.SVG) {
	node := (&ui.Path{}).Circle(x, y, artPeerR)
	d.p.FillPath(node, d.col.card)
	d.p.StrokePath(node, 1.5, d.col.edge)
	d.p.Icon(icon, ui.Rect{X: x - 10, Y: y - 10, W: 20, H: 20}, d.col.text)
}

// chip draws a rounded label centred on (x, y), in face with its words in
// ink, an icon before them when there is one.
func (d artPainter) chip(x, y float32, icon *ui.SVG, s string, face, ink ui.Color) {
	span := ui.Span{Text: s, Size: sizeSmall, Weight: 600, Color: ink}
	w, h := d.p.MeasureText(0, span)
	iw := float32(0)
	if icon != nil {
		iw = 18
	}
	cw := w + 20 + iw
	x0 := min(max(x-cw/2, 4), artW-4-cw)
	d.p.Fill(ui.Rect{X: x0, Y: y - 12, W: cw, H: 24}, face, 12)
	if icon != nil {
		d.p.Icon(icon, ui.Rect{X: x0 + 9, Y: y - 6, W: 12, H: 12}, ink)
	}
	d.p.RichText(x0+10+iw, y-h/2, 0, span)
}

// bar draws a rounded line of text's place, w wide.
func (d artPainter) bar(x, y, w float32, c ui.Color) {
	d.p.Fill(ui.Rect{X: x, Y: y, W: w, H: 6}, c, 3)
}

// pointer draws a mouse pointer with its tip at (x, y).
func (d artPainter) pointer(x, y float32, c ui.Color) {
	var p ui.Path
	p.MoveTo(x, y).LineTo(x, y+15).LineTo(x+4, y+11.3).LineTo(x+6.8, y+17.5).LineTo(x+9.4, y+16.3).LineTo(x+6.6, y+10.3).LineTo(x+12, y+9.9).Close()
	d.p.FillPath(&p, c)
	d.p.StrokePath(&p, 1.2, artWhite)
}

// roam is where a pointer is on its round: from its start, out and back.
func (d artPainter) roam(dx, dy float32) (float32, float32) {
	ph := d.phase(artPointer)
	k := float32(math.Sin(math.Pi * float64(ph)))
	j := float32(math.Sin(2 * math.Pi * float64(ph)))
	return dx*k + dx*0.3*j, dy * k
}

// window draws an app's window on a screen: its face and its title strip.
func (d artPainter) window(r ui.Rect, face, strip ui.Color) {
	d.p.Fill(r, face, 5)
	d.p.Stroke(r, d.col.edge, 5, 1)
	d.p.Fill(ui.Rect{X: r.X, Y: r.Y, W: r.W, H: 12}, strip, 5)
}

// computer draws this computer in perspective with its top's centre at
// (x, y), and its light, lit or asleep.
func (d artPainter) computer(x, y float32, asleep float32) {
	m := computerModel(artPlatform)
	m.draw(d.p, x, y)
	d.p.FillPath((&ui.Path{}).Circle(x+m.led[0], y+m.led[1], 2.2), artLED.Mix(artLEDSleep, asleep))
}

// thisComputer draws this computer and its name (and Device ID) under it.
func (d artPainter) thisComputer(y float32) {
	d.computer(160, y+d.bob(), 0)
	d.text(160, y+104, L.TourArtThis(), 13, 600, d.col.text, 1, 0)
	if d.st.device != "" {
		d.text(160, y+124, d.st.device, sizeSmall, 400, d.col.muted, 1, 0)
	}
}

// welcomeScene: another computer's screen shown here, its picture coming
// to this computer and this computer's mouse and keyboard going there.
func (d artPainter) welcomeScene() {
	c := d.col
	d.text(160, 72, L.TourArtOther(), sizeSmall, 400, c.muted, 1, 0)
	d.desktop(ui.Rect{X: 60, Y: 96, W: 200, H: 124}, c.text)
	d.flow(148, 228, 148, 300, c.flow, true)
	d.flow(172, 300, 172, 228, artInput, true)
	d.thisComputer(350)
	d.flow(70, 530, 90, 530, c.flow, false)
	d.text(98, 522, L.TourArtPicture(), sizeSmall, 400, c.muted, 0, 210)
	d.flow(70, 554, 90, 554, artInput, false)
	d.text(98, 546, L.TourArtInput(), sizeSmall, 400, c.muted, 0, 210)
}

// desktop draws a screen with two windows and a pointer going round it.
func (d artPainter) desktop(r ui.Rect, ptr ui.Color) {
	c := d.col
	d.p.Fill(r, c.card, artScreenR)
	d.p.Stroke(r, c.edge, artScreenR, 1.5)
	d.p.Fill(ui.Rect{X: r.X, Y: r.Y, W: r.W, H: 20}, c.surface, artScreenR)
	for i := range 3 {
		d.p.FillPath((&ui.Path{}).Circle(r.X+14+float32(i)*10, r.Y+10, 3), c.edge)
	}
	d.p.Fill(ui.Rect{X: r.X + 14, Y: r.Y + 32, W: r.W * 0.48, H: r.H * 0.5}, c.accent.Alpha(0.16), 5)
	d.p.Fill(ui.Rect{X: r.X + r.W*0.62, Y: r.Y + 32, W: r.W * 0.32, H: r.H * 0.62}, c.surface, 5)
	dx, dy := d.roam(46, 24)
	d.pointer(r.X+r.W*0.3+dx, r.Y+r.H*0.42+dy, ptr)
}

// networkScene: this computer and another reaching each other through
// their network's server, or directly.
func (d artPainter) networkScene() {
	c, k := d.col, d.st.direct
	via := c.flow.Alpha(1 - 0.78*k)
	d.flow(160, 136, 160, 300, via, k < 0.5)
	d.flow(76, 232, 146, 130, via, k < 0.5)
	d.flow(80, 268, 128, 316, c.flow.Alpha(k), true)
	d.peer(160, 112, rowServer)
	d.text(160, 72, L.TourArtServer(), sizeSmall, 400, c.muted, 1, 0)
	d.peer(60, 250, kindDesktop)
	d.text(60, 210, L.TourArtOther(), sizeSmall, 400, c.muted, 1, 240)
	d.thisComputer(360)
	caption := L.TourArtViaServer()
	if k >= 0.5 {
		caption = L.TourArtDirect()
	}
	d.text(160, 532, caption, sizeSmall, 400, c.text, 1, 280)
}

// connectScene: a pane of Kyokai showing another computer as what Open as
// chose.
func (d artPainter) connectScene() {
	c := d.col
	pane := ui.Rect{X: 20, Y: 100, W: 280, H: 300}
	d.p.Fill(pane, c.card, paneRadius)
	d.p.Stroke(pane, c.edge, paneRadius, 1.5)
	icons := []*ui.SVG{kindDesktop, kindSSH, kindRDP, kindVNC, navAI}
	names := []string{L.PaneDesktop(), L.ServiceSSH(), L.ServiceRDP(), L.ServiceVNC(), L.HomeOpenAI()}
	k := min(max(d.st.kind, 0), len(icons)-1)
	d.p.Icon(icons[k], ui.Rect{X: 34, Y: 112, W: 16, H: 16}, c.muted)
	d.text(58, 112, L.TourArtOther(), 13, 600, c.text, 0, 0)
	d.text(286, 113, names[k], sizeSmall, 400, c.muted, 2, 0)
	d.p.Line(20, 140, 300, 140, 1, c.edge)
	r := ui.Rect{X: 30, Y: 150, W: 260, H: 240}
	switch k {
	case 1:
		d.p.Fill(r, artTerminal, 6)
		for i, w := range []float32{120, 170, 96, 150, 120} {
			y := r.Y + 18 + float32(i)*20
			ink := artWhite.Alpha(0.85)
			if i%2 == 0 {
				d.bar(r.X+14, y, 70, artGreen)
				ink = artWhite.Alpha(0.6)
				w -= 70
				d.bar(r.X+90, y, w, ink)
				continue
			}
			d.bar(r.X+14, y, w, ink)
		}
		if d.phase(time.Second) < 0.5 {
			d.p.Fill(ui.Rect{X: r.X + 90, Y: r.Y + 114, W: 7, H: 12}, artWhite, 0)
		}
	case 2:
		d.p.Fill(r, artRDP, 6)
		d.p.Fill(ui.Rect{X: r.X, Y: r.Y + r.H - 22, W: r.W, H: 22}, ui.Hex("#20232A"), 0)
		d.window(ui.Rect{X: r.X + 24, Y: r.Y + 24, W: 150, H: 110}, ui.Hex("#F3F4F6"), artWhite)
		d.bar(r.X+36, r.Y+52, 96, artBar)
		d.bar(r.X+36, r.Y+66, 120, artBar)
		d.pointer(r.X+150, r.Y+150, c.text)
	case 3:
		d.p.Fill(r, artVNC, 6)
		d.p.Fill(ui.Rect{X: r.X, Y: r.Y, W: r.W, H: 14}, ui.Hex("#E9E9EE"), 0)
		d.window(ui.Rect{X: r.X + 50, Y: r.Y + 36, W: 160, H: 110}, ui.Hex("#F7F7F9"), ui.Hex("#F7F7F9"))
		for i, col := range []string{"#FF5F57", "#FEBC2E", "#28C840"} {
			d.p.FillPath((&ui.Path{}).Circle(r.X+62+float32(i)*10, r.Y+46, 3), ui.Hex(col))
		}
		d.bar(r.X+62, r.Y+66, 110, artBar)
		d.bar(r.X+62, r.Y+80, 80, artBar)
		d.pointer(r.X+170, r.Y+170, artWhite)
	case 4:
		d.p.Fill(ui.Rect{X: r.X + 96, Y: r.Y + 8, W: 160, H: 40}, c.accent.Alpha(0.16), 12)
		d.bar(r.X+110, r.Y+20, 120, c.text.Alpha(0.5))
		d.bar(r.X+110, r.Y+32, 80, c.text.Alpha(0.5))
		d.bar(r.X+8, r.Y+64, 180, c.muted.Alpha(0.6))
		d.bar(r.X+8, r.Y+78, 130, c.muted.Alpha(0.6))
		card := ui.Rect{X: r.X + 8, Y: r.Y + 100, W: 248, H: 66}
		d.p.Fill(card, c.surface, 10)
		d.p.Stroke(card, c.edge, 10, 1)
		d.bar(card.X+12, card.Y+14, 120, c.muted.Alpha(0.6))
		d.p.Fill(ui.Rect{X: card.X + 140, Y: card.Y + 38, W: 46, H: 20}, c.card, 6)
		d.p.Stroke(ui.Rect{X: card.X + 140, Y: card.Y + 38, W: 46, H: 20}, c.edge, 6, 1)
		d.p.Fill(ui.Rect{X: card.X + 192, Y: card.Y + 38, W: 46, H: 20}, c.accent, 6)
		box := ui.Rect{X: r.X + 8, Y: r.Y + 186, W: 248, H: 32}
		d.p.Fill(box, c.card, 9)
		d.p.Stroke(box, c.edge, 9, 1)
	default:
		d.p.Fill(r, c.accent.Alpha(0.14), 6)
		d.window(ui.Rect{X: r.X + 16, Y: r.Y + 18, W: 130, H: 96}, c.card, c.surface)
		d.window(ui.Rect{X: r.X + 120, Y: r.Y + 90, W: 124, H: 130}, c.card, c.surface)
		dx, dy := d.roam(46, 24)
		d.pointer(r.X+70+dx, r.Y+50+dy, c.text)
	}
	d.text(160, 430, L.TourArtPanes(), sizeSmall, 400, c.muted, 1, 280)
}

// letInScene: another computer signing in here with the right password and
// getting what Access grants, or asking, and this computer's user choosing.
func (d artPainter) letInScene() {
	c, k := d.col, d.st.ask
	d.peer(160, 92, kindDesktop)
	d.text(160, 52, L.TourArtOther(), sizeSmall, 400, c.muted, 1, 0)
	if with := 1 - k; with > 0.01 {
		d.flow(160, 116, 160, 160, artInput.Alpha(with), true)
		shield := (&ui.Path{}).MoveTo(160, 164).LineTo(178, 171).LineTo(178, 184).CubeTo(178, 195, 171, 203, 160, 207).CubeTo(149, 203, 142, 195, 142, 184).LineTo(142, 171).Close()
		d.p.FillPath(shield, c.good.Alpha(0.16*with))
		d.p.StrokePath(shield, 1.6, c.good.Alpha(with))
		check := (&ui.Path{}).MoveTo(152, 185).LineTo(158, 191).LineTo(169, 179)
		d.p.StrokePath(check, 2.2, c.good.Alpha(with))
		for i, icon := range []*ui.SVG{rowEye, rowMouse, barClipboard, rowFolder, navAI} {
			x := float32(48 + 48*i)
			lit := i < 4
			face, ink := c.accent.Alpha(0.14*with), c.accent.Alpha(with)
			if !lit {
				face, ink = c.card.Alpha(with), c.muted.Alpha(0.5*with)
			}
			d.p.Fill(ui.Rect{X: x, Y: 226, W: 40, H: 40}, face, 10)
			d.p.Stroke(ui.Rect{X: x, Y: 226, W: 40, H: 40}, c.edge.Alpha(with), 10, 1)
			d.p.Icon(icon, ui.Rect{X: x + 11, Y: 237, W: 18, H: 18}, ink)
		}
		d.text(160, 278, L.TourArtGrants(), sizeSmall, 400, c.muted.Alpha(with), 1, 280)
		d.flow(160, 304, 160, 338, artInput.Alpha(with), true)
	}
	if k > 0.01 {
		d.flow(160, 116, 160, 150, artInput.Alpha(k), true)
		card := ui.Rect{X: 30, Y: 154, W: 260, H: 140}
		d.p.Fill(card, c.card.Alpha(k), 12)
		d.p.Stroke(card, c.accent.Alpha(k), 12, 1.5)
		d.p.FillPath((&ui.Path{}).Circle(card.X+26, card.Y+28, 13), c.surface.Alpha(k))
		d.bar(card.X+48, card.Y+20, 150, c.text.Alpha(0.6*k))
		d.bar(card.X+48, card.Y+32, 90, c.muted.Alpha(0.5*k))
		for i := range 4 {
			r := ui.Rect{X: card.X + 16 + float32(i)*60, Y: card.Y + 54, W: 52, H: 28}
			if i < 2 {
				d.p.Fill(r, c.accent.Alpha(0.14*k), 7)
				d.p.Stroke(r, c.accent.Alpha(k), 7, 1)
			} else {
				d.p.Stroke(r, c.edge.Alpha(k), 7, 1)
			}
		}
		deny := ui.Rect{X: card.X + 136, Y: card.Y + 100, W: 52, H: 26}
		d.p.Stroke(deny, c.edge.Alpha(k), 7, 1)
		d.text(deny.X+deny.W/2, deny.Y+5, L.ActionDeny(), sizeSmall, 400, c.text.Alpha(k), 1, 0)
		allow := ui.Rect{X: card.X + 196, Y: card.Y + 100, W: 52, H: 26}
		d.p.Fill(allow, c.accent.Alpha(k), 7)
		d.text(allow.X+allow.W/2, allow.Y+5, L.ActionAllow(), sizeSmall, 600, artWhite.Alpha(k), 1, 0)
		d.text(160, 310, L.TourArtYouChoose(), sizeSmall, 400, c.muted.Alpha(k), 1, 280)
	}
	d.thisComputer(400)
}

// aiScene: an AI task working on this computer's screen, paused when the
// mouse moves; the provider's key stays on this computer.
func (d artPainter) aiScene() {
	c := d.col
	d.peer(60, 96, navAI)
	d.text(60, 56, L.TourArtProvider(), sizeSmall, 400, c.muted, 1, 120)
	d.flow(106, 330, 66, 122, c.flow, true)
	screen := ui.Rect{X: 100, Y: 124, W: 200, H: 124}
	d.p.Fill(screen, c.card, artScreenR)
	d.p.Stroke(screen, c.edge, artScreenR, 1.5)
	d.p.Fill(ui.Rect{X: screen.X, Y: screen.Y, W: screen.W, H: 20}, c.surface, artScreenR)
	d.p.Fill(ui.Rect{X: screen.X + 14, Y: screen.Y + 32, W: 88, H: 58}, c.surface, 5)
	d.p.Fill(ui.Rect{X: screen.X + 112, Y: screen.Y + 32, W: 74, H: 80}, c.accent.Alpha(0.16), 5)
	ph := d.phase(artPause)
	paused := ph >= 0.5
	t := min(ph/0.25, 1)
	if paused {
		t = 1
	}
	px, py := screen.X+46+56*t, screen.Y+56+20*t
	d.pointer(px, py, c.accent)
	spark := (&ui.Path{}).MoveTo(px+20, py-6).LineTo(px+21.4, py-2.4).LineTo(px+25, py-1).LineTo(px+21.4, py+0.4).LineTo(px+20, py+4).LineTo(px+18.6, py+0.4).LineTo(px+15, py-1).LineTo(px+18.6, py-2.4).Close()
	d.p.FillPath(spark, c.accent)
	if paused {
		d.chip(200, 268, aiPause, L.TourArtPaused(), c.warn.Alpha(0.16), textToneOn(c.warn, c.text))
	} else {
		d.chip(200, 268, navAI, L.TourArtWorking(), c.accent.Alpha(0.14), c.accent)
	}
	d.thisComputer(392)
	d.chip(250, 342, rowLock, L.TourArtKeyHere(), c.card, c.text)
}

// textToneOn is a status colour for words, darker over a light text
// colour's page, lighter over a dark one (as textTone does for the theme).
func textToneOn(col, text ui.Color) ui.Color {
	if text.R > 128 { // a dark page
		return col.Mix(artWhite, 0.3)
	}
	return col.Mix(ui.RGB(0, 0, 0), 0.3)
}

// privacyScene: this Mac and the four permissions macOS asks for, each
// allowed or locked.
func (d artPainter) privacyScene() {
	c := d.col
	names := []string{L.TourArtScreen(), L.TourArtControl(), L.TourArtYourInput(), L.TourArtKeychain()}
	n := 0
	for i, name := range names {
		x, y := float32(90+140*(i%2)), float32(120+60*(i/2))
		face, ink, icon := c.card, c.muted, rowLock
		if d.st.granted[i] {
			face, ink, icon = c.good.Alpha(0.16), textToneOn(c.good, c.text), rowCheck
			n++
		}
		d.p.Fill(ui.Rect{X: x - 62, Y: y - 17, W: 124, H: 34}, face, 17)
		d.p.Stroke(ui.Rect{X: x - 62, Y: y - 17, W: 124, H: 34}, c.edge, 17, 1)
		d.p.Icon(icon, ui.Rect{X: x - 50, Y: y - 7, W: 14, H: 14}, ink)
		d.text(x-30, y-8, name, sizeSmall, 600, ink, 0, 90)
	}
	d.computer(160, 340+d.bob(), 0)
	d.text(160, 446, L.TourArtThisMac(), 13, 600, c.text, 1, 0)
	d.text(160, 466, L.TourArtAllowed(n), sizeSmall, 400, c.muted, 1, 0)
}

// awayScene: the three screens others may reach — the desktop, the
// administrator prompts and lock screen, the sign-in screen — each lit as
// far as the run mode looked at reaches.
func (d artPainter) awayScene() {
	c := d.col
	d.text(160, 34, L.TourArtReach(), sizeSmall, 600, c.muted, 1, 0)
	prompts := L.TourArtPrompts()
	if artPlatform == "darwin" {
		prompts = L.TourArtPromptsMac()
	}
	labels := []string{L.TourArtYourDesktop(), prompts, L.TourArtSignInScreen()}
	for i, label := range labels {
		r := ui.Rect{X: 50, Y: 60 + float32(i)*160, W: 220, H: 112}
		on := d.st.reach[i]
		a := 0.3 + 0.7*on
		switch i {
		case 0:
			d.p.Fill(r, c.accent.Alpha(0.14*a), artScreenR)
			d.window(ui.Rect{X: r.X + 14, Y: r.Y + 14, W: 110, H: 62}, c.card.Alpha(a), c.surface.Alpha(a))
			d.window(ui.Rect{X: r.X + 110, Y: r.Y + 42, W: 94, H: 58}, c.card.Alpha(a), c.surface.Alpha(a))
		case 1:
			d.p.Fill(r, artPrompt.Alpha(a), artScreenR)
			dlg := ui.Rect{X: r.X + 46, Y: r.Y + 18, W: 128, H: 76}
			d.p.Fill(dlg, artWhite.Alpha(a), 7)
			d.p.Icon(aiShield, ui.Rect{X: dlg.X + 10, Y: dlg.Y + 10, W: 16, H: 16}, c.accent.Alpha(a))
			d.bar(dlg.X+34, dlg.Y+12, 80, artBar.Alpha(a))
			d.bar(dlg.X+34, dlg.Y+24, 56, artBar.Alpha(a))
			d.p.Fill(ui.Rect{X: dlg.X + 48, Y: dlg.Y + 54, W: 34, H: 14}, c.accent.Alpha(a), 4)
			d.p.Fill(ui.Rect{X: dlg.X + 86, Y: dlg.Y + 54, W: 34, H: 14}, artBar.Alpha(a), 4)
		default:
			d.p.Fill(r, artSignIn.Alpha(a), artScreenR)
			d.p.FillPath((&ui.Path{}).Circle(r.X+110, r.Y+38, 14), artWhite.Alpha(0.75*a))
			d.bar(r.X+80, r.Y+60, 60, artWhite.Alpha(0.7*a))
			field := ui.Rect{X: r.X + 66, Y: r.Y + 76, W: 88, H: 18}
			d.p.Fill(field, artWhite.Alpha(0.18*a), 9)
			d.p.Stroke(field, artWhite.Alpha(0.4*a), 9, 1)
		}
		d.p.Stroke(r, c.edge, artScreenR, 1)
		if on > 0.5 {
			dx, dy := d.roam(30, 14)
			d.pointer(r.X+86+dx, r.Y+46+dy, artInput)
		}
		face, ink, icon := c.card, c.muted, rowLock
		if on > 0.5 {
			face, ink, icon = c.good.Alpha(0.2), textToneOn(c.good, c.text), rowCheck
		}
		d.p.FillPath((&ui.Path{}).Circle(r.X+r.W, r.Y, 13), c.card)
		d.p.FillPath((&ui.Path{}).Circle(r.X+r.W, r.Y, 13), face)
		d.p.StrokePath((&ui.Path{}).Circle(r.X+r.W, r.Y, 13), 1.2, c.edge)
		d.p.Icon(icon, ui.Rect{X: r.X + r.W - 7, Y: r.Y - 7, W: 14, H: 14}, ink)
		d.text(160, r.Y+r.H+10, label, sizeSmall, 600, c.text.Alpha(0.4+0.6*on), 1, 280)
	}
	d.p.Line(84, 562, 102, 562, 3, artInput)
	d.text(110, 554, L.TourArtLetIn(), sizeSmall, 400, c.muted, 0, 0)
}

// awakeScene: this computer awake and reachable, then asleep and offline,
// in turn; on Windows, the display it needs to show a picture.
func (d artPainter) awakeScene() {
	c := d.col
	awake := float32(1)
	if !d.st.still {
		switch ph := d.phase(artSleep); {
		case ph < 0.42:
		case ph < 0.5:
			awake = 1 - (ph-0.42)/0.08
		case ph < 0.92:
			awake = 0
		default:
			awake = (ph - 0.92) / 0.08
		}
	}
	asleep := 1 - awake
	d.text(160, 52, L.TourArtReachIt(), sizeSmall, 400, c.muted, 1, 0)
	d.flow(76, 110, 150, 300, c.flow.Alpha(awake), true)
	d.flow(244, 110, 170, 300, c.flow.Alpha(awake), true)
	if asleep > 0.01 {
		d.flow(76, 110, 150, 300, c.muted.Alpha(0.45*asleep), false)
		d.flow(244, 110, 170, 300, c.muted.Alpha(0.45*asleep), false)
	}
	d.peer(64, 92, kindDesktop)
	d.peer(256, 92, kindDesktop)
	d.p.Icon(shellSun, ui.Rect{X: 226, Y: 196, W: 24, H: 24}, artSunInk.Alpha(awake))
	d.p.Icon(shellMoon, ui.Rect{X: 226, Y: 196, W: 24, H: 24}, c.muted.Alpha(asleep))
	if asleep > 0.01 {
		z := d.phase(2400 * time.Millisecond)
		d.text(254, 184-8*z, "z", 13, 700, c.muted.Alpha(asleep*(1-z)), 0, 0)
		d.text(266, 170-8*z, "z", 16, 700, c.muted.Alpha(asleep*z), 0, 0)
	}
	d.computer(160, 340+d.bob(), asleep)
	d.text(160, 450, L.TourArtAwake(), 13, 600, textToneOn(c.good, c.text).Alpha(awake), 1, 280)
	d.text(160, 450, L.TourArtAsleep(), 13, 600, c.muted.Alpha(asleep), 1, 280)
	if artPlatform != "darwin" {
		d.chip(160, 520, kindDesktop, L.TourArtDisplay(), c.card, c.text)
	}
}

// readyScene: this computer among its network's devices, a check on it,
// and the run mode chosen.
func (d artPainter) readyScene() {
	c := d.col
	d.flow(160, 300, 60, 172, c.flow, true)
	d.flow(160, 122, 160, 290, c.flow, true)
	d.flow(160, 300, 260, 172, c.flow, true)
	d.peer(60, 150, kindDesktop)
	d.peer(160, 100, kindDesktop)
	d.peer(260, 150, rowServer)
	d.computer(160, 330+d.bob(), 0)
	if d.st.pop > 0 {
		k := d.st.pop
		d.p.Transform(ui.Scaling(k, k), 222, 300, func() {
			badge := (&ui.Path{}).Circle(222, 300, 17)
			d.p.FillPath(badge, c.good)
			d.p.StrokePath(badge, 3, c.card)
			check := (&ui.Path{}).MoveTo(215, 300).LineTo(220, 305).LineTo(229, 295)
			d.p.StrokePath(check, 2.6, artWhite)
		})
	}
	d.text(160, 446, modeTitle(d.st.mode), 13, 600, c.text, 1, 0)
	when := L.TourArtSignedIn()
	if d.st.mode == modeBackground {
		when = L.TourArtFromStart()
	}
	d.text(160, 466, when, sizeSmall, 400, c.muted, 1, 280)
}

// boxModel is a rounded box seen from the front left, above, without
// perspective: a computer. Sizes are in the model's units; the front is at
// -z, the top at y = 0.
type boxModel struct {
	w, d, h, r float64
	zoom       float64
	top        [2]ui.Color // the top, from its back to its front
	side       ui.Color    // the sides, before the light falls on them
	edge       ui.Color    // the top's rim
	logo       string      // the system's mark on the top (assets/logos)
	logoColor  ui.Color
	logoSize   float32
}

// The view of every model: a point (x, y, z) shows at
// (viewX·(x, z), viewY·(x, z) − y), the right side rising and the front
// coming down toward the viewer.
var (
	viewX = [2]float64{0.906, -0.423}
	viewY = [2]float64{-0.211, -0.453}
)

// computerModel is the computer the illustrations show for a system: a Mac
// mini on macOS, a Windows Dev Kit elsewhere. The system's mark lies square
// on its top, its top edge toward the back, as on the real thing.
func computerModel(goos string) projectedBox {
	m := boxModel{w: 150, d: 110, h: 12, r: 10, zoom: 1.25,
		top: [2]ui.Color{ui.Hex("#6E6E72"), ui.Hex("#3A3A3D")}, side: ui.RGB(58, 58, 61), edge: ui.RGBA(255, 255, 255, 0.18),
		logo: "windows", logoColor: ui.RGBA(18, 18, 20, 0.5), logoSize: 20}
	led := [2]float64{55, -6} // the light, on the front face: x and height
	if goos == "darwin" {
		m = boxModel{w: 110, d: 110, h: 34, r: 20, zoom: 1.25,
			top: [2]ui.Color{ui.Hex("#FBFBFC"), ui.Hex("#D2D2D6")}, side: ui.RGB(230, 230, 234), edge: ui.RGBA(0, 0, 0, 0.1),
			logo: "apple", logoColor: ui.Hex("#1D1D1F"), logoSize: 30}
		led = [2]float64{38, -19}
	}
	pb := m.project()
	pb.led = pb.at(led[0], led[1], -m.d/2-0.3)
	return pb
}

// projectedBox is a boxModel as seen, in the illustration's units around
// the top's centre.
type projectedBox struct {
	m      boxModel
	top    [][2]float32
	bottom [][2]float32
	quads  [][4][2]float32
	stops  []ui.GradientStop
	led    [2]float32
	logoT  ui.Transform
	at     func(x, y, z float64) [2]float32
}

// project sees m: the top's outline, the sides that face the viewer as
// quads with their shading as a gradient from left to right, and the
// transform that lays the logo on the top.
func (m boxModel) project() projectedBox {
	at := func(x, y, z float64) [2]float32 {
		return [2]float32{float32((viewX[0]*x + viewX[1]*z) * m.zoom), float32((viewY[0]*x + viewY[1]*z - y) * m.zoom)}
	}
	// The outline, around four quarter circles.
	const n = 6
	hw, hd, r := m.w/2, m.d/2, m.r
	var ring [][2]float64
	for _, k := range [][3]float64{{hw - r, hd - r, 0}, {-(hw - r), hd - r, 90}, {-(hw - r), -(hd - r), 180}, {hw - r, -(hd - r), 270}} {
		for i := 0; i <= n; i++ {
			t := (k[2] + 90*float64(i)/n) * math.Pi / 180
			ring = append(ring, [2]float64{k[0] + r*math.Cos(t), k[1] + r*math.Sin(t)})
		}
	}
	pb := projectedBox{m: m, at: at}
	for _, q := range ring {
		pb.top = append(pb.top, at(q[0], 0, q[1]))
		pb.bottom = append(pb.bottom, at(q[0], -m.h, q[1]))
	}
	// The sides facing the viewer (their outward normal points down the
	// picture), lit from the front left.
	type lit struct {
		x float32
		c ui.Color
	}
	var shades []lit
	lx, lz := -0.35, -0.94
	minX, maxX := float32(math.Inf(1)), float32(math.Inf(-1))
	for i := range ring {
		p, q := ring[i], ring[(i+1)%len(ring)]
		dx, dz := q[0]-p[0], q[1]-p[1]
		l := math.Hypot(dx, dz)
		if l < 0.01 {
			continue
		}
		nx, nz := dz/l, -dx/l
		if nx*(p[0]+q[0])+nz*(p[1]+q[1]) < 0 {
			nx, nz = -nx, -nz
		}
		if viewY[0]*nx+viewY[1]*nz <= 0 {
			continue
		}
		quad := [4][2]float32{at(p[0], 0, p[1]), at(q[0], 0, q[1]), at(q[0], -m.h, q[1]), at(p[0], -m.h, p[1])}
		pb.quads = append(pb.quads, quad)
		mid := (quad[0][0] + quad[1][0] + quad[2][0] + quad[3][0]) / 4
		k := float32(0.62 + 0.42*max(0, nx*lx+nz*lz))
		shades = append(shades, lit{mid, scaleColor(m.side, k)})
		for _, v := range quad {
			minX, maxX = min(minX, v[0]), max(maxX, v[0])
		}
	}
	slices.SortFunc(shades, func(a, b lit) int { return cmp.Compare(a.x, b.x) })
	if span := maxX - minX; span > 0 {
		for _, s := range shades {
			pb.stops = append(pb.stops, ui.GradientStop{Color: s.c, At: (s.x - minX) / span})
		}
	}
	// The logo: the top's plane around its centre, as an affine transform
	// (scale, then slant, then turn); its top edge toward the back.
	o, ux, dv := at(0, 0, 0), at(10, 0, 0), at(0, 0, -10)
	sx, sy, skew, turn := affineParts(float64(ux[0]-o[0])/10, float64(ux[1]-o[1])/10, float64(dv[0]-o[0])/10, float64(dv[1]-o[1])/10)
	pb.logoT = ui.Scaling(float32(sx), float32(sy)).Then(ui.Skewing(float32(skew), 0)).Then(ui.Rotation(float32(turn)))
	return pb
}

// affineParts splits the linear map x' = a·x + c·y, y' = b·x + d·y into a
// scale (sx, sy), then a horizontal slant of skew degrees, then a clockwise
// turn of turn degrees, the transforms ui offers.
// Notes: the map must keep orientation (a·d − b·c > 0), as a top seen from
// above does.
func affineParts(a, b, c, d float64) (sx, sy, skew, turn float64) {
	sx, theta := math.Hypot(a, b), math.Atan2(b, a)
	slant := math.Cos(theta)*c + math.Sin(theta)*d
	sy = -math.Sin(theta)*c + math.Cos(theta)*d
	return sx, sy, math.Atan(slant/sy) * 180 / math.Pi, theta * 180 / math.Pi
}

// scaleColor darkens or lightens c by k.
func scaleColor(c ui.Color, k float32) ui.Color {
	f := func(v uint8) uint8 { return uint8(min(255, max(0, float32(v)*k+0.5))) }
	return ui.Color{R: f(c.R), G: f(c.G), B: f(c.B), A: c.A}
}

// poly is a closed path through pts, moved by (x, y).
func poly(pts [][2]float32, x, y float32) *ui.Path {
	var p ui.Path
	for i, v := range pts {
		if i == 0 {
			p.MoveTo(x+v[0], y+v[1])
		} else {
			p.LineTo(x+v[0], y+v[1])
		}
	}
	return p.Close()
}

// draw paints the box with its top's centre at (x, y): its shadow, its
// sides, its top, then the logo on it.
func (pb projectedBox) draw(p *ui.Painter, x, y float32) {
	m := pb.m
	x0, x1, y1 := float32(math.Inf(1)), float32(math.Inf(-1)), float32(math.Inf(-1))
	for _, v := range pb.bottom {
		x0, x1, y1 = min(x0, v[0]), max(x1, v[0]), max(y1, v[1])
	}
	// A soft oval on the ground, two of them for a blur.
	cx, cy, rx := x+(x0+x1)/2, y+y1+3, (x1-x0)/2
	p.Transform(ui.Scaling(1, 0.13), cx, cy, func() {
		p.FillPath((&ui.Path{}).Circle(cx, cy, rx*1.02), ui.RGBA(0, 0, 0, 0.07))
		p.FillPath((&ui.Path{}).Circle(cx, cy, rx*0.82), ui.RGBA(0, 0, 0, 0.08))
	})
	var sides ui.Path
	for _, q := range pb.quads {
		sides.MoveTo(x+q[0][0], y+q[0][1]).LineTo(x+q[1][0], y+q[1][1]).LineTo(x+q[2][0], y+q[2][1]).LineTo(x+q[3][0], y+q[3][1]).Close()
	}
	if len(pb.stops) > 0 {
		g := ui.LinearGradient{From: pb.stops[0].Color, To: pb.stops[len(pb.stops)-1].Color, Angle: 90, Stops: pb.stops}
		p.FillPathGradient(&sides, g)
		p.StrokePathGradient(&sides, 0.6, g)
	}
	top := poly(pb.top, x, y)
	p.FillPathGradient(top, ui.LinearGradient{From: m.top[0], To: m.top[1], Angle: 180})
	p.StrokePath(top, 1, m.edge)
	if logo := loadLogo(m.logo); logo != nil {
		s := m.logoSize
		p.Transform(pb.logoT, x, y, func() {
			p.Icon(logo, ui.Rect{X: x - s/2, Y: y - s/2, W: s, H: s}, m.logoColor)
		})
	}
}
