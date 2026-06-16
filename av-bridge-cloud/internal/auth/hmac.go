package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// VerifySignature checks an "X-Signature: sha256=<hex>" header against
// HMAC-SHA256(body, secret). Constant-time comparison. This matches the scheme
// the on-prem bridge signs with (internal/api/middleware.go VerifyHMACSignature).
func VerifySignature(secret, signatureHeader string, body []byte) bool {
	if secret == "" || signatureHeader == "" {
		return false
	}
	sig := strings.TrimPrefix(signatureHeader, "sha256=")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(sig), []byte(expected))
}
