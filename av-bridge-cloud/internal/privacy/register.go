// Package privacy holds the personal data register and the GDPR offboarding,
// access and erasure logic built on it.
package privacy

import "regexp"

// Key says how a column identifies a person.
type Key int

const (
	KeyUserID Key = iota // uuid column holding users.id
	KeyEmail             // text column holding an email address
)

// Action says what erasing a person does to a column.
type Action int

const (
	ActDeleteRow Action = iota
	ActReplace
	ActStripJSON
	ActReport
)

// Entry is one column that holds personal data.
type Entry struct {
	Table, Column string
	Key           Key
	Category      string // "account" | "network" | "activity" | "contact"
	Action        Action
	JSONKeys      []string // ActStripJSON only
	// KeyColumn is the column that identifies the row's person when it
	// differs from Column. Empty means Column is itself the key column.
	// Used for data columns (ip_address, user_agent) that sit in a row found
	// through another column (user_id, issued_by, created_by).
	KeyColumn string
	Scoped    bool   // table has customer_id; statements must filter on it
	Reason    string // required for ActReport
}

// Exempt is a column that matches GuardPattern but identifies no person.
type Exempt struct{ Table, Column, Reason string }

// GuardPattern selects the column names the guard test insists are classified.
var GuardPattern = regexp.MustCompile(`email|name|_ip$|ip_address|user_agent|actor|_by$|recipients|phone|contact`)

// Personal lists every column that holds personal data.
//
// Conventions for Erase and Collect:
//   - KeyUserID: the key column is a uuid equal to users.id. KeyEmail: it is
//     text holding an email address (matched case-insensitively).
//   - ActDeleteRow deletes the whole row found through the key column
//     (KeyColumn, or Column when empty). Entries on one table share that delete.
//   - ActReplace on a uuid column sets it to NULL; on a text column it
//     substitutes the stand-in for the email.
//   - Scoped: the table has customer_id, so every statement must filter on it.
var Personal = []Entry{
	// Accounts.
	{Table: "users", Column: "email", Key: KeyEmail, Category: "account", Action: ActDeleteRow, Scoped: true},
	{Table: "users", Column: "full_name", Key: KeyEmail, KeyColumn: "email", Category: "account", Action: ActDeleteRow, Scoped: true},

	// Sessions and one-time tokens: network details go with the row.
	{Table: "user_sessions", Column: "user_id", Key: KeyUserID, Category: "account", Action: ActDeleteRow},
	{Table: "user_sessions", Column: "ip_address", Key: KeyUserID, KeyColumn: "user_id", Category: "network", Action: ActDeleteRow},
	{Table: "user_sessions", Column: "user_agent", Key: KeyUserID, KeyColumn: "user_id", Category: "network", Action: ActDeleteRow},
	{Table: "password_reset_tokens", Column: "user_id", Key: KeyUserID, Category: "account", Action: ActDeleteRow},
	{Table: "password_reset_tokens", Column: "requester_ip", Key: KeyUserID, KeyColumn: "user_id", Category: "network", Action: ActDeleteRow},
	{Table: "password_reset_tokens", Column: "user_agent", Key: KeyUserID, KeyColumn: "user_id", Category: "network", Action: ActDeleteRow},
	{Table: "magic_link_tokens", Column: "user_id", Key: KeyUserID, Category: "account", Action: ActDeleteRow},
	{Table: "magic_link_tokens", Column: "requester_ip", Key: KeyUserID, KeyColumn: "user_id", Category: "network", Action: ActDeleteRow},
	{Table: "magic_link_tokens", Column: "user_agent", Key: KeyUserID, KeyColumn: "user_id", Category: "network", Action: ActDeleteRow},
	{Table: "magic_link_tokens", Column: "issued_by", Key: KeyUserID, Category: "activity", Action: ActReplace},
	{Table: "collector_enrollment_tokens", Column: "issued_by", Key: KeyUserID, Category: "account", Action: ActDeleteRow},
	{Table: "collector_enrollment_tokens", Column: "requester_ip", Key: KeyUserID, KeyColumn: "issued_by", Category: "network", Action: ActDeleteRow},
	{Table: "collector_enrollment_tokens", Column: "user_agent", Key: KeyUserID, KeyColumn: "issued_by", Category: "network", Action: ActDeleteRow},

	// API tokens outlive their creator, so the identity is cleared, not the row.
	{Table: "api_tokens", Column: "created_by", Key: KeyUserID, Category: "activity", Action: ActReplace, Scoped: true},
	{Table: "api_tokens", Column: "revoked_by", Key: KeyUserID, Category: "activity", Action: ActReplace, Scoped: true},
	{Table: "api_tokens", Column: "last_used_ip", Key: KeyUserID, KeyColumn: "created_by", Category: "network", Action: ActReplace, Scoped: true},

	// Email text and user ids copied into operational records.
	{Table: "remote_session", Column: "actor", Key: KeyEmail, Category: "activity", Action: ActReplace, Scoped: true},
	{Table: "remote_session", Column: "end_reason", Key: KeyEmail, Category: "activity", Action: ActReplace, Scoped: true},
	{Table: "remote_session", Column: "user_id", Key: KeyUserID, Category: "activity", Action: ActReplace, Scoped: true},
	{Table: "dashboards", Column: "created_by", Key: KeyEmail, Category: "activity", Action: ActReplace, Scoped: true},
	// A personal dashboard (owner_user_id set) belongs to the person; shared ones have NULL.
	{Table: "dashboards", Column: "owner_user_id", Key: KeyUserID, Category: "account", Action: ActDeleteRow, Scoped: true},
	{Table: "dashboard_display_links", Column: "created_by", Key: KeyEmail, Category: "activity", Action: ActReplace, Scoped: true},
	{Table: "firmware_targets", Column: "updated_by", Key: KeyEmail, Category: "activity", Action: ActReplace, Scoped: true},
	{Table: "schedule_date", Column: "created_by", Key: KeyEmail, Category: "activity", Action: ActReplace, Scoped: true},

	// The audit log is rewritten by the SECURITY DEFINER function, not by Erase directly.
	{Table: "audit_log", Column: "actor", Key: KeyEmail, Category: "activity", Action: ActStripJSON, Scoped: true,
		JSONKeys: []string{"email", "full_name", "target_email", "support_contact", "excluded_note"},
		Reason:   "via privacy_anonymise_audit"},

	// The customer's own contact settings: reported, never edited.
	{Table: "notification_channels", Column: "target", Key: KeyEmail, Category: "contact", Action: ActReport, Scoped: true,
		Reason: "The organisation's own notification address; an administrator edits it."},
	{Table: "nightly_schedule", Column: "helpdesk_email", Key: KeyEmail, Category: "contact", Action: ActReport, Scoped: true,
		Reason: "The organisation's helpdesk address for nightly reports; an administrator edits it."},
	{Table: "room_nightly_config", Column: "notification_recipients", Key: KeyEmail, Category: "contact", Action: ActReport, Scoped: true,
		Reason: "Per-room report recipients set by the organisation; an administrator edits them."},
	{Table: "customers", Column: "support_contact", Key: KeyEmail, Category: "contact", Action: ActReport,
		Reason: "The organisation's published support contact; an administrator edits it."},
}

