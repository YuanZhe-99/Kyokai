// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rfb/zlib.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package rfb

import (
	"bytes"
	"compress/flate"
	"encoding/hex"
	"errors"
	"io"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const zlibWindow = 32 * 1024

type zlibStream struct {
	started  bool
	inflater io.ReadCloser
	history  []byte
	output   []byte
	input    bytes.Reader
}

func (z *zlibStream) inflate(compressed []byte, limit int) ([]byte, error) {
	if !z.started {
		if len(compressed) < 2 {
			return nil, E.New("rfb: zlib stream of ", len(compressed), " bytes has no header")
		}
		header := uint16(compressed[0])<<8 | uint16(compressed[1])
		if compressed[0]&0x0f != 8 || header%31 != 0 || compressed[1]&0x20 != 0 {
			return nil, E.New("rfb: zlib stream header ", hex.EncodeToString(compressed[:2]))
		}
		compressed = compressed[2:]
		z.started = true
	}
	z.input.Reset(compressed)
	if z.inflater == nil {
		z.inflater = flate.NewReaderDict(&z.input, z.history)
	} else {
		err := z.inflater.(flate.Resetter).Reset(&z.input, z.history)
		if err != nil {
			return nil, err
		}
	}
	z.output = z.output[:0]
	for {
		if len(z.output) == cap(z.output) {
			z.output = append(z.output, make([]byte, max(64*1024, len(z.output)))...)[:len(z.output)]
		}
		n, err := z.inflater.Read(z.output[len(z.output):cap(z.output)])
		z.output = z.output[:len(z.output)+n]
		if len(z.output) > limit {
			return nil, E.New("rfb: rectangle inflates past ", limit, " bytes")
		}
		if err == nil {
			continue
		}
		if E.IsMulti(err, io.ErrUnexpectedEOF, io.EOF) && z.input.Len() == 0 {
			break
		}
		return nil, err
	}
	if len(z.output) >= zlibWindow {
		z.history = append(z.history[:0], z.output[len(z.output)-zlibWindow:]...)
	} else {
		keep := min(len(z.history), zlibWindow-len(z.output))
		copy(z.history, z.history[len(z.history)-keep:])
		z.history = append(z.history[:keep], z.output...)
	}
	return z.output, nil
}

func inflateIndependent(compressed []byte, limit int) ([]byte, error) {
	if len(compressed) < 2 {
		return nil, E.New("zlib data of ", len(compressed), " bytes has no header")
	}
	inflater := flate.NewReader(bytes.NewReader(compressed[2:]))
	defer inflater.Close()
	var output bytes.Buffer
	_, err := io.Copy(&output, io.LimitReader(inflater, int64(limit)+1))
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	if output.Len() > limit {
		return nil, E.New("zlib data inflates past ", limit, " bytes")
	}
	return output.Bytes(), nil
}
