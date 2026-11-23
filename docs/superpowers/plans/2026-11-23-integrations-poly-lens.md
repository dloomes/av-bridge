# Integrations and Poly Lens-managed Teams Rooms Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Customers (or Involve for them) connect Poly Lens on a new Integrations page and add Microsoft Teams Rooms managed by Lens — devices with no Collector, polled cloud-to-cloud by the worker, with their own readings and alerts.

**Architecture:** `devices.collector_id` becomes optional with a `source` column; every Collector-dependent path either handles a NULL or refuses cleanly. A new cloud package talks to Lens (OAuth client credentials + GraphQL); the worker role polls each connected integration every minute, writes device rows/telemetry like ingest does, and evaluates alerts itself. The portal gets an Integrations page (logo cards) and a Teams Room path in the device form and device page.

**Tech Stack:** Go (av-bridge-cloud, pgx v5, Postgres RLS), Next.js/TypeScript (av-bridge-portal), Poly Lens GraphQL API.

**Spec:** `docs/superpowers/specs/2026-11-23-integrations-poly-lens-design.md`

## Global Constraints

- Existing Collector-polled device behaviour must not change; the full cloud suite (including DB integration tests with `TEST_DATABASE_URL`) and portal `npx tsc --noEmit` pass after every task.
- Lens client secret: encrypted with `secrets.Cipher` before storage, write-only in every API (return `has_secret: bool`), never logged, never in audit entries.
- Every Lens query is scoped to the integration's single stored Lens account id; a customer can never see another customer's Lens data, including with a shared Involve credential.
- On an existing integration only vendor (Involve) users may change the Lens account, and changing it requires re-entering the client secret — enforced in the API.
- Tagging Lens devices (`marcus:<device id>`) only when the integration's `allow_tagging` is true; tagging failures never block linking.
- Alert defaults (spec): offline 5 min critical; Teams signed out 10 min critical; Teams health unhealthy 10 min warning; peripherals missing 10 min warning; all auto-resolve; one notification on open and one on clear; held 01:00–05:00 in the building's time zone (`buildings.timezone`, default `Europe/London`) and opened only if still present after 05:00; existing Room Readiness switched-off suppression applies.
- Lens fields not in Lens's public docs (callStatus, teamsDeviceHealthStatus, teamsPresence*, allPeripheralsLinked, activeApplication*) are optional: missing/null means "unknown", never "bad".
- Migrations: next numbers from `0081`; RLS pattern as in `0056_remote_access.sql`; new tables classified in `internal/privacy/register.go` (the guard test enforces it).
- No room resource-account email is stored.
- Commit messages end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Release as v1.2.0 via tag → UAT → Promote to production.

## Review Focus

1. **A Lens-managed device appears in every list, report, dashboard widget and Public API call without errors** — NULL `collector_id` must not break any scan or join. Test: Task 2 `TestIntegrationDeviceInReadPaths`.
2. **Lens credentials revoked or Lens down** — linked devices show unknown "Poly Lens unreachable", the card shows "Needs attention", and no flood of offline alerts. Test: Task 7 `TestPollFailureMarksUnknownNotOffline`.
3. **Overnight Teams Rooms restart** — a room offline 02:10–02:30 raises no alert; one still offline at 05:05 does. Test: Task 8 `TestMaintenanceHold`.
4. **Shared Involve credential** — a customer admin cannot switch the account or list other accounts after connection. Test: Task 5 `TestAccountChangeVendorOnly`.
5. **Commands, moves, remote access, Room Readiness power steps aimed at a Lens device** — clean refusal with a clear message, never a 500 or a stuck command. Test: Task 3 `TestNoCollectorRefusals`.

---

## Part A — Devices without a Collector

### Task 1: Schema

**Files:**
- Create: `av-bridge-cloud/internal/db/migrations/0081_integrations.sql`
- Test: `av-bridge-cloud/internal/db/integrations_migration_integration_test.go`
- Modify: `av-bridge-cloud/internal/privacy/register.go` (classify new columns)

