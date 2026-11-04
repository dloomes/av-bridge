# GDPR data requests: customer offboarding, access requests and erasure

Date: 2026-11-04 · Status: approved design, awaiting implementation plan

## Why

M.A.R.C.U.S. must meet UK GDPR (and the DPA 2018) for the personal data it holds. Customers are
controllers of their staff's data; Involve is their processor, and controller for its own staff.
Three capabilities are missing today:

1. **Customer offboarding.** No way exists to disable or delete a customer. The Service
   Description promises deletion after termination.
2. **Access requests (Article 15).** No way to export what is held about one person.
3. **Erasure (Article 17).** User hard-delete exists, but the person's email stays in
   `audit_log.actor`, audit payloads and several `*_by` columns.

## Decisions

| Topic | Decision |
|---|---|
| Scope | All three, built in order: register → access export → erasure → offboarding |
| Offboarding model | Two-stage and scheduled: offboard on day 0, permanent deletion on day 30, cancellable before then |
| Access during offboarding | None. Sign-in, API tokens and Collectors stop on day 0. Export is by archive only |
| Archive delivery | An Involve admin downloads a ZIP from the helpdesk (any time until day 30) and passes it on; every download recorded |
| Archive contents | CSV + JSON per table and a README; telemetry as daily summaries (`telemetry_daily`), not raw readings |
| Who handles access/erasure requests | Customer admins (new permission `privacy.manage`, *Handle data requests*) and Involve admins |
| Erasure of audit history | Anonymise: replace the person's email with a stand-in and strip their details from payloads; keep the entries |
| Architecture | One personal data register in Go drives all three features, guarded by a schema test |

## Current state (facts this design relies on)

- Every `REFERENCES customers(id)` is `ON DELETE CASCADE` (38 tenant tables). One blocker:
  `collectors.standby_for … ON DELETE RESTRICT` (migration 0051).
- `audit_log` is append-only by grants (SELECT, INSERT for app roles, migration 0007). The only
  deletion path is the SECURITY DEFINER `retention_prune_audit_log` (0062).
- `audit_log.actor` stores the **email** (`portalauth.ActorLabel`), not a user ID. `before`/`after`
  payloads embed `email`, `full_name`, `target_email`, `support_contact`, `excluded_note` and
  notification targets.
- Email text is also copied into `remote_session.actor`, `remote_session.end_reason`,
  `dashboards.created_by`, `dashboard_display_links.created_by`, `firmware_targets.updated_by` and
  `schedule_date.created_by`. `dashboards.owner_user_id` and `remote_session.user_id` have no FK.
- IP address and browser are held in `user_sessions`, `password_reset_tokens`,
  `magic_link_tokens`, `collector_enrollment_tokens` (`requester_ip`, `user_agent`) and
  `api_tokens.last_used_ip`. The three token tables and revoked API tokens are never pruned.
- An Entra user who is hard-deleted is recreated at their next Entra sign-in
  (`portalapi/entra_customer.go`).
- Helpdesk user deletion (`DELETE /api/v1/helpdesk/users/{id}`) writes no audit entry.
- No S3 storage holds customer data; logos are bytea on `customers`. Outside Postgres, personal
  data exists only in CloudWatch logs (30 days) and RDS backups.

## 1. Personal data register (`internal/privacy`)

`register.go` lists every column that holds personal data. Each entry has:

- **Table, column.**
- **Key:** how the column identifies the person: `user_id` (UUID column) or `email` (text column).
- **Category:** `account`, `network` (IP address, browser), `activity`, `contact`.
- **Erasure action:**
  - `delete_row`: e.g. `user_sessions`, the three token tables.
  - `replace`: set the column to the stand-in (e.g. `dashboards.created_by`, `remote_session.actor`).
  - `strip_json`: remove the listed keys from a JSON column, or replace the email where it is a value.
  - `report`: leave it alone and tell the admin, with the reason. Example: a notification channel
    whose `target` is the person's email. That is the customer's own contact setting, so the admin
    edits it.
- **Not personal:** columns that match the guard patterns but don't identify a person
  (`devices.ip_address`, `devices.name`, `buildings.name` and so on), each listed once with a
  reason.

