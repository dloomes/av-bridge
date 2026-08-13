# Bootstrap

Creates the S3 state bucket + DynamoDB lock table for a single AWS account.
Run once per account.

State for this module stays **local** — it holds only the two resources it
creates, and can be reconstructed with `terraform import` if the state file
is lost. Do not commit `terraform.tfstate*`.

---

## Run it

```powershell
# One-time
aws sso login --profile avrmm-uat

# Copy the example, edit if needed
Copy-Item uat.tfvars.example uat.tfvars

terraform init
terraform apply -var-file=uat.tfvars
```

Output includes a `backend_config_hcl` block — copy it into
`../envs/uat/backend.tf` (replacing `<stack>` with the stack name, e.g.
`network`, `platform`).

Repeat with `prod.tfvars` for the prod account when ready.

---

## What it creates

| Resource | Purpose |
|---|---|
| `s3://avrmm-tf-state-<env>` | Versioned + encrypted state bucket. Public access blocked. Non-current versions expire after 90 days. |
| DynamoDB `avrmm-tf-lock-<env>` | Terraform state locking (PAY_PER_REQUEST, PITR enabled). |

Both are tagged `Project=avrmm, Component=tf-state-backend`.
