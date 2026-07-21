# Nightly Room Lifecycle — Specification

**Prepared:** 2026-07-17
**Status:** Design locked, ready to implement in phases
**Product name (customer-facing):** Room Readiness
**Technical name (repo):** nightly-lifecycle

---

## 1. Summary

A scheduled per-room lifecycle that runs every business night: **power down at close of business, power up before the working day begins, execute a functional test to confirm the room is fully operational, and issue a report** (portal + email) with what passed and what failed.

Failed rooms are surfaced to nominated customer contacts and — in parallel — flagged to the Involve helpdesk as a proactive ticket on the customer's behalf, so we can arrange support before people arrive.

## 2. Business case

Three concrete outcomes:

- **Energy savings.** Displays / PCs / DSPs / codecs left on 24/7 versus ~10h/day is meaningful cost. Across a 50-room estate at ~300W per room average, a 14-hour daily off-cycle is roughly £3-5k/year saved per estate at current UK commercial electricity rates. Real ROI number for the sales conversation.
- **Reduced device wear.** Fewer hours-on-hardware extends useful device life — meaningful over 5-year refresh cycles.
- **Confidence at 09:00.** The report lands in an inbox at ~07:45 local. IT / AV team has time to fix anything red before people walk in. This is the operational win competitors sell hard.

## 3. Competitive positioning

- **Utelogy** ships "automated self-testing" with fixed recipes tied to their driver library.
- **Xyte** has "Advanced Workflows" but no named room-readiness product.
- **Crestron** has "Fusion Room Readiness" — mature but Crestron-only.

Our differentiation:
- **Vendor-agnostic** — works across any device our adapters support. No driver-library lock-in.
- **Customer-authorable routines** — each customer defines what "ready" means for their rooms.
- **Managed-service native** — helpdesk-queue routing is a first-class feature, not an integration afterthought.
- **UK/EU hostable** — matches broader positioning.

## 4. Locked design decisions

| Decision | Choice |
|---|---|
| Power-off strategy | Full off where supported → standby fallback. PoE-cut deferred. |
| Schedule granularity | Customer default + per-room override. Room fields nullable, inherit customer default when null. |
| Timezone | Customer-level for v1. Per-building later. |
| Days of week | Configurable (typical: Mon–Fri). |
| Calendar integration | Deferred. v1 is fixed schedule. |
| Manual override | `excluded_until` date, nullable. NULL = active; date = auto-re-enables after that day. |
| Notifications — customer | Email + Teams (existing channels). SMS deferred. |
| Notifications — helpdesk | New `helpdesk_email` field on customer record. Cc'd on every lifecycle-failure alert. |
| New alert type | `nightly_lifecycle_failed` — routes differently from ordinary telemetry alerts. |
| Digest email | HTML, sent every morning regardless of outcome. Failed rooms at top expanded; successful rooms collapsed into a summary line. Deep-links per room to portal detail. |
| Portal | `/nightly/runs` (heatmap + filters + CSV export), `/nightly/runs/[id]` (detail), `/nightly/schedule`, `/nightly/routines`. |
| Cross-tenant helpdesk view | Reuses existing vendor-tenant scope. Adds "failed only" filter. |
| Retention | Detailed step results: 90 days default, customer-configurable. Run-level summary: kept forever. |
| SIP loopback | Customer provides SIP URI. We do not host the bridge. |

## 5. Data model

New tables in the cloud DB, tenant-scoped under existing RLS (unless noted).

### 5.1 `nightly_schedule` — customer default (one row per customer)

| Column | Type | Notes |
|---|---|---|
| id | uuid PK | |
| customer_id | uuid FK | UNIQUE — one row per customer |
| power_off_time | time | e.g. `19:00` |
| power_on_time | time | e.g. `07:30` |
| days_of_week | int[] | ISO weekdays 1–7 (Mon–Sun) |
| timezone | text | IANA name, e.g. `Europe/London` |
| test_routine_id | uuid FK | → `nightly_test_routine.id` |
| helpdesk_email | text | Nullable. Vendor's helpdesk address for this customer. |
| enabled | bool | Master on/off for the customer |
| created_at / updated_at | timestamptz | |

### 5.2 `room_nightly_config` — per-room override (one row per customised room)

| Column | Type | Notes |
|---|---|---|
| id | uuid PK | |
| room_id | uuid FK | UNIQUE |
| power_off_time | time | Nullable — null = inherit |
| power_on_time | time | Nullable |
| days_of_week | int[] | Nullable |
| test_routine_id | uuid FK | Nullable |
| excluded_until | date | Nullable. Room skipped from lifecycle until this date. |
| notification_recipients | jsonb | Nullable — override the customer default. Shape: `[{"channel":"email","target":"..."}]` |
| created_at / updated_at | timestamptz | |

