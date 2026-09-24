---
title: M.A.R.C.U.S. — Software Bill of Materials & Open-Source Statement
product: M.A.R.C.U.S.
vendor: Involve Visual Collaboration Ltd
website: https://involve.vc
version: 1.0
status: General Availability
audience: Customer InfoSec, procurement, public-sector assurance
---

<!-- ============================================================
  M.A.R.C.U.S.
  Managed · Assets · Resources · Control · Updates · Status
  Involve Visual Collaboration Ltd · involve.vc
============================================================ -->

# M.A.R.C.U.S. — Software Bill of Materials & Open-Source Statement

**Managed · Assets · Resources · Control · Updates · Status**

This document lists the third-party components used to build and operate the M.A.R.C.U.S. platform, along with Involve Visual Collaboration Ltd's position on open-source software use, licensing, and supply-chain security. Public-sector and enterprise assurance teams typically need this material as part of procurement.

A machine-readable **CycloneDX 1.5 SBOM** is generated automatically on every release and available on request to security@involve.vc.

## 1. Position on open-source software

Involve uses open-source components extensively in M.A.R.C.U.S. — the platform is more secure and more maintainable because of it. Every dependency:

- is chosen for **fitness for purpose** and **maintenance health** (active maintainer, recent release cadence, responsive to CVEs);
- is **pinned by version and checksum** in source control (`go.sum`, `package-lock.json`);
- is **licence-audited** against a permitted-licence list before adoption;
- is **vulnerability-scanned** on every pull request and continuously monitored by GitHub Dependabot;
- is **patched on a defined cadence** — Critical / High CVEs within 5 business days, Medium within 30 days, routine refresh monthly.

Involve does not distribute the M.A.R.C.U.S. cloud service as a downloadable product; the compiled M.A.R.C.U.S. Collector binary is the only artefact placed on customer premises. The Collector's third-party dependencies are listed in Section 3 below.

## 2. Permitted licences

The following licences are permitted for use in M.A.R.C.U.S. components:

| Licence family | Examples |
|---|---|
| **BSD** | 2-clause, 3-clause |
| **MIT** | MIT, ISC |
| **Apache** | Apache 2.0 |
| **Mozilla** | MPL 2.0 |

**Copyleft licences (GPL, AGPL, LGPL)** are not used in the shipped Collector binary or the cloud runtime. If a copyleft dependency is required for internal tooling only (build, test, CI), it is isolated from the shipped artefact.

## 3. Third-party components (Collector — shipped to customers)

The M.A.R.C.U.S. Collector is a compiled Go binary that includes the following direct third-party modules:

| Module | Version | Licence | Purpose |
|---|---|---|---|
| `github.com/gorilla/mux` | v1.8.1 | BSD-3-Clause | HTTP routing |
| `github.com/gorilla/websocket` | v1.5.1 | BSD-2-Clause | WebSocket transport (used for real-time push to portal) |
| `github.com/prometheus-community/pro-bing` | v0.9.1 | MIT | ICMP ping adapter |
| `go.bug.st/serial` | v1.6.2 | BSD-3-Clause | Cross-platform RS-232 serial support |
| `gopkg.in/yaml.v3` | v3.0.1 | MIT / Apache-2.0 | Local Collector configuration parsing |
| `github.com/google/uuid` | v1.6.0 | BSD-3-Clause | UUID generation |
| `github.com/kardianos/service` | v1.3.0 | ZLib | Cross-platform OS service wrapper (systemd, Windows Service) |
| `golang.org/x/net` | v0.56.0 | BSD-3-Clause | Extended networking primitives |
| `golang.org/x/sync` | v0.21.0 | BSD-3-Clause | Synchronisation primitives |
| `golang.org/x/sys` | v0.46.0 | BSD-3-Clause | Platform system calls |
| Go standard library | 1.25 | BSD-3-Clause | Base runtime |

The complete indirect (transitive) dependency graph is captured in `go.sum` and reproduced in the machine-readable CycloneDX SBOM.

## 4. Third-party components (Cloud — service-side, not customer-hosted)

M.A.R.C.U.S. Cloud runs on Involve infrastructure. Customers do not host any of these components, but the inventory is provided for supply-chain transparency.

### 4.1 Cloud Go modules

| Module | Version | Licence | Purpose |
|---|---|---|---|
| `github.com/jackc/pgx/v5` | v5.6.0 | MIT | PostgreSQL driver and connection pool |
| `github.com/jackc/pgpassfile` | v1.0.0 | MIT | pgpass file support |
| `github.com/jackc/pgservicefile` | v0.0.0-20221227 | MIT | pg_service.conf support |
| `github.com/jackc/puddle/v2` | v2.2.1 | MIT | Connection pool |
| `github.com/gorilla/websocket` | v1.5.1 | BSD-2-Clause | Portal live-events WebSocket |
| `golang.org/x/crypto` | v0.17.0 | BSD-3-Clause | Cryptographic primitives (bcrypt, HMAC) |
| `golang.org/x/net` | v0.17.0 | BSD-3-Clause | Extended networking primitives |
| `golang.org/x/sync` | v0.1.0 | BSD-3-Clause | Synchronisation primitives |
| `golang.org/x/text` | v0.14.0 | BSD-3-Clause | Text handling |
| Go standard library | 1.22 | BSD-3-Clause | Base runtime |

