# Product roadmap in the portal, with share links

**Date:** 2026-11-09
**Status:** Design approved in conversation 2026-11-09

## Purpose

The product roadmap currently exists only as a private Claude artifact. Two groups need to see it:

- **Customers**, who already have portal accounts.
- **Prospects**, who don't have portal accounts.

The roadmap must stay protected, and only named Involve people may change it.

## Decisions taken with the user

- **Customer access:** every signed-in customer user, in any role, sees a read-only **Roadmap** page.
- **Prospect access:** prospects get personal share links, not a shared password.
  - Each link has a name, an expiry and a usage count.
  - Each link can be revoked.
  - Each link shows a "Shared with …" line.
- **Editing:**
  - Content is edited in the portal, in Helpdesk → Roadmap. No release is needed to change it.
  - Only Involve users who have the new **Roadmap editor** switch can edit items and manage share links.
  - Involve admins turn that switch on or off for each person.
- **One roadmap for everyone.** There are no per-customer versions.

## Current state

- Display links are the pattern to copy: migration 0059 `dashboard_display_links`, `internal/portalauth/display.go`.
  - Tokens use a prefix (`dsp_`).
  - Only a SHA-256 hash of each token is stored.
  - `last_used_at` and `revoked_at` are recorded.
- Involve (vendor) users:
  - They have the fixed roles admin, operator and viewer.
  - `Principal.IsVendor` is set, and `VendorRoleHasPermission` gates what they can do.
  - `RequireVendorAdmin` guards the helpdesk routes.
  - The helpdesk Users page is `internal/portalapi/helpdesk_users.go`; the portal side is under `app/helpdesk/`.
- Audit: `audit_log.customer_id` is NOT NULL. Involve-only actions are therefore logged with slog, as already ruled for vendor privacy actions, with the actor recorded as `user:<uuid>`.
- The next free migration number is **0078**.
- The seed content is the current roadmap page. Its `QUARTERS`, `LANES` and `ITEMS` arrays (17 items) and its "Recently shipped" list are the source. A copy is in the implementer's working folder, and the content is reproduced in the migration.

## Design

### Data (migration 0078)

All tables are global: they have no `customer_id` and no RLS.

- **Grants:**
  - `app_admin` gets SELECT, INSERT, UPDATE and DELETE.
  - `app_tenant` gets SELECT on the content tables only, not on share links.
- **Reads:**
  - Signed-in reads go through the admin pool, with permission checks in the handlers.
  - The public share endpoint also uses the admin pool.

| Table | Columns |
|---|---|
| `roadmap_settings` | One row, keyed by `id boolean PRIMARY KEY DEFAULT true CHECK (id)`. Columns: `title`, `headline`, `lede`, `as_of date`, `disclaimer`, `updated_at`, `updated_by` |
| `roadmap_periods` | `id`, `position int UNIQUE` (0–3), `name` (e.g. "Now"), `months` (e.g. "Nov 2026 – Jan 2027"), `is_current bool` |
| `roadmap_themes` | `id`, `position int`, `name`, `note` |
| `roadmap_items` | `id`, `theme_id` → themes ON DELETE RESTRICT, `period_position int` (0–3), `status text` CHECK in (`committed`, `planned`, `exploring`), `release_month text` (nullable, e.g. "Jan"), `title` (1–80 characters), `description` (1–240 characters), `position int`, `updated_at`, `updated_by` |
| `roadmap_shipped` | `id`, `when_label` (e.g. "Nov 2026"), `title`, `position int` |
| `roadmap_share_links` | `id`, `name` (1–80 characters, the recipient), `token_hash text UNIQUE`, `expires_at timestamptz NOT NULL`, `created_by`, `created_at`, `last_used_at`, `use_count int NOT NULL DEFAULT 0`, `revoked_at` |

- **Users:** add `users.roadmap_editor boolean NOT NULL DEFAULT false`. It is only meaningful for Involve users.
- **Seed data:** the migration inserts the settings, the four periods, the four themes, the 17 items and the 7 shipped entries from the current roadmap.
- **Retention:** share links are deleted 180 days after they expire or are revoked, through a new rule in `internal/retention`.
- **Privacy register:** `created_by` and `updated_by` hold operator identities. Add them to `internal/privacy/register.go`, following how existing `_by` columns are handled, so the guard test passes.

### Permissions

- **`portalauth.CanEditRoadmap(p)`:** true only for an Involve user whose `users.roadmap_editor` is true. Read the flag when the principal is resolved: add `Principal.RoadmapEditor` and fill it in `LocalResolver`. Other resolvers leave it false.
- **Viewing:** any signed-in principal can view the roadmap, whether a customer user or an Involve user. Display-link principals cannot.
- **Managing the switch:** Involve admins set it through the helpdesk Users API. Add `roadmap_editor` to the update request and the list response.

### APIs

**Signed-in:**
- `GET /api/v1/roadmap` returns `{settings, periods, themes, items, shipped}`.

