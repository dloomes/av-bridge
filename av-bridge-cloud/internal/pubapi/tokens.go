// Package pubapi implements the customer-facing programmatic API (/pub/v1).
//
// This is deliberately kept OUT of portalapi so the two surfaces evolve
// independently: the portal is an interactive user session hitting rich,
// join-heavy endpoints; the public API is a stable, versioned, token-
// authenticated contract for external systems. They share the same
// underlying tenant DB and RLS policies via db.Store.
//
// Slice 1 of the public API ships two things:
//
//   1. Token infrastructure (this file + auth.go): mint, hash, resolve.
//   2. A skeleton /pub/v1/ping so end-to-end auth can be exercised.
//
// Read endpoints (devices, rooms, alerts, events) land in slice 2 as
// separate handler files inside this package.
package pubapi

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// TokenScheme is the fixed prefix every public-API token starts with.
// A vendor / operator seeing "avb_" in a config file, log line, or
// leaked snippet immediately recognises it as an av-bridge token and
// knows to rotate it. Same idea as GitHub's "ghp_" / Slack's "xoxb-".
const TokenScheme = "avb_"

// PrefixLen / SecretLen chosen so:
//   * prefix (8 hex chars, 4 random bytes) is small enough to display in
//     full in the portal list, still distinguishing enough that an
//     operator can identify which key is which at a glance;
//   * secret (48 hex chars, 24 random bytes) gives ~192 bits of entropy,
//     comfortably above the 128-bit floor for long-lived credentials.
const (
	PrefixLen = 8
	SecretLen = 48
)

// GenerateToken mints a fresh public-API token. Returns:
//   - raw:    the full token to return to the caller EXACTLY ONCE.
//             Never persisted in plaintext.
//   - prefix: the display prefix, safe to store and show in listings.
//   - hash:   the hex SHA-256 of raw. Stored in api_tokens.token_hash;
//             the resolver looks up by this value.
//
// Format: avb_<8-hex prefix>_<48-hex secret>
//
//	e.g. avb_a1b2c3d4_e5f6…deadbeef
func GenerateToken() (raw, prefix, hash string, err error) {
	prefixBytes := make([]byte, PrefixLen/2)
	if _, err := rand.Read(prefixBytes); err != nil {
		return "", "", "", fmt.Errorf("gen prefix: %w", err)
	}
	secretBytes := make([]byte, SecretLen/2)
	if _, err := rand.Read(secretBytes); err != nil {
		return "", "", "", fmt.Errorf("gen secret: %w", err)
	}
	prefix = hex.EncodeToString(prefixBytes)
	secret := hex.EncodeToString(secretBytes)
	raw = TokenScheme + prefix + "_" + secret
	sum := sha256.Sum256([]byte(raw))
	hash = hex.EncodeToString(sum[:])
	return raw, prefix, hash, nil
}

// HashToken returns the hex SHA-256 of a raw token. Used by the resolver
// on the lookup side so it agrees with GenerateToken on the wire format.
// Callers should pass in whatever they extracted from the Authorization
// header, verbatim — trimming is the caller's responsibility (auth.go
// trims once and reuses).
func HashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// ErrInvalidTokenFormat is returned by ParseToken when the caller-supplied
// string can't possibly be an av-bridge token. The auth layer maps this
// to a 401 without touching the DB — a signal that the value is a
// user's session token, an entirely different scheme, or garbled.
var ErrInvalidTokenFormat = errors.New("not an av-bridge public API token")

// ParseToken validates the wire shape of a token and returns its prefix.
// The prefix is useful for observability (log the prefix, never the
// secret) and for a very quick sanity check before hitting the DB.
//
// Shape rules: starts with "avb_", exactly one further underscore, the
// prefix is PrefixLen hex chars, the secret is SecretLen hex chars.
// Anything else is a parse error.
func ParseToken(raw string) (prefix string, err error) {
	if !strings.HasPrefix(raw, TokenScheme) {
		return "", ErrInvalidTokenFormat
	}
	body := raw[len(TokenScheme):]
	sep := strings.IndexByte(body, '_')
	if sep != PrefixLen {
		return "", ErrInvalidTokenFormat
	}
	prefix = body[:sep]
	secret := body[sep+1:]
	if len(secret) != SecretLen {
		return "", ErrInvalidTokenFormat
	}
	if !isHex(prefix) || !isHex(secret) {
		return "", ErrInvalidTokenFormat
	}
	return prefix, nil
}

func isHex(s string) bool {
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		default:
			return false
		}
	}
	return true
}
