package db

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

// CreateCustomerOptions describes a new customer plus an optional first-admin
// user that lands in the same transaction. If InitialAdmin is set, it must
// satisfy the same rules SeedLocalUser enforces (email + password ≥ 12 chars).
type CreateCustomerOptions struct {
	Name          string
	EntraTenantID string // optional — set later when the customer federates
	Slug          string // optional — URL slug for <slug>.<env>.involvecloud.com sign-in. Empty = no branded URL.
	InitialAdmin  *InitialAdminOptions
}

type InitialAdminOptions struct {
	Email    string
	Password string
	FullName string
}

// CreateCustomerResult carries the ids so the caller can audit and the UI can
// deep-link to the new customer's dashboard.
type CreateCustomerResult struct {
	CustomerID  string
	RegionID    string
	LocationID  string
	BuildingID  string
	RoomID      string
	AdminUserID string // empty when InitialAdmin is nil
}

// CreateCustomer creates a customer + a default region/location/building/room
// hierarchy in one transaction, and optionally seeds a first admin user.
// Runs against the admin pool because there is no principal for the new
// customer's scope until they log in.
//
// Design: every new customer gets a placeholder hierarchy so a bridge that
// registers before the admin has organised their estate has somewhere to
// place devices. The admin can rename / restructure via the hierarchy CRUD.
func (s *Store) CreateCustomer(ctx context.Context, opts CreateCustomerOptions) (CreateCustomerResult, error) {
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		return CreateCustomerResult{}, errors.New("customer name required")
	}
	if opts.InitialAdmin != nil {
		if opts.InitialAdmin.Email == "" || opts.InitialAdmin.Password == "" {
			return CreateCustomerResult{}, errors.New("initial admin needs email and password")
		}
		if len(opts.InitialAdmin.Password) < 12 {
			return CreateCustomerResult{}, errors.New("initial admin password must be at least 12 characters")
		}
	}

	slug := NormalizeSlug(opts.Slug)
	if err := ValidateSlug(slug); err != nil {
		return CreateCustomerResult{}, err
	}

	tx, err := s.admin.Begin(ctx)
	if err != nil {
		return CreateCustomerResult{}, err
	}
	defer tx.Rollback(ctx)

	var res CreateCustomerResult

	// Customer row. Entra tenant id + slug are both optional and both unique
	// when set — duplicates yield a friendly error rather than raw SQLSTATE.
	entraArg := any(nil)
	if opts.EntraTenantID != "" {
		entraArg = opts.EntraTenantID
	}
	slugArg := any(nil)
	if slug != "" {
		slugArg = slug
	}
	if err := tx.QueryRow(ctx,
		`INSERT INTO customers (name, entra_tenant_id, slug)
		 VALUES ($1, $2, $3) RETURNING id::text`,
		name, entraArg, slugArg).Scan(&res.CustomerID); err != nil {
		if isDuplicateKey(err) {
			// Two possible collisions — differentiate so the operator knows
			// which field to change. pg unique-violation errors include the
			// constraint name; check that rather than the column value.
			msg := err.Error()
			if strings.Contains(msg, "customers_slug_key") {
				return CreateCustomerResult{}, errors.New("a customer with that URL slug already exists")
			}
			return CreateCustomerResult{}, errors.New("a customer with that Entra tenant id already exists")
		}
		return CreateCustomerResult{}, fmt.Errorf("insert customer: %w", err)
	}

	// Default hierarchy — matches the placeholder shape BootstrapPoC creates
	// so bridge registration and device create paths don't need to special-
	// case a "fresh customer with no rooms" state.
	if err := tx.QueryRow(ctx,
		`INSERT INTO regions (customer_id, name) VALUES ($1, 'Default Region')
		 RETURNING id::text`, res.CustomerID).Scan(&res.RegionID); err != nil {
		return CreateCustomerResult{}, fmt.Errorf("insert region: %w", err)
	}
	if err := tx.QueryRow(ctx,
		`INSERT INTO locations (customer_id, region_id, name)
		 VALUES ($1, $2, 'Default Location') RETURNING id::text`,
		res.CustomerID, res.RegionID).Scan(&res.LocationID); err != nil {
		return CreateCustomerResult{}, fmt.Errorf("insert location: %w", err)
	}
	if err := tx.QueryRow(ctx,
		`INSERT INTO buildings (customer_id, location_id, name)
		 VALUES ($1, $2, 'Default Building') RETURNING id::text`,
		res.CustomerID, res.LocationID).Scan(&res.BuildingID); err != nil {
		return CreateCustomerResult{}, fmt.Errorf("insert building: %w", err)
	}
	if err := tx.QueryRow(ctx,
		`INSERT INTO rooms (customer_id, building_id, name)
		 VALUES ($1, $2, 'Default Room') RETURNING id::text`,
		res.CustomerID, res.BuildingID).Scan(&res.RoomID); err != nil {
		return CreateCustomerResult{}, fmt.Errorf("insert room: %w", err)
	}

	// Seed the three system-default roles for this new customer. The
	// migration handles existing customers; this handles the "add customer
	// via UI" path. Permission bundles are identical to the migration's
	// seed — keep both in sync when adding new permissions.
	if err := seedSystemRoles(ctx, tx, res.CustomerID); err != nil {
		return CreateCustomerResult{}, fmt.Errorf("seed system roles: %w", err)
	}

	// Optional first admin user. Case-collapsed email so subsequent lookups
	// via LOWER(email) match. Duplicate email inside the same customer is
	// impossible because this customer is brand-new.
	if opts.InitialAdmin != nil {
		hash, err := bcrypt.GenerateFromPassword([]byte(opts.InitialAdmin.Password), 10)
		if err != nil {
			return CreateCustomerResult{}, fmt.Errorf("hash password: %w", err)
		}
		email := strings.ToLower(strings.TrimSpace(opts.InitialAdmin.Email))
		if err := tx.QueryRow(ctx, `
			INSERT INTO users (email, password_hash, full_name, role, customer_id)
			VALUES ($1, $2, NULLIF($3, ''), 'admin', $4)
			RETURNING id::text`,
			email, string(hash), opts.InitialAdmin.FullName, res.CustomerID).
			Scan(&res.AdminUserID); err != nil {
			return CreateCustomerResult{}, fmt.Errorf("insert admin user: %w", err)
		}
		// Assign the new admin the customer's admin system role so the
		// permission engine finds them straight away.
		if _, err := tx.Exec(ctx, `
			INSERT INTO user_roles (user_id, role_id)
			SELECT $1, r.id FROM roles r
			 WHERE r.customer_id = $2 AND r.name = 'admin' AND r.is_system_default`,
			res.AdminUserID, res.CustomerID); err != nil {
			return CreateCustomerResult{}, fmt.Errorf("assign initial admin role: %w", err)
		}
	}

	return res, tx.Commit(ctx)
}

