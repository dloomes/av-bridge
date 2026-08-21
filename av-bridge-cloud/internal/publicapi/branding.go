// Package publicapi serves endpoints that are safe to expose without auth.
// Today: /public/branding?slug=<slug> returns display_name + accent_color +
// logo_data_url for the customer whose slug matches, so a sign-in page
// hosted at <slug>.<env>.involvecloud.com can render pre-login branding
// before the user has any credentials to hand over.
//
// Nothing sensitive is exposed — brand assets are meant to be seen. Slug
// enumeration isn't a concern either: subdomains ARE the shareable handle
// customers link people at, so their existence is public by design.
package publicapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/jackc/pgx/v5"
)

// Mirrors the CHECK constraint from migration 0030. Enforced here so a
// malformed ?slug=... never even hits the DB.
var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,48}[a-z0-9]$`)

type Handler struct {
	store *db.Store
	log   *slog.Logger
}

func NewHandler(store *db.Store, log *slog.Logger) *Handler {
	return &Handler{store: store, log: log}
}

// brandingResp matches the portal's Branding type in
// av-bridge-portal/src/lib/api.ts so the same client code paths that
// consume /api/v1/branding can consume this endpoint too. Includes the
// sign-in surface fields added in migration 0032 so a customer's tagline,
// support line, hero image and SSO button label all render at the
// unauthenticated sign-in page.
type brandingResp struct {
	DisplayName    string `json:"display_name,omitempty"`
	AccentColor    string `json:"accent_color,omitempty"`
	LogoDataURL    string `json:"logo_data_url,omitempty"`
	SignInMessage  string `json:"sign_in_message,omitempty"`
	SupportContact string `json:"support_contact,omitempty"`
	SSOButtonLabel string `json:"sso_button_label,omitempty"`
	SignInHeroURL  string `json:"sign_in_hero_data_url,omitempty"`
}

// GetBranding — GET /public/branding?slug=<slug>
//
// Always returns 200 with a possibly-empty object. Missing slug, malformed
// slug, unknown slug, and customer-with-no-branding all yield the same
// empty response so the client (typically the Next.js sign-in server
// component) has one code path: "hydrate whatever came back, fall back to
// defaults for missing fields."
//
// Uses the admin pool (BYPASSRLS) because we have no tenant context on a
// pre-login request. Safe: the SELECT only ever exposes brand fields that
// are already public-facing by intent.
func (h *Handler) GetBranding(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	// Amplify's CloudFront honours Cache-Control, so branding lookups don't
	// hammer the DB on every sign-in page load. Five minutes is short enough
	// that a rebrand still lands within a coffee break.
	w.Header().Set("Cache-Control", "public, max-age=300")

	writeEmpty := func() { _ = json.NewEncoder(w).Encode(brandingResp{}) }

	slug := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("slug")))
	if slug == "" || !slugRe.MatchString(slug) {
		writeEmpty()
		return
	}

	var (
		displayName, accentColor, logoType string
		logo                               []byte
		signInMessage, supportContact      string
		ssoButtonLabel                     string
		heroType                           string
		hero                               []byte
	)
	err := h.store.AdminPool().QueryRow(r.Context(), `
		SELECT COALESCE(display_name,''),
		       COALESCE(accent_color,''),
		       COALESCE(logo_content_type,''),
		       logo,
		       COALESCE(sign_in_message,''),
		       COALESCE(support_contact,''),
		       COALESCE(sso_button_label,''),
		       COALESCE(sign_in_hero_content_type,''),
		       sign_in_hero
		  FROM customers
		 WHERE slug = $1`, slug,
	).Scan(&displayName, &accentColor, &logoType, &logo,
		&signInMessage, &supportContact, &ssoButtonLabel, &heroType, &hero)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			// Log the anomaly but keep the response uniform so the client
			// can't tell "slug missing" from "DB blew up".
			h.log.Error("public branding lookup failed", "slug", slug, "error", err)
		}
		writeEmpty()
		return
	}

	resp := brandingResp{
		DisplayName:    displayName,
		AccentColor:    accentColor,
		SignInMessage:  signInMessage,
		SupportContact: supportContact,
		SSOButtonLabel: ssoButtonLabel,
	}
	if logoType != "" && len(logo) > 0 {
		resp.LogoDataURL = "data:" + logoType + ";base64," +
			base64.StdEncoding.EncodeToString(logo)
	}
	if heroType != "" && len(hero) > 0 {
		resp.SignInHeroURL = "data:" + heroType + ";base64," +
			base64.StdEncoding.EncodeToString(hero)
	}
	_ = json.NewEncoder(w).Encode(resp)
}
