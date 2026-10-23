// Command shell is the M0 spike of Kyokai 0.6.0's interface (PLAN.md, "Next
// version: 0.6.0, the interface"): a material behind a transparent page, a
// sidebar drawn on it, floating panes with headers, the window's caption
// buttons at the end of the top-right pane's header, a live picture clipped
// to a rounded pane, a modal sheet and a popover. It answers whether MyGo
// gives all of that as the design draws it, and what the picture costs.
//
//	go run ./spikes/shell                 the system's appearance
//	go run ./spikes/shell -theme dark     light or dark
//	go run ./spikes/shell -material none  no material (an opaque base)
package main

import (
	"flag"
	"fmt"
	"image"
	"log"
	"runtime"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

const (
	sidebarW = 248 // the sidebar's width
	margin   = 8   // between the panes and the window's edges
	headerH  = 44  // a pane's header
	radius   = 14  // a pane's corners
)

// shell is what the window shows, and the picture's frame counts.
type shell struct {
	win      *mygo.Window
	bm       *ui.Bitmap
	settings bool // the sheet is open
	picker   bool // the + popover is open

	mu      sync.Mutex
	pushed  int       // pictures updated in the last second
	drawn   int       // views built in the last second
	since   time.Time // when the counts started
	rate    string    // the last second's counts, shown in the header
	slowest time.Duration
}

func main() {
	theme := flag.String("theme", "system", "system, light or dark")
	material := flag.String("material", "", "the window's material: mica, sidebar, none (default: the platform's)")
	barH := flag.Int("bar", margin+headerH, "the title bar's height, which the caption buttons fill")
	flag.Parse()
	m := mygo.VibrancyMica
	if runtime.GOOS == "darwin" {
		m = mygo.VibrancySidebar
	}
	switch *material {
	case "mica":
		m = mygo.VibrancyMica
	case "sidebar":
		m = mygo.VibrancySidebar
	case "none":
		m = mygo.VibrancyNone
	}
	s := &shell{since: time.Now()}
	mygo.App.WhenReady(func() {
		switch *theme {
		case "light":
			mygo.Theme.SetSource(mygo.ThemeLight)
		case "dark":
			mygo.Theme.SetSource(mygo.ThemeDark)
		}
		s.win = mygo.NewWindow(mygo.WindowOptions{
			Title: "Kyokai shell spike", Width: 1280, Height: 800, MinWidth: 720, MinHeight: 480,
			TitleBarStyle: mygo.TitleBarHidden,
			// The caption buttons are as tall as the title bar: the panes'
			// margin and a header.
			TitleBarHeight:       *barH,
			TrafficLightPosition: &mygo.Point{X: 18, Y: 20},
			Vibrancy:             m,
			Content:              ui.View(s.view),
		})
		go s.feed()
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

// feed updates the picture 60 times a second, as a viewer does, so the frame
// counts in the header say what a live picture in a rounded pane costs.
func (s *shell) feed() {
	const w, h = 1728, 1080
	frames := [2]*image.RGBA{image.NewRGBA(image.Rect(0, 0, w, h)), image.NewRGBA(image.Rect(0, 0, w, h))}
	tick := time.NewTicker(time.Second / 60)
	for n := 0; ; n++ {
		<-tick.C
		img := frames[n%2]
		paint(img, n)
		s.win.Update(func() {
			if s.bm == nil {
				s.bm = ui.NewBitmap(img)
			} else {
				s.bm.Update(img)
			}
			s.mu.Lock()
			s.pushed++
			s.mu.Unlock()
		})
	}
}

// paint draws moving bars, as busy as a screen with a video on it.
func paint(img *image.RGBA, n int) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		row := img.Pix[y*img.Stride:]
		for x := 0; x < b.Dx(); x++ {
			v := uint8((x + n*6) % 256)
			o := x * 4
			row[o], row[o+1], row[o+2], row[o+3] = v/3+40, uint8(y*255/b.Dy())/2+60, 160-v/3, 255
		}
	}
}

// count notes a view and, once a second, the counts for the header.
func (s *shell) count(took time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.drawn++
	s.slowest = max(s.slowest, took)
	if time.Since(s.since) >= time.Second {
		s.rate = fmt.Sprintf("%d pictures · %d views · slowest view %s", s.pushed, s.drawn, s.slowest.Round(100*time.Microsecond))
		s.pushed, s.drawn, s.slowest, s.since = 0, 0, 0, time.Now()
	}
}

func (s *shell) view(c *ui.Context) {
	start := time.Now()
	defer func() { s.count(time.Since(start)) }()
	t := c.Theme()
	vibrant := c.Vibrancy()
	if vibrant {
		c.Root().Background(ui.Transparent)
	} else {
		c.Root().Background(t.Surface)
	}
	ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
		s.sidebar(c)
		ui.Column(c).Grow(1).MinWidth(0).Padding(margin, margin, margin, 0).Gap(margin).Children(func() {
			ui.Row(c).Grow(3).MinHeight(0).Gap(margin).AlignItems(ui.Stretch).Children(func() {
				s.pane(c, "picture", "Studio Mac", "657 365 333", 3, false, func() {
					img := ui.Box(c).Grow(1).FillWidth().MinHeight(0).Background(ui.RGB(13, 14, 17))
					img.Children(func() {
						if s.bm != nil {
							ui.Image(c.Key("live"), s.bm).Fit(ui.Contain).Grow(1).FillWidth()
						}
					})
				})
				s.pane(c, "ssh", "Studio Mac", "SSH · yuan", 2, true, func() {
					ui.Column(c).Padding(12, 16).Gap(4).Children(func() {
						for _, l := range []string{"Last login: Thu Oct 23 09:41 on ttys002", "yuan@studio-mac ~ % uptime", "10:42 up 3 days, 2 users, load 1.92 2.10 2.31", "yuan@studio-mac ~ %"} {
							ui.Text(c, l).Font("monospace").FontSize(12)
						}
					})
				})
			})
			s.pane(c, "ai", "NUC", "AI task", 2, false, func() {
				ui.Column(c).Padding(12, 20).Gap(8).Children(func() {
					s.mu.Lock()
					rate := s.rate
					s.mu.Unlock()
					ui.Text(c, "Frames: "+rate).FontSize(13)
					bar := c.TitleBar()
					ui.Text(c, fmt.Sprintf("Material shows: %v · title bar %.0f tall, controls %.0f left, %.0f right", c.Vibrancy(), bar.Height, bar.Left, bar.Right)).FontSize(13)
					ui.Text(c, "The panes float on the window's material; the caption buttons end the top-right pane's header.").FontSize(13).TextColor(t.TextMuted)
				})
			})
		})
	})
	if s.settings {
		ui.Modal(c, &s.settings, func() {
			ui.Column(c).Width(1000).Height(640).Padding(20).Gap(12).Children(func() {
				ui.Text(c, "Settings").FontSize(16).Bold()
				ui.Text(c, "The top layer: Esc or a click outside closes it.").TextColor(t.TextMuted)
			})
		})
	}
}