// UpdateCustomerOptions carries the fields the vendor helpdesk can edit
// after a customer row exists. Every field is a pointer so absent = "leave
// stored value alone" — the wire layer maps a missing JSON key to a nil
// pointer and an explicit empty string ("") clears the field to NULL.
//
// Not extended to include EntraTenantID today because federated tenants
// aren't editable from the helpdesk yet; that lands with the Entra work.
type UpdateCustomerOptions struct {
	Name *string
	Slug *string
}

// UpdateCustomer applies the supplied changes to a customer row. Empty
// pointer means "no change"; empty-string pointer means "clear to NULL"
// (only meaningful for Slug — Name is NOT NULL and rejects blanks).
// Runs against the admin pool because helpdesk edits are cross-tenant
// by definition. Returns pgx.ErrNoRows for unknown customer ids so the
// HTTP layer can map to 404.
func (s *Store) UpdateCustomer(ctx context.Context, customerID string, opts UpdateCustomerOptions) error {
	if opts.Name == nil && opts.Slug == nil {
		return nil // no-op — accept blank PATCH so the UI can send a save without changes
	}

	setParts := []string{}
	args := []any{customerID}

	if opts.Name != nil {
		trimmed := strings.TrimSpace(*opts.Name)
		if trimmed == "" {
			return errors.New("name cannot be blank")
		}
		args = append(args, trimmed)
		setParts = append(setParts, fmt.Sprintf("name = $%d", len(args)))
	}

	if opts.Slug != nil {
		slug := NormalizeSlug(*opts.Slug)
		if err := ValidateSlug(slug); err != nil {
			return err
		}
		if slug == "" {
			args = append(args, nil)
		} else {
			args = append(args, slug)
		}
		setParts = append(setParts, fmt.Sprintf("slug = $%d", len(args)))
	}

	sql := "UPDATE customers SET " + strings.Join(setParts, ", ") + " WHERE id = $1"
	tag, err := s.admin.Exec(ctx, sql, args...)
	if err != nil {
		if isDuplicateKey(err) && strings.Contains(err.Error(), "customers_slug_key") {
			return errors.New("a customer with that URL slug already exists")
		}
		return fmt.Errorf("update customer: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// seedSystemRoles inserts the three system-default roles and their
// permission bundles for a given (brand-new) customer inside an existing
// transaction. Idempotent-ish: the UNIQUE(customer_id, name) constraint
// on roles means calling this twice for the same customer errors on the
// second call — callers should only invoke on genuinely new customers.
//
// Kept package-private and colocated with CreateCustomer so the seed logic
// lives next to the migration that seeds existing customers on upgrade.
// Any change to the permission bundles here must also be made in
// 0016_rbac_roles.sql for consistency across code paths.
func seedSystemRoles(ctx context.Context, tx pgx.Tx, customerID string) error {
	type roleSeed struct {
		Name        string
		Description string
		Perms       []string
	}
	seeds := []roleSeed{
		{
			Name:        "admin",
			Description: "Full tenant management: device + hierarchy + user + notification + firmware + role CRUD, plus all reads and controls.",
			Perms: []string{
				"view.dashboard", "view.audit", "view.reports",
				"view.firmware", "view.notifications", "view.users",
				"command.device", "command.bulk", "reconnect.device",
				"device.crud",
				"alert.acknowledge", "alert.resolve",
				"hierarchy.crud",
				"notification.crud", "notification.test",
				"firmware_target.crud",
				"user.create", "user.update", "user.reset_password", "user.delete",
				"role.crud",
				"role_mapping.manage",
				"branding.update",
				"view.assets", "asset.crud",
				"nightly.view", "nightly.manage",
				"collector.crud",
				"api_token.view", "api_token.manage",
			},
		},
		{
			Name:        "operator",
			Description: "Send commands, run bulk fan-out, acknowledge and resolve alerts, test notification channels. All reads. No user/role management.",
			Perms: []string{
				"view.dashboard", "view.audit", "view.reports",
				"view.firmware", "view.notifications", "view.users",
				"view.assets",
				"nightly.view",
				"command.device", "command.bulk", "reconnect.device",
				"alert.acknowledge", "alert.resolve",
				"notification.test",
			},
		},
		{
			Name:        "viewer",
			Description: "Read-only monitoring. No control actions.",
			Perms: []string{
				"view.dashboard", "view.audit", "view.reports",
				"view.firmware", "view.notifications", "view.users",
				"view.assets",
				"nightly.view",
			},
		},
	}
	for _, s := range seeds {
		var roleID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO roles (customer_id, name, description, is_system_default)
			VALUES ($1, $2, $3, true)
			RETURNING id::text`,
			customerID, s.Name, s.Description).Scan(&roleID); err != nil {
			return fmt.Errorf("insert role %q: %w", s.Name, err)
		}
		for _, p := range s.Perms {
			if _, err := tx.Exec(ctx,
				`INSERT INTO role_permissions (role_id, permission) VALUES ($1, $2)`,
				roleID, p); err != nil {
				return fmt.Errorf("insert permission %s for role %s: %w", p, s.Name, err)
			}
		}
	}
	return nil
}

// isDuplicateKey — pgx surfaces unique-violation errors with SQLSTATE 23505
// in the error string. Checking the string here avoids pulling in the pgconn
// package just for one branch; we only need the boolean.
func isDuplicateKey(err error) bool {
	return err != nil && strings.Contains(err.Error(), "SQLSTATE 23505")
}
