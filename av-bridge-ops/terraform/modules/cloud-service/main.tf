# ECS Fargate service running the av-bridge-cloud Go binary.
#
# Responsibilities:
#   * ECS cluster (per env) + Fargate service on it
#   * Task definition wiring the container to Secrets Manager for sensitive
#     env, plaintext env for the rest
#   * IAM: execution role (ECR pull, secret fetch, log push) + task role
#     (empty — the app itself makes no AWS API calls today)
#   * Target group + listener rule on the shared ALB, health-checked at
#     /healthz
#   * CloudWatch log group
#
# The DATABASE_MIGRATION_URL is injected from Secrets Manager (JSON key
# 'url'). The app-role URLs (app_admin, app_tenant) use passwords hardcoded
# in migration 0002_rls.sql — reproduce them here in plaintext env. Move
# these to their own secrets when we rotate away from the dev passwords.

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.70"
    }
  }
}

data "aws_region" "current" {}

locals {
  container_name = "cloud"

  admin_url  = "postgres://app_admin:app_admin_dev@${var.db_host}:${var.db_port}/${var.db_name}?sslmode=require"
  tenant_url = "postgres://app_tenant:app_tenant_dev@${var.db_host}:${var.db_port}/${var.db_name}?sslmode=require"
}

# -----------------------------------------------------------------------------
# ECS cluster
# -----------------------------------------------------------------------------

resource "aws_ecs_cluster" "this" {
  name = "${var.name_prefix}-cluster"

  setting {
    name  = "containerInsights"
    value = "enabled"
  }
}

resource "aws_ecs_cluster_capacity_providers" "this" {
  cluster_name       = aws_ecs_cluster.this.name
  capacity_providers = ["FARGATE", "FARGATE_SPOT"]

  default_capacity_provider_strategy {
    capacity_provider = "FARGATE"
    weight            = 1
    base              = 1
  }
}

# -----------------------------------------------------------------------------
# Logs
# -----------------------------------------------------------------------------

resource "aws_cloudwatch_log_group" "this" {
  name              = "/ecs/${var.name_prefix}-cloud"
  retention_in_days = var.log_retention_days
}

# -----------------------------------------------------------------------------
# IAM
# -----------------------------------------------------------------------------

# Execution role — used BY ECS itself to pull the image, fetch secrets, and
# ship logs. Never assumed by application code.
data "aws_iam_policy_document" "task_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "execution" {
  name               = "${var.name_prefix}-ecs-execution"
  assume_role_policy = data.aws_iam_policy_document.task_assume.json
}

