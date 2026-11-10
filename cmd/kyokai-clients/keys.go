package main

// Sign-in key files, read for the core's keys.add as the desktop's
// internal/gui/keys.go importKey reads them: any key x/crypto/ssh reads
// (OpenSSH, PEM PKCS#1, PKCS#8 and SEC1), with a passphrase where it has
// one, as an Ed25519 seed or PKCS#8 DER.

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"golang.org/x/crypto/ssh"
)

// Why a key file was not read, as key.import names it.
var (
	errNotAKey       = errors.New("not-a-key")
	errLocked        = errors.New("locked")
	errWrongPassword = errors.New("wrong-passphrase")
	errUnsupported   = errors.New("unsupported")
)

// importKey reads a key file for keys.add.
// Inputs: {"text": the file, "passphrase"?: its passphrase}.
// Returns: {"private": base64 of the seed or PKCS#8 DER, "type":
// ed25519|rsa|ecdsa-p256|ecdsa-p384}; errors not-a-key, locked (it has a
// passphrase, none given), wrong-passphrase, unsupported (RSA below 2048
// bits, another curve, another kind).
func importKey(raw json.RawMessage) (any, error) {
	var p struct {
		Text       string `json:"text"`
		Passphrase string `json:"passphrase"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("bad params")
	}
	key, err := parseKey([]byte(p.Text), p.Passphrase)
	if err != nil {
		return nil, err
	}
	private, kind, err := forCore(key)
	if err != nil {
		return nil, err
	}
	return map[string]any{"private": base64.StdEncoding.EncodeToString(private), "type": kind}, nil
}

// parseKey reads a private key, with the passphrase where it has one.
func parseKey(text []byte, passphrase string) (any, error) {
	key, err := ssh.ParseRawPrivateKey(text)
	var missing *ssh.PassphraseMissingError
	switch {
	case err == nil:
		return key, nil
	case !errors.As(err, &missing):
		return nil, errNotAKey
	case passphrase == "":
		return nil, errLocked
	}
	if key, err = ssh.ParseRawPrivateKeyWithPassphrase(text, []byte(passphrase)); err != nil {
		// Legacy PEM says x509.IncorrectPasswordError; OpenSSH's bcrypt
		// keys only that their check bytes differ: both, a wrong one.
		return nil, errWrongPassword
	}
	return key, nil
}

// forCore is a key as keys.add takes it: an Ed25519 seed, else PKCS#8.
func forCore(key any) ([]byte, string, error) {
	switch k := key.(type) {
	case *ed25519.PrivateKey:
		return k.Seed(), "ed25519", nil
	case ed25519.PrivateKey:
		return k.Seed(), "ed25519", nil
	case *rsa.PrivateKey:
		if k.N.BitLen() < 2048 {
			return nil, "", errUnsupported
		}
		der, err := x509.MarshalPKCS8PrivateKey(k)
		return der, "rsa", err
	case *ecdsa.PrivateKey:
		kind := ""
		switch k.Curve {
		case elliptic.P256():
			kind = "ecdsa-p256"
		case elliptic.P384():
			kind = "ecdsa-p384"
		default:
			return nil, "", errUnsupported
		}
		der, err := x509.MarshalPKCS8PrivateKey(k)
		return der, kind, err
	}
	return nil, "", errUnsupported
}
