---
title: M.A.R.C.U.S. — Business Continuity & Disaster Recovery
product: M.A.R.C.U.S.
vendor: Involve Visual Collaboration Ltd
website: https://involve.vc
version: 1.0
status: General Availability
audience: Customer InfoSec, procurement, business continuity assessors
---

<!-- ============================================================
  M.A.R.C.U.S.
  Managed · Assets · Resources · Control · Updates · Status
  Involve Visual Collaboration Ltd · involve.vc
============================================================ -->

# M.A.R.C.U.S. — Business Continuity & Disaster Recovery

**Managed · Assets · Resources · Control · Updates · Status**

This document defines Involve Visual Collaboration Ltd's approach to business continuity and disaster recovery for the M.A.R.C.U.S. platform: what is protected, how it is protected, recovery objectives, testing cadence, and customer responsibilities.

## 1. Scope

This BCP / DR plan covers:

- **M.A.R.C.U.S. Cloud** — the multi-tenant SaaS platform operated by Involve in AWS.
- **Customer data** stored on M.A.R.C.U.S. Cloud (device inventory, telemetry, alerts, audit log, users, configuration).
- **Involve internal systems** required to operate the service — source code, CI/CD, secrets, and monitoring.

Out of scope:

- **M.A.R.C.U.S. Collector** on customer premises — customer-provided host, customer-managed continuity.
- **Customer AV devices** — customer-owned, customer-supported.
- **Customer network** and **Microsoft Entra ID tenant** — outside Involve's operational boundary.

## 2. Recovery objectives

| Metric | Target | Definition |
|---|---|---|
| **RPO — Recovery Point Objective** | **15 minutes** | Maximum acceptable data loss in the event of a disaster. Achieved via point-in-time recovery on RDS. |
| **RTO — Recovery Time Objective** | **4 hours** | Maximum acceptable time between disaster declaration and service restoration. |
| **MTPD — Maximum Tolerable Period of Disruption** | **24 hours** | Beyond this, disruption is treated as a material commercial event and communicated to all customers with contractual context. |

Objectives apply to the platform as a whole. Individual customer tenants recover simultaneously — there is no per-tenant recovery ordering.

## 3. Threat model

The plan addresses the following categories of disruption:

| Category | Example |
|---|---|
| **Infrastructure failure** | AWS AZ-level failure; RDS instance failure; ECS Fargate task health failure. |
| **Data loss / corruption** | Accidental deletion via bug or misuse; database corruption; malicious tampering. |
| **Security event** | Credential compromise; supply-chain attack; DDoS. |
| **Provider outage** | AWS region-level service issue; Amplify outage; SES delivery failure. |
| **Operational error** | Bad deployment; incorrect Terraform apply; secret misconfiguration. |
| **Force majeure** | Prolonged AWS regional outage; major internet routing incident. |

## 4. Protective controls

### 4.1 Compute redundancy

- **AWS ECS Fargate** runs the cloud services across multiple availability zones within the AWS London region (`eu-west-2`). AZ failure is transparent — tasks are re-scheduled onto healthy AZs automatically.
- **AWS Application Load Balancer** health-checks every ECS task and routes only to healthy instances.
- Rolling deployments keep at least one healthy task in service at all times.

### 4.2 Database durability

- **Amazon RDS for PostgreSQL** with **Multi-AZ standby** — synchronous replication to a second availability zone. Automatic failover on primary failure, typically under 60 seconds.
- **Automated backups** with a 35-day point-in-time recovery window. Restoration to any second within that window is supported.
- **Manual snapshots** taken before every major release, retained for 12 months.
- All backups and snapshots are AES-256 encrypted using AWS-managed KMS keys.

### 4.3 Object storage durability

- **Amazon S3 with versioning enabled** for asset attachments and log archives. Accidental overwrites and deletions are recoverable.
- **AWS-managed lifecycle policies** transition older objects to lower-cost storage tiers; nothing is permanently deleted within the retention window.

### 4.4 Configuration and secrets

- **Infrastructure as code** — every environment is defined in Terraform stored in a private GitHub repository with mandatory pull-request review.
- **AWS Secrets Manager** stores database credentials, HMAC keys, and third-party API keys. Secrets are versioned; a bad rotation is reversible.
- **Terraform state** is stored in an S3 bucket with versioning + object-lock, in a separate AWS account with restricted access.

### 4.5 Source code and CI/CD

- Source code is stored in **GitHub** with branch protection on `main` requiring pull-request review and passing tests.
- The `main` branch is mirror-cloned nightly to an independent Involve-controlled backup location.
- Continuous integration runs on **GitHub Actions**; container images are stored in **Amazon ECR** in the AWS London region with immutable tags for release builds.

### 4.6 Monitoring and detection

- **Amazon CloudWatch** monitors availability, error rates, latency, and resource saturation for every service.
- **AWS Health Dashboard** subscribed for real-time notification of AWS service events.
- Alarms fire to the **PagerDuty** on-call rota; the on-call engineer acknowledges within the P1 response target (30 minutes, 24×7).

## 5. Recovery procedures

### 5.1 AZ failure (routine)

**Detection:** RDS Multi-AZ failover event; ECS task health failures in one AZ. **Response:** Automatic. RDS fails over; ECS re-schedules; ALB routes to healthy tasks. **RPO achieved:** 0 (synchronous replication). **RTO achieved:** typically under 5 minutes; well within the 4-hour target.

