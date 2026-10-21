// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/ntlm/ntlm.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package ntlm

import (
	"bytes"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/rc4"
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
	"golang.org/x/crypto/md4"
)

const (
	negotiateUnicode                 = 0x00000001
	requestTarget                    = 0x00000004
	negotiateSign                    = 0x00000010
	negotiateSeal                    = 0x00000020
	negotiateLMKey                   = 0x00000080
	negotiateNTLM                    = 0x00000200
	negotiateAlwaysSign              = 0x00008000
	negotiateExtendedSessionSecurity = 0x00080000
	negotiateTargetInfo              = 0x00800000
	negotiateVersion                 = 0x02000000
	negotiate128                     = 0x20000000
	negotiateKeyExchange             = 0x40000000
	negotiate56                      = 0x80000000

	negotiateFlags = negotiateUnicode | requestTarget | negotiateSign | negotiateSeal | negotiateLMKey | negotiateNTLM |
		negotiateAlwaysSign | negotiateExtendedSessionSecurity | negotiateTargetInfo | negotiateVersion |
		negotiate128 | negotiateKeyExchange | negotiate56
	requiredFlags = negotiateUnicode | negotiateSign | negotiateSeal | negotiateNTLM | negotiateExtendedSessionSecurity |
		negotiate128 | negotiateKeyExchange
)

const (
	avEOL             = 0
	avNbComputerName  = 1
	avNbDomainName    = 2
	avFlags           = 6
	avTimestamp       = 7
	avTargetName      = 9
	avChannelBindings = 10

	avFlagMICProvided = 0x2
)

var (
	messageSignature = []byte("NTLMSSP\x00")
	clientVersion    = []byte{10, 0, 0x64, 0x4a, 0, 0, 0, 15}
)

type Options struct {
	Username        string
	Password        string
	Domain          string
	Workstation     string
	TargetName      string
	ChannelBindings []byte
}

type Client struct {
	options        Options
	negotiate      []byte
	clientSignKey  []byte
	serverSignKey  []byte
	clientSeal     *rc4.Cipher
	serverSeal     *rc4.Cipher
	clientSequence uint32
	serverSequence uint32
}

func NewClient(options Options) *Client {
	return &Client{options: options}
}

func (c *Client) Negotiate() []byte {
	message := make([]byte, 40)
	copy(message, messageSignature)
	binary.LittleEndian.PutUint32(message[8:], 1)
	binary.LittleEndian.PutUint32(message[12:], negotiateFlags)
	copy(message[32:], clientVersion)
	c.negotiate = message
	return message
}

