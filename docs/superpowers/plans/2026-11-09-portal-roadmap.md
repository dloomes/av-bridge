# Portal Roadmap Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show the product roadmap in two places. Signed-in customers see it at `/roadmap`. Prospects see it through revocable personal share links. Named Involve "roadmap editors" maintain it from Helpdesk.

**Architecture:**
- A new `internal/roadmap` package owns the global roadmap tables (migration 0078), the content model and the share-link tokens.
- Handlers live in `internal/portalapi`: the signed-in read and the editor APIs.
- The public share read lives in `internal/publicapi`, rate-limited.
- The portal gets a shared `RoadmapView`, used by `/roadmap` and by the unauthenticated `/roadmap/s/[token]` page. It also gets a Helpdesk editor page.

**Tech Stack:** Go 1.22, pgx v5, Postgres 16; Next.js 15 / React 19, Tailwind, shadcn/ui.

**Spec:** `docs/superpowers/specs/2026-11-09-portal-roadmap-design.md`

## Global Constraints

- **Migration:** number **0078**.
- **Global tables:** no `customer_id` and no RLS. `app_admin` gets full DML; `app_tenant` gets SELECT on content tables only, never on `roadmap_share_links`.
- **Status values:** `committed`, `planned`, `exploring`.
- **Limits:** item title 1–80 chars; description 1–240 chars; period positions 0–3; link name 1–80 chars.
- **Share tokens:** `rmp_` + base64url(32 random bytes). Store only the SHA-256 hex. Default expiry 90 days, maximum 365 days. URL: `{portal base}/roadmap/s/{token}`.
- **Public endpoint:** `GET /public/roadmap/{token}`. Returns 404 with `"This link isn't valid any more."` for an unknown, expired or revoked token alike. Sets `Cache-Control: no-store` and `X-Robots-Tag: noindex, nofollow`. Rate-limited to 30 requests per minute per IP.
- **Editor-only APIs** return 403 `"Only roadmap editors can change the roadmap."` to anyone else.
- **Editors:** an editor is an Involve (vendor) user with `users.roadmap_editor = true`. Only Involve admins can set the flag, through the helpdesk Users API.
- **Logging:** editor and link actions are logged with slog as `roadmap.*`, actor `user:<uuid>`, never an email address. Nothing goes to `audit_log`, because it requires a customer.
- **Retention:** share links are deleted 180 days after they expire or are revoked.
- **Rendering:** all text is rendered as plain text, never as HTML.
- **Docs:** files under `docs/product/**` are edited in the working copy only and are never committed.
- **Testing:** integration tests need `TEST_DATABASE_URL`, connect as `app_admin`/`app_tenant` like the existing ones, and seed their own data. Port 55440 may be reserved by Windows; use another port if needed.

## Review Focus

1. **A revoked or expired link is opened again,** perhaps from a cached tab. It must return the same 404 as an unknown token, and must not bump the use counts. Test: Task 4.
2. **A non-editor Involve admin** (a roadmap editor is not the same as an Involve admin) must be refused edits, but can still turn the switch on for others, and for themselves. Test: Task 3.
3. **A customer user calls the editor APIs directly,** including with `X-Customer-Scope` tricks. They get 403. Test: Task 3.
4. **Deleting a theme that still has items** returns 409 and changes nothing. Reordering with an incomplete id list is rejected with 400. Test: Task 3.
5. **Text containing `<script>`** is stored as-is and shown as text on the portal and share pages, never run. Test: Task 5 renders it as text, checked in the build and by visual check. Task 3 checks it round-trips unchanged.

---

### Task 1: Schema, seed, register and retention

**Files:**
- Create: `av-bridge-cloud/internal/db/migrations/0078_roadmap.sql`
- Modify: `av-bridge-cloud/internal/privacy/register.go` (`roadmap_*` `_by` columns, `roadmap_share_links.name`)
- Modify: `av-bridge-cloud/internal/retention/retention.go` (`Rules`)
- Test: `av-bridge-cloud/internal/roadmap/migration_integration_test.go`, plus the existing register guard and retention tests

**Interfaces:**
- Produces: these tables, with the columns given in the spec's Data section:
  - `roadmap_settings` (single row, `id boolean PK CHECK(id)`)
  - `roadmap_periods`
  - `roadmap_themes`
  - `roadmap_items` (`theme_id` FK ON DELETE RESTRICT)
  - `roadmap_shipped`
  - `roadmap_share_links`
  - and the column `users.roadmap_editor boolean NOT NULL DEFAULT false`.

