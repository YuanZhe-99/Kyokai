// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/codec/h264/annexb.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package h264

import (
	"bytes"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const (
	nalSlice  = 1
	nalIDR    = 5
	nalSPS    = 7
	nalPPS    = 8
	nalAUD    = 9
	nalFiller = 12
)

var startCode = []byte{0, 0, 1}

func splitAnnexB(data []byte, units [][]byte) ([][]byte, error) {
	units = units[:0]
	if len(bytes.Trim(data, "\x00")) == 0 {
		return units, nil
	}
	start := bytes.Index(data, startCode)
	if start < 0 || len(bytes.TrimLeft(data[:start], "\x00")) > 0 {
		return nil, E.New("h264: bitstream does not start with a start code")
	}
	data = data[start+3:]
	for {
		end := bytes.Index(data, startCode)
		unit := data
		if end >= 0 {
			unit = data[:end]
		}
		unit = bytes.TrimRight(unit, "\x00")
		if len(unit) > 0 {
			if unit[0]&0x80 != 0 {
				return nil, E.New("h264: forbidden_zero_bit set")
			}
			units = append(units, unit)
		}
		if end < 0 {
			return units, nil
		}
		data = data[end+3:]
	}
}
