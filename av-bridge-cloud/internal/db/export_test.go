package db

import "context"

// SetRolePasswordsFor lets tests exercise the password setter against
// throwaway roles instead of the cluster-wide app_admin/app_tenant.
func SetRolePasswordsFor(ctx context.Context, dsn, adminRole, adminPw, tenantRole, tenantPw string) error {
	return setRolePasswords(ctx, dsn, adminRole, adminPw, tenantRole, tenantPw)
}