resource "aws_iam_role_policy_attachment" "execution_managed" {
  role       = aws_iam_role.execution.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

data "aws_iam_policy_document" "execution_secrets" {
  statement {
    sid       = "ReadAppSecrets"
    actions   = ["secretsmanager:GetSecretValue"]
    resources = [
      var.db_master_secret_arn,
      var.cloud_secret_key_arn,
      var.admin_api_token_arn,
      var.portal_token_arn,
      var.hmac_secret_arn,
    ]
  }
}

resource "aws_iam_policy" "execution_secrets" {
  name   = "${var.name_prefix}-ecs-execution-secrets"
  policy = data.aws_iam_policy_document.execution_secrets.json
}

resource "aws_iam_role_policy_attachment" "execution_secrets" {
  role       = aws_iam_role.execution.name
  policy_arn = aws_iam_policy.execution_secrets.arn
}

# Task role — assumed by the CONTAINER at runtime. Empty for now: the Go
# binary makes no AWS API calls. When we add S3 backups, KMS, etc, attach
# policies here.
resource "aws_iam_role" "task" {
  name               = "${var.name_prefix}-ecs-task"
  assume_role_policy = data.aws_iam_policy_document.task_assume.json
}

# -----------------------------------------------------------------------------
# Task definition
# -----------------------------------------------------------------------------

resource "aws_ecs_task_definition" "this" {
  family                   = "${var.name_prefix}-cloud"
  network_mode             = "awsvpc"
  requires_compatibilities = ["FARGATE"]
  cpu                      = var.cpu
  memory                   = var.memory
  execution_role_arn       = aws_iam_role.execution.arn
  task_role_arn            = aws_iam_role.task.arn

  container_definitions = jsonencode([{
    name      = local.container_name
    image     = "${var.image_url}:${var.image_tag}"
    essential = true

    portMappings = [{
      containerPort = 8090
      protocol      = "tcp"
    }]

    environment = concat(
      [
        { name = "CLOUD_LISTEN_ADDR", value = ":8090" },
        { name = "DATABASE_ADMIN_URL", value = local.admin_url },
        { name = "DATABASE_TENANT_URL", value = local.tenant_url },
        { name = "BOOTSTRAP_POC", value = tostring(var.bootstrap_poc) },
        { name = "POC_BRIDGE_COLLECTOR_ID", value = var.poc_collector_id },
        { name = "POC_PORTAL_ROLE", value = var.poc_portal_role },
        { name = "VENDOR_ADMIN_EMAIL", value = var.vendor_admin_email },
        { name = "VENDOR_ADMIN_NAME", value = var.vendor_admin_name },
        { name = "NIGHTLY_EXEC_ENABLED", value = tostring(var.nightly_exec_enabled) },
      ],
      # Only emit VENDOR_ADMIN_PASSWORD when set — the seed step is a
      # first-boot no-op if either the email or password is blank, and the
      # value is only ever used on the first startup with an empty vendor
      # user table (see summary: "rotating here after the first boot has
      # no effect"). Rotating it post-seed is done via the API, not env.
      var.vendor_admin_password == "" ? [] : [
        { name = "VENDOR_ADMIN_PASSWORD", value = var.vendor_admin_password }
      ],
    )

    secrets = [
      { name = "DATABASE_MIGRATION_URL", valueFrom = "${var.db_master_secret_arn}:url::" },
      { name = "CLOUD_SECRET_KEY", valueFrom = var.cloud_secret_key_arn },
      { name = "ADMIN_API_TOKEN", valueFrom = var.admin_api_token_arn },
      { name = "POC_PORTAL_TOKEN", valueFrom = var.portal_token_arn },
      { name = "POC_HMAC_SECRET", valueFrom = var.hmac_secret_arn },
      # Vendor admin password uses a Secrets Manager entry when non-empty; if
      # left blank at deploy, the seed step in the app skips creating a user.
      # We keep it configurable but never store the plaintext in task-def env.
      # See vendor_admin_password variable — passed as plain env when set.
    ]

    logConfiguration = {
      logDriver = "awslogs"
      options = {
        "awslogs-group"         = aws_cloudwatch_log_group.this.name
        "awslogs-region"        = data.aws_region.current.name
        "awslogs-stream-prefix" = "cloud"
      }
    }
  }])
}

# -----------------------------------------------------------------------------
# ALB target group + listener rule
# -----------------------------------------------------------------------------

resource "aws_lb_target_group" "this" {
  name        = "${var.name_prefix}-cloud"
  port        = 8090
  protocol    = "HTTP"
  vpc_id      = var.vpc_id
  target_type = "ip"

  health_check {
    path                = "/healthz"
    healthy_threshold   = 2
    unhealthy_threshold = 3
    timeout             = 5
    interval            = 15
    matcher             = "200-299"
  }

  deregistration_delay = 30
}

resource "aws_lb_listener_rule" "this" {
  listener_arn = var.alb_listener_arn
  priority     = var.listener_rule_priority

  # host_header requires a REAL host — ALB rejects bare "*". Until a domain
  # is attached we scope by path only (/* matches everything). When a real
  # host lands, set listener_host_names to add a host_header condition.
  dynamic "condition" {
    for_each = length(var.listener_host_names) > 0 ? [1] : []
    content {
      host_header {
        values = var.listener_host_names
      }
    }
  }

  condition {
    path_pattern {
      values = ["/*"]
    }
  }

  action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.this.arn
  }
}

# -----------------------------------------------------------------------------
# Service
# -----------------------------------------------------------------------------

resource "aws_ecs_service" "this" {
  name            = "${var.name_prefix}-cloud"
  cluster         = aws_ecs_cluster.this.id
  task_definition = aws_ecs_task_definition.this.arn
  desired_count   = var.desired_count
  launch_type     = "FARGATE"

  network_configuration {
    subnets          = var.subnet_ids
    security_groups  = [var.security_group_id]
    assign_public_ip = false
  }

  load_balancer {
    target_group_arn = aws_lb_target_group.this.arn
    container_name   = local.container_name
    container_port   = 8090
  }

  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  # Circuit-breaker rolls back to the last-good task def if new tasks can't
  # pass health checks. Combined with min_healthy=100/max=200 = zero-downtime.
  deployment_maximum_percent         = 200
  deployment_minimum_healthy_percent = 100

  # Give tasks a beat before ALB starts health-checking — the Go binary runs
  # migrations on startup.
  health_check_grace_period_seconds = 60

  # Ignore desired_count changes so autoscaling (when we add it) can adjust
  # without terraform fighting.
  lifecycle {
    ignore_changes = [desired_count]
  }

  depends_on = [aws_lb_listener_rule.this]
}
