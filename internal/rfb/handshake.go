// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rfb/handshake.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: Kerberos removed, default security types and dialect choice, standard-library helpers, named greeting, refusal and dropped sign-in errors.

package rfb

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"unicode/utf8"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

var ErrAuthenticationFailed = E.New("rfb: authentication failed")

var ErrPermissionDenied = E.New("rfb: the request to share the screen was declined")

// ErrClosedBeforeGreeting is a server that ended the connection before its greeting (Kyokai): what TightVNC
// and UltraVNC do on Windows to a connection from their own computer while loopback connections are off.
var ErrClosedBeforeGreeting = E.New("rfb: the server closed the connection before greeting")

// ErrSignInDropped is a server that closed the connection during a sign-in
// instead of answering it (Kyokai): what a Mac does to repeated failures.
var ErrSignInDropped = E.New("rfb: the server closed the connection during the sign-in")

// RefusedError is a server refusing the connection with its own reason (Kyokai).
type RefusedError struct{ Reason string }

func (e *RefusedError) Error() string { return "rfb: server refused the connection: " + e.Reason }

type ProtocolVersion struct {
	Major int
	Minor int
}

func (v ProtocolVersion) String() string {
	return fmt.Sprintf("%03d.%03d", v.Major, v.Minor)
}

func (v ProtocolVersion) apple() bool {
	return v.Major == 3 && (v.Minor == 889 || v.Minor == 888)
}

type ServerInfo struct {
	Version       ProtocolVersion
	Dialect       Dialect
	SecurityTypes []SecurityType
	SecurityType  SecurityType
	Name          string
	Width         int
	Height        int
	PixelFormat   PixelFormat
	Flags         uint32
	Commands      [16]byte
	Extended      bool
}

const (
	ServerFlagObserveOnly         uint32 = 0x01
	ServerFlagControlPrivilege    uint32 = 0x02
	ServerFlagSessionSelect       uint32 = 0x04
	ServerFlagCaptureNotPermitted uint32 = 0x08
	ServerFlagExtended            uint32 = 0x10
)

func (s *ServerInfo) Accepts(messageType uint8) bool {
	index := int(messageType) / 8
	if !s.Extended || index >= len(s.Commands) {
		return false
	}
	return s.Commands[index]&(0x80>>(messageType%8)) != 0
}

type handshake struct {
	ctx          context.Context
	conn         net.Conn
	stream       *stream
	options      *Options
	logger       *slog.Logger
	info         ServerInfo
	securityType SecurityType
	wrapKey      []byte
	selected     func(SecurityType)
}

func (h *handshake) negotiateVersion() error {
	banner, err := h.stream.bytes(12)
	if err != nil {
		return E.Extend(ErrClosedBeforeGreeting, err)
	}
	var server ProtocolVersion
	_, err = fmt.Sscanf(string(banner), "RFB %03d.%03d\n", &server.Major, &server.Minor)
	if err != nil {
		return E.New("rfb: server greeting ", strconv.Quote(string(banner)), " is not RFB")
	}
	dialect := h.options.Dialect
	if dialect == DialectAutomatic {
		// Kyokai: Apple's dialect needs an account login for its record layer; without a user name
		// an Apple server is spoken to as a standard one, which it accepts.
		if server.apple() && h.options.Username != "" {
			dialect = DialectApple
		} else {
			dialect = DialectStandard
		}
	}
	var client ProtocolVersion
	maximum := 8
	if h.options.MaximumVersion.Major == 3 && h.options.MaximumVersion.Minor > 0 {
		maximum = h.options.MaximumVersion.Minor
	}
	switch {
	case dialect == DialectApple:
		if !server.apple() {
			return E.Extend(ErrUnsupportedServer, "it speaks RFB ", server, ", not Apple's 003.889")
		}
		client = ProtocolVersion{3, 889}
	case (server.Major > 3 || server.Minor >= 8) && maximum >= 8:
		client = ProtocolVersion{3, 8}
	case server.Minor >= 7 && maximum >= 7:
		client = ProtocolVersion{3, 7}
	default:
		client = ProtocolVersion{3, 3}
	}
	_, err = fmt.Fprintf(h.conn, "RFB %s\n", client)
	if err != nil {
		return err
	}
	h.info.Version = client
	h.info.Dialect = dialect
	h.logger.Debug("protocol version", "server", server.String(), "client", client.String())
	return nil
}

// refused reads the reason of a server's refusal (Kyokai).
func (h *handshake) refused() error {
	reason, err := h.reason()
	if err != nil {
		return E.Cause(err, "server refused the connection")
	}
	return &RefusedError{Reason: reason}
}

func (h *handshake) readReason() error {
	reason, err := h.reason()
	if err != nil {
		return err
	}
	return E.New(reason)
}

// reason reads a length-prefixed reason string.
func (h *handshake) reason() (string, error) {
	length, err := h.stream.u32()
	if err != nil {
		return "", err
	}
	reason, err := h.stream.bytes(int(min(length, 1<<16)))
	if err != nil {
		return "", err
	}
	return string(reason), nil
}

