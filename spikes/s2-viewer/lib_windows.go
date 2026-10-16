package main

import (
	"syscall"
	"time"
)

type lib struct{ start, pixels, acquire, release, produced, drawNs uintptr }

func load(path string) (*lib, error) {
	h, err := syscall.LoadLibrary(path)
	if err != nil {
		return nil, err
	}
	var l lib
	for name, dst := range map[string]*uintptr{"s2_start": &l.start, "s2_slot_pixels": &l.pixels,
		"s2_acquire": &l.acquire, "s2_release": &l.release, "s2_produced": &l.produced, "s2_draw_ns": &l.drawNs} {
		if *dst, err = syscall.GetProcAddress(h, name); err != nil {
			return nil, err
		}
	}
	return &l, nil
}

func (l *lib) call(fn uintptr, args ...uintptr) uintptr {
	r, _, _ := syscall.SyscallN(fn, args...)
	return r
}

// cpuTime is the process's user + kernel time.
func cpuTime() time.Duration {
	var c, e, k, u syscall.Filetime
	h, _ := syscall.GetCurrentProcess()
	if err := syscall.GetProcessTimes(h, &c, &e, &k, &u); err != nil {
		return 0
	}
	f := func(t syscall.Filetime) int64 { return int64(t.HighDateTime)<<32 | int64(t.LowDateTime) }
	return time.Duration((f(k) + f(u)) * 100)
}
