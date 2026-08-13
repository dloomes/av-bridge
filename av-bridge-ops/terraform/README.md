# av-bridge terraform

Cloud infrastructure for the multi-tenant MEDIO Assist stack (UAT + prod).
Runs the Go cloud API, RDS Postgres, and hosts the Next.js portal on
AWS Amplify. On-prem collector deployment lives alongside in `../ansible/`.

---

## Layout

```
terraform/
├── bootstrap/          # one-shot per account: S3 state bucket + DDB lock table
├── modules/            # reusable building blocks (network, db, ecs, alb, ...)
└── envs/
    ├── uat/            # avrmm-uat account
    └── prod/           # avrmm-prod account
```

Both env stacks compose the same modules — the only differences live in
`terraform.tfvars` (instance sizes, single-AZ vs multi-AZ, domain).

---

## Prerequisites

- Terraform >= 1.9
- AWS CLI v2 configured with SSO profiles `avrmm-uat` and `avrmm-prod`
  (see [../docs/aws-sso-setup.md](../docs/aws-sso-setup.md) when written)
- Membership of the `AdministratorAccess` permission set in IAM Identity Center

Log in before running any command:

```powershell
aws sso login --profile avrmm-uat
```

---

## First-time setup (per account)

```powershell
cd bootstrap
terraform init
terraform apply -var-file=uat.tfvars       # or prod.tfvars

# Once applied, terraform prints the backend config to copy into envs/*/backend.tf
```

State for `bootstrap` itself stays local — it only holds the bucket + lock
table, and can be recreated by re-importing if lost.

---

## Deploying an env

```powershell
cd envs/uat
terraform init
terraform apply
```