func (h *handshake) readSecurityTypes() error {
	if h.info.Version.Minor == 3 {
		securityType, err := h.stream.u32()
		if err != nil {
			return E.Cause(err, "read security type")
		}
		if securityType == 0 {
			return h.refused()
		}
		// Kyokai: a type beyond a byte (UltraVNC's MS-Logon) is named whole, not cut to one.
		if securityType > 255 {
			return E.Extend(ErrUnsupportedServer, "it requires security type ", securityType)
		}
		h.info.SecurityTypes = []SecurityType{SecurityType(securityType)}
		return nil
	}
	count, err := h.stream.u8()
	if err != nil {
		return E.Cause(err, "read security types")
	}
	if count == 0 {
		return h.refused()
	}
	offered, err := h.stream.bytes(int(count))
	if err != nil {
		return E.Cause(err, "read security types")
	}
	h.info.SecurityTypes = make([]SecurityType, len(offered))
	for i, value := range offered {
		h.info.SecurityTypes[i] = SecurityType(value)
	}
	return nil
}

func (h *handshake) authenticate() error {
	preferred := h.options.SecurityTypes
	if len(preferred) == 0 {
		preferred = defaultSecurityTypes(h.options)
	}
	err := h.readSecurityTypes()
	if err != nil {
		return err
	}
	h.securityType, err = h.chooseSecurityType(preferred)
	if err != nil {
		return err
	}
	if h.info.Version.Minor != 3 && h.securityType != SecurityAppleRSA && h.securityType != SecurityAppleSRP {
		_, err = h.conn.Write([]byte{byte(h.securityType)})
		if err != nil {
			return err
		}
	}
	h.info.SecurityType = h.securityType
	h.logger.Debug("security", "offered", h.info.SecurityTypes, "selected", h.securityType)
	if h.selected != nil {
		h.selected(h.securityType)
	}
	h.wrapKey, err = authenticators[h.securityType](h)
	if err != nil {
		return E.Cause(dropped(err), "authenticate with ", h.securityType)
	}
	if h.securityType == SecurityNone && h.info.Version.Minor < 8 {
		return nil
	}
	result, err := h.stream.u32()
	if err != nil {
		return E.Cause(dropped(err), "read security result")
	}
	return h.securityResult(result)
}

// dropped names a sign-in's error ErrSignInDropped when the server closed
// the connection (Kyokai).
func dropped(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return E.Extend(ErrSignInDropped, err)
	}
	return err
}

func (h *handshake) securityResult(result uint32) error {
	if result == 0 {
		return nil
	}
	cause := ErrAuthenticationFailed
	if h.securityType == SecurityAskControl || h.securityType == SecurityAskObserve {
		cause = ErrPermissionDenied
	}
	if h.info.Version.Minor == 8 {
		return E.Cause1(cause, h.readReason())
	}
	return E.Extend(cause, "result ", result)
}

func (h *handshake) initialize() error {
	clientInit := byte(1)
	if h.info.Dialect == DialectApple {
		// ScreenSharing adds 0x80 for its extension and 0x40 for session selection
		// (ScreenSharing 776.27, macOS 27.0.1 26A434, UUID EC0B2C20-F816-3FCE-822D-F9403C09EB50,
		// _RFBAuthenticateCore 0x1cc1407fc, 0x1cc1408b0, 0x1cc140998..0x1cc1409a4).
		clientInit = 0xc1
	}
	_, err := h.conn.Write([]byte{clientInit})
	if err != nil {
		return err
	}
	header, err := h.stream.bytes(24)
	if err != nil {
		return E.Cause(err, "read ServerInit")
	}
	h.info.Width = int(binary.BigEndian.Uint16(header[0:]))
	h.info.Height = int(binary.BigEndian.Uint16(header[2:]))
	h.info.PixelFormat = parsePixelFormat(header[4:20])
	nameLength := binary.BigEndian.Uint32(header[20:])
	if nameLength > 1<<20 {
		return E.New("rfb: ServerInit name of ", nameLength, " bytes is over the 1 MiB limit")
	}
	name, err := h.stream.bytes(int(nameLength))
	if err != nil {
		return err
	}
	if h.info.Dialect == DialectApple && len(name) >= 22 && name[0] == 0 && name[1] == 0 {
		h.info.Extended = true
		h.info.Flags = binary.BigEndian.Uint32(name[2:])
		copy(h.info.Commands[:], name[6:22])
		name = name[22:]
	}
	h.info.Name = decodeName(name)
	h.logger.Debug("server init", "name", h.info.Name, "width", h.info.Width, "height", h.info.Height,
		"flags", fmt.Sprintf("%#x", h.info.Flags), "commands", fmt.Sprintf("%x", h.info.Commands))
	return nil
}

func decodeName(name []byte) string {
	if utf8.Valid(name) {
		return strings.TrimRight(string(name), "\x00")
	}
	return decodeLatin1(name)
}
