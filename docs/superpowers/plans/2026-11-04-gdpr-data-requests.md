# GDPR Data Requests Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give M.A.R.C.U.S. customer offboarding (30-day delete with certificate), per-person access-request export and per-person erasure, all driven by one register of personal data.

**Architecture:** A new `internal/privacy` package holds the register: every personal-data column and its erasure rule. It also holds `FindSubject`, `Collect`, `WriteZIP` and `Erase`. Portal and helpdesk handlers in `internal/portalapi` call it. The audit log is rewritten only through a SECURITY DEFINER function. Offboarding adds columns on `customers`, plus one SQL predicate (`db.CustomerActiveSQL`) that every authentication and background path uses. The final deletion runs inside the existing hourly retention pass.

**Tech Stack:** Go 1.22 (cloud), pgx v5, PostgreSQL 16 with RLS, `archive/zip`, `encoding/csv`; Next.js 15 / React 19 portal.

**Spec:** `docs/superpowers/specs/2026-11-04-gdpr-data-requests-design.md`

## Global Constraints

- Offboarding period: **30 days** (`delete_after = offboarding_started_at + interval '30 days'`).
- Stand-in format: `erased-user-` plus the first 8 hex characters of the user ID with dashes removed. With no account: the first 8 hex characters of `sha256(lower(email))`.
- New permission key: `privacy.manage`. Label *Handle data requests*. Seeded on every system-default `admin` role. Vendor admins have it through the bypass.
- Token retention: `password_reset_tokens`, `magic_link_tokens` and `collector_enrollment_tokens` are deleted 30 days after expiry or use. `api_tokens` are deleted 90 days after revocation or expiry.
- The export uses `telemetry_daily`, never raw `telemetry`.
- Audit actions: `privacy.access_export`, `privacy.erase`, plus `user.delete` on the helpdesk delete. Erasure and export audit rows hold no email: export metadata carries `email_sha256`.
- Erasure and customer deletion each run in **one transaction**. On failure nothing changes. Exports return an error, never a partial ZIP.
- Integration tests run only with `TEST_DATABASE_URL`, and connect as `app_admin` / `app_tenant` like `internal/portalapi/user_guards_integration_test.go`. Tests share one database and run in parallel, so seed your own customers and never assert global counts.
- Every file under `docs/product/**` is edited in the working copy only and **never committed**.
- Next migration number is **0073**. Each later task that needs schema takes the next free number.

## Review Focus

1. **Email case:** a request for `Alice@Example.com` must match rows stored as `alice@example.com`. Every match is `lower(col) = lower($email)`. Tests: Task 4 and Task 6 seed mixed case.
2. **Same email in two customers:** a customer admin's export or erasure must never touch another customer's rows. That matters most for tables keyed only by email text, and for `audit_log`. Tests: Task 4 and Task 6 seed a second customer with the same email and assert its rows are untouched.
3. **Erasing a person with no account left:** a hard-deleted user still named in the audit log must be found by email and anonymised, using the hash stand-in. Test: Task 6.
4. **A sign-in path or Collector route missed during offboarding:** there are four `INSERT INTO user_sessions` sites, a session resolver, the API token resolver and the Collector lookup. Test: Task 8 drives each one.
5. **Deleting a customer whose Collectors are a warm-standby pair:** `collectors.standby_for` is `ON DELETE RESTRICT` and must not block deletion. Test: Task 10 seeds a pair.

---

## Release 1: groundwork

### Task 1: Migration, permission and seed

**Files:**
- Create: `av-bridge-cloud/internal/db/migrations/0073_privacy.sql`
- Modify: `av-bridge-cloud/internal/portalauth/permissions.go` (add const and `KnownPermissions` entry)
- Modify: `av-bridge-cloud/internal/db/customers.go:255-270` (add `"privacy.manage"` to the default admin role list)
- Test: `av-bridge-cloud/internal/privacy/migration_integration_test.go`

