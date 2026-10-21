package rfb

import (
	"bytes"
	"context"
	"crypto/des"
	"encoding/binary"
	"errors"
	"io"
	"math/bits"
	"net"
	"testing"
	"time"

	"github.com/YuanZhe-99/Kyokai/internal/bgra"
)

// testHandler records framebuffer updates and ignores every other event.
type testHandler struct {
	updates chan Update
}

func (h *testHandler) StateChanged(State) {}
func (h *testHandler) ResizeFramebuffer(width, height int) (*bgra.Image, error) {
	return nil, nil
}
func (h *testHandler) FramebufferUpdated(update Update) {
	if h.updates != nil {
		h.updates <- update
	}
}
func (h *testHandler) CursorChanged(*Cursor)                               {}
func (h *testHandler) CursorMoved(x, y int)                                {}
func (h *testHandler) DisplaysChanged(*DisplayLayout)                      {}
func (h *testHandler) ClipboardReceived(*Clipboard)                        {}
func (h *testHandler) ClipboardFailed(ClipboardProblem, error)             {}
func (h *testHandler) Bell()                                               {}
func (h *testHandler) KeyboardSourceChanged(KeyboardSource)                {}
func (h *testHandler) StatusReceived(Status)                               {}
func (h *testHandler) DeviceInfoReceived(DeviceInfo)                       {}
func (h *testHandler) RemoteDragChanged(*RemoteDrag)                       {}
func (h *testHandler) DropDestinationReceived(id uint32, path string)      {}
func (h *testHandler) SystemCommandsChanged([]SystemCommand)               {}
func (h *testHandler) ChooseSession(SessionRequest) (SessionChoice, error) { return 0, nil }

// testPixels is a 2x2 BGRX frame the test server sends as one Raw rectangle.
var testPixels = []byte{
	0x10, 0x20, 0x30, 0x00, 0x40, 0x50, 0x60, 0x00,
	0x70, 0x80, 0x90, 0x00, 0xa0, 0xb0, 0xc0, 0x00,
}

// serveRFB plays a minimal RFB 3.8 server on conn: it offers securityType, checks the VNC
// password response against password, and sends one Raw update of testPixels.
func serveRFB(t *testing.T, conn net.Conn, securityType SecurityType, password string) {
	t.Helper()
	read := func(n int) []byte {
		b := make([]byte, n)
		if _, err := io.ReadFull(conn, b); err != nil {
			t.Errorf("server read: %v", err)
		}
		return b
	}
	write := func(b []byte) {
		if _, err := conn.Write(b); err != nil {
			t.Errorf("server write: %v", err)
		}
	}
	write([]byte("RFB 003.008\n"))
	if version := string(read(12)); version != "RFB 003.008\n" {
		t.Errorf("client version %q", version)
	}
	write([]byte{1, byte(securityType)})
	if selected := read(1)[0]; selected != byte(securityType) {
		t.Errorf("client selected %d", selected)
	}
	result := uint32(0)
	if securityType == SecurityVNCPassword {
		challenge := bytes.Repeat([]byte{0x5a, 0xc3}, 8)
		write(challenge)
		response := read(16)
		var key [8]byte
		copy(key[:], password)
		for i := range key {
			key[i] = bits.Reverse8(key[i])
		}
		block, _ := des.NewCipher(key[:])
		want := make([]byte, 16)
		block.Encrypt(want[:8], challenge[:8])
		block.Encrypt(want[8:], challenge[8:])
		if !bytes.Equal(response, want) {
			result = 1
		}
	}
	write(binary.BigEndian.AppendUint32(nil, result))
	if result != 0 {
		write(binary.BigEndian.AppendUint32(nil, 5))
		write([]byte("wrong"))
		return
	}
	read(1) // ClientInit
	init := binary.BigEndian.AppendUint16(nil, 2)
	init = binary.BigEndian.AppendUint16(init, 2)
	format := make([]byte, 16)
	pixelFormatBGRA.encode(format)
	init = append(init, format...)
	init = binary.BigEndian.AppendUint32(init, 4)
	write(append(init, "test"...))
	go io.Copy(io.Discard, conn)
	update := []byte{0, 0, 0, 1, 0, 0, 0, 0, 0, 2, 0, 2, 0, 0, 0, 0}
	write(append(update, testPixels...))
}

// TestClientRawUpdate connects over net.Pipe with each supported standard security type and
// checks the pixels of one Raw rectangle.
func TestClientRawUpdate(t *testing.T) {
	tests := []struct {
		name         string
		securityType SecurityType
		password     string
	}{
		{"none", SecurityNone, ""},
		{"vnc password", SecurityVNCPassword, "secret"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clientConn, serverConn := net.Pipe()
			defer serverConn.Close()
			go serveRFB(t, serverConn, test.securityType, test.password)
			handler := &testHandler{updates: make(chan Update, 4)}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client, err := Dial(ctx, Options{Conn: clientConn, Password: test.password, Mode: ModeControl}, handler)
			if err != nil {
				t.Fatalf("Dial: %v", err)
			}
			done := make(chan error, 1)
			go func() { done <- client.Run() }()
			select {
			case update := <-handler.updates:
				if update.Dirty != (Rectangle{Width: 2, Height: 2, Encoding: EncodingRaw}) {
					t.Errorf("dirty %v", update.Dirty)
				}
			case <-ctx.Done():
				t.Fatal("no framebuffer update")
			}
			client.Framebuffer(func(image *bgra.Image) {
				want := bytes.Clone(testPixels)
				for i := 3; i < len(want); i += 4 {
					want[i] = 0xff
				}
				if !bytes.Equal(image.Pix, want) {
					t.Errorf("pixels %x, want %x", image.Pix, want)
				}
			})
			client.Close()
			<-done
		})
	}
}

// TestClientVNCPasswordRejected checks that a failed VNC password surfaces as
// ErrAuthenticationFailed.
func TestClientVNCPasswordRejected(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()
	go serveRFB(t, serverConn, SecurityVNCPassword, "secret")
	_, err := Dial(context.Background(), Options{Conn: clientConn, Password: "other"}, &testHandler{})
	if !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("Dial error %v, want ErrAuthenticationFailed", err)
	}
}
