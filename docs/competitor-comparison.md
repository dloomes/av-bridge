# Feature comparison: av-bridge vs Utelogy vs Xyte

**Prepared:** 2026-07-17
**Purpose:** Support the shift from POC to productisation — where we sit today, where we'd need to close gaps for parity, where we could genuinely differentiate.

## Sources & confidence

- **av-bridge** — from the current codebase, `SPEC.md`, `TASKS.md`, and internal notes.
- **Utelogy** — from utelogy.com product pages, published PDFs (Application Guide 1.3.1), press releases, and partner listings. Some facts are inferred from marketing copy where docs weren't public.
- **Xyte** — from xyte.ai, dev.xyte.io, docs.xyte.io, pricing pages, and press releases (Nov 2025 AI Teammate, 2025 Advanced Workflows, Jan 2024 TechCrunch $30M raise).

Cell values: **Yes** / **Partial** / **No** / **Roadmap** / **Unknown**. "Unknown" means we couldn't find it on the public site — not that it doesn't exist. Competitor cells reflect **public marketing / docs only**; a sales conversation could turn up more.

---

## 1. Product structure & deployment

| Capability | av-bridge | Utelogy | Xyte | Notes |
|---|---|---|---|---|
| Cloud SaaS portal | Yes | Yes (U-Manage on Azure) | Yes (AWS, region unspecified) | |
| On-prem / edge component | Yes (Go bridge, systemd, Ansible-deployed) | Yes (U-Server, required per site) | Optional (Connect+ Edge software / Xyte Secure Edge hardware, ~$1,900) | |
| Pure on-prem / air-gapped mode | Partial (cloud + bridge; cloud can be self-hosted) | No (cloud required) | Private cloud on Enterprise only | |
| Multi-region hosting | Roadmap (self-host today; no managed region yet) | Unknown (Azure regions not published) | Unknown (no EU/UK residency published) | Our clearest positioning wedge. |
| Auto-update on edge | Yes (systemd timer, checksum-verified, health-check rollback) | Yes (implied via U-Enterprise) | N/A (edge is cloud-managed) | |
| Release pipeline for edge | Yes (GitHub Actions → signed releases) | Yes (in-house driver library, weekly cadence) | N/A | Just added. |

## 2. Device support

| Capability | av-bridge | Utelogy | Xyte | Notes |
|---|---|---|---|---|
| Device count in catalogue | 9 vendor/protocol adapters | 3,000+ (per marketing) | ~30 named + PJLink projectors + cloud connectors | Their catalogue is years of driver work; not a small gap. |
| Cloud-to-cloud connectors | No | Yes (Zoom, MTR, Barco, others) | Yes (Zoom, MTR, XiO, Neat, QSC Reflect, Poly, BrightSign, Domotz, Biamp, Logi Sync) | Both competitors have this; we have none. |
| Adapter extensibility | Yes (vendor-agnostic schema, adapters written in Go) | Partial (drivers .NET, no public SDK / marketplace) | Yes (self-service model definition via REST; docs.xyte.io/docs/models) | Xyte has the cleanest external-integrator story. |
| Firmware version tracking | Yes | Yes | Yes | |
| Firmware push / target versions | Yes (portal-driven target_version) | Yes (portal-driven) | Yes (Files section in model) | |

## 3. Monitoring & telemetry

| Capability | av-bridge | Utelogy | Xyte | Notes |
|---|---|---|---|---|
| Real-time device status | Yes | Yes | Yes | |
| Configurable poll rate | Yes (per-device) | Unknown (not published) | Rate-limited by plan (720–20,000/day) | |
| Metrics / lens metrics (adapter-specific) | Yes | Yes (call-quality for Teams/Zoom/Webex/SIP) | Yes | |
| Historical retention | Configurable (no plan gates) | Unknown | Tiered by plan (1wk / 1mo / 3yr / 5yr) | |
| Anomaly detection (AI/ML) | No | Marketed under 2026 "Service-as-Software" AI push | Yes (marketed) + AI Teammate (GA early 2026) | Both competitors leaning into AI narrative. |
| Room utilisation / occupancy | No | Yes | No (no calendar sync surfaced) | |
| Energy usage tracking | No | Yes | No | |

## 4. Control, automation, room-readiness

| Capability | av-bridge | Utelogy | Xyte | Notes |
|---|---|---|---|---|
| Remote command execution | Yes (command queue, bridge poll model) | Yes | Yes (custom commands per model) | |
| Workflow builder (no-code) | No | Yes (U-Automate, drag-and-drop) | Yes (Advanced Workflows, 2025) | Utelogy has the longer track record here. |
| **Nightly room-readiness test** | Roadmap (discussed 2026-07-17) | **Yes (marketed as first-class)** | No (not a named feature; could be built with Workflows) | Utelogy's clearest control-side differentiator. Our next feature ask lands us at their parity. |
| Self-healing / auto-remediation | No | Yes (occupancy-aware) | Partial (via Workflows) | |
| Full control-system replacement | No | Yes (U-Server + U-Control HTML5) | No | Utelogy plays in the Crestron/AMX replacement space; we do not. |