**Interfaces:**
- Produces:
  - `portalauth.PermPrivacyManage = "privacy.manage"`.
  - Columns `customers.offboarding_started_at timestamptz`, `customers.offboarding_started_by text` and `customers.delete_after timestamptz`.
  - Table `customer_deletions(id uuid pk default gen_random_uuid(), customer_id uuid not null, customer_name text not null, started_at timestamptz not null, started_by text not null, cancelled_at timestamptz, cancelled_by text, deleted_at timestamptz, row_counts jsonb, downloads jsonb not null default '[]')`. No RLS and no FK. GRANT SELECT, INSERT, UPDATE to `app_admin`.
  - Function `privacy_anonymise_audit(p_customer uuid, p_email text, p_user_id text, p_standin text) RETURNS bigint`.

- [ ] **Step 1: Write the failing test** `TestPrivacyMigration`. It should:
  - seed a customer through `db.Store.CreateCustomer` and assert its `admin` role has `privacy.manage`;
  - insert `audit_log` rows for that customer: one with `actor='Alice@x.io'` and `after={"email":"alice@x.io","full_name":"Alice A","role":"admin"}`, and one unrelated;
  - insert the same actor in a second customer;
  - call `SELECT privacy_anonymise_audit($cust, 'alice@x.io', $uid, 'erased-user-abcd1234')` as `app_admin`.

  Assert:
  - it returns 1;
  - that row's `actor = 'erased-user-abcd1234'`;
  - `after` has no `email` or `full_name` keys and still has `role`;
  - the other customer's row is unchanged;
  - `app_admin` still gets "permission denied" on a direct `UPDATE audit_log`.
- [ ] **Step 2: Run it.** `go test ./internal/privacy/ -run TestPrivacyMigration -v` with `TEST_DATABASE_URL` set. Expected: FAIL (function does not exist).
- [ ] **Step 3: Write `0073_privacy.sql`.**
  - The permission seed uses the 0063 `INSERT … SELECT … WHERE r.name='admin' AND r.is_system_default AND NOT EXISTS` pattern.
  - The function follows 0062 exactly: SECURITY DEFINER, `SET search_path = public, pg_temp`, `set_config('app.current_customer', p_customer::text, true)`, REVOKE from PUBLIC, GRANT EXECUTE to `app_admin`.
  - Body: for rows with `customer_id = p_customer`, and either `lower(actor) = lower(p_email)`, or `target_id = p_user_id`, or `lower(before::text || after::text || metadata::text) LIKE '%' || lower(p_email) || '%'`:
    - set `actor` to `p_standin` where it matched;
    - strip the keys `email`, `full_name`, `target_email` and `requester_email` from `before`, `after` and `metadata`;
    - replace any remaining case-insensitive occurrence of the email inside those three JSON values with `p_standin`, using `regexp_replace(x::text, quote_email, p_standin, 'gi')::jsonb` with the email regex-escaped.
  - Return the number of rows changed.
- [ ] **Step 4: Add the permission.** Add the const with a one-line comment in the existing style, the `KnownPermissions` entry, and the `customers.go` seed entry.
- [ ] **Step 5: Run it.** Same command plus `go test ./internal/portalauth/`. Expected: PASS.
- [ ] **Step 6: Commit** with the message `Privacy groundwork: permission, offboarding columns, audit anonymise function`.

### Task 2: Personal data register and guard test

**Files:**
- Create: `av-bridge-cloud/internal/privacy/register.go`
- Test: `av-bridge-cloud/internal/privacy/register_integration_test.go`

