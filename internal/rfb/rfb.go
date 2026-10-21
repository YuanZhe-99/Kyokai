// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rfb/rfb.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: package doc, Options.Conn added, media and virtual display options removed.

// Package rfb is an RFB (VNC) client for standard servers and Apple's Screen Sharing dialect:
// None, VNC password and Apple account security, the framebuffer encodings, input and clipboard.
// It runs over a stream Kyokai supplies or dials. It is vendored code; see third_party/upstream.json.
package rfb

import (
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"time"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

type Dialect int

const (
	DialectAutomatic Dialect = iota
	DialectApple
	DialectStandard
)

func (d Dialect) String() string {
	switch d {
	case DialectAutomatic:
		return "automatic"
	case DialectApple:
		return "apple"
	case DialectStandard:
		return "standard"
	default:
		return fmt.Sprint("dialect(", int(d), ")")
	}
}

type Quality int

const (
	QualityAdaptive Quality = iota
	QualityFull
	QualityHigh
	QualityMedium
	QualityLow
)

func (q Quality) String() string {
	switch q {
	case QualityAdaptive:
		return "adaptive"
	case QualityFull:
		return "full"
	case QualityHigh:
		return "high"
	case QualityMedium:
		return "medium"
	case QualityLow:
		return "low"
	default:
		return fmt.Sprint("quality(", int(q), ")")
	}
}

func ParseQuality(name string) (Quality, error) {
	for quality := QualityAdaptive; quality <= QualityLow; quality++ {
		if quality.String() == name {
			return quality, nil
		}
	}
	return 0, E.New("rfb: unknown quality ", strconv.Quote(name))
}

type Mode int

const (
	ModeObserve Mode = iota
	ModeControl
	ModeExclusiveControl
)

func (m Mode) String() string {
	switch m {
	case ModeObserve:
		return "observe"
	case ModeControl:
		return "control"
	case ModeExclusiveControl:
		return "exclusive"
	default:
		return fmt.Sprint("mode(", int(m), ")")
	}
}

type Options struct {
	// Address is the server host:port to dial; unused when Conn is set.
	Address string
	// Conn, when set, is the established stream to the server; the client uses it instead of
	// dialing Address and closes it when the session ends.
	Conn     net.Conn
	Username string
	Password string

	Dialect              Dialect
	MaximumVersion       ProtocolVersion
	SecurityTypes        []SecurityType
	ClientName           string
	AllowUnverifiedLogin bool
	VerifyServerKey      func(fingerprint string) error

	Quality   Quality
	Mode      Mode
	Clipboard bool
	DisplayID uint32

	AutomaticUpdates bool
	UpdateInterval   time.Duration
	Encodings        []Encoding

	Logger *slog.Logger

	RectangleObserver func(RawRectangle)
}

type RawRectangle struct {
	Sequence uint64
	Rectangle
	Payload []byte
}

type Rectangle struct {
	X        int
	Y        int
	Width    int
	Height   int
	Encoding Encoding
}

func (r Rectangle) Empty() bool {
	return r.Width <= 0 || r.Height <= 0
}

func (r Rectangle) Union(other Rectangle) Rectangle {
	if r.Empty() {
		return other
	}
	if other.Empty() {
		return r
	}
	left := min(r.X, other.X)
	top := min(r.Y, other.Y)
	right := max(r.X+r.Width, other.X+other.Width)
	bottom := max(r.Y+r.Height, other.Y+other.Height)
	return Rectangle{X: left, Y: top, Width: right - left, Height: bottom - top}
}

func (r Rectangle) String() string {
	return fmt.Sprint(r.Width, "x", r.Height, "+", r.X, "+", r.Y)
}