- [ ] **Step 1: Write `TestRoadmapSeed`.** After migrating, assert:
  - 1 settings row with `as_of = '2026-11-02'`;
  - 4 periods, at positions 0–3, where position 0 is `Now` / `Nov 2026 – Jan 2027` with `is_current`;
  - 4 themes, in the order Faster onboarding, Device coverage, Room operations, Integrations;
  - 17 items, including `Firmware vulnerability check`, which has status `exploring` and period 3 under Room operations;
  - 7 shipped rows;
  - `app_tenant` can SELECT `roadmap_items`, but gets "permission denied" on `roadmap_share_links`.
- [ ] **Step 2: Extend `TestRetentionRules`.** A share link revoked 181 days ago is deleted. One that expired 179 days ago stays.
- [ ] **Step 3: Run the tests.** They should FAIL.
- [ ] **Step 4: Write `0078_roadmap.sql`.** Seed it with the content of the `QUARTERS`, `LANES` and `ITEMS` arrays and the "Recently shipped" list in the current roadmap page. The controller hands over that file's path. Use the page's header text for the headline, lede and disclaimer, and map `s` c/p/e to committed/planned/exploring. Add the retention rule `"roadmap share links (180 days after expiry or revocation)"`. Add the `_by` columns and `roadmap_share_links.name` to the privacy register, following existing entries. `name` holds a company or person name the editor chooses, so classify it as ActReport or NotPersonal with a reason. Decide which, and say why in your report.
- [ ] **Step 5: Run the tests,** including `go test ./internal/privacy/ -run TestRegister`. They should PASS.
- [ ] **Step 6: Commit:** "Roadmap tables, seed and retention".

### Task 2: Content model, signed-in read, editor flag

**Files:**
- Create: `av-bridge-cloud/internal/roadmap/roadmap.go`
- Create: `av-bridge-cloud/internal/portalapi/roadmap.go`
- Modify: `internal/portalauth/middleware.go` (add `Principal.RoadmapEditor bool`) and `internal/portalauth/local.go` (select `u.roadmap_editor`)
- Modify: `internal/portalapi/helpdesk_users.go` (list returns and update accepts `roadmap_editor`)
- Modify: `internal/api/server.go` (routes)
- Test: `internal/portalapi/roadmap_integration_test.go`

**Interfaces:**
- Produces:
```go
package roadmap
type Settings struct{ Title, Headline, Lede, Disclaimer string; AsOf string /*YYYY-MM-DD*/; UpdatedAt time.Time }
type Period struct{ ID string; Position int; Name, Months string; IsCurrent bool }
type Theme struct{ ID string; Position int; Name, Note string }
type Item struct{ ID, ThemeID string; Period int; Status, ReleaseMonth, Title, Description string; Position int }
type Shipped struct{ ID, When, Title string; Position int }
type Roadmap struct{ Settings Settings; Periods []Period; Themes []Theme; Items []Item; Shipped []Shipped }
func Load(ctx context.Context, q pgx.Tx) (Roadmap, error) // ordered by position
func CanEdit(p portalauth.Principal) bool                   // p.IsVendor && p.RoadmapEditor
```
- **JSON:** snake_case tags. `release_month` is omitted when empty.
- **Route:** `GET /api/v1/roadmap`. Any authenticated principal except a display link gets `200 {settings, periods, themes, items, shipped}`. Display links can be detected from how the `dsp_` resolver marks principals.

- [ ] **Step 1: Write `TestRoadmapRead`.**
  - A customer viewer gets 200, with 17 items.
  - An Involve operator gets 200.
  - A display-link token gets 403.
  - With no auth, the request gets 401.
- [ ] **Step 2: Write `TestHelpdeskRoadmapEditorFlag`.**
  - An Involve admin sets `roadmap_editor: true` on another Involve user. The list shows it, and that user's next resolved principal has `RoadmapEditor` true.
  - An Involve operator trying to set it gets 403 through the existing vendor-admin guard.
  - Setting it on a customer user gets 400 `"Only Involve users can be roadmap editors."`
- [ ] **Step 3: Run the tests.** They should FAIL.
- [ ] **Step 4: Implement** `Load`, `CanEdit`, the read handler `(h *Handler) Roadmap`, the principal flag and the helpdesk users changes.
- [ ] **Step 5: Run the tests.** They should PASS. Also run `go test ./internal/portalauth/`.
- [ ] **Step 6: Commit:** "Roadmap read API and editor flag".

### Task 3: Editor APIs

**Files:**
- Modify: `av-bridge-cloud/internal/roadmap/roadmap.go` (write functions)
- Modify: `av-bridge-cloud/internal/portalapi/roadmap.go` (handlers)
- Modify: `internal/api/server.go`
- Test: `internal/portalapi/roadmap_integration_test.go`

