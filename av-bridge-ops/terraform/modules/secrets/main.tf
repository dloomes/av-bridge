# App-layer secrets. Terraform generates and stores each value in Secrets
# Manager. The ECS task definition references these secrets by ARN and
# AWS injects them into the task at start-up — they never appear in
# task-def env plaintext, ECS console, or CloudWatch Logs.
#
# NOTE: random_* values ARE written to the terraform state file. The state
# bucket is encrypted + private + non-versioned-cleanup-90d, but rotating a
# value here still creates a state row that persisted the OLD value in a
# previous state version until it expires. Treat state bucket access as
# equivalent to knowing every secret in the account.

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.70"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
  }
}

# 32 bytes hex = 64 chars. Matches the CLOUD_SECRET_KEY format used in
# docker-compose.yml (comment: "64 hex chars — 32 bytes").
resource "random_id" "cloud_secret_key" {
  byte_length = 32
}

resource "aws_secretsmanager_secret" "cloud_secret_key" {
  name                    = "${var.name_prefix}/cloud/secret_key"
  description             = "CLOUD_SECRET_KEY — encrypts at-rest fields (HMAC secrets etc.)"
  recovery_window_in_days = var.recovery_window_days
}

resource "aws_secretsmanager_secret_version" "cloud_secret_key" {
  secret_id     = aws_secretsmanager_secret.cloud_secret_key.id
  secret_string = random_id.cloud_secret_key.hex
}

# Bearer tokens — long random strings.
resource "random_password" "admin_api_token" {
  length  = 48
  special = false
}

resource "aws_secretsmanager_secret" "admin_api_token" {
  name                    = "${var.name_prefix}/cloud/admin_api_token"
  description             = "ADMIN_API_TOKEN — gates /admin/* endpoints"
  recovery_window_in_days = var.recovery_window_days
}

resource "aws_secretsmanager_secret_version" "admin_api_token" {
  secret_id     = aws_secretsmanager_secret.admin_api_token.id
  secret_string = random_password.admin_api_token.result
}

resource "random_password" "portal_token" {
  length  = 48
  special = false
}

resource "aws_secretsmanager_secret" "portal_token" {
  name                    = "${var.name_prefix}/cloud/portal_token"
  description             = "POC_PORTAL_TOKEN — bearer for portal → cloud API"
  recovery_window_in_days = var.recovery_window_days
}

resource "aws_secretsmanager_secret_version" "portal_token" {
  secret_id     = aws_secretsmanager_secret.portal_token.id
  secret_string = random_password.portal_token.result
}

resource "random_password" "hmac_secret" {
  length  = 48
  special = false
}

resource "aws_secretsmanager_secret" "hmac_secret" {
  name                    = "${var.name_prefix}/cloud/hmac_secret"
  description             = "POC_HMAC_SECRET — shared secret for the PoC collector"
  recovery_window_in_days = var.recovery_window_days
}

resource "aws_secretsmanager_secret_version" "hmac_secret" {
  secret_id     = aws_secretsmanager_secret.hmac_secret.id
  secret_string = random_password.hmac_secret.result
}