**Interfaces:**
- Produces:
```go
type Key int      // KeyUserID (uuid column = users.id), KeyEmail (text column holding an email)
type Action int   // ActDeleteRow, ActReplace, ActStripJSON, ActReport
type Entry struct {
    Table, Column string
    Key           Key
    Category      string   // "account" | "network" | "activity" | "contact"
    Action        Action
    JSONKeys      []string // ActStripJSON only
    Scoped        bool     // table has customer_id; statements must filter on it
    Reason        string   // required for ActReport
}
type Exempt struct{ Table, Column, Reason string }
var Personal []Entry
var NotPersonal []Exempt
var GuardPattern *regexp.Regexp // email|name|_ip$|ip_address|user_agent|actor|_by$|recipients|phone|contact
```
- `Personal` must cover at least everything in the spec's "Current state" list. That includes: `users.email`, `users.full_name`, `user_sessions.*` IP address and browser, the three token tables, `api_tokens.last_used_ip`, `api_tokens.created_by`/`revoked_by` (KeyUserID, set NULL), `remote_session.actor`/`end_reason`/`user_id`, `dashboards.created_by`/`owner_user_id`, `dashboard_display_links.created_by`, `firmware_targets.updated_by`, `schedule_date.created_by`, `notification_channels.target` (ActReport), `nightly_schedule.helpdesk_email` (ActReport), `room_nightly_config.notification_recipients` (ActReport) and `customers.support_contact` (ActReport). `audit_log` is handled by the function, so give it one entry with Action `ActStripJSON` and Reason "via privacy_anonymise_audit".

- [ ] **Step 1: Write the failing test** `TestRegisterCoversSchema`. It queries `information_schema.columns` for `table_schema='public'`. For every column matching `GuardPattern` it asserts the column is in `Personal` or `NotPersonal`, and fails with a list of the unregistered ones. A second check asserts that every `Entry.Scoped` equals "table has a `customer_id` column".
- [ ] **Step 2: Run it.** `go test ./internal/privacy/ -run TestRegisterCoversSchema -v`. Expected: FAIL listing columns.
- [ ] **Step 3: Write `register.go`.** Add entries until the test passes. Each `NotPersonal` reason is one short phrase, for example "device name, not a person".
- [ ] **Step 4: Run it.** Expected: PASS.
- [ ] **Step 5: Commit** with the message `Personal data register with schema guard test`.

### Task 3: Token retention rules

**Files:**
- Modify: `av-bridge-cloud/internal/retention/retention.go:35-53` (`Rules`)
- Create: `av-bridge-cloud/internal/db/migrations/0074_token_retention.sql` (GRANT DELETE on the four tables to `app_admin`, for any table where it lacks it)
- Test: `av-bridge-cloud/internal/retention/retention_integration_test.go`

- [ ] **Step 1: Extend `TestRetentionRules`.** Seed, for one user, each token table with one row 31 days past `expires_at` and one row 1 day past. Seed `api_tokens` with one row revoked 91 days ago, one revoked 10 days ago, and one active with no expiry. After `Pass`, assert only the old rows are gone.
- [ ] **Step 2: Run it.** `go test ./internal/retention/ -v`. Expected: FAIL.
- [ ] **Step 3: Add four `Rule`s.** Name them `"password reset tokens (30 days after expiry)"`, and so on. Use `Where` on `GREATEST(t.expires_at, COALESCE(t.used_at, t.expires_at)) < now() - interval '30 days'`, after checking the real column names in migrations 0033, 0037 and 0038. For `api_tokens`, use `COALESCE(t.revoked_at, t.expires_at) < now() - interval '90 days'`.
- [ ] **Step 4: Run it.** Expected: PASS.
- [ ] **Step 5: Update `security-whitepaper.md` §6** retention sentence in the working copy (not committed).
- [ ] **Step 6: Commit** the Go and SQL files with the message `Retention for sign-in tokens and old API tokens`.

**Release 1 ships here** as cloud vX.Y.Z (next version): build, push and roll out as in the memory notes, then check `/healthz`.

---

## Release 2: access request export

### Task 4: Collect and ZIP

