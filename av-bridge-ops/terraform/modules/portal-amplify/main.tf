# AWS Amplify Hosting for the Next.js portal.
#
# Amplify supports Next.js SSR natively when platform = WEB_COMPUTE and the
# repo is a standard Next app. Since this repo is a monorepo (av-bridge-portal
# lives alongside cloud + bridge), the build spec lives at repo root as
# amplify.yml and points at the app root.
#
# The Amplify app is git-connected: pushes to the tracked branch trigger a
# rebuild automatically. Terraform manages the app + branch definition, not
# individual builds — those happen out-of-band on git push.

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.70"
    }
  }
}

resource "aws_amplify_app" "this" {
  name         = "${var.name_prefix}-portal"
  repository   = var.repository_url
  access_token = var.github_access_token

  # WEB_COMPUTE = SSR (Next.js API routes, getServerSideProps, middleware).
  # WEB is static-only and would break the /api/v1/* rewrites.
  platform = "WEB_COMPUTE"

  # Auto-branch-creation off — we track a fixed branch per env, not every
  # feature branch. Preview branches can be added later per PR flow.
  enable_auto_branch_creation = false
  enable_branch_auto_build    = true
  enable_branch_auto_deletion = false

  # No inline build_spec — Amplify picks up amplify.yml from the repo root
  # instead, which keeps build changes reviewable in git alongside the app.

  environment_variables = var.environment_variables

  # Amplify prepends a rule that serves index.html for any 404 in SPA mode.
  # For SSR we want Next's routing to handle everything, so pass only the
  # explicit rules the caller supplied (empty by default).
  dynamic "custom_rule" {
    for_each = var.custom_rules
    content {
      source = custom_rule.value.source
      target = custom_rule.value.target
      status = custom_rule.value.status
    }
  }

  tags = { Name = "${var.name_prefix}-portal" }

  # access_token is stored inside Amplify itself once accepted; terraform
  # can't compare against the API version, so avoid a diff on every plan.
  lifecycle {
    ignore_changes = [access_token]
  }
}

resource "aws_amplify_branch" "this" {
  app_id      = aws_amplify_app.this.id
  branch_name = var.branch_name
  stage       = var.stage

  enable_auto_build       = true
  enable_pull_request_preview = false

  framework = "Next.js - SSR"

  environment_variables = var.environment_variables

  tags = { Name = "${var.name_prefix}-portal-${var.branch_name}" }
}
