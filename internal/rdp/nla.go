// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/nla.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package rdp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/binary"
	"io"
	"strconv"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
	"github.com/YuanZhe-99/Kyokai/internal/rdp/ntlm"
)

const (
	credSSPVersion     = 6
	maximumAuthMessage = 1 << 20

	authorizationSuccess      = 0x00000000
	authorizationAccessDenied = 0x00000005
)

type negoToken struct {
	Token []byte `asn1:"explicit,tag:0"`
}

type tsRequest struct {
	Version     int         `asn1:"explicit,tag:0"`
	NegoTokens  []negoToken `asn1:"optional,explicit,tag:1"`
	AuthInfo    []byte      `asn1:"optional,explicit,tag:2"`
	PubKeyAuth  []byte      `asn1:"optional,explicit,tag:3"`
	ErrorCode   int64       `asn1:"optional,explicit,tag:4"`
	ClientNonce []byte      `asn1:"optional,explicit,tag:5"`
}

type passwordCredentials struct {
	Domain   []byte `asn1:"explicit,tag:0"`
	Username []byte `asn1:"explicit,tag:1"`
	Password []byte `asn1:"explicit,tag:2"`
}

type tsCredentials struct {
	Type        int    `asn1:"explicit,tag:0"`
	Credentials []byte `asn1:"explicit,tag:1"`
}

func (t *transport) authenticate(opts Options, host string, cert *x509.Certificate) error {
	if opts.Username == "" {
		return E.Extend(ErrAuthenticationFailed, "username is required")
	}
	client := ntlm.NewClient(ntlm.Options{
		Username:        opts.Username,
		Password:        opts.Password,
		Domain:          opts.Domain,
		Workstation:     opts.ClientName,
		TargetName:      "TERMSRV/" + host,
		ChannelBindings: tlsChannelBindings(cert),
	})
	err := t.writeAuth(tsRequest{Version: credSSPVersion, NegoTokens: []negoToken{{Token: client.Negotiate()}}})
	if err != nil {
		return err
	}
	challenge, err := t.readAuth()
	if err != nil {
		return E.Cause(err, "rdp: read NTLM challenge")
	}
	if challenge.Version < 5 {
		return E.New("rdp: insecure CredSSP version ", challenge.Version)
	}
	if len(challenge.NegoTokens) != 1 {
		return E.New("rdp: expected one NTLM challenge token, got ", len(challenge.NegoTokens))
	}
	authenticate, err := client.Authenticate(challenge.NegoTokens[0].Token)
	if err != nil {
		return E.Cause(err, "rdp: answer NTLM challenge")
	}
	publicKey, err := certificatePublicKey(cert)
	if err != nil {
		return err
	}
	nonce := make([]byte, 32)
	_, _ = rand.Read(nonce)
	err = t.writeAuth(tsRequest{Version: credSSPVersion, NegoTokens: []negoToken{{Token: authenticate}}, PubKeyAuth: client.Seal(credSSPBinding(true, nonce, publicKey)), ClientNonce: nonce})
	if err != nil {
		return err
	}
	response, err := t.readAuth()
	if err != nil {
		return E.Cause(err, "rdp: read CredSSP server proof")
	}
	if response.Version != challenge.Version {
		return E.New("rdp: CredSSP version changed from ", challenge.Version, " to ", response.Version, " during authentication")
	}
	if len(response.PubKeyAuth) == 0 {
		return E.New("rdp: missing CredSSP server public-key proof")
	}
	serverProof, err := client.Unseal(response.PubKeyAuth)
	if err != nil {
		return E.Cause(err, "rdp: unseal CredSSP server proof")
	}
	if !hmac.Equal(serverProof, credSSPBinding(false, nonce, publicKey)) {
		return E.New("rdp: CredSSP public-key binding does not match the TLS certificate")
	}
	password, err := asn1.Marshal(passwordCredentials{
		Domain: utf16le(opts.Domain), Username: utf16le(opts.Username), Password: utf16le(opts.Password),
	})
	if err != nil {
		return err
	}
	defer clear(password)
	credentials, err := asn1.Marshal(tsCredentials{Type: 1, Credentials: password})
	if err != nil {
		return err
	}
	defer clear(credentials)
	err = t.writeAuth(tsRequest{Version: credSSPVersion, AuthInfo: client.Seal(credentials)})
	if err != nil || t.protocol != protocolHybridEx {
		return err
	}
	var result [4]byte
	_, err = io.ReadFull(t.reader, result[:])
	if err != nil {
		return E.Cause(err, "rdp: read early user authorization result")
	}
	t.read.Add(uint64(len(result)))
	switch status := binary.LittleEndian.Uint32(result[:]); status {
	case authorizationSuccess:
		return nil
	case authorizationAccessDenied:
		return ErrRemoteLogonDenied
	default:
		return E.New("rdp: early user authorization result 0x", strconv.FormatUint(uint64(status), 16))
	}
}

func (t *transport) writeAuth(request tsRequest) error {
	data, err := asn1.Marshal(request)
	if err != nil {
		return err
	}
	return t.write(data)
}

