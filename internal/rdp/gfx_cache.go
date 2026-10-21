// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/gfx_cache.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: reduced to the in-memory cache; the on-disk cache (zstd) removed.

package rdp

import "github.com/YuanZhe-99/Kyokai/internal/bgra"

type gfxCacheEntry struct {
	image  *bgra.Image
	format byte
	// rdpserverbase.dll of Windows 11 26200 derives the key from the pixels, size and format of the
	// bitmap and the codec settings of the session, with no per-session secret, so it stays valid
	// across sessions to the same server.
	key  uint64
	used uint64
}

func (p *rdpgfx) storeCache(slot uint16, entry *gfxCacheEntry) error {
	p.evictCache(slot)
	err := p.cacheMemory.reserve(len(entry.image.Pix))
	if err != nil {
		return err
	}
	p.cache[slot] = entry
	return nil
}

func (p *rdpgfx) evictCache(slot uint16) {
	if cached, found := p.cache[slot]; found {
		p.cacheMemory.release(len(cached.image.Pix))
		delete(p.cache, slot)
	}
}
