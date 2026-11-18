// Package brandurl builds a customer's branded sign-in origin.
package brandurl

import (
	"net/url"
	"strings"
)

// Origin returns the origin (no trailing slash) a customer with the given
// slug signs in at. portalBaseURL empty returns "". An empty slug returns the
// portal base. With brandedDomain set the result is <scheme>://<slug>.<domain>;
// otherwise the first host label of the portal base is replaced by the slug
// (https://app.uat.x.com + acme -> https://acme.uat.x.com).
func Origin(portalBaseURL, brandedDomain, slug string) string {
	if portalBaseURL == "" {
		return ""
	}
	trimmed := strings.TrimRight(portalBaseURL, "/")
	if slug == "" {
		return trimmed
	}
	u, err := url.Parse(portalBaseURL)
	if err != nil || u.Host == "" {
		return trimmed
	}
	if d := strings.Trim(strings.TrimSpace(brandedDomain), "."); d != "" {
		u.Host = slug + "." + d
		return strings.TrimRight(u.String(), "/")
	}
	parts := strings.SplitN(u.Host, ".", 2)
	if len(parts) < 2 {
		return trimmed
	}
	u.Host = slug + "." + parts[1]
	return strings.TrimRight(u.String(), "/")
}
