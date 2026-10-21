package main

import (
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
