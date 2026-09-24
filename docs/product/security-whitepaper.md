---
title: M.A.R.C.U.S. — Security & Trust
product: M.A.R.C.U.S.
vendor: Involve Visual Collaboration Ltd
website: https://involve.vc
version: 1.0
status: General Availability
audience: Customer InfoSec, procurement, technical evaluators
---

<!-- ============================================================
  M.A.R.C.U.S.
  Managed · Assets · Resources · Control · Updates · Status
  Involve Visual Collaboration Ltd · involve.vc
============================================================ -->

# M.A.R.C.U.S. — Security & Trust

**Managed · Assets · Resources · Control · Updates · Status**

Involve Visual Collaboration Ltd is responsible for the security of the M.A.R.C.U.S. platform. This document describes the security architecture, controls, and processes in place today.

Every statement below is verifiable — either by inspection of the platform, by the practices detailed here, or by a control evidence request via security@involve.vc.

## Contents

1. Architecture and trust boundaries
2. Collector-to-Cloud communication
3. Identity and access
4. Tenant isolation
5. Data protection in transit
6. Data protection at rest
7. Application security
8. Infrastructure security
9. Logging, monitoring, and audit
10. Vulnerability and incident management
11. Compliance and certification
12. Sub-processors
13. Contact

---

## 1. Architecture and trust boundaries

M.A.R.C.U.S. separates three tiers, each with its own trust boundary.

| Tier | Location | Trust boundary |
|---|---|---|
| **AV devices** | Customer LAN / AV VLAN | Never contact the internet directly. Only the M.A.R.C.U.S. Collector talks to them. |
| **M.A.R.C.U.S. Collector** | Customer network (site or shared) | Initiates outbound HTTPS only. Holds a per-Collector HMAC key and no user credentials. |
| **M.A.R.C.U.S. Cloud** | AWS London region (`eu-west-2`) | Terminates every customer connection at the edge. Multi-tenant, row-level isolated. |

The Collector-to-Cloud connection is always **initiated from the customer side**. M.A.R.C.U.S. Cloud never dials in to any customer network. There are no inbound firewall rules to open at any customer location.

## 2. Collector-to-Cloud communication

This section describes the mechanism that makes the "outbound HTTPS only" claim possible in practice, and how portal-issued commands still reach devices in under a second without an inbound firewall rule.

### 2.1 The outbound-only principle

TCP sessions are bidirectional once established. When the customer's firewall permits the Collector to open an outbound HTTPS session to `*.involvecloud.com` on TCP 443, both sides may then exchange data on that session — but the *initiation* (the SYN packet, the moment the firewall makes its allow / deny decision) came from inside the customer network.

This is the same pattern used by every mainstream SaaS agent — Datadog, CrowdStrike, Cloudflare Tunnel, GitHub self-hosted runners, and equivalents. M.A.R.C.U.S. does not require, and does not use, any inbound port or callback socket at the customer site.

### 2.2 The outbound request surface

The M.A.R.C.U.S. Collector makes exactly three types of outbound POST to M.A.R.C.U.S. Cloud, each authenticated end-to-end.

| Endpoint | Purpose | Direction of application data |
|---|---|---|
| `POST /bridge/poll` | Claim any commands queued for this Collector; long-polls when the queue is empty. | Cloud → Collector (in the response body) |
| `POST /bridge/commands/{id}/result` | Report the outcome of a claimed command. | Collector → Cloud |
| `POST` / `PUT /bridge/config` | Fetch and confirm the desired device configuration set for this Collector. | Both |

A telemetry push endpoint (used to carry periodic device metrics and events to the cloud) operates under the same posture. All are Collector-initiated, over TLS 1.2 minimum on TCP 443, and every request carries an `X-Signature: sha256=<hex>` header computed from the Collector's per-instance HMAC-SHA256 key. M.A.R.C.U.S. Cloud rejects any request whose signature does not verify.

