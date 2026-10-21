// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rfb/messages.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: virtual display message removed, import paths.

package rfb

import (
	"encoding/binary"
	"math"
)

func setPixelFormatMessage(format PixelFormat) []byte {
	message := make([]byte, 20)
	format.encode(message[4:])
	return message
}

func setEncodingsMessage(encodings []Encoding) []byte {
	message := []byte{2, 0}
	message = binary.BigEndian.AppendUint16(message, uint16(len(encodings)))
	for _, encoding := range encodings {
		message = binary.BigEndian.AppendUint32(message, uint32(encoding))
	}
	return message
}

func framebufferUpdateRequestMessage(incremental bool, width, height int) []byte {
	message := []byte{3, boolByte(incremental), 0, 0, 0, 0}
	message = binary.BigEndian.AppendUint16(message, uint16(width))
	message = binary.BigEndian.AppendUint16(message, uint16(height))
	return message
}

func keyEventMessage(down bool, keysym uint32) []byte {
	message := []byte{4, boolByte(down), 0, 0}
	return binary.BigEndian.AppendUint32(message, keysym)
}

func pointerEventMessage(buttons uint8, x, y int) []byte {
	message := []byte{5, buttons}
	message = binary.BigEndian.AppendUint16(message, clampUint16(x))
	return binary.BigEndian.AppendUint16(message, clampUint16(y))
}

func clientCutTextMessage(text []byte) []byte {
	message := []byte{6, 0, 0, 0}
	message = binary.BigEndian.AppendUint32(message, uint32(len(text)))
	return append(message, text...)
}

// Apple's RFBAutoFrameUpdate sends a 16-byte message 9 with the rectangle at byte 8
// (ScreenSharing 776.27, macOS 27.0.1 26A434, UUID EC0B2C20-F816-3FCE-822D-F9403C09EB50,
// _RFBAutoFrameUpdate 0x1cc068668..0x1cc06871c, 0x1cc0687a4..0x1cc0687b0).
func autoFramebufferUpdateMessage(intervalMicroseconds uint32, width, height int) []byte {
	message := []byte{0x09, 0}
	message = binary.BigEndian.AppendUint16(message, 1)
	message = binary.BigEndian.AppendUint32(message, intervalMicroseconds)
	message = append(message, 0, 0, 0, 0)
	message = binary.BigEndian.AppendUint16(message, uint16(width))
	return binary.BigEndian.AppendUint16(message, uint16(height))
}

func viewerInfoMessage() []byte {
	message := make([]byte, 66)
	message[0] = 0x21
	binary.BigEndian.PutUint16(message[2:], 62)
	binary.BigEndian.PutUint16(message[4:], 1)
	for i, value := range []uint32{2, 6, 1, 0, 26, 6, 2} {
		binary.BigEndian.PutUint32(message[6+i*4:], value)
	}
	bitmap := message[34:]
	bitmap[0] = 0xb0
	bitmap[2] = 0x0c
	bitmap[3] = 0x03
	bitmap[4] = 0x90
	bitmap[10] = 0x40
	return message
}

func setModeMessage(mode Mode) []byte {
	return binary.BigEndian.AppendUint16([]byte{0x0a, 0}, uint16(mode))
}

func autoPasteboardMessage(enabled bool) []byte {
	selector := byte(2)
	if enabled {
		selector = 1
	}
	return []byte{0x15, 0, 0, selector, 0, 0, 0, 0}
}

// Apple's RFBSetDisplay sends message 0x0d with the selector at byte 1 and a
// big-endian display ID at byte 4 (ScreenSharing 776.27, macOS 27.0.1 26A434,
// UUID EC0B2C20-F816-3FCE-822D-F9403C09EB50, _RFBSetDisplay 0x1cc066c1c..0x1cc066c3c).
func setDisplayMessage(id uint32) []byte {
	message := []byte{0x0d, boolByte(id == 0), 0, 0}
	return binary.BigEndian.AppendUint32(message, id)
}

func clampUint16(value int) uint16 {
	return uint16(min(max(value, 0), math.MaxUint16))
}