**Interfaces:**
- Consumes: Task 2.
- Produces:
  - **Routes**, all under `/api/v1/helpdesk/roadmap`, behind the vendor guard plus `CanEdit`:
    - `PUT /settings`
    - `POST /items`, `PATCH /items/{id}`, `DELETE /items/{id}`
    - `PUT /items/order` with body `{"theme_id":…,"period":n,"ids":[…]}`
    - `PUT /periods` (exactly 4)
    - `PUT /themes` (a full list; `id` empty means new)
    - `PUT /shipped` (a full list)
  - **Responses:**
    - Each write returns the full roadmap, as in the read API.
    - Validation errors return 400 with a plain message.
    - Deleting a theme that still has items returns 409 `"Move or delete this theme's items first."`
  - **Logging:** each write logs at slog Info as `roadmap.<action>`, with actor `user:<uuid>`.

- [ ] **Step 1: Write `TestRoadmapEditing`.**
  - An Involve admin without the flag gets 403 with the exact message.
  - After the flag is set, that user can create an item. The title `<script>alert(1)</script> Display wall` round-trips unchanged.
  - An empty title, a 241-character description, a status of `maybe` or a period of 4 each get 400.
  - PATCH moves an item's period. DELETE removes it.
  - `PUT /items/order` with an id missing from that theme and period gets 400. With the full set, it reorders.
  - `PUT /periods` with 3 entries gets 400.
  - `PUT /themes` that leaves out a theme still holding items gets 409, and nothing changes.
  - A customer admin calling any of these routes gets 403, including with `X-Customer-Scope`.
- [ ] **Step 2: Run it.** It should FAIL.
- [ ] **Step 3: Implement.** Each write runs in one transaction. Positions are rewritten as 0..n-1.
- [ ] **Step 4: Run it.** It should PASS.
- [ ] **Step 5: Commit:** "Roadmap editor APIs".

### Task 4: Share links and the public read

**Files:**
- Create: `av-bridge-cloud/internal/roadmap/links.go`
- Create: `av-bridge-cloud/internal/publicapi/roadmap.go`
- Create: `av-bridge-cloud/internal/publicapi/ratelimit.go` (small per-IP fixed-window limiter; check first that no existing limiter helper exists)
- Modify: `internal/portalapi/roadmap.go`, `internal/api/server.go`
- Test: `internal/roadmap/links_test.go` (unit), `internal/publicapi/roadmap_integration_test.go`

**Interfaces:**
- Consumes: Task 2 (`Load`).
- Produces:
```go
func NewToken() (token, hash string)                 // "rmp_"+base64url(32B), sha256 hex
func HashToken(token string) string
type Link struct{ ID, Name string; ExpiresAt time.Time; CreatedBy string; CreatedAt time.Time; LastUsedAt *time.Time; UseCount int; RevokedAt *time.Time }
func OpenLink(ctx context.Context, q pgx.Tx, token string) (Link, bool, error) // valid only if not revoked and expires_at > now(); bumps last_used_at/use_count when valid
```
- **Editor routes:**
  - `GET /api/v1/helpdesk/roadmap/links`
  - `POST /api/v1/helpdesk/roadmap/links`, body `{"name","expires_in_days"}` with default 90 and range 1–365. Returns `{link, url}`; the URL is shown once.
  - `POST /links/{id}/revoke`
  - `DELETE /links/{id}`
- **URL base:** build the URL from the configured portal base URL. Look for an existing config value, such as the one the enrol one-liner uses. If there isn't one, add `PORTAL_BASE_URL` to config, with a sensible default derived from existing settings, and report it.
- **Public route:** `GET /public/roadmap/{token}` returns 200 with the roadmap body plus `"shared_with": name`, or 404, per Global Constraints.

- [ ] **Step 1: Write `TestNewToken`** (unit). The prefix is `rmp_`, the token is 47 characters, two calls differ, and `HashToken(token)` equals the returned hash.
- [ ] **Step 2: Write `TestRoadmapShareLinks`** (integration).
  - An editor creates a link. The response URL ends `/roadmap/s/rmp_…`, and the stored row has no plain token.
  - The public GET returns 200 with `shared_with` and the headers from Global Constraints. `use_count` becomes 1.
  - After revoking, the GET returns 404 with the exact message, and `use_count` stays 1.
  - A link whose `expires_at` is in the past returns 404.
  - A random `rmp_` token returns 404.
  - `expires_in_days` of 400 returns 400.
  - A non-editor gets 403 on link routes.
  - The 31st request within a minute from one IP gets 429.
