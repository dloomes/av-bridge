# Firmware release notes check: latest version and vulnerabilities

**Date:** 2026-11-05
**Status:** Approved in conversation; awaiting written-spec review

## Purpose

Admins need to know which devices run firmware with known security vulnerabilities, and what the latest vendor release is, without reading every vendor's release notes by hand. They also need a report to hand to a customer or security team.

Each firmware group (`firmware_targets` row: customer, make, model, `target_version`, `docs_url`, notes) gets a **Check release notes** button. The button reads the group's release notes page and does three things:

- finds the latest release;
- lists the security vulnerabilities the page names;
- adds possible matches from the US National Vulnerability Database (NVD).

Devices running firmware affected by a confirmed vulnerability are flagged, and there is a CSV and PDF vulnerability report.

### Decisions taken with the user

- **Response:** visibility plus a report. No alerts or notifications.
- **Manual button only,** with a "Last checked" date and a "Check again" nudge after 30 days. No schedule.
- **Reading pages:** an AI (Claude) extracts structured data from the fetched page. NVD supplies severity.
- **NVD keyword search:** also runs. Its results are *possible* matches that need an admin to confirm them.
- **Data sent to Anthropic:** only the fixed instruction, the group's make and model, and the release notes text. This is enforced in code and by a test.

## Current state

- `firmware_targets` (migration 0014) holds `target_version`, `docs_url`, `notes`, `updated_at` and `updated_by`, unique per `(customer_id, make, model)`, with FORCE RLS and `app_tenant`-only grants. Migration 0074 added SELECT and UPDATE for `app_admin`.
- `GET` firmware summary (`internal/portalapi/firmware.go`) joins devices to targets by effective make and model (asset manufacturer/model, then device make/model, then tags). It returns each device's `firmware_version`, `target_version` and `docs_url`. The portal Firmware page (`av-bridge-portal/src/app/firmware/page.tsx`) marks devices outdated or current against the target.
- Permissions: `view.firmware` (read) and `firmware_target.crud` (edit groups).
- Nothing reads `docs_url`; it is only a link.
- The next migration number is **0078**.

## Design

### 1. The check (server)

`POST /api/v1/firmware/targets/{id}/check` needs `firmware_target.crud` and returns the check result. It runs synchronously, within about 60 seconds. The steps:

1. **Load the group.** The handler checks the customer scope and requires `docs_url` to be set.
2. **Rate limit.**
   - Each customer can check each group at most once a minute (429 *"Checked moments ago. Try again in a minute."*).
   - A check already running for the same group returns 409 *"A check is already running."*
3. **Fetch** the page through the URL guard (§3). It must be HTML, plain text or PDF.
   - **HTML** is converted to text: script, style and navigation are dropped, and the heading structure is kept.
   - **PDF** text is extracted with a pure-Go PDF text library.
   - **Unreadable pages:** if the extracted text is under 500 characters, or has no version-like token, the check stops with status `unreadable`. The message is *"This page can't be read automatically. Link to the plain release notes or a PDF instead."*
4. **Extract with Claude** (§4). Claude returns the latest version and release date, plus a list of security items.
5. **Validate.**
   - Every item's `quote` must appear in the fetched text, compared after normalising whitespace and case.
   - CVE IDs must match `^CVE-\d{4}-\d{4,}$`.
   - Items that fail are dropped and counted as `ignored`.
6. **NVD enrichment.** For each CVE from the release notes, `GET /rest/json/cves/2.0?cveId=…` supplies:
   - severity, using CVSS v3.1 base severity, then v3.0, then v2;
   - the score;
   - the published date;
   - the NVD description.
7. **NVD keyword search.** `GET /rest/json/cves/2.0?keywordSearch=<make> <model>&keywordExactMatch` returns up to 50 most recent results. Those not already found in the release notes become `possible` items. Their affected and fixed versions come from NVD configuration data when NVD gives a version range. Otherwise they stay blank.
8. **Store** the check and upsert vulnerabilities (§2), all in one transaction.
9. **Audit** the check as `firmware.check`, with metadata: group id, status, latest version, and counts of confirmed, possible and ignored items.

**Shared cache.** Release notes are public. Steps 3–5 are cached by normalised URL plus make and model for 24 hours in `firmware_page_cache` (§2). That table is not customer-scoped and holds only public-page-derived data. NVD responses are cached by CVE ID for 24 hours in the same way. Each customer's confirm and dismiss states are never shared.

