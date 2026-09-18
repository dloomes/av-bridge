---
title: AV Bridge — Data Residency & Retention
description: Where customer data is stored, what is collected, how long it is retained, and how it can be exported or deleted. GDPR-oriented statement for customer InfoSec, DPO, and procurement audiences.
audience: Data Protection Officers, procurement, information security
status: Draft v0.1 · needs Data Protection Officer + legal sign-off on items marked [TBC]
---

# AV Bridge — Data Residency & Retention

This document describes where AV Bridge stores customer data, what data is collected, how long it is retained, and the mechanisms available for export and deletion. It is written to support customer GDPR obligations and to accelerate procurement and information-security review.

This document is a statement of fact about the AV Bridge platform. Contractual protections (Data Processing Agreement, sub-processor commitments, breach-notification obligations) are documented separately — see the DPA and the *Security & Trust* whitepaper.

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

- **The customer** is the **Data Controller** — the customer determines the purposes and means of the processing carried out through AV Bridge.
- **Involve Cloud** is the **Data Processor** — Involve Cloud processes personal data only on the customer's documented instructions, as set out in the Data Processing Agreement.

## 2. Where data is stored

All customer data is stored in the **United Kingdom / European Union** [TBC — confirm exact region(s), e.g. `eu-west-2` London] on **Amazon Web Services** infrastructure.

| Category | Storage | Region |
|---|---|---|
| Application database (users, devices, alerts, telemetry, audit log) | Amazon RDS for PostgreSQL | UK / EU [TBC] |
| Object storage (attachments, exports, log archives) [TBC — confirm scope] | Amazon S3 | UK / EU [TBC] |
| Application logs (operational) | Amazon CloudWatch Logs | UK / EU [TBC] |
| Portal static assets | AWS Amplify + CloudFront | Global CDN (edge cache); origin in UK / EU |

No customer data is stored outside the region unless the customer explicitly enables a feature that requires it (e.g. integration with a customer-nominated third-party system). Such features are opt-in and disclosed at time of enablement.

## 3. What data is collected

AV Bridge processes three categories of data.

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
- Device status and telemetry snapshots (recorded over time)
- Alert history and acknowledgement records
- Command history (portal operator actions, API-issued commands)
- Room, building, and site metadata

Device and telemetry data is **not personal data** in itself, but may in some deployments be indirectly linked to individuals — for example, an alert acknowledged by a named service-desk agent. Those linkages are captured in the audit log.

### 3.3 Audit and operational logs

- **Audit log** — every security-relevant action by users, tokens, and Collectors, with actor, action, target, and timestamp. Tenant-scoped and only readable by that tenant's authorised roles.
- **Operational logs** — application-tier request logs, error traces, and performance metrics. Do not contain user credentials or full request payloads by default [TBC — confirm log redaction policy].

## 4. Retention periods

Default retention. Customers can request longer or shorter periods on written request; the platform provides tenant-scoped export and delete tooling in both cases.

| Data | Default retention | Rationale |
|---|---|---|
| Live device state | Indefinite while device exists | Product function |
| Device telemetry (historical time series) | [TBC — 12 months proposed] | Reporting, trend analysis |
| Alert history | [TBC — 24 months proposed] | Incident review, SLA reporting |
| Command history | [TBC — 12 months proposed] | Operational audit |
| Audit log | [TBC — 12 months minimum proposed] | Security compliance |
| Operational logs | [TBC — 90 days proposed] | Operational triage |
| Automated database backups | [TBC — 35 days proposed] | Disaster recovery |
| User account (dormant) | [TBC — deactivate after 12 months of inactivity, hard-delete after further 6 months] | Data minimisation |

Backups are encrypted with the same key material as the primary database, retained on the same regional footprint, and destroyed on their normal schedule after account termination.

## 5. Data subject rights and deletion

### 5.1 Access

Customer administrators can access all tenant-scoped data through the portal and Public API. Individual users can view their own audit record via [TBC — portal path].

### 5.2 Rectification

Directory data can be corrected by the customer administrator (or by the source identity provider, for federated fields). Operational data (device names, tags, location) is customer-editable in the portal.

### 5.3 Erasure

- **Individual user** — customer administrators can remove a user from the portal at any time. Removal revokes access immediately. Historical audit records referencing that user are retained for the audit-log retention period; the user identifier is replaced with a stable pseudonym on request, so the audit trail remains intact but the personal identifier is removed.
- **Tenant termination** — on written request from the customer, all customer data (application data + backups on their normal expiry) is deleted from the platform. A signed deletion certificate is provided within [TBC — SLA — recommend 30 days].

### 5.4 Data portability

- Full **Public API export** — every tenant-scoped resource is exposed via the Public API, so customers can programmatically extract their data in JSON at any time.
- **Bulk export on termination** — on request, a single archive of the tenant's data can be provided in JSON / CSV / SQL formats.

### 5.5 Objection and restriction

The customer, as controller, is responsible for handling objection and restriction requests from data subjects. The platform supports both by disabling the user account or removing the user record.

## 6. Cross-border transfers

Involve Cloud stores and processes customer data in the **United Kingdom / European Union**. No routine transfer of customer data occurs outside the UK / EEA.

Sub-processors used to support the service (see the *Security & Trust* whitepaper and the public sub-processor list) may be established outside the UK / EEA. Where any such sub-processor could receive personal data, the transfer is governed by the **UK International Data Transfer Addendum** and / or the **EU Standard Contractual Clauses** (2021), as appropriate.

The current sub-processor list, including region of processing and safeguards, is published at **[TBC — public URL]**.

## 7. Contact

- **Privacy and data protection queries:** [TBC — privacy@involve.vc]
- **Data Protection Officer:** [TBC — appointed / not appointed; if appointed, name and contact]
- **Data subject rights requests:** [TBC — how to submit]
- **Data Processing Agreement:** available on request from [TBC — legal@involve.vc]
- **Sub-processor list:** [TBC — public URL]

---

*Involve Cloud · AV Bridge Platform · Data Residency & Retention v0.1 · [YEAR]*