**Interfaces — Produces (SQL):**
- `devices.collector_id` nullable; `devices.source text NOT NULL DEFAULT 'collector' CHECK (source IN ('collector','poly_lens'))`; `CHECK ((source = 'collector') = (collector_id IS NOT NULL))`; `devices.platform text CHECK (platform IN ('windows','android'))` nullable.
- Trigger function `devices_bump_collector_config()` (0049/0065) skips when the relevant `collector_id` is NULL (no UPDATE, no `pg_notify` with NULL payload).
- `integrations (id uuid pk, customer_id uuid not null → customers cascade, provider text not null check in ('poly_lens'), status text not null check in ('connected','disconnected','error') default 'connected', client_id text, secret_enc bytea, account_id text, account_name text, allow_tagging bool not null default false, last_success_at timestamptz, last_error text, created_at, created_by text, updated_at, updated_by text, UNIQUE (customer_id, provider))`.
- `integration_devices (device_id uuid pk → devices cascade, customer_id uuid not null, integration_id uuid not null → integrations cascade, external_id text not null, external_name text, serial_number text, mac_address text, model text, tag_state text check in ('none','tagged','failed','left_behind') default 'none', tag_error text, UNIQUE (integration_id, external_id))`.
- `integration_alert_state (device_id uuid → devices cascade, customer_id uuid, condition text, since timestamptz not null, alert_id uuid → alerts set null, PRIMARY KEY (device_id, condition))`.
- RLS on all three (tenant_isolation; FORCE); grants SELECT to app_tenant and SELECT/INSERT/UPDATE/DELETE to app_admin; `integration_devices` also INSERT/DELETE to app_tenant (linking happens in the tenant API).
- `INSERT INTO role_permissions … 'integration.manage'` for system-default admin roles (pattern of 0056).

- [ ] **Step 1: Failing tests** `TestIntegrationsMigration` (own scratch DB from `TEST_DATABASE_URL`, like `internal/remote/presence_integration_test.go`): a device with `source='poly_lens'`, `collector_id NULL` inserts; `source='collector'` with NULL collector is rejected; `source='poly_lens'` with a collector is rejected; inserting/deleting a NULL-collector device raises no trigger error; `integrations` UNIQUE per customer+provider; tenant B cannot read tenant A's integrations rows through app_tenant.
- [ ] **Step 2: Run** → FAIL.
- [ ] **Step 3: Write the migration; classify new columns in `privacy.NotPersonal` (`created_by`/`updated_by` as actor labels per existing practice for similar columns, `external_name`/`account_name` as device/account names).**
- [ ] **Step 4: Run** the test, `internal/privacy` guard test and `go test ./... -count=1` → PASS.
- [ ] **Step 5: Commit** — `"Schema: devices without a Collector; integrations tables"`.

### Task 2: Read paths handle devices with no Collector

**Files:**
- Modify: `internal/devicestatus/status.go` (EffectiveStatusSQL), `internal/portalapi/handlers.go` (ListDevices :381-469, GetDevice :522-598, GetTelemetry :725-730, HelpdeskOverview inline copy :1321-1333, Status :169-171), `internal/portalapi/reports.go:115-125`, `internal/pubapi/devices.go` (:24, :73, :93, :229, :378-382), `internal/pubapi/openapi.json` (Device schema :531), `internal/audit/audit.go` (no change expected — confirm)
- Test: `internal/portalapi/integration_devices_read_integration_test.go`, `internal/devicestatus/status_test.go`

**Interfaces — Produces:**
- `EffectiveStatusSQL`: the Collector-staleness WHEN applies only `WHEN d.collector_id IS NOT NULL AND (…collector stale…) THEN 'unknown'`; the switched-off, 15-minute-stale→offline and latest_status branches apply to all devices. HelpdeskOverview uses the shared constant (remove its inline copy).
- Portal device JSON (list and detail) gains `source` (`"collector"|"poly_lens"`), `platform` (nullable), `integration` (`{provider, external_name, account_name, tag_state}` or null); `collector_id` becomes nullable (`*string`).
- Public API `publicDevice` gains `Source string` (`source`) and `Platform string` (`platform`, omitempty); OpenAPI updated.

