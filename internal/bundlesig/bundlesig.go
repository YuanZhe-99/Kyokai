// Package bundlesig signs and verifies the app's engine and core library,
// so the privileged helper can update itself from the user-writable app
// folder without an administrator prompt: it trusts only files listed in a
// manifest signed with the release key whose public half is embedded here
// (release.pub; doc/en-us/run-modes.md, "Updates").
//
//	bundle.json  {"version": "0.1.0", "files": {"kyokai-svc.exe": "<sha256>", …}}
//	bundle.sig   base64 Ed25519 signature of bundle.json
//
// Adapted from EasyTier MyGo's internal/bundlesig (same author, GPL-3.0,
// relicensed under AGPL-3.0 here).
package bundlesig

import (
	"crypto/ed25519"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed release.pub
var releasePub string

// Files a bundle manifest covers (those present in the folder).
var Files = []string{"kyokai-svc.exe", "kyokai_core.dll"}

// ErrNoKey reports a build without a release public key (development).
var ErrNoKey = errors.New("this build has no release key; bundles cannot be verified")

// Manifest lists the bundle's files and their SHA-256.
type Manifest struct {
	Version string            `json:"version"`
	Files   map[string]string `json:"files"`
}

// PublicKey returns the embedded release key.
func PublicKey() (ed25519.PublicKey, error) {
	s := strings.TrimSpace(releasePub)
	if s == "" {
		return nil, ErrNoKey
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("release.pub is malformed")
	}
	return ed25519.PublicKey(b), nil
}

// Sign writes bundle.json and bundle.sig for the files in dir.
// Side effects: writes both files into dir.
func Sign(dir, version string, key ed25519.PrivateKey) error {
	m := Manifest{Version: version, Files: map[string]string{}}
	for _, f := range Files {
		data, err := os.ReadFile(filepath.Join(dir, f))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		m.Files[f] = hex.EncodeToString(sum[:])
	}
	if len(m.Files) == 0 {
		return fmt.Errorf("nothing to sign in %s", dir)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "bundle.json"), data, 0o644); err != nil {
		return err
	}
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(key, data))
	return os.WriteFile(filepath.Join(dir, "bundle.sig"), []byte(sig+"\n"), 0o644)
}

// Bundle is a verified bundle: its manifest and the contents of every file
// it lists, exactly the bytes that were hashed during verification.
type Bundle struct {
	Manifest
	// Data maps each manifest file name to its verified contents.
	Data map[string][]byte
}

// Names returns the bundle's file names, sorted.
func (b Bundle) Names() []string {
	names := make([]string, 0, len(b.Data))
	for n := range b.Data {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Read loads dir's bundle, checks the manifest's signature with key and
// every listed file's hash, and returns the verified contents.
// Inputs: dir, a possibly user-writable app folder; key, the release key.
// Returns: the bundle when everything matches.
// Notes: each file is read once and hashed in memory. Callers install
// Bundle.Data and never read the files from dir again: whoever can write
// dir could swap a file between the check and a second read.
func Read(dir string, key ed25519.PublicKey) (Bundle, error) {
	var b Bundle
	data, err := os.ReadFile(filepath.Join(dir, "bundle.json"))
	if err != nil {
		return b, fmt.Errorf("unsigned bundle: %w", err)
	}
	sigText, err := os.ReadFile(filepath.Join(dir, "bundle.sig"))
	if err != nil {
		return b, fmt.Errorf("unsigned bundle: %w", err)
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigText)))
	if err != nil || !ed25519.Verify(key, data, sig) {
		return b, errors.New("bundle signature does not verify")
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return b, err
	}
	b.Manifest = m
	files := make(map[string][]byte, len(m.Files))
	for n, want := range m.Files {
		if n == "" || n == "." || n == ".." || strings.ContainsAny(n, `/\:`) {
			return b, fmt.Errorf("bad file name %q in manifest", n)
		}
		content, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return b, err
		}
		sum := sha256.Sum256(content)
		if hex.EncodeToString(sum[:]) != want {
			return b, fmt.Errorf("%s does not match the signed manifest", n)
		}
		files[n] = content
	}
	b.Data = files
	return b, nil
}
