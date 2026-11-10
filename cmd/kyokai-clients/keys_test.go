package main

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"testing"

	"golang.org/x/crypto/ssh"
)

func importText(t *testing.T, text []byte, passphrase string) (map[string]any, error) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"text": string(text), "passphrase": passphrase})
	r, err := importKey(raw)
	if err != nil {
		return nil, err
	}
	return r.(map[string]any), nil
}

// An OpenSSH Ed25519 key with a passphrase: locked without it, refused with
// a wrong one, its seed with the right one.
func TestImportKeyPassphrase(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("correct horse"))
	if err != nil {
		t.Fatal(err)
	}
	text := pem.EncodeToMemory(block)
	if _, err := importText(t, text, ""); !errors.Is(err, errLocked) {
		t.Fatalf("no passphrase: %v", err)
	}
	if _, err := importText(t, text, "wrong"); !errors.Is(err, errWrongPassword) {
		t.Fatalf("wrong passphrase: %v", err)
	}
	r, err := importText(t, text, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	seed, _ := base64.StdEncoding.DecodeString(r["private"].(string))
	if r["type"] != "ed25519" || string(seed) != string(priv.Seed()) {
		t.Fatalf("got %v", r)
	}
}

// SEC1 and OpenSSH ECDSA keys come out as PKCS#8 of their curve.
func TestImportKeyECDSA(t *testing.T) {
	for _, c := range []struct {
		curve elliptic.Curve
		kind  string
	}{{elliptic.P256(), "ecdsa-p256"}, {elliptic.P384(), "ecdsa-p384"}} {
		k, _ := ecdsa.GenerateKey(c.curve, rand.Reader)
		sec1, _ := x509.MarshalECPrivateKey(k)
		openssh, _ := ssh.MarshalPrivateKey(k, "")
		for _, text := range [][]byte{
			pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: sec1}),
			pem.EncodeToMemory(openssh),
		} {
			r, err := importText(t, text, "")
			if err != nil {
				t.Fatal(err)
			}
			der, _ := base64.StdEncoding.DecodeString(r["private"].(string))
			back, err := x509.ParsePKCS8PrivateKey(der)
			if err != nil || r["type"] != c.kind || !back.(*ecdsa.PrivateKey).Equal(k) {
				t.Fatalf("%s: %v %v", c.kind, r["type"], err)
			}
		}
	}
}

func TestImportKeyNotAKey(t *testing.T) {
	if _, err := importText(t, []byte("hello"), ""); !errors.Is(err, errNotAKey) {
		t.Fatalf("got %v", err)
	}
}
