package portalapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/dloomes/av-bridge-cloud/internal/audit"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/jackc/pgx/v5"
)

// Branding is stored on the customers row (see migration 0021). Reads are
// open to any authenticated tenant user because the whole portal renders in
// that customer's colours — gating the read serves no purpose and would
// force the sign-in page to fall back to unbranded. Writes gate on
// branding.update.

// Cap on stored logo size. Data URLs are ~33% bigger than the underlying
// bytes; a 256KB byte cap comfortably fits a favicon-sized PNG or a compact
// SVG mark. Anything larger belongs in blob storage, not on the row.
const maxLogoBytes = 256 * 1024

// Cap on the sign-in hero image. Wider than the logo cap because a hero
// covers the whole viewport; 1MB fits a well-compressed 1600x900 JPEG or a
// WebP. Anything larger really needs a blob store.
const maxHeroBytes = 1024 * 1024

// Length caps for the free-text branding fields — mirror the CHECK
// constraints in migration 0032 so validation is consistent both layers.
const (
	maxSignInMessageLen = 500
	maxSupportContact   = 200
	maxSSOButtonLabel   = 60
)

// Allowlist for content types we're willing to render as an <img src=> on
// the portal. SVG is included but callers should be aware SVGs can carry
// script — the handler strips <script> tags before storing (defense in
// depth; the portal renders logos inside <img> where scripts don't
// execute, but a leaked cross-origin fetch could still hurt).
var allowedLogoTypes = map[string]bool{
	"image/png":     true,
	"image/jpeg":    true,
	"image/svg+xml": true,
}

// Hero image types — WebP added since it's the modern default for
// full-viewport imagery. SVG still allowed; the same <script> strip runs.
var allowedHeroTypes = map[string]bool{
	"image/png":     true,
	"image/jpeg":    true,
	"image/svg+xml": true,
	"image/webp":    true,
}

// GetBranding — GET /api/v1/branding
//
// Returns the caller's tenant branding. All four fields are optional; a
// fresh customer has NULLs across the board and the portal falls back to
// its default look.
func (h *Handler) GetBranding(w http.ResponseWriter, r *http.Request) {
	type out struct {
		DisplayName     string `json:"display_name,omitempty"`
		AccentColor     string `json:"accent_color,omitempty"`
		LogoDataURL     string `json:"logo_data_url,omitempty"`
		SignInMessage   string `json:"sign_in_message,omitempty"`
		SupportContact  string `json:"support_contact,omitempty"`
		SSOButtonLabel  string `json:"sso_button_label,omitempty"`
		SignInHeroURL   string `json:"sign_in_hero_data_url,omitempty"`
	}
	var o out
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		var (
			displayName    *string
			accentColor    *string
			logoType       *string
			logo           []byte
			signInMessage  *string
			supportContact *string
			ssoButtonLabel *string
			heroType       *string
			hero           []byte
		)
		p, _ := portalauth.From(r.Context())
		if err := tx.QueryRow(ctx, `
			SELECT display_name, accent_color, logo_content_type, logo,
			       sign_in_message, support_contact, sso_button_label,
			       sign_in_hero_content_type, sign_in_hero
			  FROM customers WHERE id = $1`, p.CustomerID,
		).Scan(&displayName, &accentColor, &logoType, &logo,
			&signInMessage, &supportContact, &ssoButtonLabel,
			&heroType, &hero); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// No customer row visible under RLS — treat as unbranded so
				// the sign-in page still renders. Shouldn't happen for a
				// resolved principal, but fail-open on read is safer.
				return nil
			}
			return err
		}
		if displayName != nil {
			o.DisplayName = *displayName
		}
		if accentColor != nil {
			o.AccentColor = *accentColor
		}
		if logoType != nil && len(logo) > 0 {
			o.LogoDataURL = "data:" + *logoType + ";base64," +
				base64.StdEncoding.EncodeToString(logo)
		}
		if signInMessage != nil {
			o.SignInMessage = *signInMessage
		}
		if supportContact != nil {
			o.SupportContact = *supportContact
		}
		if ssoButtonLabel != nil {
			o.SSOButtonLabel = *ssoButtonLabel
		}
		if heroType != nil && len(hero) > 0 {
			o.SignInHeroURL = "data:" + *heroType + ";base64," +
				base64.StdEncoding.EncodeToString(hero)
		}
		return nil
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, o)
}

// updateBrandingReq is pointer-per-field so we can distinguish "not in
// payload" from "clear to null". An explicit empty string clears the field;
// omitting it leaves the stored value alone.
type updateBrandingReq struct {
	DisplayName      *string `json:"display_name,omitempty"`
	AccentColor      *string `json:"accent_color,omitempty"`
	LogoDataURL      *string `json:"logo_data_url,omitempty"`
	SignInMessage    *string `json:"sign_in_message,omitempty"`
	SupportContact   *string `json:"support_contact,omitempty"`
	SSOButtonLabel   *string `json:"sso_button_label,omitempty"`
	SignInHeroURL    *string `json:"sign_in_hero_data_url,omitempty"`
}

