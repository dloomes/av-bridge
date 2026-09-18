---
title: AV Bridge vs MoJ AVRMM Requirements — Gap Analysis
description: Assessment of AV Bridge against the accepted V1.0 AVRMM requirements shared by Vodafone / MoJ, with strategic gaps and proposal-posture recommendations.
audience: Involve product + commercial; Vodafone technical + commercial (once sanitised)
status: Internal working document · updated 2026-09-18 (FR39 + FR40 shipped)
source: docs/AV RMM Requirements - Vodafone - MoJ Shared v1.0.xlsx
companion: C:\Users\DLoomes\Documents\AV-Bridge-vs-MoJ-AVRMM-Gap-Analysis.xlsx
---

# AV Bridge vs MoJ AVRMM Requirements — Gap Analysis

Assessed against the V1.0 requirements accepted by MoJ (August 2026). 22 NFRs + 44 FRs across the categories NFR, Health Scripts, and RBAC.

Full per-requirement assessment lives in the companion Excel workbook at `C:\Users\DLoomes\Documents\AV-Bridge-vs-MoJ-AVRMM-Gap-Analysis.xlsx`. This document is the strategic overview.

## Shipped since last review

**2026-09-18** — FR39 and FR40 shipped to UAT (image tag `ab32651`):

- **FR39** (defer scheduled power-down for a room in use) — new `POST /api/v1/nightly/rooms/{id}/defer-tonight`, a portal "Defer tonight" button per room row, gated on a new `nightly.defer` permission that operators hold by default. The exclusion self-clears the next day.
- **FR40** (exclude a room from scheduled health-checks for a documented reason) — migration `0042` added `excluded_reason` (CHECK enum: `in_use`, `active_incident`, `awaiting_replacement`, `planned_maintenance`, `other`) and `excluded_note`. Portal room-override modal exposes both; row status column shows the reason chip with the operator note as tooltip.
- **FR41** was already MET — the reverse of FR40, unchanged.

Both requirements move from **PARTIAL → MET** in the counts below.

## Headline

| Bucket | Count | Interpretation |
|---|---|---|
| **MET** | ~35 | Feature shipped and demonstrable today. |
| **PARTIAL** | ~22 | Substantially met — small enhancement, per-adapter dependency, or configuration. |
| **GAP** | ~2 | Material capability missing. |
| **N/A** | ~7 | Commercial / supplier-delivery scope, not a platform capability. |

Of the platform-facing requirements (~59), roughly **59% MET, 37% PARTIAL, 3% material GAP**. That's a strong starting position, and the remaining partials are heavily concentrated in vendor coverage and hierarchy — both known and closable.

## Top strategic gaps

### 1. Crestron adapter coverage (FR02, FR03, FR16, FR19)

Aurora, Poly, Sony, Biamp, VISCA and ATEN are shipped. **Crestron is not.** FR02 names Crestron explicitly and Crestron dominates most UK gov AV estates.

**Recommendation:** commit to a Crestron control-processor + DM matrix + TSW-panel adapter as a headline delivery item bundled into the pilot. Well-documented vendor protocols; achievable inside a pilot timeline. Confirm exact device inventory with Vodafone first so scope is precise.

**Effort:** L (multiple months, one vendor family)

### 2. Cloud hosting — GCP vs AWS (FR05)

MoJ / Vodafone want the AVRMM colocated with the Core Video Platform in **Google Cloud Platform**. AV Bridge runs on **AWS UK / EU**.

**Recommendation:** engage MoJ / Vodafone early to determine the real position:

- If SaaS-on-AWS is acceptable given UK data residency, portability, and the option to migrate later — no build required. Preferred first option.
- If GCP colocation is a hard requirement, plan a phased migration (Terraform stack + Docker containers make this feasible but not trivial).
- Third option — commit to a future migration to the MoJ GCP tenant via data export, once operationally proven.

**Effort:** L if migration required, S if SaaS-on-AWS accepted.

### 3. Business-Unit / Region hierarchy above building level (NFR13, NFR14, NFR16, NFR17, FR42)

Today: **Buildings > Rooms > Devices**, with per-tenant role catalogue and building-level physical scope. MoJ wants **BU (HMCTS / HMPPS / Probation) > Region > Site > Room** with role scoping at each level, and BU-configurable naming (Court vs Prison).

**Recommendation:**

- **Short term:** model each BU as a separate tenant (row-level isolation is a strength here). Use tags for Region.
- **Medium term:** add configurable hierarchy nodes for Region and BU with per-BU 'friendly names' — this unlocks the natural inheritance semantics of FR42.

**Effort:** M (schema + UI, no data-model surgery).

### 4. Cross-BU / MSP-style admin role for Supplier SNOC (NFR14, NFR22)

Vodafone SNOC engineers need visibility across every BU. Row-level tenant isolation is a strength but makes cross-tenant admin non-native — today the SNOC would need a separate account per tenant.

