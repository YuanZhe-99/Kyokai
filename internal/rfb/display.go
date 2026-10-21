// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rfb/display.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package rfb

import (
	"encoding/binary"
	"fmt"
	"math"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const AllDisplays uint32 = 0xffffffff

type DisplayLayout struct {
	Version           int
	DesktopWidth      int
	DesktopHeight     int
	FramebufferWidth  int
	FramebufferHeight int
	Current           uint32
	SessionState      uint32
	Displays          []Display
}

type Display struct {
	ID      uint32
	Density float64
	Scale   float64
	Logical Rectangle
	Backing Rectangle
	Flags   uint32
}

const (
	DisplayFlagMain       uint32 = 0x01
	DisplayFlagMirrored   uint32 = 0x02
	DisplayFlagVirtual    uint32 = 0x04
	layoutHeaderSize             = 0x14
	layoutRecordSize             = 0x38
	displayInfoHeaderSize        = 10
	displayInfoRecordSize        = 0x1c
	maximumLayoutDisplays        = 25
)

func parseDisplayLayout(payload []byte) (*DisplayLayout, error) {
	if len(payload) < layoutHeaderSize {
		return nil, E.New("rfb: display layout of ", len(payload), " bytes, want at least ", layoutHeaderSize)
	}
	count := int(binary.BigEndian.Uint16(payload[0x12:]))
	if count > maximumLayoutDisplays || len(payload) < layoutHeaderSize+count*layoutRecordSize {
		return nil, E.New("rfb: display layout lists ", count, " displays in ", len(payload), " bytes; they need ", layoutHeaderSize+count*layoutRecordSize, " bytes, and the limit is ", maximumLayoutDisplays, " displays")
	}
	layout := &DisplayLayout{
		Version:           int(binary.BigEndian.Uint16(payload[0:])),
		DesktopWidth:      int(binary.BigEndian.Uint16(payload[2:])),
		DesktopHeight:     int(binary.BigEndian.Uint16(payload[4:])),
		FramebufferWidth:  int(binary.BigEndian.Uint16(payload[6:])),
		FramebufferHeight: int(binary.BigEndian.Uint16(payload[8:])),
		Current:           binary.BigEndian.Uint32(payload[0x0a:]),
		SessionState:      binary.BigEndian.Uint32(payload[0x0e:]),
	}
	edges := func(record []byte) Rectangle {
		top := int(int16(binary.BigEndian.Uint16(record[0:])))
		left := int(int16(binary.BigEndian.Uint16(record[2:])))
		bottom := int(int16(binary.BigEndian.Uint16(record[4:])))
		right := int(int16(binary.BigEndian.Uint16(record[6:])))
		return Rectangle{X: left, Y: top, Width: right - left, Height: bottom - top}
	}
	for i := 0; i < count; i++ {
		record := payload[layoutHeaderSize+i*layoutRecordSize:]
		display := Display{
			Density: math.Float64frombits(binary.BigEndian.Uint64(record[0x00:])),
			Scale:   math.Float64frombits(binary.BigEndian.Uint64(record[0x08:])),
			ID:      binary.BigEndian.Uint32(record[0x10:]),
			Logical: edges(record[0x14:]),
			Backing: edges(record[0x1c:]),
			Flags:   binary.BigEndian.Uint32(record[0x24:]),
		}
		if !(display.Scale > 0 && display.Scale <= 1) {
			display.Scale = 1
		}
		if display.Density == 0 && display.Logical.Width > 0 {
			display.Density = float64(display.Backing.Width) / float64(display.Logical.Width) / display.Scale
		}
		layout.Displays = append(layout.Displays, display)
	}
	return layout, nil
}

func (c *Client) receiveDisplayLayout() error {
	length, err := c.stream.u16()
	if err != nil {
		return err
	}
	payload, err := c.stream.bytes(int(length))
	if err != nil {
		return err
	}
	layout, err := parseDisplayLayout(payload)
	if err != nil {
		return err
	}
	c.logger.Debug("display layout", "framebuffer", fmt.Sprint(layout.FramebufferWidth, "x", layout.FramebufferHeight),
		"current", layout.Current, "state", fmt.Sprintf("%#x", layout.SessionState), "displays", len(layout.Displays))
	scale := 1.0
	if len(layout.Displays) > 0 {
		scale = layout.Displays[0].Scale
	}
	c.access.Lock()
	c.layout = layout
	c.scale = scale
	c.access.Unlock()
	if layout.FramebufferWidth > 0 && layout.FramebufferHeight > 0 {
		_, err = c.resizeLocked(layout.FramebufferWidth, layout.FramebufferHeight)
		if err != nil {
			return err
		}
	}
	c.handler.DisplaysChanged(layout)
	if !c.running {
		return nil
	}
	c.access.Lock()
	sharedClipboard := c.sharedClipboard
	c.access.Unlock()
	if sharedClipboard {
		err = c.send(autoPasteboardMessage(true))
		if err != nil {
			return err
		}
	}
	return c.rearm(c.width, c.height)
}

func (c *Client) receiveDisplayInfo(rectangle Rectangle) error {
	header, err := c.stream.bytes(displayInfoHeaderSize)
	if err != nil {
		return err
	}
	count := int(binary.BigEndian.Uint16(header[8:]))
	err = c.stream.discard(count * displayInfoRecordSize)
	if err != nil {
		return err
	}
	if rectangle.Width > 0 && rectangle.Height > 0 {
		return c.resizeAndRefresh(rectangle.Width, rectangle.Height)
	}
	return nil
}