### 2.3 Long-polling — how commands reach devices in near real time

The natural question is *"if the cloud cannot initiate a connection, how does a portal-issued command reach the device promptly?"* The answer is **long-polling**, which trades a briefly-held TCP connection for the ability to deliver commands within tens of milliseconds — with no reduction to the outbound-only firewall posture.

Mechanism:

1. The Collector sends `POST /bridge/poll`. If any commands are queued for this Collector, the cloud returns them immediately.
2. If none are queued, the cloud **holds the request open**, subscribed to a PostgreSQL `LISTEN` channel (`cmd_pending`) scoped to that Collector. The hold is bounded to 25 seconds.
3. When any authoritative source — a portal user action, a scheduled routine, an API-issued command — inserts a new command row for that Collector, the same database transaction fires `NOTIFY cmd_pending`. The waiting `/bridge/poll` request wakes and returns the command to the Collector on the still-open connection.
4. If no command arrives within the hold window, the cloud returns an empty response and the Collector immediately re-issues the poll. The Collector is therefore always holding one parked request to the cloud, ready to receive commands.

The result is that portal-to-device command latency is typically **under one second end-to-end**, without any inbound firewall rule at the customer site.

### 2.4 Timing choreography

The long-poll timings are chosen to satisfy every intermediate device on the connection path:

| Timer | Value | Reason |
|---|---|---|
| Cloud server maximum hold | 25 seconds | Bounded well below the load-balancer idle timeout. |
| AWS load-balancer idle timeout | 60 seconds | Never trips a healthy long-poll. |
| Collector HTTP client timeout | 35 seconds | Comfortably longer than the server hold; a healthy poll never trips it. |

A transport-level failure (connection reset, DNS failure, TLS error) causes the Collector to pause for a small back-off interval before retrying, preventing a wedged upstream from being retried in a tight loop.

### 2.5 Why this is safe for the customer firewall

Assurances that hold for the outbound-only model:

- **No listening service on the Collector host.** Nothing accepts connections from the internet at the customer site. There is no port to fingerprint, and no attack surface accessible from outside the customer network.
- **Destination pinning is available.** The Collector's outbound destination is `*.involvecloud.com` on TCP 443; on request, it can be pinned to a specific regional hostname or IP set for stricter allow-lists.
- **Commands are only accepted from the authenticated cloud.** A command arrives *inside the response* to a request the Collector itself signed. There is no other input path.
- **Message-level authenticity independent of transport.** Every request and response is bound to the Collector's HMAC-SHA256 key; a middlebox that terminated TLS would be unable to inject an accepted response.
- **Blast radius is a single Collector.** Compromise of one Collector's HMAC key affects only that Collector; keys can be rotated or revoked from the portal without any customer-side firewall change.
- **The failure mode is limited to loss of function.** If the outbound connection is blocked, commands do not arrive. There is no failure mode in which "commands do not arrive" becomes "an attacker reaches devices at the customer site".

## 3. Identity and access

### End users (portal)

- Authentication is via **Microsoft Entra ID** (OpenID Connect). The platform does not store passwords for federated users.
- The Entra ID application is **multi-tenant**; each customer consents its own tenant and controls its own users, MFA policy, conditional access, and revocation.
- Local (non-federated) accounts are supported for evaluation and out-of-band recovery. Local passwords are hashed with **bcrypt** (cost factor 12).
- Role-based access control gates every portal action. See *M.A.R.C.U.S. — Datasheet* for the full role and permission catalogue.

### Machine and integration access

- **Public API tokens** are minted from the portal in the form `avb_<prefix>_<secret>`. The full secret is shown once at creation and stored only as a **SHA-256 hash** in the database.
- Tokens are **scoped** (per-endpoint permission set), **expiring** (maximum 12-month lifetime), and **revocable** (single click in the portal).

### Collector authentication

