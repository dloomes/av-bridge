# Production Environment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A production M.A.R.C.U.S. stack in AWS account `avrmm-prod` that delivers what the customer documents promise, with a web/worker split, branded `<customer>.app.involvecloud.com` sign-in, UptimeRobot monitoring and a build-once/promote release pipeline, live for a Standard-tier customer next week.

**Architecture:** One cloud image runs in two ECS services chosen by `MARCUS_ROLE`: `web` (2+ copies across three AZs behind the ALB) and `worker` (one copy: every background loop plus remote access). The only state that must cross copies, live events and tunnel presence, goes through Postgres. Terraform reuses the UAT modules for a new `envs/prod`, with `app.`, `api.` and `access.involvecloud.com` delegated to Route 53 in the prod account. GitHub Actions builds and signs once, deploys UAT, and a manually-run workflow promotes the same image to prod.

**Tech Stack:** Go 1.x (av-bridge-cloud, pgx v5), Next.js (av-bridge-portal), Terraform (AWS provider), ECS Fargate, RDS PostgreSQL Multi-AZ, Amplify, Route 53, SES, CloudWatch/SNS, GitHub Actions with OIDC.

**Spec:** `docs/superpowers/specs/2026-11-18-production-environment-design.md`

**One change from the spec, agreed in planning:** `api.involvecloud.com` is delegated to Route 53 like `app.` and `access.` (instead of a CNAME at DNS Made Easy), so every certificate and alias record is terraform-managed. Daniel adds only NS records for the three subzones and the `status` CNAME at DNS Made Easy.

## Global Constraints

- Production account `avrmm-prod` = 568339496641, region `eu-west-2`, AZs `eu-west-2a`, `eu-west-2b`, `eu-west-2c`; CLI profile `avrmm-prod`.
- `terraform apply` in either account only after Daniel's explicit OK in the conversation. `terraform plan` is fine without asking.
- `docs/product/**` is edited in the working copy and **never committed**.
- The Collector update signing key is only ever passed as a BuildKit secret (`--secret id=update_signing_key,...`); never written to a file in the repo, image layer, tfvars or log.
- No plaintext secrets in any committed file or in `prod.tfvars`; alert email/phone numbers live in git-ignored tfvars and in UptimeRobot only.
- UAT keeps running as one copy with `MARCUS_ROLE` unset, and its behaviour must not change.
- Hostnames: portal `app.involvecloud.com`; branded `<slug>.app.involvecloud.com`; API/Collectors `api.involvecloud.com`; remote access `*.access.involvecloud.com`; status `status.involvecloud.com`; email from `noreply@notify.involvecloud.com` (identity already exists in prod SES, created by CLI 2026-11-18 — import it, never recreate).
- Prod settings: `ALLOW_MOCK_JWT` unset; `NIGHTLY_DRY_RUN=false`; `ACCESS_DOMAIN=access.involvecloud.com`; `CLOUD_BASE_URL=https://api.involvecloud.com`; `ENTRA_PORTAL_BASE_URL=https://app.involvecloud.com`; `BRANDED_DOMAIN=app.involvecloud.com`.
- Commit messages end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Cloud integration tests run with `TEST_DATABASE_URL` against a throwaway Postgres (see `internal/collectorha/lease_integration_test.go` for the pattern: `db.Migrate`, then `db.New` as `app_admin`/`app_tenant`); run them as `app_admin`, not `postgres`, where RLS matters.

## Review Focus

1. **Web copy restarts while browsers are connected** — a browser's live-event stream reconnects to the other web copy and keeps receiving events from Collectors whose telemetry lands on either copy. Test: Task 3 `TestRelayDeliversAcrossHubs`.
2. **Worker restarts while a Collector tunnel is open** — after restart the portal must not claim the old tunnel is connected; the Collector reconnects and presence returns. Test: Task 4 `TestPresenceClearedOnStartup`.
3. **Two copies start a deploy at the same moment with a pending migration** — exactly one applies it, the other waits, neither fails. Test: Task 1 `TestMigrateConcurrent`.
4. **A customer slug that collides with a platform host** (`status`, `access`, `notify`, `uat`, `portal`) — refused on create/edit, and never treated as a branded host by the portal. Test: Task 5 `TestReservedSlugs`.
5. **A live event larger than Postgres NOTIFY's 8000-byte limit** — still delivered intact. Test: Task 3 `TestRelayOversizeEvent`.

---

## Part A — Application changes (shippable to UAT first)

### Task 1: Migrations take a database lock

**Files:**
- Modify: `av-bridge-cloud/internal/db/migrate.go` (`Migrate`)
- Test: `av-bridge-cloud/internal/db/migrate_lock_integration_test.go`

