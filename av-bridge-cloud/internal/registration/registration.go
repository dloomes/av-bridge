// Package registration is the reusable logic for adding a new collector to a
// customer. Today it has one entry point — the admin HTTP handler — but the
// same Register call is what the Customer Admin portal endpoint will use once
// the portal is built, so adapter-specific concerns stay out of it.
package registration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/dloomes/av-bridge-cloud/internal/secrets"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Request struct {
	CustomerID        string  `json:"customer_id"`
	BuildingID        *string `json:"building_id"` // optional — collector may be placed later
	Name              string  `json:"name"`
	BridgeCollectorID string  `json:"bridge_collector_id"`
}

type Result struct {
	ID                string `json:"id"`
	BridgeCollectorID string `json:"bridge_collector_id"`
	HMACSecret        string `json:"hmac_secret"` // returned ONCE — caller must copy into the bridge config
}

var (
	ErrCustomerUnknown = errors.New("unknown customer_id")
	ErrAlreadyExists   = errors.New("bridge_collector_id already registered")
)

// Register inserts a new collector under an existing customer using the admin
// (BYPASSRLS) pool, and returns the generated HMAC secret in plaintext for
// one-time display to the caller. The stored secret is encrypted at rest via
// the provided Cipher.
func Register(ctx context.Context, admin *pgxpool.Pool, cipher secrets.Cipher, req Request) (Result, error) {
	if req.CustomerID == "" || req.Name == "" || req.BridgeCollectorID == "" {
		return Result{}, fmt.Errorf("customer_id, name, and bridge_collector_id are required")
	}

	var one int
	err := admin.QueryRow(ctx, `SELECT 1 FROM customers WHERE id = $1`, req.CustomerID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return Result{}, ErrCustomerUnknown
	}
	if err != nil {
		return Result{}, fmt.Errorf("customer lookup: %w", err)
	}

	// 32 random bytes, hex-encoded — what the bridge will paste into its config.
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return Result{}, err
	}
	plaintext := hex.EncodeToString(raw)
	enc, err := cipher.Encrypt([]byte(plaintext))
	if err != nil {
		return Result{}, fmt.Errorf("encrypt secret: %w", err)
	}

	var id string
	err = admin.QueryRow(ctx, `
		INSERT INTO collectors (customer_id, building_id, bridge_collector_id, name, hmac_secret_enc)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id::text`,
		req.CustomerID, req.BuildingID, req.BridgeCollectorID, req.Name, enc,
	).Scan(&id)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Result{}, ErrAlreadyExists
		}
		return Result{}, fmt.Errorf("insert collector: %w", err)
	}

	return Result{ID: id, BridgeCollectorID: req.BridgeCollectorID, HMACSecret: plaintext}, nil
}
