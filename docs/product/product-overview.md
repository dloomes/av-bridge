---
title: M.A.R.C.U.S. — Product Overview
product: M.A.R.C.U.S.
vendor: Involve Visual Collaboration Ltd
website: https://involve.vc
version: 1.0
status: General Availability
audience: All buyers, executive summary, RFI responses
---

<!-- ============================================================
  M.A.R.C.U.S.
  Managed · Assets · Resources · Control · Updates · Status
  Involve Visual Collaboration Ltd · involve.vc
============================================================ -->

# M.A.R.C.U.S.

**Managed · Assets · Resources · Control · Updates · Status**

*One intelligent platform to manage, understand and optimise complex AV and video collaboration estates.*

> **UK-hosted.** M.A.R.C.U.S. Cloud runs in the AWS London region. Customer data — configuration, telemetry, audit, backups — is stored, processed, and recovered within the United Kingdom. The only exceptions are the optional Multi-Region Enterprise add-on (an encrypted backup copy in AWS Ireland, enabled only with the customer's written agreement), the global CDN that serves the portal's static files, and the published sub-processors.

---

## Six simple principles. Complete AV / VC estate intelligence.

| | Principle | What it means |
|---|---|---|
| **M** | **Managed** | Proactive estate management — monitor & alert, incident & SLA workflow, engineer / service coordination |
| **A** | **Assets** | One source of truth — sites, rooms & endpoints, ownership & warranty, age, condition & lifecycle |
| **R** | **Resources** | Everything needed to support it — drawings & documentation, configuration & support history, knowledge linked to each asset |
| **C** | **Control** | Diagnose and support remotely — device interrogation, remote actions & restart, configuration management |
| **U** | **Updates** | Keep the estate current — firmware & software status and updates, approved baselines, compliance & change control |
| **S** | **Status** | Know what's happening live — health & availability, utilisation & incidents, performance & SLA analytics |

**ONE PLATFORM. COMPLETE ESTATE INTELLIGENCE.** Connect asset data, live telemetry, service activity, utilisation and lifecycle information — see the estate, understand risk, and act before users are impacted.

**Maximise uptime · Resolve faster · Reduce cost · Plan lifecycle · Prove performance.**

---

## Product components

M.A.R.C.U.S. is delivered as a two-part SaaS platform:

| Component | Where it runs | What it does |
|---|---|---|
| **M.A.R.C.U.S. Cloud** | AWS London (`eu-west-2`), operated by Involve | Multi-tenant portal, public API, alert engine, automation, historical analytics |
| **M.A.R.C.U.S. Collector** | Customer network (Linux, Windows, ARM64) | On-premise agent that speaks native vendor protocols to AV devices; connects to M.A.R.C.U.S. Cloud outbound only, over HTTPS 443 |

Customers deploy one M.A.R.C.U.S. Collector per site, or a shared Collector serving multiple sites — both models are supported and can be mixed within a single tenant.

## Who it's for

- **Corporate AV & UC teams** managing rooms across multiple offices, campuses, or regions.
- **Higher education** running lecture theatres, huddle spaces, and events venues at scale.
- **Managed service providers** operating AV estates on behalf of end customers.
- **Facilities and IT service desks** that own the room-experience ticket queue.
- **Central and local government** requiring UK-hosted, procurement-ready AV monitoring.

## Why it matters

| Without M.A.R.C.U.S. | With M.A.R.C.U.S. |
|---|---|
| Users report the failure first | You know before the meeting starts |
| Mixed vendor tooling — one console per brand | One console for the whole fleet |
| Nightly checks are a Monday-morning walk-around | Nightly checks run themselves and file tickets |
| Rooms drift from working configuration | Configuration sync catches drift the same day |
| Fleet data trapped in the tool | Fleet data flows to ITSM, BI, and CMDB |
| Manual asset registers, spreadsheets, aging data | Live asset register with warranty, lifecycle, condition |

---

## Feature breakdown

### 1. User access & role flexibility

M.A.R.C.U.S. is built for organisations where "who can do what" isn't a one-size-fits-all decision. The permission model is fine-grained, per tenant, and combines with physical scope so a user's authority is bounded to their remit.

- **Per-tenant role catalogue** — customers define their own roles alongside the three seeded defaults (admin, operator, viewer). Roles never leak between tenants.
- **Fine-grained permissions** — 34 discrete permission keys covering reads, device commands, bulk actions, remote access, alert lifecycle, hierarchy and Collector management, user and role management, sign-in mappings, notifications, firmware targets, audit access, Room Readiness, shared dashboards, branding, API tokens, and Business Unit administration.
- **Multi-role users** — a single user may hold multiple roles simultaneously; effective permissions are the union. Common pattern: *"London Operator + Global Viewer"*.
- **Physical scope** — restrict a user to specific buildings or business units, orthogonal to their role. Roles say *what*, scope says *where*.
- **Optional Business Unit tier** — for enterprise and multi-agency deployments, an optional BU hierarchy sits above Region so that HMCTS, HMPPS, or MoJ (for example) each get their own operational surface within one tenant.
- **Microsoft Entra ID SSO** with **group-to-role mapping** — assign roles based on the customer's own Entra groups; no double-maintenance of user directories.
- **Local users** for evaluation, break-glass, and small deployments without an Entra tenant.
- **Per-scope Public API tokens** — issue tokens with any subset of the read permission catalogue; revocable, expiring, one integration one token.
- **Full audit trail** — every user, token, and Collector action is logged with actor, action, target, and timestamp. Tenant-scoped and readable only by that tenant's authorised roles.

### 2. Fleet monitoring & visibility

- **Live device state** across every room, every site — codecs, displays, matrices, DSPs, touch panels, PDUs, cameras, control processors.
- **Building → Room → Device hierarchy**, with optional **Region → Location → Building** and **Business Unit** tiers for physical navigation at scale.
- **Collector-aware status** — when a Collector goes offline, its devices project to *unknown*; when a device stops reporting while its Collector is fine, it projects to *offline*. Operators see reality, not stale success.
- **Historical telemetry** — status, metrics, and event trends over time.
- **Filter, search, tag** across the fleet.
- **Nightly digest** email summarising fleet health, issues, and follow-ups.
- **Reports & exports** on device uptime, room usage (hours in calls and occupied, from the devices themselves), device events, warranty, and power consumption.
- **Customisable dashboards** — build personal dashboards by dragging and dropping widgets, or publish shared dashboards for the whole organisation.
- **Wall screens** — show a dashboard full-screen on a NOC or service-desk screen through a read-only display link, without signing the screen in.

### 3. Assets & lifecycle

- **First-class asset register** — every physical thing tracked with make, model, serial, warranty end, purchase date, notes, and physical location.
- **Warranty report** — assets grouped by expiry bucket (expired, ≤30 days, ≤90 days, ≤365 days, later, no date).
- **Power reporting** — per-device nameplate power rating (on / standby watts) feeds a kWh-consumed and kWh-saved-by-nightly-routine report per room.
- **CSV import / export** — bulk-populate warranty and lifecycle data from existing spreadsheets.
- **CMDB endpoint** — the asset surface is first-class in the Public API, ready for ServiceNow, Jira, or bespoke CMDB integrations.

### 4. Command & control

- **Sub-second portal-to-device dispatch** — actions land on the device in typically under one second, not on a polling tick.
- **Bulk fan-out** — issue one action against many devices at once (a room, a floor, a site, or a saved selection).
- **Vendor-native controls** — power on / off, mute, reboot, reconnect, and vendor-specific custom commands.
- **Reconnect on demand** — send a device a controlled disconnect / reconnect to clear a wedged session without a truck-roll.
- **Remote access to device web interfaces** — administrators open a device's own web interface from the portal in a new tab, relayed through the Collector's outbound tunnel. No inbound ports; each session is time-limited and audited.
- **Touch-panel proxy** — reach in-room touch panels on customer LANs through the Collector, without exposing them to the internet.
- **Command history + audit log** — every dispatched action is recorded with actor, target, timing, and outcome.

### 5. Alerting & incident response

- **Configurable thresholds** per device and per device class.
- **Flap suppression** — a device that briefly loses reachability doesn't page anyone until the state is stable.
- **Alert lifecycle** — acknowledge → investigate → resolve, with the audit trail attached.
- **Help desk console** — a dedicated view for service-desk agents handling room incidents.
- **Multi-channel notification** — email, Microsoft Teams, and outbound webhooks. All channels selectable per alert-severity bucket.

### 6. Automation & routines

- **Room Readiness** — rooms power down overnight and back up before the working day, then a test routine proves each one works: devices online, a test call connects, microphones hear audio. A morning digest reports every room.
- **Test routines without code** — a visual builder for power, command, status and reading checks, with a standard template to start from and a one-click test against any room.
- **Configurable per room** — per-room times, days and test routine; exclusion with a documented reason; one-click "defer tonight" for rooms in active use.
- **Results everywhere** — run history in the portal, CSV export, and the Public API for dashboards and service-management tools.
- **Configuration sync** — the desired configuration for every device lives in the cloud; the Collector reconciles it and flags drift.

### 7. Extensibility & integrations

- **Vendor-native adapters** — Cisco RoomOS, Cisco TC (TC7), Poly VideoOS, Philips Professional Displays, Sony Bravia Professional, Biamp Tesira, ATEN eco PDU, NETGEAR M4250 / M4350 switches (per-port PoE), Aurora RXT touch panels, Aurora VPX AV-over-IP, VISCA-over-IP PTZ cameras.
- **Custom devices via generic transports** — REST, WebSocket, Telnet, and RS-232 serial adapters cover any device with a documented control interface; ICMP ping covers devices with no control API.
- **Public REST API v1** with **OpenAPI 3.1 + Swagger UI** — read-only access to devices and telemetry, alerts, events, assets, locations, buildings and rooms, the audit log, and Room Readiness results. Reports, dashboards, and user and notification management are available in the portal only.
- **Bearer-token authentication**, cursor pagination, versioned endpoints — designed for machine consumption at scale.
- **Adapter SDK** — customers and partners can add new device types without waiting for a platform release.

### 8. Customer branding

M.A.R.C.U.S. is a **white-label-capable** platform. Every tenant can present the service under its own brand, without a bespoke deployment.

- **Display name** — the customer's brand appears on the sign-in page, in the browser tab, on invitation emails, and throughout the portal chrome.
- **Uploaded logo** — the customer's mark replaces the default badge on the sign-in surface and portal header.
- **Accent colour** — a single hex value re-tones the portal's primary interactive colour across every page; no CSS work required.
- **Custom sign-in message** — a per-tenant welcome / policy note shown to users during authentication.
- **Custom SSO button label** — for tenants who want *"Sign in with corporate Entra ID"* rather than the generic label.
- **Sign-in hero image** — an optional customer-controlled background image behind the sign-in surface.
- **Support contact** — customer-supplied email or phone number surfaced on the portal's help affordances so users reach the customer's own service desk, not Involve.
- **Custom subdomain** — sign-in on `<customer>.<env>.involvecloud.com`; identity, branding, and sign-in surface all resolve from the subdomain automatically.

Ideal for **managed service providers** presenting M.A.R.C.U.S. as their own operations platform, and for **large enterprises and public-sector customers** who need internal branding compliance.

### 9. Deployment & platform

- **Cross-platform Collector** — Linux (Ubuntu 20.04 LTS+, RHEL 8+, Debian 11+), Windows Server 2019+, Linux ARM64.
- **One-line install** — the portal generates a signed install command; enrolment and initial configuration happen in the same step.
- **Automatic configuration sync** — device changes made in the portal reach the Collector within seconds; no manual roll-out.
- **Self-updating Collectors** — signed updates from the portal, on demand or on a schedule, with automatic rollback if an update fails its health check.
- **Resilient by design** — a Collector keeps polling through a cloud outage and sends its buffered telemetry when the link returns; an optional standby Collector takes over automatically if the primary fails.
- **UK-hosted** on AWS London (`eu-west-2`), where customer data is stored and processed (exceptions are listed under *Security & compliance*). Multi-tenant with row-level tenant isolation enforced by the database engine.
- **Terraform-managed infrastructure** — every environment reproducible from source.

### 9. Security & compliance

- **Zero inbound firewall rules** at any customer site.
- **TLS 1.2 minimum** on every connection; TLS 1.3 preferred.
- **HMAC-SHA256 request signing** between every Collector and M.A.R.C.U.S. Cloud — message-level authenticity independent of the transport. The remote access tunnel is authenticated with a signed, time-limited handshake.
- **Row-level tenant isolation** enforced by PostgreSQL Row-Level Security.
- **Microsoft Entra ID SSO** with MFA enforced by customer conditional-access policy.
- **Full tenant-scoped audit trail** readable only by that tenant's authorised roles.
- **UK data sovereignty** — customer data is stored, processed, and backed up in the AWS London region (`eu-west-2`). Exceptions: the optional Multi-Region Enterprise add-on, which keeps an encrypted backup copy in AWS Ireland and is enabled only with the customer's written agreement; the global CDN that serves the portal's static files; and the published sub-processors.
- **GDPR compliant** — Data Processing Agreement available; sub-processor list published.

See *M.A.R.C.U.S. — Security & Trust*, *M.A.R.C.U.S. — Service Description & SLA*, and *M.A.R.C.U.S. — Business Continuity & Disaster Recovery* for full detail.

---

## How it works

1. A small **M.A.R.C.U.S. Collector** service sits on your network. It speaks every AV vendor's native protocol locally — no changes to your devices, no cloud reach into your AV VLAN.
2. The Collector connects outbound to **M.A.R.C.U.S. Cloud** over HTTPS 443 — the only firewall rule you add.
3. Your teams work in the **portal** (SSO via Microsoft Entra ID), and your systems consume the **Public API** via revocable bearer tokens.

Zero inbound firewall rules. No public exposure of AV devices. UK-hosted.

## What makes M.A.R.C.U.S. different

- **Vendor-neutral by design.** The adapter layer is open and extensible — new device types are added without breaking existing rooms.
- **Sub-second command dispatch.** Portal actions land on the device in under a second — no polling delay.
- **Role flexibility that matches the real org chart.** Per-tenant role catalogue, multi-role users, and building-level scope. Not three hardcoded personas.
- **Multi-tenant to the row level.** Isolation is enforced at the database engine, not the application — critical for MSPs and multi-BU enterprises.
- **API-first.** A read-only Public API exposes devices and telemetry, alerts, events, assets, locations, the audit log, and Room Readiness results to ITSM, CMDB, and BI systems.
- **Assets and lifecycle first-class.** Warranty, power, and room usage reporting on the same platform as live monitoring — no separate CMDB required.

---

## Getting started

- **Commercial enquiries:** commercial@involve.vc
- **Technical evaluation:** book a demo at [involve.vc](https://involve.vc)
- **Read the datasheet:** *M.A.R.C.U.S. — Datasheet*
- **For InfoSec / procurement:** *M.A.R.C.U.S. — Security & Trust*

---

*Involve Visual Collaboration Ltd · M.A.R.C.U.S. Platform · Product Overview · v1.0*
*[involve.vc](https://involve.vc)*
