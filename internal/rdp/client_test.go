package rdp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
)

// TestNormalizeOptions checks the defaults Kyokai relies on: a supplied stream needs no address,
// the client name is Kyokai's, and a certificate check is mandatory.
func TestNormalizeOptions(t *testing.T) {
	accept := func(string) error { return nil }
	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()
	tests := []struct {
		name    string
		options Options
		address string
		fails   bool
	}{
		{"stream without address", Options{Conn: conn, VerifyCertificate: accept}, "localhost:3389", false},
		{"host without port", Options{Address: "example.test", VerifyCertificate: accept}, "example.test:3389", false},
		{"no address", Options{VerifyCertificate: accept}, "", true},
		{"no certificate check", Options{Address: "example.test"}, "", true},
	}
	for _, test := range tests {
		options, err := normalizeOptions(test.options)
		if test.fails {
			if err == nil {
				t.Errorf("%s: no error", test.name)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		if options.Address != test.address || options.ClientName != "Kyokai" {
			t.Errorf("%s: address %q, client name %q", test.name, options.Address, options.ClientName)
		}
	}
}

// TestCredentialsNeedNLA is the regression test for a password sent over TLS alone: a client with
// credentials asks only for NLA, and a server that refuses it or answers with TLS alone ends the
// connection before anything carries the password. A client without credentials asks for TLS
// alone.
func TestCredentialsNeedNLA(t *testing.T) {
	accept := func(string) error { return nil }
	tests := []struct {
		name      string
		username  string
		kind      byte   // the server's answer: negotiationResponse or negotiationFailure
		value     uint32 // the protocol it selects, or its failure code
		requested uint32
		want      error
	}{
		{"server selects TLS alone", "user", negotiationResponse, protocolSSL, nlaProtocols, ErrNLAUnavailable},
		{"server requires TLS", "user", negotiationFailure, 1, nlaProtocols, ErrNLAUnavailable},
		{"no credentials, server requires NLA", "", negotiationFailure, 5, protocolSSL, ErrUnsupportedSecurity},
	}
	for _, test := range tests {
		conn, server := net.Pipe()
		requested := make(chan uint32, 1)
		go func() {
			defer server.Close()
			request := make([]byte, tpktHeaderLength+1+x224ConnectionHeaderLength+negotiationLength)
			if _, err := io.ReadFull(server, request); err != nil {
				requested <- 0
				return
			}
			requested <- binary.LittleEndian.Uint32(request[len(request)-4:])
			confirm := []byte{tpktVersion, 0, 0, 19, 14, x224Confirm, 0, 0, 0, 0, 0, test.kind, 0, negotiationLength, 0}
			_, _ = server.Write(binary.LittleEndian.AppendUint32(confirm, test.value))
		}()
		opts := Options{Address: "localhost:3389", Conn: conn, Username: test.username, Password: "secret", VerifyCertificate: accept}
		if test.username == "" {
			opts.Password = ""
		}
		_, err := negotiateTransport(t.Context(), opts, nil)
		if got := <-requested; got != test.requested {
			t.Errorf("%s: requested protocols %#x, want %#x", test.name, got, test.requested)
		}
		if !errors.Is(err, test.want) {
			t.Errorf("%s: error %v, want %v", test.name, err, test.want)
		}
	}
}

// TestClientInfoPassword checks that the Client Info PDU carries the password and INFO_AUTOLOGON
// only after NLA.
func TestClientInfoPassword(t *testing.T) {
	opts := Options{Username: "user", Password: "secret-password"}
	password := utf16le(opts.Password)
	for _, nla := range []bool{true, false} {
		info := clientInfo(opts, nla)
		flags := binary.LittleEndian.Uint32(info[8:]) // after the security header and the code page
		if got := bytes.Contains(info, password); got != nla {
			t.Errorf("nla %v: password present %v", nla, got)
		}
		if got := flags&infoAutoLogon != 0; got != nla {
			t.Errorf("nla %v: INFO_AUTOLOGON %v", nla, got)
		}
	}
}
