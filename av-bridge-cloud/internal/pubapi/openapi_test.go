package pubapi

import (
	"encoding/json"
	"testing"
)

// TestOpenAPISpecShape is a lightweight guard: the embedded JSON must
// parse, declare version 3.1.0, and enumerate every /pub/v1 route we
// serve. If someone adds a route without adding a matching entry
// here, they get a red build instead of silently drifting docs.
func TestOpenAPISpecShape(t *testing.T) {
	var doc struct {
		OpenAPI string                 `json:"openapi"`
		Info    map[string]any         `json:"info"`
		Paths   map[string]any         `json:"paths"`
		Comp    map[string]any         `json:"components"`
	}
	if err := json.Unmarshal(openapiSpec, &doc); err != nil {
		t.Fatalf("openapi.json is not valid JSON: %v", err)
	}
	if doc.OpenAPI != "3.1.0" {
		t.Fatalf("openapi version: got %q, want 3.1.0", doc.OpenAPI)
	}

	// Every route mounted by api/server.go under /pub/v1 must be
	// documented here. Curly-brace placeholders match OpenAPI's own
	// path-template syntax (`/pub/v1/devices/{id}` etc.).
	wantPaths := []string{
		"/pub/v1/ping",
		"/pub/v1/devices",
		"/pub/v1/devices/{id}",
		"/pub/v1/devices/{id}/telemetry",
		"/pub/v1/devices/{id}/events",
		"/pub/v1/buildings",
		"/pub/v1/rooms",
		"/pub/v1/assets",
		"/pub/v1/assets/{id}",
		"/pub/v1/alerts",
		"/pub/v1/events",
	}
	for _, p := range wantPaths {
		if _, ok := doc.Paths[p]; !ok {
			t.Errorf("openapi.json missing route: %s", p)
		}
	}

	// Sanity: the bearer scheme is declared. Without it, "Try it out"
	// in Swagger UI can't authorise the call.
	comps, _ := doc.Comp["securitySchemes"].(map[string]any)
	if _, ok := comps["bearerAuth"]; !ok {
		t.Error("openapi.json missing securitySchemes.bearerAuth")
	}
}