## 5. Multi-tenancy & MSP support

| Capability | av-bridge | Utelogy | Xyte | Notes |
|---|---|---|---|---|
| True multi-tenant isolation | Yes (Postgres FORCE RLS, per-tenant policies) | Unknown (MSP SKU exists; isolation model undocumented) | Yes ("full data separation" claimed; Connect+) | Our isolation is arguably tighter than either competitor discloses. |
| Federated manufacturer → reseller → customer | No | Partial (MSP + Integrator packages) | Yes (core architectural claim) | Xyte's hierarchy is deeper and codified. |
| Per-tenant branding | Yes (logo, accent, display name) | Unknown | Yes (branded storefronts on Premium OEM tier) | |
| Vendor / helpdesk cross-tenant access | Yes (dedicated vendor_tenants + X-Customer-Scope) | Unknown | Yes (via Xyte-side support tools) | |

## 6. Auth & RBAC

| Capability | av-bridge | Utelogy | Xyte | Notes |
|---|---|---|---|---|
| Local username / password | Yes (bcrypt + hashed session tokens) | Yes (implied) | Yes | |
| SSO — Entra ID / Azure AD | Roadmap (mock JWT wired, real IdP integration pending) | Partial (Okta explicitly named; Entra not confirmed) | Yes (via Descope broker; also Okta, Google Workspace) | |
| SSO — SAML / OIDC generic | Roadmap | Unknown (likely via SAML) | Yes (Descope brokers both) | |
| MFA | Roadmap | Yes | Yes (2FA on Standard+) | |
| SCIM auto-provisioning | No | Unknown | No (users pre-provisioned) | Both competitors also lack it. |
| Custom roles / permission catalogue | Yes (21+ permissions, portal-editable custom roles) | Yes (granular RBAC advertised, catalogue not public) | Partial ("advanced roles" gated to Premium; catalogue not public) | Our permission model is more transparent than either. |
| Physical scope RBAC (per-building) | **Yes** | Unknown | Unknown | Neither publishes an equivalent — potential differentiator. |
| Audit log | Yes (actor role + scope + vendor flag on every row) | Yes | Yes (retention gated: 1wk / 1yr) | |

## 7. Integrations

| Capability | av-bridge | Utelogy | Xyte | Notes |
|---|---|---|---|---|
| Public REST API | Roadmap (post-Entra) | Yes (mentioned in FAQ; no public OpenAPI docs) | Yes (dev.xyte.io/reference; keys add-on) | Both have API; ours is roadmap. |
| Webhooks (inbound alerts) | Yes | Yes (standardised JSON) | Yes (all tiers) | |
| ITSM — ServiceNow | No | Partial (generic webhook, no named connector) | **Yes (two-way incident sync)** | Xyte deepest here. |
| ITSM — Jira | No | Partial (webhook) | Yes (webhook-driven) | |
| ITSM — Freshservice / Zendesk / Zoho | No | No | Yes | |
| Email notifications | Yes | Yes | Yes | |
| Teams / Slack notifications | Yes (Teams + generic webhook) | Yes | Yes (Slack) | |
| Calendaring (Exchange / Google Graph) | No | No (not named on public site) | No (not named) | Absent on all three. |
| Power BI connector | No | Yes | No | |

## 8. Reporting & analytics

| Capability | av-bridge | Utelogy | Xyte | Notes |
|---|---|---|---|---|
| Fleet dashboards | Yes (portal + Grafana bundle for ops) | Yes | Yes | |
| Device uptime / room activity reports | Yes (24h/7d/30d/90d) | Yes | Yes | |
| CSV export | Yes | Unknown (dashboards "exportable") | Unknown | |
| Firmware fleet view | Yes | Yes | Yes | |
| Alert SLA reporting | No | Yes | Yes (via incidents) | |
| Room utilisation reporting | No | Yes | No | |

## 9. Security, compliance, data residency

| Capability | av-bridge | Utelogy | Xyte | Notes |
|---|---|---|---|---|
| Encryption at rest | Yes (AES-GCM, env-key today) | Yes (AES-256) | Yes (unspecified) | We're roadmap on KMS-backed keys. |
| Encryption in transit | Yes (TLS + HMAC-signed webhooks) | Yes (TLS) | Yes (TLS) | |
| SOC 2 Type 2 | No | Yes | Yes | |
| ISO 27001 | No | Yes | Unknown | |
| GDPR | No formal statement | Yes | Yes (self-declared) | |
| HIPAA / HITRUST | No | Yes | Unknown | |
| **UK data residency** | **Achievable (single-region self-host)** | Not published | Not published | Both competitors decline to publish this. Genuine wedge if we commit. |
| Penetration testing | No formal programme | Unknown | Yes (regular, per marketing) | |