**Files:**
- Create: `av-bridge-cloud/internal/privacy/subject.go`, `collect.go`, `zip.go`
- Test: `av-bridge-cloud/internal/privacy/collect_integration_test.go`, `zip_test.go`

**Interfaces:**
- Consumes: `Personal` from Task 2.
- Produces:
```go
type Subject struct {
    CustomerID     string // empty for vendor staff
    VendorTenantID string // set for vendor staff
    Email          string // as requested
    UserID         string // empty if no account remains
}
func FindSubject(ctx context.Context, tx pgx.Tx, customerID, vendorTenantID, email string) (Subject, error)
type Table struct { Name string; Columns []string; Rows [][]any }
func Collect(ctx context.Context, tx pgx.Tx, s Subject) ([]Table, error)   // tables with ≥1 row only
func WriteZIP(w io.Writer, tables []Table, readme string) error            // <name>.csv + <name>.json each, README.txt
func AccessREADME(s Subject, tables []Table) string
```
- `Collect` runs in an `AdminPool` transaction (BYPASSRLS). So every query on a `Scoped` table filters `customer_id = s.CustomerID`, and tables keyed by user ID filter `user_id = s.UserID`. For `audit_log`, rows match on actor or `target_id` or payload, the same conditions as the Task 1 function.

- [ ] **Step 1: Write `TestWriteZIP`** (unit). Given two tables, the ZIP holds `users.csv`, `users.json`, `audit_log.csv`, `audit_log.json` and `README.txt`. The CSV header equals `Columns`, and a JSON value with a comma survives the CSV round trip.
- [ ] **Step 2: Write `TestCollect`** (integration). Seed customer A with user `Alice@Example.com`: two sessions with IP addresses, an API token she created, three audit rows she made, one audit row targeting her, a personal dashboard and a remote session. Seed customer B with a user of the same email and one audit row. Call `Collect(FindSubject(A, "alice@example.com"))` and assert:
  - `users`: 1 row;
  - `user_sessions`: 2;
  - `audit_log`: 4;
  - `dashboards`: 1;
  - `remote_session`: 1;
  - no row from customer B.

  Then hard-delete Alice's `users` row. `FindSubject` returns `UserID == ""`, and `Collect` still returns the 4 audit rows.
- [ ] **Step 3: Run both.** `go test ./internal/privacy/ -v`. Expected: FAIL.
- [ ] **Step 4: Implement the three files.** The README lists each table with its category and retention period. Take retention from `retention.Rules` names where they exist, and copy the spec's fixed sentence about application logs (30 days) and backups (35 days).
- [ ] **Step 5: Run both.** Expected: PASS.
- [ ] **Step 6: Commit** with the message `privacy: collect a person's data and write it as a ZIP`.

### Task 5: Access export endpoints

**Files:**
- Create: `av-bridge-cloud/internal/portalapi/privacy.go`
- Modify: `av-bridge-cloud/internal/api/server.go` (routes next to the users routes and the helpdesk block)
- Test: `av-bridge-cloud/internal/portalapi/privacy_integration_test.go`

**Interfaces:**
- Consumes: Task 4.
- Produces:
  - `POST /api/v1/privacy/access-export` `{email}` uses `wrapPerm(portalauth.PermPrivacyManage, …)`.
  - `POST /api/v1/helpdesk/privacy/access-export` `{customer_id, email}`, or `{vendor: true, email}` for Involve staff, uses `RequireVendorAdmin`.
  - Both respond `200 application/zip`, `Content-Disposition: attachment; filename="access-request-<yyyy-mm-dd>.zip"`, `Cache-Control: no-store`.
  - 404 `"no data found for that email"` when `Collect` returns nothing.
  - Handlers: `(h *Handler) AccessExport` and `(h *Handler) HelpdeskAccessExport`.