### 5.3 `nightly_test_routine` — reusable functional-test definition

| Column | Type | Notes |
|---|---|---|
| id | uuid PK | |
| customer_id | uuid FK | |
| name | text | e.g. "Standard room readiness — VC + audio loopback" |
| description | text | |
| steps | jsonb | Array of step objects (see §7) |
| created_at / updated_at | timestamptz | |

### 5.4 `nightly_run` — one row per scheduled execution (per room, per night)

| Column | Type | Notes |
|---|---|---|
| id | uuid PK | |
| customer_id | uuid FK | |
| room_id | uuid FK | |
| routine_id | uuid FK | Snapshot of which routine was in effect |
| phase | text | `scheduled_off` / `off` / `scheduled_on` / `waking` / `warming` / `testing` / `ready` / `failed` |
| status | text | `pending` / `in_progress` / `succeeded` / `failed` / `skipped` |
| scheduled_at | timestamptz | Local-time-resolved instant when the run should start |
| started_at | timestamptz | Actual start |
| completed_at | timestamptz | Actual end |
| failure_reason | text | Short human-readable summary if failed |
| created_at | timestamptz | |

### 5.5 `nightly_step_result` — per-step outcome within a run

| Column | Type | Notes |
|---|---|---|
| id | uuid PK | |
| run_id | uuid FK | |
| device_id | uuid FK | Nullable — some steps are room-scoped |
| step_index | int | 0-based position in routine |
| step_name | text | Snapshot from routine |
| step_type | text | See §7 |
| expected | jsonb | What the step required |
| actual | jsonb | What was observed |
| passed | bool | |
| error | text | Nullable |
| started_at / completed_at | timestamptz | For duration |

Retention: `nightly_step_result` rows older than the customer's retention window (90 days default) are deleted by the same in-process cleaner pattern as `sessioncleanup`.

### 5.6 Extensions to existing tables

