---
title: AV Bridge — Security & Trust
description: How AV Bridge protects customer data, isolates tenants, authenticates users and services, and manages the security lifecycle. Written for information security, procurement, and technical evaluators.
audience: Customer InfoSec, procurement, technical evaluators
status: Draft v0.2 · needs security lead + legal sign-off on items marked [TBC]
---

# AV Bridge — Security & Trust

Involve Cloud is responsible for the security of the AV Bridge platform. This document describes the security architecture, controls, and processes in place today, and the roadmap for the controls we are actively building.

Every statement below should be independently verifiable — either by inspection of the platform, by the practices detailed here, or by a control evidence request. Where a control is aspirational rather than shipped, it is marked as such.

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

AV Bridge separates three tiers, each with its own trust boundary.

| Tier | Location | Trust boundary |
|---|---|---|
| **AV devices** | Customer LAN / AV VLAN | Never contacts the internet directly. Only the Collector talks to it. |
| **Collector** | Customer network (site or shared) | Initiates outbound HTTPS only. Holds a per-device HMAC key and no user credentials. |
| **Involve Cloud** | UK / EU AWS regions [TBC — confirm regions] | Terminates every customer connection at the edge. Multi-tenant, row-level isolated. |

The Collector-to-Cloud connection is always **initiated from the customer side**. The cloud never dials in to any customer network. There are no inbound firewall rules to open at any customer location.

## 2. Collector-to-Cloud communication

This section describes the mechanism that makes the "outbound HTTPS only" claim possible in practice, and how portal-issued commands still reach devices in under a second without an inbound firewall rule.

### 2.1 The outbound-only principle

TCP sessions are bidirectional once established. When the customer's firewall permits the Collector to open an outbound HTTPS session to `*.involvecloud.com` on TCP 443, both sides may then exchange data on that session — but the *initiation* (the SYN packet, the moment the firewall makes its allow / deny decision) came from inside the customer network.

This is the same pattern used by every mainstream SaaS agent — Datadog, CrowdStrike, Cloudflare Tunnel, GitHub self-hosted runners, and equivalents. AV Bridge does not require, and does not use, any inbound port or callback socket at the customer site.

### 2.2 The outbound request surface

The Collector makes exactly three types of outbound POST to Involve Cloud, each authenticated end-to-end.

| Endpoint | Purpose | Direction of application data |
|---|---|---|
| `POST /bridge/poll` | Claim any commands queued for this Collector; long-polls when the queue is empty. | Cloud → Collector (in the response body) |
| `POST /bridge/commands/{id}/result` | Report the outcome of a claimed command. | Collector → Cloud |
| `POST` / `PUT /bridge/config` | Fetch and confirm the desired device configuration set for this Collector. | Both |

A telemetry push endpoint (used to carry periodic device metrics and events to the cloud) operates under the same posture. All are Collector-initiated, over TLS 1.2+ on TCP 443, and every request carries an `X-Signature: sha256=<hex>` header computed from the Collector's per-instance HMAC-SHA256 key. The cloud rejects any request whose signature does not verify.

### 2.3 Long-polling — how commands reach devices in near real time

The natural question is *"if the cloud cannot initiate a connection, how does a portal-issued command reach the device promptly?"* The answer is **long-polling**, which trades a briefly-held TCP connection for the ability to deliver commands within tens of milliseconds — with no reduction to the outbound-only firewall posture.

Mechanism:

1. The Collector sends `POST /bridge/poll`. If any commands are queued for this Collector, the cloud returns them immediately.
2. If none are queued, the cloud **holds the request open**, subscribed to a PostgreSQL `LISTEN` channel (`cmd_pending`) scoped to that Collector. The hold is bounded to **25 seconds**.
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

### 2.5 End-to-end round-trip

For a portal operator issuing, for example, a "reboot codec" command:

1. **t=0 ms** — Portal calls the cloud command-dispatch endpoint. The command row is inserted in a database transaction; the same transaction fires `NOTIFY cmd_pending`.
2. **t=1–5 ms** — The already-parked `/bridge/poll` request wakes, reads the new row, and closes the response body.
3. **t=~50 ms** — The Collector receives the command over its open HTTPS session.
4. **t=~50 ms** — The Collector dispatches through its local hub to the appropriate vendor adapter.
5. **t=~150 ms** — The adapter communicates with the device on the local network, executes the command, and receives a response.
6. **t=~200 ms** — The Collector issues `POST /bridge/commands/{id}/result` on a new outbound request, reporting the outcome.
7. **t=~250 ms** — The cloud streams the result back to the portal user.

Actual timings vary with device response time. Sub-second is the typical outcome; the platform itself contributes tens of milliseconds.

### 2.6 Why this is safe for the customer firewall

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
- Local (non-federated) accounts are supported for evaluation and out-of-band recovery. Local passwords are hashed with **[TBC — bcrypt cost factor / argon2 parameters]**.
- Role-based access control gates every portal action. Roles include: **[TBC — final role catalogue]** — typically owner, admin, operator, viewer, integrator.

### Machine and integration access

- **Public API tokens** are minted from the portal in the form `avb_<prefix>_<secret>`. The full secret is shown once at creation and stored only as a **SHA-256 hash** in the database.
- Tokens are **scoped** (per-endpoint permission set), **expiring** (max lifetime enforced), and **revocable** (single click in the portal).

### Collector authentication

- Each Collector is enrolled via a one-time bootstrap flow originating in the portal. Enrolment issues a **device-scoped HMAC-SHA256 key** unique to that Collector.
- Every Collector-to-Cloud request is signed with that key. Signature failures result in immediate 401 responses; keys can be rotated or revoked from the portal.
- Compromise of one Collector's key affects only that Collector.

## 4. Tenant isolation

Multi-tenant isolation is enforced at the **database row level**, not at the application layer.

- Every tenant-scoped table carries a `tenant_id` column.
- **PostgreSQL Row-Level Security (RLS)** policies restrict every session to rows matching its bound `tenant_id`. Enforcement is by the database engine — an application-layer bug cannot cross the boundary.
- Application code sets the session tenant on every request; RLS enforces it.
- Shared services (alert engine, ingest, scheduler) run under the same policy — no privileged bypass path exists in production code.

## 5. Data protection in transit

- **TLS 1.2 minimum** on every connection to Involve Cloud (portal, Public API, Collector channel). TLS 1.3 preferred where the client supports it.
- Cipher suites follow AWS-managed load-balancer policy [TBC — cite policy name].
- The Collector performs standard TLS certificate validation against the cloud endpoint before initiating any request.
- All Collector requests carry an HMAC-SHA256 signature in addition to the TLS channel, providing message-level authenticity independent of the transport.

## 6. Data protection at rest

- Application data is stored in **Amazon RDS for PostgreSQL** with **AES-256 encryption at rest**, using AWS-managed KMS keys [TBC — customer-managed key option if required].
- Automated database snapshots are encrypted using the same key material.
- Object storage (attachments, log archives) [TBC — confirm scope] is stored in **Amazon S3 with server-side encryption (SSE-S3 / SSE-KMS)**.
- Secrets used by cloud services (database credentials, external API keys) are stored in **AWS Secrets Manager** and rotated according to [TBC — rotation cadence].

## 7. Application security

### Software development lifecycle

- Source code is managed in [TBC — GitHub / other] with mandatory pull-request review for all production code.
- Automated tests run on every pull request; production releases are cut from `main` after test suite passes.
- Dependency vulnerability scanning: [TBC — Dependabot / Snyk / other, cadence].
- Static analysis: `go vet`, `staticcheck` [TBC — extend list].
- Secret scanning: [TBC — gitleaks / equivalent].

### Third-party dependencies

- Every dependency is pinned by version and checksum (`go.sum`).
- Update policy: [TBC — patch cadence].

### Web application

- Portal is built on [TBC — Next.js 14, React 18] with Content-Security-Policy, X-Frame-Options, and X-Content-Type-Options headers set at the edge.
- CSRF protection [TBC — describe mechanism].
- Public API is CORS-permissive to enable browser-based tooling (Swagger, integrators) but requires bearer-token auth for any data access.

