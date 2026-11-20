# Operations runbooks

Runbooks for the M.A.R.C.U.S. production environment (AWS account `568339496641`,
CLI profile `avrmm-prod`, region `eu-west-2`). UAT is account `628088982408`
(profile `avrmm-uat`). Sign in first: `aws sso login --profile avrmm-prod`.

| Runbook | Purpose | Cadence | Customer-visible |
|---|---|---|---|
| [release-and-promote.md](release-and-promote.md) | Tag a release, test in UAT, promote to production, roll back | Every release | Brief (rolling deploy) |
| [laptop-release-fallback.md](laptop-release-fallback.md) | Build, sign, push and deploy by hand when GitHub Actions cannot | Emergency only | Brief (rolling deploy) |
| [restore-rehearsal.md](restore-rehearsal.md) | Prove the database backups restore, and measure RTO and RPO | Quarterly | No |
| [failover-simulation.md](failover-simulation.md) | Force an RDS Multi-AZ failover and measure recovery | Quarterly | Yes (short outage) |
| [full-stack-rebuild.md](full-stack-rebuild.md) | Rebuild the whole stack from terraform plus a backup in a separate environment | Semi-annual | No (but costs money) |

## Conventions

- Every runbook has a **Record** table at the end. After each run, add a row and
  commit it (`Runbook record: <name> <date>`).
- Steps marked **DESTRUCTIVE** or **CUSTOMER-VISIBLE** need the stated approval
  before you run them.
- Placeholders are written `<like-this>`. Never put secrets, key file paths,
  tokens or personal data in a runbook or its Record table.
- Names used throughout: ECS cluster `avrmm-prod-cluster`, services
  `avrmm-prod-web-cloud` (2 tasks) and `avrmm-prod-worker-cloud` (1 task), ECR
  `avrmm-prod-cloud` (task definitions run `:latest`), RDS instance
  `avrmm-prod-postgres` (PostgreSQL 16, Multi-AZ, 35-day point-in-time recovery,
  deletion protection on), SNS topic `avrmm-prod-alerts`, health check
  `https://api.involvecloud.com/healthz`, portal on Amplify app `avrmm-prod-portal`
  (id `d3mm9kij0w4gk`, branch `release`, `https://app.involvecloud.com`), secrets
  under `avrmm-prod/*`, terraform in `av-bridge-ops/terraform/envs/prod` (state
  bucket `avrmm-tf-state-prod`, lock table `avrmm-tf-lock-prod`), code mirror
  `s3://avrmm-prod-code-mirror/av-bridge/`.
- The approver for everything here is Daniel unless stated otherwise.
