# Production env stack. Same modules as envs/uat with prod-sized inputs:
# three AZs with a NAT each, Multi-AZ RDS, and the cloud binary split into
# a web service (2 copies) and a worker service (1 copy) on one cluster.
#
# DNS: involvecloud.com stays at DNS Made Easy. Three subzones are delegated
# to this account - app (portal), api (API + Collectors) and access (remote
# access session addresses). Each record and certificate validation lives in
# the zone that owns its name.

locals {
  app_domain    = "app.involvecloud.com"
  api_domain    = "api.involvecloud.com"
  access_domain = "access.involvecloud.com"

  api_url = "https://${local.api_domain}"
  ws_url  = "wss://${local.api_domain}"
  app_url = "https://${local.app_domain}"

  # Settings both cloud services carry besides the module's own (see the
  # plan's Global Constraints). ENTRA_PORTAL_BASE_URL comes from the module:
  # the Entra secret ARN below is always set, so the module always emits it.
  cloud_extra_environment = [
    { name = "BRANDED_DOMAIN", value = local.app_domain },
  ]

  entra_redirect_uri = "https://${local.app_domain}/api/v1/auth/entra/vendor/callback"
}

# -----------------------------------------------------------------------------
# Secrets created once by hand in avrmm-prod (plain string values), so no
# secret ever sits in terraform.tfvars:
#   avrmm-prod/github-token               GitHub PAT for Amplify
#   avrmm-prod/entra-vendor-client-secret Entra vendor app client secret
# A missing secret fails the plan at these data sources ("couldn't find
# resource"). The zone-only stage 1 plan doesn't read them.
# -----------------------------------------------------------------------------

data "aws_secretsmanager_secret_version" "github_token" {
  secret_id = "${var.name_prefix}/github-token"

  lifecycle {
    postcondition {
      condition     = length(trimspace(self.secret_string)) > 0
      error_message = "Secret ${var.name_prefix}/github-token is empty. Put the GitHub PAT (plain string) in it - see the Task 10 runbook."
    }
  }
}

# Metadata only: the ARN goes to the task definitions (valueFrom); terraform
# never reads the secret value.
data "aws_secretsmanager_secret" "entra_vendor_client_secret" {
  name = "${var.name_prefix}/entra-vendor-client-secret"
}

# First-boot vendor admin password: generated here, held only in Secrets
# Manager and injected via the task's secrets block. Fetch it from the
# console (output vendor_admin_password_secret_arn) for the first sign-in,
# then change it in the portal.
resource "random_password" "vendor_admin" {
  length           = 24
  override_special = "-_.!"
}

resource "aws_secretsmanager_secret" "vendor_admin_password" {
  name                    = "${var.name_prefix}/cloud/vendor_admin_password"
  description             = "First-boot vendor admin password (seed only; change after first sign-in)"
  recovery_window_in_days = 7
}

resource "aws_secretsmanager_secret_version" "vendor_admin_password" {
  secret_id     = aws_secretsmanager_secret.vendor_admin_password.id
  secret_string = random_password.vendor_admin.result
}

# -----------------------------------------------------------------------------
# DNS zones (stage 1: apply these first, then delegate at DNS Made Easy)
# -----------------------------------------------------------------------------

module "dns_app" {
  source = "../../modules/dns-zone"

  zone_name = local.app_domain
}

module "dns_api" {
  source = "../../modules/dns-zone"

  zone_name = local.api_domain
}

module "dns_access" {
  source = "../../modules/dns-zone"

  zone_name = local.access_domain
}

# -----------------------------------------------------------------------------
# Network, secrets, database, registry
# -----------------------------------------------------------------------------

module "network" {
  source = "../../modules/network"

  name_prefix        = var.name_prefix
  vpc_cidr           = var.vpc_cidr
  azs                = var.azs
  single_nat_gateway = var.single_nat_gateway
}

module "secrets" {
  source = "../../modules/secrets"

  name_prefix          = var.name_prefix
  recovery_window_days = 7
}

module "db" {
  source = "../../modules/db"

  name_prefix       = var.name_prefix
  vpc_id            = module.network.vpc_id
  subnet_ids        = module.network.private_subnet_ids
  security_group_id = aws_security_group.db.id

  instance_class = "db.t4g.medium"
  # engine_version left at the module default - the same version UAT runs.
  multi_az              = true
  backup_retention_days = 35
  deletion_protection   = true
  # Storage autoscaling: module default 20 GB growing to 100 GB.
  max_allocated_storage_gb     = 100
  performance_insights_enabled = true
  # Instance changes wait for the Sunday maintenance window.
  apply_immediately           = false
  secret_recovery_window_days = 7
}

module "ecr_cloud" {
  source = "../../modules/ecr"

  name = "${var.name_prefix}-cloud"
  # Rollback retags an earlier version as latest, so keep more history.
  keep_last_n = 20
}