**Partial failure.**
- If NVD fails or is rate-limited, the release-notes results are still stored without severity, with the message *"NVD lookup failed, retry later"*.
- If Claude fails, the check is stored as `failed` with *"The reading service is unavailable. Try again later."*, and previous results are kept.

**Config.**
- `ANTHROPIC_API_KEY` is required for the feature. Without it, the endpoint returns 503 *"Release notes checks aren't set up."* and the portal hides the button and shows that text.
- `NVD_API_KEY` is optional. It raises NVD rate limits.
- Both are added to `internal/config`, and `ANTHROPIC_API_KEY` goes in as a Secrets Manager secret through terraform. That needs the user's OK before apply.

### 2. Data model (migration 0078)

**`firmware_checks`.** Customer-scoped with FORCE RLS, using the same policy pattern as `firmware_targets`. Grants: `app_tenant` gets SELECT and INSERT; `app_admin` gets SELECT and DELETE.

| column | type | notes |
|---|---|---|
| `id` | uuid pk | |
| `customer_id` | uuid not null → customers ON DELETE CASCADE | |
| `target_id` | uuid not null → firmware_targets ON DELETE CASCADE | |
| `checked_at` | timestamptz not null default now() | |
| `checked_by` | text not null | `user:<uuid>` |
| `source_url` | text not null | |
| `status` | text not null | `ok`, `unreadable` or `failed` |
| `latest_version` | text | |
| `latest_released` | date | |
| `message` | text | plain-words note or error |
| `ignored_count` | int not null default 0 | |
| `nvd_ok` | boolean not null default true | |

Index: `(target_id, checked_at desc)`.

**`firmware_vulnerabilities`.** Customer-scoped with FORCE RLS. Grants: `app_tenant` gets SELECT, INSERT and UPDATE; `app_admin` gets SELECT, UPDATE and DELETE.

| column | type | notes |
|---|---|---|
| `id` | uuid pk | |
| `customer_id` | uuid not null → customers ON DELETE CASCADE | |
| `target_id` | uuid not null → firmware_targets ON DELETE CASCADE | |
| `ident` | text not null | CVE ID, or a vendor advisory ID |
| `title` | text | |
| `severity` | text | `critical`, `high`, `medium`, `low` or null |
| `cvss` | numeric(3,1) | |
| `affected` | text | version range as written |
| `fixed_in` | text | |
| `source` | text not null | `release_notes` or `nvd_search` |
| `quote` | text | release-notes items only |
| `state` | text not null | `confirmed`, `possible` or `dismissed` |
| `state_by` | text | `user:<uuid>`; null if set by the system |
| `state_at` | timestamptz | |
| `first_seen` | timestamptz not null default now() | |
| `last_seen` | timestamptz not null default now() | |

Unique on `(target_id, ident)`.

Upsert rules:
- New `release_notes` items start `confirmed`. New `nvd_search` items start `possible`.
- On re-check, an existing row keeps its `state` if an admin set it (`state_by` not null). The other columns and `last_seen` are refreshed.
- An NVD-search item later found in the release notes moves to `source = release_notes`. If no admin has set its state, it moves to `confirmed`.
- Items not seen in a later check stay as they are; history is not deleted.

**`firmware_page_cache`.** Not customer-scoped, with no RLS, because it holds only data derived from public pages. Grants: `app_admin` gets SELECT, INSERT, UPDATE and DELETE.

| column | type |
|---|---|
| `cache_key` | text pk |
| `fetched_at` | timestamptz not null |
| `payload` | jsonb not null |

The cache key is a sha256 of the normalised URL plus the lower-cased make and model, or of `nvd:<CVE>`. The retention pass deletes rows older than 7 days.

**Register.** The guard test (`internal/privacy/register_integration_test.go`) will flag `checked_by` and `state_by`, because they match `_by$`. Both hold text of the form `user:<uuid>`, not a bare uuid, so neither `KeyUserID` nor `KeyEmail` fits. Add a new key kind, `KeyActorID`, meaning the column equals `'user:' || users.id`.
- **Collect** matches it with `col = ANY('user:' || ids)`.
- **Erase** applies `ActReplace` and writes the stand-in.

Register both columns as `Scoped`, `ActReplace`, `KeyActorID`, and add Collect and Erase tests.

**Offboarding and deletion.** The new customer-scoped tables cascade from `customers`, so day-30 deletion and the customer export pick them up automatically through `customer_id` discovery.