**Interfaces:**
- Produces: `Migrate(ctx, dsn, log) error` unchanged signature; now serialised across processes.

- [ ] **Step 1: Write the failing test** `TestMigrateConcurrent` (skips without `TEST_DATABASE_URL`): drop and recreate a scratch database `migrate_lock_test`, run `db.Migrate` from 4 goroutines at once against it, assert all return nil and `SELECT count(*) FROM schema_migrations` equals the number of embedded migrations (no duplicate-key or "already exists" errors).
- [ ] **Step 2: Run it to see it fail** — `TEST_DATABASE_URL=... go test ./internal/db/ -run TestMigrateConcurrent -count=1` → FAIL (duplicate key / relation already exists).
- [ ] **Step 3: Implement** — after connecting, `SELECT pg_advisory_lock($1)` with `const migrateLockKey int64 = 0x4d415243` and `defer` `pg_advisory_unlock($1)` before `ensureMigrationsTable`. Session-level lock on the dedicated migrate connection.
- [ ] **Step 4: Run the test** → PASS; also `go test ./internal/db/ -count=1` (unit) → PASS.
- [ ] **Step 5: Commit** — `git commit -m "Migrations take an advisory lock so two copies can't apply them at once"`.

### Task 2: Role setting — web, worker, or both

**Files:**
- Modify: `av-bridge-cloud/internal/config/config.go` (new field + parse)
- Modify: `av-bridge-cloud/cmd/av-bridge-cloud/main.go` (gate background loops)
- Test: `av-bridge-cloud/internal/config/role_test.go`

**Interfaces:**
- Produces: `type Role string`; constants `RoleAll Role = "all"`, `RoleWeb Role = "web"`, `RoleWorker Role = "worker"`; `func ParseRole(s string) (Role, error)` (`""` → `RoleAll`, case-insensitive, anything else → error naming the allowed values); `func (r Role) RunsBackground() bool` (true for all/worker); `func (r Role) ServesTunnels() bool` (true for all/worker); `Config.Role Role` from env `MARCUS_ROLE`.

- [ ] **Step 1: Write failing tests** `TestParseRole`: `""`→all, `"WEB"`→web, `"worker"`→worker, `"both"`→error containing `all, web, worker`; `TestRoleCapabilities`: web → `RunsBackground()==false`, `ServesTunnels()==false`; worker and all → both true.
- [ ] **Step 2: Run** `go test ./internal/config/ -run 'Role' -count=1` → FAIL (undefined).
- [ ] **Step 3: Implement** the type in `config.go`; `Load` fails startup on an invalid `MARCUS_ROLE`.
- [ ] **Step 4: Gate main.go** — every `go X.Run(sweeperCtx)` (collector updates, telemetry, retention, remote sessions, command sweeper, session cleaner, collector watcher, nightly scheduler, nightly digest, ServiceNow ticket sync) runs only when `cfg.Role.RunsBackground()`. Log the role at startup (`log.Info("role", "role", cfg.Role)`). HTTP server, migrations and the nightly executor (lazy, used by run-now) stay in every role.
- [ ] **Step 5: Run** `go test ./internal/config/ -count=1 && go build ./...` → PASS.
- [ ] **Step 6: Commit** — `"Cloud: MARCUS_ROLE selects web, worker or both (default both)"`.

### Task 3: Live events reach browsers on every web copy

**Files:**
- Create: `av-bridge-cloud/internal/wsfanout/relay.go`
- Create: `av-bridge-cloud/internal/db/migrations/0079_live_event_overflow.sql`
- Modify: `av-bridge-cloud/internal/ingest/handler.go` (publisher type), `av-bridge-cloud/internal/portalapi/offboarding.go` (`SetLiveEvents` type), `av-bridge-cloud/cmd/av-bridge-cloud/main.go` (wire relay, start `relay.Run`)
- Test: `av-bridge-cloud/internal/wsfanout/relay_integration_test.go`

**Interfaces:**
- Produces:
  - `type Publisher interface { Publish(customerID string, ev Event); CloseCustomer(customerID string) int }` — `*Hub` already satisfies it.
  - `func NewRelay(pool *pgxpool.Pool, hub *Hub, log *slog.Logger) *Relay`; `(*Relay) Publish(customerID string, ev Event)`; `(*Relay) CloseCustomer(customerID string) int` (returns 0: closes happen asynchronously on every copy); `(*Relay) Run(ctx context.Context)` (LISTEN loop with reconnect, backoff 1s→30s; every 10 min deletes overflow rows older than 1 hour).
  - NOTIFY channel `live_events`; message JSON `{"c":"<customer id>","k":"event"|"close"|"ref","e":<Event>,"id":<overflow id>}`; inline limit `maxInlineNotify = 7900` bytes; above it insert into `live_event_overflow` and send `k:"ref"`.
