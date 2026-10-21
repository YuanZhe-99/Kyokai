// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/types.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: Options.Conn added; UDP and graphics-cache options and statistics removed.

package rdp

import (
	"image"
	"log/slog"
	"net"
	"time"

	"github.com/YuanZhe-99/Kyokai/internal/bgra"
)

type State int

const (
	StateConnecting State = iota
	StateAuthenticating
)

type Options struct {
	// Address is the server host and optional port (3389 by default). With Conn set it only names
	// the server for TLS and NTLM, and may be empty ("localhost").
	Address string
	// Conn, when set, is the established stream to the server; the client uses it instead of
	// dialing Address and closes it when the session ends.
	Conn               net.Conn
	Username           string
	Password           string
	Domain             string
	ClientName         string
	Width              int
	Height             int
	DesktopScaleFactor int
	KeyboardLayout     uint32
	Clipboard          bool
	Audio              bool
	Microphone         bool
	VerifyCertificate  func(fingerprint string) error
	KeepAlive          bool
	DialTimeout        time.Duration
	Logger             *slog.Logger
}

type AudioFormat struct {
	Channels      int
	SampleRate    int
	BitsPerSample int
}

type Update struct {
	Rectangle image.Rectangle
	Hold      time.Duration
}

type Events interface {
	StateChanged(State)
	InfoChanged(Info)
	ResizeFramebuffer(width, height int) (*bgra.Image, error)
	FramebufferUpdated(Update)
	CursorChanged(*bgra.Cursor)
	CursorMoved(x, y int)
	ClipboardReceived(string)
	Bell()
	AudioFormatChanged(AudioFormat)
	AudioPacketReceived([]byte, uint64)
	MicrophoneOpened(AudioFormat)
	MicrophoneClosed()
}

type Info struct {
	Width     int
	Height    int
	Scale     int
	LoggedOn  bool
	Clipboard bool
	Resize    bool
	Audio     bool
	Unicode   bool
	Touch     bool
}

type TouchPhase uint8

const (
	TouchBegan TouchPhase = iota + 1
	TouchMoved
	TouchEnded
	TouchCancelled
)

type Touch struct {
	ID    int64
	Phase TouchPhase
	X     int
	Y     int
}

type Statistics struct {
	BytesRead    uint64
	BytesWritten uint64
	Frames       uint64
	Encoding     string
	RoundTrip    time.Duration
}

const (
	ButtonLeft   uint8 = 1
	ButtonMiddle uint8 = 2
	ButtonRight  uint8 = 4
)
