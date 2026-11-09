package roadmap

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Share links let an editor show the roadmap to someone outside the portal.
// The token is "rmp_" + base64url(32 random bytes); only its SHA-256 hex is
// stored.

const (
	tokenPrefix      = "rmp_"
	tokenLen         = len(tokenPrefix) + 43 // 32 bytes, unpadded base64url
	defaultLinkDays  = 90
	maxLinkDays      = 365
	maxLinkNameRunes = 80
)

// ErrLinkNotFound is a share link id that does not exist.
var ErrLinkNotFound = errors.New("share link not found")

// Link is a share link as editors see it. The token is never part of it.
type Link struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	ExpiresAt  time.Time  `json:"expires_at"`
	CreatedBy  string     `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	UseCount   int        `json:"use_count"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

// LinkInput is the body of POST /api/v1/helpdesk/roadmap/links.
// ExpiresInDays is optional (90 when omitted), 1-365.
type LinkInput struct {
	Name          string `json:"name"`
	ExpiresInDays *int   `json:"expires_in_days"`
}

// NewToken returns a fresh share token and its SHA-256 hex.
func NewToken() (token, hash string) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand never fails on supported platforms.
		panic("roadmap: crypto/rand: " + err.Error())
	}
	token = tokenPrefix + base64.RawURLEncoding.EncodeToString(b[:])
	return token, HashToken(token)
}

// HashToken is the SHA-256 hex stored in roadmap_share_links.token_hash.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// LooksLikeToken reports whether s has the shape of a share token, so the
// public endpoint can answer junk without a database round-trip.
func LooksLikeToken(s string) bool {
	if len(s) != tokenLen || !strings.HasPrefix(s, tokenPrefix) {
		return false
	}
	_, err := base64.RawURLEncoding.Strict().DecodeString(s[len(tokenPrefix):])
	return err == nil
}

func linkExpiryDays(in *int) (int, error) {
	if in == nil {
		return defaultLinkDays, nil
	}
	if *in < 1 || *in > maxLinkDays {
		return 0, &ValidationError{Msg: "Expiry must be between 1 and 365 days."}
	}
	return *in, nil
}

const linkColumns = `id::text, name, expires_at, created_by, created_at, last_used_at, use_count, revoked_at`

func scanLink(row pgx.Row) (Link, error) {
	var l Link
	err := row.Scan(&l.ID, &l.Name, &l.ExpiresAt, &l.CreatedBy, &l.CreatedAt, &l.LastUsedAt, &l.UseCount, &l.RevokedAt)
	return l, err
}

// ListLinks returns every share link, newest first. Never nil.
func ListLinks(ctx context.Context, q pgx.Tx) ([]Link, error) {
	rows, err := q.Query(ctx, `SELECT `+linkColumns+` FROM roadmap_share_links ORDER BY created_at DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Link{}
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// CreateLink stores a new link and returns it with its token, which is
// shown once and never stored.
func CreateLink(ctx context.Context, q pgx.Tx, in LinkInput, by string) (Link, string, error) {
	name := strings.TrimSpace(in.Name)
	if n := utf8.RuneCountInString(name); n < 1 || n > maxLinkNameRunes {
		return Link{}, "", &ValidationError{Msg: "Name must be 1 to 80 characters."}
	}
	days, err := linkExpiryDays(in.ExpiresInDays)
	if err != nil {
		return Link{}, "", err
	}
	token, hash := NewToken()
	l, err := scanLink(q.QueryRow(ctx, `
		INSERT INTO roadmap_share_links (name, token_hash, expires_at, created_by)
		VALUES ($1, $2, now() + make_interval(days => $3), $4)
		RETURNING `+linkColumns, name, hash, days, by))
	if err != nil {
		return Link{}, "", err
	}
	return l, token, nil
}

// RevokeLink marks a link revoked. Revoking twice keeps the first time.
func RevokeLink(ctx context.Context, q pgx.Tx, id string) error {
	tag, err := q.Exec(ctx, `UPDATE roadmap_share_links SET revoked_at = COALESCE(revoked_at, now()) WHERE id::text = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrLinkNotFound
	}
	return nil
}

// DeleteLink removes a link.
func DeleteLink(ctx context.Context, q pgx.Tx, id string) error {
	tag, err := q.Exec(ctx, `DELETE FROM roadmap_share_links WHERE id::text = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrLinkNotFound
	}
	return nil
}

// OpenLink looks a token up. It is valid only if not revoked and not yet
// expired; a valid open bumps last_used_at and use_count in the same
// statement. ok is false for unknown, expired and revoked tokens alike.
func OpenLink(ctx context.Context, q pgx.Tx, token string) (Link, bool, error) {
	if !LooksLikeToken(token) {
		return Link{}, false, nil
	}
	l, err := scanLink(q.QueryRow(ctx, `
		UPDATE roadmap_share_links
		   SET last_used_at = now(), use_count = use_count + 1
		 WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > now()
		 RETURNING `+linkColumns, HashToken(token)))
	if errors.Is(err, pgx.ErrNoRows) {
		return Link{}, false, nil
	}
	if err != nil {
		return Link{}, false, err
	}
	return l, true, nil
}
