// Command s2-viewer is the S2 spike: Rust renders 1920×1080 RGBA frames into
// slots the MyGo window shows with ui.Bitmap.Update, without copying, next
// to a 500-row list. It runs for -seconds, then prints the frame rate and
// the process CPU and quits. Input on the picture is logged to stdout.
//
//	go run . -lib ../../core/target/... (see RESULTS.md)
package main

import (
	"flag"
	"fmt"
	"image"
	"log"
	"os"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

const slots = 4

var (
	libPath = flag.String("lib", "", "path of the s2_frames library")
	width   = flag.Int("w", 1920, "frame width")
	height  = flag.Int("h", 1080, "frame height")
	fps     = flag.Int("fps", 60, "producer frame rate")
	seconds = flag.Int("seconds", 20, "measurement time")
	rows    = flag.Int("rows", 500, "rows in the side list")
)

type viewer struct {
	win    *mygo.Window
	bm     *ui.Bitmap
	imgs   [slots]*image.RGBA
	shown  int // slot on screen, -1 before the first frame
	frames atomic.Int64
	list   ui.ListState
	events []string
}

func main() {
	flag.Parse()
	if *libPath == "" {
		log.Fatal("-lib is required")
	}
	l, err := load(*libPath)
	if err != nil {
		log.Fatal(err)
	}
	if r := l.call(l.start, uintptr(*width), uintptr(*height), uintptr(*fps)); int32(r) != 0 {
		log.Fatal("s2_start failed")
	}
	v := &viewer{shown: -1}
	for i := range slots {
		p := l.call(l.pixels, uintptr(i))
		// The pixels are Rust memory that lives for the whole process.
		pix := unsafe.Slice(*(**byte)(unsafe.Pointer(&p)), *width**height*4)
		v.imgs[i] = &image.RGBA{Pix: pix, Stride: *width * 4, Rect: image.Rect(0, 0, *width, *height)}
	}
	v.bm = ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, *width, *height)))

	mygo.App.WhenReady(func() {
		v.win = mygo.NewWindow(mygo.WindowOptions{Title: "S2 viewer", Width: 1400, Height: 820, Content: ui.View(v.view)})
		go v.pump(l)
		go v.measure(l)
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

// pump hands every new frame to the bitmap on the main thread. A slot is
// given back only after the next Update replaced it on screen.
func (v *viewer) pump(l *lib) {
	tick := time.NewTicker(time.Second / time.Duration(*fps*2))
	for range tick.C {
		slot := int(int32(l.call(l.acquire)))
		if slot < 0 {
			continue
		}
		done := make(chan struct{})
		v.win.Update(func() {
			v.bm.Update(v.imgs[slot])
			if v.shown >= 0 {
				l.call(l.release, uintptr(v.shown))
			}
			v.shown = slot
			v.frames.Add(1)
			close(done)
		})
		<-done
	}
}

// measure prints frames shown per second and the process CPU (percent of
// one core) once a second, then a summary, and quits.
func (v *viewer) measure(l *lib) {
	time.Sleep(2 * time.Second) // warm-up
	f0, c0, p0, d0, t0 := v.frames.Load(), cpuTime(), l.call(l.produced), l.call(l.drawNs), time.Now()
	last, lastCPU := f0, c0
	for i := 0; i < *seconds; i++ {
		time.Sleep(time.Second)
		f, c := v.frames.Load(), cpuTime()
		fmt.Printf("t=%2ds shown=%3d fps cpu=%5.1f%%\n", i+1, f-last, 100*(c-lastCPU).Seconds())
		last, lastCPU = f, c
	}
	el := time.Since(t0).Seconds()
	draw := time.Duration(l.call(l.drawNs) - d0).Seconds()
	fmt.Printf("SUMMARY %dx%d: shown %.1f fps, produced %.1f fps, CPU %.1f%% of one core (test-pattern drawing %.1f%%) over %.0fs\n",
		*width, *height, float64(v.frames.Load()-f0)/el, float64(l.call(l.produced)-p0)/el,
		100*(cpuTime()-c0).Seconds()/el, 100*draw/el, el)
	os.Stdout.Sync()
	mygo.App.Quit()
}

func (v *viewer) view(c *ui.Context) {
	ui.Row(c).Fill().Children(func() {
		ui.Image(c, v.bm).Fit(ui.Contain).Grow(1).Focusable().HandleInput(func(ev ui.InputEvent) bool {
			fmt.Printf("input kind=%v key=%v physical=%v mods=%v button=%d clicks=%d x=%.0f y=%.0f dx=%.1f dy=%.1f text=%q\n",
				ev.Kind, ev.Key, ev.PhysicalKey, ev.Mods, ev.Button, ev.Clicks, ev.X, ev.Y, ev.DX, ev.DY, ev.Text)
			return true
		})
		ui.List(c, &v.list, *rows, func(i int) {
			ui.Text(c, fmt.Sprintf("Message %d — a line of an AI conversation", i)).Padding(6, 10)
		}).Width(360)
	})
}
