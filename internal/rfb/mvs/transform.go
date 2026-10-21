// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rfb/mvs/transform.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package mvs

var defaultLuminanceQuantization = [64]uint8{
	0x10, 0x0b, 0x0b, 0x0e, 0x18, 0x16, 0x18, 0x21, 0x0b, 0x0c, 0x0b, 0x0b, 0x1a, 0x12, 0x13, 0x17,
	0x0d, 0x0d, 0x0e, 0x18, 0x14, 0x15, 0x16, 0x1c, 0x0d, 0x11, 0x0e, 0x15, 0x17, 0x19, 0x22, 0x22,
	0x11, 0x16, 0x12, 0x19, 0x1f, 0x20, 0x22, 0x2c, 0x18, 0x14, 0x11, 0x1b, 0x23, 0x28, 0x2c, 0x36,
	0x1e, 0x23, 0x1c, 0x1e, 0x22, 0x2c, 0x36, 0x40, 0x23, 0x1e, 0x21, 0x26, 0x2d, 0x37, 0x41, 0x4b,
}

var defaultChrominanceQuantization = [64]uint8{
	0x13, 0x13, 0x18, 0x2f, 0x4c, 0x63, 0x63, 0x63, 0x13, 0x15, 0x1a, 0x42, 0x63, 0x63, 0x63, 0x63,
	0x18, 0x1a, 0x38, 0x63, 0x63, 0x63, 0x63, 0x63, 0x2f, 0x42, 0x63, 0x63, 0x63, 0x63, 0x63, 0x63,
	0x4c, 0x63, 0x63, 0x63, 0x63, 0x63, 0x63, 0x63, 0x63, 0x63, 0x63, 0x63, 0x63, 0x63, 0x63, 0x63,
	0x63, 0x63, 0x63, 0x63, 0x63, 0x63, 0x63, 0x63, 0x63, 0x63, 0x63, 0x63, 0x63, 0x63, 0x63, 0x63,
}

var zigzag = [64]uint8{
	0, 1, 8, 16, 9, 2, 3, 10, 17, 24, 32, 25, 18, 11, 4, 5, 12, 19, 26, 33, 40, 48, 41, 34, 27, 20,
	13, 6, 7, 14, 21, 28, 35, 42, 49, 56, 57, 50, 43, 36, 29, 22, 15, 23, 30, 37, 44, 51, 58, 59,
	52, 45, 38, 31, 39, 46, 53, 60, 61, 54, 47, 55, 62, 63,
}

const (
	fix0298631336 = 2446
	fix0390180644 = 3196
	fix0541196100 = 4433
	fix0765366865 = 6270
	fix0899976223 = 7373
	fix1175875602 = 9633
	fix1501321110 = 12299
	fix1847759065 = 15137
	fix1961570560 = 16069
	fix2053119869 = 16819
	fix2562915447 = 20995
	fix3072711026 = 25172
)

type quantizationTable [64]int32

type block [64]int16

type samples [64]uint8

func clampSample(value int32) uint8 {
	return uint8(min(max(value, 0), 255))
}

