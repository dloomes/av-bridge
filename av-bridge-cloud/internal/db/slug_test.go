package db

import (
	"errors"
	"testing"
)

func TestReservedSlugs(t *testing.T) {
	for _, s := range []string{"status", "access", "notify", "uat", "portal"} {
		if err := ValidateSlug(s); !errors.Is(err, ErrSlugReserved) {
			t.Errorf("%q: got %v, want ErrSlugReserved", s, err)
		}
	}
	if err := ValidateSlug("acme"); err != nil {
		t.Errorf("acme: %v", err)
	}
}
