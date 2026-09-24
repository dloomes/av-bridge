<!-- ============================================================
  M.A.R.C.U.S.
  Managed · Assets · Resources · Control · Updates · Status
  Involve Visual Collaboration Ltd · involve.vc
============================================================ -->

# M.A.R.C.U.S. — Product Documentation Pack

**Managed · Assets · Resources · Control · Updates · Status**

*One intelligent platform to manage, understand and optimise complex AV and video collaboration estates.*

This folder holds the customer-facing, procurement-ready documentation pack for M.A.R.C.U.S. Every document is v1.0 · General Availability. The full pack sits behind a two-part architecture:

- **M.A.R.C.U.S. Cloud** — multi-tenant SaaS operated by Involve in AWS London (`eu-west-2`).
- **M.A.R.C.U.S. Collector** — on-premise agent on customer-provided host(s); one outbound HTTPS connection to Cloud.

---

## Document set

| # | Document | Audience | Format |
|---|---|---|---|
| 1 | [Product Overview](product-overview.md) | All buyers, executive summary, RFI responses | `.md` + `.docx` |
| 2 | [Datasheet](datasheet.md) | Technical evaluators, integrators | `.md` + `.docx` |
| 3 | [Security & Trust](security-whitepaper.md) | Customer InfoSec, procurement | `.md` + `.docx` |
| 4 | [Service Description & SLA](service-description-and-sla.md) | Procurement, commercial contacts, service management | `.md` + `.docx` |
| 5 | [Release & Upgrade Policy](release-and-upgrade-policy.md) | Integrators, customer change managers | `.md` + `.docx` |
| 6 | [Business Continuity & Disaster Recovery](business-continuity-and-dr.md) | Customer InfoSec, procurement, BCP assessors | `.md` + `.docx` |
| 7 | [Software Bill of Materials & OSS Statement](sbom-and-oss-statement.md) | Customer InfoSec, public-sector assurance | `.md` + `.docx` |

**DOCX versions** are generated to `C:\Users\DLoomes\Documents\MARCUS-*.docx` from these Markdown sources. The generator script lives in the session scratchpad and can be re-run whenever a source doc changes.

### Also in this folder

| Document | Audience | Notes |
|---|---|---|
| [Deployment Guide](deployment-guide.md) | Customer IT / networking | Predates the M.A.R.C.U.S. rebrand; awaits refresh before it moves into the pack proper. |
| [Data Residency & Retention](data-residency.md) | Customer InfoSec, privacy | Predates the M.A.R.C.U.S. rebrand; awaits refresh. Retention numbers are now the source of truth in *Service Description & SLA* and *Security & Trust*. |

---

## Moving to Mintlify

The following surfaces sit outside this pack and belong in the customer documentation portal at [docs.involvecloud.com](https://docs.involvecloud.com) (Mintlify-hosted, source in a companion `docs-marcus/` folder or repo):

- Installation & Deployment Guide (Collector prerequisites, networking, enrolment, upgrade, proxy, certificates, troubleshooting)
- Administrator Guide (tenants, users, RBAC, sites, devices, alerts, integrations)
- User / Operator Guide (day-to-day operations)
- Supported Devices & Compatibility Matrix (per-model, per-firmware — living document)
- Public API Documentation (rendered from the OpenAPI 3.1 spec)
- Release Notes (per-release, controlled)

---

## Reading order

**Buyer / exec:** [Product Overview](product-overview.md) → [Datasheet](datasheet.md)

**Procurement / commercial:** [Datasheet](datasheet.md) → [Service Description & SLA](service-description-and-sla.md) → [Release & Upgrade Policy](release-and-upgrade-policy.md)

**InfoSec / security assessor:** [Security & Trust](security-whitepaper.md) → [Business Continuity & Disaster Recovery](business-continuity-and-dr.md) → [Software Bill of Materials & OSS Statement](sbom-and-oss-statement.md)

**IT / networking:** [Datasheet](datasheet.md) (Firewall + host requirements sections) → *Installation & Deployment Guide* (Mintlify)

---

## Brand template

All documents in the pack share the same brand template, derived from the marketing PowerPoint:

| Element | Value |
|---|---|
| Primary | Indigo `#2A1B5E` (header bar, headings) |
| Accent (bold) | Magenta `#E91E7D` (acronym tagline, H3) |
| Accent (soft) | Cyan `#4FC3E0` (heading underline) |
| Body | Neutral grey `#1F2937` on white |
| Header bar | Solid indigo with `M.A.R.C.U.S.` left · document title right |
| Footer | `Involve Visual Collaboration Ltd · involve.vc` left · page number right |
| Body font | Segoe UI |
| Code font | Consolas |

Regenerate all seven DOCX files by re-running the converter script:

```
python "%LOCALAPPDATA%/Temp/claude/c--Code-av-bridge/<session-id>/scratchpad/marcus_md_to_docx.py"
```

---

## Contact

- **Commercial:** commercial@involve.vc
- **Support:** support@involve.vc
- **Security:** security@involve.vc
- **Privacy / DPA:** privacy@involve.vc
- **Status page:** [status.involvecloud.com](https://status.involvecloud.com)

---

*Involve Visual Collaboration Ltd · M.A.R.C.U.S. Platform · Documentation Pack · v1.0*
*[involve.vc](https://involve.vc)*
