// Package update is the collector's self-updater: it downloads a new
// binary the cloud offers, proves it was signed by the release key,
// swaps it in, restarts, and rolls back if the new version can't reach
// the cloud.
//
// Trust model: the cloud only relays updates. Binaries are signed at
// build time with an Ed25519 key that never lives on the cloud server;
// the matching public keys are compiled in below. A compromised cloud
// can withhold updates but can't make a collector run code the release
// key didn't sign.
package update

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// trustedKeys are the base64 Ed25519 public keys allowed to sign
// collector releases. More than one so the key can be rotated: ship a
// release trusting old + new, then start signing with the new one.
var trustedKeys = []string{
	// Release key created 2026-10-06. Private half is held off-cloud by
	// the release owner (see deployment docs, "Signing collector updates").
	"oNAJvOfo14Jvlkbj+xFX6bFik7lYjAwsgoPHwk9NyhI=",
}

// signedMessage is what a release signature covers: the artefact name,
// the version it claims to be and its SHA-256. Binding the version stops
// an older signed binary being replayed as a "newer" release.
func signedMessage(artefact, version, sha256Hex string) []byte {
	return []byte(fmt.Sprintf("av-bridge-update-v1\n%s\n%s\n%s\n", artefact, version, sha256Hex))
}

// Sign returns the base64 signature for an artefact. Used by the release
// signing tool (cmd/av-bridge-sign), never by a running collector.
func Sign(priv ed25519.PrivateKey, artefact, version, sha256Hex string) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, signedMessage(artefact, version, sha256Hex)))
}

// Verify reports whether sigB64 is a valid signature by any trusted key.
func Verify(artefact, version, sha256Hex, sigB64 string) bool {
	return verifyWith(trustedKeys, artefact, version, sha256Hex, sigB64)
}

func verifyWith(keys []string, artefact, version, sha256Hex, sigB64 string) bool {
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	msg := signedMessage(artefact, version, sha256Hex)
	for _, k := range keys {
		pub, err := base64.StdEncoding.DecodeString(k)
		if err != nil || len(pub) != ed25519.PublicKeySize {
			continue
		}
		if ed25519.Verify(ed25519.PublicKey(pub), msg, sig) {
			return true
		}
	}
	return false
}

// HasTrustedKey reports whether this build can verify updates at all.
func HasTrustedKey() bool {
	for _, k := range trustedKeys {
		if b, err := base64.StdEncoding.DecodeString(k); err == nil && len(b) == ed25519.PublicKeySize {
			return true
		}
	}
	return false
}

// SHA256Hex is a small helper shared with the signing tool.
func SHA256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