## 10. Pricing model

| Aspect | av-bridge | Utelogy | Xyte |
|---|---|---|---|
| Structure | Not yet modelled | Per-room, tiered by device count | OEM: platform fee ($4k–$16k/mo); Integrator: platform fee + per-device |
| Entry price | TBD | Pro Room ~$35/room/mo (2024 list, inferred from PDF filename) | OEM Basic $4,199/mo; Integrator Professional $5,000/yr + $1.99–$3.99/device/mo |
| Public pricing page | N/A | Partial (PDFs; sales-led) | Yes (published per-tier tables) |
| Free / self-serve tier | N/A | No | No |

## 11. Target market & positioning

| | av-bridge | Utelogy | Xyte |
|---|---|---|---|
| Enterprise IT/AV | Fit | Explicit vertical | Explicit vertical |
| Higher education | Fit | Explicit vertical | Not primary |
| Government / federal | Possible (if compliance closed) | Explicit vertical (no FedRAMP claim) | Not primary |
| AV integrators / MSP | Fit | Explicit SKU | Explicit product (Connect+) |
| Device OEMs / manufacturers | Not a fit | Not a fit | **Core product** (OEM Hubs; entire different business model) |
| Named large customers (public) | POC only | Numerous higher-ed + enterprise references | Schneider Electric, Legrand (RackLink), GPA |

---

## Strategic summary

### Where we could genuinely differentiate today

1. **UK / EU data residency, published.** Neither competitor makes this claim on their public site. For UK public sector, financial services, or GDPR-nervous enterprises, this is a real wedge — worth committing to and publishing before competitors do.
2. **Physical-scope RBAC (per-building).** We've built granular scoping deep into the RLS layer. Neither competitor advertises equivalent. Real differentiator for large multi-site estates where different site teams need isolated views.
3. **Transparent, portal-editable custom RBAC.** We publish our 21+ permission catalogue and let customers assemble roles. Both competitors gate this behind sales or lock it to top tiers.
4. **Open adapter architecture.** Xyte's manufacturer-integration story is closest to ours in spirit — but ours is truly vendor-agnostic without a monetisation gate. Position as "no vendor lock-in" against Utelogy's driver library and Xyte's platform tax.
5. **Postgres-with-FORCE-RLS multi-tenancy.** Isolation guarantees that a competitor incident (data leak between customers) can't reproduce on our stack. Worth naming this in security docs.

### Where we must close gaps to be table-stakes

These are the "must-have or you don't get shortlisted" items:

| Gap | Level of effort | Priority |
|---|---|---|
| Real Entra ID / SAML / OIDC SSO | High (already scoped) | Critical |
| Public REST API + OpenAPI spec | Medium (post-Entra per roadmap) | Critical |
| SOC 2 Type 2 (or credible path to it) | High (12+ months) | Critical for enterprise |
| Nightly room-readiness testing | Medium (was just discussed) | High — this is Utelogy's headline pitch |
| Cloud-to-cloud connectors (Zoom Rooms / MTR / XiO at minimum) | Medium per connector | High |
| MFA | Low (once real SSO lands) | High |
| ITSM connector (ServiceNow first, then Jira) | Medium | High — Xyte's strongest integration story |
| No-code workflow builder | High | Medium — differentiator if we build it well |

### Where we can't compete without a business-model shift

- **Xyte OEM monetisation platform.** That's a fundamentally different product (subscription/licensing platform for hardware makers). Don't chase it.
- **Utelogy's 3,000-driver catalogue.** Years of dedicated driver-engineering effort. The right answer is a self-service adapter SDK + community model, not to build 3,000 drivers ourselves.

### Suggested positioning statement (draft)

> An AV monitoring platform for UK and European enterprises that want real multi-tenant isolation, transparent role-based access, and a code-first adapter model — without the driver-library lock-in of Utelogy or the OEM-monetisation overhead of Xyte. Cloud-hosted in your region, or self-hosted for full data control.

### What this tells us about roadmap sequencing

Given the gaps above, the natural product order is:

1. **Real Entra ID / SSO** (already roadmap; unblocks enterprise sales and MFA and public API)
2. **Nightly room-readiness testing** (was just discussed; parity feature that any AV buyer expects)
3. **Cloud-to-cloud connectors — start with Zoom Rooms and MTR** (parity; both competitors have them, buyers will ask)
4. **Public API v1** (post-Entra; unblocks ITSM/BI integrations without us building each one)
5. **ServiceNow connector** (highest-leverage ITSM; consumes the public API)
6. **SOC 2 Type 2 readiness project** (12+ month path; kick off in parallel with the above so audit lands when we're selling)

---

*This is a snapshot as of the prep date; competitor sites change. Recommend a refresh every 6 months, or before any pricing/positioning conversation.*