# -----------------------------------------------------------------------------
# Email (SES)
# -----------------------------------------------------------------------------
#
# notify.involvecloud.com lives at DNS Made Easy, so the module creates no
# records (manage_dns = false); ses_dns_records lists what must exist there.
# The identity, DKIM and MAIL FROM were created by CLI on 2026-11-18 and
# are imported below - never recreated.

module "ses" {
  source = "../../modules/ses"

  name_prefix         = var.name_prefix
  sending_domain      = "notify.involvecloud.com"
  manage_dns          = false
  mail_from_subdomain = "bounce"
  from_local_part     = "noreply"
  from_display_name   = "\"M.A.R.C.U.S.\""
  dmarc_policy        = "none"
  dmarc_rua           = var.dmarc_rua

  secret_recovery_window_days = 7
}

import {
  to = module.ses.aws_ses_domain_identity.this
  id = "notify.involvecloud.com"
}

import {
  to = module.ses.aws_ses_domain_dkim.this
  id = "notify.involvecloud.com"
}

import {
  to = module.ses.aws_ses_domain_mail_from.this[0]
  id = "notify.involvecloud.com"
}

# Entra vendor SSO: no module.entra here. That module stores a client secret
# value passed in through tfvars; prod instead hands the hand-made secret's
# ARN straight to the services (data.aws_secretsmanager_secret above). The
# callback goes through the portal origin so authorize and callback share
# cookies (see envs/uat).

# -----------------------------------------------------------------------------
# Certificates + load balancer
# -----------------------------------------------------------------------------

module "api_cert" {
  source = "../../modules/acm-cert"

  primary_domain = local.api_domain
  hosted_zone_id = module.dns_api.zone_id
}

# Remote access session addresses, <label>.access.involvecloud.com.
module "access_cert" {
  source = "../../modules/acm-cert"

  primary_domain = "*.${local.access_domain}"
  hosted_zone_id = module.dns_access.zone_id
}

module "alb" {
  source = "../../modules/alb"

  name_prefix                = var.name_prefix
  vpc_id                     = module.network.vpc_id
  subnet_ids                 = module.network.public_subnet_ids
  security_group_id          = aws_security_group.alb.id
  enable_tls                 = true
  certificate_arn            = module.api_cert.certificate_arn
  enable_deletion_protection = true
  # tls_policy left at the module default, as UAT.
}

resource "aws_lb_listener_certificate" "access" {
  listener_arn    = module.alb.https_listener_arn
  certificate_arn = module.access_cert.certificate_arn
}

resource "aws_route53_record" "api" {
  zone_id = module.dns_api.zone_id
  name    = local.api_domain
  type    = "A"

  alias {
    name                   = module.alb.dns_name
    zone_id                = module.alb.zone_id
    evaluate_target_health = true
  }
}

resource "aws_route53_record" "access" {
  zone_id = module.dns_access.zone_id
  name    = "*.${local.access_domain}"
  type    = "A"

  alias {
    name                   = module.alb.dns_name
    zone_id                = module.alb.zone_id
    evaluate_target_health = true
  }
}

# -----------------------------------------------------------------------------
# Cloud services: web (portal API, ingest, live events) + worker (scheduled
# jobs, Collector tunnels, remote access sessions). One cluster, created by
# web, shared by worker.
# -----------------------------------------------------------------------------

module "cloud_web" {
  source = "../../modules/cloud-service"

  name_prefix       = var.name_prefix
  role              = "web"
  vpc_id            = module.network.vpc_id
  subnet_ids        = module.network.private_subnet_ids
  security_group_id = aws_security_group.app.id

  container_insights = true

  alb_listener_arn = module.alb.primary_listener_arn
  listener_rules = [
    { priority = 100, host_headers = [local.api_domain], path_patterns = ["/*"] },
  ]

  image_url = module.ecr_cloud.repository_url
  image_tag = "latest"

  cpu                = 1024
  memory             = 2048
  desired_count      = 2
  log_retention_days = 30

  db_master_secret_arn = module.db.master_secret_arn
  db_host              = module.db.address
  db_port              = module.db.port
  db_name              = module.db.db_name

  cloud_secret_key_arn = module.secrets.cloud_secret_key_arn
  admin_api_token_arn  = module.secrets.admin_api_token_arn
  portal_token_arn     = module.secrets.portal_token_arn
  hmac_secret_arn      = module.secrets.hmac_secret_arn

  # No PoC tenant in prod.
  bootstrap_poc      = false
  vendor_admin_email = var.vendor_admin_email
  vendor_admin_name  = var.vendor_admin_name
  # Password via Secrets Manager (valueFrom), never a plain env var.
  vendor_admin_password_secret_arn = aws_secretsmanager_secret_version.vendor_admin_password.arn

  nightly_dry_run = false

  smtp_host                   = module.ses.smtp_host
  smtp_port                   = module.ses.smtp_port
  smtp_from                   = module.ses.smtp_from
  smtp_credentials_secret_arn = module.ses.smtp_credentials_secret_arn

