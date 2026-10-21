// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/license.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package rdp

import (
	"bytes"
	"crypto/md5"
	"crypto/rand"
	"crypto/rc4"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/subtle"
	"crypto/x509"
	"encoding/binary"
	"math/big"
	"slices"
	"strconv"
	"strings"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const (
	licenseInitial = iota
	licenseRequested
	licenseChallenged
	licenseCompleted

	licensePlatformID = 0x04010000

	licenseRequest                   = 0x01
	licensePlatformChallenge         = 0x02
	licenseNew                       = 0x03
	licenseUpgrade                   = 0x04
	licenseNewRequest                = 0x13
	licensePlatformChallengeResponse = 0x15
	licenseErrorAlert                = 0xff
	licensePreambleVersion3          = 0x03
	licensePreambleVersionMask       = 0x0f
	licensePreambleReservedMask      = 0x70

	licenseBlobAny           = 0x0000
	licenseBlobRandom        = 0x0002
	licenseBlobCertificate   = 0x0003
	licenseBlobError         = 0x0004
	licenseBlobRSAKey        = 0x0006
	licenseBlobRSASignature  = 0x0008
	licenseBlobEncryptedData = 0x0009
	licenseBlobKeyExchange   = 0x000d
	licenseBlobScope         = 0x000e
	licenseBlobUserName      = 0x000f
	licenseBlobMachineName   = 0x0010

	licenseStatusValidClient   = 0x07
	licenseTransitionNone      = 2
	licenseTransitionReset     = 3
	licenseTransitionResend    = 4
	licenseMaximumRetries      = 3
	licenseKeyExchangeRSA      = 1
	licenseSignatureRSA        = 1
	licenseChallengeVersion    = 0x0100
	licenseChallengeClientType = 0xff00
	licenseDetailLevel         = 3
	licenseCertificateChain1   = 1
	licenseCertificateChain2   = 2
	licenseCertificateMask     = 0x7fffffff
	licenseRSAMagic            = 0x31415352
	licenseSignatureLength     = 72
	licenseMACPad1             = 0x36
	licenseMACPad2             = 0x5c
	licensePremasterLength     = 48
)

type licenseState struct {
	username, hostname string
	phase              int
	macKey, cipherKey  [16]byte
	hardwareID         [20]byte
	lastResponse       []byte
	retries            int
}

func (l *licenseState) receive(packet []byte) ([]byte, error) {
	if len(packet) < 4 {
		return nil, E.New("rdp: truncated licensing PDU: ", len(packet), " of 4 header bytes")
	}
	if size := int(binary.LittleEndian.Uint16(packet[2:])); size != len(packet) {
		return nil, E.New("rdp: licensing PDU declares ", size, " bytes, got ", len(packet))
	}
	version := packet[1] & licensePreambleVersionMask
	if (version != 2 && version != licensePreambleVersion3) || packet[1]&licensePreambleReservedMask != 0 {
		return nil, E.New("rdp: unsupported licensing version 0x", strconv.FormatUint(uint64(packet[1]), 16))
	}
	if packet[0] == licenseErrorAlert {
		return l.receiveAlert(packet[4:])
	}
	var response []byte
	var err error
	switch packet[0] {
	case licenseRequest:
		if l.phase != licenseInitial {
			return nil, E.New("rdp: unexpected server license request")
		}
		response, err = l.receiveRequest(packet[4:])
	case licensePlatformChallenge:
		if l.phase != licenseRequested {
			return nil, E.New("rdp: unexpected licensing platform challenge")
		}
		response, err = l.receiveChallenge(packet[4:])
	case licenseNew, licenseUpgrade:
		if l.phase != licenseChallenged {
			return nil, E.New("rdp: unexpected new or upgraded license")
		}
		err = l.receiveIssuedLicense(packet[4:])
	default:
		err = E.New("rdp: unsupported licensing PDU 0x", strconv.FormatUint(uint64(packet[0]), 16))
	}
	if err == nil && response != nil {
		l.lastResponse = bytes.Clone(response)
	}
	return response, err
}

func (l *licenseState) receiveAlert(payload []byte) ([]byte, error) {
	r := &reader{data: payload}
	code, transition := r.u32(), r.u32()
	licenseBlob(r, licenseBlobError)
	err := licenseDone(r)
	if err != nil {
		return nil, err
	}
	if code == licenseStatusValidClient {
		if transition != licenseTransitionNone {
			return nil, E.New("rdp: licensing transition ", transition, " for a valid client")
		}
		l.phase = licenseCompleted
		l.lastResponse = nil
		return nil, nil
	}
	if l.phase == licenseCompleted {
		return nil, E.New("rdp: licensing error 0x", strconv.FormatUint(uint64(code), 16), " after completion")
	}
	if (transition == licenseTransitionReset || transition == licenseTransitionResend) && l.retries < licenseMaximumRetries {
		l.retries++
		if transition == licenseTransitionReset {
			l.phase = licenseInitial
			l.macKey, l.cipherKey = [16]byte{}, [16]byte{}
			l.lastResponse = nil
			return nil, nil
		}
		if l.lastResponse != nil {
			return bytes.Clone(l.lastResponse), nil
		}
	}
	return nil, E.New("rdp: server licensing error 0x", strconv.FormatUint(uint64(code), 16), ", transition ", transition)
}

func (l *licenseState) receiveRequest(payload []byte) ([]byte, error) {
	r := &reader{data: payload}
	serverRandom := r.take(32)
	r.take(4)
	r.take(int(r.u32()))
	r.take(int(r.u32()))
	algorithms := licenseBlob(r, licenseBlobKeyExchange)
	certificate := licenseBlob(r, licenseBlobCertificate)
	scopes := r.u32()
	if scopes > uint32(r.remaining()/4) {
		return nil, E.New("rdp: licensing scope count ", scopes, " exceeds the remaining ", r.remaining(), " bytes")
	}
	for range scopes {
		licenseBlob(r, licenseBlobScope)
	}
	err := licenseDone(r)
	if err != nil {
		return nil, err
	}
	var supportsRSA bool
	if len(algorithms)%4 != 0 {
		return nil, E.New("rdp: licensing key exchange list of ", len(algorithms), " bytes is not a multiple of 4")
	}
	for offset := 0; offset < len(algorithms); offset += 4 {
		supportsRSA = supportsRSA || binary.LittleEndian.Uint32(algorithms[offset:]) == licenseKeyExchangeRSA
	}
	if !supportsRSA {
		return nil, E.New("rdp: server does not offer RSA licensing key exchange")
	}
	publicKey, err := parseLicensingCertificate(certificate)
	if err != nil {
		return nil, err
	}
	if len(l.username) > 16384 || len(l.hostname) > 4096 || strings.ContainsRune(l.username, 0) || strings.ContainsRune(l.hostname, 0) {
		return nil, E.New("rdp: invalid licensing client identity")
	}
	var clientRandom [32]byte
	var premaster [licensePremasterLength]byte
	_, _ = rand.Read(clientRandom[:])
	_, _ = rand.Read(premaster[:])
	defer clear(premaster[:])
	l.macKey, l.cipherKey = deriveLicenseKeys(premaster[:], clientRandom[:], serverRandom)
	binary.LittleEndian.PutUint32(l.hardwareID[:], licensePlatformID)
	id := md5.Sum([]byte(l.hostname))
	copy(l.hardwareID[4:], id[:])
	littleEndianSecret := slices.Clone(premaster[:])
	slices.Reverse(littleEndianSecret)
	secret := new(big.Int).SetBytes(littleEndianSecret)
	clear(littleEndianSecret)
	encrypted := new(big.Int).Exp(secret, big.NewInt(int64(publicKey.E)), publicKey.N).Bytes()
	slices.Reverse(encrypted)
	encryptedSecret := make([]byte, publicKey.Size()+8)
	copy(encryptedSecret, encrypted)
	body := binary.LittleEndian.AppendUint32(nil, licenseKeyExchangeRSA)
	body = binary.LittleEndian.AppendUint32(body, licensePlatformID)
	body = append(body, clientRandom[:]...)
	body = appendLicenseBlob(body, licenseBlobRandom, encryptedSecret)
	body = appendLicenseBlob(body, licenseBlobUserName, append([]byte(l.username), 0))
	body = appendLicenseBlob(body, licenseBlobMachineName, append([]byte(l.hostname), 0))
	l.phase = licenseRequested
	return licensePacket(licenseNewRequest, body), nil
}

func (l *licenseState) receiveChallenge(payload []byte) ([]byte, error) {
	r := &reader{data: payload}
	r.take(4)
	encrypted := licenseBlob(r, licenseBlobAny)
	mac := r.take(16)
	err := licenseDone(r)
	if err != nil {
		return nil, err
	}
	if len(encrypted) == 0 || len(encrypted) > 4096 {
		return nil, E.New("rdp: licensing challenge of ", len(encrypted), " bytes is outside 1 to 4096")
	}
	challenge := licenseCrypt(l.cipherKey, encrypted)
	expectedMAC := licenseMAC(l.macKey, challenge)
	if subtle.ConstantTimeCompare(mac, expectedMAC[:]) != 1 {
		return nil, E.New("rdp: invalid licensing challenge MAC")
	}
	response := binary.LittleEndian.AppendUint16(nil, licenseChallengeVersion)
	response = binary.LittleEndian.AppendUint16(response, licenseChallengeClientType)
	response = binary.LittleEndian.AppendUint16(response, licenseDetailLevel)
	response = binary.LittleEndian.AppendUint16(response, uint16(len(challenge)))
	response = append(response, challenge...)
	body := appendLicenseBlob(nil, licenseBlobEncryptedData, licenseCrypt(l.cipherKey, response))
	body = appendLicenseBlob(body, licenseBlobEncryptedData, licenseCrypt(l.cipherKey, l.hardwareID[:]))
	response = append(response, l.hardwareID[:]...)
	responseMAC := licenseMAC(l.macKey, response)
	body = append(body, responseMAC[:]...)
	l.phase = licenseChallenged
	return licensePacket(licensePlatformChallengeResponse, body), nil
}

func (l *licenseState) receiveIssuedLicense(payload []byte) error {
	r := &reader{data: payload}
	encrypted := licenseBlob(r, licenseBlobEncryptedData)
	mac := r.take(16)
	err := licenseDone(r)
	if err != nil {
		return err
	}
	plain := licenseCrypt(l.cipherKey, encrypted)
	expectedMAC := licenseMAC(l.macKey, plain)
	if subtle.ConstantTimeCompare(mac, expectedMAC[:]) != 1 {
		return E.New("rdp: invalid issued license MAC")
	}
	r = &reader{data: plain}
	r.take(4)
	r.take(int(r.u32()))
	r.take(int(r.u32()))
	r.take(int(r.u32()))
	license := r.take(int(r.u32()))
	err = licenseDone(r)
	if err != nil {
		return err
	}
	if len(license) == 0 {
		return E.New("rdp: empty issued license")
	}
	l.phase = licenseCompleted
	l.lastResponse = nil
	return nil
}

func deriveLicenseKeys(premaster, clientRandom, serverRandom []byte) (macKey, cipherKey [16]byte) {
	master := licenseSaltedHashes(premaster, clientRandom, serverRandom)
	session := licenseSaltedHashes(master, serverRandom, clientRandom)
	copy(macKey[:], session[:16])
	h := md5.New()
	h.Write(session[16:32])
	h.Write(clientRandom)
	h.Write(serverRandom)
	copy(cipherKey[:], h.Sum(nil))
	clear(master)
	clear(session)
	return
}

func licenseSaltedHashes(secret, firstRandom, secondRandom []byte) []byte {
	result := make([]byte, 0, 48)
	for _, label := range []string{"A", "BB", "CCC"} {
		sha := sha1.New()
		sha.Write([]byte(label))
		sha.Write(secret)
		sha.Write(firstRandom)
		sha.Write(secondRandom)
		md := md5.New()
		md.Write(secret)
		md.Write(sha.Sum(nil))
		result = md.Sum(result)
	}
	return result
}

func licenseMAC(key [16]byte, data []byte) [16]byte {
	sha := sha1.New()
	sha.Write(key[:])
	sha.Write(bytes.Repeat([]byte{licenseMACPad1}, 40))
	sha.Write(binary.LittleEndian.AppendUint32(nil, uint32(len(data))))
	sha.Write(data)
	md := md5.New()
	md.Write(key[:])
	md.Write(bytes.Repeat([]byte{licenseMACPad2}, 48))
	md.Write(sha.Sum(nil))
	var mac [16]byte
	copy(mac[:], md.Sum(nil))
	return mac
}

func licenseCrypt(key [16]byte, data []byte) []byte {
	cipher, _ := rc4.NewCipher(key[:])
	result := make([]byte, len(data))
	cipher.XORKeyStream(result, data)
	return result
}

func parseLicensingCertificate(data []byte) (*rsa.PublicKey, error) {
	r := &reader{data: data}
	version := r.u32() & licenseCertificateMask
	var key *rsa.PublicKey
	switch version {
	case licenseCertificateChain1:
		signatureAlgorithm, keyAlgorithm := r.u32(), r.u32()
		if signatureAlgorithm != licenseSignatureRSA || keyAlgorithm != licenseKeyExchangeRSA {
			return nil, E.New("rdp: unsupported licensing certificate algorithms: signature ", signatureAlgorithm, ", key exchange ", keyAlgorithm)
		}
		public := licenseBlob(r, licenseBlobRSAKey)
		signature := licenseBlob(r, licenseBlobRSASignature)
		err := licenseDone(r)
		if err != nil {
			return nil, err
		}
		if len(signature) != licenseSignatureLength {
			return nil, E.New("rdp: licensing certificate signature of ", len(signature), " bytes, expected 72")
		}
		p := &reader{data: public}
		magic, keyLength, bitLength, dataLength, exponent := p.u32(), p.u32(), p.u32(), p.u32(), p.u32()
		if magic != licenseRSAMagic || keyLength < 72 || keyLength > 2056 || bitLength != (keyLength-8)*8 || dataLength != keyLength-9 {
			return nil, E.New("rdp: invalid licensing RSA key: magic 0x", strconv.FormatUint(uint64(magic), 16), ", key length ", keyLength, ", bit length ", bitLength, ", data length ", dataLength)
		}
		modulus := p.take(int(keyLength - 8))
		p.take(8)
		err = licenseDone(p)
		if err != nil {
			return nil, err
		}
		if exponent > 0x7fffffff {
			return nil, E.New("rdp: licensing RSA exponent 0x", strconv.FormatUint(uint64(exponent), 16), " exceeds 31 bits")
		}
		modulus = slices.Clone(modulus)
		slices.Reverse(modulus)
		key = &rsa.PublicKey{N: new(big.Int).SetBytes(modulus), E: int(exponent)}
	case licenseCertificateChain2:
		count := r.u32()
		if count == 0 || count > 200 || count > uint32(r.remaining()/4) {
			return nil, E.New("rdp: invalid licensing certificate count ", count)
		}
		var last []byte
		for range count {
			last = r.take(int(r.u32()))
		}
		if r.remaining() != 0 {
			r.take(8 + 4*int(count))
		}
		err := licenseDone(r)
		if err != nil {
			return nil, err
		}
		certificate, err := x509.ParseCertificate(last)
		if err != nil {
			return nil, E.Cause(err, "rdp: parse licensing certificate")
		}
		var ok bool
		key, ok = certificate.PublicKey.(*rsa.PublicKey)
		if !ok {
			return nil, E.New("rdp: licensing certificate does not contain an RSA key")
		}
	default:
		return nil, E.New("rdp: unsupported licensing certificate version ", version)
	}
	// The fixed licensing key of xrdp before 0.10.1 has a 511-bit modulus, though its key blob declares 512 bits.
	if key.N.BitLen() <= licensePremasterLength*8 || key.N.BitLen() > 16384 || key.N.Bit(0) != 1 || key.E < 3 || key.E&1 != 1 || uint64(key.E) > 0x7fffffff {
		return nil, E.New("rdp: unsupported licensing RSA public key: ", key.N.BitLen(), "-bit modulus, exponent ", key.E)
	}
	return key, nil
}

func licensePacket(kind byte, body []byte) []byte {
	packet := []byte{kind, licensePreambleVersion3}
	packet = binary.LittleEndian.AppendUint16(packet, uint16(len(body)+4))
	return append(packet, body...)
}

func appendLicenseBlob(dst []byte, kind uint16, data []byte) []byte {
	dst = binary.LittleEndian.AppendUint16(dst, kind)
	dst = binary.LittleEndian.AppendUint16(dst, uint16(len(data)))
	return append(dst, data...)
}

func licenseBlob(r *reader, expected uint16) []byte {
	kind, length := r.u16(), r.u16()
	if r.err == nil && expected != licenseBlobAny && kind != expected && length != 0 {
		r.err = E.New("rdp: unexpected licensing blob type ", kind, ", expected ", expected)
	}
	return r.take(int(length))
}

func licenseDone(r *reader) error {
	if r.err != nil {
		return E.Cause(r.err, "rdp: parse licensing data")
	}
	if r.remaining() != 0 {
		return E.New("rdp: ", r.remaining(), " bytes of trailing licensing data")
	}
	return nil
}
