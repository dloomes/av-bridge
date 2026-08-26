package pubapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
)

// Pagination for the public API is cursor-based on (timestamp, id):
// the caller passes ?limit=N&cursor=<opaque> and gets back
// {"data":[...], "next_cursor":"..."|null}. Cursor payloads are
// opaque base64-encoded JSON so we can extend the shape (e.g. add a
// tiebreaker column) without breaking clients — they treat the cursor
// as an unstructured string.
//
// Why cursor instead of offset:
//   * A cursor over an indexed (ts, id) pair is O(log n) regardless of
//     depth; offset pagination degrades on large event tables.
//   * Cursors are stable under concurrent inserts — no duplicate or
//     missed rows if new events arrive between page fetches, which is
//     exactly the situation an integrating system polls in.

// Cursor is the shared shape every list endpoint uses. All fields are
// optional so a fresh listing can start with a nil cursor and only
// populate what it needs (event feeds carry ts+id; static listings
// like buildings/rooms don't paginate at all and skip this helper).
type Cursor struct {
	// TS is the row's timestamp column value, in RFC3339Nano so the
	// encoding survives sub-second precision.
	TS *time.Time `json:"ts,omitempty"`
	// ID is the row's UUID string, used as a secondary key so pages
	// stay stable when many rows share a timestamp.
	ID string `json:"id,omitempty"`
}

// ErrInvalidCursor is returned when a caller-supplied cursor string
// can't be decoded. The handler maps this to a 400 with a message so
// integrators know to drop the cursor and restart pagination.
var ErrInvalidCursor = errors.New("invalid cursor")

// EncodeCursor turns a Cursor into the opaque string returned to the
// caller as next_cursor. Base64-url so it's safe to drop straight into
// a query string without further escaping.
func EncodeCursor(c Cursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeCursor parses an opaque cursor string. Returns ErrInvalidCursor
// on any decode failure — malformed or truncated cursors deserve a
// clear client-side error rather than a silent restart from the top,
// which would surprise a caller that lost their spot.
func DecodeCursor(s string) (Cursor, error) {
	if s == "" {
		return Cursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	var c Cursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	return c, nil
}

// DefaultPageSize + MaxPageSize gate the list endpoints. The default
// is chosen large enough that a dashboard-style caller can grab a full
// screen of rows in one hit, and small enough that a runaway loop
// paging with the max limit still can't hammer the DB.
const (
	DefaultPageSize = 100
	MaxPageSize     = 500
)

// ParseLimit returns the caller's requested page size, clamped to the
// [1, MaxPageSize] range. An unset or invalid value falls back to
// DefaultPageSize rather than 400ing — the friendlier default lets
// quick curl exploration work with just ?cursor=... on the second call.
func ParseLimit(r *http.Request) int {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return DefaultPageSize
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return DefaultPageSize
	}
	if n > MaxPageSize {
		return MaxPageSize
	}
	return n
}

// ParseCursor pulls ?cursor=... off the request and decodes it. Handlers
// call this at the top; on a returned error they should writeErr 400
// with the message from ErrInvalidCursor so the caller can fix and
// retry.
func ParseCursor(r *http.Request) (Cursor, error) {
	return DecodeCursor(r.URL.Query().Get("cursor"))
}

// Page wraps the data slice + next cursor into the envelope every list
// endpoint returns. Kept as a generic map so a page of any element
// type can flow through the same writeJSON path without a type switch.
//
// next_cursor is null when the page IS the last one — a caller
// receiving null stops paging. When present, the caller uses it
// verbatim in ?cursor= on the next call.
func Page(data any, next *string) map[string]any {
	out := map[string]any{"data": data}
	if next != nil {
		out["next_cursor"] = *next
	} else {
		out["next_cursor"] = nil
	}
	return out
}
