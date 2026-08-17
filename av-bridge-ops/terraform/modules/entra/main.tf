# Microsoft Entra ID (Azure AD) vendor SSO secret plumbing.
#
# What this module does NOT do: create the Entra app registration itself.
# Registering the app has to happen in the Azure Portal (or via the
# azuread terraform provider, out of scope for M1) — the operator does
# that once, then hands us the tenant_id / client_id / client_secret to
# store here.
#
# What this DOES:
#   * Stores the client_secret in Secrets Manager so the ECS task can
#     pull it at runtime without ever seeing it in plaintext env or in
#     `aws ecs describe-task-definition` output.
#   * Exposes tenant_id / client_id / redirect_uri as passthroughs so the
#     env stack composes cleanly (one module.entra reference, not four
#     separate variables threaded through).
#
# Guard: when client_secret is empty (the default), no Secrets Manager
# entry is created and the ARN outputs empty. cloud-service then skips
# emitting the ENTRA_VENDOR_* env + secrets, keeping the app in
# "no vendor SSO" mode. This lets terraform apply run before the operator
# has finished the Azure portal setup.

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.70"
    }
  }
}

locals {
  enabled = var.client_secret != ""
}

resource "aws_secretsmanager_secret" "client_secret" {
  count                   = local.enabled ? 1 : 0
  name                    = "${var.name_prefix}/cloud/entra_vendor_client_secret"
  description             = "Entra vendor SSO client secret — Microsoft OAuth code exchange."
  recovery_window_in_days = var.recovery_window_days
}

resource "aws_secretsmanager_secret_version" "client_secret" {
  count         = local.enabled ? 1 : 0
  secret_id     = aws_secretsmanager_secret.client_secret[0].id
  secret_string = var.client_secret
}
