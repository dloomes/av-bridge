---
title: AV Bridge — Datasheet
description: Technical specifications, supported vendors, deployment options, sizing, and integration surfaces for AV Bridge.
audience: Technical evaluators, buyers, and integrators
status: Draft v0.2 · needs marketing polish and confirmation of items marked [TBC]
---

# AV Bridge Datasheet

Involve Cloud's AV Bridge is a cloud-managed monitoring, control, and automation platform for corporate audio-visual estates. This document lists what it does, what it runs on, what it integrates with, and what you need to deploy it.

## Product summary

| | |
|---|---|
| **Product** | AV Bridge Platform |
| **Vendor** | Involve Cloud |
| **Deployment model** | SaaS (multi-tenant), UK / EU hosted |
| **Customer-side footprint** | One "Collector" service per site (or one shared across sites) |
| **Target customers** | Corporate AV, higher education, MSPs, facilities & IT |
| **Latest release** | [TBC — release identifier] |

## Core capabilities

- **Live device monitoring** across every room and site, vendor-agnostic.
- **Fleet-wide command dispatch** with sub-second portal-to-device round-trip.
- **Alerts and escalation** with configurable thresholds, quiet hours, and flap suppression.
- **Nightly and scheduled routines** — health checks, power sequences, config sync, room-open flows.
- **Public REST API** for downstream ITSM, CMDB, BI, and dashboards.
- **Multi-tenant, row-level isolation** for MSPs and multi-BU enterprises.
- **Flexible access model** — per-tenant role catalogue, multi-role users, building-level physical scope.
- **Microsoft Entra ID single sign-on** with Entra-group-to-role mapping.

## Supported devices and vendors

Adapter coverage as at [DATE]. The adapter layer is extensible; new vendors are added without breaking existing deployments.

### Vendor-specific adapters

| Adapter | Vendor | Devices |
|---|---|---|
| Poly VideoOS | Poly / HP | G7500, Studio X70 / X52 / X50 / X30 |
| Sony Bravia Professional | Sony | Bravia Professional Displays (JSON-RPC + PSK) |
| Biamp Tesira | Biamp | Tesira DSPs (TTP over Telnet, subscription-based metrics) |
| Aurora RXT | Aurora Multimedia | RXT-x wall-mount touch panels |
| Aurora VPX | Aurora Multimedia | VPX-series AV-over-IP encoders / decoders |
| ATEN eco PDU | ATEN | PE6108G and siblings (per-outlet control + metering) |
| VISCA-over-IP | Sony / Panasonic / PTZOptics / HuddleCam / Marshall / Lumens | PTZ cameras speaking the Sony VISCA standard |

### Generic transport adapters

| Adapter | Use |
|---|---|
| Generic REST | Any device with an HTTP/JSON control API |
| Generic WebSocket | Any device with a WebSocket control channel |
| Generic Telnet | CLI-driven control processors, matrix switchers, older displays |
| Generic Serial (RS-232) | Direct RS-232 attached to the Collector host |

### Standalone probes

| Adapter | Use |
|---|---|
| ICMP Ping | Reachability + latency for devices with no vendor API |

### Adapter roadmap

Additional vendor adapters are added in most releases. Priority is driven by customer requests — [TBC — link to public roadmap or contact].

## Architecture at a glance

```
[ Room devices ] ── LAN ──► [ Collector ] ── HTTPS 443 ──► [ Involve Cloud ] ──► [ Portal · Public API · Alerts ]
```

- **Collector** — small Linux or Windows service on your network. Speaks native vendor protocols locally; makes one outbound HTTPS connection to the cloud.
- **Involve Cloud** — multi-tenant platform hosted in UK / EU AWS regions. Runs the portal, ingest pipeline, alert engine, and Public API.
- **Portal** — modern web application. Microsoft Entra ID SSO. Role-based access.
- **Public API** — versioned REST at `/pub/v1`. OpenAPI 3.1 spec + Swagger UI. Bearer-token auth.

Detailed architecture and network flow diagrams are available separately (see *Network & Firewall Requirements*).

## Deployment options

| Option | When to use |
|---|---|
| **Per-site Collector** | Preferred. Sites without reliable WAN reach between them; large sites; sites needing local resilience. |
| **Shared central Collector** | Suitable when a corporate WAN (MPLS, SD-WAN, site-to-site VPN) provides reliable RFC1918 reach into every site's AV VLAN. |
| **Hybrid** | Common in practice — one Collector per major site, plus a central Collector serving smaller regional offices. |

## Sizing guidance

Per Collector, on the recommended host spec:

| Host | Comfortable | Stretch (relaxed poll rates) |
|---|---|---|
| 2 vCPU · 4 GB RAM | 250 – 500 devices | up to ~1,000 devices |
| 4 vCPU · 8 GB RAM | 500 – 1,000 devices | up to ~2,000 devices |
| Heavy-adapter mix (many SSH / VC codecs) | ~60% of the above | ~75% of the above |

Numbers depend on poll rate, WAN latency, and adapter mix. Firm sizing for a specific fleet is available on request via load-test against the target host — [TBC — process link].

## Collector host requirements

| | Linux | Windows |
|---|---|---|
| OS | Ubuntu 20.04+, RHEL 8+, Debian 11+ [TBC — confirmed list] | Windows Server 2019+ [TBC] |
| CPU | 2 vCPU minimum, 4 vCPU recommended | Same |
| RAM | 4 GB minimum, 8 GB recommended | Same |
| Disk | 10 GB for logs and telemetry buffer | Same |
| Network | Outbound HTTPS to `*.involvecloud.com` on TCP 443 | Same |
| Privileges | Systemd service, non-root user | Windows service |