- [ ] **Step 1: Write `TestAccessExportHTTP`** following `user_guards_integration_test.go` for sessions. Check:
  - a customer admin gets 200 and a ZIP that unzips;
  - a customer viewer without `privacy.manage` gets 403;
  - an unknown email gets 404;
  - one `privacy.access_export` audit row is written, with metadata `email_sha256` equal to the hex SHA-256 of `lower(email)` and no `@` anywhere in the row.
- [ ] **Step 2: Run it.** Expected: FAIL.
- [ ] **Step 3: Implement.** Build the ZIP into a `bytes.Buffer` first, then write the headers and body.
- [ ] **Step 4: Run it.** Expected: PASS.
- [ ] **Step 5: Commit** with the message `Access request export endpoints`.

**Release 2 ships here, API only.** This differs from spec §7: the *Data requests* page arrives in Task 7 with both actions, so the page appears once, complete. Until then, export can be run with `curl` against the endpoint.

---

## Release 3: erasure

### Task 6: Erase

**Files:**
- Create: `av-bridge-cloud/internal/privacy/erase.go`
- Modify: `av-bridge-cloud/internal/portalapi/privacy.go` (handlers), `internal/api/server.go` (routes)
- Modify: `av-bridge-cloud/internal/portalapi/helpdesk_users.go:178-201` (write the `user.delete` audit entry)
- Test: `av-bridge-cloud/internal/privacy/erase_integration_test.go`, plus cases in `privacy_integration_test.go`

**Interfaces:**
- Consumes: `Subject`, `FindSubject`, `Personal`, and the `privacy_anonymise_audit` function.
- Produces:
```go
func StandIn(s Subject) string
type Result struct {
    StandIn string
    Counts  map[string]int64 // "table.column" → rows changed (or "table" for deletes)
    Actions []string         // ActReport messages + Entra note
}
func Erase(ctx context.Context, tx pgx.Tx, s Subject, dryRun bool) (Result, error)
```
- Endpoints:
  - `POST /api/v1/privacy/erase` `{email, confirm, dry_run}` (privacy.manage).
  - `POST /api/v1/helpdesk/privacy/erase` `{customer_id | vendor, email, confirm, dry_run}` (vendor admin).
  - 400 `"confirm must match email"`. The guards return the same errors as `DeleteUser`, via `h.guardManageUser`, plus a self check.
  - 200 returns `{stand_in, counts, actions}`.
- Order inside `Erase`:
  1. the `ActReplace` and `ActStripJSON` entries;
  2. `ActDeleteRow` entries, and personal dashboards (`owner_user_id = UserID AND NOT shared`; check the real column in 0058);
  3. `SELECT privacy_anonymise_audit(...)`;
  4. `DELETE FROM users`;
  5. the `privacy.erase` audit row with `TargetKind "user"`, `TargetID` set to the stand-in, and `Metadata {"counts": …}`.

  A dry run executes the same statements with `RETURNING`/counts inside a savepoint that is rolled back. Use `tx.Begin` (a nested pgx savepoint) and `Rollback`.
- The Entra action message is added when `users.provider = 'entra'`. Copy: `"Remove this person from Microsoft Entra ID too, or they will be created again as a new account when they next sign in."`

- [ ] **Step 1: Write `TestErase`** with the Task 4 seed (A and B, mixed-case email) plus a shared dashboard Alice created and a notification channel targeting her email. After `Erase(dryRun=true)`, nothing has changed and the counts are non-zero. After `Erase(dryRun=false)`:
  - no `Personal` column for customer A contains `alice@example.com` or `Alice A`, case-insensitive; the test loops over `Personal`, skipping ActReport;
  - the `audit_log` count for A is unchanged;
  - the shared dashboard exists, with `created_by` set to the stand-in;
  - customer B is untouched;
  - `Actions` mentions the notification channel by name.
