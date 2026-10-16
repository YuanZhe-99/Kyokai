package main

import (
	"syscall"
	"time"

	"github.com/ebitengine/purego"
)

type lib struct{ start, pixels, acquire, release, produced, drawNs uintptr }

func load(path string) (*lib, error) {
	h, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, err
	}
	var l lib
	for name, dst := range map[string]*uintptr{"s2_start": &l.start, "s2_slot_pixels": &l.pixels,
		"s2_acquire": &l.acquire, "s2_release": &l.release, "s2_produced": &l.produced, "s2_draw_ns": &l.drawNs} {
		if *dst, err = purego.Dlsym(h, name); err != nil {
			return nil, err
		}
	}
	return &l, nil
}

func (l *lib) call(fn uintptr, args ...uintptr) uintptr {
	r, _, _ := purego.SyscallN(fn, args...)
	return r
}

// cpuTime is the process's user + system time.
func cpuTime() time.Duration {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}