## Firewall requirements

Zero inbound rules required.

| Direction | Port | Destination | Source |
|---|---|---|---|
| Outbound (internet) | TCP 443 (HTTPS) | `*.involvecloud.com` | Collector host(s) |
| Outbound (internet) | TCP 443 (HTTPS) | `*.involvecloud.com` | Operator + service-desk browsers |
| Outbound (internet) *(optional)* | TCP 443 (HTTPS) | `api.involvecloud.com` | ITSM / CMDB / BI systems |
| Internal (WAN) | Vendor-specific | Site AV VLANs | Collector host |
| Inbound (internet) | — | — | None. No public-facing ports at any customer location. |

## Integrations

| Category | Approach | Status |
|---|---|---|
| **Single Sign-On** | Microsoft Entra ID (multi-tenant), OpenID Connect — see *Access & role model* | Shipped |
| **ITSM** | Public REST API (ServiceNow, Jira, other) via customer-side glue | API shipped; ServiceNow cookbook [TBC] |
| **CMDB** | `/pub/v1/assets` endpoint | Shipped |
| **BI / Reporting** | Public REST API + Power BI cookbook [TBC] | API shipped |
| **Notifications** | Email; webhooks / SMS [TBC — roadmap] | Partial |

## Access & role model

Roles are defined **per tenant**. Every customer starts with three system-default roles seeded into their account, and may compose additional roles from the platform's permission catalogue at any time.

### Seeded default roles

| Role | Description |
|---|---|
| **admin** | Full tenant management: device / hierarchy / user / notification / firmware / role CRUD, plus all reads and controls. |
| **operator** | Send commands, run bulk fan-out, acknowledge and resolve alerts, test notification channels. All reads. No user or role management. |
| **viewer** | Read-only monitoring. No control actions. |

### Custom roles

Any subset of the permission catalogue can be composed into a new role. Roles are tenant-scoped and never leak between customers. System-default roles are protected against edit or deletion.

### Permission catalogue

| Category | Permissions |
|---|---|
| Reads | `view.dashboard`, `view.audit`, `view.reports`, `view.firmware`, `view.notifications`, `view.users` |
| Commands | `command.device`, `command.bulk`, `reconnect.device` |
| Device & hierarchy | `device.crud`, `hierarchy.crud` |
| Alerts | `alert.acknowledge`, `alert.resolve` |
| Notifications | `notification.crud`, `notification.test` |
| Firmware | `firmware_target.crud` |
| Users & roles | `user.create`, `user.update`, `user.reset_password`, `user.delete`, `role.crud` |

### Multi-role users

A user may hold multiple roles simultaneously; effective permissions are the **union** of every role held. Common pattern: *"London Operator + Global Viewer"* is one user with two role assignments.

### Physical scope

A user can be restricted to a subset of the tenant's buildings, **orthogonal to their roles**. Example: an Operator scoped to London and Manchester only. Scope lives on the user record so promoting or demoting a user does not require touching scope.

### Federation & local users

| Item | Detail |
|---|---|
| SSO protocol | OpenID Connect via Microsoft Entra ID (multi-tenant application) |
| Group-to-role mapping | Assign roles automatically from the customer's Entra security groups |
| MFA | Enforced by the customer's own Entra ID conditional-access policy |
| Local (non-federated) users | Supported for evaluation, out-of-band recovery, and small deployments |
| Password storage (local users) | Hashed with **[TBC — bcrypt cost / argon2 parameters]** |

### Public API access

| Item | Detail |
|---|---|
| Token format | `avb_<prefix>_<secret>` — full secret displayed once at creation |
| Storage | Only a SHA-256 hash of the secret is retained |
| Scoping | Per-endpoint permission subset (read scopes in v1) |
| Lifecycle | Expiring and revocable; issue one token per integration |
| Auth header | `Authorization: Bearer avb_...` |

### Audit

Every user, token, and Collector action is recorded in a **tenant-scoped audit log** with actor, action, target, and timestamp. Audit records are readable only by roles that hold the `view.audit` permission.

## Security highlights

- **TLS 1.2+** on every connection.
- **HMAC-SHA256** signing of every Collector-to-cloud request.
- **Scoped bearer tokens** for Public API — per-integration, revocable, expiring, least-privilege.
- **Row-level tenant isolation** enforced at the database, not the application layer.
- **Microsoft Entra ID SSO** for portal users. Per-tenant, per-role, per-scope access control — see *Access & role model*.
- **No public exposure of AV devices** — Collector-initiated egress only.

See *AV Bridge — Security & Trust* for full detail.

## Data & compliance

- **Data hosting:** United Kingdom / EU AWS regions [TBC — confirm exact region(s)]
- **Data at rest:** Encrypted using AWS RDS encryption (AES-256)
- **Retention:** [TBC — set policy]
- **Sub-processors:** [TBC — publish list]
- **GDPR:** [TBC — statement / DPA]
- **Certifications:** SOC 2 Type II and ISO 27001 [TBC — roadmap]

See *AV Bridge — Data Residency & Retention* for full detail.

## Service & support

- **Availability target:** [TBC — SLA]
- **Support channels:** [TBC]
- **Response time targets:** [TBC — severity matrix]
- **Status page:** [TBC — URL]
- **Release cadence:** [TBC — commitment]

## Getting started

- Read the *Product Overview* for a one-page summary.
- Read *AV Bridge — Security & Trust* if you are in procurement or InfoSec.
- Read *Network & Firewall Requirements* if you are the network or IT owner.
- Contact **[TBC — sales@involve.vc]** to book an evaluation.

---

*Involve Cloud · AV Bridge Platform · Datasheet v0.2 · [YEAR]*
