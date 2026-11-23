package gui

import (
	"math"
	"sync"
	"time"
)

// TouchpadSink receives pointer movements, button clicks, wheel scrolling and
// pinch gestures translated from raw touch points by Touchpad.
type TouchpadSink interface {
	Move(dx, dy float32)
	Button(button int, down bool)
	Scroll(dx, dy float32)
	Pinch(scale, focalX, focalY, panX, panY float32)
}

const (
	tpSlop       = 6.0
	tpTapTime    = 250 * time.Millisecond
	tpTapHold    = 180 * time.Millisecond
	tpDragGrace  = 320 * time.Millisecond
	tpPxPerNotch = 48.0
	tpFrame      = 16 * time.Millisecond
)

type tpMode int

const (
	tpModeNone tpMode = iota
	tpModePointer
	tpModeTwo
	tpModeScroll
	tpModePinch
)

type tpAxis int

const (
	tpAxisFree tpAxis = iota
	tpAxisVertical
	tpAxisHorizontal
)

type tpSpeedSample struct {
	at   time.Time
	dist float64
}

type tpScrollSample struct {
	at     time.Time
	dx, dy float64
}

// Touchpad translates 1-, 2- and 3-finger touch sequences into laptop-style
// relative pointer movement, taps, tap-and-drag, smooth two-finger scrolling
// with flick inertia, and optional two-finger pinch-zoom.
type Touchpad struct {
	mu    sync.Mutex
	sink  TouchpadSink
	pinch bool
	speed float64

	touches      map[int][2]float64
	most         int
	began        time.Time
	mode         tpMode
	released     bool
	heldX, heldY float64
	samples      []tpSpeedSample
	vel          float64
	velAt        time.Time

	tapTimer     *time.Timer
	dragTimer    *time.Timer
	inertiaStop  chan struct{}
	dragging     bool
	gestureMoved bool

	focalX, focalY float64
	span           float64
	spanStart      float64
	travelX        float64
	travelY        float64
	axis           tpAxis
	scrollSamples  []tpScrollSample
}

// NewTouchpad creates a Touchpad driving sink, with pinch zoom enabled when
// pinch is true and initial pointer speed multiplier speed (1.6 when <= 0).
func NewTouchpad(sink TouchpadSink, pinch bool, speed float32) *Touchpad {
	sp := float64(speed)
	if sp <= 0 {
		sp = 1.6
	}
	return &Touchpad{
		sink:    sink,
		pinch:   pinch,
		speed:   sp,
		touches: map[int][2]float64{},
	}
}

// SetSpeed updates the pointer speed multiplier.
func (tp *Touchpad) SetSpeed(speed float32) {
	tp.mu.Lock()
	if speed > 0 {
		tp.speed = float64(speed)
	}
	tp.mu.Unlock()
}

// SetSink updates the target TouchpadSink in place so geometry updates on
// subsequent frames reach the existing Touchpad instance.
func (tp *Touchpad) SetSink(sink TouchpadSink) {
	tp.mu.Lock()
	tp.sink = sink
	tp.mu.Unlock()
}

// Active reports whether any finger is currently touching or dragging.
func (tp *Touchpad) Active() bool {
	tp.mu.Lock()
	active := len(tp.touches) > 0 || tp.dragging || tp.tapTimer != nil || tp.dragTimer != nil
	tp.mu.Unlock()
	return active
}

// TouchpadGain returns the pointer gain for finger velocity v in DIPs/ms.
func TouchpadGain(v float64) float64 {
	switch {
	case v <= 0.05:
		return 0.7
	case v <= 0.35:
		return 0.7 + (v-0.05)/0.3*0.3
	case v <= 1.6:
		return 1.0 + (v-0.35)/1.25*2.2
	default:
		return 3.2
	}
}