- [ ] **Step 3: Run them.** They should FAIL.
- [ ] **Step 4: Implement.** The public handler reads through the admin pool. Its 404 body is identical for unknown, expired and revoked tokens.
- [ ] **Step 5: Run them.** They should PASS. Then run `go build ./...`, `go vet` on the touched packages, and the full `go test ./...` with the test DB.
- [ ] **Step 6: Commit:** "Roadmap share links and public read".

### Task 5: Portal read view and public share page

**Files:**
- Create: `av-bridge-portal/src/components/roadmap-view.tsx`
- Create: `av-bridge-portal/src/app/roadmap/page.tsx`
- Create: `av-bridge-portal/src/app/roadmap/s/[token]/page.tsx`
- Modify: `src/lib/api.ts` (`getRoadmap()`, `getSharedRoadmap(token)` with an unauthenticated fetch), `src/lib/types.ts`, `src/components/sidebar.tsx` (a top-level **Roadmap** entry for every signed-in user)

**Interfaces:**
- Consumes: the Task 2 and Task 4 response shapes.
- Produces: `<RoadmapView roadmap={Roadmap} sharedWith?: string />`.

- [ ] **Step 1: Build `RoadmapView`,** recreating the current roadmap page's design with portal theme tokens:
  - a header with headline, lede and an "as of" date;
  - the legend: Committed is solid, Planned is outlined, Exploring is dashed;
  - a 4-column by theme-row grid, with cards showing title, description, status chip and "{Month} release";
  - "Recently shipped";
  - the disclaimer;
  - a stacked list for each period below 760 px.

  All text is rendered as plain text.
- [ ] **Step 2: Build `/roadmap`,** inside the AppShell, using `getRoadmap()`. Include loading and error states.
- [ ] **Step 3: Build `/roadmap/s/[token]`:**
  - It sits outside the AppShell and auth guard. Check how the sign-in and wall-display pages avoid them.
  - It sends no session header.
  - At the top it shows: "Shared with {name} by Involve. Please don't forward this link."
  - On 404 it shows the plain message and "Contact your Involve account manager for a new link."
  - Its metadata sets `robots: { index: false, follow: false }`.
- [ ] **Step 4: Run `npx tsc --noEmit` and `npm run build`.** Both should pass. If a dev server is practical, check `/roadmap` at 375 px wide.
- [ ] **Step 5: Commit:** "Portal: roadmap page and shared roadmap link page".

### Task 6: Helpdesk roadmap editor and Users toggle

**Files:**
- Create: `av-bridge-portal/src/app/helpdesk/roadmap/page.tsx` (sections may be split into `src/components/roadmap-editor/*.tsx`)
- Modify: `src/lib/api.ts` (editor and link calls), `src/app/helpdesk/page.tsx` (link to Roadmap, shown only when `session.user.roadmap_editor`), the helpdesk Users UI (a **Roadmap editor** toggle for Involve admins)
- Modify: the session user type and the cloud session or me response, so the portal knows `roadmap_editor`. Check where `is_vendor` comes from and add the flag alongside it.

**Interfaces:**
- Consumes: the Task 3 and Task 4 routes; `RoadmapView` from Task 5 for the Preview.

- [ ] **Step 1: Build the editor.** Every save shows its error inline, and the busy state blocks a double submit.
  - **Items:** a table grouped by theme, with columns for period, status, release month, title and description, plus up/down controls that call the order endpoint. Editing happens in a modal form. Delete asks for confirmation.
  - **Periods and themes:** inline edit, reorder, and a "Current period" radio.
  - **Recently shipped:** add, edit, reorder and remove.
  - **Settings:** headline, lede, as-of date and disclaimer.
  - **Share links:**
    - The list shows name, created, expires, last opened, opens and status.
    - **New link** opens a modal with name and expiry (30, 90, 180 or 365 days). It then shows the URL once with a **Copy** button and the warning "You won't see this link again."
    - **Revoke** asks for confirmation.
  - **Preview** opens `/roadmap` in a new tab.
- [ ] **Step 2: Add the Users toggle.** It's visible to Involve admins, on Involve users only.
- [ ] **Step 3: Run `npx tsc --noEmit` and `npm run build`.** Both should pass.
- [ ] **Step 4: Commit:** "Helpdesk: roadmap editor and share links".

### Task 7: Docs (working copy only)

- [ ] Write `docs/product/mintlify/static/portal/roadmap.mdx`, aimed at customers: where the Roadmap page is and what the three statuses mean. Add `"roadmap"` to the portal group in `docs/product/mintlify/build.py` `STATIC_GROUPS`. Don't commit either. Report the files.

**Release:** cloud next version (migration 0078) plus a portal push. Then the controller tells the user to switch on Roadmap editor for themselves under Helpdesk → Users.
