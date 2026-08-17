# Product marketing context — MEDIO Assist

**As of:** 2026-07-31
**Owner:** Daniel Loomes (dloomes@involve.vc)
**Brand parent:** MEDIO (Involve's healthcare-focused brand family)
**Purpose:** Source of truth for how we talk about MEDIO Assist externally. Every marketing artefact — homepage, product pages, sales deck, blog, ad copy, cold email — reads this file. Update it when positioning changes; don't let it drift.

> **v0.2 reset note:** an earlier version of this document positioned the product as "Involve Insights" for corporate multi-vendor AV estates. That was wrong direction. The platform lives inside the **MEDIO** brand family and is called **MEDIO Assist**. NHS/healthcare is the primary market, not corporate offices. Everything below reflects the corrected positioning.

---

## 1. Product one-liner

**MEDIO Assist keeps healthcare AV working — 24/7 monitoring, preventative maintenance, and every room ready before the working day begins.**

## 2. Elevator pitch (30 seconds)

Every consultation room, telehealth suite, and multi-disciplinary meeting space in the modern NHS runs on AV — codecs, displays, DSPs, cameras — and when a room fails at 09:00, a whole clinic list slips. MEDIO Assist watches over every device across every vendor from a single platform, hosted in the UK. Every night we test the rooms end-to-end, and every morning your team knows exactly what's ready before the first appointment. When something needs a hand, our 24/7 UK-based NOC is already on it.

## 3. What it does, in plain English

- A small **on-premise bridge** collects live telemetry from AV devices (Poly VideoOS, Sony BRAVIA, Biamp Tesira, Aurora RXT today, more each month) using each vendor's native protocol.
- The bridge streams telemetry to the **UK-hosted MEDIO Assist cloud** where clinical IT, digital programme leads, and MEDIO's own NOC see rooms in real time.
- **Preventative alerts** fire when a device goes offline or degrades. Routed to the customer's own channels *and* into MEDIO's NOC queue so problems are being worked on before someone rings the service desk.
- **Room Readiness** — the flagship capability — powers rooms off overnight, wakes them, runs the readiness routine the customer defines (dial a test bridge, listen for audio, hang up cleanly), and emails a **morning digest** listing every room's status before AM clinics begin.
- Failed rooms cc'd to **MEDIO's helpdesk** on managed-service tiers so the fix is already in motion by the time the ward or clinic starts.
- Compliant with the credentials NHS procurement already checks for: **ISO 27001, ISO 9001, ISO/IEC 20000-1, Cyber Essentials Plus, GDPR, NHS Digital Toolkit**.

## 4. Personas

### Primary buyer: Digital programme manager / Head of Digital & IT
- Sits inside an NHS trust, hospital group, or larger care organisation
- Responsible for the digital services clinicians rely on — including AV kit in consultation rooms, MDT rooms, telehealth suites, boardrooms, education spaces
- Currently firefights: SLA calls, "the codec is stuck in a call" tickets, clinicians walking out of a room that isn't ready, vendor-by-vendor troubleshooting
- Cares about: **uptime linked to clinic capacity**, mean time to detect, mean time to repair, compliance evidence for audits
- Buys against operational impact, not features
- **The homepage headline speaks to them first**

### Blocker: NHS procurement / CFO / Digital Board
- Signs off spend above threshold and reviews security/compliance posture
- Cares about: **data residency (UK-only, no US clouds)**, procurement-standard accreditations, defensible ROI (reduced downtime, fewer emergency call-outs, extended device life through preventative maintenance)
- Won't read technical detail; will scan the trust-marker strip and the credentials list
- Below-the-fold sections and the credentials strip speak to them

### End beneficiary: Clinicians & clinical support staff
- Don't buy the product; live the impact
- Care about: **walking into a room that just works** so their AM list starts on time
- Not the target of homepage copy directly, but their outcomes ARE the copy — every claim ties back to their working day
- Photography always features them (per MEDIO brand guide §Photography)

### Internal user: MEDIO NOC engineer
- Uses the Assist platform every shift to manage incidents across every customer
- Cares about: cross-tenant view, prioritised alerts, quick drill-down to affected rooms and devices
- Doesn't feature in customer marketing but IS the differentiator behind "eyes on the system, all the time"

## 5. Positioning statement

> For **NHS trusts, hospital groups and healthcare organisations that run clinical AV at scale**,
> who need **AV that's ready before every clinic and monitored around the clock**,
> **MEDIO Assist** is **the 24/7 healthcare AV support platform**
> that **watches every room across every vendor from a UK-hosted platform, tests them nightly, and puts our NOC on the incident before you know it happened.**
> Unlike **generic IT-support contracts that treat AV as an afterthought**, or **vendor-native tools that only see their own kit**,
> MEDIO Assist is **healthcare-native, vendor-agnostic, and comes with real preventative maintenance — not just reactive tickets**.

## 6. Competitive landscape

Positioning is different from what the corporate-AV space has (Utelogy, Xyte, Crestron Fusion). In healthcare we're competing with what customers already have — usually one of these:

| Alternative | What they have today | Where we win |
|---|---|---|
| **Generic IT MSP contracts** | Break-fix on AV bundled into a wider IT support agreement. AV is a footnote; response is reactive. | We're healthcare-AV-native. Preventative, not reactive. NOC engineers who understand codecs and DSPs, not generalists. |
| **Vendor-native support** (Poly Lens, Cisco Control Hub, Q-SYS Reflect) | Each vendor's own management tool. Great for their kit, blind to everything else. | One platform across the mixed reality of real hospital estates. No vendor lock-in. |
| **In-house monitoring scripts** | Someone in the trust's IT team wrote a Python script that pings 30 codecs and emails a report. Fragile, undocumented, one person from redundancy. | Real platform, audit-logged, multi-tenant, our engineers on the hook — not the trust's. |
| **Doing nothing / reactive-only** | Trust runs AV, waits for tickets, dispatches an engineer. Common at smaller sites. | Preventative catches ~80% of failures before they hit the working day. Direct ROI in avoided clinic slippage. |

We don't try to compete on "we have more integrations than [vendor] does on their own kit." That's their turf. We win on **healthcare focus, mixed-estate reality, and preventative service model.**

## 7. Voice & tone

**We are:** clinically empathetic, precise about technical capability, honest about what we haven't built yet, warm without being saccharine.

**Aligned with the MEDIO master voice:** "connect, care and collaborate." We keep clinicians and patient outcomes at the centre of every claim.

**Do:**
- Anchor claims to the clinician's working day ("your morning MDT starts on time")
- Use concrete numbers where we have them (specific SLAs, response times, compliance accreditations named)
- Name specific settings ("consultation rooms", "telehealth suites", "MDT rooms", "ward rounds") not generic ("meeting rooms")
- Name specific vendors when talking about adapters (Poly, Sony, Biamp) rather than generic categories
- Emphasise **preventative** as a positioning word — it's central to Assist's story
- Say "we" and "your team" — service-forward
- Show the credentials NHS procurement asks for, unforced

**Don't:**
- Overclaim adapter coverage — real vs roadmap must be clearly separated
- Use corporate hedge words: *solutions*, *empower*, *streamline*, *leverage*, *robust*, *cutting-edge*
- Speak down to clinicians or over-clinicise for the technical buyer
- Position as "AI-powered" — we're not, and it's not the story
- Use "customer" where "clinician" or "your team" would land more warmly

**Adjectives that fit MEDIO Assist:** trustworthy, calm, preventative, considered, quietly capable
**Adjectives to avoid:** revolutionary, disruptive, next-gen, smart, cutting-edge, game-changing

**Voice cues from the MEDIO brand guide:**
- "Designed with clinicians, for clinicians"
- "Helping clinicians spend more time caring, less time waiting"
- "Connect, care and collaborate" (master tagline)
- "Preventative, not reactive" (Assist's own philosophy)
- "Eyes on the system, all the time"

## 8. Visual identity (from MEDIO brand guide v1.2, Jan 2026)

- **Primary colours:** Deep Green `#003330` · Digital Cyan `#6affff`
- **Secondary palette:** Green `#007753`, Lime Green `#cfeb57`, Coral `#ff8970`, Burgundy `#5b2445`, Pink `#ffcee6`, Off-white `#f3f1f3`. Use sparingly, one at a time, always alongside the primary.
- **Typeface:** Wix Madefor Display — Bold, SemiBold, Regular. Single-family across all comms.
- **Type scale example:** Heading 97pt / SemiBold subhead 40pt / Body 20pt @ 27pt leading.
- **Signature graphic device:** "The MEDIO Stack" — three rounded parallelogram shapes forming the M silhouette. Frame content; don't overlap across multiple stacks unless necessary.
- **Photography rules:** real clinicians in real settings; technology in use *by people*, never standalone equipment; authenticity + diversity emphasis.
- **Logo lockup for this product:** MEDIO Assist (see brand guide §Logo · Product lockups, p.10). Never used below 30px height.

## 9. Feature-to-benefit map

| Feature | Technical claim | Business benefit | Persona |
|---|---|---|---|
| Vendor-agnostic adapters | First-class Poly VideoOS, Sony BRAVIA, Biamp Tesira, Aurora RXT — more each month | One platform across the mixed estate real hospitals actually have | Digital lead |
| Room Readiness | Nightly power cycle + functional test routine + morning digest per site | Clinics start on time; failures caught before AM list | Clinical + digital lead |
| 24/7 UK NOC | UK-based operations team monitoring events around the clock | Failures worked before the service desk is called; managed-service SLA | Digital lead + procurement |
| Preventative maintenance visits | Data-driven site visits triggered by trending metrics, not calendars | Fewer emergency call-outs; extended device life; supports 5-yr refresh cycles | Procurement / CFO |
| UK-hosted platform + NHS-standard accreditation | ISO 27001, ISO 9001, ISO/IEC 20000-1, Cyber Essentials Plus, GDPR, NHS Digital Toolkit | Passes procurement without a security-review fight | Procurement |
| Customer-authorable routines | Drag-and-drop builder gated by adapter capability | Each trust defines what "ready" means for a paediatric outpatient vs an MDT vs a telehealth suite | Digital lead |
| Multi-tenant portal | Trust-level tenant isolation with per-user identity via Entra ID | Regional shared services can safely run one platform across trusts | Digital lead |
| Immutable audit log | Every state-changing action recorded with actor, role, target, before/after | Forensics + compliance evidence on demand | Procurement / IG |

## 10. Objection handling

**"We already get AV support bundled with our IT MSP."**
> Fine — most trusts do. The bundled AV clause usually means "someone will drive out when a codec breaks", not proactive monitoring. Assist sits alongside your existing MSP for the AV rooms that matter most (MDTs, telehealth, boardrooms) and catches issues before they hit your list.

**"Data residency — we can't put patient-adjacent data in a US cloud."**
> The MEDIO Assist platform is UK-hosted end to end. No US clouds, no US processors. Cyber Essentials Plus, ISO 27001, NHS Digital Toolkit — the standard procurement checks are already covered. Signed DPAs and data-flow diagrams available before contract.

**"How is this different from our existing IT monitoring tool?"**
> Standard IT monitoring pings a device and tells you it's online. That doesn't tell you a consultation room is *ready* — the display's woken, the codec's dialled a test bridge, the mics heard back. Room Readiness proves the room works before your clinic list starts. That's the difference between "device up" and "room ready".

**"Your adapter list looks thin."**
> True — four native adapters today (Poly, Sony, Biamp, Aurora). One added per month. If you tell us your top three vendors, we'll prioritise them. Adapters are a documented Go interface, so if you have an in-house team, they can add their own.

**"What if MEDIO Assist goes down at 07:45 — do we miss the morning?"**
> Bridges continue collecting telemetry during a cloud outage. Digest generation is the affected surface. On managed-service tiers, our NOC is monitoring platform health as well as customer estates — 24/7 UK coverage.

**"Pricing?"**
> [PLACEHOLDER — decide before homepage ships. Per-room-per-month is the current working assumption, brackets by estate size, managed-service uplift on top.]

**"We're a small trust with 20 rooms. Is this overkill?"**
> No — the platform is per-room-priced, so it scales down. The value case still holds: a single missed AM list costs more in slipped clinic revenue than a year of Assist.

## 11. Terminology — internal vs external

| Internal (repo / code) | External (customer-facing) | Notes |
|---|---|---|
| av-bridge | **MEDIO Assist** | Product name. Never use "av-bridge" externally. |
| Involve Insights | (do not use) | Old placeholder; ignore. |
| bridge / collector | **on-site bridge** | "collector" is jargon |
| routine | **routine** | The reusable test sequence. Renamed from "recipe" in July 2026. |
| step / block | **step** in a routine; **block** in the palette | "step" = what's in a routine; "block" = what you drag from the palette |
| Room Readiness | **Room Readiness** | Product-level feature name; always capitalise |
| adapter | **adapter** (technical) / **supported device family** (executive) | Use context-appropriate term |
| capabilities | **supported actions** | "capabilities" is a technical term |
| morning digest | **morning digest** | Same |
| helpdesk queue routing | **NOC alerts** or **cc'd to our NOC** | "NOC" (Network Operations Centre) is the term the current Assist page uses |
| tenant | **trust** / **organisation** / **customer** | NHS context prefers "trust"; wider healthcare, "organisation" |
| RLS / row-level security | **tenant isolation** | Plainer language |
| room | **consultation room** / **telehealth suite** / **MDT room** — whichever fits | Be specific about the clinical setting when we can |

## 12. Proof points

_Empty. Fill as case studies emerge — real proof unblocks the long-form product page._

- **Named NHS trusts / customers:** [—]
- **Reference deployment sizes:** [—]
- **Documented case studies:** [—]
- **Clinician quotes:** [—]
- **Metrics from real deployments** (rooms readied, incidents caught, clinic slippage avoided): [—]
- **NHS Digital Toolkit accreditation:** ✓ (per current Assist page)
- **ISO 27001:** ✓
- **ISO 9001:** ✓
- **ISO/IEC 20000-1:** ✓
- **Cyber Essentials Plus:** ✓
- **GDPR compliant:** ✓

## 13. Open decisions blocking marketing work

- [ ] **Pricing model** — per-room / per-tenant / managed-tier bundle. Blocks any page with pricing on it.
- [ ] **First named customer** — POC customer is a placeholder; a real NHS trust unblocks case-study work.
- [ ] **Adapter roadmap accuracy** — need honest today (Poly / Sony / Biamp / Aurora) vs on-the-roadmap list. Copy currently reflects today-state.
- [ ] **Relationship to MEDIO AV product** — Assist is one thing, MEDIO AV (design + install) is another. Does the homepage explain the boundary or gloss it?
- [ ] **Existing Assist page merge or replace?** — the live page frames Assist as "24/7 UK NOC support". The platform work we've been building adds Room Readiness, adapter platform, routines, portal. Do we rewrite one page or add sub-pages? (Current decision: prototype only for now; don't touch live yet.)

## 14. Notes for future marketing writers

- **Every claim must be honest today**, not aspirational. Aspirational features go behind a "coming soon" or "roadmap" label.
- **When updating this file, bump the "As of" date** at the top so downstream readers know its freshness.
- **This file is not the copy** — it's the context copy grips onto. Don't paste it directly to the site. Write to it, then compress into whatever the surface calls for.
- **Voice check:** if the copy would sit awkwardly next to the MEDIO master voice on medio.co.uk, rewrite it. Everything MEDIO Assist ships should feel like a sibling of the wider brand, not a corporate refugee.
