---
title: M.A.R.C.U.S. — Datasheet
product: M.A.R.C.U.S.
vendor: Involve Visual Collaboration Ltd
website: https://involve.vc
version: 1.0
status: General Availability
audience: Technical evaluators, buyers, and integrators
---

<!-- ============================================================
  M.A.R.C.U.S.
  Managed · Assets · Resources · Control · Updates · Status
  Involve Visual Collaboration Ltd · involve.vc
============================================================ -->

# M.A.R.C.U.S. — Datasheet

**Managed · Assets · Resources · Control · Updates · Status**

M.A.R.C.U.S. is a UK-hosted, cloud-managed monitoring, control, and automation platform for audio-visual and video collaboration estates. This document lists what it does, what it runs on, what it integrates with, and what you need to deploy it.

> **UK-hosted exclusively.** M.A.R.C.U.S. Cloud runs in the AWS London region (`eu-west-2`). Every byte of customer data — configuration, telemetry, audit, backups — is stored, processed, and recovered within the United Kingdom. No customer data ever transits to non-UK regions.

## Product summary

| | |
|---|---|
| **Product** | M.A.R.C.U.S. Platform |
| **Vendor** | Involve Visual Collaboration Ltd |
| **Deployment model** | SaaS (multi-tenant), **UK-hosted exclusively** (AWS London `eu-west-2`) |
| **Cloud component** | M.A.R.C.U.S. Cloud — operated by Involve |
| **On-premise component** | M.A.R.C.U.S. Collector — one per site (or shared across sites) |
| **Target customers** | Corporate AV, higher education, MSPs, government, facilities & IT |
| **Product baseline** | v1.0 — General Availability |
| **Release cadence** | Monthly minor releases; quarterly major releases; security patches out-of-band |

## Core capabilities

- **Live device monitoring** across every room and site, vendor-agnostic.
- **Fleet-wide command dispatch** with sub-second portal-to-device round-trip.
- **Alerts and escalation** with configurable thresholds and flap suppression, delivered via email, Microsoft Teams, or webhooks.
- **Nightly and scheduled routines** — health checks, power sequences, config sync, room-open flows.
- **Warranty, utilisation, and power reporting** — first-class asset lifecycle without a separate CMDB.
- **Public REST API v1** for downstream ITSM, CMDB, BI, and dashboards.
- **Multi-tenant, row-level isolation** for MSPs and multi-BU enterprises.
- **Flexible access model** — per-tenant role catalogue, multi-role users, building- and business-unit-level physical scope.
- **Microsoft Entra ID single sign-on** with Entra-group-to-role mapping.
- **White-label-capable** — per-tenant display name, logo, accent colour, sign-in message and custom subdomain out of the box.

## Supported devices and vendors

The full compatibility matrix (per-model, per-firmware) is maintained separately in *M.A.R.C.U.S. — Supported Devices & Compatibility Matrix*. Summary below.

### Vendor-native adapters

| Adapter | Vendor | Devices |
|---|---|---|
| Poly VideoOS | Poly / HP | G7500, Studio X70 / X52 / X50 / X30 |
| Sony Bravia Professional | Sony | Bravia Professional Displays (JSON-RPC + PSK) |
| Biamp Tesira | Biamp | Tesira DSPs (TTP over Telnet, subscription-based metrics, DEVICE-level identity + faults) |
| Aurora RXT | Aurora Multimedia | RXT-x wall-mount touch panels |
| Aurora VPX | Aurora Multimedia | VPX-series AV-over-IP encoders / decoders |
| ATEN eco PDU | ATEN | PE6108G and siblings (per-outlet control + metering) |
| VISCA-over-IP | Sony / Panasonic / PTZOptics / HuddleCam / Marshall / Lumens | PTZ cameras speaking the Sony VISCA standard |

### Generic transport adapters

| Adapter | Use |
|---|---|
| Generic REST | Any device with an HTTP / JSON control API |
| Generic WebSocket | Any device with a WebSocket control channel |
| Generic Telnet | CLI-driven control processors, matrix switchers, older displays |
| Generic Serial (RS-232) | Direct RS-232 attached to the Collector host |
| ICMP Ping | Reachability + latency for devices with no vendor API |

### Adapter roadmap

Additional vendor adapters are added in most releases. Prioritisation is driven by customer requests — contact commercial@involve.vc.

## Architecture at a glance

```
[ Room devices ] ── LAN ──► [ M.A.R.C.U.S. Collector ] ── HTTPS 443 ──► [ M.A.R.C.U.S. Cloud ] ──► [ Portal · Public API · Alerts ]
```

- **M.A.R.C.U.S. Collector** — small Linux or Windows service on your network. Speaks native vendor protocols locally; makes one outbound HTTPS connection to the cloud.
- **M.A.R.C.U.S. Cloud** — multi-tenant platform hosted in AWS London (`eu-west-2`). Runs the portal, ingest pipeline, alert engine, and Public API.
- **Portal** — modern web application. Microsoft Entra ID SSO. Role-based access.
- **Public API** — versioned REST at `/pub/v1`. OpenAPI 3.1 specification + Swagger UI. Bearer-token authentication.

