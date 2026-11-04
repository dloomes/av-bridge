package portalapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"

	"github.com/dloomes/av-bridge-cloud/internal/db"
)

// Test hooks for package portalapi_test: the Entra sign-in paths need a
// live Microsoft tenant end to end, so the offboarding test drives their
// session-creating helpers and the customer authorize step directly.

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// EntraCustomerMintSession runs the customer Entra callback's session mint.
func EntraCustomerMintSession(ctx context.Context, store *db.Store, userID string, r *http.Request) (string, error) {
	h := &EntraCustomerHandler{store: store, log: quietLog()}
	return h.mintSession(ctx, userID, r)
}

// EntraVendorMintSession runs the vendor Entra callback's session mint.
func EntraVendorMintSession(ctx context.Context, store *db.Store, userID string, r *http.Request) (string, error) {
	h := &EntraVendorHandler{store: store, log: quietLog()}
	return h.mintSession(ctx, userID, r)
}

// EntraCustomerAuthorize is the customer Entra authorize step (the redirect
// to Microsoft), with a placeholder client id.
func EntraCustomerAuthorize(store *db.Store) http.HandlerFunc {
	h := &EntraCustomerHandler{store: store, log: quietLog(), clientID: "test-client", redirectURI: "http://localhost/cb"}
	return h.Authorize
}
