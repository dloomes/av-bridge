package secrets

import (
	"bytes"
	"testing"
)

const (
	key1 = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	key2 = "ff02030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
)

func TestAESGCM_RoundTrip(t *testing.T) {
	c, err := NewAESGCMFromHexKey(key1)
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte("a-shared-collector-secret")

	ct, err := c.Encrypt(plain)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if bytes.Equal(ct, plain) {
		t.Fatal("ciphertext equals plaintext — encryption did nothing")
	}

	out, err := c.Decrypt(ct)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(out, plain) {
		t.Fatalf("round-trip mismatch: got %q want %q", out, plain)
	}
}

func TestAESGCM_DecryptWithWrongKeyFails(t *testing.T) {
	c1, _ := NewAESGCMFromHexKey(key1)
	c2, _ := NewAESGCMFromHexKey(key2)

	ct, _ := c1.Encrypt([]byte("secret"))
	if _, err := c2.Decrypt(ct); err == nil {
		t.Fatal("expected decrypt with wrong key to fail (would silently leak)")
	}
}

func TestAESGCM_NonceIsRandom(t *testing.T) {
	c, _ := NewAESGCMFromHexKey(key1)
	plain := []byte("same input")
	a, _ := c.Encrypt(plain)
	b, _ := c.Encrypt(plain)
	if bytes.Equal(a, b) {
		t.Fatal("two encryptions of the same plaintext produced the same ciphertext — nonce reuse")
	}
}

func TestAESGCM_RejectsBadKey(t *testing.T) {
	cases := map[string]string{
		"short":     "abcd",
		"odd hex":   "abc",
		"non-hex":   "zzzz" + key1[4:],
		"too long":  key1 + "00",
		"empty":     "",
	}
	for name, k := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewAESGCMFromHexKey(k); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
