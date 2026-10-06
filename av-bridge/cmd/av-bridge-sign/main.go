// av-bridge-sign creates the release signing key and signs collector
// binaries for the self-updater.
//
//	av-bridge-sign -genkey <private-key-file>
//	    Writes a new Ed25519 private key (base64 seed, mode 0600) and
//	    prints the public key to paste into internal/update/signing.go.
//
//	av-bridge-sign -key <private-key-file> -version v1.2.3 -out manifest.json <binary>...
//	    Writes the update manifest: per binary its SHA-256 and signature.
//	    With -key empty (or the file missing) the manifest is written
//	    unsigned, and collectors will refuse to install from it.
//
// The private key must never be stored on the cloud server; the cloud
// image build receives it as a BuildKit secret.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dloomes/av-bridge/internal/update"
)

type manifestFile struct {
	SHA256    string `json:"sha256"`
	Signature string `json:"signature,omitempty"`
	Size      int64  `json:"size"`
}

type manifest struct {
	Version string                  `json:"version"`
	Files   map[string]manifestFile `json:"files"`
}

func main() {
	genkey := flag.String("genkey", "", "write a new private key to this file and print the public key")
	keyPath := flag.String("key", "", "private key file (base64 Ed25519 seed)")
	version := flag.String("version", "", "release version, e.g. v1.2.3")
	out := flag.String("out", "manifest.json", "manifest output path")
	flag.Parse()

	if *genkey != "" {
		if err := writeKey(*genkey); err != nil {
			fmt.Fprintln(os.Stderr, "genkey:", err)
			os.Exit(1)
		}
		return
	}
	if *version == "" || flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: av-bridge-sign -key KEY -version vX.Y.Z -out manifest.json BINARY...")
		os.Exit(2)
	}

	priv, err := readKey(*keyPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "key:", err)
		os.Exit(1)
	}
	if priv == nil {
		fmt.Fprintln(os.Stderr, "warning: no signing key — writing an UNSIGNED manifest; collectors will not auto-update from it")
	}

	m := manifest{Version: *version, Files: map[string]manifestFile{}}
	for _, path := range flag.Args() {
		b, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		name := filepath.Base(path)
		f := manifestFile{SHA256: update.SHA256Hex(b), Size: int64(len(b))}
		if priv != nil {
			f.Signature = update.Sign(priv, name, *version, f.SHA256)
		}
		m.Files[name] = f
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (%d files, signed=%v)\n", *out, len(m.Files), priv != nil)
}

func writeKey(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists — refusing to overwrite a signing key", path)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(priv.Seed())+"\n"), 0o600); err != nil {
		return err
	}
	fmt.Printf("private key: %s (keep it safe and off the cloud server)\npublic key:  %s\n",
		path, base64.StdEncoding.EncodeToString(pub))
	return nil
}

func readKey(path string) (ed25519.PrivateKey, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("not a base64 Ed25519 seed")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}