  entra_vendor_tenant_id         = var.entra_vendor_tenant_id
  entra_vendor_client_id         = var.entra_vendor_client_id
  entra_vendor_redirect_uri      = local.entra_redirect_uri
  entra_vendor_client_secret_arn = data.aws_secretsmanager_secret.entra_vendor_client_secret.arn
  entra_portal_base_url          = local.app_url
  cloud_base_url                 = local.api_url
  access_domain                  = local.access_domain

  extra_environment = local.cloud_extra_environment
}

module "cloud_worker" {
  source = "../../modules/cloud-service"

  name_prefix       = var.name_prefix
  role              = "worker"
  cluster_arn       = module.cloud_web.cluster_arn
  vpc_id            = module.network.vpc_id
  subnet_ids        = module.network.private_subnet_ids
  security_group_id = aws_security_group.app.id

  # Lower priority numbers match first: session addresses and the Collector
  # tunnel go to the worker before web's catch-all on api.
  alb_listener_arn = module.alb.primary_listener_arn
  listener_rules = [
    { priority = 10, host_headers = ["*.${local.access_domain}"], path_patterns = [] },
    { priority = 20, host_headers = [local.api_domain], path_patterns = ["/bridge/tunnel"] },
  ]

  image_url = module.ecr_cloud.repository_url
  image_tag = "latest"

  cpu                = 512
  memory             = 1024
  desired_count      = 1
  log_retention_days = 30

  db_master_secret_arn = module.db.master_secret_arn
  db_host              = module.db.address
  db_port              = module.db.port
  db_name              = module.db.db_name

  cloud_secret_key_arn = module.secrets.cloud_secret_key_arn
  admin_api_token_arn  = module.secrets.admin_api_token_arn
  portal_token_arn     = module.secrets.portal_token_arn
  hmac_secret_arn      = module.secrets.hmac_secret_arn

  bootstrap_poc      = false
  vendor_admin_email = var.vendor_admin_email
  vendor_admin_name  = var.vendor_admin_name
  # Password via Secrets Manager (valueFrom), never a plain env var.
  vendor_admin_password_secret_arn = aws_secretsmanager_secret_version.vendor_admin_password.arn

  nightly_dry_run = false

  smtp_host                   = module.ses.smtp_host
  smtp_port                   = module.ses.smtp_port
  smtp_from                   = module.ses.smtp_from
  smtp_credentials_secret_arn = module.ses.smtp_credentials_secret_arn

  entra_vendor_tenant_id         = var.entra_vendor_tenant_id
  entra_vendor_client_id         = var.entra_vendor_client_id
  entra_vendor_redirect_uri      = local.entra_redirect_uri
  entra_vendor_client_secret_arn = data.aws_secretsmanager_secret.entra_vendor_client_secret.arn
  entra_portal_base_url          = local.app_url
  cloud_base_url                 = local.api_url
  access_domain                  = local.access_domain

  extra_environment = local.cloud_extra_environment
}

# -----------------------------------------------------------------------------
# Portal (Amplify) at app.involvecloud.com + *.app.involvecloud.com
# -----------------------------------------------------------------------------
#
# The domain association targets the app zone itself (empty prefix = the
# zone apex) plus a "*" sub-domain for branded customer addresses. Amplify
# issues its own certificate and writes its validation + traffic records
# into the app zone because it lives in this account.

module "portal" {
  source = "../../modules/portal-amplify"

  name_prefix         = var.name_prefix
  repository_url      = var.portal_repository_url
  github_access_token = data.aws_secretsmanager_secret_version.github_token.secret_string
  branch_name         = var.portal_branch
  stage               = "PRODUCTION"

  custom_domain             = local.app_domain
  custom_domain_prefix      = ""
  enable_wildcard_subdomain = true

  environment_variables = {
    AMPLIFY_MONOREPO_APP_ROOT = "av-bridge-portal"

    # Same-origin rewrites to the API (server side) + build-time inlined copy.
    AV_BRIDGE_UPSTREAM             = local.api_url
    NEXT_PUBLIC_AV_BRIDGE_UPSTREAM = local.api_url
    # NEXT_PUBLIC_AV_BRIDGE_HTTP deliberately unset (see envs/uat).

    NEXT_PUBLIC_AV_BRIDGE_WS             = local.ws_url
    NEXT_PUBLIC_AV_BRIDGE_ENV            = "production"
    NEXT_PUBLIC_AV_BRIDGE_BRANDED_DOMAIN = local.app_domain
    NEXT_PUBLIC_MAPBOX_TOKEN             = var.mapbox_public_token

    # Mock-JWT sign-in presets stay hidden (the cloud refuses them anyway:
    # ALLOW_MOCK_JWT is never set in prod).
    NEXT_PUBLIC_AV_BRIDGE_ENABLE_DEV_SIGNINS = "false"

    NEXT_TELEMETRY_DISABLED = "1"
  }
}
