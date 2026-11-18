# Production environment — design

Date: 2026-11-18 · Status: draft for approval

## Why

Involve has signed its first production customer for M.A.R.C.U.S. Only UAT exists today. Production must deliver what the customer-facing documents promise (docs/product: Service Description & SLA, Business Continuity & DR, Security & Trust, Data Residency), because the customer's security team will read them. Where a promise can't honestly be met yet, the document is reworded before it goes to the customer.

- Customer tier: **Standard** — 99.5% monthly availability, UK business-hours support, P1 response within 1 business hour. No 24×7 paging.
- The customer is **new**: nothing moves from UAT. Production starts empty and the customer is onboarded there.
- Go-live: **next week** (week of 2026-11-23).

## Goals

- A separate production stack in AWS account `avrmm-prod` (568339496641), London region, built from the existing terraform modules.
- Meets the written promises: Multi-AZ database with automatic failover; 35-day point-in-time recovery (RPO 15 min, RTO 4 h); app containers spread across availability zones behind the load balancer; secrets in Secrets Manager; CloudWatch logs kept 30 days with alarms; all customer data, logs and backups in eu-west-2; TLS 1.2+ in transit, AES-256 at rest.
- External uptime checks, a public status page and alerting with UptimeRobot.
- Branded sign-in addresses per customer: `<customer>.app.involvecloud.com`.
- A release pipeline that tests, builds and signs once, deploys to UAT, and promotes the same build to production only when Daniel runs the promotion.
- Customer-facing documents updated to match what is actually built.

## Non-goals

- 24×7 on-call paging (Enterprise only; revisit when an Enterprise customer signs).
- Moving any data from UAT.
- Customer-owned sign-in domains (`marcus.customer.co.uk`); possible later on top of this design.
- Automated rotation of each Collector's HMAC key and of the data-encryption key (documents reworded instead; see Documents).
- The Multi-Region add-on, BYOK, WAF, autoscaling.
- Moving the repositories to the Involve GitHub organisation (free plan) before go-live; done afterwards.
- Making every app copy interchangeable (approach A).

## Anti-goals

- Any path by which UAT credentials, data or deploys can touch production.
- Production depending on Daniel's laptop to release or recover.
- Customer documents that say something production doesn't do.
- Customer data, logs or backups leaving the UK.
- DNS changes that affect Involve's own email or other services on `involvecloud.com`.

## Approaches considered

The app can't run as two copies today: live updates to browsers are fanned out in memory (internal/wsfanout, published from ingest), remote-access tunnels live in the memory of the copy the Collector connected to (internal/remote), and every copy starts every background job.

- **A. Every copy interchangeable.** Live updates through Postgres, jobs claim runs in the database, copies forward remote-access traffic to the copy holding the tunnel. Cleanest; most code.
- **B. Web service + worker service (chosen).** Same image, a role setting. Web: 2+ copies across zones (portal API, Public API, ingest, Collector commands and config, live updates). Worker: one copy running all background jobs and remote access, restarted by ECS in any zone if it fails. Only live updates must work across copies. The SLA counts portal, Public API and ingest availability, which web provides.
- **C. One copy now.** Fastest; contradicts the continuity document.

Daniel chose **B** (2026-11-18). AWS estimate ~$370–390/month (UAT ~$124), mostly the Multi-AZ database and a NAT gateway per zone.

## Design

### Accounts and DNS

- Production in `avrmm-prod`, eu-west-2, three AZs. CLI profile `avrmm-prod` (added 2026-11-18, same SSO session as UAT).
- Terraform: new `envs/prod` using the existing modules; state in S3 `avrmm-tf-state-prod` with lock table `avrmm-tf-lock-prod`, created by `bootstrap` with `prod.tfvars`.
- `involvecloud.com` stays at DNS Made Easy, managed by Daniel. Two subzones are delegated to Route 53 in `avrmm-prod` (NS records added by Daniel), so everything inside them is terraform-managed:
  - `app.involvecloud.com` — the portal at `app.involvecloud.com` and branded sign-in at `*.app.involvecloud.com` (wildcard), on Amplify with an AWS-issued certificate.
  - `access.involvecloud.com` — remote access sessions at `*.access.involvecloud.com` on the ALB.
