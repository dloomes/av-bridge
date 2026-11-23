# Integrations and Poly Lens-managed Microsoft Teams Rooms — design

Date: 2026-11-23 · Status: approved

## Why

Customers' Microsoft Teams Rooms (MTRs) are a large share of the rooms M.A.R.C.U.S. should watch, and Microsoft offers no API for Teams Rooms device health (the Graph teamwork devices API was retired in December 2025). Many MTRs are Poly devices or are watched by Poly Lens, whose API exposes the Teams app, call status, Teams health and peripheral state. Daniel wants customers (or Involve on their behalf) to connect their Poly Lens account themselves on a new **Integrations** page, and to add a Teams Room that is **managed by Poly Lens** — without needing a Collector on site. Windows-direct monitoring (WinRM to the MTR) is parked for later; Logitech Sync and Microsoft 365 are later integrations on the same page.

## Goals

- **Integrations page** (Manage → Integrations) in the card layout Daniel chose (mockup option A): one card per integration with the vendor logo, status pill ("Connected", "Not connected", "Coming soon", "Needs attention") and a short summary (e.g. "12 rooms linked · checked 1 min ago"). Poly Lens is live; Logitech Sync and Microsoft 365 show as "Coming soon"; ServiceNow shows its status and links to Alert channels (where it is configured today).
- **Connect Poly Lens** per customer: client ID, client secret (stored encrypted, never shown again), a choice of Lens account when the credential covers several, "Test and connect", and an opt-in "Allow M.A.R.C.U.S. to tag linked devices in Lens". Edit and disconnect.
- **New device type "Microsoft Teams Room"** with **platform** (Windows or Android) and **managed by** (Poly Lens now; others later).
- **Add a Lens-managed Teams Room one at a time** (Daniel: no bulk import in the first release): pick the device from a searchable list of the customer's Lens Teams Rooms devices; already-linked devices are greyed out; the M.A.R.C.U.S. room is suggested from the Lens room name; on create, if tagging is allowed, M.A.R.C.U.S. adds the tag `marcus:<device id>` to the Lens device.
- **No Collector** for Lens-managed rooms: they are devices with no Collector, polled cloud-to-cloud by the cloud worker.
- **Device page** for these rooms: status pills (Online/Offline, Teams signed in/out, In a call/Idle, Teams health), cards for Teams Rooms (app version, presence), Peripherals, Device (model, software, serial, last seen by Lens) and Source (Poly Lens account, tag, "Open in Lens").
- **Alerts** like any device: offline, Teams signed out, Teams health unhealthy, peripheral missing. Readings feed dashboards, reports, room views and the Public API like any device.

## Non-goals

- Bulk "import rooms from Lens" (later).
- Commands or remote access to Lens-managed rooms (Lens actions such as reboot are a later addition).
- Windows-direct MTR monitoring (WinRM), Logitech Sync, Microsoft 365/Graph — later integrations.
- Changing the existing Collector-side Lens enrichment (Collector config `lens:`, `tags.lens_managed`) — it stays as it is for Poly devices a Collector polls.
- Room Readiness *power* steps for Lens-managed rooms (no command path).

## Anti-goals

- A customer seeing or affecting another customer's Lens data — including when Involve uses one Lens credential that covers several customers' Lens accounts.
- Any Lens secret visible after saving, in logs, audit entries, API responses or the Public API.
- Breaking any existing Collector-based behaviour while making the Collector optional.
- Writing to a customer's Lens account (tags) without the customer's explicit opt-in.

## Approaches considered

- **A. Devices that genuinely have no Collector (chosen).** `devices.collector_id` becomes optional; each device has a **source** (`collector` or an integration). Status for integration devices comes from the integration, never "Collector offline". Every code path that assumes a Collector is checked. Cleanest; scales to Logitech Sync and Microsoft 365; touches the most code.
- **B. A built-in "cloud integrations" Collector per customer.** Lens rooms hang off a synthetic, always-online Collector. Least code now, but the fake Collector shows in Collector lists and needs special-casing (updates, enrolment, logs, health alerts) that grows with each integration.
- **C. Lens rooms as a separate list, not devices.** No risk to device code, but loses alerts, dashboards, reports, room views and the Public API.

