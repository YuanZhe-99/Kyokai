package rdp

import (
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
