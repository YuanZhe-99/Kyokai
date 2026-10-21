// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/codec/rfx/pool.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package rfx

import (
	"runtime"
	"sync"
	"sync/atomic"
)

const tilesPerWorker = 2

type workerPool struct {
	scratch []*scratch
}

func (p *workerPool) get(i int) *scratch {
	for len(p.scratch) <= i {
		p.scratch = append(p.scratch, new(scratch))
	}
	return p.scratch[i]
}

func (p *workerPool) run(count int, sequential bool, work func(s *scratch, i int)) {
	workers := min(runtime.GOMAXPROCS(0), count/tilesPerWorker)
	if sequential || workers <= 1 {
		s := p.get(0)
		for i := range count {
			work(s, i)
		}
		return
	}
	var (
		next  atomic.Int64
		group sync.WaitGroup
	)
	loop := func(s *scratch) {
		for {
			i := int(next.Add(1) - 1)
			if i >= count {
				return
			}
			work(s, i)
		}
	}
	for w := 1; w < workers; w++ {
		s := p.get(w)
		group.Go(func() { loop(s) })
	}
	loop(p.get(0))
	group.Wait()
}