- Consumes: Task 2 not required (relay runs in every role).

- [ ] **Step 1: Migration 0079** — `CREATE TABLE live_event_overflow (id bigserial PRIMARY KEY, body jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now()); GRANT SELECT, INSERT, DELETE ON live_event_overflow TO app_admin; GRANT USAGE ON SEQUENCE live_event_overflow_id_seq TO app_admin;`
- [ ] **Step 2: Write failing tests** (skip without `TEST_DATABASE_URL`): `TestRelayDeliversAcrossHubs` — two relays on two separate hubs share one DB; a test subscriber on hub B (use an exported test helper `hub.SubscribeForTest(customerID) <-chan Event` added in this task) receives an event published through relay A within 2 s, and a subscriber for a different customer receives nothing. `TestRelayOversizeEvent` — an event whose `Payload` holds a 20 kB string arrives intact on hub B and the overflow row exists. `TestRelayCloseCustomer` — `CloseCustomer` on relay A kicks a subscriber on hub B.
- [ ] **Step 3: Run** `go test ./internal/wsfanout/ -count=1` → FAIL.
- [ ] **Step 4: Implement `relay.go`** — publish via `SELECT pg_notify('live_events', $1)` on the admin pool with a 2 s timeout, logging (not returning) errors so ingest is never blocked; `Run` acquires a dedicated connection, `LISTEN live_events`, `WaitForNotification`, decodes and calls `hub.Publish` / `hub.CloseCustomer`; `k:"ref"` reads `body` from `live_event_overflow` first.
- [ ] **Step 5: Wire it** — ingest `NewHandler` and `portalapi.SetLiveEvents` take `wsfanout.Publisher`; main.go builds `relay := wsfanout.NewRelay(store.AdminPool(), hub, log)`, passes `relay` to both, `go relay.Run(sweeperCtx)` in every role; `/ws/events` keeps using `hub`. Offboarding's log field `event_streams_closed` becomes `event_streams_close_requested`.
- [ ] **Step 6: Run** the integration tests and `go test ./... -count=1` → PASS.
- [ ] **Step 7: Commit** — `"Live events go through Postgres so every web copy's browsers get them"`.

### Task 4: Tunnel presence in the database; worker drops offboarded customers' tunnels

**Files:**
- Create: `av-bridge-cloud/internal/db/migrations/0080_remote_tunnel.sql`
- Create: `av-bridge-cloud/internal/remote/presence.go`
- Modify: `av-bridge-cloud/internal/remote/tunnel.go` (record connect/close), `av-bridge-cloud/internal/remote/sessions.go` (`Sweep` drops offboarding tunnels), `av-bridge-cloud/internal/portalapi/remote_access.go:116` (use DB presence), `av-bridge-cloud/cmd/av-bridge-cloud/main.go`
- Test: `av-bridge-cloud/internal/remote/presence_integration_test.go`

