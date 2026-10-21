package gui

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// importKey reads Ed25519 (OpenSSH), RSA and ECDSA (PEM and OpenSSH) keys,
// asks for an encrypted key's passphrase, refuses a wrong one, and refuses
// keys Kyokai does not keep; the engine gets a seed or PKCS#8.
func TestKeyImport(t *testing.T) {
	_, edKey, _ := ed25519.GenerateKey(rand.Reader)
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	smallRSA, _ := rsa.GenerateKey(rand.Reader, 1024)
	p256, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	p521, _ := ecdsa.GenerateKey(elliptic.P521(), rand.Reader)
	openssh := func(k any, passphrase string) []byte {
		var b *pem.Block
		var err error
		if passphrase == "" {
			b, err = ssh.MarshalPrivateKey(k, "test")
		} else {
			b, err = ssh.MarshalPrivateKeyWithPassphrase(k, "test", []byte(passphrase))
		}
		if err != nil {
			t.Fatal(err)
		}
		return pem.EncodeToMemory(b)
	}
	pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rsaKey)})
	for _, c := range []struct {
		name       string
		data       []byte
		passphrase string
		typ        string
		err        error
	}{
		{"ed25519", openssh(edKey, ""), "", "ed25519", nil},
		{"ed25519 encrypted, no passphrase", openssh(edKey, "secret"), "", "", errKeyLocked},
		{"ed25519 encrypted, wrong passphrase", openssh(edKey, "secret"), "nope", "", errKeyPassphrase},
		{"ed25519 encrypted", openssh(edKey, "secret"), "secret", "ed25519", nil},
		{"rsa pkcs1", pkcs1, "", "rsa", nil},
		{"rsa openssh", openssh(rsaKey, ""), "", "rsa", nil},
		{"rsa too small", openssh(smallRSA, ""), "", "", errKeyType},
		{"ecdsa p256", openssh(p256, ""), "", "ecdsa-p256", nil},
		{"ecdsa p521", openssh(p521, ""), "", "", errKeyType},
	} {
		k, err := importKey(c.data, c.passphrase)
		if c.err != nil {
			if !errors.Is(err, c.err) {
				t.Errorf("%s: err = %v, want %v", c.name, err, c.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if k.Type != c.typ {
			t.Errorf("%s: type %q, want %q", c.name, k.Type, c.typ)
		}
		if fp, _ := keyFingerprint(k.Public); !strings.HasPrefix(fp, "SHA256:") {
			t.Errorf("%s: public line %q has no fingerprint", c.name, k.Public)
		}
		switch c.typ {
		case "ed25519":
			if len(k.Private) != ed25519.SeedSize {
				t.Errorf("%s: private part is not a seed", c.name)
			}
		default:
			if _, err := x509.ParsePKCS8PrivateKey(k.Private); err != nil {
				t.Errorf("%s: private part is not PKCS#8: %v", c.name, err)
			}
		}
	}
	if _, err := importKey([]byte("not a key"), ""); err == nil || errors.Is(err, errKeyLocked) {
		t.Errorf("garbage: err = %v", err)
	}
}

// A generated pair's take-away copy opens with its passphrase, and its
// public key is the engine's.
func TestNewKeyPair(t *testing.T) {
	k, text, err := newKeyPair("laptop", "pass")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ssh.ParseRawPrivateKey([]byte(text)); err == nil {
		t.Fatal("an encrypted key opened without its passphrase")
	}
	back, err := importKey([]byte(text), "pass")
	if err != nil {
		t.Fatal(err)
	}
	if back.Public != k.Public || string(back.Private) != string(k.Private) {
		t.Fatal("the take-away copy is another key")
	}
}
