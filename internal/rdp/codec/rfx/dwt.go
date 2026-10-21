// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/codec/rfx/dwt.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package rfx

func inverseDWT(buf, tmp *[tileCoefficients]int16) {
	inverseDWTLevel(buf[linearLayout.offset[bandLevel3]:], tmp[:], tileSize/8)
	inverseDWTLevel(buf[linearLayout.offset[bandLevel2]:], tmp[:], tileSize/4)
	inverseDWTLevel(buf[:], tmp[:], tileSize/2)
}

func inverseDWTLevel(buf, tmp []int16, sw int) {
	tw := sw * 2
	band := sw * sw
	buf = buf[:4*band]
	tmp = tmp[:2*band*2]
	for y := range sw {
		hl := buf[y*sw:][:sw]
		lh := buf[band+y*sw:][:sw]
		hh := buf[2*band+y*sw:][:sw]
		ll := buf[3*band+y*sw:][:sw]
		l := tmp[y*tw:][:tw]
		h := tmp[sw*tw+y*tw:][:tw]
		le := int16(int(ll[0]) - int(hl[0]))
		he := int16(int(lh[0]) - int(hh[0]))
		l[0] = le
		h[0] = he
		for n := 1; n < sw; n++ {
			hlp, hln := int(hl[n-1]), int(hl[n])
			hhp, hhn := int(hh[n-1]), int(hh[n])
			ln := int16(int(ll[n]) - ((hlp + hln + 1) >> 1))
			hn := int16(int(lh[n]) - ((hhp + hhn + 1) >> 1))
			l[2*n] = ln
			h[2*n] = hn
			l[2*n-1] = int16(hlp<<1 + (int(le)+int(ln))>>1)
			h[2*n-1] = int16(hhp<<1 + (int(he)+int(hn))>>1)
			le, he = ln, hn
		}
		l[tw-1] = int16(int(hl[sw-1])<<1 + int(le))
		h[tw-1] = int16(int(hh[sw-1])<<1 + int(he))
	}
	{
		l0 := tmp[:tw]
		h0 := tmp[sw*tw:][:tw]
		d0 := buf[:tw]
		for x := range d0 {
			d0[x] = int16(int(l0[x]) - int(h0[x]))
		}
	}
	for n := 1; n < sw; n++ {
		ln := tmp[n*tw:][:tw]
		hp := tmp[(sw+n-1)*tw:][:tw]
		hn := tmp[(sw+n)*tw:][:tw]
		dp := buf[(2*n-2)*tw:][:tw]
		do := buf[(2*n-1)*tw:][:tw]
		de := buf[2*n*tw:][:tw]
		for x := range de {
			h := int(hp[x])
			e := int16(int(ln[x]) - ((h + int(hn[x]) + 1) >> 1))
			de[x] = e
			do[x] = int16(h<<1 + (int(dp[x])+int(e))>>1)
		}
	}
	{
		hl := tmp[(2*sw-1)*tw:][:tw]
		dp := buf[(tw-2)*tw:][:tw]
		dl := buf[(tw-1)*tw:][:tw]
		for x := range dl {
			dl[x] = int16(int(hl[x])<<1 + int(dp[x]))
		}
	}
}

func inverseDWTExtrapolate(buf, tmp *[tileCoefficients]int16) {
	inverseDWTExtrapolateLevel(buf[extrapolateLayout.offset[bandLevel3]:], tmp[:], 9, 8)
	inverseDWTExtrapolateLevel(buf[extrapolateLayout.offset[bandLevel2]:], tmp[:], 17, 16)
	inverseDWTExtrapolateLevel(buf[:], tmp[:], 33, 31)
}

func clamp16(v int) int16 {
	return int16(min(max(v, -32768), 32767))
}

func inverseDWTExtrapolateLevel(buf, tmp []int16, nl, nh int) {
	w := nl + nh
	hl := buf[:nh*nl]
	lh := buf[nh*nl:][:nl*nh]
	hh := buf[2*nh*nl:][:nh*nh]
	ll := buf[2*nh*nl+nh*nh:][:nl*nl]
	lo := tmp[:nl*w]
	hi := tmp[nl*w:][:nh*w]
	for i := range nl {
		extrapolateRow(ll[i*nl:][:nl], hl[i*nh:][:nh], lo[i*w:][:w])
	}
	for i := range nh {
		extrapolateRow(lh[i*nl:][:nl], hh[i*nh:][:nh], hi[i*w:][:w])
	}
	out := buf[:w*w]
	{
		l0 := lo[:w]
		h0 := hi[:w]
		e0 := out[:w]
		for x := range e0 {
			e0[x] = clamp16(int(l0[x]) - int(h0[x]))
		}
	}
	for j := 1; j < nh; j++ {
		lj := lo[j*w:][:w]
		hp := hi[(j-1)*w:][:w]
		hj := hi[j*w:][:w]
		ep := out[(2*j-2)*w:][:w]
		op := out[(2*j-1)*w:][:w]
		ej := out[2*j*w:][:w]
		for x := range ej {
			h := int(hp[x])
			e := clamp16(int(lj[x]) - (h+int(hj[x]))/2)
			ej[x] = e
			op[x] = clamp16((int(ep[x])+int(e))/2 + 2*h)
		}
	}
	ep := out[(2*nh-2)*w:][:w]
	op := out[(2*nh-1)*w:][:w]
	en := out[2*nh*w:][:w]
	hp := hi[(nh-1)*w:][:w]
	ln := lo[nh*w:][:w]
	if nl == nh+1 {
		for x := range en {
			h := int(hp[x])
			e := clamp16(int(ln[x]) - h)
			en[x] = e
			op[x] = clamp16((int(ep[x])+int(e))/2 + 2*h)
		}
		return
	}
	lt := lo[(nh+1)*w:][:w]
	last := out[(2*nh+1)*w:][:w]
	for x := range en {
		h := int(hp[x])
		e := clamp16(int(ln[x]) - h/2)
		en[x] = e
		op[x] = clamp16((int(ep[x])+int(e))/2 + 2*h)
		last[x] = clamp16((int(e) + int(lt[x])) / 2)
	}
}

func extrapolateRow(l, h, out []int16) {
	nl, nh := len(l), len(h)
	out = out[:nl+nh]
	h0 := int(h[0])
	e := clamp16(int(l[0]) - h0)
	out[0] = e
	for j := 1; j < nh; j++ {
		h1 := int(h[j])
		en := clamp16(int(l[j]) - (h0+h1)/2)
		out[2*j-1] = clamp16((int(e)+int(en))/2 + 2*h0)
		out[2*j] = en
		e = en
		h0 = h1
	}
	if nl == nh+1 {
		en := clamp16(int(l[nh]) - h0)
		out[2*nh-1] = clamp16((int(e)+int(en))/2 + 2*h0)
		out[2*nh] = en
		return
	}
	en := clamp16(int(l[nh]) - h0/2)
	out[2*nh-1] = clamp16((int(e)+int(en))/2 + 2*h0)
	out[2*nh] = en
	out[2*nh+1] = clamp16((int(en) + int(l[nh+1])) / 2)
}