// Down records finger id touching down at (x, y) at now.
func (tp *Touchpad) Down(id int, x, y float32, now time.Time) {
	tp.mu.Lock()
	tp.stopInertiaLocked()
	if len(tp.touches) == 0 {
		tp.most = 0
		tp.began = now
		tp.mode = tpModeNone
		tp.released = false
		tp.heldX, tp.heldY = 0, 0
		tp.samples = tp.samples[:0]
		tp.velAt = time.Time{}
		tp.gestureMoved = false
		if tp.tapTimer != nil || tp.dragTimer != nil {
			if tp.tapTimer != nil {
				tp.tapTimer.Stop()
				tp.tapTimer = nil
			}
			if tp.dragTimer != nil {
				tp.dragTimer.Stop()
				tp.dragTimer = nil
			}
			tp.dragging = true
		}
	}
	tp.touches[id] = [2]float64{float64(x), float64(y)}
	if len(tp.touches) > tp.most {
		tp.most = len(tp.touches)
	}
	if len(tp.touches) == 2 {
		tp.mode = tpModeTwo
		tp.focalX, tp.focalY = tp.centroidLocked()
		tp.span = tp.spreadLocked()
		tp.spanStart = tp.span
		tp.travelX, tp.travelY = 0, 0
		tp.axis = tpAxisFree
		tp.scrollSamples = tp.scrollSamples[:0]
	}
	tp.mu.Unlock()
}

// Move records finger id moving to (x, y) at now.
func (tp *Touchpad) Move(id int, x, y float32, now time.Time) {
	var actions []func()
	tp.mu.Lock()
	prev, ok := tp.touches[id]
	if !ok {
		tp.mu.Unlock()
		return
	}
	dx, dy := float64(x)-prev[0], float64(y)-prev[1]
	tp.touches[id] = [2]float64{float64(x), float64(y)}
	if len(tp.touches) >= 2 {
		actions = tp.twoLocked(now)
	} else if tp.most <= 1 {
		actions = tp.oneLocked(dx, dy, now)
	}
	tp.mu.Unlock()
	for _, fn := range actions {
		fn()
	}
}

// Up records finger id lifting at now.
func (tp *Touchpad) Up(id int, now time.Time) {
	var actions []func()
	tp.mu.Lock()
	if _, ok := tp.touches[id]; !ok {
		tp.mu.Unlock()
		return
	}
	delete(tp.touches, id)
	if len(tp.touches) == 1 && tp.mode == tpModeTwo {
		tp.focalX, tp.focalY = tp.centroidLocked()
	}
	if len(tp.touches) > 0 {
		tp.mu.Unlock()
		return
	}
	short := now.Sub(tp.began) <= tpTapTime
	tap := short && !tp.gestureMoved && tp.mode != tpModeScroll && tp.mode != tpModePinch
	if tp.mode == tpModeScroll {
		tp.startFlickLocked(now)
	}
	if tp.dragging {
		switch {
		case tap && tp.most == 1:
			tp.dragging = false
			actions = append(actions, func() {
				tp.sink.Button(0, false)
				tp.sink.Button(0, true)
				tp.sink.Button(0, false)
			})
		case tp.gestureMoved && tp.most == 1:
			tp.dragTimer = time.AfterFunc(tpDragGrace, func() {
				tp.mu.Lock()
				if tp.dragTimer == nil {
					tp.mu.Unlock()
					return
				}
				tp.dragTimer = nil
				tp.dragging = false
				tp.mu.Unlock()
				tp.sink.Button(0, false)
			})
		default:
			tp.dragging = false
			actions = append(actions, func() { tp.sink.Button(0, false) })
		}
		tp.mu.Unlock()
		for _, fn := range actions {
			fn()
		}
		return
	}
	if tap {
		switch tp.most {
		case 1:
			actions = append(actions, func() { tp.sink.Button(0, true) })
			tp.tapTimer = time.AfterFunc(tpTapHold, func() {
				tp.mu.Lock()
				if tp.tapTimer == nil {
					tp.mu.Unlock()
					return
				}
				tp.tapTimer = nil
				tp.mu.Unlock()
				tp.sink.Button(0, false)
			})
		case 2:
			actions = append(actions, func() {
				tp.sink.Button(1, true)
				tp.sink.Button(1, false)
			})
		case 3:
			actions = append(actions, func() {
				tp.sink.Button(2, true)
				tp.sink.Button(2, false)
			})
		}
	}
	tp.mu.Unlock()
	for _, fn := range actions {
		fn()
	}
}

