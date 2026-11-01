package main

import (
	"io"
	"testing"
	"time"
)

// TestPathRules: rates and stalls parse as written, and a stall holds what
// falls into it until it ends.
func TestPathRules(t *testing.T) {
	for _, c := range []struct {
		in   string
		want float64
		ok   bool
	}{{"", 0, true}, {"3mbit", 3e6, true}, {"500kbit", 5e5, true}, {"1.5gbit", 1.5e9, true}, {"3mb", 0, false}, {"-1mbit", 0, false}} {
		got, err := parseRate(c.in)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("rate %q: %v %v", c.in, got, err)
		}
	}
	if _, _, err := parseStall("3s/45s"); err == nil {
		t.Error("a stall longer than its period")
	}
	every, dur, err := parseStall("10s/2s")
	if err != nil || every != 10*time.Second || dur != 2*time.Second {
		t.Fatalf("stall: %v %v %v", every, dur, err)
	}
	t0 := time.Unix(0, 0)
	p := path{stallEvery: every, stallFor: dur, start: t0}
	for _, c := range []struct{ at, want time.Duration }{
		{time.Second, time.Second},          // before the first stall
		{8 * time.Second, 10 * time.Second}, // in it: held to its end
		{9500 * time.Millisecond, 10 * time.Second},
		{10 * time.Second, 10 * time.Second}, // the path moves again
		{19 * time.Second, 20 * time.Second}, // the next one
	} {
		if got := p.stallEnd(t0.Add(c.at)).Sub(t0); got != c.want {
			t.Errorf("at %v: released at %v", c.at, got)
		}
	}
}

// TestCutRules: cuts parse as written, and a cut holds what falls into it
// until it heals, or for good.
func TestCutRules(t *testing.T) {
	for _, c := range []struct {
		in         string
		after, dur time.Duration
		ok         bool
	}{
		{"", 0, 0, true},
		{"20s/5s", 20 * time.Second, 5 * time.Second, true},
		{"1m/0", time.Minute, 0, true},
		{"20s", 0, 0, false},
		{"0s/5s", 0, 0, false},
		{"20s/-1s", 0, 0, false},
		{"x/5s", 0, 0, false},
	} {
		after, dur, err := parseCut(c.in)
		if (err == nil) != c.ok || after != c.after || dur != c.dur {
			t.Errorf("cut %q: %v %v %v", c.in, after, dur, err)
		}
	}
	t0 := time.Unix(0, 0)
	for _, c := range []struct {
		name          string
		cutAt, cutFor time.Duration
		at, want      time.Duration
		ok            bool
	}{
		{"no cut", 0, 0, 30 * time.Second, 30 * time.Second, true},
		{"before", 20 * time.Second, 5 * time.Second, 19 * time.Second, 19 * time.Second, true},
		{"at its start", 20 * time.Second, 5 * time.Second, 20 * time.Second, 25 * time.Second, true},
		{"inside", 20 * time.Second, 5 * time.Second, 24 * time.Second, 25 * time.Second, true},
		{"healed", 20 * time.Second, 5 * time.Second, 25 * time.Second, 25 * time.Second, true},
		{"after", 20 * time.Second, 5 * time.Second, 40 * time.Second, 40 * time.Second, true},
		{"for good, before", 20 * time.Second, 0, 19 * time.Second, 19 * time.Second, true},
		{"for good", 20 * time.Second, 0, time.Hour, 0, false},
	} {
		p := path{cutAt: c.cutAt, cutFor: c.cutFor, start: t0}
		got, ok := p.cutEnd(t0.Add(c.at))
		if ok != c.ok || (ok && got.Sub(t0) != c.want) {
			t.Errorf("%s: %v %v", c.name, got.Sub(t0), ok)
		}
	}
}

// closer records whether pipe closed its destination.
type closer struct {
	got    chan []byte
	closed chan struct{}
}

// Write passes each chunk to the test.
func (c *closer) Write(b []byte) (int, error) {
	c.got <- append([]byte(nil), b...)
	return len(b), nil
}

// Close notes that the destination was closed.
func (c *closer) Close() error {
	close(c.closed)
	return nil
}

// TestCutHolds: through a real pipe, bytes sent into a cut arrive when it
// heals, and in a cut for good nothing arrives and nothing is closed, even
// when the source ends.
func TestCutHolds(t *testing.T) {
	for _, c := range []struct {
		name   string
		cutFor time.Duration
	}{{"heals", 300 * time.Millisecond}, {"for good", 0}} {
		t.Run(c.name, func(t *testing.T) {
			p := path{cutAt: 50 * time.Millisecond, cutFor: c.cutFor, start: time.Now()}
			r, w := io.Pipe()
			dst := &closer{got: make(chan []byte, 4), closed: make(chan struct{})}
			go pipe(dst, r, p)
			time.Sleep(100 * time.Millisecond)
			if _, err := w.Write([]byte("held")); err != nil {
				t.Fatal(err)
			}
			w.Close()
			select {
			case b := <-dst.got:
				if c.cutFor == 0 {
					t.Fatalf("%q passed a cut for good", b)
				}
				if since := time.Since(p.start); since < p.cutAt+c.cutFor || string(b) != "held" {
					t.Fatalf("%q after %v, inside the cut", b, since)
				}
				<-dst.closed // the source's end follows, after the bytes
			case <-dst.closed:
				t.Fatal("the cut closed the destination")
			case <-time.After(600 * time.Millisecond):
				if c.cutFor > 0 {
					t.Fatal("nothing after the cut healed")
				}
			}
		})
	}
}