**Guard test** (integration, live schema): every column whose name matches `email`, `name`, `_ip`,
`ip_address`, `user_agent`, `actor`, `_by`, `recipients`, `phone` or `contact` must be in the
register as personal or as not personal. A new column fails the test until someone decides.

**Retention gaps closed** (new rules in `internal/retention`):

- `password_reset_tokens`, `magic_link_tokens`, `collector_enrollment_tokens`: deleted 30 days
  after expiry or use.
- `api_tokens`: deleted 90 days after revocation or expiry.

## 2. Access request export

- `POST /api/v1/privacy/access-export` `{ "email": "…" }` → `application/zip`. Needs
  `privacy.manage`; scoped to the caller's customer by RLS.
- Helpdesk equivalent `POST /api/v1/helpdesk/privacy/access-export` `{ "customer_id", "email" }`,
  for Involve admins. Also covers Involve staff accounts (`vendor_tenant_id`).
- Matching is **by email**, so it finds people whose account was already deleted. Match rules:
  the account row (if any); register rows keyed by that user's ID; register rows whose email
  column equals the address (case-insensitive); `audit_log` rows where `actor` = email, or
  `target_id` = the user's ID, or `before`/`after` contain the email.
- ZIP: one CSV and one JSON per table with matches, plus `README.txt`. The README lists categories,
  sources and retention periods. It also states that application logs (30 days) and backups
  (35 days) may hold copies that expire on their own.
- Audit: `privacy.access_export` entry whose metadata holds the SHA-256 of the lower-cased email,
  never the email itself.
- A partial failure returns an error, never a partial ZIP. Build the ZIP in memory or a temp file,
  then send it.

## 3. Erasure

- `POST /api/v1/privacy/erase` `{ "email": "…", "confirm": "…" }`. `confirm` must equal `email`.
  Needs `privacy.manage`. A `dry_run: true` flag returns the preview counts without changing
  anything (used by the portal preview).
- Helpdesk equivalent `POST /api/v1/helpdesk/privacy/erase` `{ "customer_id", "email", "confirm" }`.
- Guards, the same as user deletion: you can't erase yourself or the customer's last admin.
- **Stand-in:** `erased-user-` plus the first 8 hex characters of the user ID. If no account
  remains, the first 8 hex characters of the SHA-256 of the lower-cased email.
- **One transaction:**
  1. Apply each register entry's action: `delete_row`, `replace`, `strip_json`. Personal
     (unshared) dashboards owned by the user are deleted; shared dashboards keep their content
     and get the stand-in as `created_by`.
  2. Rewrite `audit_log` through the new SECURITY DEFINER function
     `privacy_anonymise_audit(p_customer uuid, p_email text, p_user_id text, p_standin text)`.
     It sets `app.current_customer` (FORCE RLS), updates only `actor`, `before`, `after` and
     `metadata` (replacing the email and removing `email`, `full_name` and `target_email` keys) for
     rows of that one customer, and returns the count. EXECUTE granted to `app_admin` only; owned
     by the migration role; fixed `search_path`. It is the same pattern as 0062.
  3. Delete the `users` row (this cascades to `user_roles`, `user_sessions` and the token tables).
  4. Write a `privacy.erase` audit entry whose target is the stand-in, with per-table counts. It
     contains no email.
- **Response:** per-table counts, plus action items from `report` entries. For example, "This
  email is a recipient on notification channel *Facilities*", and for Entra users, "Remove this
  person from Entra ID, or they will be created again as a new account at sign-in."
- Also fix: helpdesk user deletion writes a `user.delete` audit entry.

## 4. Customer offboarding

**Schema:**

- `customers`: add `offboarding_started_at timestamptz`, `offboarding_started_by text`,
  `delete_after timestamptz`.
- New `customer_deletions` table: not tenant-scoped, no FK to `customers`, so it survives the
  delete. Columns: `id`, `customer_id`, `customer_name`, `started_at`, `started_by`,
  `cancelled_at`, `cancelled_by`, `deleted_at`, `row_counts jsonb`, `downloads jsonb` (who, when).
  Vendor-only read.

