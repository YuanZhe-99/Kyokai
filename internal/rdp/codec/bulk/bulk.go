// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/codec/bulk/bulk.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package bulk

import (
	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const (
	type8K    = 0x0
	type64K   = 0x1
	typeRDP6  = 0x2
	typeRDP61 = 0x3

	typeMask         = 0x0F
	flagsMask        = 0xE0
	packetCompressed = 0x20
	packetAtFront    = 0x40
	packetFlushed    = 0x80
)

type Decompressor struct {
	mppc   *mppcDecompressor
	ncrush *ncrushDecompressor
	xcrush *xcrushDecompressor
}

func (d *Decompressor) Decompress(data []byte, flags uint8) ([]byte, error) {
	if flags&flagsMask == 0 {
		return data, nil
	}
	switch flags & typeMask {
	case type8K, type64K:
		if d.mppc == nil {
			d.mppc = new(mppcDecompressor)
		}
		return d.mppc.decompress(data, flags, flags&typeMask == type64K)
	case typeRDP6:
		if d.ncrush == nil {
			d.ncrush = new(ncrushDecompressor)
		}
		return d.ncrush.decompress(data, flags)
	case typeRDP61:
		if d.xcrush == nil {
			d.xcrush = new(xcrushDecompressor)
		}
		return d.xcrush.decompress(data, flags)
	default:
		return nil, E.New("bulk: unsupported compression type ", flags&typeMask)
	}
}
