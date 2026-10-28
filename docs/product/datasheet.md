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

> **UK-hosted.** M.A.R.C.U.S. Cloud runs in the AWS London region (`eu-west-2`). Customer data — configuration, telemetry, audit, backups — is stored, processed, and recovered within the United Kingdom. The only exceptions are the optional Multi-Region Enterprise add-on (an encrypted backup copy in AWS Ireland, enabled only with the customer's written agreement), the global CDN that serves the portal's static files, and the published sub-processors.

## Product summary

| | |
|---|---|
| **Product** | M.A.R.C.U.S. Platform |
| **Vendor** | Involve Visual Collaboration Ltd |
| **Deployment model** | SaaS (multi-tenant), **UK-hosted** (AWS London `eu-west-2`) |
| **Cloud component** | M.A.R.C.U.S. Cloud — operated by Involve |
| **On-premise component** | M.A.R.C.U.S. Collector — one per site (or shared across sites) |
| **Target customers** | Corporate AV, higher education, MSPs, government, facilities & IT |
| **Product baseline** | v1.0 — General Availability |
| **Release cadence** | Monthly minor releases; quarterly major releases; security patches out-of-band |

## Core capabilities

- **Live device monitoring** across every room and site, vendor-agnostic.
- **Fleet-wide command dispatch** with sub-second portal-to-device round-trip.
- **Alerts and escalation** with configurable thresholds and flap suppression, delivered via email, Microsoft Teams, or webhooks.
- **Room Readiness** — overnight power-down, morning power-on and a test routine that proves each room is ready, with a morning digest, CSV export and API access to the results.
- **Reporting** — device uptime, room usage (hours in calls and occupied, from device readings), device events, warranty and power, with CSV export. Asset lifecycle without a separate CMDB.
- **Customisable dashboards** — drag-and-drop widgets on personal dashboards, or shared dashboards the whole organisation can see.
- **Wall screens** — full-screen dashboards for a NOC or service desk, shown on screens that don't sign in through read-only display links.
- **Remote access to device web interfaces** — administrators open a device's own web interface from the portal, relayed through the Collector's outbound connection. No inbound ports; every session is time-limited and audited.
- **Public REST API v1** (read-only) for downstream ITSM, CMDB, BI, and dashboards.
- **Multi-tenant, row-level isolation** for MSPs and multi-BU enterprises.
- **Flexible access model** — per-tenant role catalogue, multi-role users, building- and business-unit-level physical scope.
- **Microsoft Entra ID single sign-on** with Entra-group-to-role mapping.
- **White-label-capable** — per-tenant display name, logo, accent colour, sign-in message and custom subdomain out of the box.

## Supported devices and vendors

The full compatibility matrix (per-model, per-firmware) is maintained separately in *M.A.R.C.U.S. — Supported Devices & Compatibility Matrix*. Summary below.

### Vendor-native adapters

| Adapter | Vendor | Devices |
|---|---|---|
| Cisco RoomOS | Cisco | Room Kit, Room Bar, Board, Desk and Codec series (xAPI over HTTPS) |
| Cisco TC (TC7) | Cisco | TelePresence SX, MX, EX, C and Profile series on TC7.x software (xAPI over HTTPS) |
| Poly VideoOS | Poly / HP | G7500, Studio X70 / X52 / X50 / X30 |
| Philips Professional Displays | Philips (PPDS) | Professional and signage displays (SICP on TCP 5000, Wake-on-LAN) |
| Sony Bravia Professional | Sony | Bravia Professional Displays (JSON-RPC + PSK) |
| Biamp Tesira | Biamp | Tesira DSPs (TTP over Telnet, subscription-based metrics, DEVICE-level identity + faults) |
| Aurora RXT | Aurora Multimedia | RXT-x wall-mount touch panels |
| Aurora VPX | Aurora Multimedia | VPX-series AV-over-IP encoders / decoders |
| ATEN eco PDU | ATEN | PE6108G and siblings (per-outlet control + metering) |
| NETGEAR M4250 / M4350 | NETGEAR | AV Line managed switches (per-port link and PoE draw, PoE power-cycle) |
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

- **M.A.R.C.U.S. Collector** — small Linux or Windows service on your network. Speaks native vendor protocols locally; connects to the cloud outbound only, over HTTPS on TCP 443.
- **M.A.R.C.U.S. Cloud** — multi-tenant platform hosted in AWS London (`eu-west-2`). Runs the portal, ingest pipeline, alert engine, and Public API.
- **Portal** — modern web application. Microsoft Entra ID SSO. Role-based access.
- **Public API** — versioned, read-only REST at `/pub/v1`. OpenAPI 3.1 specification + Swagger UI. Bearer-token authentication.

Full architecture and network flow diagrams are available separately (see *M.A.R.C.U.S. — Network & Firewall Requirements*).

## Deployment options

| Option | When to use |
|---|---|
| **Per-site Collector** | Preferred. Sites without reliable WAN reach between them; large sites; sites needing local resilience. |
| **Shared central Collector** | Suitable when a corporate WAN (MPLS, SD-WAN, site-to-site VPN) provides reliable RFC1918 reach into every site's AV VLAN. |
| **Hybrid** | Common in practice — one Collector per major site, plus a central Collector serving smaller regional offices. |

## Sizing guidance

Per Collector, at the default 60-second poll rate:

| Host | Devices per Collector |
|---|---|
| **Recommended:** 4 vCPU · 8 GB RAM · 50 GB disk | Up to 5,000 |
| Minimum: 2 vCPU · 4 GB RAM · 20 GB disk | Up to 5,000 |
| Small sites: 1 vCPU · 2 GB RAM · 20 GB disk | Up to 1,000 |

In a two-hour load test, one Collector serving 5,000 devices, including encrypted video-codec sessions, used 0.13 CPU cores and at most 500 MB of memory. Larger estates add Collectors per site or region rather than larger hosts.

**Bandwidth**

| Path | Typical |
|---|---|
| Collector → M.A.R.C.U.S. Cloud (HTTPS 443) | ~0.11 Mbps per 1,000 devices (~0.6 Mbps per 5,000-device Collector) |
| Collector → devices | ~0.1 kbps per display, DSP or control processor; ~0.3 kbps per AV-over-IP endpoint; ~0.6 kbps per PDU (5-minute polling); 1–2 kbps per network switch or Poly codec; ~14 kbps per Cisco codec |

Example: 50,000 devices without video codecs, across ten Collectors, needs about 6 Mbps to the cloud in total and about 1 Mbps between each Collector and its devices. Firm sizing for a specific fleet is available on request — contact commercial@involve.vc.

## Collector host requirements

| | Linux | Windows |
|---|---|---|
| OS | Ubuntu 20.04 LTS+, RHEL 8+, Debian 11+ | Windows Server 2019, 2022 |
| CPU | 2 vCPU minimum, 4 vCPU recommended | Same |
| RAM | 4 GB minimum, 8 GB recommended | Same |
| Disk | 20 GB minimum, 50 GB recommended, for logs, local state and outage buffering | Same |
| Network | Outbound HTTPS (TCP 443) to `*.involvecloud.com` | Same |
| Privileges | Systemd service, non-root user | Windows service |
| ARM64 support | Yes (Ubuntu / Debian) | — |

## Firewall requirements

Zero inbound rules required at any customer site.

| Direction | Port | Destination | Source |
|---|---|---|---|
| Outbound (internet) | TCP 443 (HTTPS) | `*.involvecloud.com` | Collector host(s) |
| Outbound (internet) | TCP 443 (HTTPS) | `*.involvecloud.com` | Operator + service-desk browsers |
| Outbound (internet) *(remote access)* | TCP 443 (HTTPS) | `*.access.involvecloud.com` | Administrator browsers that open device web interfaces |
| Outbound (internet) *(optional)* | TCP 443 (HTTPS) | `api.involvecloud.com` | ITSM / CMDB / BI systems |
| Internal (LAN / WAN) | Vendor-specific | Site AV VLANs | Collector host |
| Inbound (internet) | — | — | None. No public-facing ports at any customer location. |

Remote access sessions use addresses one level deeper than `*.involvecloud.com` (`<session>.access.involvecloud.com`). Many proxies and allow-lists match a wildcard to one level only, so allow `*.access.involvecloud.com` explicitly. The Collector's remote access tunnel is a WebSocket on its existing TCP 443 connection; proxies must allow WebSocket upgrades to `*.involvecloud.com`.

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
| **admin** | Full tenant management: devices, locations, collectors, users, roles, notifications, firmware targets, business units, branding, shared dashboards, API tokens and remote access, plus all reads and controls. |
| **operator** | Send commands, run bulk fan-out, acknowledge and resolve alerts, defer nightly routines, test notification channels. All reads. No user or role management. |
| **viewer** | Read-only monitoring. No control actions. |

### Custom roles

Any subset of the permission catalogue can be composed into a new role. Roles are tenant-scoped and never leak between customers. System-default roles are protected against edit or deletion.

### Permission catalogue (34 permissions)

| Category | Permission | Key | What it allows |
|---|---|---|---|
| Reads | View dashboard | `view.dashboard` | Fleet summary, device list, telemetry, live event stream. |
| Reads | View audit log | `view.audit` | See who did what and when across the tenant. |
| Reads | View reports | `view.reports` | Device uptime, room usage, device events, warranty and power reports, CSV export. |
| Reads | View firmware | `view.firmware` | Fleet firmware histogram and target versions. |
| Reads | View notifications | `view.notifications` | List the notification channels configured for the tenant. |
| Reads | View users | `view.users` | List of users in the tenant and their assigned roles. |
| Reads | View assets | `view.assets` | List physical assets (monitored and not) tracked in the tenant's rooms. |
| Device control | Send device commands | `command.device` | Issue single-device commands (power, input, mute, dial). |
| Device control | Bulk command fan-out | `command.bulk` | Send the same command to many devices at once (up to 200). |
| Device control | Reconnect device | `reconnect.device` | Force the Collector to drop and re-open a device connection. |
| Device control | Remote access | `device.remote_access` | Open a device's web interface from the portal, through its Collector. |
| Alerts | Acknowledge alerts | `alert.acknowledge` | Move an open alert to acknowledged so on-call knows it is being handled. |
| Alerts | Resolve alerts | `alert.resolve` | Close an alert as resolved. Records who resolved it. |
| Fleet management | Manage devices | `device.crud` | Add, edit, and remove devices from the tenant. |
| Fleet management | Manage locations | `hierarchy.crud` | Regions, locations, buildings and rooms. |
| Fleet management | Manage business units | `business_unit.crud` | Create, rename and remove business units, and assign regions to them (when business units are switched on). |
| Fleet management | Manage collectors | `collector.crud` | Pre-provision Collectors from the portal and issue enrolment tokens for site setup. |
| Fleet management | Share dashboards | `dashboard.share` | Publish dashboards the whole organisation can see, and edit or delete them. |
| Notifications | Manage notification channels | `notification.crud` | Add, edit, and remove email, Teams and webhook channels. |
| Notifications | Test notification channel | `notification.test` | Send a test message to a channel to verify its configuration. |
| Firmware | Manage firmware targets | `firmware_target.crud` | Set the target firmware version and documentation URL per make and model. |
| Users & Roles | Create users | `user.create` | Add new users to the tenant. |
| Users & Roles | Update users | `user.update` | Change a user's full name, role assignments, or enabled state. |
| Users & Roles | Reset user passwords | `user.reset_password` | Set a new password for a user without knowing the current one. |
| Users & Roles | Delete users | `user.delete` | Permanently remove a user from the tenant. |
| Users & Roles | Manage roles | `role.crud` | Create, edit, and delete custom roles inside this tenant. |
| Users & Roles | Manage sign-in mappings | `role_mapping.manage` | Map Entra security groups to roles so SSO users receive the right permissions on first sign-in. |
| Tenant | Update branding | `branding.update` | Upload the tenant logo, change the accent colour and portal display name. |
| Assets | Manage assets | `asset.crud` | Add, edit, and remove assets (physical inventory) in the tenant. |
| Room Readiness | View Room Readiness | `nightly.view` | See nightly schedules, routines, and run history. |
| Room Readiness | Defer tonight's power-down | `nightly.defer` | Keep a room powered up tonight when it is still in use; it returns to schedule the next day. |
| Room Readiness | Manage Room Readiness | `nightly.manage` | Edit the nightly schedule, room overrides, and test routines. |
| Public API | View API tokens | `api_token.view` | See the API tokens issued for programmatic access, and when each was last used. |
| Public API | Manage API tokens | `api_token.manage` | Issue, name, and revoke API tokens used by external systems to read from the tenant. |

User management is split into four permissions (create, update, reset password, delete); the earlier single `user.crud` permission no longer exists.

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

Every user, token, and Collector action is recorded in a **tenant-scoped audit log** with actor, action, target, and timestamp. Remote access sessions are audited at start and end, with who opened them, duration and bytes transferred. Audit records are readable only by roles that hold the `view.audit` permission. Retention: 24 months.

## Security highlights

- **TLS 1.2 minimum** on every connection.
- **HMAC-SHA256** signing of every Collector-to-cloud API request, and of the remote access tunnel handshake.
- **Remote access without inbound ports** — sessions run through the Collector's outbound tunnel, reach only that Collector's own devices on permitted ports, expire after 15 minutes idle or 8 hours, and are audited. Session content is not recorded.
- **Scoped bearer tokens** for the Public API — per-integration, revocable, expiring, least-privilege.
- **Row-level tenant isolation** enforced at the database engine, not the application layer.
- **Microsoft Entra ID SSO** for portal users. Per-tenant, per-role, per-scope access control.
- **No public exposure of AV devices** — Collector-initiated egress only.
- **Full tenant-scoped audit trail** for user, token, and Collector actions.

See *M.A.R.C.U.S. — Security & Trust* for full detail.

## UK data sovereignty & compliance

- **Data hosting:** customer data is stored and processed in the AWS London region (`eu-west-2`). Exceptions: the optional Multi-Region Enterprise add-on (below), the global CDN that serves the portal's static files (no customer data), and the published sub-processors.
- **Backup residency:** all backups and point-in-time recovery snapshots are held in the London region. An optional Multi-Region Enterprise add-on (which places an encrypted copy in AWS Ireland) is available only with the customer's explicit written agreement.
- **Support access:** Involve support engineers operate from the UK. No offshore support tier.
- **Data at rest:** AES-256 (AWS RDS + S3 SSE-KMS). Customer-managed KMS keys available on request for Enterprise deployments.
- **Data in transit:** TLS 1.2 minimum, TLS 1.3 preferred; HMAC-SHA256 message signing on every Collector-to-Cloud API request and on the remote access tunnel handshake.
- **Retention (default policy):**
  - Raw telemetry: 90 days (configurable)
  - Daily telemetry summaries per device: 400 days (reports cover up to 12 months)
  - Device events: 90 days
  - Alerts: 12 months after they are resolved; open and acknowledged alerts are kept
  - Command history: 90 days after completion
  - Room Readiness run history: 90 days by default, set per customer (30–365 days)
  - Remote access session records: 90 days after the session ends (their audit entries follow the audit log)
  - Audit log: 24 months
  - Application logs: 30 days
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
