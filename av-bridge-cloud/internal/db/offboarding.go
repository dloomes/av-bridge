package db

import (
	"context"
	"errors"
)

// Offboarding (GDPR, migration 0073): once customers.offboarding_started_at
// is set, every way in refuses that customer — sessions, sign-in by every
// method, API tokens, display links, Collectors — and background work skips
// it. Vendor (Involve) users have no customer and are never blocked.
//
// Every path uses the helpers below, so none can drift from the others.

// OffboardingMessage is the refusal shown when a customer's user tries to
// sign in while the customer is offboarding.
const OffboardingMessage = "This organisation's M.A.R.C.U.S. account has been closed."

// ErrCustomerOffboarding is returned by session-creating helpers when the
// user's customer is offboarding.
var ErrCustomerOffboarding = errors.New("customer offboarding")

// CustomerActiveSQL returns a predicate true when the customer in col is not
// offboarding. col is a SQL expression naming a customer id; qualify it with
// its table or alias, since the predicate's own subquery reads customers.
func CustomerActiveSQL(col string) string {
	return "NOT EXISTS (SELECT 1 FROM customers oc WHERE oc.id = " + col + " AND oc.offboarding_started_at IS NOT NULL)"
}

// UserActiveSQL returns a predicate true when a user whose customer id is in
// col may sign in: vendor users (no customer) always, customer users while
// their customer is not offboarding.
func UserActiveSQL(col string) string {
	return "(" + col + " IS NULL OR " + CustomerActiveSQL(col) + ")"
}

// CustomerOffboarding reports whether the customer is offboarding. An
// unknown customer is not offboarding.
func (s *Store) CustomerOffboarding(ctx context.Context, customerID string) (bool, error) {
	var active bool
	err := s.admin.QueryRow(ctx,
		`SELECT `+CustomerActiveSQL("$1::uuid"), customerID).Scan(&active)
	return !active, err
}

// UserOffboarding reports whether the user belongs to a customer that is
// offboarding. Vendor users and unknown users are not.
func (s *Store) UserOffboarding(ctx context.Context, userID string) (bool, error) {
	var blocked bool
	err := s.admin.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM users u
		                WHERE u.id = $1::uuid
		                  AND NOT `+UserActiveSQL("u.customer_id")+`)`,
		userID).Scan(&blocked)
	return blocked, err
}
