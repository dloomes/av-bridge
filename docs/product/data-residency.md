---
title: M.A.R.C.U.S. — Data Residency & Retention
description: Where customer data is stored, what is collected, how long it is retained, and how it can be exported or deleted. GDPR-oriented statement for customer InfoSec, DPO, and procurement audiences.
audience: Data Protection Officers, procurement, information security
status: Draft v0.1 · needs Data Protection Officer + legal sign-off on items marked [TBC]
---

# M.A.R.C.U.S. — Data Residency & Retention

This document describes where M.A.R.C.U.S. stores customer data, what data is collected, how long it is retained, and the mechanisms available for export and deletion. It is written to support customer GDPR obligations and to accelerate procurement and information-security review.

This document is a statement of fact about the M.A.R.C.U.S. platform. Contractual protections (Data Processing Agreement, sub-processor commitments, breach-notification obligations) are documented separately — see the DPA and the *Security & Trust* whitepaper.

## Contents

1. Roles
2. Where data is stored
3. What data is collected
4. Retention periods
5. Data subject rights and deletion
6. Cross-border transfers
7. Contact

---

## 1. Roles

For the purposes of the UK GDPR and the EU GDPR:

- **The customer** is the **Data Controller** — the customer determines the purposes and means of the processing carried out through M.A.R.C.U.S.
- **Involve Visual Collaboration Ltd** ("Involve") is the **Data Processor** — Involve processes personal data only on the customer's documented instructions, as set out in the Data Processing Agreement.

## 2. Where data is stored

All customer data is stored and processed in the **United Kingdom**, in the **AWS London region (`eu-west-2`)**, on **Amazon Web Services** infrastructure.

| Category | Storage | Region |
|---|---|---|
| Application database (users, devices, alerts, telemetry, audit log) | Amazon RDS for PostgreSQL | UK — AWS London (`eu-west-2`) |
| Object storage (attachments, exports, log archives) [TBC — confirm scope] | Amazon S3 | UK — AWS London (`eu-west-2`) |
| Application logs (operational) | Amazon CloudWatch Logs | UK — AWS London (`eu-west-2`) |
| Database backups and point-in-time recovery | Amazon RDS automated backups | UK — AWS London (`eu-west-2`) |
| Portal static assets (no customer data) | AWS Amplify + CloudFront | Global CDN (edge cache); origin in UK — AWS London (`eu-west-2`) |

Customer data is not stored outside the London region, with these exceptions:

- **Optional Multi-Region Enterprise add-on** — an encrypted copy of the customer's database snapshots is held in AWS Ireland (`eu-west-1`). It is enabled only with the customer's explicit written agreement and recorded in their Data Processing Agreement. See *Business Continuity & Disaster Recovery*, §5.3.
- **Sub-processors** listed in the *Security & Trust* whitepaper, §12 — for example, the status page and internal on-call alerting providers process data in the EU.
- **Customer-enabled integrations** — features that send data to a customer-nominated third-party system (for example a webhook or a Microsoft Teams channel) are opt-in and disclosed at time of enablement.

## 3. What data is collected

M.A.R.C.U.S. processes three categories of data.

### 3.1 Directory / identity data

Personal data used to authenticate and authorise users.

| Field | Source | Purpose |
|---|---|---|
| Email address | Entra ID SSO or local invite | Sign-in, notifications |
| Display name | Entra ID SSO or local invite | UI identification, audit records |
| Entra ID object identifier | Entra ID SSO | Federation binding |
| Role assignment | Set by customer administrator | Authorisation |
| Last-sign-in timestamp | Sign-in flow | Security review, dormant-account handling |

The platform does not store passwords for federated users. For local (non-federated) accounts, only the hashed password is retained.

### 3.2 Device and telemetry data

Operational data about AV devices, rooms, and their state. This is the primary purpose of the platform.

