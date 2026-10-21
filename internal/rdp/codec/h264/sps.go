// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/codec/h264/sps.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package h264

import (
	"strconv"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const (
	macroblockSize   = 16
	codedSizePadding = 2 * macroblockSize
)

func validateSequenceSets(sequenceSets [][]byte, width, height int) error {
	maxWidth := uint64(width+codedSizePadding-1) &^ (codedSizePadding - 1)
	maxHeight := uint64(height+codedSizePadding-1) &^ (codedSizePadding - 1)
	for _, sequence := range sequenceSets {
		codedWidth, codedHeight, err := spsDimensions(sequence)
		if err != nil {
			return err
		}
		if codedWidth > maxWidth || codedHeight > maxHeight {
			return E.New("h264: coded picture ", codedWidth, "x", codedHeight, " exceeds the destination maximum ", maxWidth, "x", maxHeight)
		}
	}
	return nil
}

func spsDimensions(nal []byte) (uint64, uint64, error) {
	if len(nal) == 0 {
		return 0, 0, E.New("h264: empty SPS NAL unit")
	}
	if nal[0]&0x80 != 0 || nal[0]&0x1F != nalSPS {
		return 0, 0, E.New("h264: invalid SPS NAL header 0x", strconv.FormatUint(uint64(nal[0]), 16))
	}
	r := spsReader{data: nal[1:]}
	profile := r.bits(8)
	if r.bits(8)&3 != 0 {
		r.invalid(E.New("reserved constraint bits set"))
	}
	r.bits(8)
	r.ueLimit(31)
	switch profile {
	case 44, 83, 86, 100, 110, 118, 122, 128, 134, 135, 138, 139, 144, 244:
		chroma := r.ueLimit(3)
		if chroma == 3 {
			r.bits(1)
		}
		r.ueLimit(6)
		r.ueLimit(6)
		r.bits(1)
		if r.bits(1) != 0 {
			count := 8
			if chroma == 3 {
				count = 12
			}
			for i := 0; i < count && r.err == nil; i++ {
				if r.bits(1) != 0 {
					size := 16
					if i >= 6 {
						size = 64
					}
					r.scalingList(size)
				}
			}
		}
	case 66, 77, 88:
	default:
		r.invalid(E.New("unsupported profile_idc ", profile))
	}
	r.ueLimit(12)
	switch r.ueLimit(2) {
	case 0:
		r.ueLimit(12)
	case 1:
		r.bits(1)
		r.se()
		r.se()
		count := r.ueLimit(255)
		for i := uint32(0); i < count && r.err == nil; i++ {
			r.se()
		}
	}
	r.ue()
	r.bits(1)
	width := (uint64(r.ue()) + 1) * macroblockSize
	height := (uint64(r.ue()) + 1) * macroblockSize
	if r.bits(1) == 0 {
		height *= 2
	}
	if r.err != nil {
		return 0, 0, E.Cause(r.err, "h264: parse SPS")
	}
	return width, height, nil
}

type spsReader struct {
	data      []byte
	next      int
	zeros     uint8
	value     byte
	remaining uint8
	err       error
}

func (r *spsReader) invalid(err error) {
	if r.err == nil {
		r.err = err
	}
}

func (r *spsReader) bits(count uint8) uint32 {
	var value uint32
	for range count {
		if r.err != nil {
			return 0
		}
		if r.remaining == 0 {
			if r.next == len(r.data) {
				r.err = E.New("truncated after ", len(r.data), " bytes")
				return 0
			}
			b := r.data[r.next]
			r.next++
			if r.zeros == 2 {
				if b == 3 {
					if r.next == len(r.data) {
						r.err = E.New("truncated after ", len(r.data), " bytes")
						return 0
					}
					b = r.data[r.next]
					r.next++
					if b > 3 {
						r.invalid(E.New("invalid emulation-prevention byte 0x", strconv.FormatUint(uint64(b), 16), " at byte ", r.next))
						return 0
					}
					r.zeros = 0
				} else if b < 3 {
					r.invalid(E.New("missing emulation-prevention byte at byte ", r.next))
					return 0
				}
			}
			if b == 0 {
				r.zeros++
			} else {
				r.zeros = 0
			}
			r.value, r.remaining = b, 8
		}
		r.remaining--
		value = value<<1 | uint32(r.value>>r.remaining&1)
	}
	return value
}

func (r *spsReader) ue() uint32 {
	var zeros uint8
	for r.bits(1) == 0 {
		if r.err != nil {
			return 0
		}
		zeros++
		if zeros > 31 {
			r.invalid(E.New("Exp-Golomb code with more than 31 leading zeros"))
			return 0
		}
	}
	return (uint32(1)<<zeros - 1) + r.bits(zeros)
}

func (r *spsReader) ueLimit(limit uint32) uint32 {
	value := r.ue()
	if value > limit {
		r.invalid(E.New("syntax value ", value, " exceeds ", limit))
	}
	return value
}

func (r *spsReader) se() int64 {
	value := r.ue()
	if value&1 != 0 {
		return (int64(value) + 1) / 2
	}
	return -int64(value) / 2
}

func (r *spsReader) scalingList(size int) {
	last := int64(8)
	for i := 0; i < size && r.err == nil; i++ {
		delta := r.se()
		if delta < -128 || delta > 127 {
			r.invalid(E.New("scaling-list delta ", delta, " out of range"))
			return
		}
		next := (last + delta + 256) % 256
		if next == 0 {
			return
		}
		last = next
	}
}
