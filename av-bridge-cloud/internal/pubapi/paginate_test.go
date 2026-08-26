package pubapi

import (
	"errors"
	"net/http/httptest"
	"testing"
	"time"
)

func TestEncodeDecodeCursorRoundTrip(t *testing.T) {
	ts := time.Date(2026, 8, 26, 12, 34, 56, 789_000_000, time.UTC)
	in := Cursor{TS: &ts, ID: "abc-123"}
	enc := EncodeCursor(in)
	if enc == "" {
		t.Fatal("empty cursor")
	}
	out, err := DecodeCursor(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ID != in.ID {
		t.Fatalf("id mismatch: got %q want %q", out.ID, in.ID)
	}
	if out.TS == nil || !out.TS.Equal(ts) {
		t.Fatalf("ts mismatch: got %v want %v", out.TS, ts)
	}
}

func TestDecodeCursorEmpty(t *testing.T) {
	got, err := DecodeCursor("")
	if err != nil {
		t.Fatalf("empty cursor should not error: %v", err)
	}
	if got.TS != nil || got.ID != "" {
		t.Fatalf("empty cursor should be zero: %+v", got)
	}
}

func TestDecodeCursorInvalid(t *testing.T) {
	if _, err := DecodeCursor("not-base64!!!"); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("want ErrInvalidCursor, got %v", err)
	}
	// Valid base64 but not JSON.
	if _, err := DecodeCursor("YWJjZA"); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("want ErrInvalidCursor for non-JSON payload, got %v", err)
	}
}

func TestParseLimit(t *testing.T) {
	cases := []struct {
		name, query string
		want        int
	}{
		{"unset", "", DefaultPageSize},
		{"valid", "limit=42", 42},
		{"zero rejected", "limit=0", DefaultPageSize},
		{"negative rejected", "limit=-5", DefaultPageSize},
		{"garbage rejected", "limit=abc", DefaultPageSize},
		{"over cap clamped", "limit=99999", MaxPageSize},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/?"+tc.query, nil)
			if got := ParseLimit(r); got != tc.want {
				t.Fatalf("ParseLimit(%q) = %d, want %d", tc.query, got, tc.want)
			}
		})
	}
}