// ScreenSharing installs jpeg_idct_islow as its MVS IDCT and calls it for Y, Cb and Cr
// (ScreenSharing 776.27, macOS 27.0.1 26A434, UUID EC0B2C20-F816-3FCE-822D-F9403C09EB50,
// _InitializeIDCT 0x1cc06b16c..0x1cc06b178, _PerformInverseDCT8By8 0x1cc06c150..0x1cc06c194;
// ScreenSharing 786.3, macOS 27.2 26B5101f, UUID 21DF7A41-0D38-3DF3-B87A-8A6249686735,
// _InitializeIDCT 0x22e4ae1f0..0x22e4ae1fc, _PerformInverseDCT8By8 0x22e4af1d4..0x22e4af218).
func inverseDCT(coefficients *block, quantization *quantizationTable, output *samples) {
	var workspace [64]int32
	uniform := true
	for column := 0; column < 8; column++ {
		input0 := int32(coefficients[column]) * quantization[column]
		if coefficients[8+column]|coefficients[16+column]|coefficients[24+column]|coefficients[32+column]|
			coefficients[40+column]|coefficients[48+column]|coefficients[56+column] == 0 {
			value := input0 << 2
			workspace[column] = value
			workspace[8+column] = value
			workspace[16+column] = value
			workspace[24+column] = value
			workspace[32+column] = value
			workspace[40+column] = value
			workspace[48+column] = value
			workspace[56+column] = value
			continue
		}
		input1 := int32(coefficients[8+column]) * quantization[8+column]
		input2 := int32(coefficients[16+column]) * quantization[16+column]
		input3 := int32(coefficients[24+column]) * quantization[24+column]
		input4 := int32(coefficients[32+column]) * quantization[32+column]
		input5 := int32(coefficients[40+column]) * quantization[40+column]
		input6 := int32(coefficients[48+column]) * quantization[48+column]
		input7 := int32(coefficients[56+column]) * quantization[56+column]
		if input1|input2|input3|input4|input5|input6|input7 == 0 {
			value := input0 << 2
			workspace[column] = value
			workspace[8+column] = value
			workspace[16+column] = value
			workspace[24+column] = value
			workspace[32+column] = value
			workspace[40+column] = value
			workspace[48+column] = value
			workspace[56+column] = value
			continue
		}
		uniform = false
		output0, output1, output2, output3, output4, output5, output6, output7 := idctPass(input0, input1, input2, input3, input4, input5, input6, input7)
		workspace[column] = (output0 + 1024) >> 11
		workspace[8+column] = (output1 + 1024) >> 11
		workspace[16+column] = (output2 + 1024) >> 11
		workspace[24+column] = (output3 + 1024) >> 11
		workspace[32+column] = (output4 + 1024) >> 11
		workspace[40+column] = (output5 + 1024) >> 11
		workspace[48+column] = (output6 + 1024) >> 11
		workspace[56+column] = (output7 + 1024) >> 11
	}
	rows := 64
	if uniform {
		rows = 8
	}
	for row := 0; row < rows; row += 8 {
		line := (*[8]int32)(workspace[row : row+8])
		target := (*[8]uint8)(output[row : row+8])
		if line[1]|line[2]|line[3]|line[4]|line[5]|line[6]|line[7] == 0 {
			value := clampSample((line[0]+16)>>5 + 128)
			for i := range target {
				target[i] = value
			}
			continue
		}
		output0, output1, output2, output3, output4, output5, output6, output7 := idctPass(line[0], line[1], line[2], line[3], line[4], line[5], line[6], line[7])
		target[0] = clampSample((output0+131072)>>18 + 128)
		target[1] = clampSample((output1+131072)>>18 + 128)
		target[2] = clampSample((output2+131072)>>18 + 128)
		target[3] = clampSample((output3+131072)>>18 + 128)
		target[4] = clampSample((output4+131072)>>18 + 128)
		target[5] = clampSample((output5+131072)>>18 + 128)
		target[6] = clampSample((output6+131072)>>18 + 128)
		target[7] = clampSample((output7+131072)>>18 + 128)
	}
	if uniform {
		first := (*[8]uint8)(output[:8])
		for row := 8; row < 64; row += 8 {
			*(*[8]uint8)(output[row : row+8]) = *first
		}
	}
}

func idctPass(input0, input1, input2, input3, input4, input5, input6, input7 int32) (int32, int32, int32, int32, int32, int32, int32, int32) {
	even := (input2 + input6) * fix0541196100
	even2 := even - input6*fix1847759065
	even3 := even + input2*fix0765366865
	even0 := (input0 + input4) << 13
	even1 := (input0 - input4) << 13
	sum10 := even0 + even3
	sum13 := even0 - even3
	sum11 := even1 + even2
	sum12 := even1 - even2

	odd0 := input7
	odd1 := input5
	odd2 := input3
	odd3 := input1
	z1 := odd0 + odd3
	z2 := odd1 + odd2
	z3 := odd0 + odd2
	z4 := odd1 + odd3
	z5 := (z3 + z4) * fix1175875602
	odd0 *= fix0298631336
	odd1 *= fix2053119869
	odd2 *= fix3072711026
	odd3 *= fix1501321110
	z1 *= -fix0899976223
	z2 *= -fix2562915447
	z3 = z3*-fix1961570560 + z5
	z4 = z4*-fix0390180644 + z5
	odd0 += z1 + z3
	odd1 += z2 + z4
	odd2 += z2 + z3
	odd3 += z1 + z4

	return sum10 + odd3, sum11 + odd2, sum12 + odd1, sum13 + odd0,
		sum13 - odd0, sum12 - odd1, sum11 - odd2, sum10 - odd3
}

var (
	redFromCr   [256]int32
	blueFromCb  [256]int32
	greenFromCb [256]int32
	greenFromCr [256]int32
)

func init() {
	for i := range 256 {
		chroma := int32(i) - 128
		redFromCr[i] = (91881*chroma + 32768) >> 16
		blueFromCb[i] = (116130*chroma + 32768) >> 16
		greenFromCb[i] = -22554 * chroma
		greenFromCr[i] = -46802*chroma + 32768
	}
}

func pixelFromYCbCr(luma, cb, cr uint8) uint32 {
	y := int32(luma)
	red := uint32(clampSample(y + redFromCr[cr]))
	green := uint32(clampSample(y + (greenFromCb[cb]+greenFromCr[cr])>>16))
	blue := uint32(clampSample(y + blueFromCb[cb]))
	return blue | green<<8 | red<<16 | 0xff000000
}

func pixelFromRGB(red, green, blue uint8) uint32 {
	return uint32(blue) | uint32(green)<<8 | uint32(red)<<16 | 0xff000000
}

func dcSample(coefficient int16, quantization int32) uint8 {
	return clampSample((int32(coefficient)*quantization+4)>>3 + 128)
}
