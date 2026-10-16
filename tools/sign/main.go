// Command sign manages the release key with which an installed privileged
// helper accepts its own updates on Windows (doc/en-us/packaging.md).
//
//	go run ./tools/sign keygen -out FILE
//	    writes the private key (keep it secret: a GitHub secret and the
//	    maintainer's offline copy, never the repository) and prints the
//	    public key
//
// Signing a bundle arrives with the privileged helper.
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "keygen" {
		fmt.Fprintln(os.Stderr, "usage: go run ./tools/sign keygen -out FILE")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	out := fs.String("out", "", "where to write the private key")
	fs.Parse(os.Args[2:])
	if err := keygen(*out); err != nil {
		fmt.Fprintln(os.Stderr, "sign:", err)
		os.Exit(1)
	}
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
