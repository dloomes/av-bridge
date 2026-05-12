package api

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

// AuthConfig holds authentication settings for the local API
type AuthConfig struct {
	Enabled    bool     `yaml:"enabled"`
	APIKeys    []string `yaml:"api_keys"`    // static bearer tokens
	HMACSecret string   `yaml:"hmac_secret"` // for signed webhook callbacks from cloud
}

// authMiddleware enforces Bearer token authentication on all API routes.
func authMiddleware(cfg AuthConfig) func(http.Handler) http.Handler {
	// Build a fast lookup set
	validKeys := make(map[string]struct{}, len(cfg.APIKeys))
	for _, k := range cfg.APIKeys {
		validKeys[strings.TrimSpace(k)] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !cfg.Enabled {
				next.ServeHTTP(w, r)
				return
			}

			// Always allow health check unauthenticated
			if r.URL.Path == "/healthz" {
				next.ServeHTTP(w, r)
				return
			}

			token := extractBearer(r)
			if token == "" {
				slog.Warn("auth: missing token", "remote", r.RemoteAddr, "path", r.URL.Path)
				writeJSONError(w, http.StatusUnauthorized, "missing or invalid Authorization header")
				return
			}

			if _, ok := validKeys[token]; !ok {
				slog.Warn("auth: invalid token", "remote", r.RemoteAddr, "path", r.URL.Path)
				writeJSONError(w, http.StatusForbidden, "invalid API key")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// loggingMiddleware logs every request with method, path, status, and latency.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		slog.Info("api request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"latency_ms", time.Since(start).Milliseconds(),
			"remote", r.RemoteAddr,
		)
	})
}

// recoveryMiddleware catches panics and returns a 500 instead of crashing.
func recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("api panic recovered", "error", rec, "path", r.URL.Path)
				writeJSONError(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// VerifyHMACSignature validates a cloud webhook callback is legitimately signed.
// Cloud portal should send: X-Signature: sha256=<hex(hmac-sha256(body, secret))>
func VerifyHMACSignature(secret, signature string, body []byte) bool {
	if secret == "" {
		return true // not configured, skip check
	}
	sig := strings.TrimPrefix(signature, "sha256=")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(sig), []byte(expected))
}

func extractBearer(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	// Also accept as query param for WebSocket clients
	return r.URL.Query().Get("token")
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// responseWriter wraps http.ResponseWriter to capture the status code for logging.
// Must also expose http.Hijacker so the gorilla/websocket upgrader can hijack
// the underlying TCP connection on /ws/events — without this, WS upgrades fail
// with "response does not implement http.Hijacker".
type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := rw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("underlying ResponseWriter does not implement http.Hijacker")
	}
	return h.Hijack()
}
