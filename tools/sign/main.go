// Command sign manages the release key with which an installed privileged
// helper accepts its own updates on Windows (doc/en-us/packaging.md).
//
//	go run ./tools/sign keygen -out FILE
//	    writes the private key (keep it secret: a GitHub secret and the
//	    maintainer's offline copy, never the repository) and prints the
//	    public key
//	go run ./tools/sign bundle -dir DIR -version V
//	    signs DIR's kyokai-svc.exe and core library (bundle.json and
//	    bundle.sig, internal/bundlesig) with the private key in the
//	    KYOKAI_SIGNING_KEY environment variable, never the command line
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/YuanZhe-99/Kyokai/internal/bundlesig"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		fs := flag.NewFlagSet("keygen", flag.ExitOnError)
		out := fs.String("out", "", "where to write the private key")
		fs.Parse(os.Args[2:])
		err = keygen(*out)
	case "bundle":
		fs := flag.NewFlagSet("bundle", flag.ExitOnError)
		dir := fs.String("dir", "", "the folder holding kyokai-svc.exe and the core library")
		ver := fs.String("version", "", "the release version")
		fs.Parse(os.Args[2:])
		err = bundle(*dir, *ver)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sign:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: go run ./tools/sign keygen -out FILE | bundle -dir DIR -version V")
	os.Exit(2)
}

// bundle signs dir's executables with the key in KYOKAI_SIGNING_KEY.
// Side effects: writes bundle.json and bundle.sig into dir.
func bundle(dir, version string) error {
	if dir == "" || version == "" {
		return errors.New("-dir and -version are required")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("KYOKAI_SIGNING_KEY")))
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return errors.New("KYOKAI_SIGNING_KEY does not hold a signing key")
	}
	return bundlesig.Sign(dir, version, ed25519.PrivateKey(raw))
}

// keygen writes a new Ed25519 private key to out and prints its public key.
// Side effects: creates out with mode 0600; refuses to replace a file.
func keygen(out string) error {
	if out == "" {
		return fmt.Errorf("-out is required")
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(base64.StdEncoding.EncodeToString(priv) + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Println(base64.StdEncoding.EncodeToString(pub))
	return nil
}
