package privacy

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Subject is the person a data request is about. Matching is by email, so a
// Subject exists even when the account has already been deleted.
type Subject struct {
	CustomerID     string // empty for vendor staff
	VendorTenantID string // set for vendor staff
	Email          string // as requested
	UserID         string // empty if no account remains
}

// FindSubject resolves the account behind an email within one organisation:
// a customer (customerID) or, for vendor staff, a vendor tenant
// (vendorTenantID). Exactly one of the two must be set. Users are unique per
// organisation on lower(email) (migration 0015), so at most one row matches.
// No account is not an error: UserID is left empty.
func FindSubject(ctx context.Context, tx pgx.Tx, customerID, vendorTenantID, email string) (Subject, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return Subject{}, errors.New("privacy: email required")
	}
	if (customerID == "") == (vendorTenantID == "") {
		return Subject{}, errors.New("privacy: exactly one of customer and vendor tenant required")
	}
	s := Subject{CustomerID: customerID, VendorTenantID: vendorTenantID, Email: email}
	scope, id := "customer_id", customerID
	if vendorTenantID != "" {
		scope, id = "vendor_tenant_id", vendorTenantID
	}
	err := tx.QueryRow(ctx, `SELECT id::text FROM users WHERE `+scope+` = $1::uuid AND lower(email) = lower($2)`,
		id, email).Scan(&s.UserID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Subject{}, err
	}
	return s, nil
}