- [ ] **Step 1: Failing test** `TestIntegrationDeviceInReadPaths`: seed one Collector device and one `poly_lens` device (with integration + link rows); assert ListDevices, GetDevice, GetTelemetry, Status counts, HelpdeskOverview, DeviceUptimeReport, pubapi ListDevices/GetDevice/GetDeviceTelemetry and the status filter all succeed and include the Lens device with `source` set; its status is `latest_status` (not `unknown`) when fresh, `offline` when `last_seen_at` is 16 min old.
- [ ] **Step 2: Run** → FAIL (NULL scan errors).
- [ ] **Step 3: Implement** — scan `collector_id` into `*string`/`pgtype.Text`; LEFT JOIN `integration_devices`/`integrations` where the integration summary is returned.
- [ ] **Step 4: Run** the new test, `go test ./... -count=1`, and `internal/pubapi` OpenAPI test → PASS.
- [ ] **Step 5: Commit** — `"Read paths: devices without a Collector (status, lists, reports, Public API)"`.

### Task 3: Write and command paths refuse cleanly

**Files:**
- Modify: `internal/commands/queue.go:64-90` (Submit), `internal/portalapi/commands.go` (:115, :196), `internal/portalapi/device_move.go` (:79-89, MoveDevices/ReplaceCollector), `internal/portalapi/remote_access.go:61-62`, `internal/nightly/fresh.go`, `internal/nightly/ping.go:161`, `internal/nightly/power.go:98`, `internal/nightly/executor.go:817`
- Test: `internal/portalapi/no_collector_integration_test.go`

**Interfaces — Produces:**
- `var ErrNoCollector = errors.New("device has no Collector")` in `internal/commands`; `Submit` returns it (no row inserted, no notify) for a device whose `collector_id` is NULL.
- Portal command/reconnect/bulk endpoints → HTTP 409 `{"error":"not available for devices managed by Poly Lens"}` per device; bulk reports per-device skips.
- Device move/replace → 409 for Lens devices (and they're excluded from ReplaceCollector's device set by `WHERE collector_id = $1`, unchanged).
- Remote access start → 409 same message.
- Room Readiness: `freshReadings` skips devices with no Collector (uses last report, as for `noFreshPoll` protocols); power steps and `_ping` skip them with step result "skipped: managed by Poly Lens"; `dispatchOne` treats `ErrNoCollector` as skipped, not failed.

- [ ] **Step 1: Failing test** `TestNoCollectorRefusals`: for a `poly_lens` device — POST command → 409 with the message, no `commands` row; bulk with one Lens + one Collector device → Collector device queued, Lens device skipped; move → 409; remote access start → 409; a readiness run containing the Lens device completes with the Lens device's power/ping steps "skipped" and a check-metric step using its latest reading.
- [ ] **Step 2: Run** → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the test and `go test ./... -count=1` → PASS.
- [ ] **Step 5: Commit** — `"Commands, moves, remote access and Room Readiness refuse cleanly for Lens devices"`.

## Part B — Integrations backend

### Task 4: Poly Lens cloud client

**Files:**
- Create: `av-bridge-cloud/internal/integrations/polylens/client.go`, `client_test.go`, `testdata/*.json`

