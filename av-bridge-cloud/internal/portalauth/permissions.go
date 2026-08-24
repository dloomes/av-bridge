package portalauth

// Permission catalogue — the canonical list of capability keys the tenant
// RBAC engine understands. Every value in role_permissions.permission must
// be a member of KnownPermissions or the row is unreachable (the middleware
// will silently ignore an unknown key, which is a bug in the row not in
// the middleware).
//
// The migration 0016_rbac_roles.sql seeds the three system-default roles
// using these exact strings — keep both in sync. Adding a new permission is
// a two-step change:
//
//   1. Add the constant + register in KnownPermissions here.
//   2. Update the seeds in db/customers.seedSystemRoles() and in the
//      relevant migration so existing customers get the new permission on
//      the appropriate role (usually admin).
//
// The portal has a mirror catalogue in src/lib/permissions.ts with UI
// metadata (category label, human description). When adding permissions
// here, add the matching entry there so the /roles page renders them.

// Read permissions — a user with these can see the corresponding data.
const (
	PermViewDashboard     = "view.dashboard"
	PermViewAudit         = "view.audit"
	PermViewReports       = "view.reports"
	PermViewFirmware      = "view.firmware"
	PermViewNotifications = "view.notifications"
	PermViewUsers         = "view.users"
	PermViewAssets        = "view.assets"
)

// Device control permissions — send commands, reconnect, fan-out.
const (
	PermCommandDevice    = "command.device"
	PermCommandBulk      = "command.bulk"
	PermReconnectDevice  = "reconnect.device"
)

// Alert lifecycle permissions — an ops-team caller usually has both.
const (
	PermAlertAcknowledge = "alert.acknowledge"
	PermAlertResolve     = "alert.resolve"
)

// Fleet + hierarchy management.
const (
	PermDeviceCRUD    = "device.crud"
	PermHierarchyCRUD = "hierarchy.crud"
	PermAssetCRUD     = "asset.crud"
)

// Outbound notifications — channel CRUD is the manage side; test is the
// send-a-test-message operator side, kept separate so an on-call operator
// can verify their own channel without full admin rights.
const (
	PermNotificationCRUD = "notification.crud"
	PermNotificationTest = "notification.test"
)

// Firmware policy — per-(make, model) target_version + docs_url.
const PermFirmwareTargetCRUD = "firmware_target.crud"

// User + role management. Delete is separated from update so an operator-
// like role can be defined with the ability to disable/enable users but
// not permanently delete them (via update.disabled=true without owning
// user.delete).
const (
	PermUserCreate        = "user.create"
	PermUserUpdate        = "user.update"
	PermUserResetPassword = "user.reset_password"
	PermUserDelete        = "user.delete"
	PermRoleCRUD          = "role.crud"
	// role_mapping.manage — CRUD over the Entra group → role mappings
	// consulted at JIT time. Kept distinct from role.crud so a tenant
	// can grant the "who defines groups → roles" capability separately
	// from "who defines what roles mean", though in practice the
	// system-default admin role holds both.
	PermRoleMappingManage = "role_mapping.manage"
)

// Tenant-branding management. Reads are open to any authenticated user in
// the tenant (the whole portal renders in that customer's colours, so
// gating the read serves no purpose); writes gate on this permission.
const PermBrandingUpdate = "branding.update"

// Nightly lifecycle (Room Readiness). view lets a user see schedules,
// routines, and run history; manage lets an admin edit schedules, author
// routines, set per-room overrides. Operators + viewers get view only.
const (
	PermNightlyView   = "nightly.view"
	PermNightlyManage = "nightly.manage"
)

// KnownPermissions is the closed set of valid permission keys — used by the
// roles CRUD handler to reject unknown strings, and by the permission
// engine to fail-closed if a role somehow references a permission that has
// been removed from the code without a matching data migration.
var KnownPermissions = map[string]struct{}{
	PermViewDashboard:      {},
	PermViewAudit:          {},
	PermViewReports:        {},
	PermViewFirmware:       {},
	PermViewNotifications:  {},
	PermViewUsers:          {},
	PermCommandDevice:      {},
	PermCommandBulk:        {},
	PermReconnectDevice:    {},
	PermAlertAcknowledge:   {},
	PermAlertResolve:       {},
	PermDeviceCRUD:         {},
	PermHierarchyCRUD:      {},
	PermNotificationCRUD:   {},
	PermNotificationTest:   {},
	PermFirmwareTargetCRUD: {},
	PermUserCreate:         {},
	PermUserUpdate:         {},
	PermUserResetPassword:  {},
	PermUserDelete:         {},
	PermRoleCRUD:           {},
	PermRoleMappingManage:  {},
	PermBrandingUpdate:     {},
	PermViewAssets:         {},
	PermAssetCRUD:          {},
	PermNightlyView:        {},
	PermNightlyManage:      {},
}

// IsKnownPermission returns true if the string is a member of the
// catalogue above. Used by role CRUD to validate incoming lists.
func IsKnownPermission(p string) bool {
	_, ok := KnownPermissions[p]
	return ok
}
