package androidhost

import "bytes"

var startCode4 = []byte{0, 0, 0, 1}

// SplitParameterSets splits an Annex B key frame into its parameter sets
// (H.264 SPS/PPS NAL 7, 8; HEVC VPS/SPS/PPS NAL 32–34) and the remaining
// picture slices, normalizing every NAL start code to 00 00 00 01 and omitting
// access-unit delimiters (H.264 NAL 9, HEVC NAL 35).
func SplitParameterSets(data []byte, hevc bool) (config, rest []byte) {
	if !hevc {
		sps, pps, tail := SplitH264ParameterSets(data)
		if len(sps) == 0 && len(pps) == 0 {
			return nil, tail
		}
		cfg := make([]byte, 0, len(sps)+len(pps))
		cfg = append(cfg, sps...)
		cfg = append(cfg, pps...)
		return cfg, tail
	}
	spans := scanAnnexB(data)
	if len(spans) == 0 {
		return nil, data
	}
	var cfgBuf, restBuf bytes.Buffer
	for _, s := range spans {
		nalType := (data[s.nal] >> 1) & 0x3F
		switch {
		case nalType == 35:
			continue
		case nalType >= 32 && nalType <= 34:
			cfgBuf.Write(startCode4)
			cfgBuf.Write(data[s.nal:s.end])
		default:
			restBuf.Write(startCode4)
			restBuf.Write(data[s.nal:s.end])
		}
	}
	return cfgBuf.Bytes(), restBuf.Bytes()
}

// SplitH264ParameterSets extracts H.264 SPS (NAL 7), PPS (NAL 8) and the
// remaining slice NAL units, each normalized to a 4-byte 00 00 00 01 start
// code, omitting access-unit delimiters (NAL 9).
func SplitH264ParameterSets(data []byte) (sps, pps, rest []byte) {
	spans := scanAnnexB(data)
	if len(spans) == 0 {
		return nil, nil, data
	}
	var spsBuf, ppsBuf, restBuf bytes.Buffer
	for _, s := range spans {
		nalType := data[s.nal] & 0x1F
		switch nalType {
		case 9:
			continue
		case 7:
			spsBuf.Write(startCode4)
			spsBuf.Write(data[s.nal:s.end])
		case 8:
			ppsBuf.Write(startCode4)
			ppsBuf.Write(data[s.nal:s.end])
		default:
			restBuf.Write(startCode4)
			restBuf.Write(data[s.nal:s.end])
		}
	}
	return spsBuf.Bytes(), ppsBuf.Bytes(), restBuf.Bytes()
}

type nalSpan struct {
	nal int
	end int
}

func scanAnnexB(data []byte) []nalSpan {
	type mark struct{ start, nal int }
	var marks []mark
	for i := 0; ; {
		s, n, ok := nextStartCode(data, i)
		if !ok {
			break
		}
		marks = append(marks, mark{s, n})
		i = n
	}
	if len(marks) == 0 {
		return nil
	}
	out := make([]nalSpan, 0, len(marks))
	for k, m := range marks {
		end := len(data)
		if k+1 < len(marks) {
			end = marks[k+1].start
		}
		if m.nal < end {
			out = append(out, nalSpan{nal: m.nal, end: end})
		}
	}
	return out
}

func nextStartCode(d []byte, from int) (start, nal int, ok bool) {
	for i := from; i+3 <= len(d); i++ {
		if d[i] == 0 && d[i+1] == 0 {
			if d[i+2] == 1 {
				return i, i + 3, true
			}
			if i+4 <= len(d) && d[i+2] == 0 && d[i+3] == 1 {
				return i, i + 4, true
			}
		}
	}
	return 0, 0, false
}
