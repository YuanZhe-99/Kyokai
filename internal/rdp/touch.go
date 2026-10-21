// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/touch.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: standard-library helpers in place of sing, import paths.

package rdp

import (
	"encoding/binary"
	"log/slog"
	"slices"
	"sync"
	"time"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const (
	touchEventServerReady = 0x0001
	touchEventClientReady = 0x0002
	touchEventTouch       = 0x0003
	touchEventSuspend     = 0x0004
	touchEventResume      = 0x0005

	touchProtocolV100 = 0x00010000
	touchProtocolV101 = 0x00010001

	touchReadyShowVisuals               = 0x00000001
	touchReadyDisableTimestampInjection = 0x00000002

	contactDown      = 0x0001
	contactUpdate    = 0x0002
	contactUp        = 0x0004
	contactInRange   = 0x0008
	contactInContact = 0x0010
	contactCanceled  = 0x0020

	touchHeaderLength = 6
	maxTouchContacts  = 10
)

type packedInteger struct {
	countBits uint
	valueBits uint
}

var (
	twoByteUnsigned   = packedInteger{countBits: 1, valueBits: 7}
	fourByteUnsigned  = packedInteger{countBits: 2, valueBits: 6}
	fourByteSigned    = packedInteger{countBits: 2, valueBits: 5}
	eightByteUnsigned = packedInteger{countBits: 3, valueBits: 5}
)

func (p packedInteger) append(dst []byte, value uint64) []byte {
	var extra uint
	for extra < 1<<p.countBits-1 && value >= 1<<(p.valueBits+8*extra) {
		extra++
	}
	dst = append(dst, byte(extra<<(8-p.countBits))|byte(value>>(8*extra))&(1<<p.valueBits-1))
	for shift := int(extra) - 1; shift >= 0; shift-- {
		dst = append(dst, byte(value>>(8*shift)))
	}
	return dst
}

func touchPDU(event uint16, body []byte) []byte {
	pdu := binary.LittleEndian.AppendUint16(nil, event)
	pdu = binary.LittleEndian.AppendUint32(pdu, uint32(touchHeaderLength+len(body)))
	return append(pdu, body...)
}

type touchContact struct {
	id    int64
	x     int
	y     int
	flags uint32
}

type touchChannel struct {
	logger  *slog.Logger
	changed func()

	access    sync.Mutex
	send      func([]byte) error
	ready     bool
	suspended bool
	contacts  [maxTouchContacts]*touchContact
	lastFrame time.Time
}

func (t *touchChannel) OnOpen(send func([]byte) error) error {
	t.access.Lock()
	t.send = send
	t.access.Unlock()
	return nil
}

func (t *touchChannel) OnClose() {
	t.access.Lock()
	t.send, t.ready, t.suspended = nil, false, false
	t.resetLocked()
	t.access.Unlock()
	t.changed()
}

func (t *touchChannel) OnData(data []byte) error {
	r := &reader{data: data}
	event, length := r.u16(), r.u32()
	if r.err != nil {
		return r.err
	}
	if length < touchHeaderLength || int(length) > len(data) {
		return E.New("rdp: touch input PDU of ", length, " bytes in a ", len(data), "-byte message")
	}
	switch event {
	case touchEventServerReady:
		version := min(r.u32(), touchProtocolV101)
		if r.err != nil {
			return r.err
		}
		flags := uint32(touchReadyShowVisuals)
		if version > touchProtocolV100 {
			flags |= touchReadyDisableTimestampInjection
		}
		body := binary.LittleEndian.AppendUint32(nil, flags)
		body = binary.LittleEndian.AppendUint32(body, version)
		body = binary.LittleEndian.AppendUint16(body, maxTouchContacts)
		t.access.Lock()
		err := t.send(touchPDU(touchEventClientReady, body))
		t.ready, t.suspended = err == nil, false
		t.resetLocked()
		t.access.Unlock()
		t.changed()
		return err
	case touchEventSuspend, touchEventResume:
		t.access.Lock()
		t.suspended = event == touchEventSuspend
		t.resetLocked()
		t.access.Unlock()
		t.changed()
	}
	return nil
}

func (t *touchChannel) resetLocked() {
	t.contacts = [maxTouchContacts]*touchContact{}
	t.lastFrame = time.Time{}
}

func (t *touchChannel) enabled() bool {
	t.access.Lock()
	defer t.access.Unlock()
	return t.ready && !t.suspended
}

func (t *touchChannel) update(touches []Touch) error {
	t.access.Lock()
	defer t.access.Unlock()
	if !t.ready || t.suspended {
		return nil
	}
	var pending bool
	var lifted []*touchContact
	for _, touch := range touches {
		var contact *touchContact
		if touch.Phase == TouchBegan {
			slot := slices.Index(t.contacts[:], nil)
			if slot < 0 {
				t.logger.Debug("ignore a touch beyond the contact limit", "limit", maxTouchContacts)
				continue
			}
			contact = &touchContact{id: touch.ID}
			t.contacts[slot] = contact
		} else {
			index := slices.IndexFunc(t.contacts[:], func(contact *touchContact) bool { return contact != nil && contact.id == touch.ID })
			if index < 0 {
				continue
			}
			contact = t.contacts[index]
		}
		switch touch.Phase {
		case TouchBegan:
			contact.flags = contactDown | contactInRange | contactInContact
		case TouchMoved:
			contact.flags = contactUpdate | contactInRange | contactInContact
		case TouchEnded:
			if touch.X == contact.x && touch.Y == contact.y {
				contact.flags = contactUp
			} else {
				contact.flags = contactUpdate | contactInRange | contactInContact
				lifted = append(lifted, contact)
			}
		case TouchCancelled:
			contact.flags = contactUp | contactCanceled
		default:
			return E.New("rdp: unknown touch phase ", int(touch.Phase))
		}
		contact.x, contact.y = touch.X, touch.Y
		pending = true
	}
	if !pending {
		return nil
	}
	if len(lifted) > 0 {
		// Windows 11 26200 turns an up frame that moves the contact into a canceled one, which drops a dragged
		// window back where it started and loses a tap, so a lift that moves first moves the contact in its own
		// frame and then lifts it in place.
		err := t.flushLocked()
		if err != nil {
			return err
		}
		for _, contact := range lifted {
			contact.flags = contactUp
		}
	}
	return t.flushLocked()
}

func (t *touchChannel) flushLocked() error {
	var count int
	for _, contact := range t.contacts {
		if contact != nil {
			count++
		}
	}
	now := time.Now()
	var offset uint64
	if !t.lastFrame.IsZero() {
		offset = uint64(now.Sub(t.lastFrame).Microseconds())
	}
	t.lastFrame = now
	body := fourByteUnsigned.append(nil, 0)
	body = twoByteUnsigned.append(body, 1)
	body = twoByteUnsigned.append(body, uint64(count))
	body = eightByteUnsigned.append(body, offset)
	for slot, contact := range t.contacts {
		if contact == nil {
			continue
		}
		// rdpinput.exe of Windows 11 26200 drops a contact whose ID is not below maxTouchContacts, which the
		// MaxActiveTouchContacts policy can lower further, so the ID is the lowest free slot.
		body = append(body, byte(slot))
		body = twoByteUnsigned.append(body, 0)
		body = fourByteSigned.append(body, uint64(contact.x))
		body = fourByteSigned.append(body, uint64(contact.y))
		body = fourByteUnsigned.append(body, uint64(contact.flags))
		if contact.flags&contactUp != 0 {
			t.contacts[slot] = nil
		} else {
			contact.flags = contactUpdate | contactInRange | contactInContact
		}
	}
	// rdpinput.exe of Windows 11 26200 re-injects the contacts still in range every 200 ms after the last
	// frame, so a held contact needs no repeated frames.
	return t.send(touchPDU(touchEventTouch, body))
}

func (c *Client) SendTouches(touches []Touch) error {
	mapped := make([]Touch, len(touches))
	for i, touch := range touches {
		x, y := c.position(touch.X, touch.Y)
		touch.X, touch.Y = int(x), int(y)
		mapped[i] = touch
	}
	return c.touch.update(mapped)
}
