---
title: AV Bridge — Product Overview
description: An introduction to AV Bridge — what it does, how it works, and the feature breakdown for AV operations, IT decision-makers, and buyers evaluating a modern AV monitoring platform.
audience: All buyers, sales cover pages, RFI responses
status: Draft v0.2 · needs marketing polish
---

# AV Bridge

**Real-time monitoring, control, and automation for corporate AV estates — from a single cloud portal.**

Involve Cloud's AV Bridge platform gives AV operations, service desks, and IT teams one place to see the health of every meeting room, video conference endpoint, display, and control system across every site — and to act on issues before users notice them.

---

## Who it's for

- **Corporate AV & UC teams** managing rooms across multiple offices, campuses, or regions.
- **Higher education** running lecture theatres, huddle spaces, and events venues at scale.
- **Managed service providers** running AV estates on behalf of end customers.
- **Facilities and IT service desks** that own the room-experience ticket queue.

## Why it matters

| Without AV Bridge | With AV Bridge |
|---|---|
| Users report the failure first | You know before the meeting starts |
| Mixed vendor tooling — one console per brand | One console for the whole fleet |
| Nightly checks are a Monday-morning walk-around | Nightly checks run themselves and file tickets |
| Rooms drift from working configuration | Config sync catches drift the same day |
| Fleet data trapped in the tool | Fleet data flows to ITSM, BI, and CMDB |

---

## Feature breakdown

### 1. User access & role flexibility

AV Bridge is built for organisations where "who can do what" isn't a one-size-fits-all decision. The permission model is fine-grained, per tenant, and combines with physical scope so a user's authority is bounded to their remit.

- **Per-tenant role catalogue** — customers can define their own roles beyond the three seeded defaults (admin, operator, viewer). Roles never leak between tenants.
- **Fine-grained permissions** — 22+ discrete permission keys covering reads, device commands, bulk actions, alert lifecycle, hierarchy CRUD, user and role management, notifications, firmware targets, and audit access. Compose roles from whichever bundle a customer needs.
- **Multi-role users** — a single user may hold multiple roles simultaneously; effective permissions are the union. Common pattern: *"London Operator + Global Viewer"*.
- **Physical scope** — restrict a user to specific buildings, orthogonal to their role. Roles say *what*, scope says *where*. Perfect for regional operators, on-site engineers, or MSPs handling multiple end customers.
- **Microsoft Entra ID SSO** with **group-to-role mapping** — assign roles based on the customer's own Entra groups; no double-maintenance of user directories.
- **Local users** for evaluation, break-glass, and small deployments without an Entra tenant.
- **Per-scope Public API tokens** — issue tokens with any subset of the read permission catalogue; revocable and expiring. One-integration-one-token, not shared credentials.
- **Full audit trail** — every user, token, and Collector action is logged with actor, action, target, and timestamp. Tenant-scoped and readable only by that tenant's authorised roles.

### 2. Fleet monitoring & visibility

- **Live device state** across every room, every site — codecs, displays, matrices, DSPs, touch panels, PDUs, cameras, control processors.
- **Building → Room → Device hierarchy** for physical navigation of the estate.
- **Collector-aware status** — when a Collector goes offline, its devices project to *unknown*, not stale *online*. Operators go check the Collector, not the wrong device.
- **Historical telemetry** — status, metrics, and event trends over time.
- **Filter, search, tag** across the fleet.
- **Nightly digest** email summarising fleet health, issues, and follow-ups.
- **Reports & exports** on device availability, alert volume, and command activity.

### 3. Command & control

- **Sub-second portal-to-device dispatch** — actions land on the device in typically under one second, not on a polling tick.
- **Bulk fan-out** — issue one action against many devices at once (a room, a floor, a site, or a saved selection).
- **Vendor-native controls** — power on / off, mute, reboot, reconnect, and vendor-specific custom commands.
- **Command history + audit log** — every dispatched action is recorded with actor, target, timing, and outcome.

### 4. Alerting & incident response

