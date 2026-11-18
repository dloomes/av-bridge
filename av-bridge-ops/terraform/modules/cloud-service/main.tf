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

  # An externally supplied cluster_arn means a sibling service owns the
  # cluster; this module then only attaches its service to it.
  create_cluster = var.cluster_arn == ""

  # Per-instance resource name stem. Services sharing a name_prefix (web and
  # worker) are told apart by role; the cluster keeps the bare name_prefix.
  # UAT has no role, so its names are unchanged.
  svc         = var.role == "" ? var.name_prefix : "${var.name_prefix}-${var.role}"
  cluster_arn = local.create_cluster ? aws_ecs_cluster.this[0].arn : var.cluster_arn

  # Default reproduces the single catch-all rule this module always created.
  listener_rules = length(var.listener_rules) > 0 ? var.listener_rules : [{
    priority      = var.listener_rule_priority
    host_headers  = var.listener_host_names
    path_patterns = ["/*"]
  }]
}

# -----------------------------------------------------------------------------
# ECS cluster
# -----------------------------------------------------------------------------

moved {
  from = aws_ecs_cluster.this
  to   = aws_ecs_cluster.this[0]
}

moved {
  from = aws_ecs_cluster_capacity_providers.this
  to   = aws_ecs_cluster_capacity_providers.this[0]
}

resource "aws_ecs_cluster" "this" {
  count = local.create_cluster ? 1 : 0
  name  = "${var.name_prefix}-cluster"

  setting {
    name  = "containerInsights"
    value = var.container_insights ? "enabled" : "disabled"
  }
}

resource "aws_ecs_cluster_capacity_providers" "this" {
  count              = local.create_cluster ? 1 : 0
  cluster_name       = aws_ecs_cluster.this[0].name
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
  name              = "/ecs/${local.svc}-cloud"
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
  name               = "${local.svc}-ecs-execution"
  assume_role_policy = data.aws_iam_policy_document.task_assume.json
}