// UpdateBranding — PATCH /api/v1/branding
//
// Gated on branding.update at the route layer. Vendor callers with
// X-Customer-Scope set pass through as usual (vendor bypass).
func (h *Handler) UpdateBranding(w http.ResponseWriter, r *http.Request) {
	var req updateBrandingReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// Validate + normalize before the tx. We want to reject bad input with
	// a 400, not a 500 from a constraint violation.
	var normalizedAccent *string
	if req.AccentColor != nil {
		trimmed := strings.TrimSpace(*req.AccentColor)
		if trimmed != "" {
			if !isValidHexColor(trimmed) {
				writeErr(w, http.StatusBadRequest, "accent_color must be #RGB or #RRGGBB hex")
				return
			}
			trimmed = strings.ToLower(trimmed)
			normalizedAccent = &trimmed
		} else {
			empty := ""
			normalizedAccent = &empty
		}
	}

	var (
		logoBytes []byte
		logoType  string
		logoClear bool
	)
	if req.LogoDataURL != nil {
		if *req.LogoDataURL == "" {
			logoClear = true
		} else {
			b, ct, err := decodeLogoDataURL(*req.LogoDataURL)
			if err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			logoBytes = b
			logoType = ct
		}
	}

	// Sign-in hero decode — separate allowlist + size cap from the logo.
	var (
		heroBytes []byte
		heroType  string
		heroClear bool
	)
	if req.SignInHeroURL != nil {
		if *req.SignInHeroURL == "" {
			heroClear = true
		} else {
			b, ct, err := decodeImageDataURL(*req.SignInHeroURL, allowedHeroTypes, maxHeroBytes, "sign-in hero")
			if err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			heroBytes = b
			heroType = ct
		}
	}

	// Length validation for the free-text fields. Trim first so a caller
	// posting all whitespace hits the same "clear" path as an empty string.
	if req.SignInMessage != nil {
		if len(strings.TrimSpace(*req.SignInMessage)) > maxSignInMessageLen {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("sign_in_message exceeds %d chars", maxSignInMessageLen))
			return
		}
	}
	if req.SupportContact != nil {
		if len(strings.TrimSpace(*req.SupportContact)) > maxSupportContact {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("support_contact exceeds %d chars", maxSupportContact))
			return
		}
	}
	if req.SSOButtonLabel != nil {
		if len(strings.TrimSpace(*req.SSOButtonLabel)) > maxSSOButtonLabel {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("sso_button_label exceeds %d chars", maxSSOButtonLabel))
			return
		}
	}

	p, _ := portalauth.From(r.Context())

	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		// Build the UPDATE dynamically so absent fields don't overwrite
		// stored values. RLS on customers restricts the row to the caller's
		// tenant (or vendor's X-Customer-Scope).
		set := []string{}
		args := []any{p.CustomerID}
		add := func(col string, val any) {
			args = append(args, val)
			set = append(set, fmt.Sprintf("%s = $%d", col, len(args)))
		}
		if req.DisplayName != nil {
			v := strings.TrimSpace(*req.DisplayName)
			if v == "" {
				add("display_name", nil)
			} else {
				add("display_name", v)
			}
		}
		if normalizedAccent != nil {
			if *normalizedAccent == "" {
				add("accent_color", nil)
			} else {
				add("accent_color", *normalizedAccent)
			}
		}
		if req.LogoDataURL != nil {
			if logoClear {
				add("logo", nil)
				add("logo_content_type", nil)
			} else {
				add("logo", logoBytes)
				add("logo_content_type", logoType)
			}
		}
		if req.SignInMessage != nil {
			v := strings.TrimSpace(*req.SignInMessage)
			if v == "" {
				add("sign_in_message", nil)
			} else {
				add("sign_in_message", v)
			}
		}
		if req.SupportContact != nil {
			v := strings.TrimSpace(*req.SupportContact)
			if v == "" {
				add("support_contact", nil)
			} else {
				add("support_contact", v)
			}
		}
		if req.SSOButtonLabel != nil {
			v := strings.TrimSpace(*req.SSOButtonLabel)
			if v == "" {
				add("sso_button_label", nil)
			} else {
				add("sso_button_label", v)
			}
		}
		if req.SignInHeroURL != nil {
			if heroClear {
				add("sign_in_hero", nil)
				add("sign_in_hero_content_type", nil)
			} else {
				add("sign_in_hero", heroBytes)
				add("sign_in_hero_content_type", heroType)
			}
		}
		if len(set) == 0 {
			// No-op PATCH — treat as success rather than 400 so the portal
			// can send a save even when nothing changed.
			return nil
		}
		sql := "UPDATE customers SET " + strings.Join(set, ", ") + " WHERE id = $1"
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return err
		}
		// Audit — capture what changed (not the image bytes themselves).
		auditPayload := map[string]any{}
		if req.DisplayName != nil {
			auditPayload["display_name"] = *req.DisplayName
		}
		if normalizedAccent != nil {
			auditPayload["accent_color"] = *normalizedAccent
		}
		if req.LogoDataURL != nil {
			if logoClear {
				auditPayload["logo"] = "cleared"
			} else {
				auditPayload["logo"] = fmt.Sprintf("uploaded (%d bytes, %s)", len(logoBytes), logoType)
			}
		}
		if req.SignInMessage != nil {
			auditPayload["sign_in_message"] = strings.TrimSpace(*req.SignInMessage)
		}
		if req.SupportContact != nil {
			auditPayload["support_contact"] = strings.TrimSpace(*req.SupportContact)
		}
		if req.SSOButtonLabel != nil {
			auditPayload["sso_button_label"] = strings.TrimSpace(*req.SSOButtonLabel)
		}
		if req.SignInHeroURL != nil {
			if heroClear {
				auditPayload["sign_in_hero"] = "cleared"
			} else {
				auditPayload["sign_in_hero"] = fmt.Sprintf("uploaded (%d bytes, %s)", len(heroBytes), heroType)
			}
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action:     "branding.update",
			TargetKind: "customer", TargetID: p.CustomerID,
			After: mustJSON(auditPayload),
		}))
	})
	if !ok {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// isValidHexColor accepts #RGB and #RRGGBB (case-insensitive). Matches the
// migration's CHECK constraint so validation is consistent between the
// two layers.
func isValidHexColor(s string) bool {
	if len(s) != 4 && len(s) != 7 {
		return false
	}
	if s[0] != '#' {
		return false
	}
	for _, c := range s[1:] {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// decodeLogoDataURL parses the logo upload. Delegates to decodeImageDataURL
// with the logo-specific allowlist + size cap; kept as its own name so the
// call site reads intent-first.
func decodeLogoDataURL(dataURL string) ([]byte, string, error) {
	return decodeImageDataURL(dataURL, allowedLogoTypes, maxLogoBytes, "logo")
}

// decodeImageDataURL parses a "data:<content-type>;base64,<payload>" string
// against a caller-supplied allowlist and byte cap. Returns the raw bytes,
// the content-type, or an error suitable for a 400. Strips <script> tags
// from SVG bytes before returning — cheap defense in depth against an SVG
// with embedded JavaScript getting rendered anywhere that isn't an <img>.
//
// The `field` argument prefixes error messages so a 400 reads
// "sign-in hero exceeds 1048576 byte cap" rather than a generic "image
// exceeds cap" that would leave a caller guessing which upload failed.
func decodeImageDataURL(dataURL string, allowed map[string]bool, maxBytes int, field string) ([]byte, string, error) {
	const prefix = "data:"
	if !strings.HasPrefix(dataURL, prefix) {
		return nil, "", fmt.Errorf("%s must be a data URI", field)
	}
	rest := dataURL[len(prefix):]
	semi := strings.Index(rest, ";")
	comma := strings.Index(rest, ",")
	if semi < 0 || comma < 0 || comma < semi {
		return nil, "", fmt.Errorf("%s data URI is malformed", field)
	}
	contentType := rest[:semi]
	encoding := rest[semi+1 : comma]
	payload := rest[comma+1:]
	if encoding != "base64" {
		return nil, "", fmt.Errorf("%s must be base64-encoded", field)
	}
	if !allowed[contentType] {
		return nil, "", fmt.Errorf("%s content type %q not allowed", field, contentType)
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, "", fmt.Errorf("%s is not valid base64", field)
	}
	if len(raw) > maxBytes {
		return nil, "", fmt.Errorf("%s exceeds %d byte cap", field, maxBytes)
	}
	if contentType == "image/svg+xml" {
		raw = stripSVGScripts(raw)
	}
	return raw, contentType, nil
}

// stripSVGScripts removes <script>…</script> blocks from SVG bytes,
// case-insensitively. Not a full XML parser — a hardened tenant would use
// a proper sanitizer (bluemonday etc.). This is enough for the "we uploaded
// a designer's SVG that happens to contain analytics script tags" case.
func stripSVGScripts(b []byte) []byte {
	s := string(b)
	// Repeatedly find and strip <script...>...</script> blocks. Case-fold.
	for {
		lower := strings.ToLower(s)
		open := strings.Index(lower, "<script")
		if open < 0 {
			break
		}
		close := strings.Index(lower[open:], "</script>")
		if close < 0 {
			// unmatched — strip from open to end so we don't leave a
			// dangling tag the browser might try to interpret.
			s = s[:open]
			break
		}
		end := open + close + len("</script>")
		s = s[:open] + s[end:]
	}
	return []byte(s)
}