- Records Daniel adds at DNS Made Easy: the two NS delegations; `api.involvecloud.com` (CNAME to the ALB) and its certificate validation CNAME; `status.involvecloud.com` (CNAME to UptimeRobot); the SES records for the sending subdomain (below). Terraform outputs the exact values.
- Hostnames customers see: `app.`, `<customer>.app.`, `api.`, `*.access.`, `status.involvecloud.com`. All covered by the documented allow-list `*.involvecloud.com` plus `*.access.involvecloud.com`.

### Branded sign-in addresses (code change)

- The portal's slug parsing already works for `acme.app.involvecloud.com` (first label; `app` is reserved).
- The cloud builds branded URLs by replacing `app` in the portal base URL (internal/publicapi/branding.go), which would give `acme.involvecloud.com`. Make the branded-address pattern configurable per environment (e.g. a suffix setting: UAT `uat.involvecloud.com`, prod `app.involvecloud.com`) and use it wherever branded URLs are built or shown.
- The portal shows `.uat.involvecloud.com` hard-coded in customers, helpdesk, branding settings and customer forms; read it from configuration instead.
- Add `status`, `access`, `notify`, `uat` and `portal` to the reserved slugs (portal and the server-side check that mirrors it).
- Sign-in from branded hosts (Entra redirects, password reset links) must work on `*.app.involvecloud.com`; the builder confirms the redirect flow and allowed-origin/CORS settings cover the new pattern.

### Network

- VPC across three AZs, public subnets (ALB, NAT) and private subnets (containers, database) per AZ; `single_nat_gateway = false`.
- ALB in public subnets, deletion protection on, TLS 1.2+ policy, HTTP → HTTPS.

### App: web and worker services

- One image, one ECS cluster, two ECS services selected by an environment variable (e.g. `MARCUS_ROLE=web|worker`; unset = both, which UAT keeps using as one copy).
- **Web**: 2 copies across AZs, 1 vCPU / 2 GB to start; serves everything except remote access; runs no background jobs.
- **Worker**: 1 copy, 0.5 vCPU / 1 GB to start; runs every background loop started in cmd/av-bridge-cloud/main.go (collector updates, telemetry rollup, retention, remote-access session sweeps, command sweeper, session cleaner, collector health watcher, nightly scheduler, nightly digest, ServiceNow ticket sync) and serves `GET /bridge/tunnel` and all `*.access.involvecloud.com` requests, via ALB listener rules to the worker's target group.
- **Live updates across web copies**: ingest publishes via `pg_notify`; each web copy listens and fans out to its own browser connections. Payloads over NOTIFY's 8000-byte limit carry a reference and are read back.
- **Tunnel state read by web**: any portal API handler that reads in-memory tunnel state (e.g. "is this Collector connected?") reads it from the database, written by the worker, or is routed to the worker.
- **Migrations** run once per deploy, not concurrently from two copies: the builder confirms the runner takes a database lock, or restricts migrations to one role.
- Deploys: web min healthy 100%, max 200%. The worker's brief restart pauses remote access and schedules for under a minute; scheduled runs are idempotent and catch up.

### Database

- RDS PostgreSQL, same major version as UAT, Multi-AZ, `db.t4g.medium` to start, gp3 with storage autoscaling, encrypted (AWS-managed KMS key), private subnets only.
- 35-day backup retention, deletion protection, final snapshot on delete, Performance Insights (free tier).
- A manual snapshot before every major release, kept 12 months (pipeline step).

### Secrets

- Secrets Manager holds the database master credential (RDS-managed, rotated automatically), cloud secret key, admin API token, portal token, HMAC secret, SMTP credentials and Entra client secret. No plaintext secrets in `prod.tfvars`.
- **Collector update signing key**: production uses the existing key (one key, so one signed build can be promoted). It moves from Daniel's laptop into the GitHub Actions secret store and is passed to the build only as a BuildKit secret. The laptop copy remains the documented fallback until the pipeline has done several releases.

### Email (SES)