**Day 0, offboard:** `POST /api/v1/helpdesk/customers/{id}/offboard` `{ "confirm": "<customer name>" }`
(vendor admin). Sets the columns (`delete_after` = now + 30 days), revokes all the customer's
sessions and inserts the `customer_deletions` row. While a customer is offboarding:

- Portal sign-in by every method (password, magic link, Entra) is refused, and so is session
  resolution.
- Public API tokens return 401.
- Collector HMAC routes (`/ingest`, `/bridge/*`, tunnel) return 401.
- The nightly scheduler, alert notifications and digests skip the customer.
- `/public/branding` returns the default look.

The offboarding check must be one shared helper used by each of these paths, so none is missed.

**Cancel:** `POST /api/v1/helpdesk/customers/{id}/cancel-offboarding` clears the columns and records
`cancelled_*`. Nothing was deleted, so everything works again. Users simply sign in again.

**Export:** `GET /api/v1/helpdesk/customers/{id}/export.zip` (vendor admin) is generated on request
and never stored. It contains every tenant table as CSV + JSON, with `telemetry_daily` in place of
raw `telemetry`. The README names the columns the register marks as personal. Each download is
appended to `customer_deletions.downloads`.

**Day 30, delete:** the hourly retention pass picks customers with `delete_after < now()`. In one
transaction per customer it:

1. clears `collectors.standby_for` for the customer;
2. counts rows per tenant table;
3. runs `DELETE FROM customers WHERE id = $1`;
4. writes `row_counts` and `deleted_at`.

On error it rolls back, logs a warning, and tries again next hour. The pass uses the existing
advisory lock, so only one cloud task deletes.

## 5. Portal

- **Customer:** a *Data requests* page under Manage (needs `privacy.manage`) with *Export a
  person's data* and *Erase a person*. Erase shows the dry-run preview, asks you to type the email,
  then shows the result and action items. The Users page row menu gets *Export data* and *Erase*,
  which open the same flows with the email filled in.
- **Helpdesk:** the same page with a customer picker. On a customer: *Offboard customer*, with the
  name typed to confirm. While offboarding, a banner says "Offboarding: deleted in N days", with
  *Download export* and *Cancel offboarding*. A *Deletions* list shows past certificates, which
  can be downloaded as CSV.

## 6. Documents (working copy only, after release; not committed)

- `service-description-and-sla.md` §9 and `data-residency.md`: export by archive for 30 days;
  permanent deletion on day 30; backups expire within a further 35 days; certificate issued
  automatically. Remove "[TBC]" and "via the Public API".
- `security-whitepaper.md`: remove the S3 attachments claim; Next.js 14 → 15; add token retention
  rules and how erasure treats the audit log.
- New Mintlify page Admin → Data requests.
- On request, a DPIA note on the process.

## 7. Release order

Each release is checked on UAT before the next one starts.

1. Migrations (permission seed, function, offboarding columns, `customer_deletions`), the register,
   the guard test and the token retention rules.
2. Access request export, with its portal and helpdesk UI.
3. Erasure, with its UI and the helpdesk delete audit fix.
4. Offboarding, with its UI, the shared offboarding check and the deletion job.

## 8. Testing

- Guard test (above).
- Access export: seed a user with sessions, tokens, audit entries, dashboards and a remote session.
  The ZIP holds each one; a second user's data is absent. A deleted-account email still finds the
  audit entries.
- Erasure: after erasing, no register column of that customer contains the email or full name;
  the `audit_log` row count is unchanged; shared dashboards survive; the guards refuse
  self-erasure and last-admin erasure; a dry run changes nothing.
- Offboarding: a seeded customer's users, tokens and Collectors are rejected on every route;
  cancelling restores access; after the deletion pass, no row with that `customer_id` remains in any
  tenant table; `row_counts` match the seeded counts; the RESTRICT standby link doesn't block.
- Integration tests run as `app_admin`, as the existing retention tests do. Other tests' rows share
  the DB, so the deletion pass must only touch customers past `delete_after`.

## Out of scope

- A "download my own data" button for individual users.
- Deleting from CloudWatch logs or backups. They expire on their own (30 and 35 days), and the README
  and documents say so.
- Production infrastructure changes (Multi-AZ, 35-day backups). They are tracked separately.
