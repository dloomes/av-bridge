package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func signFixture(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifySignature_Valid(t *testing.T) {
	secret := "shared-secret"
	body := []byte(`{"hello":"world"}`)
	if !VerifySignature(secret, signFixture(secret, body), body) {
		t.Fatal("valid signature was rejected")
	}
}

func TestVerifySignature_Rejects(t *testing.T) {
	secret := "shared-secret"
	body := []byte(`{"hello":"world"}`)
	good := signFixture(secret, body)

	cases := []struct {
		name   string
		secret string
		sig    string
		body   []byte
	}{
		{"wrong secret", "other-secret", good, body},
		{"empty secret", "", good, body},
		{"empty signature", secret, "", body},
		{"tampered body", secret, good, []byte(`{"hello":"tampered"}`)},
		{"garbage signature", secret, "sha256=00", body},
		{"missing prefix", secret, hex.EncodeToString([]byte("nothex")), body},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if VerifySignature(c.secret, c.sig, c.body) {
				t.Fatal("expected rejection")
			}
		})
	}
}