- `customers`: no change (helpdesk_email lives in `nightly_schedule` since it's tied to this feature).
- `devices`: add `capabilities jsonb` column populated by adapters — declares which commands / metrics the device supports.
- `alerts`: no schema change — new alert type `nightly_lifecycle_failed` uses existing shape; metadata carries `run_id`, `failed_step_index`.

## 6. Backend architecture

### 6.1 Scheduler goroutine

Mirrors the existing `commands.Sweeper` pattern.

- Runs on every cloud instance; leader-elects if we later run multiple.
- Every 60 seconds, queries: "which rooms have a lifecycle action due in the next minute?"
- For each due action, creates a `nightly_run` row with `status=pending`, then dispatches to the runner.

### 6.2 Lifecycle runner

Orchestrates the phases per room:

```
[scheduled_off]  → send power-off commands via command queue
[off]            → devices confirmed off
[scheduled_on]   → send power-on commands via command queue
[waking]         → wait for power-on confirmation (timeout: 120s)
[warming]        → fixed wait for steady state (default: 60s)
[testing]        → execute routine (see §7)
[ready]          → success — record run, emit digest tile as green
[failed]         → any prior phase failed — emit alert, digest tile red
```

Each phase persists `nightly_run.phase` so a portal viewer can see live progress.

### 6.3 Routine executor

Iterates `steps` array, dispatches each step by `type`, records outcome to `nightly_step_result`. Honours `on_failure` policy per step.

### 6.4 Notification dispatcher

Reuses existing `notify.Dispatcher`. New alert kind `nightly_lifecycle_failed` is routed to:
- Customer's configured notification channels (email + Teams, per existing channel config).
- `helpdesk_email` on `nightly_schedule` (added as a cc destination).

### 6.5 Digest sender

Separate goroutine that fires once per customer per morning at `power_on_time + 30 minutes` (giving all rooms time to complete). Aggregates the night's runs into a single HTML email, sends to nominated recipients + cc'd helpdesk.

## 7. Test routine schema

Routines are JSON documents stored in `nightly_test_routine.steps`. Every step:

```
{
  "name": "human-readable",
  "type": "step_type_key",
  "target": { ... target selector ... },
  "parameters": { ... type-specific ... },
  "expected": { ... success criteria ... },
  "timeout_seconds": 30,
  "on_failure": "abort" | "continue" | "retry:N"
}
```

Step types (initial set):

| Type | Purpose | Parameters |
|---|---|---|
| `power_on` | Power on all devices in the target scope | `target: {"scope": "room"}` or `{"device_type": "..."}` |
| `power_off` | Power off all devices in the target scope | (same) |
| `wait` | Fixed delay | `duration_seconds` |
| `device_command` | Send an arbitrary command to a device / device group | `command`, `parameters`, `target` |
| `check_metric` | Compare a device metric against a threshold | `metric`, `operator` (gt/gte/lt/lte/eq), `threshold`, `sample_window_seconds` |
| `expect_status` | Assert a device is in a specific status (e.g. `online`) | `status`, `target` |

Target selector shapes:
- `{"scope": "room"}` — every device in the room
- `{"device_type": "vc"}` — every device of a type
- `{"device_id": "uuid"}` — one specific device
- `{"device_tag": "audio_primary"}` — devices matching a tag key

### 7.1 Canonical routine — Standard room readiness

Encodes the flow you supplied. Uses a customer-supplied SIP URI as the dial target.

```json
{
  "name": "Standard room readiness — VC + audio loopback",
  "steps": [
    {
      "name": "Power on all room devices",
      "type": "power_on",
      "target": {"scope": "room"},
      "timeout_seconds": 120,
      "on_failure": "abort"
    },
    {
      "name": "Warm-up gap",
      "type": "wait",
      "duration_seconds": 60
    },
    {
      "name": "Dial test bridge",
      "type": "device_command",
      "target": {"device_type": "vc"},
      "command": "dial",
      "parameters": {"uri": "sip:loopback@customer-provided.example"},
      "expected": {"call_state": "connected"},
      "timeout_seconds": 30,
      "on_failure": "abort"
    },
    {
      "name": "Let audio play",
      "type": "wait",
      "duration_seconds": 10
    },
    {
      "name": "Check microphones hear audio",
      "type": "check_metric",
      "target": {"device_type": "dsp"},
      "metric": "input_level_dbfs",
      "operator": "gt",
      "threshold": -40,
      "sample_window_seconds": 5,
      "on_failure": "continue"
    },
    {
      "name": "Hang up",
      "type": "device_command",
      "target": {"device_type": "vc"},
      "command": "disconnect",
      "on_failure": "continue"
    },
    {
      "name": "Reset DSP to default preset",
      "type": "device_command",
      "target": {"device_type": "dsp"},
      "command": "recall_preset",
      "parameters": {"preset": "default"}
    }
  ]
}
```

**Note on `on_failure`:** the mic check and the hang-up both use `continue` (not `abort`) — critical, because if the mic check fails we still need to tear down the call and reset the DSP so the room isn't stuck in a call all morning.

## 8. Adapter capabilities

Each adapter declares its capabilities statically. The declaration is included in the telemetry payload the bridge sends, and ingest writes it to `devices.capabilities`.

Shape:

```json
{
  "power_off": {"supported": true, "method": "command"},
  "power_on":  {"supported": true, "method": "command"},
  "commands": ["dial", "disconnect", "recall_preset", "reboot"],
  "metrics":  ["input_level_dbfs", "cpu_pct", "temperature_c"]
}
```

The portal routine editor reads `capabilities` per device to offer valid commands / metrics only — no way to author a routine that references a command a device doesn't support.

### 8.1 Adapters that need extending

| Adapter | Current | Needed for the canonical routine |
|---|---|---|
| Poly VideoOS | Status read only | Add `dial` + `disconnect` write commands |
| Biamp Tesira | Status + metric polling | Add live meter reads (`input_level_dbfs`) |
| Displays (Sony BRAVIA etc.) | Power state read | Add power-on / power-off write commands where supported |

## 9. Portal UI

### 9.1 `/nightly/runs` — main runs view

- Heatmap or table: rows = rooms, columns = last N nights, cells = status colour.
- Filters: date range, building, rooms, status (all / failed only / excluded).
- Summary header: `47 / 50 rooms ready · 3 failed · 0 excluded · Last run 07:32`.
- CSV export button (last N days).
- Vendor cross-tenant view: adds a customer filter, otherwise identical.

### 9.2 `/nightly/runs/[id]` — one-run detail

- Room name + breadcrumb (region · location · building).
- Phase timeline visualisation: `scheduled_off → off → scheduled_on → waking → warming → testing → ready` with time-per-phase.
- Step-by-step table: index, name, type, expected, actual, passed, duration, error.
- If failed: recommended-action panel (heuristic based on step type + error).

### 9.3 `/nightly/schedule` — schedule editor

- Customer default at top: time pickers, days-of-week, timezone, helpdesk email, enabled toggle, routine selector.
- Room override list below: table of rooms showing effective schedule (inherited or customised), "customise" per row opens override editor.
- Exclusion widget per room: date picker for `excluded_until`.

### 9.4 `/nightly/routines` — routine author

- List of customer routines.
- Editor: step-by-step builder. Each step type has its own form. Target selector shows only valid options based on adapter capabilities.
- Preview: renders the routine as a numbered list matching what the digest will show.

## 10. Notification model

### 10.1 Digest email

- Sent once per customer per morning at `power_on_time + 30min` (all rooms have completed by then).
- HTML content, structured:
  - **Header**: date, customer name, estate summary (`47 / 50 ready · 3 failed`).
  - **Failed rooms section** (expanded, at the top): each room with room name, phase reached, per-step results, expected vs actual for the failed step, recommended action, direct link to `/nightly/runs/[id]`.
  - **Successful rooms section** (collapsed): one-line summary `42 rooms passed` — expandable list.
  - **Excluded rooms section**: list of rooms skipped due to `excluded_until` or master disable.
- Recipients: customer's nominated addresses (default: all users with `alerts.receive` permission).
- Cc: `helpdesk_email` if set on the schedule.

### 10.2 Per-failure alert

- Fires immediately when a run enters `failed` phase.
- Type: `nightly_lifecycle_failed`.
- Routes through existing `notify.Dispatcher` channels (email / Teams / webhook) plus helpdesk email.
- Metadata: `run_id`, `room_id`, `failed_step_index`, `failure_reason`.

### 10.3 Helpdesk queue view

- Existing vendor-tenant cross-tenant view gets a new filter: `alert.type = nightly_lifecycle_failed`.
- Sorted by time desc, grouped by customer.
- Direct link to run detail from each entry.

## 11. Reports

- **Morning digest email** — see §10.1.
- **In-portal runs view** — see §9.1.
- **CSV export** — last N days of runs, one row per `nightly_step_result` so downstream tools can pivot.
- **Future**: PDF attachment for the digest, weekly executive rollup, per-device reliability trend — not in v1.

## 12. Retention

- `nightly_step_result` rows older than customer's retention (90 days default) deleted by an in-process cleaner (mirrors `sessioncleanup`).
- `nightly_run` rows kept forever (small rows, useful for long-term trends and compliance).
- Retention configurable per customer via `nightly_schedule.retention_days` (add this column) — floor of 30 days to keep enough context for reasonable debugging.

## 13. Phasing

### Phase A — Scheduling & lifecycle skeleton

Everything except the functional test itself. Ship this first — already a substantial product.

- Data model: all tables except `nightly_test_routine` and `nightly_step_result` (or with `test_routine_id` nullable and step-results empty).
- Scheduler goroutine + lifecycle runner up to `[ready]` phase.
- Routines limited to `power_on` + `wait` step types initially.
- Adapter capability declaration in ingest payload + `devices.capabilities` column.
- Adapter extensions: power-on / power-off write commands where the device supports it (displays, PCs via WoL later, codecs).
- Portal: `/nightly/runs`, `/nightly/runs/[id]` (limited step display), `/nightly/schedule`.
- Digest email + helpdesk email routing.
- Retention cleaner.

**Deliverable:** "we schedule your rooms on/off, tell you if power came up, and email you a morning digest." Substantial enough to demo and sell on the energy-savings + reliability story alone.

### Phase B — Full functional test

Adds the functional-test layer that makes the readiness check meaningful.

- Routine step types: `device_command`, `check_metric`, `expect_status`.
- Routine editor in the portal (`/nightly/routines`).
- Adapter extensions: Poly VideoOS `dial` / `disconnect`, Biamp Tesira meter reads.
- Recommended-action heuristics on the run detail view.
- CSV export.

**Deliverable:** "we prove your rooms are actually ready, not just powered on." This is the competitive differentiator.

### Phase C — Future / roadmap

- SMS notifications
- Calendar integration for out-of-hours occupancy
- PoE-cut power option (needs a switch adapter)
- PDF digest attachments
- Weekly / monthly executive rollup
- Per-device reliability trend view
- Per-building timezones
- Routine templates / marketplace

## 14. Open items

- **Room device inventory freshness.** The scheduler needs to know which devices are in a room at the time the schedule runs. If a device is added/removed between schedule creation and execution, the run reflects the room-as-configured at execution time — not at schedule-creation time.
- **Concurrent runs.** If a room's power-on happens across day boundaries (e.g. 23:55 off / 00:05 on for an all-night room), that's not a realistic case for v1 — the schedule assumes off < on within the same 24h window. Documented as a v1 constraint.
- **What if the room is in use during the off time?** v1 answer: it powers off anyway. If a customer flags this as a problem, calendar integration in Phase C resolves it.
- **How does a customer test their routine without waiting 24h?** Portal needs a "run now" button on the schedule / routine pages that dispatches an ad-hoc run against a chosen room. Not blocking for Phase A but worth adding early in Phase B.

---

*This spec is the reference for building Room Readiness. Keep it in sync with implementation — the pattern SPEC.md fell into (drift from code) is not something we want to repeat here.*
