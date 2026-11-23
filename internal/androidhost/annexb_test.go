package androidhost

import (
	"bytes"
	"testing"
)

func TestSplitParameterSets(t *testing.T) {
	h264 := []byte{
		0, 0, 0, 1, 0x09, 0xF0, // AUD (dropped)
		0, 0, 0, 1, 0x67, 1, 2, 3, // SPS (7)
		0, 0, 0, 1, 0x68, 4, 5, // PPS (8)
		0, 0, 0, 1, 0x65, 9, 9, 9, // IDR (5)
	}
	cfg, rest := SplitParameterSets(h264, false)
	wantCfg := []byte{0, 0, 0, 1, 0x67, 1, 2, 3, 0, 0, 0, 1, 0x68, 4, 5}
	wantRest := []byte{0, 0, 0, 1, 0x65, 9, 9, 9}
	if !bytes.Equal(cfg, wantCfg) || !bytes.Equal(rest, wantRest) {
		t.Fatalf("h264 got cfg=%x rest=%x", cfg, rest)
	}

	hevc := []byte{
		0, 0, 0, 1, 35 << 1, 0x01, 0x80, // AUD (35, dropped)
		0, 0, 0, 1, 32 << 1, 0x01, 0xAA, // VPS (32)
		0, 0, 0, 1, 33 << 1, 0x01, 0xBB, // SPS (33)
		0, 0, 0, 1, 34 << 1, 0x01, 0xCC, // PPS (34)
		0, 0, 0, 1, 19 << 1, 0x01, 0xDD, // IDR_W_RADL (19)
	}
	cfg, rest = SplitParameterSets(hevc, true)
	wantH := []byte{
		0, 0, 0, 1, 32 << 1, 0x01, 0xAA,
		0, 0, 0, 1, 33 << 1, 0x01, 0xBB,
		0, 0, 0, 1, 34 << 1, 0x01, 0xCC,
	}
	wantHR := []byte{0, 0, 0, 1, 19 << 1, 0x01, 0xDD}
	if !bytes.Equal(cfg, wantH) || !bytes.Equal(rest, wantHR) {
		t.Fatalf("hevc got cfg=%x rest=%x", cfg, rest)
	}

	// VA-API 3-byte 00 00 01 start codes are normalized to 4-byte 00 00 00 01.
	vaapiH264 := []byte{
		0, 0, 1, 0x67, 0x42, 0x80,
		0, 0, 1, 0x68, 0xCE,
		0, 0, 1, 0x65, 0x88,
	}
	sps, pps, tail := SplitH264ParameterSets(vaapiH264)
	if !bytes.Equal(sps, []byte{0, 0, 0, 1, 0x67, 0x42, 0x80}) ||
		!bytes.Equal(pps, []byte{0, 0, 0, 1, 0x68, 0xCE}) ||
		!bytes.Equal(tail, []byte{0, 0, 0, 1, 0x65, 0x88}) {
		t.Fatalf("3-byte start code split got sps=%x pps=%x tail=%x", sps, pps, tail)
	}
}
