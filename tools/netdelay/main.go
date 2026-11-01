// Command netdelay is a TCP proxy that makes a slow, uneven path out of a
// fast one, for trying sessions over high latency on one machine
// (doc/en-us/development.md): each direction is delayed by half the round
// trip, limited to a rate, given random jitter, frozen now and then, and
// cut once if asked.
//
//	go run ./tools/netdelay -listen 127.0.0.1:47400 -to 127.0.0.1:47310 \
//	    -rtt 230ms -rate 3mbit -jitter 20ms -stall 45s/3s -cut 20s/5s
//
// A cut is a black hole, not a close: from AFTER past the start the path
// passes nothing either way and neither side sees its connection end; what
// was sent waits and arrives, in order, when the cut heals after FOR (FOR 0:
// never). A connection accepted during a cut reaches `to` once it heals.
//
// Bytes keep their order; what waits behind the rate is bounded (queue),
// so a sender that outpaces the path is held back as by a real one.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net"
	"strconv"
	"strings"
	"time"
)

// chunk is the most a reader passes on at once, and queue how many chunks
// wait for the path in each direction.
const (
	chunk = 4 << 10
	queue = 64
)

// path is how one direction of every connection behaves.
type path struct {
	delay  time.Duration // half the round trip
	jitter time.Duration
	rate   float64 // bits per second; 0 unlimited
	// Every stallEvery the path freezes for stallFor (0: never), counted
	// from start.
	stallEvery, stallFor time.Duration
	// From cutAt past start the path passes nothing for cutFor (0: for
	// good); cutAt 0 is no cut.
	cutAt, cutFor time.Duration
	start         time.Time
}