func (t *transport) readAuth() (*tsRequest, error) {
	data, err := readDER(t.reader)
	if err != nil {
		return nil, err
	}
	t.read.Add(uint64(len(data)))
	var request tsRequest
	rest, err := asn1.Unmarshal(data, &request)
	if err != nil {
		return nil, E.Cause(err, "rdp: decode CredSSP message")
	}
	if len(rest) != 0 {
		return nil, E.New("rdp: ", len(rest), " bytes of trailing data after CredSSP message")
	}
	if request.ErrorCode != 0 {
		return nil, credSSPError(uint32(request.ErrorCode))
	}
	return &request, nil
}

func credSSPError(status uint32) error {
	switch status {
	case 0xC000006D, 0xC000006A, 0xC0000064:
		return E.Extend(ErrAuthenticationFailed, "NTSTATUS 0x", strconv.FormatUint(uint64(status), 16))
	case 0xC0000071, 0xC0000224:
		return E.Extend(ErrAuthenticationFailed, "the password has expired and must be changed on the computer")
	case 0xC0000234:
		return E.Extend(ErrAuthenticationFailed, "the account is locked out")
	case 0xC0000072, 0xC0000193:
		return E.Extend(ErrAuthenticationFailed, "the account is disabled or expired")
	case 0xC000015B, 0xC000006E, 0xC000006F, 0xC0000070:
		return E.Extend(ErrRemoteLogonDenied, "NTSTATUS 0x", strconv.FormatUint(uint64(status), 16))
	default:
		return E.New("rdp: CredSSP server status 0x", strconv.FormatUint(uint64(status), 16))
	}
}

func readDER(r io.Reader) ([]byte, error) {
	var header [6]byte
	_, err := io.ReadFull(r, header[:2])
	if err != nil {
		return nil, err
	}
	if header[0] != 0x30 {
		return nil, E.New("rdp: expected an ASN.1 SEQUENCE, got tag 0x", strconv.FormatUint(uint64(header[0]), 16))
	}
	used, length := 2, int(header[1])
	if length&0x80 != 0 {
		count := length & 0x7F
		if count == 0 || count > 4 {
			return nil, E.New("rdp: unsupported ASN.1 length of ", count, " bytes")
		}
		_, err = io.ReadFull(r, header[2:2+count])
		if err != nil {
			return nil, err
		}
		if header[2] == 0 {
			return nil, E.New("rdp: non-minimal ASN.1 length")
		}
		used += count
		length = 0
		for _, v := range header[2:used] {
			length = length<<8 | int(v)
			if length > maximumAuthMessage {
				return nil, E.New("rdp: CredSSP message exceeds the 1 MiB limit")
			}
		}
		if length < 128 {
			return nil, E.New("rdp: non-minimal ASN.1 length ", length)
		}
	}
	if length > maximumAuthMessage-used {
		return nil, E.New("rdp: CredSSP message of ", used+length, " bytes exceeds the 1 MiB limit")
	}
	data := make([]byte, used+length)
	copy(data, header[:used])
	_, err = io.ReadFull(r, data[used:])
	return data, err
}

func certificatePublicKey(cert *x509.Certificate) ([]byte, error) {
	var key struct {
		Algorithm pkix.AlgorithmIdentifier
		PublicKey asn1.BitString
	}
	rest, err := asn1.Unmarshal(cert.RawSubjectPublicKeyInfo, &key)
	if err != nil {
		return nil, E.Cause(err, "rdp: parse TLS subject public key")
	}
	if len(rest) != 0 || key.PublicKey.BitLength%8 != 0 || len(key.PublicKey.Bytes) == 0 {
		return nil, E.New("rdp: invalid TLS subject public key")
	}
	return key.PublicKey.Bytes, nil
}

func credSSPBinding(client bool, nonce, publicKey []byte) []byte {
	magic := "CredSSP Server-To-Client Binding Hash\x00"
	if client {
		magic = "CredSSP Client-To-Server Binding Hash\x00"
	}
	h := sha256.New()
	h.Write([]byte(magic))
	h.Write(nonce)
	h.Write(publicKey)
	return h.Sum(nil)
}

func tlsChannelBindings(cert *x509.Certificate) []byte {
	var digest []byte
	switch cert.SignatureAlgorithm {
	case x509.SHA384WithRSA, x509.SHA384WithRSAPSS, x509.ECDSAWithSHA384:
		sum := sha512.Sum384(cert.Raw)
		digest = sum[:]
	case x509.SHA512WithRSA, x509.SHA512WithRSAPSS, x509.ECDSAWithSHA512:
		sum := sha512.Sum512(cert.Raw)
		digest = sum[:]
	default:
		sum := sha256.Sum256(cert.Raw)
		digest = sum[:]
	}
	application := append([]byte("tls-server-end-point:"), digest...)
	data := make([]byte, 20, 20+len(application))
	binary.LittleEndian.PutUint32(data[16:], uint32(len(application)))
	return append(data, application...)
}