### 3. URL guard (`internal/firmwarecheck/fetch.go`)

- **Scheme:** only `https`.
- **Credentials:** refuse URLs that contain userinfo, or a query parameter whose name (case-insensitive) is one of `sig`, `signature`, `token`, `access_token`, `key`, `apikey`, `api_key`, `code`, `auth`, `password`, `x-amz-signature` or `x-goog-signature`. The message is *"This link looks like it includes a password or token. Use the vendor's public release notes link."*
- **Address checks:**
  - Resolve the host and refuse loopback, private (RFC 1918 and RFC 4193), link-local (including `169.254.169.254`), CGNAT, multicast, unspecified and IPv4-mapped forms of these.
  - Connect only to the checked IP, with a custom dialer, so a DNS answer that changes between the check and the connection can't get around the guard.
- **Redirects:** at most 3, each re-checked by the same rules.
- **Limits:** a 15-second total timeout and a 10 MB body cap.
- **Content types:** accept `text/html`, `text/plain` and `application/pdf`.
- **User agent:** `MARCUS-firmware-check/1.0`.

The same guard is used when the `docs_url` is saved, so admins get the credential message straight away.

### 4. Claude request (`internal/firmwarecheck/extract.go`)

- **Single builder.** `buildExtractRequest(make, model, pageText string)` is the only function that builds the request, and those three strings are its only inputs.
- **Payload.** A test asserts that the serialised request body contains exactly:
  - the fixed instruction;
  - make and model;
  - the page text wrapped in `<release_notes>` tags, which the instruction says to treat as data, not instructions;
  - nothing else.
- **Model:** `claude-sonnet-5-5`. One Messages API call through the official Go SDK, with `temperature` 0 and a JSON-schema-constrained output (tool use with a single tool, `report_release_notes`).
- **Output schema:**

  ```
  {
    latest_version?, latest_released? (YYYY-MM-DD),
    items: [{ident, title, affected?, fixed_in?, quote}]
  }
  ```

  `quote` must be at most 300 characters.
- **Trimming.** Page text is capped at about 150k characters. If a page is longer, keep the start, which is usually the newest release, and the sections that mention "security", "CVE" or "vulnerab".
- **Instruction text.**
  - Report only what the page states for this make and model.
  - Give the shortest exact quote supporting each item.
  - Never infer CVEs.
  - Leave fields empty when the page doesn't say.

### 5. Version comparison (`internal/firmwarecheck/version.go`)

`Compare(a, b string) (int, bool)` returns the ordering and whether the comparison is confident. It works like this:

1. Lower-case both strings and strip known prefixes and suffixes: `roomos`, `ce`, `tc`, `v`, `version`, `firmware`, `build …`, commit hashes, and anything in brackets.
2. Take the first dotted numeric run, such as `11.14.2.3`, and compare it field by field. Missing fields count as 0.
3. Return *not confident* when either string has no numeric run, or the runs differ in field count by more than 2. `affected` ranges are parsed for `<`, `<=`, `before`, `prior to`, `through`, and `x to y`; anything else is not confident.

Device status for a group:

- **vulnerable:** any `confirmed` item where the device firmware is below `fixed_in`, or inside `affected`, with a confident comparison.
- **check manually:** a `confirmed` item exists, but no comparison was confident.
- **outdated / current:** unchanged from today.

`possible` and `dismissed` items never mark a device.

Tests use real firmware strings from our adapters: Cisco RoomOS and TC, Poly, Sony Bravia, Biamp Tesira, ATEN, NETGEAR, Philips, Crestron and Aurora.

### 6. API additions

- **`GET` firmware summary.** Each device gains `vuln_status`, which is `vulnerable`, `check` or empty, and `vulns`, a list of `{ident, severity, fixed_in}`.
- **`GET /api/v1/firmware/targets`.** Each group gains `last_check` (`checked_at`, `checked_by_name`, `status`, `latest_version`, `latest_released`, `message`) and `vulnerabilities` (the full rows).
- **`POST /api/v1/firmware/targets/{id}/use-latest`** (`firmware_target.crud`) sets `target_version` to the latest found version and audits it as `firmware_target.update`.
- **`PATCH /api/v1/firmware/vulnerabilities/{id}`** (`firmware_target.crud`) takes `{state: "confirmed"|"dismissed"|"possible"}` and is audited as `firmware.vulnerability_state`.
- **`GET /api/v1/firmware/vulnerability-report.csv`** (`view.firmware`) returns one row per vulnerable or check-manually device per confirmed item. Columns: device, location, make, model, current firmware, CVE/advisory, severity, CVSS, fixed in, source, confirmed by, group last checked. CSV cells are formula-escaped.

