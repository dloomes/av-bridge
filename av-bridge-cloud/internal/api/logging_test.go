package api

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A roadmap share token in the path is a live credential: the request log
// must never contain it.
func TestRequestLogRedactsRoadmapToken(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	srv := NewServer(":0", http.NotFoundHandler(), nil, nil, nil, BridgeCommandRoutes{}, BridgeConfigRoutes{},
		PublicRoutes{RoadmapShared: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}}, nil, log)

	const token = "rmp_SECRETSECRETSECRETSECRETSECRETSECRETSECRE"
	for _, c := range []struct{ method, path string }{
		{"GET", "/public/roadmap/" + token},
		{"POST", "/public/roadmap/" + token},       // 405 still logged
		{"GET", "/public/roadmap/" + token + "/x"}, // unmatched still logged
	} {
		rec := httptest.NewRecorder()
		srv.Handler.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
	}
	out := buf.String()
	if strings.Contains(out, "SECRET") {
		t.Fatalf("request log contains the token:\n%s", out)
	}
	if strings.Count(out, "path=/public/roadmap/{token}") != 3 {
		t.Fatalf("expected 3 redacted request lines:\n%s", out)
	}

	// Other paths are logged unchanged.
	buf.Reset()
	srv.Handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/healthz", nil))
	if !strings.Contains(buf.String(), "path=/healthz") {
		t.Fatalf("healthz not logged as-is:\n%s", buf.String())
	}
}

func TestLogPath(t *testing.T) {
	for in, want := range map[string]string{
		"/public/roadmap/rmp_abc":        "/public/roadmap/{token}",
		"/public/roadmap/":               "/public/roadmap/{token}",
		"/public/roadmap":                "/public/roadmap",
		"/public/branding":               "/public/branding",
		"/api/v1/helpdesk/roadmap/links": "/api/v1/helpdesk/roadmap/links",
		"//public/roadmap/rmp_abc":       "//public/roadmap/{token}",
		"/elsewhere/rmp_abc/x":           "/elsewhere/{token}/x",
	} {
		if got := logPath(in); got != want {
			t.Errorf("logPath(%q) = %q, want %q", in, got, want)
		}
	}
}
