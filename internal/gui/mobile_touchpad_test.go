package gui

import (
	"testing"
	"time"
)

type recordSink struct {
	moves   [][2]float32
	buttons [][2]int
	scrolls [][2]float32
	pinches [][3]float32
}

func (r *recordSink) Move(dx, dy float32) { r.moves = append(r.moves, [2]float32{dx, dy}) }
func (r *recordSink) Button(b int, down bool) {
	d := 0
	if down {
		d = 1
	}
	r.buttons = append(r.buttons, [2]int{b, d})
}
func (r *recordSink) Scroll(dx, dy float32) { r.scrolls = append(r.scrolls, [2]float32{dx, dy}) }
func (r *recordSink) Pinch(scale, fx, fy, px, py float32) {
	r.pinches = append(r.pinches, [3]float32{scale, px, py})
}

func TestTouchpadTapsAndDrag(t *testing.T) {
	sink := &recordSink{}
	tp := NewTouchpad(sink, true, 1.0)
	defer tp.Reset()

	t0 := time.Unix(100, 0)
	// Micro-jitter under slop during a tap does not move the pointer.
	tp.Down(1, 100, 100, t0)
	tp.Move(1, 102, 101, t0.Add(20*time.Millisecond))
	tp.Up(1, t0.Add(60*time.Millisecond))
	if len(sink.moves) != 0 {
		t.Fatalf("expected 0 moves on tap jitter, got %v", sink.moves)
	}
	if len(sink.buttons) != 1 || sink.buttons[0] != [2]int{0, 1} {
		t.Fatalf("expected primary down on tap, got %v", sink.buttons)
	}

	// Second tap while tapHold is active becomes a double click.
	tp.Down(2, 100, 100, t0.Add(90*time.Millisecond))
	tp.Up(2, t0.Add(130*time.Millisecond))
	want := [][2]int{{0, 1}, {0, 0}, {0, 1}, {0, 0}}
	if len(sink.buttons) != len(want) {
		t.Fatalf("double tap buttons=%v, want %v", sink.buttons, want)
	}
	for i := range want {
		if sink.buttons[i] != want[i] {
			t.Fatalf("button[%d]=%v, want %v", i, sink.buttons[i], want[i])
		}
	}

	// Two-finger tap -> right click, three-finger tap -> middle click.
	sink.buttons = nil
	tp.Down(1, 50, 50, t0)
	tp.Down(2, 90, 50, t0.Add(10*time.Millisecond))
	tp.Up(2, t0.Add(50*time.Millisecond))
	tp.Up(1, t0.Add(60*time.Millisecond))
	if len(sink.buttons) != 2 || sink.buttons[0] != [2]int{1, 1} || sink.buttons[1] != [2]int{1, 0} {
		t.Fatalf("2-finger tap buttons=%v", sink.buttons)
	}

	sink.buttons = nil
	tp.Down(1, 50, 50, t0)
	tp.Down(2, 80, 50, t0.Add(5*time.Millisecond))
	tp.Down(3, 110, 50, t0.Add(10*time.Millisecond))
	tp.Up(3, t0.Add(40*time.Millisecond))
	tp.Up(2, t0.Add(45*time.Millisecond))
	tp.Up(1, t0.Add(50*time.Millisecond))
	if len(sink.buttons) != 2 || sink.buttons[0] != [2]int{2, 1} || sink.buttons[1] != [2]int{2, 0} {
		t.Fatalf("3-finger tap buttons=%v", sink.buttons)
	}
}

func TestTouchpadTwoFingerScrollAndPinch(t *testing.T) {
	sink := &recordSink{}
	tp := NewTouchpad(sink, true, 1.0)
	defer tp.Reset()

	t0 := time.Unix(200, 0)
	tp.Down(1, 100, 100, t0)
	tp.Down(2, 160, 100, t0)
	tp.Move(1, 100, 148, t0.Add(20*time.Millisecond))
	tp.Move(2, 160, 148, t0.Add(20*time.Millisecond))
	if len(sink.scrolls) == 0 || sink.scrolls[0][1] <= 0 {
		t.Fatalf("expected vertical scroll, got %v", sink.scrolls)
	}
	tp.Reset()

	// Spreading two fingers triggers pinch instead of scroll.
	sink.scrolls = nil
	sink.pinches = nil
	tp.Down(1, 100, 100, t0)
	tp.Down(2, 140, 100, t0)
	tp.Move(1, 70, 100, t0.Add(20*time.Millisecond))
	tp.Move(2, 170, 100, t0.Add(20*time.Millisecond))
	if len(sink.pinches) == 0 || sink.pinches[0][0] <= 1.0 {
		t.Fatalf("expected pinch > 1, got %v", sink.pinches)
	}
}