Full architecture and network flow diagrams are available separately (see *M.A.R.C.U.S. — Network & Firewall Requirements*).

## Deployment options

| Option | When to use |
|---|---|
| **Per-site Collector** | Preferred. Sites without reliable WAN reach between them; large sites; sites needing local resilience. |
| **Shared central Collector** | Suitable when a corporate WAN (MPLS, SD-WAN, site-to-site VPN) provides reliable RFC1918 reach into every site's AV VLAN. |
| **Hybrid** | Common in practice — one Collector per major site, plus a central Collector serving smaller regional offices. |

## Sizing guidance

Per Collector, on the recommended host specification:

| Host | Comfortable | Stretch (relaxed poll rates) |
|---|---|---|
| 2 vCPU · 4 GB RAM | 250 – 500 devices | up to ~1,000 devices |
| 4 vCPU · 8 GB RAM | 500 – 1,000 devices | up to ~2,000 devices |
| Heavy-adapter mix (many session-based VC codecs) | ~60% of the above | ~75% of the above |

Numbers depend on poll rate, WAN latency, and adapter mix. Firm sizing for a specific fleet is available on request via load-test against the target host — contact commercial@involve.vc.

## Collector host requirements

| | Linux | Windows |
|---|---|---|
| OS | Ubuntu 20.04 LTS+, RHEL 8+, Debian 11+ | Windows Server 2019, 2022 |
| CPU | 2 vCPU minimum, 4 vCPU recommended | Same |
| RAM | 4 GB minimum, 8 GB recommended | Same |
| Disk | 10 GB for logs and local state | Same |
| Network | Outbound HTTPS (TCP 443) to `*.involvecloud.com` | Same |
| Privileges | Systemd service, non-root user | Windows service |
| ARM64 support | Yes (Ubuntu / Debian) | — |

## Firewall requirements

Zero inbound rules required at any customer site.

| Direction | Port | Destination | Source |
|---|---|---|---|
| Outbound (internet) | TCP 443 (HTTPS) | `*.involvecloud.com` | Collector host(s) |
| Outbound (internet) | TCP 443 (HTTPS) | `*.involvecloud.com` | Operator + service-desk browsers |
| Outbound (internet) *(optional)* | TCP 443 (HTTPS) | `api.involvecloud.com` | ITSM / CMDB / BI systems |
| Internal (LAN / WAN) | Vendor-specific | Site AV VLANs | Collector host |
| Inbound (internet) | — | — | None. No public-facing ports at any customer location. |

## Integrations

| Category | Approach | Status |
|---|---|---|
| **Single Sign-On** | Microsoft Entra ID (multi-tenant), OpenID Connect | Generally Available |
| **ITSM** | Public REST API (ServiceNow, Jira, other) via customer-side glue | Generally Available |
| **CMDB** | `/pub/v1/assets` endpoint | Generally Available |
| **BI / Reporting** | Public REST API | Generally Available |
| **Notifications** | Email, Microsoft Teams, outbound webhooks | Generally Available |
| **Customer branding** | Per-tenant display name, accent colour, logo, sign-in message, SSO button label, sign-in hero image, support contact, custom subdomain (`<customer>.<env>.involvecloud.com`) | Generally Available |

## Access & role model

Roles are defined **per tenant**. Every customer starts with three system-default roles seeded into their account, and may compose additional roles from the platform's permission catalogue at any time.

### Seeded default roles

| Role | Description |
|---|---|
| **admin** | Full tenant management: device / hierarchy / user / notification / firmware / role / business-unit CRUD, plus all reads and controls. |
| **operator** | Send commands, run bulk fan-out, acknowledge and resolve alerts, defer nightly routines, test notification channels. All reads. No user or role management. |
| **viewer** | Read-only monitoring. No control actions. |

### Custom roles

Any subset of the permission catalogue can be composed into a new role. Roles are tenant-scoped and never leak between customers. System-default roles are protected against edit or deletion.

### Permission catalogue (v1.0 baseline — 24 permissions)

| Category | Permissions |
|---|---|
| Reads | `view.dashboard`, `view.audit`, `view.reports`, `view.firmware`, `view.notifications`, `view.users`, `view.assets` |
| Commands | `command.device`, `command.bulk`, `reconnect.device` |
| Device & hierarchy | `device.crud`, `hierarchy.crud`, `asset.crud` |
| Alerts | `alert.acknowledge`, `alert.resolve` |
| Nightly routines | `nightly.view`, `nightly.manage`, `nightly.defer` |
| Notifications | `notification.crud`, `notification.test` |
| Firmware | `firmware_target.crud` |
| Users & roles | `user.crud`, `role.crud` |
| Business Units | `business_unit.crud` |

