---
title: M.A.R.C.U.S. — Service Description & SLA
product: M.A.R.C.U.S.
vendor: Involve Visual Collaboration Ltd
website: https://involve.vc
version: 1.0
status: General Availability
audience: Procurement, commercial contacts, service management
---

<!-- ============================================================
  M.A.R.C.U.S.
  Managed · Assets · Resources · Control · Updates · Status
  Involve Visual Collaboration Ltd · involve.vc
============================================================ -->

# M.A.R.C.U.S. — Service Description & SLA

**Managed · Assets · Resources · Control · Updates · Status**

This document defines the service Involve Visual Collaboration Ltd provides for the M.A.R.C.U.S. platform: what is included, how availability is measured, how incidents are categorised, response and restoration commitments, planned maintenance, and how the service is contracted.

## 1. Service description

M.A.R.C.U.S. is a cloud-managed monitoring, control, and automation platform for AV and video collaboration estates, delivered as a multi-tenant SaaS service by Involve.

### 1.1 Service components

| Component | Responsibility |
|---|---|
| **M.A.R.C.U.S. Cloud** | Operated by Involve on AWS. Portal, Public API, ingest, alerting, automation, historical analytics. |
| **M.A.R.C.U.S. Collector** | Software supplied by Involve; installed on customer-provided host(s). Speaks native vendor protocols to AV devices. |
| **Portal** | Web application, accessed via any modern browser over the internet. Microsoft Entra ID SSO. |
| **Public API** | REST at `/pub/v1`. Bearer-token authenticated. OpenAPI 3.1 specification. |
| **Documentation portal** | Product documentation, admin guides, user guides, release notes, and status page. |

### 1.2 What Involve provides

- Continuous operation of M.A.R.C.U.S. Cloud in AWS London (`eu-west-2`).
- Signed Collector binaries for Linux (Ubuntu, RHEL, Debian) amd64 / arm64 and Windows Server amd64.
- Monthly minor releases and quarterly major releases.
- Security patches on an out-of-band cadence when required.
- Public status page, incident communication, and post-incident reviews.
- Support during defined hours as per the customer's service tier.

### 1.3 What the customer provides

- Collector host(s) meeting the *M.A.R.C.U.S. — Datasheet* specification.
- Outbound HTTPS access from Collector host(s) to `*.involvecloud.com` on TCP 443.
- Vendor credentials for any AV devices requiring authenticated control.
- A named tenant administrator to receive service communication and manage users.
- Microsoft Entra ID tenant configuration for SSO (optional; local users supported).

## 2. Service tiers

| Feature | Standard | Enterprise |
|---|---|---|
| **Availability target** | 99.5% monthly | 99.9% monthly |
| **Support hours** | UK business hours (08:00 – 18:00 GMT/BST, Mon–Fri) | 24×7 for P1; UK business hours for P2–P4 |
| **P1 response** | 1 business hour | 30 minutes |
| **P2 response** | 4 business hours | 4 business hours |
| **P3 response** | 1 business day | 1 business day |
| **P4 response** | 5 business days | 5 business days |
| **Named support contact** | — | Included |
| **Quarterly service review** | — | Included |
| **Custom KMS keys (BYOK)** | — | Available on request |

## 3. Availability commitment

### 3.1 Definition of availability

**Available** means the M.A.R.C.U.S. Cloud portal, Public API, and ingest endpoint are reachable and return successful HTTP responses to representative synthetic requests originated by Involve monitoring.

### 3.2 Availability calculation

Monthly Availability % = ((Total Minutes − Downtime Minutes) / Total Minutes) × 100

**Downtime Minutes** counts continuous unavailability of the portal, Public API, or ingest endpoint of ≥ 5 minutes duration, measured by Involve synthetic monitoring, excluding:

- Scheduled maintenance windows announced ≥ 5 business days in advance.
- Emergency maintenance for security patches (customer notified as far in advance as circumstances allow).
- Unavailability caused by customer action or customer-side network / firewall changes.
- Force majeure events.
- Unavailability of the customer's Entra ID tenant, corporate network, or Collector host.

### 3.3 Service credits (Enterprise)

If Monthly Availability falls below the target:

| Monthly Availability | Service credit |
|---|---|
| < 99.9% and ≥ 99.0% | 5% of monthly fee |
| < 99.0% and ≥ 95.0% | 10% of monthly fee |
| < 95.0% | 25% of monthly fee |

Service credits are the customer's sole and exclusive remedy for availability failures. Credits are requested in writing to commercial@involve.vc within 30 days of the affected month.

