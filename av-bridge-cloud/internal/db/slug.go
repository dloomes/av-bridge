package db

import (
	"errors"
	"regexp"
	"strings"
)

// slugRE mirrors the customers_slug_format CHECK constraint from migration
// 0030: 3-50 chars, [a-z0-9-], must start and end alphanumeric. Enforced at
// the app layer so validation errors come back as 400 instead of a raw
// SQLSTATE 23514 from the DB.
var slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,48}[a-z0-9]$`)

// reservedSlugs are hostnames the routing layer needs for its own purposes —
// letting a customer claim any of these would either shadow a real subdomain
// (app / api / helpdesk) or collide with common infra names. Kept in sync
// with the portal-side list in av-bridge-portal/src/lib/branding-slug.ts.
var reservedSlugs = map[string]bool{
	"app":       true,
	"www":       true,
	"api":       true,
	"admin":     true,
	"helpdesk":  true,
	"staging":   true,
	"preview":   true,
	"localhost": true,
	"dev":       true,
	"test":      true,
	"demo":      true,
	"support":   true,
}

// ErrSlugReserved is returned when the caller tries to set a slug that is
// permanently unavailable (routing conflict). Kept as a sentinel so the HTTP
// layer can return 409 instead of 400.
var ErrSlugReserved = errors.New("slug is reserved and cannot be assigned to a customer")

// NormalizeSlug lower-cases + trims a user-supplied slug. Empty in, empty out
// (callers use "" to mean "clear the slug" on update). Doesn't validate — that
// is ValidateSlug's job.
func NormalizeSlug(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// ValidateSlug returns nil for empty (meaning "unset"), a friendly error for
// malformed input, or ErrSlugReserved for a routing-conflict. Callers must
// normalize first (NormalizeSlug) — this function trusts its input is
// already lower-cased.
func ValidateSlug(s string) error {
	if s == "" {
		return nil // empty is valid — represents "no slug set"
	}
	if !slugRE.MatchString(s) {
		return errors.New("slug must be 3-50 characters, lowercase letters/digits/hyphens only, starting and ending alphanumeric")
	}
	if reservedSlugs[s] {
		return ErrSlugReserved
	}
	return nil
}