resource "aws_iam_role_policy_attachment" "execution_managed" {
  role       = aws_iam_role.execution.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

data "aws_iam_policy_document" "execution_secrets" {
  statement {
    sid     = "ReadAppSecrets"
    actions = ["secretsmanager:GetSecretValue"]
    # SMTP secret is optional — compact drops the empty string when the
    # caller doesn't wire SES, keeping the policy tight.
    resources = compact([
      var.db_master_secret_arn,
      var.cloud_secret_key_arn,
      var.admin_api_token_arn,
      var.portal_token_arn,
      var.hmac_secret_arn,
      var.smtp_credentials_secret_arn,
      var.entra_vendor_client_secret_arn,
      var.vendor_admin_password_secret_arn,
      var.db_app_credentials_secret_arn,
    ])
  }
}

resource "aws_iam_policy" "execution_secrets" {
  name   = "${local.svc}-ecs-execution-secrets"
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
  name               = "${local.svc}-ecs-task"
  assume_role_policy = data.aws_iam_policy_document.task_assume.json
}

# -----------------------------------------------------------------------------
# Task definition
# -----------------------------------------------------------------------------

resource "aws_ecs_task_definition" "this" {
  family                   = "${local.svc}-cloud"
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
      # Database URLs as plain env vars only for the legacy dev-password
      # setup (UAT); with db_app_credentials_secret_arn they come from
      # Secrets Manager (see the secrets block).
      var.db_app_credentials_secret_arn == "" ? [
        { name = "DATABASE_ADMIN_URL", value = local.admin_url },
        { name = "DATABASE_TENANT_URL", value = local.tenant_url },
      ] : [],
      [
        { name = "CLOUD_LISTEN_ADDR", value = ":8090" },
        { name = "BOOTSTRAP_POC", value = tostring(var.bootstrap_poc) },
        { name = "POC_BRIDGE_COLLECTOR_ID", value = var.poc_collector_id },
        { name = "POC_PORTAL_ROLE", value = var.poc_portal_role },
        { name = "VENDOR_ADMIN_EMAIL", value = var.vendor_admin_email },
        { name = "VENDOR_ADMIN_NAME", value = var.vendor_admin_name },
        { name = "NIGHTLY_EXEC_ENABLED", value = tostring(var.nightly_exec_enabled) },
        { name = "NIGHTLY_DRY_RUN", value = tostring(var.nightly_dry_run) },
      ],
      var.role == "" ? [] : [
        { name = "MARCUS_ROLE", value = var.role }
      ],
      # Origin Collectors are told to use for /ingest + /bridge/*. Without
      # it the install script bakes in whichever host served it — the
      # portal, when copied from Add Collector — and the portal doesn't
      # proxy /bridge or /ingest, so every poll 404s.
      var.cloud_base_url == "" ? [] : [
        { name = "CLOUD_BASE_URL", value = var.cloud_base_url }
      ],
      # Remote access session addresses: <label>.<access_domain>.
      var.access_domain == "" ? [] : [
        { name = "ACCESS_DOMAIN", value = var.access_domain }
      ],
      # Only emit VENDOR_ADMIN_PASSWORD when set — the seed step is a
      # first-boot no-op if either the email or password is blank, and the
      # value is only ever used on the first startup with an empty vendor
      # user table (see summary: "rotating here after the first boot has
      # no effect"). Rotating it post-seed is done via the API, not env.
      var.vendor_admin_password == "" ? [] : [
        { name = "VENDOR_ADMIN_PASSWORD", value = var.vendor_admin_password }
      ],
      # SMTP: host empty = the app stays in dry-run (logs the email
      # instead of sending). Emitting POC_SMTP_HOST flips it live and the
      # accompanying port/from + secrets-block username/password take
      # over. See av-bridge-cloud/internal/config/config.go.
      var.smtp_host == "" ? [] : [
        { name = "POC_SMTP_HOST", value = var.smtp_host },
        { name = "POC_SMTP_PORT", value = var.smtp_port },
        { name = "POC_SMTP_FROM", value = var.smtp_from },
      ],
      # Entra vendor SSO — the tenant + client ids are non-sensitive so
      # they ride here as plain env; the client secret is fetched via
      # Secrets Manager in the `secrets` block below.
      var.entra_vendor_client_secret_arn == "" ? [] : [
        { name = "ENTRA_VENDOR_TENANT_ID", value = var.entra_vendor_tenant_id },
        { name = "ENTRA_VENDOR_CLIENT_ID", value = var.entra_vendor_client_id },
        { name = "ENTRA_VENDOR_REDIRECT_URI", value = var.entra_vendor_redirect_uri },
        { name = "ENTRA_PORTAL_BASE_URL", value = var.entra_portal_base_url },
      ],
      var.extra_environment,
    )

    secrets = concat(
      [
        { name = "DATABASE_MIGRATION_URL", valueFrom = "${var.db_master_secret_arn}:url::" },
        { name = "CLOUD_SECRET_KEY", valueFrom = var.cloud_secret_key_arn },
        { name = "ADMIN_API_TOKEN", valueFrom = var.admin_api_token_arn },
        { name = "POC_PORTAL_TOKEN", valueFrom = var.portal_token_arn },
        { name = "POC_HMAC_SECRET", valueFrom = var.hmac_secret_arn },
      ],
      # SMTP credentials — one Secrets Manager JSON with username +
      # password keys. ECS' `:key::` syntax pulls each key out into its
      # own env var without exposing the other in the task def.
      var.smtp_credentials_secret_arn == "" ? [] : [
        { name = "POC_SMTP_USERNAME", valueFrom = "${var.smtp_credentials_secret_arn}:username::" },
        { name = "POC_SMTP_PASSWORD", valueFrom = "${var.smtp_credentials_secret_arn}:password::" },
      ],
      # Entra client secret — the whole value is the secret string (not
      # a JSON with keys), so no :key:: suffix.
      var.entra_vendor_client_secret_arn == "" ? [] : [
        { name = "ENTRA_VENDOR_CLIENT_SECRET", valueFrom = var.entra_vendor_client_secret_arn },
      ],
      # First-boot vendor admin password from Secrets Manager (plain string
      # value), so it never appears in the task definition. Use instead of
      # vendor_admin_password, not alongside it.
      var.vendor_admin_password_secret_arn == "" ? [] : [
        { name = "VENDOR_ADMIN_PASSWORD", valueFrom = var.vendor_admin_password_secret_arn },
      ],
      # App role connection URLs + passwords from one JSON secret (keys
      # admin_url, tenant_url, admin_password, tenant_password). The service
      # sets the role passwords at startup so the repo's dev passwords are
      # never valid.
      var.db_app_credentials_secret_arn == "" ? [] : [
        { name = "DATABASE_ADMIN_URL", valueFrom = "${var.db_app_credentials_secret_arn}:admin_url::" },
        { name = "DATABASE_TENANT_URL", valueFrom = "${var.db_app_credentials_secret_arn}:tenant_url::" },
        { name = "DB_APP_ADMIN_PASSWORD", valueFrom = "${var.db_app_credentials_secret_arn}:admin_password::" },
        { name = "DB_APP_TENANT_PASSWORD", valueFrom = "${var.db_app_credentials_secret_arn}:tenant_password::" },
      ],
    )

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
  name        = "${local.svc}-cloud"
  port        = 8090
  protocol    = "HTTP"
  vpc_id      = var.vpc_id
  target_type = "ip"

  lifecycle {
    precondition {
      condition     = length("${local.svc}-cloud") <= 32
      error_message = "Target group name \"${local.svc}-cloud\" exceeds 32 characters; shorten name_prefix or role."
    }
  }

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

moved {
  from = aws_lb_listener_rule.this
  to   = aws_lb_listener_rule.this["100"]
}

resource "aws_lb_listener_rule" "this" {
  for_each = { for r in local.listener_rules : tostring(r.priority) => r }

  listener_arn = var.alb_listener_arn
  priority     = each.value.priority

  # host_header requires a REAL host - ALB rejects bare "*". Until a domain
  # is attached we scope by path only (/* matches everything). When a real
  # host lands, set host_headers to add a host_header condition.
  dynamic "condition" {
    for_each = length(each.value.host_headers) > 0 ? [1] : []
    content {
      host_header {
        values = each.value.host_headers
      }
    }
  }

  dynamic "condition" {
    for_each = length(each.value.path_patterns) > 0 ? [1] : []
    content {
      path_pattern {
        values = each.value.path_patterns
      }
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
  name            = "${local.svc}-cloud"
  cluster         = local.cluster_arn
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