// Cancel drops finger id without triggering tap clicks.
func (tp *Touchpad) Cancel(id int) {
	release := false
	tp.mu.Lock()
	delete(tp.touches, id)
	if len(tp.touches) == 0 && tp.dragging {
		tp.dragging = false
		release = true
	}
	tp.mu.Unlock()
	if release {
		tp.sink.Button(0, false)
	}
}

// Reset cancels timers and releases any held primary button.
func (tp *Touchpad) Reset() {
	release := false
	tp.mu.Lock()
	tp.stopInertiaLocked()
	if tp.tapTimer != nil {
		tp.tapTimer.Stop()
		tp.tapTimer = nil
		release = true
	}
	if tp.dragTimer != nil {
		tp.dragTimer.Stop()
		tp.dragTimer = nil
		release = true
	}
	if tp.dragging {
		tp.dragging = false
		release = true
	}
	clear(tp.touches)
	tp.mu.Unlock()
	if release {
		tp.sink.Button(0, false)
	}
}

func (tp *Touchpad) oneLocked(dx, dy float64, now time.Time) []func() {
	dist := math.Hypot(dx, dy)
	tp.samples = append(tp.samples, tpSpeedSample{at: now, dist: dist})
	cut := now.Add(-60 * time.Millisecond)
	keep := tp.samples[:0]
	for _, s := range tp.samples {
		if !s.at.Before(cut) {
			keep = append(keep, s)
		}
	}
	tp.samples = keep
	if !tp.released {
		tp.heldX += dx
		tp.heldY += dy
		heldDist := math.Hypot(tp.heldX, tp.heldY)
		waited := now.Sub(tp.began) > tpTapTime
		if heldDist < tpSlop && !waited {
			return nil
		}
		tp.released = true
		tp.gestureMoved = tp.gestureMoved || heldDist >= tpSlop
		tp.mode = tpModePointer
		dx, dy = tp.heldX, tp.heldY
	}
	if dx == 0 && dy == 0 {
		return nil
	}
	tp.gestureMoved = true
	mult := tp.speed * TouchpadGain(tp.smoothVelLocked(now))
	mx, my := float32(dx*mult), float32(dy*mult)
	return []func(){func() { tp.sink.Move(mx, my) }}
}

func (tp *Touchpad) smoothVelLocked(now time.Time) float64 {
	v := tp.rawVelLocked(now)
	last := tp.velAt
	tp.velAt = now
	if last.IsZero() || now.Sub(last) > 100*time.Millisecond {
		tp.vel = v
		return v
	}
	dt := float64(now.Sub(last).Microseconds()) / 1000.0
	a := 1.0 - math.Exp(-dt/40.0)
	tp.vel += (v - tp.vel) * a
	return tp.vel
}

func (tp *Touchpad) rawVelLocked(now time.Time) float64 {
	if len(tp.samples) < 2 {
		return 0
	}
	span := float64(now.Sub(tp.samples[0].at).Microseconds()) / 1000.0
	if span <= 0 {
		return 0
	}
	sum := 0.0
	for _, s := range tp.samples[1:] {
		sum += s.dist
	}
	return sum / span
}