- Each M.A.R.C.U.S. Collector is enrolled via a one-time bootstrap flow originating in the portal. Enrolment issues a **Collector-scoped HMAC-SHA256 key** unique to that Collector.
- Every Collector-to-Cloud request is signed with that key. Signature failures result in immediate 401 responses; keys can be rotated or revoked from the portal.
- Compromise of one Collector's key affects only that Collector.

## 4. Tenant isolation

Multi-tenant isolation is enforced at the **database row level**, not at the application layer.

- Every tenant-scoped table carries a `customer_id` column.
- **PostgreSQL Row-Level Security (RLS)** policies restrict every session to rows matching its bound `customer_id`. Enforcement is by the database engine — an application-layer bug cannot cross the boundary.
- Application code sets the session tenant on every request; RLS enforces it.
- Shared services (alert engine, ingest, scheduler) run under the same policy — no privileged bypass path exists in production code.
- An orthogonal building-scope and business-unit-scope layer restricts users to a subset of a single tenant's rooms and BUs, also enforced by RLS RESTRICTIVE policies.

## 5. Data protection in transit

- **TLS 1.2 minimum** on every connection to M.A.R.C.U.S. Cloud (portal, Public API, Collector channel). TLS 1.3 preferred where the client supports it.
- Cipher suites follow the AWS Application Load Balancer `ELBSecurityPolicy-TLS13-1-2-2021-06` policy — modern suites only, no legacy support.
- The Collector performs standard TLS certificate validation against the cloud endpoint before initiating any request.
- All Collector requests carry an HMAC-SHA256 signature in addition to the TLS channel, providing message-level authenticity independent of the transport.

## 6. Data protection at rest

- Application data is stored in **Amazon RDS for PostgreSQL** with **AES-256 encryption at rest**, using AWS-managed KMS keys. Customer-managed KMS keys are available on request for Enterprise deployments.
- Automated database snapshots are encrypted using the same key material and retained for 35 days.
- Object storage (asset attachments, log archives) is stored in **Amazon S3 with server-side encryption (SSE-KMS)**.
- Secrets used by cloud services (database credentials, HMAC keys, external API keys) are stored in **AWS Secrets Manager** and rotated on a 90-day cadence.

## 7. Application security

### Software development lifecycle

- Source code is managed in GitHub with mandatory pull-request review for all production code.
- Automated tests run on every pull request; production releases are cut from `main` after test suite passes.
- Dependency vulnerability scanning: **GitHub Dependabot** with weekly review and immediate action on Critical / High CVEs.
- Static analysis: `go vet`, `staticcheck`, `gosec` on the Go modules; `eslint` + `typescript-strict` on the portal.
- Secret scanning: **GitHub push protection** and repository secret scanning enabled.

### Third-party dependencies

- Every dependency is pinned by version and checksum (`go.sum`, `package-lock.json`).
- Update policy: Critical / High CVEs patched within 5 business days; Medium within 30 days; routine dependency refresh monthly.
- See *M.A.R.C.U.S. — Software Bill of Materials* for the current dependency inventory.

### Web application

- Portal is built on **Next.js 14** (App Router) with **Content-Security-Policy**, **X-Frame-Options: DENY**, **X-Content-Type-Options: nosniff**, and **Referrer-Policy** headers set at the edge.
- CSRF protection via session cookies scoped `SameSite=Lax` combined with an `Origin` header check on state-changing requests.
- Public API is bearer-token only; there is no session-cookie surface on the API host.

### API security

- Every Public API request is authenticated. Anonymous access is limited to the OpenAPI specification document and the Swagger UI.
- Pagination is cursor-based (base64-encoded opaque tokens) — no leaky offset counters.
- Rate limiting: **1,000 requests per minute per token** by default; higher limits available for high-volume integrations on request.

## 8. Infrastructure security