**Interfaces — Produces:**
- `func New(clientID, clientSecret string, opts ...Option) *Client` (`WithBaseURLs(tokenURL, graphqlURL)` option for tests; defaults `https://login.lens.poly.com/oauth/token` and `https://api.silica-prod01.io.lens.poly.com/graphql`); token cached until 60 s before `expires_in`; one retry on 401.
- `type Account struct { ID, Name string }`; `func (c *Client) Accounts(ctx) ([]Account, error)` (tenants the credential can reach).
- `type Device struct { ID, Name, SerialNumber, MACAddress, Model, SoftwareVersion, RoomName, SiteName, AppName, AppVersion, CallStatus, TeamsHealth, PresenceAvailability, PresenceActivity string; Connected bool; LastDetected time.Time; PeripheralsLinked *bool; Tags []string }`.
- `func (c *Client) ListTeamsRooms(ctx, accountID string) ([]Device, error)` — pages `tenant(id){inventory{deviceSearch}}` with `nextToken`, page size 500; keeps devices where `AppName` contains "Teams" (case-insensitive) or a Teams field is non-empty (initial discriminator; Task 11 verifies).
- `func (c *Client) Devices(ctx, accountID string, ids []string) ([]Device, error)` — the linked devices, batched ≤ 500 ids per query.
- `func (c *Client) AddTag(ctx, deviceID, tag string) error`; `func (c *Client) RemoveTag(ctx, deviceID, tag string) error` (`addDeviceTag`/`removeDeviceTag`).
- Errors: `ErrUnauthorized` (bad credentials), `ErrRateLimited` (honour `ratelimit-reset`), others wrapped; secrets never in error strings.

- [ ] **Step 1: Failing tests** against an `httptest` fake (token + GraphQL): token caching and refresh; `Accounts`; `ListTeamsRooms` paging across 3 pages and filtering non-Teams devices; missing optional fields → zero values / nil `PeripheralsLinked`; 401 → `ErrUnauthorized` and the secret absent from `err.Error()`; rate-limit 429 → `ErrRateLimited`; `AddTag` sends `addDeviceTag(deviceId, tag)`.
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement.** **Step 4: Run** → PASS.
- [ ] **Step 5: Commit** — `"Poly Lens cloud client"`.

### Task 5: Integrations API

**Files:**
- Create: `internal/db/integrations.go`, `internal/portalapi/integrations.go`, `internal/portalapi/integrations_integration_test.go`
- Modify: `internal/portalauth/permissions.go` (`PermIntegrationManage = "integration.manage"`, KnownPermissions), `internal/db/customers.go` (`seedSystemRoles` admin list), `internal/api/server.go` (routes), `av-bridge-portal/src/lib/permissions.ts` (mirror entry)

**Interfaces — Produces (routes, JSON):**
- `GET /api/v1/integrations` (view.dashboard): `[{provider, status, account_name, allow_tagging, has_secret, client_id, last_success_at, last_error, linked_devices}]` plus static "coming soon" providers (`logitech_sync`, `microsoft_365`) and `servicenow` status derived from notification channels.
- `POST /api/v1/integrations/poly_lens/test` (integration.manage): body `{client_id, client_secret?}` (secret optional only when reusing the stored one for a vendor user) → `{accounts:[{id,name}]}`; customer users get the account list only on first connect.
- `PUT /api/v1/integrations/poly_lens` (integration.manage): body `{client_id, client_secret?, account_id, allow_tagging}`; first connect requires secret + account; on an existing integration a changed `account_id` → 403 for non-vendor, 400 if `client_secret` absent; secret encrypted; audit `integration.connect`/`integration.update` (no secret).
- `DELETE /api/v1/integrations/poly_lens` (integration.manage): status `disconnected`, `secret_enc` NULL, links kept; audit `integration.disconnect`.
- `GET /api/v1/integrations/poly_lens/devices?q=` (device.crud): Teams Rooms from Lens for the stored account: `[{external_id, name, model, serial, mac, room_name, site_name, connected, linked_device_id?, already_monitored_as?}]` — `already_monitored_as` when serial or MAC matches a Collector device of this customer.

- [ ] **Step 1: Failing tests** (fake Lens via `polylens.WithBaseURLs`): connect stores `secret_enc` (not plaintext) and responses never contain the secret; `TestAccountChangeVendorOnly` (customer admin changing `account_id` → 403; vendor without secret → 400; vendor with secret → 200; customer `test` after connection returns no other accounts); disconnect wipes secret and keeps links; picker marks linked and already-monitored rows; permission gates (viewer → 403 on PUT).
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement.** **Step 4: Run** + full suite → PASS.
- [ ] **Step 5: Commit** — `"Integrations API: connect Poly Lens, account rules, device picker"`.