- [ ] **Step 2: Write `TestEraseNoAccount`.** Hard-delete the user first. Erase by email uses the hash stand-in, and the audit rows are anonymised.
- [ ] **Step 3: Write HTTP cases** in `TestPrivacyEraseHTTP`:
  - erasing yourself returns 400;
  - erasing the last admin returns the guard error;
  - a mismatched confirm returns 400;
  - success writes exactly one `privacy.erase` row containing no `@`;
  - the helpdesk `DELETE /api/v1/helpdesk/users/{id}` now writes a `user.delete` row.
- [ ] **Step 4: Run them.** Expected: FAIL.
- [ ] **Step 5: Implement.**
- [ ] **Step 6: Run them.** Expected: PASS. Also run `go test ./...`.
- [ ] **Step 7: Commit** with the message `Erasure: anonymise a person across the register and the audit log`.

### Task 7: Portal: Data requests page

**Files:**
- Create: `av-bridge-portal/src/app/data-requests/page.tsx`
- Modify: `av-bridge-portal/src/lib/api.ts`. Add `privacyAccessExport(email): Promise<Blob>` and `privacyErase(req): Promise<EraseResult>`, plus the helpdesk variants that take `customerId?` and `vendor?`. The blob download follows the `exportAssetsCSV` fetch pattern, but with `res.blob()`.
- Modify: `av-bridge-portal/src/components/sidebar.tsx` (under Manage, shown when `privacy.manage`)
- Modify: `av-bridge-portal/src/app/users/page.tsx` (row menu: *Export data*, *Erase* → `/data-requests?email=…&action=…`)
- Modify: `av-bridge-portal/src/app/helpdesk/page.tsx` (link to `/data-requests?helpdesk=1`, which shows a customer picker plus "Involve staff")

- [ ] **Step 1: Build the page.**
  - **Export:** an email field and a button that downloads `access-request-<date>.zip`.
  - **Erase:** an email field, then *Preview* (dry run), which shows the counts table. Then a confirm field ("Type the email address to confirm") and an *Erase* button, which shows the stand-in, the counts and each action in an amber list.
  - Copy uses plain words, matching the existing pages.
- [ ] **Step 2: Run `npx tsc --noEmit` and `npm run build`.** Expected: no errors.
- [ ] **Step 3: Commit** with the message `Portal: Data requests page (export and erase)`.

**Release 3 ships here.** Cloud gets the next version and the portal is pushed (check Amplify list-jobs). Release 2's endpoints go out in the same cloud build if Release 2 wasn't shipped on its own.

---

## Release 4: offboarding

### Task 8: Offboarding check on every path

**Files:**
- Create: `av-bridge-cloud/internal/db/offboarding.go`
- Modify:
  - `internal/portalauth/local.go:79-100` (session query)
  - `internal/pubapi/auth.go` (`resolve` query)
  - `internal/db/db.go:297` (`LookupCollectorByBridgeID`)
  - the 4 session inserts: `internal/portalapi/auth.go:124`, `entra.go:443`, `entra_customer.go:516` and `internal/publicapi/magic_link.go:108`
  - `/public/branding` handler
  - the customer loops in `internal/nightly`, `internal/notify` and `internal/collectorhealth`
- Test: `av-bridge-cloud/internal/portalapi/offboarding_integration_test.go`

**Interfaces:**
- Produces:
```go
// CustomerActiveSQL returns a predicate true when the customer in col is not offboarding.
func CustomerActiveSQL(col string) string // "NOT EXISTS (SELECT 1 FROM customers oc WHERE oc.id = <col> AND oc.offboarding_started_at IS NOT NULL)"
func (s *Store) CustomerOffboarding(ctx context.Context, customerID string) (bool, error)
var ErrCustomerOffboarding = errors.New("customer offboarding")
```
- Sign-in refusals return 403 with `"This organisation's M.A.R.C.U.S. account has been closed."` Vendor users (customer_id NULL) are never blocked: the predicate is `(u.customer_id IS NULL OR <CustomerActiveSQL("u.customer_id")>)`.