func (tp *Touchpad) twoLocked(now time.Time) []func() {
	fx, fy := tp.centroidLocked()
	span := tp.spreadLocked()
	dx, dy := fx-tp.focalX, fy-tp.focalY
	scale := 1.0
	if tp.span > 0 {
		scale = span / tp.span
	}
	tp.focalX, tp.focalY = fx, fy
	tp.span = span
	tp.travelX += dx
	tp.travelY += dy
	if tp.mode == tpModeTwo {
		spanChange := math.Abs(span - tp.spanStart)
		travel := math.Hypot(tp.travelX, tp.travelY)
		if math.Max(spanChange, travel) < tpSlop*1.5 {
			return nil
		}
		if tp.pinch && tp.spanStart > 0 && spanChange > travel {
			tp.mode = tpModePinch
			sc := float32(span / tp.spanStart)
			cfx, cfy, tx, ty := float32(fx), float32(fy), float32(tp.travelX), float32(tp.travelY)
			return []func(){func() { tp.sink.Pinch(sc, cfx, cfy, tx, ty) }}
		}
		tp.mode = tpModeScroll
		switch {
		case math.Abs(tp.travelY) > math.Abs(tp.travelX)*2:
			tp.axis = tpAxisVertical
		case math.Abs(tp.travelX) > math.Abs(tp.travelY)*2:
			tp.axis = tpAxisHorizontal
		default:
			tp.axis = tpAxisFree
		}
		return tp.scrollByLocked(tp.travelX, tp.travelY, now)
	}
	switch tp.mode {
	case tpModePinch:
		sc, cfx, cfy, tx, ty := float32(scale), float32(fx), float32(fy), float32(dx), float32(dy)
		return []func(){func() { tp.sink.Pinch(sc, cfx, cfy, tx, ty) }}
	case tpModeScroll:
		return tp.scrollByLocked(dx, dy, now)
	}
	return nil
}

func (tp *Touchpad) scrollByLocked(fx, fy float64, now time.Time) []func() {
	switch tp.axis {
	case tpAxisVertical:
		fx = 0
	case tpAxisHorizontal:
		fy = 0
	}
	if fx == 0 && fy == 0 {
		return nil
	}
	// Natural scrolling: content follows fingers.
	nx, ny := -fx/tpPxPerNotch, fy/tpPxPerNotch
	tp.scrollSamples = append(tp.scrollSamples, tpScrollSample{at: now, dx: nx, dy: ny})
	cut := now.Add(-80 * time.Millisecond)
	keep := tp.scrollSamples[:0]
	for _, s := range tp.scrollSamples {
		if !s.at.Before(cut) {
			keep = append(keep, s)
		}
	}
	tp.scrollSamples = keep
	sx, sy := float32(nx), float32(ny)
	return []func(){func() { tp.sink.Scroll(sx, sy) }}
}

func (tp *Touchpad) startFlickLocked(now time.Time) {
	if len(tp.scrollSamples) < 2 {
		return
	}
	last := tp.scrollSamples[len(tp.scrollSamples)-1]
	if now.Sub(last.at) > 50*time.Millisecond {
		return
	}
	span := float64(now.Sub(tp.scrollSamples[0].at).Microseconds()) / 1000.0
	if span <= 0 || span > 120 {
		return
	}
	var sumX, sumY float64
	for _, s := range tp.scrollSamples {
		sumX += s.dx
		sumY += s.dy
	}
	vx, vy := sumX/span, sumY/span
	if math.Hypot(vx, vy) < 0.005 {
		return
	}
	stop := make(chan struct{})
	tp.inertiaStop = stop
	go func() {
		tick := time.NewTicker(tpFrame)
		defer tick.Stop()
		frameMS := float64(tpFrame.Milliseconds())
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				vx *= 0.93
				vy *= 0.93
				if math.Hypot(vx, vy) < 0.002 {
					return
				}
				tp.sink.Scroll(float32(vx*frameMS), float32(vy*frameMS))
			}
		}
	}()
}

func (tp *Touchpad) stopInertiaLocked() {
	if tp.inertiaStop != nil {
		close(tp.inertiaStop)
		tp.inertiaStop = nil
	}
}

func (tp *Touchpad) centroidLocked() (float64, float64) {
	if len(tp.touches) == 0 {
		return 0, 0
	}
	var sx, sy float64
	for _, p := range tp.touches {
		sx += p[0]
		sy += p[1]
	}
	n := float64(len(tp.touches))
	return sx / n, sy / n
}

func (tp *Touchpad) spreadLocked() float64 {
	lo, hi := -1, -1
	for id := range tp.touches {
		switch {
		case lo < 0 || id < lo:
			lo, hi = id, lo
		case hi < 0 || id < hi:
			hi = id
		}
	}
	if lo < 0 || hi < 0 {
		return 0
	}
	a, b := tp.touches[lo], tp.touches[hi]
	return math.Hypot(a[0]-b[0], a[1]-b[1])
}
