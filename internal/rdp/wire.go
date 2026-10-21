// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/wire.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package rdp

import (
	"encoding/binary"
	"unicode/utf16"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

type reader struct {
	data []byte
	err  error
}

func (r *reader) take(n int) []byte {
	if r.err != nil {
		return nil
	}
	if n < 0 {
		r.err = E.New("rdp: negative field length ", n)
		r.data = nil
		return nil
	}
	if n > len(r.data) {
		r.err = E.New("rdp: truncated message: ", len(r.data), " of ", n, " bytes")
		r.data = nil
		return nil
	}
	b := r.data[:n:n]
	r.data = r.data[n:]
	return b
}

func (r *reader) u8() uint8 {
	b := r.take(1)
	if b == nil {
		return 0
	}
	return b[0]
}

func (r *reader) u16() uint16 {
	b := r.take(2)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint16(b)
}

func (r *reader) u16be() uint16 {
	b := r.take(2)
	if b == nil {
		return 0
	}
	return binary.BigEndian.Uint16(b)
}

func (r *reader) u32() uint32 {
	b := r.take(4)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint32(b)
}

func (r *reader) u64() uint64 {
	b := r.take(8)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint64(b)
}

func (r *reader) rest() []byte {
	b := r.data
	r.data = nil
	return b
}

func (r *reader) remaining() int {
	return len(r.data)
}

func (r *reader) sub(n int) *reader {
	b := r.take(n)
	if b == nil {
		return &reader{err: r.err}
	}
	return &reader{data: b}
}

func uint16s(values ...uint16) []byte {
	var b []byte
	for _, v := range values {
		b = binary.LittleEndian.AppendUint16(b, v)
	}
	return b
}

func lengthBlock(kind uint16, data []byte) []byte {
	b := binary.LittleEndian.AppendUint16(nil, kind)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(data)+4))
	return append(b, data...)
}

func zeros(b []byte, n int) []byte {
	return append(b, make([]byte, n)...)
}

func utf16le(s string) []byte {
	var b []byte
	for _, unit := range utf16.Encode([]rune(s)) {
		b = binary.LittleEndian.AppendUint16(b, unit)
	}
	return b
}

func fixedUTF16(b []byte, s string, size int) []byte {
	start := len(b)
	for _, unit := range utf16.Encode([]rune(s)) {
		if len(b)-start+2 > size-2 {
			break
		}
		b = binary.LittleEndian.AppendUint16(b, unit)
	}
	return zeros(b, size-(len(b)-start))
}

func decodeUTF16(b []byte) string {
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		unit := binary.LittleEndian.Uint16(b[i:])
		if unit == 0 {
			break
		}
		units = append(units, unit)
	}
	return string(utf16.Decode(units))
}
