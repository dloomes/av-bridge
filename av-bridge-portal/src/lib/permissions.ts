// Portal-side mirror of av-bridge-cloud/internal/portalauth/permissions.go.
//
// The Go catalogue is the source of truth — the backend validates every
// incoming permission key against IsKnownPermission and rejects unknowns
// with a 400. This file adds UI metadata (category label, human title,
// description) so the /roles page permission matrix renders sensibly.
//
// When adding a permission on the backend, ALSO add it here so the UI can
// display + toggle it. When removing one, remove here too; a permission
// row without a matching entry falls out of the matrix silently (the UI
// only renders keys it recognises).

export type PermissionCategory =
  | "Reads"
  | "Device control"
  | "Alerts"
  | "Fleet management"
  | "Notifications"
  | "Firmware"
  | "Users & Roles"
  | "Tenant"
  | "Assets"
  | "Room Readiness"
  | "Public API";

export interface PermissionDef {
  key: string;
  title: string;
  description: string;
  category: PermissionCategory;
}

// Ordering here drives display order in the /roles matrix. Categories are
// listed in the order they first appear.
export const PERMISSIONS: readonly PermissionDef[] = [
  // Reads
  { key: "view.dashboard",     title: "View dashboard",       description: "Fleet summary, device list, telemetry, live event stream.", category: "Reads" },
  { key: "view.audit",         title: "View audit log",       description: "See who did what and when across the tenant.",             category: "Reads" },
  { key: "view.reports",       title: "View reports",         description: "Device uptime and room activity reports, CSV export.",     category: "Reads" },
  { key: "view.firmware",      title: "View firmware",        description: "Fleet firmware histogram + target versions.",              category: "Reads" },
  { key: "view.notifications", title: "View notifications",   description: "List the notification channels configured for the tenant.", category: "Reads" },
  { key: "view.users",         title: "View users",           description: "List of users in the tenant and their assigned roles.",   category: "Reads" },
  { key: "view.assets",        title: "View assets",          description: "List physical assets (monitored + not) tracked in the tenant's rooms.", category: "Reads" },

  // Device control
  { key: "command.device",    title: "Send device commands", description: "Issue single-device commands (power, input, mute, dial...).", category: "Device control" },
  { key: "command.bulk",      title: "Bulk command fan-out", description: "Send the same command to many devices at once (up to 200).",  category: "Device control" },
  { key: "reconnect.device",  title: "Reconnect device",     description: "Force the bridge to drop and re-open a device connection.",   category: "Device control" },

  // Alerts
  { key: "alert.acknowledge", title: "Acknowledge alerts",   description: "Move an open alert to acknowledged so on-call knows it's being handled.", category: "Alerts" },
  { key: "alert.resolve",     title: "Resolve alerts",       description: "Close an alert as resolved. Records who resolved it.",                    category: "Alerts" },

  // Fleet management
  { key: "device.crud",       title: "Manage devices",       description: "Add, edit, and remove devices from the tenant.",           category: "Fleet management" },
  { key: "hierarchy.crud",    title: "Manage locations",     description: "Regions, locations, buildings, rooms — full CRUD.",         category: "Fleet management" },
  { key: "collector.crud",    title: "Manage collectors",    description: "Pre-provision on-prem bridges from the portal and issue enrollment tokens for site setup.", category: "Fleet management" },

  // Notifications
  { key: "notification.crud", title: "Manage notification channels", description: "Add, edit, and remove email / Teams / webhook channels.",       category: "Notifications" },
  { key: "notification.test", title: "Test notification channel",     description: "Send a test message to a channel to verify it's configured right.", category: "Notifications" },

  // Firmware
  { key: "firmware_target.crud", title: "Manage firmware targets", description: "Set the target firmware version + docs URL per (make, model).", category: "Firmware" },

  // Users & Roles
  { key: "user.create",         title: "Create users",         description: "Add new users to the tenant.",                                     category: "Users & Roles" },
  { key: "user.update",         title: "Update users",         description: "Change a user's full name, role assignments, or enabled state.",   category: "Users & Roles" },
  { key: "user.reset_password", title: "Reset user passwords", description: "Set a new password for a user without knowing the current one.",   category: "Users & Roles" },
  { key: "user.delete",         title: "Delete users",         description: "Permanently remove a user from the tenant.",                       category: "Users & Roles" },
  { key: "role.crud",           title: "Manage roles",         description: "Create, edit, and delete custom roles inside this tenant.",        category: "Users & Roles" },
  { key: "role_mapping.manage", title: "Manage sign-in mappings", description: "Map Entra security groups to roles so SSO users land with the right permissions on first sign-in.", category: "Users & Roles" },
  // Tenant
  { key: "branding.update",     title: "Update branding",      description: "Upload the tenant logo, change the accent colour and portal display name.", category: "Tenant" },
  // Assets
  { key: "asset.crud",          title: "Manage assets",        description: "Add, edit, and remove assets (physical inventory) in the tenant.", category: "Assets" },
  // Room Readiness (nightly lifecycle)
  { key: "nightly.view",        title: "View Room Readiness",  description: "See nightly schedules, routines, and run history.",                       category: "Room Readiness" },
  { key: "nightly.manage",      title: "Manage Room Readiness", description: "Edit the nightly schedule, room overrides, and test routines.",         category: "Room Readiness" },
  // Public API
  { key: "api_token.view",      title: "View API tokens",       description: "See the list of API tokens minted for programmatic access, and their last-used timestamps.", category: "Public API" },
  { key: "api_token.manage",    title: "Manage API tokens",     description: "Mint, name, and revoke API tokens used by external systems to read from the tenant.",       category: "Public API" },
];

// PERMISSIONS_BY_CATEGORY is what the matrix renders — same items, grouped
// for the sectioned checkbox grid.
export const PERMISSIONS_BY_CATEGORY: { category: PermissionCategory; items: readonly PermissionDef[] }[] = (() => {
  const seen = new Set<PermissionCategory>();
  const order: PermissionCategory[] = [];
  for (const p of PERMISSIONS) {
    if (!seen.has(p.category)) {
      seen.add(p.category);
      order.push(p.category);
    }
  }
  return order.map((category) => ({
    category,
    items: PERMISSIONS.filter((p) => p.category === category),
  }));
})();

// KNOWN_PERMISSION_KEYS is the exact string set the backend accepts — used
// to filter out unknown keys arriving in a role's permissions[] payload.
export const KNOWN_PERMISSION_KEYS = new Set(PERMISSIONS.map((p) => p.key));
