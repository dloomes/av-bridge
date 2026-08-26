package pubapi

import (
	_ "embed"
	"net/http"
	"strings"
)

// OpenAPI + Swagger UI. Both endpoints are unauthenticated:
//   * openapi.json is the machine-readable contract. Integrators
//     import it into Postman/Insomnia BEFORE they have a token, so
//     gating it defeats the point.
//   * docs is Swagger UI pointed at the JSON above — same rationale.
//     "Try it out" still needs a real bearer token from Settings →
//     API tokens, so nothing is actually callable without auth.
//
// The spec is a hand-written JSON file kept alongside this package
// (see openapi.json). go:embed pulls it into the binary at compile
// time so the deployed cloud image serves the exact version that
// shipped — no risk of the on-disk spec drifting.

//go:embed openapi.json
var openapiSpec []byte

// OpenAPISpec — GET /pub/v1/openapi.json
//
// Serves the spec verbatim. cache: 5 minutes at the edge — the spec
// only changes on a new release, and we want a client's tooling to
// reflect the change on the next reload without hammering the cloud
// on every dev interaction.
func (h *Handler) OpenAPISpec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_, _ = w.Write(openapiSpec)
}

// SwaggerUI — GET /pub/v1/docs
//
// Single self-contained HTML page that mounts Swagger UI against
// /pub/v1/openapi.json. Assets load from the jsdelivr CDN — the
// alternative is embedding ~1.5MB of Swagger CSS/JS in the binary,
// which is a lot to carry for a page most callers only visit once.
// If offline browsing becomes a requirement we can flip this to
// embedded assets later.
//
// Same-origin for the spec URL keeps the "Try it out" button working
// without CORS gymnastics on the caller's side.
func (h *Handler) SwaggerUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; "+
			"style-src 'self' https://cdn.jsdelivr.net 'unsafe-inline'; "+
			"script-src 'self' https://cdn.jsdelivr.net 'unsafe-inline'; "+
			"img-src 'self' data: https://cdn.jsdelivr.net; "+
			"font-src 'self' data: https://cdn.jsdelivr.net; "+
			"connect-src 'self'")
	_, _ = w.Write([]byte(swaggerHTML))
}

// Version-pinned CDN URLs so a swagger-ui release doesn't silently
// alter the docs page. Bump the pin deliberately when we want the
// upstream fixes.
const swaggerHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>av-bridge API</title>
  <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14/swagger-ui.css" />
  <style>
    body { margin: 0; background: #fafafa; }
    .topbar { display: none; }
  </style>
</head>
<body>
  <div id="swagger"></div>
  <script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14/swagger-ui-bundle.js"></script>
  <script>
    window.addEventListener("load", function () {
      window.ui = SwaggerUIBundle({
        url: "/pub/v1/openapi.json",
        dom_id: "#swagger",
        deepLinking: true,
        docExpansion: "list",
        persistAuthorization: true
      });
    });
  </script>
</body>
</html>`

// StripTrailingSlash normalises /pub/v1/docs/ → /pub/v1/docs so both
// paths hit the SwaggerUI handler. Go's ServeMux distinguishes them,
// which surprises callers who type the trailing slash out of habit.
// Not currently wired — kept for future use if we mount a /docs sub-
// tree with an index and want the bare form to redirect.
func StripTrailingSlash(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") && r.URL.Path != "/" {
			r.URL.Path = strings.TrimRight(r.URL.Path, "/")
		}
		next.ServeHTTP(w, r)
	})
}
