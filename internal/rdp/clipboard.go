// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/clipboard.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package rdp

import (
	"encoding/binary"
	"strings"
	"sync"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const (
	clipboardMonitorReady       = 1
	clipboardFormatList         = 2
	clipboardFormatListResponse = 3
	clipboardFormatDataRequest  = 4
	clipboardFormatDataResponse = 5
	clipboardCapabilities       = 7
	clipboardResponseOK         = 1
	clipboardResponseFail       = 2
	clipboardUnicodeText        = 13
	clipboardShortNameLength    = 32
	clipboardCapabilityGeneral  = 1
	clipboardVersion2           = 2
	clipboardLongFormatNames    = 0x00000002
	maxClipboardData            = 16 << 20
)

type clipboardResponse struct {
	generation uint64
	accepted   chan error
}

type clipboardChannel struct {
	events  Events
	changed func()

	access        sync.Mutex
	send          func([]byte) error
	ready         bool
	longNames     bool
	shared        bool
	localText     string
	localOwned    bool
	remoteText    bool
	pending       bool
	pendingManual bool
	stale         bool
	generation    uint64
	responses     []clipboardResponse
}

func clipboardPDU(kind, flags uint16, data []byte) []byte {
	pdu := binary.LittleEndian.AppendUint16(nil, kind)
	pdu = binary.LittleEndian.AppendUint16(pdu, flags)
	pdu = binary.LittleEndian.AppendUint32(pdu, uint32(len(data)))
	return append(pdu, data...)
}

func (c *clipboardChannel) OnOpen(send func([]byte) error) error {
	c.access.Lock()
	defer c.access.Unlock()
	c.send = send
	return nil
}

func (c *clipboardChannel) OnClose() {
	c.access.Lock()
	ready := c.ready
	c.send, c.ready = nil, false
	c.localText, c.localOwned = "", false
	c.supersede(E.New("rdp: the clipboard channel closed before pasting"))
	c.responses = nil
	c.access.Unlock()
	if ready {
		c.changed()
	}
}

func (c *clipboardChannel) enabled() bool {
	c.access.Lock()
	defer c.access.Unlock()
	return c.ready
}

func (c *clipboardChannel) setText(text string, accepted chan error) (uint64, error) {
	text, _, _ = strings.Cut(text, "\x00")
	if len(text) > maxClipboardData/4 {
		return 0, E.New("rdp: clipboard text of ", len(text), " bytes is over the ", maxClipboardData/4, "-byte limit")
	}
	c.access.Lock()
	defer c.access.Unlock()
	if c.send == nil {
		return 0, E.New("rdp: the clipboard channel is not open")
	}
	if accepted != nil && !c.ready {
		return 0, E.New("rdp: the remote clipboard is not ready to paste")
	}
	c.supersede(E.New("rdp: the clipboard changed before pasting"))
	c.localText, c.localOwned = text, text != ""
	c.remoteText = false
	if !c.ready {
		return c.generation, nil
	}
	return c.generation, c.sendFormatList(accepted)
}

func (c *clipboardChannel) supersede(err error) {
	c.generation++
	for i := range c.responses {
		if accepted := c.responses[i].accepted; accepted != nil {
			accepted <- err
			c.responses[i].accepted = nil
		}
	}
}

func (c *clipboardChannel) request() error {
	c.access.Lock()
	defer c.access.Unlock()
	if !c.ready {
		return E.New("rdp: the remote clipboard is not ready to read")
	}
	if !c.remoteText {
		return E.New("rdp: the remote clipboard has no text")
	}
	if c.pending {
		c.pendingManual = true
		return nil
	}
	return c.sendRequest(true)
}

func (c *clipboardChannel) setShared(shared bool) error {
	c.access.Lock()
	defer c.access.Unlock()
	c.shared = shared
	if !shared || !c.ready || !c.remoteText || c.pending {
		return nil
	}
	return c.sendRequest(false)
}

func (c *clipboardChannel) sendRequest(manual bool) error {
	err := c.send(clipboardPDU(clipboardFormatDataRequest, 0, binary.LittleEndian.AppendUint32(nil, clipboardUnicodeText)))
	if err != nil {
		return err
	}
	c.pending, c.pendingManual, c.stale = true, manual, false
	return nil
}

func (c *clipboardChannel) sendFormatList(accepted chan error) error {
	var data []byte
	if c.localOwned {
		data = binary.LittleEndian.AppendUint32(data, clipboardUnicodeText)
		if c.longNames {
			data = binary.LittleEndian.AppendUint16(data, 0)
		} else {
			data = zeros(data, clipboardShortNameLength)
		}
	}
	err := c.send(clipboardPDU(clipboardFormatList, 0, data))
	if err != nil {
		return err
	}
	c.responses = append(c.responses, clipboardResponse{generation: c.generation, accepted: accepted})
	return nil
}

func (c *clipboardChannel) OnData(data []byte) error {
	r := &reader{data: data}
	kind, flags, size := r.u16(), r.u16(), r.u32()
	if r.err != nil {
		return r.err
	}
	if size > maxClipboardData {
		return E.New("rdp: clipboard message ", kind, " of ", size, " bytes is over the ", maxClipboardData, "-byte limit")
	}
	if uint64(size) > uint64(r.remaining()) {
		return E.New("rdp: truncated clipboard message ", kind, ": ", r.remaining(), " of ", size, " bytes")
	}
	body := r.take(int(size))
	c.access.Lock()
	ready := c.ready
	received, deliver, err := c.receive(kind, flags, body)
	becameReady := !ready && c.ready
	c.access.Unlock()
	if becameReady {
		c.changed()
	}
	if deliver {
		c.events.ClipboardReceived(received)
	}
	return err
}

func (c *clipboardChannel) receive(kind, flags uint16, body []byte) (string, bool, error) {
	r := &reader{data: body}
	switch kind {
	case clipboardCapabilities:
		count := r.u16()
		r.take(2)
		for range count {
			capability, size := r.u16(), r.u16()
			if r.err != nil {
				return "", false, r.err
			}
			if size < 4 {
				return "", false, E.New("rdp: clipboard capability ", capability, " declares ", size, " bytes, less than its 4-byte header")
			}
			data := r.sub(int(size) - 4)
			if capability == clipboardCapabilityGeneral {
				data.take(4)
				c.longNames = data.u32()&clipboardLongFormatNames != 0
			}
		}
		return "", false, r.err
	case clipboardMonitorReady:
		capabilityFlags := uint32(0)
		if c.longNames {
			capabilityFlags = clipboardLongFormatNames
		}
		general := binary.LittleEndian.AppendUint32(binary.LittleEndian.AppendUint32(nil, clipboardVersion2), capabilityFlags)
		capabilities := binary.LittleEndian.AppendUint16(binary.LittleEndian.AppendUint16(nil, 1), 0)
		capabilities = append(capabilities, lengthBlock(clipboardCapabilityGeneral, general)...)
		err := c.send(clipboardPDU(clipboardCapabilities, 0, capabilities))
		if err != nil {
			return "", false, err
		}
		c.ready = true
		return "", false, c.sendFormatList(nil)
	case clipboardFormatList:
		remoteText, err := clipboardHasUnicodeText(body, c.longNames)
		if err != nil {
			return "", false, err
		}
		c.ready = true
		c.remoteText = remoteText
		c.supersede(E.New("rdp: the remote clipboard changed before pasting"))
		c.localText, c.localOwned = "", false
		err = c.send(clipboardPDU(clipboardFormatListResponse, clipboardResponseOK, nil))
		if err != nil || !remoteText {
			return "", false, err
		}
		if c.pending {
			c.stale = true
			return "", false, nil
		}
		if c.shared {
			return "", false, c.sendRequest(false)
		}
		return "", false, nil
	case clipboardFormatListResponse:
		if len(c.responses) == 0 {
			return "", false, nil
		}
		response := c.responses[0]
		c.responses = c.responses[1:]
		var result error
		if flags&clipboardResponseFail != 0 || flags&clipboardResponseOK == 0 {
			result = E.New("rdp: the server rejected the clipboard")
			if response.generation == c.generation {
				c.localText, c.localOwned = "", false
				c.supersede(result)
			}
		}
		if response.accepted != nil {
			response.accepted <- result
		}
		return "", false, nil
	case clipboardFormatDataRequest:
		format := r.u32()
		if r.err != nil {
			return "", false, r.err
		}
		if !c.localOwned || format != clipboardUnicodeText {
			return "", false, c.send(clipboardPDU(clipboardFormatDataResponse, clipboardResponseFail, nil))
		}
		text := strings.ReplaceAll(strings.ReplaceAll(c.localText, "\r\n", "\n"), "\n", "\r\n")
		return "", false, c.send(clipboardPDU(clipboardFormatDataResponse, clipboardResponseOK, binary.LittleEndian.AppendUint16(utf16le(text), 0)))
	case clipboardFormatDataResponse:
		if !c.pending {
			return "", false, nil
		}
		manual := c.pendingManual
		c.pending, c.pendingManual = false, false
		if c.stale && c.remoteText && (manual || c.shared) {
			return "", false, c.sendRequest(manual)
		}
		if flags&clipboardResponseOK == 0 || !(manual || c.shared) {
			return "", false, nil
		}
		if len(body)%2 != 0 {
			return "", false, E.New("rdp: Unicode clipboard data of odd length ", len(body))
		}
		return strings.ReplaceAll(decodeUTF16(body), "\r\n", "\n"), true, nil
	default:
		return "", false, nil
	}
}

func clipboardHasUnicodeText(data []byte, longNames bool) (bool, error) {
	r := &reader{data: data}
	found := false
	for r.remaining() > 0 {
		format := r.u32()
		if longNames {
			for r.err == nil && r.u16() != 0 {
			}
		} else {
			r.take(clipboardShortNameLength)
		}
		if r.err != nil {
			return false, r.err
		}
		found = found || format == clipboardUnicodeText
	}
	return found, nil
}

func (c *Client) SetClipboard(text string) error {
	_, err := c.clipboard.setText(text, nil)
	return err
}

func (c *Client) RequestClipboard() error { return c.clipboard.request() }

func (c *Client) SetSharedClipboard(shared bool) error { return c.clipboard.setShared(shared) }
