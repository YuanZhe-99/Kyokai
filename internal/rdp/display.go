// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/display.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package rdp

import (
	"encoding/binary"
	"log/slog"
	"math"
	"sync"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const DisplayWidthMultiple = 2

const (
	displayControlCapabilities = 5
	displayControlLayout       = 2
	displayControlHeaderLength = 8
	displayMonitorLength       = 40
	displayMonitorPrimary      = 1
	displayCapabilitiesLength  = 20
)

func normalizeLayout(width, height, scale int) (int, int, int) {
	width = max(200, min(width, maxGraphicsDimension))
	height = max(200, min(height, maxGraphicsDimension))
	if width*height > maxGraphicsPixels {
		ratio := math.Sqrt(float64(maxGraphicsPixels) / float64(width*height))
		width, height = int(float64(width)*ratio), int(float64(height)*ratio)
	}
	return width - width%DisplayWidthMultiple, height, max(100, min(scale, 500))
}

type displayChannel struct {
	logger  *slog.Logger
	changed func()

	access      sync.Mutex
	send        func([]byte) error
	maxMonitors uint32
	maxArea     uint64
	width       int
	height      int
	scale       int
	sentScale   int
	pending     bool
	logonSeen   bool
	beforeLogon bool
}

func (d *displayChannel) OnOpen(send func([]byte) error) error {
	d.access.Lock()
	d.send = send
	d.access.Unlock()
	d.changed()
	return nil
}

func (d *displayChannel) OnClose() {
	d.access.Lock()
	d.send = nil
	d.maxMonitors, d.maxArea = 0, 0
	d.access.Unlock()
	d.changed()
}

func (d *displayChannel) OnData(data []byte) error {
	r := &reader{data: data}
	kind, size := r.u32(), r.u32()
	monitors, a, b := r.u32(), r.u32(), r.u32()
	if r.err != nil || kind != displayControlCapabilities || size < displayCapabilitiesLength {
		return nil
	}
	if monitors == 0 || a == 0 || b == 0 {
		d.logger.Debug("ignore display control capabilities", "monitors", monitors, "a", a, "b", b)
		return nil
	}
	d.access.Lock()
	d.maxMonitors, d.maxArea = monitors, uint64(monitors)*uint64(a)*uint64(b)
	err := d.flush()
	d.access.Unlock()
	if err != nil {
		d.logger.Debug("send display layout", "error", err)
	}
	d.changed()
	return nil
}

func (d *displayChannel) enabled() bool {
	d.access.Lock()
	defer d.access.Unlock()
	return d.send != nil && d.maxMonitors > 0
}

func (d *displayChannel) layoutScale() int {
	d.access.Lock()
	defer d.access.Unlock()
	return d.sentScale
}

func (d *displayChannel) set(width, height, scale int) error {
	d.access.Lock()
	defer d.access.Unlock()
	if !d.pending && width == d.width && height == d.height && scale == d.scale {
		return nil
	}
	if d.maxMonitors > 0 && uint64(width)*uint64(height) > d.maxArea {
		return E.New("rdp: the server does not accept a ", width, "x", height, " display, over its ", d.maxArea, "-pixel limit")
	}
	d.width, d.height, d.scale, d.pending = width, height, scale, true
	return d.flush()
}

// Some Windows sessions negotiate Display Control before logon completes and discard layouts sent
// in that interval.
func (d *displayChannel) loggedOn() {
	d.access.Lock()
	defer d.access.Unlock()
	d.logonSeen = true
	if !d.beforeLogon {
		return
	}
	d.beforeLogon, d.pending = false, true
	err := d.flush()
	if err != nil {
		d.logger.Debug("resend display layout", "error", err)
	}
}

func (d *displayChannel) flush() error {
	if !d.pending || d.send == nil || d.maxMonitors == 0 {
		return nil
	}
	if uint64(d.width)*uint64(d.height) > d.maxArea {
		d.pending = false
		return E.New("rdp: the server does not accept a ", d.width, "x", d.height, " display, over its ", d.maxArea, "-pixel limit")
	}
	data := binary.LittleEndian.AppendUint32(nil, displayControlLayout)
	data = binary.LittleEndian.AppendUint32(data, displayControlHeaderLength+8+displayMonitorLength)
	data = binary.LittleEndian.AppendUint32(data, displayMonitorLength)
	data = binary.LittleEndian.AppendUint32(data, 1)
	data = binary.LittleEndian.AppendUint32(data, displayMonitorPrimary)
	data = binary.LittleEndian.AppendUint32(data, 0)
	data = binary.LittleEndian.AppendUint32(data, 0)
	data = binary.LittleEndian.AppendUint32(data, uint32(d.width))
	data = binary.LittleEndian.AppendUint32(data, uint32(d.height))
	data = binary.LittleEndian.AppendUint32(data, 0)
	data = binary.LittleEndian.AppendUint32(data, 0)
	data = binary.LittleEndian.AppendUint32(data, 0)
	data = binary.LittleEndian.AppendUint32(data, uint32(d.scale))
	data = binary.LittleEndian.AppendUint32(data, deviceScaleFactor)
	err := d.send(data)
	if err != nil {
		return err
	}
	d.pending = false
	d.sentScale = d.scale
	d.beforeLogon = !d.logonSeen
	return nil
}

func (c *Client) SetDisplay(width, height, scale int) error {
	width, height, scale = normalizeLayout(width, height, scale)
	err := c.display.set(width, height, scale)
	c.refreshInfo()
	return err
}