- Device identification (name, model, IP address, location, tags)
- Device status and telemetry readings (recorded over time), and daily summaries derived from them
- Device events reported by devices and Collectors
- Alert history and acknowledgement records
- Command history (portal operator actions, scheduled routines, API-issued commands)
- Room Readiness run history (nightly power and test-routine results per room)
- Remote access session records (which administrator opened which device's web interface, when, for how long, and how many bytes were transferred; session content is not recorded)
- Room, building, and site metadata

Device and telemetry data is **not personal data** in itself, but may in some deployments be indirectly linked to individuals — for example, an alert acknowledged by a named service-desk agent, or a remote access session opened by a named administrator. Those linkages are captured in the audit log.

### 3.3 Audit and operational logs

- **Audit log** — every security-relevant action by users, tokens, and Collectors, with actor, action, target, and timestamp. Tenant-scoped and only readable by that tenant's authorised roles.
- **Operational logs** — application-tier request logs, error traces, and performance metrics. Do not contain user credentials or full request payloads by default [TBC — confirm log redaction policy].
- **Collector logs** — what each on-site Collector logs about its own operation (device connections, errors, updates), sent with its telemetry. They can include device names, IP addresses and error messages from devices. Passwords, keys and tokens are never logged and are removed if they appear; usernames are masked to their first letter. Tenant-scoped and only readable by users with the View Collector logs permission and by Involve support.

## 4. Retention periods

Default retention. Customers can request longer or shorter periods on written request; the platform provides tenant-scoped export and delete tooling in both cases.

| Data | Default retention | Rationale |
|---|---|---|
| Live device state | Indefinite while device exists | Product function |
| Raw device telemetry (historical time series) | 90 days (configurable) | Recent trend analysis, troubleshooting |
| Daily telemetry summaries (per device) | 400 days | Reporting (reports cover up to 12 months) |
| Device events | 90 days | Troubleshooting, device events report |
| Alert history | 12 months after the alert is resolved; open and acknowledged alerts are kept | Incident review, SLA reporting |
| Command history | 90 days after the command completes | Operational audit |
| Room Readiness run history | 90 days by default; set per customer between 30 and 365 days | Readiness reporting |
| Remote access session records | 90 days after the session ends; the session's audit entries follow the audit log | Operational audit |
| Audit log | 24 months | Security compliance |
| Operational logs (Amazon CloudWatch Logs) | 30 days | Operational triage |
| Collector logs | 30 days from arrival | Collector troubleshooting |
| Automated database backups | 35 days point-in-time recovery | Disaster recovery |
| User account (dormant) | [TBC — deactivate after 12 months of inactivity, hard-delete after further 6 months] | Data minimisation |

Backups are encrypted with the same key material as the primary database, retained on the same regional footprint, and destroyed on their normal schedule after account termination.

## 5. Data subject rights and deletion

### 5.1 Access

Customer administrators can access all tenant-scoped data through the portal, and much of it through the Public API (§5.4). Individual users can view their own audit record via [TBC — portal path].

### 5.2 Rectification

Directory data can be corrected by the customer administrator (or by the source identity provider, for federated fields). Operational data (device names, tags, location) is customer-editable in the portal.

### 5.3 Erasure

- **Individual user** — customer administrators can remove a user from the portal at any time. Removal revokes access immediately. Historical audit records referencing that user are retained for the audit-log retention period; the user identifier is replaced with a stable pseudonym on request, so the audit trail remains intact but the personal identifier is removed.
- **Tenant termination** — on written request from the customer, all customer data is deleted from the platform within 90 days of termination; backup copies age out on the standard 35-day point-in-time recovery cycle. A signed deletion certificate is provided within [TBC — SLA — recommend 30 days].

### 5.4 Data portability

- **Public API export** — the read-only Public API exposes devices and telemetry, alerts, events, assets, locations, buildings and rooms, the audit log, and Room Readiness results, so customers can programmatically extract that data in JSON at any time. Reports also export to CSV from the portal. The customer may request an export via the Public API for 30 days after termination.
- **Bulk export on termination** — on request, a single archive of the tenant's data can be provided in JSON / CSV / SQL formats.

### 5.5 Objection and restriction

The customer, as controller, is responsible for handling objection and restriction requests from data subjects. The platform supports both by disabling the user account or removing the user record.

## 6. Cross-border transfers

Involve stores and processes customer data in the **United Kingdom** (AWS London, `eu-west-2`). No routine transfer of customer data occurs outside the UK. The exceptions are those listed in §2: the optional, customer-agreed Multi-Region Enterprise add-on (AWS Ireland), the global CDN that serves the portal's static files, and the sub-processors.

Sub-processors used to support the service (see the *Security & Trust* whitepaper and the public sub-processor list) may be established outside the UK. Where any such sub-processor could receive personal data, the transfer is governed by the **UK International Data Transfer Addendum** and / or the **EU Standard Contractual Clauses** (2021), as appropriate.

The current sub-processor list, including region of processing and safeguards, is published at **[involve.vc/trust/sub-processors](https://involve.vc)**.

## 7. Contact

- **Privacy and data protection queries:** privacy@involve.vc
- **Data Protection Officer:** [TBC — appointed / not appointed; if appointed, name and contact]
- **Data subject rights requests:** [TBC — how to submit]
- **Data Processing Agreement:** available on request from privacy@involve.vc
- **Sub-processor list:** [involve.vc/trust/sub-processors](https://involve.vc)

---

*Involve Visual Collaboration Ltd · M.A.R.C.U.S. Platform · Data Residency & Retention v0.1 · [YEAR]*