### Task 6: Create and delete Lens-managed devices

**Files:**
- Modify: `internal/portalapi/admin_handlers.go` (`allowedTypes` :1169 add `teams_room`; `createDeviceReq` :1190; CreateDevice :1250-1529; DeleteDevice :1977-2001), `internal/adapters/catalogue.go` (no new protocol; `protocol` empty for Lens devices — confirm validation)
- Test: `internal/portalapi/lens_device_crud_integration_test.go`

**Interfaces — Produces:**
- `createDeviceReq` gains `Source string` (`source`, default `collector`), `Platform string`, `ExternalID string` (`external_id`). For `source=poly_lens`: type must be `teams_room`, platform `windows|android` required, `collector_id`/`reported_id`/`protocol`/credentials must be empty, the customer's Lens integration must be connected, `external_id` must exist in the Lens account and not be linked already (409); creates device + `integration_devices` row; if `allow_tagging`, best-effort `AddTag(external_id, "marcus:"+deviceID)` → `tag_state` tagged/failed; audit `device.create` includes source.
- DeleteDevice: for Lens devices with `tag_state='tagged'` and integration connected → best-effort `RemoveTag`; disconnected → `tag_state='left_behind'` recorded in the audit metadata.

- [ ] **Step 1: Failing tests:** create Lens device (no collector) → 201, link row, tag call made when allowed and not when disabled; tag failure → device created, `tag_state='failed'`; second link of same external id → 409; invalid platform/type → 400; delete removes tag when connected, records left_behind when disconnected.
- [ ] **Step 2–4:** fail → implement → pass (+ full suite).
- [ ] **Step 5: Commit** — `"Create and delete Teams Rooms managed by Poly Lens"`.

### Task 7: Worker poller

**Files:**
- Create: `internal/integrations/poller.go`, `poller_integration_test.go`
- Modify: `internal/config/config.go` (`IntegrationPollInterval` from `INTEGRATION_POLL_INTERVAL`, default 60s), `cmd/av-bridge-cloud/main.go` (construct with admin pool, cipher, relay, dispatcher; `go poller.Run(sweeperCtx)` under `if background`)

