package rfb

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"log/slog"
	"testing"

	"github.com/YuanZhe-99/Kyokai/internal/bgra"
)

// FuzzDecode feeds a rectangle header and its payload from a server to the decoders: no input
// may panic. The seeds are the regressions found so far.
func FuzzDecode(f *testing.F) {
	rect := func(x, y, w, h uint16, encoding Encoding, payload ...byte) []byte {
		b := binary.BigEndian.AppendUint16(nil, x)
		b = binary.BigEndian.AppendUint16(b, y)
		b = binary.BigEndian.AppendUint16(b, w)
		b = binary.BigEndian.AppendUint16(b, h)
		b = binary.BigEndian.AppendUint32(b, uint32(encoding))
		return append(b, payload...)
	}
	// A Raw rectangle of width 0 and a height past the framebuffer, which an empty rectangle
	// passed as inside.
	f.Add(rect(0, 0, 0, 100, EncodingRaw))
	f.Add(rect(4, 4, 8, 8, EncodingRaw, make([]byte, 8*8*4)...))
	f.Add(rect(0, 0, 4, 4, EncodingCopyRect, 0, 10, 0, 10))
	f.Add(rect(0, 0, 20, 20, EncodingHextile, 1))
	// A rectangle of 65535 by 65535, which allocated 17 GB before reading a byte.
	f.Add(rect(0, 0, 0xffff, 0xffff, EncodingRaw))
	f.Add(rect(0, 0, 0xffff, 0xffff, EncodingZlib, 0, 0, 0, 1, 0))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 12 {
			return
		}
		r := Rectangle{
			X:        int(binary.BigEndian.Uint16(data[0:])),
			Y:        int(binary.BigEndian.Uint16(data[2:])),
			Width:    int(binary.BigEndian.Uint16(data[4:])),
			Height:   int(binary.BigEndian.Uint16(data[6:])),
			Encoding: Encoding(int32(binary.BigEndian.Uint32(data[8:]))),
		}
		raw := bufio.NewReader(bytes.NewReader(data[12:]))
		input := &stream{raw: raw, source: raw}
		var d decoders
		_ = d.decode(input, bgra.New(16, 16), r)
	})
}

// TestServerSizesLimited is the regression test for sizes from a server that crashed the client:
// a desktop of 65535 by 65535 (ServerInit, DesktopSize, ExtendedDesktopSize, DisplayLayout all
// resize through resizeLocked), a rich cursor of that size, and an Apple session selection whose
// declared length is under the 2 bytes it counts.
func TestServerSizesLimited(t *testing.T) {
	c := &Client{handler: &testHandler{}, logger: slog.New(slog.DiscardHandler)}
	for _, size := range [][2]int{{0xffff, 0xffff}, {8193, 1}, {8192, 8192}, {0, 10}} {
		if _, err := c.resizeLocked(size[0], size[1]); err == nil {
			t.Errorf("resize to %dx%d accepted", size[0], size[1])
		}
	}
	if _, err := c.resizeLocked(3840, 2160); err != nil {
		t.Errorf("resize to 3840x2160: %v", err)
	}
	stream := func(data ...byte) *stream {
		raw := bufio.NewReader(bytes.NewReader(data))
		return &stream{raw: raw, source: raw}
	}
	c.stream = stream()
	if err := c.receiveRichCursor(Rectangle{Width: 0xffff, Height: 0xffff}); err == nil {
		t.Error("a 65535x65535 rich cursor was accepted")
	}
	for _, length := range []byte{0, 1} {
		c.stream = stream(0, length, 0, 0)
		if err := c.selectSession(); err == nil {
			t.Errorf("a session selection of declared length %d was accepted", length)
		}
	}
}