func (c *Client) Authenticate(challenge []byte) ([]byte, error) {
	if len(challenge) < 48 {
		return nil, E.New("ntlm: truncated challenge message: ", len(challenge), " of 48 header bytes")
	}
	if !bytes.Equal(challenge[:8], messageSignature) {
		return nil, E.New("ntlm: challenge message signature 0x", hex.EncodeToString(challenge[:8]))
	}
	if messageType := binary.LittleEndian.Uint32(challenge[8:]); messageType != 2 {
		return nil, E.New("ntlm: message type ", messageType, ", expected a challenge")
	}
	flags := binary.LittleEndian.Uint32(challenge[20:])
	if flags&requiredFlags != requiredFlags {
		return nil, E.New("ntlm: server did not accept required negotiate flags 0x", strconv.FormatUint(uint64(requiredFlags&^flags), 16))
	}
	serverChallenge := challenge[24:32]
	infoLength, infoOffset := uint64(binary.LittleEndian.Uint16(challenge[40:])), uint64(binary.LittleEndian.Uint32(challenge[44:]))
	if infoOffset > uint64(len(challenge)) || infoLength > uint64(len(challenge))-infoOffset {
		return nil, E.New("ntlm: target info of ", infoLength, " bytes at offset ", infoOffset, " is outside the ", len(challenge), "-byte challenge message")
	}
	info := challenge[infoOffset : infoOffset+infoLength]
	var (
		pairs, timestamp         []byte
		pairFlags                uint32
		computerName, domainName bool
	)
	for {
		if len(info) < 4 {
			return nil, E.New("ntlm: unterminated target info")
		}
		id, size := binary.LittleEndian.Uint16(info), int(binary.LittleEndian.Uint16(info[2:]))
		if id == avEOL {
			break
		}
		if size > len(info)-4 {
			return nil, E.New("ntlm: truncated target info attribute ", id)
		}
		switch id {
		case avNbComputerName:
			computerName = true
		case avNbDomainName:
			domainName = true
		case avFlags:
			if size != 4 {
				return nil, E.New("ntlm: invalid target info flags length ", size)
			}
			pairFlags = binary.LittleEndian.Uint32(info[4:])
		case avTimestamp:
			if size != 8 {
				return nil, E.New("ntlm: invalid target info timestamp length ", size)
			}
			timestamp = info[4:12]
		}
		if id != avFlags && id != avTargetName && id != avChannelBindings {
			pairs = append(pairs, info[:4+size]...)
		}
		info = info[4+size:]
	}
	if !computerName || !domainName {
		return nil, E.New("ntlm: challenge target info lacks the NetBIOS computer or domain name")
	}
	withMIC := timestamp != nil
	if withMIC {
		pairFlags |= avFlagMICProvided
	}
	if pairFlags != 0 {
		pairs = appendPair(pairs, avFlags, binary.LittleEndian.AppendUint32(nil, pairFlags))
	}
	bindingHash := md5.Sum(c.options.ChannelBindings)
	pairs = appendPair(pairs, avChannelBindings, bindingHash[:])
	pairs = appendPair(pairs, avTargetName, utf16le(c.options.TargetName))
	pairs = appendPair(pairs, avEOL, nil)

	random := make([]byte, 24)
	_, _ = rand.Read(random)
	clientChallenge, exportedSessionKey := random[:8], random[8:]
	if !withMIC {
		now := time.Now()
		timestamp = binary.LittleEndian.AppendUint64(nil, uint64(now.Unix()+11644473600)*10000000+uint64(now.Nanosecond()/100))
	}
	passwordHash := md4.New()
	passwordHash.Write(utf16le(c.options.Password))
	responseKey := hmacMD5(passwordHash.Sum(nil), utf16le(strings.ToUpper(c.options.Username)+c.options.Domain))
	temp := make([]byte, 0, 32+len(pairs))
	temp = append(temp, 1, 1, 0, 0, 0, 0, 0, 0)
	temp = append(temp, timestamp...)
	temp = append(temp, clientChallenge...)
	temp = append(temp, 0, 0, 0, 0)
	temp = append(temp, pairs...)
	temp = append(temp, 0, 0, 0, 0)
	proof := hmacMD5(responseKey, serverChallenge, temp)
	sessionBaseKey := hmacMD5(responseKey, proof)
	lmResponse := make([]byte, 24)
	if !withMIC {
		lmResponse = append(hmacMD5(responseKey, serverChallenge, clientChallenge), clientChallenge...)
	}
	encryptedSessionKey := make([]byte, 16)
	keyCipher, _ := rc4.NewCipher(sessionBaseKey)
	keyCipher.XORKeyStream(encryptedSessionKey, exportedSessionKey)

	headerLength := 72
	if withMIC {
		headerLength = 88
	}
	message := make([]byte, headerLength, headerLength+128+len(temp))
	copy(message, messageSignature)
	binary.LittleEndian.PutUint32(message[8:], 3)
	for _, field := range []struct {
		position int
		value    []byte
	}{
		{28, utf16le(c.options.Domain)},
		{36, utf16le(c.options.Username)},
		{44, utf16le(c.options.Workstation)},
		{12, lmResponse},
		{20, append(proof, temp...)},
		{52, encryptedSessionKey},
	} {
		if len(field.value) > 0xFFFF {
			return nil, E.New("ntlm: authenticate message field of ", len(field.value), " bytes exceeds 65535")
		}
		binary.LittleEndian.PutUint16(message[field.position:], uint16(len(field.value)))
		binary.LittleEndian.PutUint16(message[field.position+2:], uint16(len(field.value)))
		binary.LittleEndian.PutUint32(message[field.position+4:], uint32(len(message)))
		message = append(message, field.value...)
	}
	binary.LittleEndian.PutUint32(message[60:], flags)
	if flags&negotiateVersion != 0 {
		copy(message[64:], clientVersion)
	}
	if withMIC {
		copy(message[72:], hmacMD5(exportedSessionKey, c.negotiate, challenge, message))
	}

	clientSignKey := md5.Sum(append(bytes.Clone(exportedSessionKey), "session key to client-to-server signing key magic constant\x00"...))
	serverSignKey := md5.Sum(append(bytes.Clone(exportedSessionKey), "session key to server-to-client signing key magic constant\x00"...))
	clientSealKey := md5.Sum(append(bytes.Clone(exportedSessionKey), "session key to client-to-server sealing key magic constant\x00"...))
	serverSealKey := md5.Sum(append(bytes.Clone(exportedSessionKey), "session key to server-to-client sealing key magic constant\x00"...))
	c.clientSignKey, c.serverSignKey = clientSignKey[:], serverSignKey[:]
	c.clientSeal, _ = rc4.NewCipher(clientSealKey[:])
	c.serverSeal, _ = rc4.NewCipher(serverSealKey[:])
	return message, nil
}

func (c *Client) Seal(message []byte) []byte {
	token := make([]byte, 16+len(message))
	c.clientSeal.XORKeyStream(token[16:], message)
	sign(token[:16], c.clientSignKey, c.clientSeal, &c.clientSequence, message)
	return token
}

func (c *Client) Unseal(token []byte) ([]byte, error) {
	if len(token) < 16 {
		return nil, E.New("ntlm: truncated sealed message: ", len(token), " of 16 signature bytes")
	}
	message := make([]byte, len(token)-16)
	c.serverSeal.XORKeyStream(message, token[16:])
	var expected [16]byte
	sign(expected[:], c.serverSignKey, c.serverSeal, &c.serverSequence, message)
	if !hmac.Equal(expected[:], token[:16]) {
		return nil, E.New("ntlm: invalid message signature or sequence number")
	}
	return message, nil
}

func sign(signature, key []byte, handle *rc4.Cipher, sequence *uint32, message []byte) {
	binary.LittleEndian.PutUint32(signature, 1)
	binary.LittleEndian.PutUint32(signature[12:], *sequence)
	handle.XORKeyStream(signature[4:12], hmacMD5(key, signature[12:], message)[:8])
	*sequence++
}

func appendPair(b []byte, id uint16, value []byte) []byte {
	b = binary.LittleEndian.AppendUint16(b, id)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(value)))
	return append(b, value...)
}

func hmacMD5(key []byte, data ...[]byte) []byte {
	mac := hmac.New(md5.New, key)
	for _, chunk := range data {
		mac.Write(chunk)
	}
	return mac.Sum(nil)
}

func utf16le(s string) []byte {
	var b []byte
	for _, unit := range utf16.Encode([]rune(s)) {
		b = binary.LittleEndian.AppendUint16(b, unit)
	}
	return b
}
