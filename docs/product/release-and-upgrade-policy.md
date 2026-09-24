---
title: M.A.R.C.U.S. — Release & Upgrade Policy
product: M.A.R.C.U.S.
vendor: Involve Visual Collaboration Ltd
website: https://involve.vc
version: 1.0
status: General Availability
audience: Procurement, integrators, customer change managers
---

<!-- ============================================================
  M.A.R.C.U.S.
  Managed · Assets · Resources · Control · Updates · Status
  Involve Visual Collaboration Ltd · involve.vc
============================================================ -->

# M.A.R.C.U.S. — Release & Upgrade Policy

**Managed · Assets · Resources · Control · Updates · Status**

This document defines how Involve Visual Collaboration Ltd versions, releases, and evolves the M.A.R.C.U.S. platform, and the compatibility commitments customers can rely on.

## 1. Versioning

M.A.R.C.U.S. follows **semantic versioning** (`MAJOR.MINOR.PATCH`) at the product level.

| Version part | Increments when |
|---|---|
| **MAJOR** | An incompatible change is made to the Public API contract, the on-premise Collector protocol, or a documented behaviour on which customers depend. |
| **MINOR** | New functionality is added in a backward-compatible way (new fields, new endpoints, new UI features, new adapters). |
| **PATCH** | Backward-compatible bug fixes and security patches. |

Both M.A.R.C.U.S. Cloud and M.A.R.C.U.S. Collector carry their own version numbers. Cloud and Collector versions are independently released and independently versioned.

## 2. Release cadence

| Track | Cadence | Typical content |
|---|---|---|
| **Minor releases** (`1.x.0`) | Monthly | New features, new adapters, UI improvements, non-breaking API additions. |
| **Major releases** (`x.0.0`) | Quarterly (as needed) | Breaking changes, deprecation removals, platform version upgrades. |
| **Patch / security releases** (`1.x.y`) | Out-of-band | Bug fixes and security vulnerabilities. Deployed as soon as validated. |

Not every scheduled window produces a release — sometimes the calendar month simply does not require one. The status page ([status.involvecloud.com](https://status.involvecloud.com)) is authoritative for what is actually live in production.

## 3. Deployment mechanics

### 3.1 M.A.R.C.U.S. Cloud

- Cloud releases are deployed via rolling **AWS ECS Fargate** updates with zero-downtime intent.
- Deployments happen during the standard maintenance window (Wednesdays 21:00 – 23:00 GMT/BST) unless a security patch requires immediate out-of-band deployment.
- Brief single-request retries are possible during rollout; they do not count as service downtime.
- Every release is announced on the status page and by email to the primary tenant admin.

### 3.2 M.A.R.C.U.S. Collector

- Collectors are **customer-controlled**. Involve publishes signed binaries; the customer chooses when to install them.
- Update mechanism: portal one-liner `curl` install script pulls the current signed binary; systemctl restart to activate.
- Rollback is supported by keeping the previous binary and restarting against it.
- Backward compatibility with the current cloud release is guaranteed for **the two most recent Collector minor versions**.

### 3.3 Portal

- The portal is served from **AWS Amplify**. Amplify builds automatically from the `main` branch on every merged pull request.
- Portal deployments are near-instant (CDN cache invalidation, no downtime).
- Portal releases align with cloud minor releases in most cases.

## 4. Backward-compatibility guarantees

### 4.1 Public API (`/pub/v1`)

- No breaking change to existing endpoints, request shapes, or response shapes within a single MAJOR version.
- New fields may be added to response payloads at any time. Clients must ignore fields they do not recognise.
- Enum values may be added within existing string fields; clients must default gracefully on unknown values.
- Removal of an endpoint, removal of a field, or a change in semantics is a breaking change and follows the deprecation process below.

### 4.2 Collector-to-Cloud protocol

- The Collector protocol (`/bridge/poll`, `/bridge/commands/{id}/result`, `/bridge/config`, ingest) is treated as an API contract.
- Two consecutive Collector minor versions are supported by every Cloud release. This gives customers a ≥ 1-month window to update Collectors after a Cloud release lands.
- Collector protocol changes that require an update are called out in the release notes.

### 4.3 Portal

- Portal user-facing workflows are backward-compatible within a MAJOR version. Redesigns of pages that materially change a workflow are announced in the release notes and, for Enterprise customers, in the quarterly service review.

## 5. Deprecation policy

When a Public API endpoint, field, or documented behaviour is to be removed:

1. **Announcement** — the deprecation is documented in the release notes and marked `Deprecated` in the OpenAPI specification.
2. **Deprecation warning header** — every response from the deprecated endpoint carries a `Deprecation: true` and `Sunset: <ISO date>` header.
3. **Minimum notice period:** 6 months between announcement and removal.
4. **Removal** — happens in a MAJOR release, no earlier than the announced sunset date.

Emergency removal for security reasons overrides the minimum notice period. In that case, the fastest-available mitigation is deployed and customers are informed via the status page and by direct email.

## 6. Feature classification

To eliminate ambiguity in what a customer is contracting for, every feature in M.A.R.C.U.S. is classified as one of:

| Classification | Meaning | Documentation surface |
|---|---|---|
| **Generally Available (GA)** | Shipped, supported, subject to the standard SLA. Ready for production use. | Product Overview, Datasheet, Public API OpenAPI spec, admin & user guides. |
| **Preview** | Shipped in production but not covered by the standard SLA. Behaviour may change without the 6-month deprecation window. | Release notes (marked *Preview*). Not present in the Datasheet. |
| **Roadmap** | Committed for a future release. Not shipped. | Release notes forward section; commercial conversations. Not present in customer-facing collateral. |

Only GA features are described in the *Product Overview* and *Datasheet*. If the sales conversation touches Preview or Roadmap items, they are called out explicitly as such.

## 7. Release notes

- Every release (Cloud and Collector) publishes release notes.
- Release notes cover: new features (GA and Preview), bug fixes, security patches, deprecation announcements, upgrade actions required (if any).
- Location: the customer documentation portal at [docs.involvecloud.com](https://docs.involvecloud.com) and the status page.

## 8. Change communication

| Change type | Channel |
|---|---|
| Monthly minor release | Release notes + status page + email to primary tenant admin |
| Quarterly major release | Release notes + status page + email to all tenant admins ≥ 5 business days ahead |
| Security patch (out-of-band) | Release notes + status page + email; timing as fast as safely possible |
| Deprecation announcement | Release notes + OpenAPI spec update + `Deprecation` response header |
| Sub-processor change | Email to primary tenant admin ≥ 30 days ahead + [involve.vc/trust](https://involve.vc) update |

## 9. Rollback and hotfix

- Every Cloud release is deployable in reverse (rollback) within one maintenance window.
- Hotfixes for Cloud regressions are triaged as P1 and deployed out-of-band within the P1 restoration target.
- Collector rollback is customer-controlled — the previous binary is retained on disk when a new one is installed via the standard update path.

## 10. Long-term support

- Major versions are supported for **12 months** after the release of the next MAJOR version.
- During the support window, security patches and critical bug fixes are backported at Involve's discretion.
- Customers are strongly encouraged to remain within one MAJOR version of the current release for full support.

## 11. Contact

- **Release enquiries:** commercial@involve.vc
- **Bug reports:** support@involve.vc
- **Security-sensitive reports:** security@involve.vc
- **Status and incident history:** [status.involvecloud.com](https://status.involvecloud.com)

---

*Involve Visual Collaboration Ltd · M.A.R.C.U.S. Platform · Release & Upgrade Policy · v1.0*
*[involve.vc](https://involve.vc)*