### 4.2 Portal (Next.js) direct dependencies

| Package | Version | Licence | Purpose |
|---|---|---|---|
| `next` | 14.2.15 | MIT | React application framework |
| `react`, `react-dom` | 18.3.1 | MIT | UI library |
| `@radix-ui/react-*` | 1.1–1.2 | MIT | Accessible unstyled UI primitives (scroll area, separator, slot) |
| `@dnd-kit/core`, `@dnd-kit/sortable`, `@dnd-kit/utilities` | 6.3 / 10.0 / 3.2 | MIT | Drag-and-drop for the routine builder |
| `mapbox-gl` | 3.29 | MPL-2.0 with Mapbox terms | Interactive map view for the estate |
| `react-map-gl` | 7.1 | MIT | React binding for Mapbox GL |
| `lucide-react` | 0.453 | ISC | Icon set |
| `class-variance-authority`, `clsx`, `tailwind-merge` | Latest | MIT | Tailwind utility helpers |
| `tailwindcss-animate` | 1.0 | MIT | Tailwind animation preset |

### 4.3 Portal build & dev dependencies

| Package | Version | Licence | Purpose |
|---|---|---|---|
| `typescript` | 5.6 | Apache-2.0 | Type-checking |
| `tailwindcss` | 3.4 | MIT | Utility-first CSS |
| `postcss`, `autoprefixer` | Latest | MIT | CSS processing |
| `eslint`, `eslint-config-next` | 8.57 / 14.2 | MIT | Linting |
| `@types/*` | Latest | MIT | Type declarations |

## 5. Infrastructure and operational software

Involve operates M.A.R.C.U.S. Cloud on the following managed services. These are contracted directly with the provider; source code is not exposed to Involve or its customers.

| Service | Provider | Purpose |
|---|---|---|
| Amazon ECS Fargate | AWS | Container runtime for cloud services |
| Amazon RDS for PostgreSQL | AWS | Primary application database |
| Amazon S3 | AWS | Object storage (attachments, log archives) |
| Amazon ECR | AWS | Container image registry |
| AWS Application Load Balancer | AWS | Public entry point, TLS termination |
| AWS Secrets Manager | AWS | Secrets storage and rotation |
| AWS CloudWatch | AWS | Metrics, logs, alarms |
| AWS Route 53 | AWS | Authoritative DNS |
| AWS Amplify | AWS | Portal static hosting + CDN |
| Amazon SES | AWS | Transactional email |
| GitHub | GitHub, Inc. | Source control, CI/CD (Actions) |
| Terraform (open source) | HashiCorp | Infrastructure-as-code |
| Statuspage | Atlassian | Public status page |
| PagerDuty | PagerDuty | On-call rota and incident escalation |

See *M.A.R.C.U.S. — Security & Trust* Section 12 for the sub-processor list with data-flow context.

## 6. Vulnerability management

- **Dependency scanning** — GitHub Dependabot enabled on all repositories. Alerts triaged weekly.
- **Container image scanning** — Amazon ECR Enhanced Scanning on every image push. Critical CVEs block deployment.
- **SAST** — `go vet`, `staticcheck`, `gosec` on every pull request (Go); `eslint` + strict `typescript` on every pull request (portal).
- **Secret scanning** — GitHub push protection blocks commits containing detected secrets.
- **Penetration testing** — annual third-party test against production; findings tracked to remediation.

**Patch cadence:**

| Severity | Target |
|---|---|
| Critical / High CVE with public exploit | Same business day |
| Critical / High CVE | 5 business days |
| Medium CVE | 30 days |
| Low / Informational | Rolled into monthly dependency refresh |

## 7. Machine-readable SBOM

A **CycloneDX 1.5** SBOM in JSON format is generated automatically on every release build. It includes:

- All direct and transitive dependencies for the Collector binary (per architecture).
- All direct and transitive dependencies for the Cloud services.
- All direct and transitive dependencies for the Portal (from `package-lock.json`).
- Component licences and hashes.
- Build provenance (commit SHA, build timestamp, builder identity).

The SBOM is available on request to **security@involve.vc**, or by API for Enterprise customers with an active support contract.

## 8. Distribution model

- The **Collector binary** is signed and served from `*.involvecloud.com/public/downloads/`. The signing key is held in AWS KMS; verification instructions are published in the customer documentation portal.
- **Cloud services** are not distributed — customers consume them as SaaS.
- **Portal source** is not distributed; portal code is served by AWS Amplify from Involve's build output.

## 9. Attribution

Full attribution and licence text for every third-party component is published at [involve.vc/trust/oss](https://involve.vc) and is included in the Collector binary distribution under `/usr/share/av-bridge/THIRD_PARTY_NOTICES` (Linux) or the equivalent path on Windows.

## 10. Contact

- **Supply-chain / SBOM enquiries:** security@involve.vc
- **Commercial enquiries:** commercial@involve.vc
- **Machine-readable SBOM request:** security@involve.vc (specify release version)

---

*Involve Visual Collaboration Ltd · M.A.R.C.U.S. Platform · Software Bill of Materials & OSS Statement · v1.0*
*[involve.vc](https://involve.vc)*