### API security

- Every Public API request is authenticated. Anonymous access is limited to the OpenAPI spec and the Swagger UI.
- Pagination is cursor-based (base64-encoded opaque tokens) — no leaky offset counters.
- Rate limiting: [TBC — configured limits].

## 8. Infrastructure security

- The platform runs on **Amazon Web Services** in the **UK / EU** regions [TBC — confirm regions].
- Compute runs on **Amazon ECS Fargate**. No shared operating systems with other customers.
- Public entry is via **Amazon Application Load Balancer** with TLS termination. Backend services do not accept internet traffic directly.
- Database is **Amazon RDS for PostgreSQL** in private subnets. No public endpoint.
- Network segmentation is enforced by **AWS VPC security groups**; least privilege between tiers.
- Infrastructure is provisioned via **Terraform**; every change is reviewed and versioned.

## 9. Logging, monitoring, and audit

### Audit log

- Every security-relevant action performed by users, tokens, and Collectors is written to a dedicated `audit_log` table.
- Audit records include actor, action, target, and timestamp.
- Audit records are tenant-scoped and readable only to that tenant's authorised roles.
- Retention: [TBC — set policy — recommend 12 months minimum].

### Operational logging

- Application logs are shipped to **Amazon CloudWatch Logs** with a retention of [TBC — set policy].
- Logs are tenant-tagged where they cross the tenant boundary, so operational triage does not require reading raw tenant data.

### Monitoring

- Availability and error-rate alarms fire to the on-call rota via [TBC — PagerDuty / Opsgenie / equivalent].
- Public status page: [TBC — URL] — records planned maintenance and post-incident summaries.

## 10. Vulnerability and incident management

### Vulnerability management

- **Dependency vulnerabilities:** [TBC — tool + cadence — recommend Dependabot with weekly review].
- **Container images:** scanned at build time [TBC — tool].
- **Penetration testing:** [TBC — cadence and provider].
- **Coordinated disclosure:** we welcome reports at **[TBC — security@involve.vc or similar]**.

### Incident response

- On-call rota available 24×7 for platform incidents [TBC — confirm coverage].
- Customer-impacting incidents are communicated via [TBC — status page + email].
- Post-incident review is published within [TBC — commitment] of a customer-impacting outage.

## 11. Compliance and certification

Current position:

| Certification | Status |
|---|---|
| SOC 2 Type II | [TBC — planned / in progress / date target] |
| ISO 27001 | [TBC — planned / in progress / date target] |
| Cyber Essentials Plus | [TBC — held / planned] |
| GDPR (UK & EU) | Compliant — see *Data Residency & Retention* |

Contractual controls (SLA, DPA, sub-processor list) are documented separately — see *[TBC — link to legal / trust page]*.

## 12. Sub-processors

The current list of sub-processors is published at **[TBC — public URL]**. Categories in use today:

| Category | Provider | Region | Purpose |
|---|---|---|---|
| Cloud infrastructure | Amazon Web Services | UK / EU [TBC] | Compute, database, storage |
| Portal hosting | AWS Amplify | UK / EU [TBC] | Static hosting + CDN for the portal |
| Identity | Microsoft Entra ID | Customer-controlled | Federated SSO |
| Transactional email | [TBC] | [TBC] | Notifications, invitations, password reset |
| Observability | [TBC] | [TBC] | Logs, metrics, alerting |
| Payment processing | [TBC or "not applicable"] | [TBC] | Subscription billing |

Sub-processor changes are notified in advance via [TBC — mechanism — often email + status page].

## 13. Contact

- Security disclosures and questions: **[TBC — security@involve.vc]**
- Privacy and data protection: **[TBC — privacy@involve.vc or DPO details]**
- Sales and evaluation: **[TBC — sales@involve.vc]**

---

*Involve Cloud · AV Bridge Platform · Security & Trust v0.2 · [YEAR]*