### Multi-role users

A user may hold multiple roles simultaneously; effective permissions are the **union** of every role held. Common pattern: *"London Operator + Global Viewer"* is one user with two role assignments.

### Physical scope

A user can be restricted to a subset of the tenant's buildings and (optionally) business units, **orthogonal to their roles**. Example: an Operator scoped to London and Manchester only, within the HMCTS business unit. Scope lives on the user record so promoting or demoting a user does not require touching scope.

### Federation & local users

| Item | Detail |
|---|---|
| SSO protocol | OpenID Connect via Microsoft Entra ID (multi-tenant application) |
| Group-to-role mapping | Assign roles automatically from the customer's Entra security groups |
| MFA | Enforced by the customer's own Entra ID conditional-access policy |
| Local (non-federated) users | Supported for evaluation, out-of-band recovery, and small deployments |
| Password storage (local users) | Hashed with bcrypt (cost factor 12) |

### Public API access

| Item | Detail |
|---|---|
| Token format | `avb_<prefix>_<secret>` — full secret displayed once at creation |
| Storage | Only a SHA-256 hash of the secret is retained |
| Scoping | Per-endpoint permission subset (read scopes in v1) |
| Lifecycle | Expiring and revocable; issue one token per integration |
| Auth header | `Authorization: Bearer avb_...` |

### Audit

Every user, token, and Collector action is recorded in a **tenant-scoped audit log** with actor, action, target, and timestamp. Audit records are readable only by roles that hold the `view.audit` permission. Retention: 24 months.

## Security highlights

- **TLS 1.2 minimum** on every connection.
- **HMAC-SHA256** signing of every Collector-to-cloud request.
- **Scoped bearer tokens** for the Public API — per-integration, revocable, expiring, least-privilege.
- **Row-level tenant isolation** enforced at the database engine, not the application layer.
- **Microsoft Entra ID SSO** for portal users. Per-tenant, per-role, per-scope access control.
- **No public exposure of AV devices** — Collector-initiated egress only.
- **Full tenant-scoped audit trail** for user, token, and Collector actions.

See *M.A.R.C.U.S. — Security & Trust* for full detail.

## UK data sovereignty & compliance

- **Data hosting:** AWS London region (`eu-west-2`) exclusively. **No customer data ever leaves the United Kingdom.**
- **Backup residency:** all backups and point-in-time recovery snapshots are held in the London region. An optional Multi-Region Enterprise add-on (which places an encrypted copy in AWS Ireland) is available only with the customer's explicit written agreement.
- **Support access:** Involve support engineers operate from the UK. No offshore support tier.
- **Data at rest:** AES-256 (AWS RDS + S3 SSE-KMS). Customer-managed KMS keys available on request for Enterprise deployments.
- **Data in transit:** TLS 1.2 minimum, TLS 1.3 preferred; HMAC-SHA256 message signing on every Collector-to-Cloud request.
- **Retention (default policy):**
  - Telemetry: 90 days
  - Alerts: 12 months
  - Audit log: 24 months
  - Backups: 35 days point-in-time recovery
- **Sub-processors:** AWS (UK-hosted infrastructure), Microsoft (Entra ID SSO — customer-controlled). Full list published at [involve.vc/trust/sub-processors](https://involve.vc).
- **UK GDPR:** Data Processing Agreement available on request.
- **Certifications:** Cyber Essentials Plus held; ISO 27001 in progress.
- **Public sector fit:** aligned with NCSC Cloud Security Principles; UK data sovereignty supports NHS, central government, MoJ, local authority, and education procurement.

See *M.A.R.C.U.S. — Business Continuity & Disaster Recovery* and *M.A.R.C.U.S. — Security & Trust* for detail.

## Service & support

- **Availability target:** 99.5% monthly uptime.
- **Support channels:** email (support@involve.vc) and portal ticketing.
- **Support hours:** UK business hours (08:00 – 18:00 GMT / BST, Monday to Friday) for P2–P4. 24×7 P1 response for Enterprise tier.
- **Response targets:** P1 30 minutes · P2 4 hours · P3 1 business day · P4 5 business days.
- **Status page:** [status.involvecloud.com](https://status.involvecloud.com) — planned maintenance, current incidents, post-incident summaries.
- **Release cadence:** monthly minor releases; quarterly major releases; security patches out-of-band.

See *M.A.R.C.U.S. — Service Description & SLA* for the full commercial specification.

## Getting started

- Read the *Product Overview* for a one-page summary.
- Read *M.A.R.C.U.S. — Security & Trust* if you are in procurement or InfoSec.
- Read *M.A.R.C.U.S. — Network & Firewall Requirements* if you are the network or IT owner.
- Contact **commercial@involve.vc** to book an evaluation.

---

*Involve Visual Collaboration Ltd · M.A.R.C.U.S. Platform · Datasheet · v1.0*
*[involve.vc](https://involve.vc)*