- Sends from a subdomain, `notify.involvecloud.com` (e.g. `noreply@notify.involvecloud.com`, display name "M.A.R.C.U.S."), so DKIM/SPF/DMARC records never touch the `involvecloud.com` apex or Involve's own mail.
- Records (DKIM CNAMEs, MAIL FROM MX/SPF, DMARC for the subdomain) added by Daniel at DNS Made Easy from terraform outputs.
- Request SES production access in `avrmm-prod` on day one (AWS review takes about a day).

### Sign-in (Entra)

- Add production redirect URIs to the vendor Entra app registration (multi-tenant), or use a separate production registration; the builder chooses and records why.
- `ALLOW_MOCK_JWT` off; `NIGHTLY_DRY_RUN=false`; dev sign-in presets unavailable.

### Portal

- Amplify app in `avrmm-prod` on the `app.involvecloud.com` zone (apex + wildcard), building a `release` branch with production API settings. Auto-build stays on for `release`, but only the promotion workflow moves `release` forward, so the portal reaches production together with its matching API.

### Monitoring, status and alerting

- **UptimeRobot** (paid plan; free is non-commercial): 1-minute HTTPS checks of `app.`, `api.` (health endpoint) and the Public API from several locations, as the availability record; status page on `status.involvecloud.com`.
- **CloudWatch**: logs for both services kept 30 days; alarms on ALB 5xx rate, no healthy web targets, worker not running, ECS task restarts, RDS CPU/storage/memory/connections and failover events. Alarms go to an SNS topic.
- Alert recipient for now: Daniel's mailbox, plus SMS/voice to Daniel's phone in UptimeRobot. Addresses and numbers live in UptimeRobot and in terraform variables, never in the repo.

### Release pipeline (GitHub Actions)

- Stays in the current repository for go-live; moves to the Involve GitHub organisation (free plan) afterwards. Go module paths do not change.
- On every push: Go tests (Collector and cloud, including the DB integration tests), portal type-check and build, gosec, staticcheck, Dependabot, and GitHub secret scanning / push protection.
- On a release tag: build the cloud image once (signing key as BuildKit secret), check the manifest has 3 signatures, push to UAT's ECR, deploy UAT.
- **Promote to production**: a separate workflow run manually by Daniel (workflow_dispatch) with a version that is already in UAT. It snapshots the production database, copies the image to prod's ECR, deploys worker and web, waits for stability and health checks, and moves the `release` branch to that commit. (Required reviewers on environments aren't available for private repos on the free plan; the manual trigger is the gate.)
- AWS access by GitHub OIDC roles in each account (terraform), no long-lived keys.

### Operations

- Nightly mirror of `main` to a location independent of GitHub (the builder picks, e.g. a git bundle to an S3 bucket in `avrmm-prod`).
- Runbooks: restore rehearsal (quarterly), failover simulation (quarterly), full-stack rebuild (semi-annual). First restore and failover rehearsals before go-live if time allows, otherwise in the week after.
- Annual third-party penetration test: Involve commissions the first one (business action).

### Documents to update (working copy only, Daniel publishes)

- Security & Trust: monitoring and status page (UptimeRobot; remove PagerDuty and Atlassian Statuspage from the sub-processor list); development practices — drop "mandatory pull-request review", describe the automated checks; secrets — database credentials rotated automatically, other platform secrets and Collector keys rotated on demand and on re-enrolment.
- Business Continuity & DR: alarm routing and business-hours response for Standard tier; manual snapshots before major releases (kept as written).
- Data Residency: sub-processor mention.
- Service Description & SLA: status page provider wording if named; addresses (`app.`, branded `<customer>.app.`).
- Customer pre-requirements and deployment guide: production hostnames.

## Constraints

- Terraform apply to either account only with Daniel's explicit OK.
- `docs/product/**` edited in the working copy, never committed.
- The signing key only ever passed as a BuildKit secret.
- Collectors in the field stay on UAT; production Collectors enrol fresh against `api.involvecloud.com`.
- Go-live next week: the critical path is infrastructure, the web/worker and branded-address code changes, SES production access, DNS records, and the promotion workflow. Rehearsals, the repository move and the pen test can follow go-live.

## Left to the builder

- Container sizes beyond the starting points; names of services, target groups, roles and alarms.
- How each in-memory tunnel read is replaced.
- Separate vs shared Entra registration.
- Where the nightly code mirror lives.
- The exact configuration setting names for the branded-address pattern.