- **Configurable thresholds** per device or device class.
- **Flap suppression** — a device that briefly loses reachability doesn't page anyone until the state is stable.
- **Escalation routes** — different first-line, second-line, and out-of-hours channels.
- **Alert lifecycle** — acknowledge → investigate → resolve, with the audit trail attached.
- **Help desk console** — a dedicated view for service-desk agents handling room incidents.
- **Multi-channel notification** — email today, with webhooks and additional channels on the roadmap.

### 5. Automation & routines

- **Scheduled routines** — nightly health checks, weekend power-downs, Monday-morning warm-ups, room-open sequences.
- **Config sync** — the desired configuration for every device lives in the cloud; the Collector reconciles it and flags drift.
- **Reconnect on demand** — send a device a controlled disconnect / reconnect to clear a wedged session without a truck-roll.

### 6. Extensibility & integrations

- **Vendor-agnostic adapter layer** — Poly VideoOS, Sony Bravia Professional, Biamp Tesira, ATEN eco PDU, Aurora RXT touch panels, Aurora VPX AV-over-IP, VISCA-over-IP PTZ cameras, and more per release.
- **Custom devices via generic transports** — REST, WebSocket, Telnet, and Serial adapters cover any device with a documented control interface.
- **Public REST API v1** with **OpenAPI 3.1 + Swagger UI** — every field visible in the portal is available via the API.
- **CMDB endpoint** — the assets surface is first-class in the API, ready for ServiceNow, Jira, or your own CMDB.
- **Cursor pagination, bearer-token auth** — designed for machine consumption at scale.
- **Adapter SDK** — customers and partners can add new device types without waiting for a platform release.

### 7. Deployment & platform

- **Cross-platform Collector** — Linux (Ubuntu, RHEL, Debian), Windows Server, ARM64.
- **Flexible topology** — per-site Collector for isolation and low latency, or one shared Collector serving multiple sites over your corporate WAN. Both models are supported side by side.
- **One-line install** — the portal generates a signed install command; enrolment and initial config happen in the same step.
- **Automatic config sync** — updates cascade from the cloud to every Collector without a manual roll-out.
- **UK / EU hosted** on AWS. Multi-tenant with strict row-level isolation.

### 8. Security & compliance

- **Zero inbound firewall rules** at any customer site.
- **TLS 1.2+** on every connection.
- **HMAC-SHA256 signing** of every Collector-to-Cloud request — device identity verified per request.
- **Row-level tenant isolation** enforced by the database, not the application.
- **Microsoft Entra ID SSO** with MFA at customer policy.
- **Full audit trail** tenant-scoped and readable by authorised roles only.
- **GDPR-compliant** with UK / EU data residency. SOC 2 and ISO 27001 [TBC — roadmap dates].

See *AV Bridge — Security & Trust* and *AV Bridge — Data Residency & Retention* for full detail.

---

## How it works

1. A small **Collector** service sits on your network. It speaks every AV vendor's native protocol locally — no changes to your devices, no cloud reach into your AV VLAN.
2. The Collector makes one outbound HTTPS connection to the **Involve Cloud** — the *only* firewall rule you add.
3. Your teams work in the **portal** (SSO via Microsoft Entra ID), and your systems consume the **Public API** via revocable bearer tokens.

Zero inbound firewall rules. No public exposure of AV devices. UK / EU hosted.

## What makes AV Bridge different

- **Vendor-neutral by design.** The adapter layer is open and extensible — new device types are added without breaking existing rooms.
- **Sub-second command dispatch.** Portal actions land on the device in under a second — no polling delay.
- **Role flexibility that matches the real org chart.** Per-tenant role catalogue, multi-role users, and building-level scope. Not three hardcoded personas.
- **Multi-tenant to the row level.** Isolation is enforced at the database, not the application — critical for MSPs and multi-BU enterprises.
- **API-first.** Every field visible in the portal is available via the Public API. No feature gap between what humans see and what systems can consume.

## What's next

- Talk to us: **[TBC — sales@involve.vc or similar]**
- Read the datasheet: **[link to datasheet]**
- Book a demo: **[link]**

---

*Involve Cloud · AV Bridge Platform · Product Overview v0.2 · [YEAR]*