// NotPersonal lists columns that match GuardPattern but identify no person.
var NotPersonal = []Exempt{
	{"alerts", "acknowledged_by", "role name or system tag, not a person"},
	{"alerts", "resolved_by", "role name or system tag, not a person"},
	{"api_tokens", "name", "token label chosen by the admin"},
	{"assets", "name", "asset name, not a person"},
	{"audit_log", "actor_is_vendor", "flag, not an identity"},
	{"audit_log", "actor_role", "role name"},
	{"audit_log", "actor_scope", "permission scopes"},
	{"buildings", "name", "building name, not a person"},
	{"business_units", "name", "business unit name"},
	{"collectors", "name", "collector name, not a person"},
	{"commands", "name", "command name"},
	{"commands", "submitted_by", "role or schedule tag, not a person"},
	{"customer_deletions", "cancelled_by", "vendor operator, kept as deletion evidence"},
	{"customer_deletions", "customer_name", "organisation name"},
	{"customer_deletions", "started_by", "vendor operator, kept as deletion evidence"},
	{"customers", "display_name", "organisation name"},
	{"customers", "name", "organisation name"},
	{"customers", "offboarding_started_by", "vendor operator, kept as deletion evidence"},
	{"dashboard_display_links", "name", "link label"},
	{"dashboards", "name", "dashboard title"},
	{"devices", "ip_address", "device address, not a person"},
	{"devices", "name", "device name, not a person"},
	{"devices", "username_enc", "encrypted device credential"},
	{"locations", "name", "location name, not a person"},
	{"nightly_step_result", "step_name", "check step name"},
	{"nightly_test_routine", "name", "routine name"},
	{"notification_channels", "name", "channel label"},
	{"regions", "name", "region name"},
	{"roles", "name", "role name"},
	{"room_tags", "name", "tag name"},
	{"rooms", "name", "room name, not a person"},
	{"user_roles", "granted_by", "manual or entra, not a person"},
	{"vendor_tenants", "name", "vendor organisation name"},
}