**Interfaces:**
- Produces:
  - Table `remote_tunnel (collector_id uuid PRIMARY KEY REFERENCES collectors(id) ON DELETE CASCADE, connected_at timestamptz NOT NULL DEFAULT now())`, granted SELECT/INSERT/UPDATE/DELETE to `app_admin`.
  - `func (t *Tunnels) SetPresence(pool *pgxpool.Pool)`; on connect upsert `(collector_id, now())` and remember the timestamp; on close `DELETE ... WHERE collector_id=$1 AND connected_at=$2` (a reconnect's newer row survives).
  - `func ClearPresence(ctx context.Context, pool *pgxpool.Pool) error` — deletes all rows; main.go calls it at startup when `cfg.Role.ServesTunnels()` (one tunnel-serving copy per environment).
  - `func TunnelConnected(ctx context.Context, pool *pgxpool.Pool, collectorID string) (bool, error)`.
  - `Sweep` additionally calls `t.Drop(ids...)` for collectors whose customer has `offboarding_started_at IS NOT NULL` (reuse the condition in `internal/db/offboarding.go`).
- Consumes: `cfg.Role.ServesTunnels()` (Task 2).

- [ ] **Step 1: Migration 0080** as above.
- [ ] **Step 2: Write failing tests:** `TestPresenceRecordsAndClears` (simulate connect/close through the presence helpers: row appears, then is gone; a second "connect" after the first leaves the newer row when the older closes); `TestPresenceClearedOnStartup` (rows exist → `ClearPresence` → `TunnelConnected` false); `TestSweepDropsOffboardingTunnels` (a registered fake session for a collector whose customer is offboarding is closed by `Sweep`).
- [ ] **Step 3: Run** → FAIL.
- [ ] **Step 4: Implement** presence.go and the tunnel.go hooks (presence errors are logged, never fail the tunnel).
- [ ] **Step 5: Portal check** — `remote_access.go` replaces `h.remote.Tunnels().Connected(machine)` with `remote.TunnelConnected(ctx, pool, machine)`; on error return 503 "remote access status unavailable".
- [ ] **Step 6: Run** tests + `go test ./... -count=1` → PASS.
- [ ] **Step 7: Commit** — `"Remote access: tunnel presence in the database; worker drops offboarded customers' tunnels"`.

### Task 5: Branded addresses follow a configured domain; more reserved slugs (cloud)

**Files:**
- Create: `av-bridge-cloud/internal/brandurl/brandurl.go`, `av-bridge-cloud/internal/brandurl/brandurl_test.go`
- Modify: `internal/publicapi/branding.go:~97-118` (`resetOriginFor`), `internal/publicapi/magic_link.go:~160-170`, `internal/portalapi/magic_link.go:~290-300`, `internal/portalapi/entra_customer.go:~585-625` (both sites), `internal/config/config.go` (`BRANDED_DOMAIN`), wiring in `main.go`, `internal/db/slug.go` (reserved list)
- Test: `internal/db/slug_test.go` (extend or create)

**Interfaces:**
- Produces: `func brandurl.Origin(portalBaseURL, brandedDomain, slug string) string` — empty `portalBaseURL` → `""`; empty slug → portal base without trailing slash; `brandedDomain` set → `<scheme>://<slug>.<brandedDomain>`; `brandedDomain` empty → today's behaviour (swap the first label). `Config.BrandedDomain` from env `BRANDED_DOMAIN`.

- [ ] **Step 1: Write failing tests** `TestOrigin` table: (`https://app.uat.involvecloud.com`, `""`, `acme`) → `https://acme.uat.involvecloud.com`; (`https://app.involvecloud.com`, `app.involvecloud.com`, `acme`) → `https://acme.app.involvecloud.com`; (`https://app.involvecloud.com/`, `app.involvecloud.com`, `""`) → `https://app.involvecloud.com`; (`""`, `x`, `acme`) → `""`. `TestReservedSlugs`: `status`, `access`, `notify`, `uat`, `portal` are rejected by the server-side slug validation in `internal/db/slug.go`; `acme` accepted.
- [ ] **Step 2: Run** → FAIL.
- [ ] **Step 3: Implement** `brandurl.Origin`; add the five names to the reserved set in `slug.go`.
- [ ] **Step 4: Replace all five first-label swaps** with `brandurl.Origin(base, cfg.BrandedDomain, slug)` (pass `BrandedDomain` into the handlers alongside the existing portal base URL).
- [ ] **Step 5: Run** `go test ./... -count=1` → PASS; `grep -rn 'slug + "\."' internal` → no matches.
- [ ] **Step 6: Commit** — `"Branded sign-in addresses follow BRANDED_DOMAIN; reserve status/access/notify/uat/portal"`.

### Task 6: Portal shows the configured branded domain

**Files:**
- Modify: `av-bridge-portal/src/lib/branding-slug.ts` (reserved set + helper), `src/app/customers/page.tsx:214`, `src/app/helpdesk/page.tsx:288`, `src/app/settings/branding/page.tsx:339`, `src/components/customer-forms.tsx:131,326,337`

**Interfaces:**
- Produces: `export const BRANDED_DOMAIN = process.env.NEXT_PUBLIC_AV_BRIDGE_BRANDED_DOMAIN ?? "uat.involvecloud.com"`; `export function brandedHost(slug: string): string` → `` `${slug}.${BRANDED_DOMAIN}` ``. Reserved set gains `status`, `access`, `notify`, `uat`, `portal`.

- [ ] **Step 1: Implement** the helper and replace every hard-coded `.uat.involvecloud.com` in the listed files (and the `<slug>.uat.involvecloud.com` hint text) with `brandedHost(...)` / `BRANDED_DOMAIN`.
- [ ] **Step 2: Verify** — `cd av-bridge-portal && npx tsc --noEmit` → no errors; `grep -rn "uat.involvecloud.com" src --include=*.tsx` → only comments.
- [ ] **Step 3: Commit** — `"Portal: branded addresses use NEXT_PUBLIC_AV_BRIDGE_BRANDED_DOMAIN"`.

### Task 7: Release Part A to UAT and confirm nothing changed

- [ ] **Step 1:** Add `BRANDED_DOMAIN=uat.involvecloud.com` to the UAT task definition and `NEXT_PUBLIC_AV_BRIDGE_BRANDED_DOMAIN=uat.involvecloud.com` to UAT Amplify env — in terraform (`envs/uat/main.tf`, cloud-service env + portal env vars), part of Task 8's plan/apply. Until then the defaults reproduce today's behaviour.
- [ ] **Step 2:** Release cloud vNEXT to UAT with the existing laptop flow (memory: git archive → docker build with signing secret → manifest 3 signatures → temp DOCKER_CONFIG push → ECS force-new-deployment → healthz), push `main` for Amplify.
- [ ] **Step 3: Verify in UAT** — portal live device updates still arrive; start a remote access session to a device (Connected check passes); a customer's branded sign-in address shows `<slug>.uat.involvecloud.com`; logs show `role=all`; CloudWatch shows "migration applied" for 0079 and 0080 exactly once.

## Part B — Infrastructure

### Task 8: Modules support web/worker, external-DNS SES, and UAT stays identical

**Files:**
- Modify: `av-bridge-ops/terraform/modules/cloud-service/{main,variables,outputs}.tf`
- Modify: `av-bridge-ops/terraform/modules/ses/{main,variables,outputs}.tf`
- Modify: `av-bridge-ops/terraform/envs/uat/main.tf` (new env vars from Task 7; `moved` blocks if addresses change)

**Interfaces:**
- Produces (cloud-service): new variables `role` (string, default `""` → no `MARCUS_ROLE` env), `cluster_arn` (string, default `""` → module creates the cluster as today), `extra_environment` (list of `{name,value}`, default `[]`), `listener_rules` (list of `{priority=number, host_headers=list(string), path_patterns=list(string)}`; default reproduces today's single rule from `listener_rule_priority`/`listener_host_names`), `container_insights` (bool, default false). Outputs add `cluster_arn`, `service_name`, `target_group_arn_suffix`.
- Produces (ses): `manage_dns` (bool, default true; false skips every `aws_route53_record` and the verification resource and outputs the records instead), `mail_from_subdomain` (string, default `""`; set → `aws_ses_domain_mail_from`).

- [ ] **Step 1:** Implement the variables with defaults that keep UAT's resources unchanged; add `moved {}` blocks for any resource whose address gains `count`/`for_each`.
- [ ] **Step 2: Verify** — `cd envs/uat && terraform init && terraform plan -out uat.plan`; expected: **no destroy or replace**; only the two new env vars as in-place task-definition/Amplify changes. Paste the plan summary to Daniel.
- [ ] **Step 3:** With Daniel's OK, `terraform apply uat.plan`; confirm UAT healthz 200 and portal loads.
- [ ] **Step 4: Commit** — `"Terraform: cloud-service role/rules/shared cluster, SES with external DNS"`.

### Task 9: Bootstrap prod state

- [ ] **Step 1:** Create git-ignored `av-bridge-ops/terraform/bootstrap/prod.tfvars` from `prod.tfvars.example` (region eu-west-2, profile avrmm-prod, environment prod).
- [ ] **Step 2:** `terraform plan` in `bootstrap` with a separate state for prod (the bootstrap module uses local state per environment — follow how UAT's was created; keep prod's state file out of git) → expect S3 `avrmm-tf-state-prod` + DynamoDB `avrmm-tf-lock-prod`.
- [ ] **Step 3:** With Daniel's OK, apply. Record the backend block from the `backend_config_hcl` output.

### Task 10: `envs/prod` stack

**Files:**
- Create: `av-bridge-ops/terraform/envs/prod/{backend,providers,variables,main,sg,outputs}.tf`, `terraform.tfvars.example` (committed, no secrets); git-ignored `terraform.tfvars`.

**Interfaces (values pinned):**
- `name_prefix = "avrmm-prod"`; network `azs = ["eu-west-2a","eu-west-2b","eu-west-2c"]`, `single_nat_gateway = false`.
- Zones (dns-zone module ×3): `app.involvecloud.com`, `api.involvecloud.com`, `access.involvecloud.com`; outputs `name_servers` for each.
- Certs: `api.involvecloud.com` (zone api) as the ALB default cert; `*.access.involvecloud.com` (zone access) as an `aws_lb_listener_certificate`.
- ALB: `enable_deletion_protection = true`, TLS policy as UAT.
- DB: `instance_class = "db.t4g.medium"`, `multi_az = true`, `backup_retention_days = 35`, `deletion_protection = true`, same engine version as UAT, storage autoscaling on, Performance Insights on (7-day free retention).
- CloudWatch log groups for both services: retention 30 days.
- Task definitions reference `avrmm-prod-cloud:latest` (as UAT does), so releases never change terraform state; rollback = retag the previous version as `latest` and force a new deployment.
- ECR `avrmm-prod-cloud`; secrets module; entra module with `redirect_uri = "https://app.involvecloud.com/api/v1/auth/entra/vendor/callback"`.
- SES: `sending_domain = "notify.involvecloud.com"`, `manage_dns = false`, `mail_from_subdomain = "bounce"`, `from_local_part = "noreply"`, display name `"M.A.R.C.U.S."`; `terraform import` the existing identity, DKIM and MAIL FROM before the first apply.
- cloud-service ×2 sharing one cluster (`container_insights = true` on the first):
  - web: `role = "web"`, `cpu = 1024`, `memory = 2048`, `desired_count = 2`, rule priority 100 host `api.involvecloud.com`.
  - worker: `role = "worker"`, `cpu = 512`, `memory = 1024`, `desired_count = 1`, rules priority 10 host `*.access.involvecloud.com` and priority 20 path `/bridge/tunnel` (host `api.involvecloud.com`).
  - both: the Global Constraints env values; `extra_environment` carries `BRANDED_DOMAIN`.
- Route 53: `api.involvecloud.com` A alias → ALB (zone api); `*.access.involvecloud.com` A alias → ALB (zone access).
- Portal (portal-amplify): `custom_domain = "app.involvecloud.com"`, `custom_domain_prefix = ""`, `enable_wildcard_subdomain = true`, `branch_name = "release"`, `stage = "PRODUCTION"`, env `AV_BRIDGE_UPSTREAM`/`NEXT_PUBLIC_AV_BRIDGE_UPSTREAM = https://api.involvecloud.com`, `NEXT_PUBLIC_AV_BRIDGE_WS = wss://api.involvecloud.com`, `NEXT_PUBLIC_AV_BRIDGE_ENV = production`, `NEXT_PUBLIC_AV_BRIDGE_BRANDED_DOMAIN = app.involvecloud.com`, dev sign-ins off; GitHub token from a git-ignored variable (never in a committed file).

- [ ] **Step 1:** Write the stack; `terraform validate` → Success.
- [ ] **Step 2: Stage 1** — with Daniel's OK, `terraform apply -target` the three `module.dns_*` zones only; give Daniel the three NS sets (output) to add at DNS Made Easy; confirm with `nslookup -type=NS app.involvecloud.com` etc.
- [ ] **Step 3:** Push a bootstrap image to `avrmm-prod-cloud` (copy the UAT image of the Task 7 release: pull from UAT ECR, tag, push to prod ECR) so services can start.
- [ ] **Step 4:** Import SES resources; `terraform plan` → expect creates only (no SES identity create). With Daniel's OK, apply.
- [ ] **Step 5: Verify** — `https://api.involvecloud.com/healthz` → 200; ECS shows web 2/2 in two AZs and worker 1/1; RDS `MultiAZ: true`, `BackupRetentionPeriod: 35`; CloudWatch logs show `role=web` and `role=worker`; `https://app.involvecloud.com` loads the sign-in page once the `release` branch exists (Task 15).
- [ ] **Step 6: Commit** (no tfvars) — `"Terraform: production environment (Multi-AZ, web + worker, delegated app/api/access zones)"`.

### Task 11: Alarms

**Files:**
- Create: `av-bridge-ops/terraform/modules/monitoring/{main,variables,outputs}.tf`; use it from `envs/prod/main.tf`.

**Interfaces (pinned):** SNS topic `avrmm-prod-alerts` with an email subscription from git-ignored `alert_email`. Alarms (period 60 s unless stated):
- ALB `HTTPCode_Target_5XX_Count` sum ≥ 20 over 5 min.
- Web target group `HealthyHostCount` < 1 for 2 min; worker target group `HealthyHostCount` < 1 for 5 min.
- ECS Container Insights `RunningTaskCount` web < 2 for 5 min, worker < 1 for 5 min.
- RDS `CPUUtilization` > 80% for 15 min; `FreeStorageSpace` < 5 GiB; `FreeableMemory` < 256 MiB for 10 min; `DatabaseConnections` > 80 for 10 min.
- RDS event subscription (categories `failover`, `failure`, `low storage`) → the SNS topic.
- Each alarm also notifies on OK.

- [ ] **Step 1:** Implement; `terraform plan` shows only the monitoring resources.
- [ ] **Step 2:** With Daniel's OK, apply; Daniel confirms the SNS subscription email.
- [ ] **Step 3: Verify** — `aws cloudwatch set-alarm-state` on the worker alarm to ALARM → email arrives; set back to OK.
- [ ] **Step 4: Commit** — `"Terraform: production alarms to SNS"`.

### Task 12: GitHub OIDC deploy roles and the code-mirror bucket

**Files:**
- Create: `av-bridge-ops/terraform/modules/github-oidc/{main,variables,outputs}.tf`; use from `envs/uat/main.tf` and `envs/prod/main.tf`.
- Modify: `envs/prod/main.tf` (S3 bucket `avrmm-prod-code-mirror`, versioned, encrypted, public access blocked, lifecycle: noncurrent versions expire after 90 days).

**Interfaces (pinned):**
- OIDC provider `token.actions.githubusercontent.com`; role `github-deploy` in each account trusting repo `dloomes/av-bridge`:
  - UAT role: subjects `repo:dloomes/av-bridge:ref:refs/tags/v*` and `repo:dloomes/av-bridge:ref:refs/heads/main`; ECR push to `avrmm-uat-cloud`, ECR pull, `ecs:UpdateService`/`DescribeServices` on the UAT service.
  - Prod role: subject `repo:dloomes/av-bridge:ref:refs/heads/main` (promotion is dispatched on main); ECR push to `avrmm-prod-cloud`, ECS update/describe on both prod services, `rds:CreateDBSnapshot`/`DescribeDBSnapshots`/`DeleteDBSnapshot` on the prod DB, `s3:PutObject` on `avrmm-prod-code-mirror/*`.
- Outputs `role_arn` per account.

- [ ] **Step 1:** Implement; `terraform plan` in both envs shows only these additions.
- [ ] **Step 2:** With Daniel's OK, apply UAT and prod.
- [ ] **Step 3: Commit** — `"Terraform: GitHub OIDC deploy roles; code mirror bucket"`.

## Part C — Release pipeline

### Task 13: CI on every push

**Files:**
- Create: `.github/workflows/ci.yml`, `.github/dependabot.yml`

- [ ] **Step 1:** `ci.yml` on push and pull_request: job `go` (matrix `av-bridge`, `av-bridge-cloud`: `go vet ./...`, `go test ./... -count=1`; cloud job with a `postgres:16` service and `TEST_DATABASE_URL` set so integration tests run), job `portal` (`npm ci`, `npx tsc --noEmit`, `npm run build` in `av-bridge-portal`), job `security` (`gosec ./...` and `staticcheck ./...` per Go module; fail on findings at severity ≥ medium for gosec). `dependabot.yml`: weekly for `gomod` in both Go dirs, `npm` in portal, `github-actions`.
- [ ] **Step 2: Verify** — push to a branch; all jobs green on GitHub (fix or explicitly `#nosec`-annotate with a reason any finding).
- [ ] **Step 3:** Check in the repository settings whether secret scanning / push protection is available on this plan. Record the answer in the Task 17 doc edits (if unavailable for a private repo on the free plan, the Security & Trust wording omits it).
- [ ] **Step 4: Commit** — `"CI: tests, type-check, gosec, staticcheck, Dependabot"`.

### Task 14: Release workflow — build and sign once, deploy UAT

**Files:**
- Create: `.github/workflows/release-cloud.yml`

- [ ] **Step 1:** On tags `v*` (alongside the existing `release-bridge.yml`): OIDC into the UAT role; `docker buildx build -f av-bridge-cloud/Dockerfile --build-arg BRIDGE_VERSION=${tag} --build-arg BRIDGE_BUILD_TIME=... --secret id=update_signing_key,env=UPDATE_SIGNING_KEY` with repository secret `UPDATE_SIGNING_KEY`; extract `/downloads/manifest.json` via `docker create`/`docker cp` and fail unless it holds exactly 3 `signature` entries and `version == tag`; push `avrmm-uat-cloud:${tag}` and `:latest`; `aws ecs update-service --force-new-deployment` on the UAT service; `aws ecs wait services-stable`; `curl -fsS https://api.uat.involvecloud.com/healthz`.
- [ ] **Step 2:** Daniel adds the `UPDATE_SIGNING_KEY` repository secret (contents of the key file) — never echo it.
- [ ] **Step 3: Verify** — tag the next release; workflow green; UAT healthz 200 and the Collectors page offers the new version.
- [ ] **Step 4: Commit** — `"Release: build, sign and deploy cloud to UAT from a tag"`.

### Task 15: Promote to production

**Files:**
- Create: `.github/workflows/promote.yml`

- [ ] **Step 1:** `workflow_dispatch` with input `version` (e.g. `v1.0.93`), run on `main`: assume UAT role → confirm `avrmm-uat-cloud:${version}` exists; assume prod role → `aws rds create-db-snapshot --db-snapshot-identifier avrmm-prod-pre-${version//./-}` and wait available; delete `avrmm-prod-pre-*` manual snapshots older than 365 days; copy the image (pull from UAT ECR, push `avrmm-prod-cloud:${version}` and `:latest`); `aws ecs update-service --force-new-deployment` on **worker first** (wait stable), **then web**; `aws ecs wait services-stable`; `curl -fsS https://api.involvecloud.com/healthz`; then `git push origin <commit-of-version>:refs/heads/release` (force) so Amplify builds the matching portal.
- [ ] **Step 2: Verify** — run it for the Task 14 release; prod services stable on the new tag; snapshot present; Amplify `release` build succeeds; `https://app.involvecloud.com` loads.
- [ ] **Step 3: Commit** — `"Release: promote a UAT-tested version to production"`.

### Task 16: Nightly code mirror

**Files:**
- Create: `.github/workflows/mirror.yml`

- [ ] **Step 1:** Schedule `0 2 * * *` + `workflow_dispatch`: full checkout (`fetch-depth: 0`), `git bundle create av-bridge-$(date +%F).bundle --all`, OIDC into the prod role, `aws s3 cp` to `s3://avrmm-prod-code-mirror/`.
- [ ] **Step 2: Verify** — dispatch once; object present; `git bundle verify` on a downloaded copy passes.
- [ ] **Step 3: Commit** — `"Nightly mirror of the repository to S3 in the prod account"`.

## Part D — Documents and go-live

### Task 17: Customer documents match production (working copy only — do not commit)

**Files (edit, never commit):** `docs/product/security-whitepaper.md`, `business-continuity-and-dr.md`, `data-residency.md`, `service-description-and-sla.md`, `customer-pre-requirements.md`, `deployment-guide.md`, `diagrams/production-architecture.html` (title "Production Architecture", sub "v1.0", outside-AWS boxes name UptimeRobot, on-call box "Business-hours alerting (Standard tier)").

- [ ] **Step 1:** Make the spec's "Documents to update" edits: UptimeRobot replaces PagerDuty and Atlassian Statuspage (monitoring section and sub-processor table); remove "mandatory pull-request review", describe the CI checks actually in Task 13; secrets wording — database credentials rotated automatically, other platform secrets and Collector keys rotated on demand and on re-enrolment; addresses `app.` and `<customer>.app.`; alert routing and business-hours response for Standard.
- [ ] **Step 2:** `bash docs/product/diagrams/render.sh production-architecture`; rebuild the pre-requirements PDF with the scratchpad converter if still available.
- [ ] **Step 3: Verify** — `grep -rn -i "pagerduty\|statuspage\|pull-request review" docs/product/*.md` → no matches; `git status` shows the edits uncommitted.

### Task 18: Runbooks

**Files:**
- Create: `av-bridge-ops/runbooks/release-and-promote.md`, `restore-rehearsal.md`, `failover-simulation.md`, `full-stack-rebuild.md`, `laptop-release-fallback.md`

- [ ] **Step 1:** Write each as numbered steps with exact commands for this stack (instance ids, cluster/service names from Task 10 outputs), expected results, and where to record the outcome (date, measured RTO). Restore rehearsal restores the latest automated backup to a temporary instance in `avrmm-prod`, checks row counts on `customers`, `devices`, `telemetry_daily`, starts nothing against it, deletes it.
- [ ] **Step 2: Commit** — `"Runbooks: release/promote, restore, failover, rebuild, laptop fallback"`.

### Task 19: Go-live

- [ ] **Step 1 (Daniel):** Add the prod redirect URIs to the vendor Entra app registration (`https://app.involvecloud.com/api/v1/auth/entra/vendor/callback` and the customer callback the cloud logs at startup).
- [ ] **Step 2 (Daniel):** UptimeRobot paid plan: monitors (1 min) for `https://app.involvecloud.com`, `https://api.involvecloud.com/healthz`, and a Public API endpoint; status page on `status.involvecloud.com` (CNAME at DNS Made Easy as UptimeRobot instructs); alerts to Daniel's email and phone.
- [ ] **Step 3:** Vendor admin sign-in on prod; create the customer with its slug; confirm `https://<slug>.app.involvecloud.com` shows their branding.
- [ ] **Step 4: Smoke test** — enrol a test Collector against `api.involvecloud.com`; a device comes online; a live update appears on two browser sessions (refresh several times so both web copies serve); remote access to a device web page; a test alert email arrives from `noreply@notify.involvecloud.com`; stop one web task in ECS and confirm the portal keeps working.
- [ ] **Step 5:** Run the restore rehearsal and failover simulation runbooks once (failover: `aws rds reboot-db-instance --force-failover` off-peak) and record the measured times.
- [ ] **Step 6:** Daniel publishes the updated documents (Task 17) and books the penetration test.
