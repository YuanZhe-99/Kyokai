// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/codec/rfx/rlgr.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package rfx

import (
	"math/bits"

	"github.com/YuanZhe-99/Kyokai/internal/rdp/codec/internal/bitio"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const (
	kpMax = 80
	lsgr  = 3
	upGR  = 4
	dnGR  = 6
	uqGR  = 3
	dqGR  = 3
)

func rlgrDecode(src []byte, out *[tileCoefficients]int16, rlgr3 bool) error {
	if len(src) == 0 {
		return E.New("empty component data")
	}
	acc, have, pos := bitio.Refill(src, 0, 0, 0)
	left := len(src) * 8
	k, kp, kr, krp := uint(1), uint(1<<lsgr), uint(1), uint(1<<lsgr)
	n := 0
	for left > 0 && n < tileCoefficients {
		if have < 32 {
			acc, have, pos = bitio.Refill(src, pos, acc, have)
		}
		if k != 0 {
			vk := uint(bits.LeadingZeros64(acc))
			if vk < have {
				acc <<= vk
				have -= vk
			} else {
				vk, acc, have, pos = countRun(src, pos, acc, have, 0)
			}
			if int(vk) >= left {
				break
			}
			left -= int(vk) + 1
			acc <<= 1
			have--
			if have < 32 {
				acc, have, pos = bitio.Refill(src, pos, acc, have)
			}
			run := 0
			for ; vk > 0 && kp < kpMax; vk-- {
				run += 1 << k
				kp = min(kp+upGR, kpMax)
				k = kp >> lsgr
			}
			run += int(vk) << k
			if left < int(k)+1 {
				break
			}
			run += int(acc >> (64 - k))
			sign := acc << k >> 63
			acc <<= k + 1
			have -= k + 1
			left -= int(k) + 1
			vk = uint(bits.LeadingZeros64(^acc))
			if vk < have {
				acc <<= vk
				have -= vk
			} else {
				vk, acc, have, pos = countRun(src, pos, acc, have, ^uint64(0))
			}
			if int(vk) >= left {
				break
			}
			left -= int(vk) + 1
			acc <<= 1
			have--
			if have < 32 {
				acc, have, pos = bitio.Refill(src, pos, acc, have)
			}
			if left < int(kr) {
				break
			}
			code := uint16(acc>>(64-kr)) | uint16(uint32(vk)<<kr)
			acc <<= kr
			have -= kr
			left -= int(kr)
			krp = updateKRP(krp, vk)
			kr = krp >> lsgr
			kp = max(kp, dnGR) - dnGR
			k = kp >> lsgr
			negative := -int16(sign)
			magnitude := (int16(code+1) ^ negative) - negative
			run = min(run, tileCoefficients-n)
			clear(out[n : n+run])
			n += run
			if n < tileCoefficients {
				out[n] = magnitude
				n++
			}
			continue
		}
		vk := uint(bits.LeadingZeros64(^acc))
		if vk < have {
			acc <<= vk
			have -= vk
		} else {
			vk, acc, have, pos = countRun(src, pos, acc, have, ^uint64(0))
		}
		if int(vk) >= left {
			break
		}
		left -= int(vk) + 1
		acc <<= 1
		have--
		if have < 32 {
			acc, have, pos = bitio.Refill(src, pos, acc, have)
		}
		if left < int(kr) {
			break
		}
		code := uint16(acc>>(64-kr)) | uint16(uint32(vk)<<kr)
		acc <<= kr
		have -= kr
		left -= int(kr)
		krp = updateKRP(krp, vk)
		kr = krp >> lsgr
		if !rlgr3 {
			up := min(kp+uqGR, kpMax)
			kp = max(kp, dqGR) - dqGR
			if code == 0 {
				kp = up
			}
			k = kp >> lsgr
			out[n] = interleavedValue(uint32(code))
			n++
			continue
		}
		// FreeRDP sign-extends code through int16 before measuring it, and a resulting 32-bit
		// field reads as zero without consuming any bits.
		var val1 uint32
		if code&0x8000 != 0 {
			if left < 32 {
				break
			}
		} else {
			nIdx := uint(bits.Len16(code))
			if left < int(nIdx) {
				break
			}
			if have < 16 {
				acc, have, pos = bitio.Refill(src, pos, acc, have)
			}
			val1 = uint32(acc >> (64 - nIdx))
			acc <<= nIdx
			have -= nIdx
			left -= int(nIdx)
		}
		val2 := uint32(code) - val1
		if val1 != 0 && val2 != 0 {
			kp = max(kp, 2*dqGR) - 2*dqGR
		} else if val1 == 0 && val2 == 0 {
			kp = min(kp+2*uqGR, kpMax)
		}
		k = kp >> lsgr
		out[n] = interleavedValue(val1)
		n++
		if n < tileCoefficients {
			out[n] = interleavedValue(val2)
			n++
		}
	}
	clear(out[n:])
	return nil
}

func updateKRP(krp, vk uint) uint {
	next := min(krp+vk, kpMax)
	if vk == 1 {
		next = krp
	}
	if vk == 0 {
		next = max(krp, 2) - 2
	}
	return next
}

func interleavedValue(v uint32) int16 {
	negative := -int16(v & 1)
	return (int16((v+1)>>1) ^ negative) - negative
}