func main() {
	listen := flag.String("listen", "127.0.0.1:47400", "where to accept connections")
	to := flag.String("to", "127.0.0.1:47310", "where to pass them on")
	rtt := flag.Duration("rtt", 230*time.Millisecond, "the round trip to add")
	rate := flag.String("rate", "", "each direction's rate, as 3mbit, 500kbit or 1gbit (empty: unlimited)")
	jitter := flag.Duration("jitter", 0, "random extra delay of each chunk, up to this")
	stall := flag.String("stall", "", "freeze the path: EVERY/FOR, as 45s/3s (empty: never)")
	cut := flag.String("cut", "", "cut the path once, both ways, holding its bytes: AFTER/FOR, as 20s/5s, FOR 0 for good (empty: never)")
	flag.Parse()
	p := path{delay: *rtt / 2, jitter: *jitter, start: time.Now()}
	var err error
	if p.rate, err = parseRate(*rate); err != nil {
		log.Fatal(err)
	}
	if p.stallEvery, p.stallFor, err = parseStall(*stall); err != nil {
		log.Fatal(err)
	}
	if p.cutAt, p.cutFor, err = parseCut(*cut); err != nil {
		log.Fatal(err)
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("netdelay: %s -> %s, rtt %v, rate %s, jitter %v, stall %q, cut %q", *listen, *to, *rtt, *rate, *jitter, *stall, *cut)
	if p.stallEvery > 0 {
		go announceStalls(p)
	}
	if p.cutAt > 0 {
		go announceCut(p)
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go proxy(c, *to, p)
	}
}

// parseRate reads a rate such as 3mbit.
// Returns: bits per second, 0 for an empty one.
func parseRate(s string) (float64, error) {
	if s == "" {
		return 0, nil
	}
	units := []struct {
		suffix string
		mul    float64
	}{{"gbit", 1e9}, {"mbit", 1e6}, {"kbit", 1e3}, {"bit", 1}}
	for _, u := range units {
		if n, ok := strings.CutSuffix(s, u.suffix); ok {
			v, err := strconv.ParseFloat(n, 64)
			if err != nil || v <= 0 {
				return 0, fmt.Errorf("bad rate %q", s)
			}
			return v * u.mul, nil
		}
	}
	return 0, fmt.Errorf("bad rate %q (want e.g. 3mbit)", s)
}

// parseStall reads EVERY/FOR, such as 45s/3s.
func parseStall(s string) (every, dur time.Duration, err error) {
	if s == "" {
		return 0, 0, nil
	}
	a, b, ok := strings.Cut(s, "/")
	if !ok {
		return 0, 0, fmt.Errorf("bad stall %q (want EVERY/FOR)", s)
	}
	if every, err = time.ParseDuration(a); err != nil {
		return 0, 0, err
	}
	if dur, err = time.ParseDuration(b); err != nil {
		return 0, 0, err
	}
	if every <= dur || dur <= 0 {
		return 0, 0, errors.New("a stall must be shorter than its period")
	}
	return every, dur, nil
}

// stallEnd is when the stall that covers t ends, or t outside one: the
// last stallFor of each stallEvery.
func (p path) stallEnd(t time.Time) time.Time {
	if p.stallEvery == 0 {
		return t
	}
	in := t.Sub(p.start) % p.stallEvery
	if in < p.stallEvery-p.stallFor {
		return t
	}
	return t.Add(p.stallEvery - in)
}

// parseCut reads AFTER/FOR, such as 20s/5s; FOR 0 cuts for good.
// Returns: when the cut begins after the start and how long it lasts, both
// 0 for an empty one.
func parseCut(s string) (after, dur time.Duration, err error) {
	if s == "" {
		return 0, 0, nil
	}
	a, b, ok := strings.Cut(s, "/")
	if !ok {
		return 0, 0, fmt.Errorf("bad cut %q (want AFTER/FOR)", s)
	}
	if after, err = time.ParseDuration(a); err != nil {
		return 0, 0, err
	}
	if dur, err = time.ParseDuration(b); err != nil {
		return 0, 0, err
	}
	if after <= 0 || dur < 0 {
		return 0, 0, fmt.Errorf("bad cut %q: AFTER must be positive, FOR not negative", s)
	}
	return after, dur, nil
}

// cutEnd is when bytes due at t may go: t itself outside the cut, the
// cut's end inside it.
// Returns: false when t falls into a cut that never heals.
func (p path) cutEnd(t time.Time) (time.Time, bool) {
	if p.cutAt == 0 {
		return t, true
	}
	begin := p.start.Add(p.cutAt)
	if t.Before(begin) {
		return t, true
	}
	if p.cutFor == 0 {
		return time.Time{}, false
	}
	if end := begin.Add(p.cutFor); t.Before(end) {
		return end, true
	}
	return t, true
}

// announceCut logs the cut as it begins and as it heals.
func announceCut(p path) {
	time.Sleep(time.Until(p.start.Add(p.cutAt)))
	if p.cutFor == 0 {
		log.Printf("netdelay: cut for good")
		return
	}
	log.Printf("netdelay: cut for %v", p.cutFor)
	time.Sleep(p.cutFor)
	log.Printf("netdelay: cut healed")
}

// announceStalls logs each stall as it begins.
func announceStalls(p path) {
	for k := 1; ; k++ {
		begin := p.start.Add(time.Duration(k)*p.stallEvery - p.stallFor)
		time.Sleep(time.Until(begin))
		log.Printf("netdelay: stalled for %v", p.stallFor)
	}
}

// proxy passes one connection on to `to` and back through the path.
// Notes: a connection accepted during a cut dials only once the cut heals,
// and never if it does not.
func proxy(c net.Conn, to string, p path) {
	defer c.Close()
	at, ok := p.cutEnd(time.Now())
	if !ok {
		select {} // the cut never heals: the black hole keeps the connection
	}
	time.Sleep(time.Until(at))
	up, err := net.Dial("tcp", to)
	if err != nil {
		log.Printf("netdelay: %s: %v", to, err)
		return
	}
	defer up.Close()
	log.Printf("netdelay: %s connected", c.RemoteAddr())
	done := make(chan struct{}, 2)
	go func() { pipe(up, c, p); done <- struct{}{} }()
	go func() { pipe(c, up, p); done <- struct{}{} }()
	<-done
	log.Printf("netdelay: %s closed", c.RemoteAddr())
}

// timed is a chunk with when it arrived.
type timed struct {
	b  []byte
	at time.Time
}

// pipe copies src to dst through the path until either ends: a reader
// stamps chunks as they arrive, a writer sends each when the path lets it
// (behind the ones before it, the rate and the delay, after any stall and
// the cut). Notes: in a cut that never heals the writer holds its chunk for
// good, so the reader stops once the queue is full and nothing is closed.
func pipe(dst io.WriteCloser, src io.Reader, p path) {
	ch := make(chan timed, queue)
	go func() {
		defer close(ch)
		for {
			b := make([]byte, chunk)
			n, err := src.Read(b)
			if n > 0 {
				ch <- timed{b[:n], time.Now()}
			}
			if err != nil {
				return
			}
		}
	}()
	var free, last time.Time // when the rate lets the next chunk go; the last release
	for t := range ch {
		start := t.at
		if free.After(start) {
			start = free
		}
		if p.rate > 0 {
			free = start.Add(time.Duration(float64(len(t.b)*8) / p.rate * float64(time.Second)))
		} else {
			free = start
		}
		at := free.Add(p.delay)
		if p.jitter > 0 {
			at = at.Add(rand.N(p.jitter))
		}
		if at.Before(last) {
			at = last // order is kept
		}
		at, ok := p.cutEnd(p.stallEnd(at))
		if !ok {
			select {} // cut for good: hold everything, close nothing
		}
		last = at
		time.Sleep(time.Until(at))
		if _, err := dst.Write(t.b); err != nil {
			break
		}
	}
	dst.Close()
	for range ch {
	}
}
