// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/channels.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: standard-library helpers in place of sing, import paths.

package rdp

import (
	"encoding/binary"
	"errors"
	"log/slog"
	"slices"
	"sync/atomic"

	"github.com/YuanZhe-99/Kyokai/internal/rdp/codec/bulk"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const (
	channelOptionInitialized  = 0x80000000
	channelOptionCompressRDP  = 0x00800000
	channelOptionShowProtocol = 0x00200000

	channelFlagFirst        = 0x00000001
	channelFlagLast         = 0x00000002
	channelFlagShowProtocol = 0x00000010
	channelFlagSuspend      = 0x00000020
	channelFlagResume       = 0x00000040
	channelCompressionShift = 16

	channelChunkLength = 1600
	maxChannelMessage  = 64 << 20
	maxChannelSend     = 16 << 20
)

var errRequiredChannel = E.New("rdp: required virtual channel failed")

type channelHandler interface {
	OnOpen(send func([]byte) error) error
	OnData([]byte) error
	OnClose()
}

type channelFragment struct {
	total uint32
	data  []byte
}

type staticChannel struct {
	name     string
	options  uint32
	handler  channelHandler
	id       uint16
	fragment *channelFragment
	closed   atomic.Bool
}

type channelRegistry struct {
	mcs          *mcsLayer
	logger       *slog.Logger
	channels     []*staticChannel
	decompressor *bulk.Decompressor
}

func (r *channelRegistry) open() {
	for _, channel := range r.channels {
		err := channel.handler.OnOpen(func(data []byte) error { return r.send(channel, data) })
		if err != nil {
			r.disable(channel, err)
		}
	}
}

func (r *channelRegistry) send(channel *staticChannel, data []byte) error {
	if channel.closed.Load() {
		return E.New("rdp: virtual channel ", channel.name, " is closed")
	}
	if len(data) > maxChannelSend {
		return E.New("rdp: virtual channel ", channel.name, " message of ", len(data), " bytes exceeds ", maxChannelSend, " bytes")
	}
	total := uint32(len(data))
	var packets []byte
	for first := true; first || len(data) > 0; first = false {
		n := min(len(data), channelChunkLength)
		var flags uint32
		if channel.options&channelOptionShowProtocol != 0 {
			flags |= channelFlagShowProtocol
		}
		if first {
			flags |= channelFlagFirst
		}
		if n == len(data) {
			flags |= channelFlagLast
		}
		chunk := binary.LittleEndian.AppendUint32(binary.LittleEndian.AppendUint32(nil, total), flags)
		packet, err := r.mcs.packet(channel.id, append(chunk, data[:n]...))
		if err != nil {
			return err
		}
		packets = append(packets, packet...)
		data = data[n:]
	}
	return r.mcs.outbox.Push(packets, false)
}

func (r *channelRegistry) receive(id uint16, payload []byte) error {
	index := slices.IndexFunc(r.channels, func(channel *staticChannel) bool { return channel.id == id })
	if index < 0 {
		return nil
	}
	channel := r.channels[index]
	cr := &reader{data: payload}
	total, flags := cr.u32(), cr.u32()
	if cr.err != nil {
		return cr.err
	}
	data := cr.rest()
	if flags&(channelFlagSuspend|channelFlagResume) != 0 {
		return nil
	}
	data, err := r.decompressor.Decompress(data, byte(flags>>channelCompressionShift))
	if err != nil {
		return E.Cause(err, "rdp: decompress virtual channel ", channel.name)
	}
	if channel.closed.Load() {
		return nil
	}
	message, err := channel.reassemble(flags, total, data)
	if err == nil && message != nil {
		err = channel.handler.OnData(message)
	}
	if err != nil {
		if errors.Is(err, errRequiredChannel) {
			return err
		}
		r.disable(channel, err)
	}
	return nil
}

func (c *staticChannel) reassemble(flags, total uint32, data []byte) ([]byte, error) {
	if total > maxChannelMessage {
		return nil, E.New("rdp: virtual channel ", c.name, " message of ", total, " bytes exceeds ", maxChannelMessage, " bytes")
	}
	if len(data) > int(total) {
		return nil, E.New("rdp: virtual channel ", c.name, " chunk of ", len(data), " bytes exceeds its ", total, "-byte message")
	}
	if flags&channelFlagFirst != 0 {
		if f := c.fragment; f != nil {
			c.fragment = nil
			return nil, E.New("rdp: virtual channel ", c.name, " message interrupted at ", len(f.data), " of ", f.total, " bytes")
		}
		c.fragment = &channelFragment{total: total}
	}
	f := c.fragment
	if f == nil {
		return nil, E.New("rdp: virtual channel ", c.name, " continues a message it never started")
	}
	if f.total != total {
		c.fragment = nil
		return nil, E.New("rdp: virtual channel ", c.name, " message length changed from ", f.total, " to ", total, " bytes")
	}
	if len(data) > int(total)-len(f.data) {
		c.fragment = nil
		return nil, E.New("rdp: virtual channel ", c.name, " chunk of ", len(data), " bytes overruns its ", total, "-byte message at ", len(f.data))
	}
	f.data = append(f.data, data...)
	if flags&channelFlagLast == 0 {
		return nil, nil
	}
	c.fragment = nil
	if len(f.data) != int(total) {
		return nil, E.New("rdp: virtual channel ", c.name, " message ended at ", len(f.data), " of ", total, " bytes")
	}
	return f.data, nil
}

func (r *channelRegistry) disable(channel *staticChannel, err error) {
	if !channel.closed.CompareAndSwap(false, true) {
		return
	}
	r.logger.Warn("disable virtual channel", "channel", channel.name, "error", err)
	channel.fragment = nil
	channel.handler.OnClose()
}

func (r *channelRegistry) close() {
	for _, channel := range r.channels {
		if channel.closed.CompareAndSwap(false, true) {
			channel.fragment = nil
			channel.handler.OnClose()
		}
	}
}