- [ ] **Step 1: Write `TestOffboardingBlocksEveryPath`.** Seed a customer with an admin session, a magic link, an API token and a Collector with a known HMAC secret. Set `offboarding_started_at = now()` directly. Assert:
  - the session cookie gets 401 on `GET /api/v1/users`;
  - password login gets 403;
  - redeeming the magic link gets 403;
  - the bearer token gets 401 on `/pub/v1/ping`;
  - a signed `POST /bridge/poll` gets 401;
  - `/public/branding?slug=` returns the default (empty) branding;
  - a vendor session still works.

  Then clear the column, and the session and token work again. Drive the Entra paths by calling their session-creating helper directly, if one exists. Otherwise assert the check happens before the insert by code review, and note that in the PR.
- [ ] **Step 2: Write `TestBackgroundSkipsOffboarding`.** Drive one pass of the Collector health watcher and the nightly scheduler for an offboarding customer with a stale Collector and a due schedule. Assert there is no alert, no notification row and no command.
- [ ] **Step 3: Run them.** Expected: FAIL.
- [ ] **Step 4: Implement**, adding the predicate in each listed place.
- [ ] **Step 5: Run them**, then `go test ./...`. Expected: PASS.
- [ ] **Step 6: Commit** with the message `Offboarding: refuse sign-in, tokens, Collectors and background work`.

### Task 9: Offboard, cancel and customer export

**Files:**
- Create: `av-bridge-cloud/internal/portalapi/offboarding.go`, `av-bridge-cloud/internal/privacy/customer_export.go`
- Modify: `internal/api/server.go` (helpdesk block, `RequireVendorAdmin`)
- Test: add cases to `offboarding_integration_test.go`

**Interfaces:**
- Consumes: `WriteZIP`, `Table`, `Personal`.
- Produces:
```go
func CollectCustomer(ctx context.Context, tx pgx.Tx, customerID string) ([]Table, error) // every customer_id table except raw telemetry
func CustomerREADME(name string, tables []Table) string                                 // names Personal columns
```
- Endpoints:
  - `POST /api/v1/helpdesk/customers/{id}/offboard` `{confirm}`. `confirm` must equal the customer name; on mismatch, 400 `"confirm must match the customer name"`. If already offboarding, 409.
  - `POST /api/v1/helpdesk/customers/{id}/cancel-offboarding`. If not offboarding, 409.
  - `GET /api/v1/helpdesk/customers/{id}/export.zip`. Appends `{"by": actor, "at": now}` to `customer_deletions.downloads` of the open row.
  - `GET /api/v1/helpdesk/deletions` returns the list.
  - `GET /api/v1/helpdesk/deletions/{id}.csv` returns `table,rows` lines plus a header block.
  - Offboard sets the three columns, revokes every session of the customer's users, and inserts the `customer_deletions` row, all in one transaction.
  - The helpdesk customer list response gains `offboarding_started_at` and `delete_after`.

- [ ] **Step 1: Write the HTTP cases.**
  - Offboard with the wrong name returns 400.
  - Offboard returns 200: `delete_after` is about now + 30 days, sessions are revoked and a deletion row exists.
  - A second offboard returns 409.
  - The export ZIP contains `devices.csv` and `telemetry_daily.csv`, has no `telemetry.csv`, and adds one download entry.
  - Cancel works, a second cancel returns 409, and the customer's token works again.
  - A non-admin vendor gets 403.
- [ ] **Step 2: Run them.** Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them.** Expected: PASS.
- [ ] **Step 5: Commit** with the message `Offboard, cancel and export a customer`.

### Task 10: Day-30 deletion and certificate

**Files:**
- Create: `av-bridge-cloud/internal/retention/customers.go`
- Modify: `av-bridge-cloud/internal/retention/retention.go` (call `deleteDueCustomers` at the start of `Pass`, under the existing advisory lock)
- Test: `av-bridge-cloud/internal/retention/customers_integration_test.go`

