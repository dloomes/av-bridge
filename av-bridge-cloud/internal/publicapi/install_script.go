package publicapi

import (
	_ "embed"
	"net/http"
	"strings"
)

// GET /public/collectors/install.sh — the bootstrap script the portal's
// one-liner pipes into bash on the target Linux box. Served from the
// same origin that redeems the token (via /public/collectors/enroll) so
// there's exactly ONE hostname involved in on-boarding and the bridge's
// cloud URL is derived automatically. TLS is assumed for the origin
// the script came from — the portal proxy inherits Amplify's cert.
//
// The template embeds the cloud base URL at request-time. Everything
// else is static bash.

//go:embed install_script.sh
var installScriptTmpl string

// ServeInstallScript writes the bootstrap script with the cloud URL
// substituted in. Content-type text/x-shellscript so curl users can
// eyeball it as text; the pipe-to-bash path doesn't care.
func (h *Handler) ServeInstallScript(w http.ResponseWriter, r *http.Request) {
	// Prefer the configured cloud base URL; fall back to the request
	// scheme + host so an operator hitting the endpoint from a local
	// dev deploy still gets a usable script. Never emit a raw
	// {{CLOUD_BASE_URL}} placeholder — the shell script wouldn't run.
	base := strings.TrimRight(h.cloudBaseURL, "/")
	if base == "" {
		scheme := "https"
		if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
			scheme = p
		} else if r.TLS == nil {
			scheme = "http"
		}
		host := r.Host
		if hv := r.Header.Get("X-Forwarded-Host"); hv != "" {
			host = hv
		}
		base = scheme + "://" + host
	}

	body := strings.ReplaceAll(installScriptTmpl, "@@CLOUD_BASE_URL@@", base)

	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	// Short cache lets the same script survive on CloudFront for a few
	// minutes but doesn't lock in an old cloud URL if we ever rotate.
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write([]byte(body))
}
