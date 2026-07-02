package db

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

// SeedLocalUserOptions describes a single user to ensure exists at startup.
// Exactly one of CustomerID / VendorTenantID must be set. Password is only
// used on first creation — SeedLocalUser never overwrites an existing hash,
// which prevents an operator with env access from silently locking real
// users out.
type SeedLocalUserOptions struct {
	Email          string
	Password       string
	FullName       string
	Role           string // "admin" | "operator" | "viewer"
	CustomerID     string // set for customer-scoped users
	VendorTenantID string // set for vendor-scoped users
}

// SeedLocalUser upserts a users row only if no row with the same
// (scope, lower(email)) exists. Idempotent — safe to call on every startup.
// Returns (created=true, nil) on first creation, (false, nil) if a row
// already existed. Any DB failure surfaces as an error.
//
// Password rules: at least 12 chars. bcrypt at cost 10. Rejecting weak
// passwords at seed time is better than letting an operator accidentally set
// a trivial credential on the first vendor admin.
func (s *Store) SeedLocalUser(ctx context.Context, opts SeedLocalUserOptions) (bool, error) {
	if opts.Email == "" || opts.Password == "" {
		return false, errors.New("email and password required")
	}
	if len(opts.Password) < 12 {
		return false, errors.New("password must be at least 12 characters")
	}
	if opts.Role != "admin" && opts.Role != "operator" && opts.Role != "viewer" {
		return false, fmt.Errorf("invalid role %q", opts.Role)
	}
	hasCustomer := opts.CustomerID != ""
	hasVendor := opts.VendorTenantID != ""
	if hasCustomer == hasVendor {
		return false, errors.New("exactly one of CustomerID / VendorTenantID must be set")
	}
	email := strings.ToLower(strings.TrimSpace(opts.Email))

	tx, err := s.admin.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	// Look up first — treat "row exists" as a no-op rather than upserting.
	// Prevents env-based rotation from silently overwriting a real password.
	var existing string
	var lookupErr error
	if hasCustomer {
		lookupErr = tx.QueryRow(ctx,
			`SELECT id::text FROM users
			  WHERE customer_id = $1 AND lower(email) = $2`,
			opts.CustomerID, email).Scan(&existing)
	} else {
		lookupErr = tx.QueryRow(ctx,
			`SELECT id::text FROM users
			  WHERE vendor_tenant_id = $1 AND lower(email) = $2`,
			opts.VendorTenantID, email).Scan(&existing)
	}
	switch {
	case lookupErr == nil:
		return false, tx.Commit(ctx)
	case errors.Is(lookupErr, pgx.ErrNoRows):
		// fall through — create
	default:
		return false, lookupErr
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(opts.Password), 10)
	if err != nil {
		return false, err
	}

	var customerArg, vendorArg any
	if hasCustomer {
		customerArg = opts.CustomerID
	}
	if hasVendor {
		vendorArg = opts.VendorTenantID
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO users (email, password_hash, full_name, role, customer_id, vendor_tenant_id)
		VALUES ($1, $2, NULLIF($3, ''), $4, $5, $6)`,
		email, string(hash), opts.FullName, opts.Role, customerArg, vendorArg); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
