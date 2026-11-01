package gui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/YuanZhe-99/Kyokai/internal/localapi"
)

// The decoder box-filters a picture shown at half its size or less: each
// pixel the average of the f×f it covers, fewer at the edges, as MyGo's own
// halving does; reusing its row buffer allocates nothing per frame
// (regression: MyGo filtered every frame of a 4K stream on the main thread).
func TestShrinkInto(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 5, 3))
	for y := range 3 {
		for x := range 5 {
			src.SetRGBA(x, y, color.RGBA{uint8(10 * (x + 5*y)), uint8(x), uint8(y), 255})
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, shrunk(5, 2), shrunk(3, 2)))
	if dst.Rect.Dx() != 3 || dst.Rect.Dy() != 2 {
		t.Fatalf("shrunk size %v", dst.Rect)
	}
	sums := shrinkInto(dst, src, 2, nil)
	avg := func(xs ...int) uint8 {
		s := 0
		for _, x := range xs {
			s += x
		}
		return uint8((s + len(xs)/2) / len(xs))
	}
	r := func(x, y int) int { return 10 * (x + 5*y) }
	for _, c := range []struct {
		x, y int
		want color.RGBA
	}{
		{0, 0, color.RGBA{avg(r(0, 0), r(1, 0), r(0, 1), r(1, 1)), avg(0, 1, 0, 1), avg(0, 0, 1, 1), 255}},
		{2, 0, color.RGBA{avg(r(4, 0), r(4, 1)), 4, avg(0, 1), 255}}, // the odd last column
		{1, 1, color.RGBA{avg(r(2, 2), r(3, 2)), avg(2, 3), 2, 255}}, // the odd last row
		{2, 1, color.RGBA{uint8(r(4, 2)), 4, 2, 255}},                // the corner, one pixel
		{1, 0, color.RGBA{avg(r(2, 0), r(3, 0), r(2, 1), r(3, 1)), avg(2, 3, 2, 3), avg(0, 0, 1, 1), 255}},
	} {
		if got := dst.RGBAAt(c.x, c.y); got != c.want {
			t.Errorf("pixel %d,%d is %v, want %v", c.x, c.y, got, c.want)
		}
	}
	// Four times smaller: one pixel per 4×4 of a 4K frame, no allocation.
	big := image.NewRGBA(image.Rect(0, 0, 3840, 2160))
	for i := range big.Pix {
		big.Pix[i] = uint8(i % 7 * 30)
	}
	small := image.NewRGBA(image.Rect(0, 0, shrunk(3840, 4), shrunk(2160, 4)))
	sums = shrinkInto(small, big, 4, sums)
	// The next frame sums in the same buffer (the race detector's
	// allocations make AllocsPerRun no measure here).
	buf := &sums[0]
	if sums = shrinkInto(small, big, 4, sums); &sums[0] != buf {
		t.Error("the row sums were made again for the next frame")
	}
}

