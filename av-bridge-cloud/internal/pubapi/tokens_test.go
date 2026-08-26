package pubapi

import (
	"errors"
	"strings"
	"testing"
)

// TestGenerateTokenShape asserts the wire format never drifts. Any
// change here is a breaking change for integrating systems already
// storing tokens — bump the scheme prefix or issue a deprecation before
// touching this test.
func TestGenerateTokenShape(t *testing.T) {
	raw, prefix, hash, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if !strings.HasPrefix(raw, TokenScheme) {
		t.Fatalf("raw missing scheme: %q", raw)
	}
	body := raw[len(TokenScheme):]
	parts := strings.Split(body, "_")
	if len(parts) != 2 {
		t.Fatalf("raw not prefix_secret shape: %q", raw)
	}
	if len(parts[0]) != PrefixLen {
		t.Fatalf("prefix len = %d, want %d", len(parts[0]), PrefixLen)
	}
	if len(parts[1]) != SecretLen {
		t.Fatalf("secret len = %d, want %d", len(parts[1]), SecretLen)
	}
	if prefix != parts[0] {
		t.Fatalf("prefix mismatch: parsed=%q returned=%q", parts[0], prefix)
	}
	if hash != HashToken(raw) {
		t.Fatalf("hash disagrees with HashToken(raw)")
	}
}

// TestGenerateTokenUnique catches an accidental non-random source (e.g.
// a badly-initialised rng) that would mint identical secrets across
// calls. Two draws is enough — collision odds on 24 random bytes are
// vanishing.
func TestGenerateTokenUnique(t *testing.T) {
	a, _, _, err := GenerateToken()
	if err != nil {
		t.Fatalf("first draw: %v", err)
	}
	b, _, _, err := GenerateToken()
	if err != nil {
		t.Fatalf("second draw: %v", err)
	}
	if a == b {
		t.Fatalf("two draws returned identical token: %q", a)
	}
}

// TestParseTokenAccepts the tokens GenerateToken emits.
func TestParseTokenAcceptsGenerated(t *testing.T) {
	raw, prefix, _, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	got, err := ParseToken(raw)
	if err != nil {
		t.Fatalf("ParseToken(%q): %v", raw, err)
	}
	if got != prefix {
		t.Fatalf("ParseToken returned prefix %q, want %q", got, prefix)
	}
}

// TestParseTokenRejects covers the format-violation shapes the resolver
// short-circuits on so we don't waste a DB round-trip on garbage.
func TestParseTokenRejects(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"portal session token", "av_deadbeef"},
		{"wrong scheme", "gh_abcdef01_0123456789012345678901234567890123456789012345678901234567890abc"},
		{"no separator", "avb_abcdef01" + strings.Repeat("0", SecretLen)},
		{"short prefix", "avb_abcd_" + strings.Repeat("0", SecretLen)},
		{"short secret", "avb_abcdef01_short"},
		{"non-hex prefix", "avb_zzzzzzzz_" + strings.Repeat("0", SecretLen)},
		{"non-hex secret", "avb_abcdef01_" + strings.Repeat("z", SecretLen)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseToken(tc.input); !errors.Is(err, ErrInvalidTokenFormat) {
				t.Fatalf("ParseToken(%q): want ErrInvalidTokenFormat, got %v", tc.input, err)
			}
		})
	}
}
