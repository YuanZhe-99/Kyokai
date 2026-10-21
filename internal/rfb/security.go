// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rfb/security.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: Kerberos removed, default security type order added, standard-library helpers.

package rfb

import (
	"crypto/des"
	"math/bits"
	"slices"
	"strconv"
	"strings"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

type SecurityType uint8

const (
	SecurityNone        SecurityType = 1
	SecurityVNCPassword SecurityType = 2
	SecurityAppleDH     SecurityType = 30
	SecurityAskObserve  SecurityType = 31
	SecurityAskControl  SecurityType = 32
	SecurityAppleRSA    SecurityType = 33
	SecurityApplePSK    SecurityType = 34
	SecurityKerberos    SecurityType = 35
	SecurityAppleSRP    SecurityType = 36
)

var securityNames = map[SecurityType]string{
	SecurityNone:        "none",
	SecurityVNCPassword: "vnc-password",
	SecurityAppleDH:     "apple-dh",
	SecurityAskObserve:  "ask-observe",
	SecurityAskControl:  "ask-control",
	SecurityAppleRSA:    "apple-rsa-srp",
	SecurityApplePSK:    "apple-psk",
	SecurityKerberos:    "kerberos",
	SecurityAppleSRP:    "apple-srp",
}

func (t SecurityType) String() string {
	name, found := securityNames[t]
	if found {
		return name + "(" + strconv.Itoa(int(t)) + ")"
	}
	return "security(" + strconv.Itoa(int(t)) + ")"
}

func ParseSecurityType(value string) (SecurityType, error) {
	number, err := strconv.Atoi(value)
	if err == nil && number > 0 && number < 256 {
		return SecurityType(number), nil
	}
	for securityType, name := range securityNames {
		if strings.EqualFold(name, value) {
			return securityType, nil
		}
	}
	return 0, E.New("rfb: unknown security type ", strconv.Quote(value))
}

var (
	SecurityAccount        = []SecurityType{SecurityAppleRSA, SecurityAppleSRP, SecurityAppleDH}
	SecurityAskPermission  = []SecurityType{SecurityAskControl, SecurityAskObserve}
	SecurityObserveRequest = []SecurityType{SecurityAskObserve, SecurityAskControl}
)

type authenticator func(h *handshake) ([]byte, error)

var authenticators = map[SecurityType]authenticator{
	SecurityNone:        authenticateNone,
	SecurityVNCPassword: authenticateVNCPassword,
	SecurityAppleDH:     authenticateAppleDH,
	SecurityAskObserve:  authenticateAppleDH,
	SecurityAskControl:  authenticateAppleDH,
	SecurityAppleRSA:    authenticateAppleRSA,
	SecurityAppleSRP:    authenticateAppleSRP,
}

var ErrUnsupportedServer = E.New("rfb: unsupported server")

var ErrUnverifiedServer = E.New("rfb: the server offers only account logins that do not verify its identity")

var ErrServerKeyChanged = E.New("rfb: the server key changed")

func (h *handshake) chooseSecurityType(preferred []SecurityType) (SecurityType, error) {
	offered := h.info.SecurityTypes
	var unverified bool
	for _, candidate := range preferred {
		_, implemented := authenticators[candidate]
		if !implemented || !slices.Contains(offered, candidate) {
			continue
		}
		switch candidate {
		case SecurityAppleRSA:
			// ScreenSharing drops type 33 from an Apple server that offers
			// neither 35 nor 36 (ScreenSharing 776.27, macOS 27.0.1 26A434,
			// UUID EC0B2C20-F816-3FCE-822D-F9403C09EB50, 0x1cc08a038..0x1cc08a09c).
			if h.info.Dialect == DialectApple && !slices.Contains(offered, SecurityAppleSRP) && !slices.Contains(offered, SecurityKerberos) {
				continue
			}
		case SecurityAppleDH:
			if !h.options.AllowUnverifiedLogin {
				unverified = true
				continue
			}
		}
		return candidate, nil
	}
	offeredNames := securityList(offered)
	if unverified {
		return 0, E.Extend(ErrUnverifiedServer, "it offers security types ", offeredNames)
	}
	return 0, E.Extend(ErrUnsupportedServer, "it offers security types ", offeredNames, "; none of ", securityList(preferred))
}

func authenticateNone(*handshake) ([]byte, error) {
	return nil, nil
}

func authenticateVNCPassword(h *handshake) ([]byte, error) {
	challenge, err := h.stream.bytes(16)
	if err != nil {
		return nil, err
	}
	var key [8]byte
	copy(key[:], h.options.Password)
	for i := range key {
		key[i] = bits.Reverse8(key[i])
	}
	block, err := des.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	response := make([]byte, 16)
	block.Encrypt(response[:8], challenge[:8])
	block.Encrypt(response[8:], challenge[8:])
	_, err = h.conn.Write(response)
	return nil, err
}

func credentialBlock(username, password string) ([]byte, error) {
	// ScreenSharing's type-33 credential block randomizes 128 bytes, then copies
	// the username and password into separate 64-byte slots before encryption
	// (ScreenSharing 776.27, macOS 27.0.1 26A434, UUID EC0B2C20-F816-3FCE-822D-F9403C09EB50,
	// _RFBAuthenticateCore 0x1cc13faf4..0x1cc13fb44).
	if len(username) > 63 {
		return nil, E.New("rfb: user name of ", len(username), " bytes is over the 63-byte limit")
	}
	if len(password) > 63 {
		return nil, E.New("rfb: password is over the 63-byte limit")
	}
	if strings.IndexByte(username, 0) >= 0 || strings.IndexByte(password, 0) >= 0 {
		return nil, E.New("rfb: user name or password contains NUL")
	}
	block := randomBytes(128)
	copy(block, username)
	block[len(username)] = 0
	copy(block[64:], password)
	block[64+len(password)] = 0
	return block, nil
}

// securityList names security types for an error message, separated by commas.
func securityList(types []SecurityType) string {
	names := make([]string, len(types))
	for i, securityType := range types {
		names[i] = securityType.String()
	}
	return strings.Join(names, ", ")
}

// defaultSecurityTypes orders the security types tried when Options.SecurityTypes is empty: the
// Apple account logins when a user name is set, then the VNC password, then none (first when no
// password is set either). Added by Kyokai.
func defaultSecurityTypes(options *Options) []SecurityType {
	switch {
	case options.Username != "":
		return append(slices.Clone(SecurityAccount), SecurityVNCPassword, SecurityNone)
	case options.Password != "":
		return []SecurityType{SecurityVNCPassword, SecurityNone}
	default:
		return []SecurityType{SecurityNone, SecurityVNCPassword}
	}
}
