package roadmap

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestNewToken(t *testing.T) {
	tok, hash := NewToken()
	if !strings.HasPrefix(tok, "rmp_") {
		t.Fatalf("token %q lacks rmp_ prefix", tok)
	}
	if len(tok) != 47 {
		t.Fatalf("token length = %d, want 47", len(tok))
	}
	if strings.ContainsAny(tok[4:], "+/=") {
		t.Fatalf("token %q is not unpadded base64url", tok)
	}
	tok2, hash2 := NewToken()
	if tok == tok2 || hash == hash2 {
		t.Fatal("two calls returned the same token")
	}
	if HashToken(tok) != hash {
		t.Fatal("HashToken(token) != returned hash")
	}
	sum := sha256.Sum256([]byte(tok))
	if hash != hex.EncodeToString(sum[:]) {
		t.Fatal("hash is not the SHA-256 hex of the token")
	}
}

func TestLooksLikeToken(t *testing.T) {
	tok, _ := NewToken()
	if !LooksLikeToken(tok) {
		t.Fatalf("%q should look like a token", tok)
	}
	for _, bad := range []string{"", "rmp_", "rmp_" + strings.Repeat("a", 42), "xyz_" + tok[4:], tok + "a", "rmp_" + strings.Repeat("!", 43)} {
		if LooksLikeToken(bad) {
			t.Errorf("%q should not look like a token", bad)
		}
	}
}

func TestLinkExpiryDays(t *testing.T) {
	days := func(n int) *int { return &n }
	cases := []struct {
		in   *int
		want int
		ok   bool
	}{
		{nil, 90, true}, {days(1), 1, true}, {days(365), 365, true},
		{days(0), 0, false}, {days(366), 0, false}, {days(400), 0, false}, {days(-1), 0, false},
	}
	for _, c := range cases {
		got, err := linkExpiryDays(c.in)
		if (err == nil) != c.ok || (c.ok && got != c.want) {
			t.Errorf("linkExpiryDays(%v) = %d, %v", c.in, got, err)
		}
	}
}