**Interfaces:**
- Produces: `func deleteDueCustomers(ctx context.Context, conn *pgx.Conn, log *slog.Logger) error`, which works through each customer with `delete_after < now()`. Each customer gets its own transaction:
  1. `UPDATE collectors SET standby_for = NULL WHERE customer_id = $1`;
  2. count rows for every table with a `customer_id` column, discovered from `information_schema` so new tables are included;
  3. `DELETE FROM customers WHERE id = $1`;
  4. `UPDATE customer_deletions SET deleted_at = now(), row_counts = $2 WHERE customer_id = $1 AND deleted_at IS NULL AND cancelled_at IS NULL`.

  An error rolls back, logs `retention: customer delete failed` at Warn with the customer ID, and continues with the next customer.

- [ ] **Step 1: Write `TestDeleteDueCustomers`.** Seed customer X with a hierarchy, two Collectors in a warm-standby pair, devices, telemetry, audit rows, users with sessions, and a `customer_deletions` row with `delete_after` in the past. Seed customer Y offboarding with `delete_after` in the future. Seed customer Z active. After `Pass`:
  - no row with X's `customer_id` remains in any table that has the column;
  - X's `row_counts` equal the seeded counts;
  - Y and Z are untouched.
- [ ] **Step 2: Run it.** Expected: FAIL.
- [ ] **Step 3: Implement.** Confirm the cascade can delete `audit_log` rows when the app role lacks DELETE; Postgres runs FK cascades as the table owner. If the test shows otherwise, add a SECURITY DEFINER `privacy_delete_customer(uuid)` in a new migration, following the 0062 pattern.
- [ ] **Step 4: Run it**, then `go test ./...`. Expected: PASS.
- [ ] **Step 5: Commit** with the message `Delete offboarded customers on day 30 and record a certificate`.

### Task 11: Helpdesk offboarding UI

**Files:**
- Modify: `av-bridge-portal/src/lib/api.ts` (`offboardCustomer`, `cancelOffboarding`, `downloadCustomerExport` (blob), `listDeletions`, `downloadDeletionCSV`)
- Modify: `av-bridge-portal/src/app/helpdesk/page.tsx` (or the customer detail component in `components/customer-forms.tsx`)
- Create: `av-bridge-portal/src/app/helpdesk/deletions/page.tsx`

- [ ] **Step 1: Build the UI.**
  - *Offboard customer* button: a dialog explaining what happens on day 0 and day 30, with a field to type the customer name.
  - Offboarding banner: "Offboarding: deleted on <date> (N days)", with *Download export* and *Cancel offboarding*.
  - Deletions page: a table of customer, started, by, deleted/cancelled and downloads, with a CSV link per row.
- [ ] **Step 2: Run `npx tsc --noEmit` and `npm run build`.** Expected: no errors.
- [ ] **Step 3: Commit** with the message `Helpdesk: offboard customers and view deletion certificates`.

### Task 12: Documents (working copy only)

**Files** (none committed):
- `docs/product/service-description-and-sla.md` §9
- `docs/product/data-residency.md` (tenant termination and export bullets)
- `docs/product/security-whitepaper.md` (§6, §7 Next.js 15, S3 claim)
- new `docs/product/mintlify/static/admin/data-requests.mdx` plus its `build.py` page entry

- [ ] **Step 1: Make the spec §6 wording changes.** Use the 30 days → delete → 35-day backup expiry → automatic certificate wording, and remove "via the Public API" and "[TBC]".
- [ ] **Step 2: Write the Mintlify page**, covering who can use it, export, erase, what erase can't reach (Entra, notification channels, logs and backups), and offboarding (helpdesk).
- [ ] **Step 3: Leave all of it uncommitted** and tell the user which files changed.

**Release 4 ships here:** next cloud version plus a portal push.