## 4. Incident severity

### 4.1 Definitions

| Severity | Definition | Examples |
|---|---|---|
| **P1 — Critical** | M.A.R.C.U.S. Cloud is unreachable or entirely non-functional. Customer cannot perform any monitoring or control across their estate. | Portal returns 5xx on every page. Public API entirely unresponsive. Ingest pipeline down for > 10 minutes. |
| **P2 — Major** | Significant feature or function is impaired. Workaround is impractical or requires substantial manual effort. | Alerting engine not delivering notifications. Command dispatch failing for a majority of devices. A single tenant unable to sign in. |
| **P3 — Minor** | Feature is degraded but usable; workaround is available. | Reports generating slowly. A specific adapter returning stale telemetry. Portal UI rendering issue that doesn't block workflow. |
| **P4 — Cosmetic / Enhancement** | Cosmetic issue, documentation gap, or feature request. | Typographic error. Small UI polish. Request to add a new dashboard filter. |

### 4.2 Response and restoration commitments

**Response** = an Involve engineer has acknowledged the incident and begun investigation. **Restoration** = customer function is restored, either by fix or by workaround.

| Severity | Response (Standard) | Response (Enterprise) | Target restoration |
|---|---|---|---|
| P1 | 1 business hour | 30 minutes (24×7) | 4 hours |
| P2 | 4 business hours | 4 business hours | 1 business day |
| P3 | 1 business day | 1 business day | 5 business days |
| P4 | 5 business days | 5 business days | Next scheduled release |

Response is measured from ticket receipt (email or portal). Response and restoration times exclude time waiting on the customer for information or approval.

## 5. Support channels

| Channel | Purpose | Hours |
|---|---|---|
| **support@involve.vc** | Primary support channel. All severities. | 24×7 receipt; response per SLA. |
| **Portal ticketing** | Alternative to email; ties the ticket to the tenant and reporter automatically. | 24×7 receipt; response per SLA. |
| **status.involvecloud.com** | Real-time incident and maintenance information. | 24×7 |
| **security@involve.vc** | Security disclosures and questions. | 2 business day acknowledgement. |
| **Named support contact (Enterprise)** | Direct-dial phone / Teams to the assigned engineer during UK business hours. | UK business hours. |

## 6. Planned maintenance

- **Standard maintenance window:** Wednesdays, 21:00 – 23:00 GMT/BST. Not every window is used; used windows are announced ≥ 5 business days in advance on the status page and by email to the primary tenant admin.
- **Emergency maintenance:** may be performed outside the standard window when required to address a security vulnerability or protect service integrity. Customers are notified as far in advance as circumstances permit.
- **Monthly minor releases** are deployed via rolling ECS Fargate updates with zero-downtime intent; brief service degradation (single-request retries) is possible during rollout and does not count as downtime.

## 7. Change management

- **Backward-compatible changes** (new fields, new endpoints, new UI surfaces) ship in monthly minor releases without customer action required.
- **Breaking changes to the Public API** are subject to a 6-month deprecation window with visible warnings and documentation. See *M.A.R.C.U.S. — Release & Upgrade Policy* for full detail.
- **Collector updates** are opt-in per collector — the customer chooses when to update the on-premise Collector binary. Backward compatibility with the current cloud release is guaranteed for the two most recent Collector minor versions.

## 8. Data and confidentiality

- Customer data is stored, processed, and backed up exclusively in the AWS London region (`eu-west-2`).
- Data retention (default): telemetry 90 days; alerts 12 months; audit log 24 months; database backups 35 days point-in-time recovery.
- A signed Data Processing Agreement is available on request; contact privacy@involve.vc.
- Involve does not use customer data for training, marketing, or any purpose beyond delivering the service.

## 9. Termination and data return

- Either party may terminate for material breach on 30 days' written notice, subject to opportunity to cure.
- On termination the customer may request an export of their data via the Public API for 30 days after termination.
- Involve deletes customer data within 90 days of termination; backup copies are aged out on the standard 35-day PITR cycle.

## 10. Contact

- **Commercial:** commercial@involve.vc
- **Support:** support@involve.vc
- **Security:** security@involve.vc
- **Privacy / DPA:** privacy@involve.vc
- **Status page:** [status.involvecloud.com](https://status.involvecloud.com)

---

*Involve Visual Collaboration Ltd · M.A.R.C.U.S. Platform · Service Description & SLA · v1.0*
*[involve.vc](https://involve.vc)*