- The platform runs on **Amazon Web Services** in the **AWS London region (`eu-west-2`)**.
- Compute runs on **Amazon ECS Fargate**. No shared operating systems with other customers.
- Public entry is via **Amazon Application Load Balancer** with TLS termination. Backend services do not accept internet traffic directly.
- Database is **Amazon RDS for PostgreSQL** in private subnets. No public endpoint.
- Network segmentation is enforced by **AWS VPC security groups**; least privilege between tiers.
- Infrastructure is provisioned via **Terraform**; every change is reviewed and versioned in source control.

## 9. Logging, monitoring, and audit

### Audit log

- Every security-relevant action performed by users, tokens, and Collectors is written to a dedicated `audit_log` table.
- Audit records include actor, action, target, before / after payloads (where applicable), and timestamp.
- Audit records are tenant-scoped and readable only to that tenant's authorised roles.
- Retention: 24 months.

### Operational logging

- Application logs are shipped to **Amazon CloudWatch Logs** with a retention of 90 days.
- Logs are tenant-tagged where they cross the tenant boundary, so operational triage does not require reading raw tenant data.

### Monitoring

- Availability and error-rate alarms fire to the on-call rota via **PagerDuty**.
- Public status page: [status.involvecloud.com](https://status.involvecloud.com) — records planned maintenance, current incidents, and post-incident summaries.

## 10. Vulnerability and incident management

### Vulnerability management

- **Dependency vulnerabilities:** GitHub Dependabot alerts, reviewed weekly. Critical / High CVEs patched within 5 business days.
- **Container images:** scanned at build time via Amazon ECR image scanning (Enhanced) with automatic block on Critical CVEs.
- **Penetration testing:** annual third-party penetration test against production; internal grey-box test every 6 months.
- **Coordinated disclosure:** we welcome reports at **security@involve.vc**. Response within 2 business days; remediation timelines communicated in the first response.

### Incident response

- On-call rota is available 24×7 for platform-severity incidents (P1 — service down).
- Customer-impacting incidents are communicated via the status page ([status.involvecloud.com](https://status.involvecloud.com)) and by direct email to the primary tenant admin.
- Post-incident review is published to the status page within 5 business days of a customer-impacting outage.

See *M.A.R.C.U.S. — Service Description & SLA* for severity definitions and response commitments.

## 11. Compliance and certification

| Certification | Status |
|---|---|
| Cyber Essentials Plus | Held (annual renewal) |
| ISO 27001 | In progress — target certification within 12 months |
| SOC 2 Type II | Planned |
| UK GDPR / EU GDPR | Compliant — Data Processing Agreement available on request |

Contractual controls (SLA, DPA, sub-processor list) are documented separately — see *M.A.R.C.U.S. — Service Description & SLA* and [involve.vc/trust](https://involve.vc).

## 12. Sub-processors

The current list of sub-processors is published at **[involve.vc/trust/sub-processors](https://involve.vc)**.

| Category | Provider | Region | Purpose |
|---|---|---|---|
| Cloud infrastructure | Amazon Web Services | UK (`eu-west-2`) | Compute, database, storage, networking |
| Portal hosting | AWS Amplify | UK (`eu-west-2`) | Static hosting + CDN for the portal |
| Identity (federated) | Microsoft Entra ID | Customer-controlled | Federated SSO (customer's own tenant) |
| Transactional email | Amazon SES | UK (`eu-west-2`) | Notification and password-reset delivery |
| Status page | Statuspage (Atlassian) | EU | Public status and incident communication |
| Alerting (internal) | PagerDuty | EU | On-call rota and incident escalation (internal Involve use only) |

Sub-processor changes are notified in advance via email to the primary tenant admin and via the status page.

## 13. Contact

- **Security disclosures and questions:** security@involve.vc
- **Privacy and data protection:** privacy@involve.vc
- **Commercial enquiries:** commercial@involve.vc
- **Support:** support@involve.vc

---

*Involve Visual Collaboration Ltd · M.A.R.C.U.S. Platform · Security & Trust · v1.0*
*[involve.vc](https://involve.vc)*