**Recommendation:** add an **MSP administrator** role that spans tenants for the SNOC. Admin pool separation already exists in the codebase; the RLS boundary can be bypassed for a specific role.

**Effort:** M.

### 5. Network-switch PoE control (FR15, FR27)

MoJ wants **PoE port up/down control from the network switch itself**, not only via vendor devices. AV Bridge has vendor-native power today (Sony WoL, Poly, ATEN PDU) but no switch PoE port control.

**Recommendation:** scope switch inventory with Vodafone; add adapter(s) for the specific switch family (Cisco IOS-XE, HP ProCurve, Aruba, etc.).

**Effort:** M per switch family.

## Other gaps worth naming

- **Bulk firmware push (FR12)** — monitoring firmware versions is universal; pushing firmware in bulk is per-vendor and today shipped only where the vendor API supports it (Poly). Honest positioning: universal monitoring, per-vendor push commits.
- **Recovery state machine + ITSM webhook (NFR07, FR36, FR37)** — routines + alerts + ITSM cookbook work today. Chained 'if X fails try Y' recovery and direct webhook to the Supplier's incident system are enhancements — v2 routine builder + webhook alert channel.
- **Dashboard customisation (FR23)** — saved filter views are the current answer; widget-based user-composed dashboards are roadmap.
- **SCIM auto-provisioning (NFR21)** — Entra group-based role assignment covers most JML; SCIM is not shipped. Often accepted as equivalent, but MoJ may mandate SCIM.
- **Reporting library — warranty and utilisation (FR25)** — nightly digest + assets + uptime today. Warranty (as first-class asset field) and utilisation report templates need adding.

## Genuine strengths to lead with

- **Role flexibility that matches the real org chart** — per-tenant role catalogue, 22 fine-grained permissions, multi-role users, building-level physical scope. Directly supports NFR13 / NFR18 / FR20 / FR42 / FR43 / FR44.
- **Row-level tenant isolation at the database** — audit-verifiable segregation for NFR14 / NFR15. Rare enough to be a differentiator in this market.
- **Outbound-only Collector-to-Cloud architecture** — zero inbound firewall rules at any customer site. Simplifies FR28 network-integration story significantly.
- **Sub-second command dispatch** — LISTEN/NOTIFY long-poll gives portal-to-device round-trip under one second. Direct match for FR15 / FR19 / FR38.
- **Cross-platform Collector** — Linux, Windows, ARM64. Matches diverse deployment needs.
- **Public REST API v1** — 11+ endpoints, OpenAPI 3.1, bearer tokens. Direct match for FR22 (ServiceNow / Dynatrace integration).
- **SaaS with UK / EU hosting, HMAC-signed channel, Entra ID SSO** — the security posture answers the majority of NFR / FR security requirements out of the box.

## Recommended proposal posture

1. **Lead with the ~35 MET requirements** — the shipped surface is broad, and the tooling to demonstrate it exists (portal, API, adapter catalogue, security whitepaper).
2. **Frame the Crestron adapter as a scoped pilot deliverable** — well-documented protocols, achievable timeline, dependency on Vodafone confirming exact inventory.
3. **Ask early about AWS vs GCP** — it's the largest strategic gap and has commercial implications. Get MoJ / Vodafone position before committing.
4. **Commit to the BU / Region hierarchy extension** — small schema + UI change, unlocks half the RBAC requirements at their preferred altitude.
5. **Commit to network-switch PoE adapter** — tied to confirmed switch inventory.
6. **Position Health Scripts as routines** — the routine engine is the answer to FR33 – FR41. Commit to routine-builder v2 (conditional branches) + webhook alert channel to cover FR36 / FR37 depth.
7. **Ask for the PoC (FR31) explicitly** — 50 rooms across 10 sites is exactly the scale where AV Bridge shines and the sizing story lands cleanly.

## Assumptions and things to confirm with Vodafone / MoJ

- **Identity provider** — SSO integration mentions "MoJ Identity services". Assumed Entra ID; if not, generic OIDC needed. Confirm.
- **AVoIP inventory** — Aurora VPX shipped, but the MoJ AVoIP mix beyond Aurora is unclear. Get inventory to size adapter scope.
- **Switch inventory** — for the PoE adapter scope.
- **Cloud hosting position** — is AWS-UK-SaaS acceptable given the FR05 GCP wording, or is GCP colocation with Core Video Platform a hard constraint?
- **BU set** — HMCTS, HMPPS, MoJ, Probation named; are there more? Impacts the tenant / hierarchy modelling.
- **Volume** — total room count and device count across the estate. Impacts sizing story and Collector topology.
- **JML mechanism** — Entra group changes vs SCIM. If SCIM is mandated, scope accordingly.
- **3LS tools in scope** — Poly Lens shipped. Crestron XiO, Cisco Control Hub, others?
- **Reporting library expectations** — which specific reports beyond asset / firmware / warranty / utilisation?