**Roadmap editors only**, otherwise 403 `"Only roadmap editors can change the roadmap."`:
- `PUT /api/v1/helpdesk/roadmap/settings`
- `POST /api/v1/helpdesk/roadmap/items`, plus `PATCH` and `DELETE` on `/items/{id}`
- `PUT /api/v1/helpdesk/roadmap/items/order`, taking the full ordered list of ids per theme and period
- `PUT /api/v1/helpdesk/roadmap/periods`, which replaces all four
- `PUT /api/v1/helpdesk/roadmap/themes`, a full list with add, rename and reorder. Deleting a theme that still has items is refused with 409.
- `PUT /api/v1/helpdesk/roadmap/shipped`, a full list
- `GET` and `POST /api/v1/helpdesk/roadmap/links`, `POST /links/{id}/revoke`, and `DELETE /links/{id}`
  - Creating a link returns the full URL **once**: `{base}/roadmap/s/rmp_<32 random bytes, base64url>`.
  - The default expiry is 90 days and the maximum is 1 year.

**Public, no authentication**, served through the same rewrite as `/public`:
- `GET /public/roadmap/{token}` returns the same body as the signed-in endpoint, plus `shared_with: <link name>`.
- It returns 404 `"This link isn't valid any more."` when the token is unknown, expired or revoked, giving no hint about which of those applies.
- **On success:** it bumps `last_used_at` and `use_count`.
- **Rate limit:** 30 requests per minute per IP address. Copy the existing public rate-limit helper if there is one.
- **Response headers:** `Cache-Control: no-store` and `X-Robots-Tag: noindex, nofollow`.

**Validation and logging:**
- Every editor action is logged with slog as `roadmap.*`, with the actor recorded as `user:<uuid>` and no email address.
- All text fields are length-checked and trimmed.
- The portal renders them as plain text, never as HTML.

### Portal

- **`/roadmap`**, the read view:
  - The sidebar shows it for every signed-in user under a top-level **Roadmap** entry. Customers also see it in their menu.
  - It recreates the current design: a header with headline, lede and as-of date; the legend; four period columns by four theme rows, with cards showing the status style and release month; the "Recently shipped" list; and the disclaimer.
  - Below 760 px it shows a stacked list per period.
  - It uses portal theme tokens, so light and dark modes both work.
  - Build it as a shared `RoadmapView` component.
- **`/roadmap/s/[token]`**, the public share page:
  - It has no AppShell and no sign-in.
  - It fetches `/public/roadmap/{token}` and renders `RoadmapView` with a top line *"Shared with {name} by Involve. Please don't forward this link."*
  - Its page metadata sets `robots: noindex`.
  - A bad or expired link shows the plain 404 message and an Involve contact line.
  - Next.js must allow this path without a session; check the middleware and AppShell auth guard.
- **Helpdesk → Roadmap**, at `/helpdesk/roadmap`, visible only to roadmap editors:
  - **Items:** a table grouped by theme with columns for period, status, release month, title, description, and up/down order controls. Editing and adding happen in a modal form; deleting asks for confirmation.
  - **Periods and themes:** inline edit, reorder, and mark the current period.
  - **Recently shipped:** add, edit, reorder and remove.
  - **Settings:** headline, lede, as-of date and disclaimer.
  - **Share links:**
    - The list shows name, created, expires, last opened, opens and status.
    - **New link** asks for a name and expiry, then shows the URL once with a Copy button.
    - **Revoke** asks for confirmation.
  - A **Preview** button opens `/roadmap`.
- **Helpdesk Users:** a **Roadmap editor** checkbox or toggle on each Involve user, which Involve admins can change.

### Security notes

- Tokens are random 32-byte values with an `rmp_` prefix. Only their SHA-256 hash is stored, and they are compared by hash lookup.
- The public endpoint exposes only roadmap content and the link's own name, with no other data.
- Expired and revoked links behave identically to unknown ones.
- The share page sends no auth header, and the portal must not attach a session to it.

### Testing

- **Integration tests**, with `TEST_DATABASE_URL`:
  - the signed-in read works for customer and Involve users;
  - edits are refused for a non-editor Involve admin, then allowed after the switch is turned on;
  - the item validation rules hold;
  - the theme-delete 409;
  - the full share-link lifecycle: create, then public read bumps the counts, then revoke gives 404, and expiry gives 404;
  - an unknown token gives 404;
  - the rate limit;
  - the seed rows exist;
  - the retention rule;
  - the privacy guard test still passes.
- **Portal:** `tsc` and `next build` pass. Check the read view at phone width.

### Rollout

- Cloud release with migration 0078, plus the portal push.
- After deployment, switch on Roadmap editor for the user through Helpdesk → Users.
- Docs, kept in the working copy only: a short `portal/roadmap.mdx` page for customers, and its `build.py` entry.

## Out of scope

- Per-customer roadmaps.
- Comments or voting.
- Email notifications when the roadmap changes.
- A public, unauthenticated roadmap without a link.
- Edit history and versioning beyond `updated_by` and `updated_at`.
