package publicapi

import (
	_ "embed"
	"net/http"
	"regexp"
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

//go:embed install_script.ps1
var installScriptPsTmpl string

// resolveCloudBase derives the cloud base URL that should be baked into
// an install script. Preference order: explicit config → request-time
// scheme + host (via X-Forwarded-Proto / X-Forwarded-Host when Amplify
// sits in front). Never emits an unresolved @@CLOUD_BASE_URL@@ token.
//
// ok is false when the request-derived origin isn't a plain
// http(s)://host[:port]. The script is piped into a shell and cached
// publicly, so a forwarded header must never put anything else into it.
func (h *Handler) resolveCloudBase(r *http.Request) (base string, ok bool) {
	base = strings.TrimRight(h.cloudBaseURL, "/")
	if base != "" {
		return base, true
	}
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
	if (scheme != "http" && scheme != "https") || !safeHost.MatchString(host) {
		return "", false
	}
	return scheme + "://" + host, true
}

// safeHost is a DNS name or IPv4 address with an optional port: nothing a
// shell or PowerShell would interpret.
var safeHost = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?(?::[0-9]{1,5})?$`)

// installScript fills in the cloud URL, or answers 400 when there is no
// safe one to use.
func (h *Handler) installScript(w http.ResponseWriter, r *http.Request, tmpl string) (string, bool) {
	base, ok := h.resolveCloudBase(r)
	if !ok {
		http.Error(w, "cannot determine the cloud URL for this request", http.StatusBadRequest)
		return "", false
	}
	return strings.ReplaceAll(tmpl, "@@CLOUD_BASE_URL@@", base), true
}

// ServeInstallScript writes the Linux bootstrap script with the cloud
// URL substituted in. Content-type text/x-shellscript so curl users can
// eyeball it as text; the pipe-to-bash path doesn't care.
func (h *Handler) ServeInstallScript(w http.ResponseWriter, r *http.Request) {
	body, ok := h.installScript(w, r, installScriptTmpl)
	if !ok {
		return
	}
	// bash reads a CRLF file's `set -euo pipefail` as "pipefail\r" and
	// dies. .gitattributes pins *.sh to LF, but a checkout made with
	// different settings would still embed CRLF — so normalise here too.
	body = strings.ReplaceAll(body, "\r\n", "\n")
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	// Short cache lets the same script survive on CloudFront for a few
	// minutes but doesn't lock in an old cloud URL if we ever rotate.
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write([]byte(body)) // #nosec G705 -- served as text/x-shellscript, not HTML; the only request-derived value (the cloud origin) is validated by resolveCloudBase
}

// ServeInstallScriptPS writes the Windows PowerShell bootstrap. Same
// contract as the bash flavour: one endpoint, one substitution. The
// content-type is text/plain because there's no widely-accepted
// PowerShell MIME type and text/plain lets `iwr | iex` work unchanged.
func (h *Handler) ServeInstallScriptPS(w http.ResponseWriter, r *http.Request) {
	body, ok := h.installScript(w, r, installScriptPsTmpl)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write([]byte(body)) // #nosec G705 -- served as text/plain, not HTML; the only request-derived value (the cloud origin) is validated by resolveCloudBase
}