// How much smaller the picture is made follows its size on screen, in
// pixels: the largest power of two it shows that many times smaller, none
// above half its size.
func TestShrinkFor(t *testing.T) {
	for _, c := range []struct {
		name   string
		box    ui.Rect
		scale  float32
		pw, ph int
		want   int
	}{
		{"1440p at half size", ui.Rect{W: 1280, H: 720}, 1, 2560, 1440, 2},
		{"1440p at half size on a 2× screen", ui.Rect{W: 1280, H: 720}, 2, 2560, 1440, 1},
		{"4K in a small pane", ui.Rect{W: 900, H: 600}, 1, 3840, 2160, 4},
		{"5K in a small pane", ui.Rect{W: 640, H: 400}, 1, 5120, 2880, 8},
		{"a little smaller", ui.Rect{W: 1500, H: 900}, 1, 2560, 1440, 1},
		{"larger", ui.Rect{W: 3000, H: 2000}, 1, 1920, 1080, 1},
		{"no box yet", ui.Rect{}, 1, 1920, 1080, 1},
		{"tiny", ui.Rect{W: 10, H: 10}, 1, 7680, 4320, 16},
	} {
		if got := shrinkFor(c.box, c.scale, c.pw, c.ph); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
}

// A picture made smaller shows where the full one did, and the pointer
// maps to the same place on the host (stream coordinates, not the
// bitmap's); the view tells the decoder how much smaller to make it, and
// Original size asks for every pixel again.
func TestShrunkPicturePointer(t *testing.T) {
	full := image.NewRGBA(image.Rect(0, 0, 2560, 1440))
	half := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	at := func(pic *image.RGBA) (ui.Rect, inputEvent, int32) {
		v := &viewer{device: "657365333", preset: 1, pumping: true, control: true, app: &App{},
			out: make(chan []byte, 8), moved: make(chan struct{}, 1)}
		v.start, v.bm = videoStart{Codec: "h264", Width: 2560, Height: 1440}, ui.NewBitmap(pic)
		tt := ui.NewTester(v.view, 1100, 560)
		tt.Frame()
		tt.Frame()
		v.mu.Lock()
		box := v.box
		v.mu.Unlock()
		tt.Move(box.X+box.W/2+100, box.Y+box.H/2-50)
		v.mu.Lock()
		defer v.mu.Unlock()
		if v.move == nil {
			t.Fatal("no pointer move")
		}
		return box, *v.move, v.fit.Load()
	}
	box1, e1, fit := at(full)
	box2, e2, _ := at(half)
	if box1 != box2 || e1.X != e2.X || e1.Y != e2.Y {
		t.Errorf("full size: %v at %v,%v; made smaller: %v at %v,%v", box1, e1.X, e1.Y, box2, e2.X, e2.Y)
	}
	if fit != 2 {
		t.Errorf("a 1440p picture in a 1100×560 pane is made %d times smaller, want 2", fit)
	}
	v := &viewer{out: make(chan []byte, 1)}
	v.fit.Store(2)
	v.setFit(1)
	select {
	case p := <-v.out:
		if !bytes.Contains(p, []byte(`"key-frame"`)) {
			t.Errorf("asked %q", p)
		}
	default:
		t.Error("a picture shown larger again did not ask for a key frame")
	}
	v.setFit(1)
	if len(v.out) != 0 {
		t.Error("asked again for nothing")
	}
}

// A tab not built lately and not shown is not redrawn for its pictures
// (regression: every frame of a hidden desktop rebuilt its whole window);
// its next build shows the newest one.
func TestHiddenTabPictures(t *testing.T) {
	a := demoApp(t)
	vw := &viewerWindow{app: a}
	v, other := &viewer{app: a, device: "657365333"}, &viewer{app: a, device: "402118905"}
	v.home.Store(vw)
	vw.mu.Lock()
	vw.tabs = []tab{v, other}
	g := &group{}
	g.split.show(other)
	vw.groups, vw.cur = []*group{g}, g
	vw.mu.Unlock()
	now := time.Now()
	if v.seen(now) {
		t.Error("a hidden tab never built is redrawn")
	}
	v.builtAt.Store(now.Add(-time.Second).UnixNano())
	if v.seen(now) {
		t.Error("a hidden tab built a second ago is redrawn")
	}
	v.builtAt.Store(now.Add(-100 * time.Millisecond).UnixNano())
	if !v.seen(now) {
		t.Error("a tab built just now is not redrawn")
	}
	pic := image.NewRGBA(image.Rect(0, 0, 4, 2))
	v.imgs, v.shown, v.ready = [3]*image.RGBA{pic}, -1, 0
	v.present()
	if v.bm == nil || v.ready != -1 || v.shown != 0 {
		t.Errorf("the next build shows bitmap %v, ready %d, shown %d", v.bm, v.ready, v.shown)
	}
}

// Preferences are written off the main thread by one writer: changes made
// while it writes become one write after it, the last change is the one
// saved, and quitting writes what is still due (regression: every setting
// synced gui.json to disk in the click handler).
func TestPrefsWriter(t *testing.T) {
	a := demoApp(t)
	var mu sync.Mutex
	var written []string
	block, started := make(chan struct{}), make(chan struct{}, 1)
	saver := prefsSaver
	t.Cleanup(func() { prefsSaver = saver })
	prefsSaver = func(dir string, p Prefs) error {
		select {
		case started <- struct{}{}:
			<-block
		default:
		}
		mu.Lock()
		written = append(written, p.Theme)
		mu.Unlock()
		return saver(dir, p)
	}
	set := func(theme string) {
		a.mu.Lock()
		p := a.prefs
		a.mu.Unlock()
		p.Theme = theme
		_ = a.setPrefs(p)
	}
	set("t0")
	<-started // the writer is writing t0
	for i := 1; i <= 20; i++ {
		set("t" + string(rune('a'+i)))
	}
	started <- struct{}{} // later writes do not wait
	close(block)
	if err := a.writePrefs(); err != nil { // as quitting does
		t.Fatal(err)
	}
	mu.Lock()
	got := append([]string(nil), written...)
	mu.Unlock()
	last := "t" + string(rune('a'+20))
	if len(got) < 2 || len(got) > 3 || got[0] != "t0" || got[len(got)-1] != last {
		t.Errorf("writes %q, want t0 then %s (at most once more)", got, last)
	}
	if p := loadPrefs(a.stateDir); p.Theme != last {
		t.Errorf("gui.json has %q, want %q", p.Theme, last)
	}
	// A caller that needs the outcome writes at once.
	p := loadPrefs(a.stateDir)
	p.Theme = "now"
	if err := a.setPrefsNow(p); err != nil || loadPrefs(a.stateDir).Theme != "now" {
		t.Errorf("setPrefsNow: %v, gui.json %q", err, loadPrefs(a.stateDir).Theme)
	}
}

// mineEngine notes the requests it gets, in order.
type mineEngine struct {
	okEngine
	mu    *sync.Mutex
	order *[]string
}

func (e mineEngine) Handle(ctx context.Context, req localapi.Request) localapi.Response {
	e.mu.Lock()
	*e.order = append(*e.order, string(req.Type)+" "+req.Digest)
	e.mu.Unlock()
	return e.okEngine.Handle(ctx, req)
}

// Text Kyokai copies itself is announced to the engine by its SHA-256
// (clipboard.mine) before it reaches the clipboard, so that the engine
// does not offer it to this device's controllers (regression: the
// temporary password was).
func TestCopyTextMarksMine(t *testing.T) {
	dir, err := os.MkdirTemp("", "ky")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "e.sock")
	ln, err := localapi.ListenWith(localapi.ListenOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var order []string
	go localapi.ServeWith(ctx, ln, mineEngine{mu: &mu, order: &order}, localapi.Authorizer{OwnerUID: uint32(os.Getuid()), OwnerSID: localapi.CurrentSID()})
	a := demoApp(t)
	a.socket = path
	done := make(chan struct{})
	write := clipboardWrite
	t.Cleanup(func() { clipboardWrite = write })
	clipboardWrite = func(text string) {
		mu.Lock()
		order = append(order, "write "+text)
		mu.Unlock()
		close(done)
	}
	a.copyText("k7mq2xpa")
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("nothing written")
	}
	sum := sha256.Sum256([]byte("k7mq2xpa"))
	mu.Lock()
	defer mu.Unlock()
	want := []string{"clipboard.mine " + hex.EncodeToString(sum[:]), "write k7mq2xpa"}
	if len(order) != 2 || order[0] != want[0] || order[1] != want[1] {
		t.Errorf("%q, want %q", order, want)
	}
}
