// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rfb/session.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package rfb

import (
	"encoding/binary"
	"fmt"
	"strings"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

type SessionRequest struct {
	ConsoleUser string
	Choices     []SessionChoice
}

type SessionChoice int

const (
	SessionShareConsole SessionChoice = 0
	SessionOwn          SessionChoice = 2
	SessionCancel       SessionChoice = -1
)

func (s SessionChoice) String() string {
	switch s {
	case SessionShareConsole:
		return "share-console"
	case SessionOwn:
		return "own-session"
	case SessionCancel:
		return "cancel"
	default:
		return fmt.Sprint("session-choice(", int(s), ")")
	}
}

var ErrSessionCancelled = E.New("rfb: session selection cancelled")

func (c *Client) selectSession() error {
	// ScreenSharing writes the session choice at byte 6 of the response body;
	// its credential-bearing response has a 200-byte body with two 64-byte
	// credential slots starting at body offset 0x48 (ScreenSharing 776.27,
	// macOS 27.0.1 26A434, UUID EC0B2C20-F816-3FCE-822D-F9403C09EB50,
	// _RFBSelectSession 0x1cc05ead0, 0x1cc05eb04..0x1cc05eb50, 0x1cc05f494).
	prompt, err := c.stream.bytes(4)
	if err != nil {
		return err
	}
	length := int(binary.BigEndian.Uint16(prompt))
	rest, err := c.stream.bytes(length - 2)
	if err != nil {
		return err
	}
	prompt = append(prompt, rest...)
	if len(prompt) < 13 {
		return E.New("rfb: session selection of ", len(prompt), " bytes, want at least 13")
	}
	flags := binary.BigEndian.Uint32(prompt[4:])
	consoleUser, _, _ := strings.Cut(string(prompt[12:]), "\x00")
	request := SessionRequest{ConsoleUser: consoleUser}
	// Bit 1 shares the console without asking, and bit 3 vetoes the separate
	// session offered by bit 2 (ScreenSharing 776.27, macOS 27.0.1 26A434,
	// _CommandsAllowedToSSSessionSelectArray 0x1cc05e480..0x1cc05e4e8,
	// -[SSSessionView ssSession:wantsSessionSelection:consoleUser:] 0x1cc08db2c..0x1cc08db68).
	if flags&3 != 0 {
		request.Choices = append(request.Choices, SessionShareConsole)
	}
	if flags&0xc == 4 {
		request.Choices = append(request.Choices, SessionOwn)
	}
	c.logger.Debug("session selection", "console", consoleUser, "flags", fmt.Sprintf("%#x", flags))
	c.handler.StateChanged(StateChoosingSession)
	choice, err := c.handler.ChooseSession(request)
	if err != nil {
		return err
	}
	var body []byte
	switch choice {
	case SessionShareConsole:
		body = make([]byte, 72)
		binary.BigEndian.PutUint16(body, 1)
		if flags&2 != 0 {
			body[6] = 1
		} else {
			c.handler.StateChanged(StateWaitingForApproval)
		}
	case SessionOwn:
		if len(c.options.Username) > 63 {
			return E.New("rfb: user name of ", len(c.options.Username), " bytes is over the 63-byte limit")
		}
		if len(c.options.Password) > 63 {
			return E.New("rfb: password is over the 63-byte limit")
		}
		body = make([]byte, 200)
		binary.BigEndian.PutUint16(body, 2)
		body[6] = 2
		var credentials [128]byte
		copy(credentials[:63], c.options.Username)
		copy(credentials[64:127], c.options.Password)
		err = encryptECB(c.wrapKey[:], credentials[:])
		if err != nil {
			return err
		}
		copy(body[0x48:], credentials[:])
	default:
		return ErrSessionCancelled
	}
	copy(body[8:71], c.options.ClientName)
	message := binary.BigEndian.AppendUint16(nil, uint16(len(body)))
	_, err = c.conn.Write(append(message, body...))
	if err != nil {
		return err
	}
	// The server answers with a sized result of at least 80 bytes whose byte 2
	// is the outcome (ScreenSharing 776.27, _RFBSelectSession 0x1cc05f3f4..0x1cc05f4d0,
	// 0x1cc05f5c4..0x1cc05f6c8; screensharingd 0x10005ea94, 0x10005edcc).
	resultLength, err := c.stream.u16()
	if err != nil {
		return E.Cause(err, "read session selection result")
	}
	if resultLength < 80 {
		return E.New("rfb: session selection result of ", resultLength, " bytes, want at least 80")
	}
	result, err := c.stream.bytes(int(resultLength))
	if err != nil {
		return E.Cause(err, "read session selection result")
	}
	switch result[2] {
	case 0:
		return nil
	case 1:
		return ErrPermissionDenied
	default:
		return E.New("rfb: session selection failed with result ", result[2])
	}
}
