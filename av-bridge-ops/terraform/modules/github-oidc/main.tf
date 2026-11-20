# GitHub Actions deploy role, assumed through the GitHub OIDC provider (no
# long-lived keys). Permissions are scoped to named resources; the only "*"
# is ecr:GetAuthorizationToken, which AWS does not scope.

data "aws_caller_identity" "current" {}
data "aws_region" "current" {}

locals {
  account_id = data.aws_caller_identity.current.account_id
  region     = data.aws_region.current.name

  oidc_provider_arn = var.create_oidc_provider ? aws_iam_openid_connect_provider.github[0].arn : var.existing_oidc_provider_arn

  ecr_push_arns = var.ecr_push_repository_arns
  ecr_pull_arns = distinct(concat(var.ecr_push_repository_arns, var.ecr_pull_repository_arns))

  ecs_service_arns = [for s in var.ecs_service_names : "arn:aws:ecs:${local.region}:${local.account_id}:service/${var.ecs_cluster_name}/${s}"]

  rds_instance_arn = "arn:aws:rds:${local.region}:${local.account_id}:db:${var.rds_instance_identifier}"
  rds_snapshot_arn = "arn:aws:rds:${local.region}:${local.account_id}:snapshot:${var.rds_snapshot_prefix}*"
}

resource "aws_iam_openid_connect_provider" "github" {
  count = var.create_oidc_provider ? 1 : 0

  url            = "https://token.actions.githubusercontent.com"
  client_id_list = ["sts.amazonaws.com"]
  # AWS validates the GitHub certificate chain itself; the thumbprint is
  # required by the API/provider but ignored for this issuer.
  thumbprint_list = ["6938fd4d98bab03faadb97b34396831e3780aea1"]
}

data "aws_iam_policy_document" "trust" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]

    principals {
      type        = "Federated"
      identifiers = [local.oidc_provider_arn]
    }

    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"
      values   = ["sts.amazonaws.com"]
    }

    condition {
      test     = "StringLike"
      variable = "token.actions.githubusercontent.com:sub"
      values   = var.github_subjects
    }
  }
}

resource "aws_iam_role" "this" {
  name                 = var.role_name
  assume_role_policy   = data.aws_iam_policy_document.trust.json
  max_session_duration = 3600
}

data "aws_iam_policy_document" "permissions" {
  dynamic "statement" {
    for_each = length(local.ecr_pull_arns) > 0 ? [1] : []
    content {
      sid       = "EcrAuthToken"
      actions   = ["ecr:GetAuthorizationToken"]
      resources = ["*"]
    }
  }

  dynamic "statement" {
    for_each = length(local.ecr_push_arns) > 0 ? [1] : []
    content {
      sid = "EcrPush"
      actions = [
        "ecr:InitiateLayerUpload",
        "ecr:UploadLayerPart",
        "ecr:CompleteLayerUpload",
        "ecr:PutImage",
      ]
      resources = local.ecr_push_arns
    }
  }

  dynamic "statement" {
    for_each = length(local.ecr_pull_arns) > 0 ? [1] : []
    content {
      sid = "EcrPull"
      actions = [
        "ecr:BatchCheckLayerAvailability",
        "ecr:BatchGetImage",
        "ecr:DescribeImages",
        "ecr:GetDownloadUrlForLayer",
      ]
      resources = local.ecr_pull_arns
    }
  }

  dynamic "statement" {
    for_each = length(var.ecs_service_names) > 0 ? [1] : []
    content {
      sid       = "EcsDeploy"
      actions   = ["ecs:UpdateService", "ecs:DescribeServices"]
      resources = local.ecs_service_arns
    }
  }

  dynamic "statement" {
    for_each = var.rds_instance_identifier != "" ? [1] : []
    content {
      sid       = "RdsSnapshotCreate"
      actions   = ["rds:CreateDBSnapshot"]
      resources = [local.rds_instance_arn, local.rds_snapshot_arn]
    }
  }

  dynamic "statement" {
    for_each = var.rds_instance_identifier != "" ? [1] : []
    content {
      sid       = "RdsSnapshotDescribe"
      actions   = ["rds:DescribeDBSnapshots"]
      resources = [local.rds_instance_arn, "arn:aws:rds:${local.region}:${local.account_id}:snapshot:*"]
    }
  }

  dynamic "statement" {
    for_each = var.rds_instance_identifier != "" ? [1] : []
    content {
      sid       = "RdsSnapshotDelete"
      actions   = ["rds:DeleteDBSnapshot"]
      resources = [local.rds_snapshot_arn]
    }
  }

  dynamic "statement" {
    for_each = length(var.s3_put_object_arns) > 0 ? [1] : []
    content {
      sid       = "S3Put"
      actions   = ["s3:PutObject", "s3:AbortMultipartUpload"]
      resources = var.s3_put_object_arns
    }
  }
}

resource "aws_iam_role_policy" "this" {
  name   = "deploy"
  role   = aws_iam_role.this.id
  policy = data.aws_iam_policy_document.permissions.json
}