**Interfaces — Produces:**
- `type Readings struct { Status string; SignedIn, PeripheralsLinked *bool; TeamsHealth string }` and `type AlertEvaluator interface { Evaluate(ctx context.Context, customerID, deviceID string, r Readings) error }` (Task 8's `Alerter` implements it; until then main.go passes nil and the poller skips evaluation).
- `func NewPoller(admin *pgxpool.Pool, cipher secrets.Cipher, live wsfanout.Publisher, alerts AlertEvaluator, interval time.Duration, log *slog.Logger) *Poller`; `Run(ctx)`; `PollOnce(ctx) error`.
- Per connected integration: decrypt secret, `Devices(account, linked ids)`, then per device in one tenant transaction: update `devices` (`latest_status` = `online|offline`, `latest_metrics`, `last_seen_at=now()`, `last_online_at` when online, make/model/serial/firmware/mac), insert a `telemetry` row, publish a live `status`/`metrics` event via the relay; set integration `last_success_at`, clear `last_error`, status `connected`.
- Readings in `latest_metrics`: `teams_signed_in` (bool: presence not in Offline/PresenceUnknown), `call_state` (`in_call`|`idle`|`unknown` from CallStatus), `active_calls` (1 when in_call else 0), `teams_health`, `teams_presence`, `teams_app_version`, `peripherals_linked` (bool, omitted when unknown), `software_version`, `lens_last_seen` (RFC 3339).
- On `ErrUnauthorized`/network failure: integration `status='error'`, `last_error` sanitised; linked devices `latest_status='unknown'`, `latest_metrics.error = "Poly Lens unreachable"`, `last_seen_at=now()` (so the 15-minute stale rule does not turn them offline). Devices not returned for the account → unknown "Not found in this Lens account". Disconnected integrations → unknown "Integration disconnected", not polled.

- [ ] **Step 1: Failing tests:** `TestPollWritesReadings` (fake Lens; readings and status written, telemetry row, live event published); `TestPollFailureMarksUnknownNotOffline`; device missing from account → unknown with reason; disconnected integration not polled.
- [ ] **Step 2–4:** fail → implement → pass (+ full suite).
- [ ] **Step 5: Commit** — `"Worker polls Poly Lens integrations"`.

### Task 8: Alerts for integration devices

**Files:**
- Create: `internal/integrations/alerts.go`, `alerts_integration_test.go`

**Interfaces — Produces:**
- `func NewAlerter(admin *pgxpool.Pool, dispatcher *notify.Dispatcher, now func() time.Time, log *slog.Logger) *Alerter`; `func (a *Alerter) Evaluate(ctx, customerID, deviceID string, r Readings) error` (implements Task 7's `AlertEvaluator`; main.go now passes the Alerter to `NewPoller`).
- Conditions (key, hold, severity): `device_offline` 5m critical (status offline); `teams_signed_out` 10m critical (`teams_signed_in` false); `teams_unhealthy` 10m warning (`teams_health` present, not healthy, not unknown — healthy/unknown value set confirmed in Task 11); `peripherals_missing` 10m warning (`peripherals_linked` false). Unknown readings neither start nor clear a condition.
- State in `integration_alert_state`: record `since` when a condition starts; open when `now - since >= hold` and not in the maintenance window; insert into `alerts` (device subject, `alert_key`, severity, message) using the `ON CONFLICT … WHERE status='open'` pattern; `dispatcher.Dispatch` once on open. On condition end: resolve (`resolved_by='auto:recovered'`), Dispatch an info event `<key>_recovered`, delete state.
- Maintenance hold: building local time (rooms→buildings.timezone, default Europe/London) 01:00–05:00 → don't open; after 05:00 open if still present (a condition that started at 02:10 and persists opens at 05:00, not 02:20). Skip opening `device_offline` when `devices.switched_off_by_run IS NOT NULL` (same as `ingest.switchedOffOvernight`).

- [ ] **Step 1: Failing tests** with an injected clock: opens after hold; not before; `TestMaintenanceHold` (offline 02:10–02:30 → nothing; offline 02:10 still at 05:05 → opened once); auto-resolve + recovered dispatch; restart-safe (state survives a new Alerter); unknown readings don't flap; switched-off suppression; a fake dispatcher records exactly one open + one clear.
- [ ] **Step 2–4:** fail → implement → pass (+ full suite).
- [ ] **Step 5: Commit** — `"Alerts for Lens-managed rooms with overnight hold"`.

## Part C — Portal

### Task 9: Integrations page

**Files:**
- Create: `av-bridge-portal/src/app/integrations/page.tsx`, `src/components/integrations/lens-connect-form.tsx`, `public/integrations/{poly,logitech,microsoft,servicenow}.svg`
- Modify: `src/components/sidebar.tsx` (Manage: `{ href: "/integrations", label: "Integrations", icon: Plug, requires: "view.dashboard" }`), `src/lib/api.ts` (listIntegrations, testLens, saveLens, disconnectLens, listLensDevices), `src/lib/types.ts` (Integration types)

**Behaviour (pinned):** layout option A — responsive card grid; each card: logo (vendor's published mark, unaltered), name, category line, status pill (Connected green / Not connected grey / Coming soon grey / Needs attention amber with `last_error`), summary ("12 rooms linked · checked 1 min ago"), "Manage" / "Connect" button (hidden without `integration.manage`). Lens connect uses the existing `Modal`: Client ID, Client secret (placeholder "Leave blank to keep the current secret" when `has_secret`), "Test" → account select (shown on first connect, or to vendor users), "Allow M.A.R.C.U.S. to tag linked devices in Lens" checkbox, "Test and connect"; Disconnect with a confirm dialog explaining devices become unknown. Help text: "In Poly Lens: Admin Portal → Account Settings → API Credentials → Add New."

- [ ] **Step 1:** Implement; `npx tsc --noEmit` → no errors; `npm run build` → success.
- [ ] **Step 2:** The portal has no component-test harness; the visual check against real data happens in Task 11 Step 5 (UAT).
- [ ] **Step 3: Commit** — `"Portal: Integrations page and Poly Lens connect"`.

### Task 10: Teams Room device form and device page

**Files:**
- Modify: `src/lib/types.ts` (`DeviceType` + `teams_room` label "Microsoft Teams Room"; `DeviceSummary/DeviceDetail.collector_id` optional; `source`, `platform`, `integration`), `src/components/device-form.tsx`, `src/app/devices/[id]/page.tsx`, `src/components/telemetry-grid.tsx`, `src/components/status-badge.tsx`, `src/app/devices/page.tsx` (Collector column shows "Poly Lens")

**Behaviour (pinned):** device form — choosing type "Microsoft Teams Room" shows Platform (Windows/Android), Managed by (Poly Lens enabled only when connected; others disabled with hint), a searchable picker from `listLensDevices` (linked rows disabled "Linked already"; `already_monitored_as` shown as a warning but selectable), Room select pre-filled by matching the Lens room name (case-insensitive) — and hides Collector/protocol/address/credentials; submits `source:"poly_lens"`. Device page for Lens devices — pills: status, "Teams: signed in/out", "In a call/Idle" (existing CallBadge from `call_state`), "Teams health: …"; cards: Teams Rooms (app version, presence), Peripherals ("All peripherals linked"/"Peripheral missing"/"Unknown"), Device (model, software, serial, last seen by Lens), Source (Poly Lens account name, tag state, "Open in Lens" link to `https://lens.poly.com`); hide Command, Reconnect, Move, Remote access for these devices; StatusBadge shows the `error` reason ("Poly Lens unreachable") instead of "Collector offline".

- [ ] **Step 1:** Implement; `npx tsc --noEmit` and `npm run build` → success.
- [ ] **Step 2: Commit** — `"Portal: add and view Teams Rooms managed by Poly Lens"`.

## Part D — Verify, document, release

### Task 11: Verify against Involve's Lens account, docs, release

**Files:**
- Modify: `internal/integrations/polylens/client.go` and `internal/integrations/alerts.go` only if verification shows the discriminator, presence mapping or health values need adjusting (with tests)
- Docs (working copy in the main checkout, never committed): `docs/product/mintlify/static/` new `admin/integrations.mdx` and Poly Lens page; Security & Trust and Data Residency: Poly Lens (HP) as a customer-enabled integration

- [ ] **Step 1:** Daniel provides Involve's Lens credential via an environment variable (`LENS_CLIENT_ID`/`LENS_CLIENT_SECRET`) on the machine running the check — never in chat. Run a small Go test/driver (build-tagged `lensverify`, not run in CI) that calls `Accounts`, `ListTeamsRooms` and `Devices`, and prints counts and the distinct values of AppName, CallStatus, TeamsHealth and PresenceAvailability (no device names, serials or emails). Record findings in the task report.
- [ ] **Step 2:** Adjust the Teams discriminator, `teams_signed_in` mapping and healthy/unknown health values to the observed values; tests updated.
- [ ] **Step 3:** Tagging round-trip on two Involve Lens devices (`AddTag` then `RemoveTag`), confirmed in Lens; leave no tags behind.
- [ ] **Step 4:** Docs edits as listed.
- [ ] **Step 5:** Release: tag `v1.2.0` → UAT via pipeline; in UAT connect Involve's Lens account (Daniel), add one Windows and one Android MTR, watch readings and a test alert; then Daniel promotes v1.2.0.
- [ ] **Step 6: Commit** any code adjustments — `"Poly Lens: field mapping verified against a real tenant"`.