### 7. Portal (`app/firmware/page.tsx`, device page Firmware card)

- **Per group:**
  - **Check release notes** button: disabled while a check runs, with a spinner, and hidden with a note if the feature isn't set up.
  - "Last checked 5 Nov 2026 by A. Admin". After 30 days, an amber "Check again".
  - **Latest release:** `11.20.1.4 (released 12 Oct 2026)` with a **Use as target** button that asks for confirmation.
  - The status message when a check was unreadable or failed.
- **Security list:** ident (linking to `https://nvd.nist.gov/vuln/detail/<CVE>` for CVEs), severity as colour plus text, fixed in, a source badge (*Release notes* or *Possible, from NVD*), the quote on hover or tap, and Confirm or Dismiss buttons. Dismissed items fold into a "Dismissed (n)" toggle.
- **Device badges:** a **Vulnerable** badge, red with text, and a **Check manually** badge, amber. A **Vulnerable only** filter.
- **Report:** a **Vulnerability report** button with CSV (server endpoint) and PDF options. The PDF is built with jsPDF like the run report. It has severity counts, the table, the check date for each group, and the footer *"Findings come from vendor release notes and the US National Vulnerability Database. Verify before acting."*
- **Data note** next to the button: *"Only the make, model and the release notes page are sent to Anthropic for reading."*
- **Device page:** the Firmware card shows *"Vulnerable: CVE-2026-1234 (High), fixed in 11.20"*.
- Everything works at phone width, in plain words.

### 8. Errors and messages

| Situation | HTTP | Message |
|---|---|---|
| No docs URL | 400 | "Add a release notes link to this group first." |
| URL refused by guard | 400 | specific guard message (§3) |
| Page fetch failed or timed out | 200, status `failed` | "Couldn't reach the release notes page." |
| Unreadable page | 200, status `unreadable` | §1 step 3 text |
| AI unavailable or bad output | 200, status `failed` | "The reading service is unavailable. Try again later." |
| NVD failed | 200, status `ok`, `nvd_ok=false` | "NVD lookup failed, retry later." |
| Rate limited | 429 | §1 step 2 |
| Already running | 409 | §1 step 2 |
| Feature not configured | 503 | §1 config |

### 9. Testing

- **Unit:**
  - `version.Compare`, using a real-string table, range parsing and not-confident cases;
  - the URL guard: schemes, credentials, private, link-local and metadata IPs, IPv6 and mapped forms, redirects into private space, DNS changing between lookup and connect (via the custom dialer);
  - HTML and PDF text extraction, using fixtures;
  - quote validation and the CVE pattern;
  - the request builder's exact payload;
  - cache key normalisation.
- **Integration** (`TEST_DATABASE_URL`, with a fake Claude and a fake NVD served by `httptest`, run as `app_admin` and `app_tenant`):
  - check → stored results → device flagged vulnerable;
  - confirm and dismiss survive a re-check;
  - an NVD item promoted when it is later found in the notes;
  - NVD failure keeps the notes results;
  - unreadable page;
  - rate limit and running guard;
  - customer isolation (customer B's states are untouched by A's check; B can't see A's checks);
  - CSV report content and formula escaping;
  - the privacy register guard still passes;
  - erase replaces `checked_by` and `state_by`.
- No live Claude or NVD calls in tests.

### 10. Rollout

1. Cloud release with migration 0078 and the `firmwarecheck` package. Without `ANTHROPIC_API_KEY` the feature is off but harmless.
2. Push the portal.
3. Add `ANTHROPIC_API_KEY` (and optionally `NVD_API_KEY`) to Secrets Manager and the task definition through terraform. **Apply only with the user's OK.**
4. Update the Mintlify page `docs/product/mintlify/static/portal/firmware.mdx`, or create it if it's missing, in the working copy only, never committed.

## Out of scope

- Alerts and notifications.
- Scheduled checks.
- Public API fields.
- A dashboard widget.
- Firmware push (FR12).
- Vendor-specific parsers.
- Vendor advisory feeds other than NVD, such as Cisco openVuln.

All of these can build on the same tables later.