// sidebar is drawn on the material: a toggle and +, the entries, and the
// bottom row with search and the gear.
func (s *shell) sidebar(c *ui.Context) {
	t := c.Theme()
	bar := c.TitleBar()
	ui.Column(c).Width(sidebarW).Shrink(0).Padding(10, 10, 10, 12).Gap(2).Children(func() {
		ui.Row(c).Height(34).Shrink(0).Gap(2).AlignItems(ui.Center).Padding(0, 0, 0, bar.Left).DragWindow().Children(func() {
			ui.Text(c, "▤").FontSize(16).TextColor(t.TextMuted).Padding(0, 6)
			plus := ui.ButtonBase(c.Key("plus")).Size(28, 28).Radius(7).Center().Label("New window")
			plus.Children(func() { ui.Text(c, "+").FontSize(18).TextColor(t.TextMuted) })
			if plus.Clicked() {
				s.picker = !s.picker
			}
			if s.picker {
				ui.Popover(c, plus, &s.picker, func() {
					ui.Column(c).Width(400).Padding(14).Gap(8).Children(func() {
						ui.Text(c, "Open").Bold()
						ui.Text(c, "Desktop · SSH · Remote Desktop · VNC · AI task").TextColor(t.TextMuted)
					})
				})
			}
		})
		for i, name := range []string{"Home", "Studio Mac + NUC", "   Studio Mac  Desktop", "   Studio Mac  SSH", "   NUC  AI task", "Office laptop  Remote Desktop", "Media box  VNC"} {
			row := ui.Row(c.Key(fmt.Sprint("sb", i))).Height(32).Padding(0, 10).Radius(8).AlignItems(ui.Center)
			if i == 2 {
				row.Background(t.Background).Shadow(0, 1, 4, 0, ui.RGBA(16, 24, 40, 20))
			}
			row.Children(func() { ui.Text(c, name).FontSize(13).SingleLine() })
		}
		ui.Box(c).Grow(1)
		ui.Row(c).Gap(2).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "⌕").Padding(0, 8).TextColor(t.TextMuted)
			gear := ui.ButtonBase(c.Key("gear")).Size(28, 28).Radius(7).Center().Label("Settings")
			gear.Children(func() { ui.Text(c, "⚙").TextColor(t.TextMuted) })
			if gear.Clicked() {
				s.settings = true
			}
		})
	})
}

// pane is a floating pane: rounded, shadowed, its header with the title, a
// second line and, in the top-right pane, room for the caption buttons.
func (s *shell) pane(c *ui.Context, key, title, sub string, grow float32, topRight bool, body func()) {
	t := c.Theme()
	bar := c.TitleBar()
	p := ui.Column(c.Key("pane-"+key)).Grow(grow).MinWidth(0).MinHeight(0).Radius(radius).Clip().
		Background(t.Background).Border(1, t.Border).Shadow(0, 6, 18, 0, ui.RGBA(16, 24, 40, 18))
	p.Children(func() {
		right := float32(8)
		if topRight {
			// The caption buttons sit over the window's corner: the pane
			// keeps their room less its own margin.
			right = max(bar.Right-margin, 8)
		}
		ui.Row(c).Height(headerH).Shrink(0).Padding(0, right, 0, 14).Gap(8).AlignItems(ui.Center).DragWindow().Children(func() {
			ui.Text(c, title).FontSize(13.5).Bold().SingleLine()
			ui.Text(c, sub).FontSize(12.5).TextColor(t.TextMuted).SingleLine()
			ui.Box(c).Grow(1)
			for _, g := range []string{"◫", "⊟", "⤢", "×"} {
				ui.Text(c, g).FontSize(14).TextColor(t.TextMuted).Padding(0, 6)
			}
		})
		body()
	})
}