Daniel chose **A** (2026-11-23): "go with A". Polling Lens runs in the **cloud worker** in every option.

## Design

### Data model

- `devices.collector_id` → nullable. New `devices.source text NOT NULL DEFAULT 'collector'` with values `collector` | `poly_lens` (extensible), and a CHECK that `collector_id IS NOT NULL` exactly when `source = 'collector'`.
- `devices.platform` for Teams Rooms (`windows` | `android`), nullable for other types — or carried in the device's config/tags if that fits existing patterns better (builder's call, but it must be filterable and shown in the portal).
- New device type `teams_room` (portal label "Microsoft Teams Room") alongside display, projector, conferencing, audio, camera, control, avoip, power.
- New table `integrations` (per customer): id, customer_id, provider (`poly_lens`), status, encrypted credentials (client ID stored plainly; client secret encrypted with the existing secrets cipher), provider account/tenant id and name, `allow_tagging` bool, last_success_at, last_error (sanitised text), created/updated by/at. One Poly Lens integration per customer. RLS like other tenant tables.
- New table `integration_devices` (or columns on devices — builder's call): device_id → integration_id + external id (Lens device id), plus the external name, serial, MAC, model for display and matching. Unique (integration_id, external_id) so a Lens device can be linked to at most one M.A.R.C.U.S. device per customer.
- Migrations follow the existing conventions (next numbers after 0080; RLS; grants to app_admin/app_tenant; the RDS-master-doesn't-bypass-RLS note from migration 0068 if any data migration is needed).

### Lens client (cloud)

- A cloud-side Lens client (new package, e.g. `internal/integrations/polylens`), OAuth 2.0 client credentials against `https://login.lens.poly.com/oauth/token`, tokens cached for their 24-hour lifetime per integration, GraphQL at `https://api.silica-prod01.io.lens.poly.com/graphql`. The Collector's existing client (`av-bridge/internal/cloud/lens`) is a reference, not shared (different module).
- Queries scoped to the integration's chosen Lens tenant/account (`tenant(id:)` → `inventory.deviceSearch`), paged with `nextToken`, page size well under the 5,000 limit, and within the documented cost limits (10,000 points per query, 100,000 rolling per 60 s; respect the rate-limit headers and back off).
- Fields used (confirmed present in the Lens schema by introspection of Involve's tenant, 2026-11-06; several are not in Lens's public docs, so the implementation must treat missing/null values as "unknown", not "bad"): id, name, serialNumber, macAddress, hardware model/product, softwareVersion, connected, lastDetected, room { name }, site { name }, activeApplicationName/Version, callStatus (IN_CALL / NOT_IN_CALL / UNKNOWN / UNSUPPORTED), allPeripheralsLinked, teamsDeviceHealthStatus, teamsPresenceAvailability/Activity, lensTags. The device picker lists only Teams Rooms devices (determined from the active application / Teams fields — the builder confirms the reliable discriminator against a real tenant).
- Tagging: `addDeviceTag(deviceId, tag)` on link when `allow_tagging` is on; `removeDeviceTag` when the M.A.R.C.U.S. device is deleted or unlinked. A tagging failure (e.g. the credential's role can't edit devices) is shown on the integration card and the device's Source card; it never blocks the link.
- Push: Lens offers a `deviceStream` subscription (connected/software changes). Not required for the first release — polling is the baseline; the stream is a possible later optimisation (builder may note it).

### Polling and status (cloud worker)

- A worker loop (runs only where `MARCUS_ROLE` runs background loops) polls each connected integration every 60 seconds (configurable): one batched query for all linked devices of that integration, mapped into the existing telemetry/readings pipeline so readings, events, dashboards, reports and the Public API work unchanged.
- Readings (names follow existing conventions; final names the builder's call): `online`, `teams_signed_in`, `call_state` (in_call / idle / unknown — reuse the existing `call_state` reading so the Devices list call pill works), `teams_health`, `teams_presence`, `teams_app_version`, `peripherals_linked`, `software_version`, `lens_last_seen`, model/serial into device info.
- Device status: online/offline from Lens `connected` (and last-detected age); a device whose integration is failing (bad credentials, Lens down) shows **unknown** with the reason "Poly Lens unreachable", the counterpart of "Collector offline".
- Effective status (`internal/devicestatus.EffectiveStatusSQL` and its 13 call sites) handles `collector_id IS NULL`: integration devices use the integration's health instead of the Collector's.
- **Alerts (new cloud-side alert evaluation).** Today the Collector raises device alerts (`internal/ingest/alerts.go` stores what arrives); for integration devices the cloud worker must decide when to open and resolve alerts itself: track each condition's start time per device, open after the hold time, resolve automatically on recovery, store state so restarts don't re-alert, and feed the existing alerts table and notification channels. Defaults approved by Daniel (2026-11-23):

  | Alert | Opens after | Severity | Clears |
  |---|---|---|---|
  | Room offline (Lens `connected` false) | 5 min | Critical | automatically on recovery |
  | Teams signed out | 10 min | Critical | automatically |
  | Teams health unhealthy | 10 min | Warning | automatically |
  | Peripherals missing (`allPeripheralsLinked` false) | 10 min | Warning | automatically |

  One notification when an alert opens and one when it clears; no repeats while open. **Maintenance hold:** Teams Rooms update and restart overnight, so these alerts are held between 01:00 and 05:00 in the building's local time zone and open only if the condition is still present after 05:00 (rooms with no building time zone use Europe/London). Existing suppression for rooms switched off by Room Readiness also applies.
- **Derived readings (Claude's calls, to confirm against real Lens data before release):** "Teams signed in" is derived from Teams presence (Offline or PresenceUnknown = signed out) because Lens has no direct signed-in field; peripherals are one reading ("All peripherals linked" / "Peripheral missing") because Lens reports only `allPeripheralsLinked`; which `teamsDeviceHealthStatus` values count as unhealthy is set from the values observed in Involve's tenant (anything not healthy and not unknown raises the warning until refined).
- **Not stored:** the room's resource-account email (personal-data minimisation); the mockup's "Room account on hover" is dropped.

### Making the Collector optional (approach A's sweep)

Every code path that reads `devices.collector_id` is checked and handles a NULL safely: device list/detail APIs and portal pages (Collector column shows "Poly Lens" or "—"), device create/edit/move (move between Collectors not offered for integration devices), commands and `_poll` fresh readings (refuse cleanly with "not available for Lens-managed devices"; Room Readiness checks use the latest reading, power steps skip these devices), remote access (not offered), Collector deletion/offboarding cascades, Collector health and Collector logs, reports grouped by Collector, Public API device fields (collector id null + new source field, documented in the OpenAPI spec), GDPR export/erase and retention, audit entries. The builder produces the full list from the code and covers each with a test.

### Portal

- Manage → **Integrations** page, option A layout, real vendor logos (Poly, Logitech, Microsoft, ServiceNow) used under their brand guidelines as small marks on the cards; permission-gated.
- Poly Lens connect/edit/disconnect drawer as in the mockup; "Test and connect" validates credentials and lists the Lens accounts the credential can reach before saving.
- Disconnect: confirm dialog; linked devices become **unknown** with "Integration disconnected" (not deleted); reconnecting restores them. Deleting a device removes its Lens tag (if tagging on).
- New device form: device type "Microsoft Teams Room" → platform (Windows/Android) → managed by (Poly Lens when connected; others disabled with a hint) → device picker → room (suggested from Lens room name) → create. No Collector field for this path.
- Device page cards and pills as in the mockup.

### Permissions and access

- New permission `integration.manage` (connect, edit, disconnect, see integration status); admin role gets it by default; Involve users act on a customer's behalf through the existing customer scope. Viewing the Integrations page needs `view.notifications`-level access or a new `view.integrations` (builder's call; must not expose secrets either way).
- Adding a Lens-managed device needs the existing `device.crud`.
- Every change (connect, edit, disconnect, tagging on/off, link/unlink) is audit-logged without secret values.

### Multi-account credentials (Involve-managed)

- A single Lens credential may cover several Lens accounts. Each customer's integration stores **one chosen Lens account id**; every query is scoped to that account, so a shared Involve credential pasted into two customers can never surface one customer's Lens devices to the other. Test and connect shows the accounts the credential can see so the right one is chosen.
- **Who may change the account (approved by Daniel):** a customer admin with `integration.manage` may connect with their own credentials (choosing among the accounts that credential reaches at connect time). On an existing integration, **only Involve (vendor) users** can change which Lens account it uses, and changing the account always requires re-entering the client secret. Customer users never see the list of other accounts a shared Involve credential can reach after connection; the API enforces this, not just the portal.

### Disconnect, reconnect and duplicates (Claude's calls)

- **Disconnect** keeps the integration record and the device links and wipes the stored secret; the integration's status becomes "disconnected" and its devices show **unknown** ("Integration disconnected"). Reconnecting to the **same** Lens account restores them. Reconnecting to a **different** account keeps the links but devices not found in the new account show unknown ("Not found in this Lens account") until relinked or deleted.
- Deleting a device while its integration is disconnected cannot remove the Lens tag; the tag is left in place and the device page/audit entry says so (no queued removal in the first release).
- **Already monitored:** if the chosen Lens device's serial or MAC matches a device already monitored through a Collector for the same customer, the picker shows "Already monitored as <device name>" and still allows linking (some customers want both views).

### Documents

- Customer docs (working copy, not committed): new Integrations and Poly Lens pages in the Mintlify docs; Data Residency and Security & Trust list Poly Lens (HP) as a sub-processor only when a customer connects it (customer-enabled integration — Poly Lens data location as stated by HP), consistent with how customer-enabled integrations are described today.

## Constraints

- Existing behaviour for Collector-polled devices must not change; the full test suite (including DB integration tests) passes.
- Secrets: client secret encrypted at rest with the existing cipher, write-only in the API, never logged.
- Production deploys go through the pipeline (tag → UAT → Promote to production); migrations must be safe with web and worker copies starting at once (migrations already take an advisory lock).
- Semantic versioning: this is a feature release (next minor, e.g. v1.2.0).

## Left to the builder

- Exact reading names (within the existing conventions), column-vs-table choice for the Lens link and platform, poll interval default, whether to add `view.integrations`, the reliable Teams Rooms discriminator in Lens data, and whether to use `deviceStream` now or later.

## Development and testing against Lens

- Daniel approved (2026-11-23) using **Involve's own Lens account** (276 devices, 64 with Teams data, including Windows and Android MTRs) for development and verification, and writing `marcus:` test tags on a few of Involve's own Lens devices, removed afterwards. The credential is supplied by Daniel through a local environment variable or Secrets Manager — never pasted into chat, committed, or logged.
- Unit tests use recorded/fixture Lens responses; a verification step against Involve's tenant confirms the Teams Rooms discriminator, the presence-to-signed-in mapping, the health values and the tagging round-trip before release.

## Logos

- Daniel approved (2026-11-23) using each vendor's published logo/brand mark (Poly, Logitech, Microsoft, ServiceNow) as small marks on the integration cards, following the vendors' brand guidelines (no recolouring or distortion), stored as static assets in the portal.

Status: approved by Daniel 2026-11-23.