### 5.2 Database corruption or accidental data loss

**Detection:** Customer report; internal audit; alert on unexpected data-shape changes. **Response:**

1. On-call engineer triages the scope and time window of the loss (P1).
2. Snapshot the current database state before any recovery action.
3. **Point-in-time restore** to a new RDS instance targeting a moment immediately before the loss event.
4. Diff the restored data against the live database to identify affected rows.
5. Restore affected rows into the live database via a scripted, reviewed migration.
6. Customer communication throughout via the primary tenant admin and status page.

**RPO achieved:** ≤ 15 minutes from the loss event. **RTO achieved:** target 4 hours for a scoped restore; a full-database rollback within the same target for catastrophic corruption.

### 5.3 Region-level AWS outage

M.A.R.C.U.S. is operated from the **AWS London region** (`eu-west-2`) — the only AWS region in the United Kingdom. To honour the UK data sovereignty commitment, **the standard service keeps every copy of customer data, including all backups and snapshots, inside the London region.** The region's three physically separate availability zones provide the resilience described in Sections 5.1 and 5.2.

A full-region outage — affecting all three availability zones at once — triggers the following:

1. **Communication first** — the status page and email to all tenant admins acknowledge the incident and the AWS provider event within 30 minutes.
2. **Restore in London** — service is restored in the London region as AWS brings regional services back, using the in-region Multi-AZ database, point-in-time backups, and Terraform-defined infrastructure. Throughout, each M.A.R.C.U.S. Collector keeps polling its devices locally and retries automatically; current device status and health are re-established within one polling cycle of the cloud becoming reachable.
3. **Regular updates** — status page updates at least every 60 minutes until service is restored, followed by a post-incident review.

**Optional Multi-Region Enterprise add-on.** Customers whose contracted RPO / RTO cannot tolerate a single-region posture may opt in to cross-region recovery. Because no second UK AWS region exists, this necessarily places a copy of the customer's data outside the United Kingdom (AWS Ireland / `eu-west-1`), so it is enabled **only with the customer's explicit written agreement** and is recorded in their Data Processing Agreement. Under this add-on, encrypted snapshot copies are taken every 24 hours with a 7-day retention, giving a cross-region RPO of 24 hours. Contact commercial@involve.vc.

### 5.4 Security incident

Security incidents follow the incident response runbook in *M.A.R.C.U.S. — Security & Trust* Section 10. Recovery-relevant actions:

- **Credential rotation** — all HMAC keys, database passwords, and API tokens rotated on suspected compromise.
- **Selective revocation** — a single tenant's tokens or a single Collector's key can be revoked from the portal without customer-side change.
- **Immutable audit trail** — the tenant-scoped audit log supports post-incident forensics; audit records are append-only and retained for 24 months.

### 5.5 Bad deployment

- Every Cloud release is **reversible within one maintenance window** by redeploying the prior ECS task definition.
- Database migrations are backward-compatible for at least one release step — a bad release can be rolled back without a schema rollback.
- Emergency hotfix path bypasses the standard release schedule and follows the P1 timeline.

## 6. Testing and validation

| Test | Cadence | Scope |
|---|---|---|
| **Database restore rehearsal** | Quarterly | Restore last night's RDS backup to a staging environment; validate schema, row counts, and application startup. |
| **Full-stack rebuild from code** | Semi-annually | Rebuild a complete cloud stack in London from Terraform and the latest backup; validate end-to-end request flow. (Multi-Region add-on customers additionally have their cross-region restore rehearsed.) |
| **Failover simulation** | Quarterly | Force RDS Multi-AZ failover during off-peak; measure recovery time and validate zero-downtime. |
| **Runbook walk-through** | Monthly | Duty engineer walks the primary incident runbook; identifies drift. |
| **External penetration test** | Annually | Third-party grey-box test against production; findings tracked to remediation. |

Test outcomes are recorded in the internal continuity register; material lessons are integrated into the next runbook revision.

## 7. Customer responsibilities

For M.A.R.C.U.S. to recover as designed, customers should:

- Keep the **primary tenant admin** email address current — this is the address that receives incident and maintenance communication.
- Maintain **at least one Collector per site** on supported hardware and OS; the Collector retries automatically through short cloud outages and resumes reporting as soon as the cloud is reachable.
- Export any data required for offline retention (compliance archives, regulatory copies) via the Public API on the customer's own cadence.
- Rotate their own **Microsoft Entra ID** integration if the customer's own tenant is compromised — Involve cannot recover a customer-controlled identity provider.

## 8. Continuity of Involve as a supplier

Involve maintains additional controls to protect the service from Involve-side risks:

- **Source code escrow** available on request for Enterprise customers.
- **Key personnel redundancy** — no single-person-key-holder for any production system.
- **Financial resilience** — appropriate insurance and reserves; annual accounts available on request.
- **Continuity of documentation** — this document, runbooks, and the customer-facing product documentation are maintained under source control and mirror-backed.

## 9. Contact

- **BCP / DR enquiries:** security@involve.vc
- **Commercial (Multi-Region Enterprise, source escrow):** commercial@involve.vc
- **Real-time incident information:** [status.involvecloud.com](https://status.involvecloud.com)

---

*Involve Visual Collaboration Ltd · M.A.R.C.U.S. Platform · Business Continuity & Disaster Recovery · v1.0*
*[involve.vc](https://involve.vc)*
