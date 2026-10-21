// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/negotiation.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: Options.Conn used instead of dialing, import paths.

package rdp

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"net"
	"strconv"
	"strings"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const (
	protocolRDP        = 0
	protocolSSL        = 1
	protocolHybrid     = 2
	protocolHybridEx   = 8
	requestedProtocols = protocolSSL | protocolHybrid | protocolHybridEx

	negotiationRequest  = 1
	negotiationResponse = 2
	negotiationFailure  = 3
	negotiationLength   = 8

	x224ConnectionHeaderLength = 6
)

var (
	ErrNotRDP               = E.New("rdp: the server did not answer as an RDP server")
	ErrRFBServer            = E.New("rdp: the server sent an RFB greeting")
	ErrUnsupportedSecurity  = E.New("rdp: the server offers neither TLS nor Network Level Authentication")
	ErrAuthenticationFailed = E.New("rdp: authentication failed")
	ErrRemoteLogonDenied    = E.New("rdp: the account is not allowed to sign in remotely")
	ErrCertificateChanged   = E.New("rdp: the server certificate changed")
)

func Probe(ctx context.Context, conn net.Conn) (_ string, err error) {
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer func() {
		stop()
		if ctx.Err() != nil {
			err = E.Errors(ctx.Err(), err)
		}
	}()
	host, _, err := net.SplitHostPort(conn.RemoteAddr().String())
	if err != nil {
		return "", err
	}
	certificate, err := newTransport(conn).negotiate(host)
	if err != nil {
		return "", err
	}
	return fingerprint(certificate), nil
}

func fingerprint(certificate *x509.Certificate) string {
	sum := sha256.Sum256(certificate.Raw)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func negotiateTransport(ctx context.Context, opts Options, authenticating func()) (_ *transport, err error) {
	host, _, err := net.SplitHostPort(opts.Address)
	if err != nil {
		return nil, E.Cause(err, "rdp: parse server address")
	}
	conn := opts.Conn
	if conn == nil {
		conn, err = new(net.Dialer).DialContext(ctx, "tcp", opts.Address)
		if err != nil {
			return nil, err
		}
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer func() {
		stop()
		if ctx.Err() != nil {
			err = E.Errors(ctx.Err(), err)
		}
		if err != nil {
			_ = conn.Close()
		}
	}()
	t := newTransport(conn)
	certificate, err := t.negotiate(host)
	if err != nil {
		return nil, err
	}
	err = opts.VerifyCertificate(fingerprint(certificate))
	if err != nil {
		return nil, err
	}
	if t.protocol != protocolSSL {
		if authenticating != nil {
			authenticating()
		}
		err = t.authenticate(opts, host, certificate)
		if err != nil {
			return nil, err
		}
	}
	err = ctx.Err()
	if err != nil {
		return nil, err
	}
	return t, nil
}

func (t *transport) negotiate(host string) (*x509.Certificate, error) {
	err := t.write(connectionRequest())
	if err != nil {
		return nil, err
	}
	response, err := t.readX224()
	if err != nil {
		if E.IsClosedOrCanceled(err) {
			return nil, err
		}
		return nil, E.Cause1(ErrNotRDP, err)
	}
	protocol, err := parseConnectionConfirm(response)
	if err != nil {
		return nil, err
	}
	switch protocol {
	case protocolSSL, protocolHybrid, protocolHybridEx:
	default:
		return nil, E.Extend(ErrUnsupportedSecurity, "the server selected protocol ", protocol)
	}
	t.protocol = protocol
	return t.upgrade(&tls.Config{
		MinVersion: tls.VersionTLS12,
		// Over TLS 1.3 negotiated by crypto/tls, Windows 11 26100 sends an internal_error alert in
		// place of the NTLM challenge or never answers the NTLM authenticate message, also with
		// X25519 only; the same server completes NLA over TLS 1.2.
		MaxVersion:         tls.VersionTLS12,
		ServerName:         host,
		InsecureSkipVerify: true,
	})
}

func connectionRequest() []byte {
	body := []byte{negotiationRequest, 0}
	body = binary.LittleEndian.AppendUint16(body, negotiationLength)
	body = binary.LittleEndian.AppendUint32(body, requestedProtocols)
	packet := []byte{tpktVersion, 0}
	packet = binary.BigEndian.AppendUint16(packet, uint16(tpktHeaderLength+1+x224ConnectionHeaderLength+len(body)))
	packet = append(packet, byte(x224ConnectionHeaderLength+len(body)), x224Request, 0, 0, 0, 0, 0)
	return append(packet, body...)
}

func parseConnectionConfirm(payload []byte) (uint32, error) {
	r := &reader{data: payload}
	length := int(r.u8())
	code := r.u8()
	r.take(5)
	if r.err != nil {
		return 0, E.Cause1(ErrNotRDP, r.err)
	}
	if code&0xF0 != x224Confirm {
		return 0, E.Extend(ErrNotRDP, "X.224 TPDU code 0x", strconv.FormatUint(uint64(code), 16), " is not a connection confirm")
	}
	if length != len(payload)-1 {
		return 0, E.Extend(ErrNotRDP, "X.224 length indicator ", length, " in a ", len(payload), "-byte TPDU")
	}
	if r.remaining() == 0 {
		return protocolRDP, nil
	}
	kind := r.u8()
	r.take(1)
	size := r.u16()
	value := r.u32()
	if r.err != nil {
		return 0, E.Cause1(ErrNotRDP, r.err)
	}
	if size != negotiationLength || r.remaining() != 0 {
		return 0, E.Extend(ErrNotRDP, "negotiation of ", len(payload)-1-x224ConnectionHeaderLength, " bytes declares length ", size)
	}
	switch kind {
	case negotiationResponse:
		return value, nil
	case negotiationFailure:
		return 0, E.Extend(ErrUnsupportedSecurity, "negotiation failure ", value)
	default:
		return 0, E.Extend(ErrNotRDP, "negotiation type ", kind)
	}
}
