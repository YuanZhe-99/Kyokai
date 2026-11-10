//go:build !cgo || windows

package main

// Without cgo there is no core to call: this builds the clients' logic for
// its tests on a machine without a C compiler, and on Windows, whose
// runners' cgo may be an x86 toolchain on ARM64 (the library itself always
// builds with cgo, for Android).

import (
	"errors"
	"time"
)

func main() {}

func coreCall(string, any, any) error { return errors.New("no core") }

func mediaRead(uint64, []byte, time.Duration) int64 { return -2 }

func mediaWrite(uint64, []byte) int32 { return -2 }
