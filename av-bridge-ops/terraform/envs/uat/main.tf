# UAT env stack. Composes modules with UAT-sized inputs.
# Prod uses the same modules with prod-sized inputs — the ONLY differences
# between envs should live in variables.tf / terraform.tfvars.

locals {
  # Single vendor-admin identity — used both as the initial helpdesk seed
  # user AND as the SES sandbox verified recipient / DMARC report inbox.
  # Change here and both usages update in step.
  vendor_admin_email = "dloomes@involve.vc"
  vendor_admin_name  = "Daniel Loomes"
}

module "network" {
  source = "../../modules/network"

  name_prefix        = var.name_prefix
  vpc_cidr           = var.vpc_cidr
  azs                = var.azs
  single_nat_gateway = var.single_nat_gateway
}

module "secrets" {
  source = "../../modules/secrets"

  name_prefix = var.name_prefix
  # UAT: immediate secret deletion so `terraform destroy && apply` doesn't
  # trip on "secret pending deletion". Prod should use 7-30.
  recovery_window_days = 0
}

module "db" {
  source = "../../modules/db"

  name_prefix       = var.name_prefix
  vpc_id            = module.network.vpc_id
  subnet_ids        = module.network.private_subnet_ids
  security_group_id = aws_security_group.db.id

  instance_class      = "db.t4g.micro"
  multi_az            = false
  deletion_protection = false
  # UAT: 1 day of backups is plenty. Prod: 7-14.
  backup_retention_days = 1
}

module "ecr_cloud" {
  source = "../../modules/ecr"

  # ECR repo names are per-account, so include the env suffix.
  name        = "${var.name_prefix}-cloud"
  keep_last_n = 10
}

module "dns" {
  source = "../../modules/dns-zone"

  zone_name = var.dns_zone_name
}

module "ses" {
  source = "../../modules/ses"

  name_prefix    = var.name_prefix
  sending_domain = var.dns_zone_name
  hosted_zone_id = module.dns.zone_id
  # noreply@uat.involvecloud.com is the initial From. Display name is
  # rendered in the visible sender column of mail clients.
  from_local_part   = "noreply"
  from_display_name = "AV Bridge"
  # Start in monitoring-only DMARC — flip to quarantine/reject once we've
  # observed a week or two of clean sends via the aggregate reports.
  dmarc_policy = "none"
  dmarc_rua    = local.vendor_admin_email

  # Sandbox recipients: SES rejects sends to unverified addresses until
  # production access is granted. Pre-verify the vendor admin so alert
  # test-sends land during UAT. Add more addresses here as devs join.
  verified_recipient_addresses = [local.vendor_admin_email]
}

module "cert" {
  source = "../../modules/acm-cert"

  # Wildcard covers api + app + anything future under uat.involvecloud.com.
  # Apex included so links back to the naked env domain also verify.
  primary_domain            = var.dns_zone_name
  subject_alternative_names = ["*.${var.dns_zone_name}"]
  hosted_zone_id            = module.dns.zone_id
}

module "alb" {
  source = "../../modules/alb"

  name_prefix       = var.name_prefix
  vpc_id            = module.network.vpc_id
  subnet_ids        = module.network.public_subnet_ids
  security_group_id = aws_security_group.alb.id
  enable_tls        = true
  certificate_arn   = module.cert.certificate_arn
}

module "cloud_service" {
  source = "../../modules/cloud-service"

  name_prefix       = var.name_prefix
  vpc_id            = module.network.vpc_id
  subnet_ids        = module.network.private_subnet_ids
  security_group_id = aws_security_group.app.id

  # Attach service rule to the primary listener — :443 once TLS is on. The
  # :80 listener redirects to :443, so no rule needed there.
  alb_listener_arn    = module.alb.primary_listener_arn
  listener_host_names = ["api.${var.dns_zone_name}"]

  image_url = module.ecr_cloud.repository_url
  image_tag = "latest"

  cpu           = 512
  memory        = 1024
  desired_count = 1

  db_master_secret_arn = module.db.master_secret_arn
  db_host              = module.db.address
  db_port              = module.db.port
  db_name              = module.db.db_name

  cloud_secret_key_arn = module.secrets.cloud_secret_key_arn
  admin_api_token_arn  = module.secrets.admin_api_token_arn
  portal_token_arn     = module.secrets.portal_token_arn
  hmac_secret_arn      = module.secrets.hmac_secret_arn

  # UAT: seed the PoC tenant so we can test end-to-end immediately. Password
  # is set once via terraform.tfvars for the initial boot, then rotated by
  # the admin from the portal — see variables.tf for the reasoning.
  bootstrap_poc         = true
  vendor_admin_email    = local.vendor_admin_email
  vendor_admin_name     = local.vendor_admin_name
  vendor_admin_password = var.vendor_admin_password

  # SES-backed outbound email. When these are wired, the notify package
  # sends real mail via SES SMTP; existing alert channels + the nightly
  # digest start delivering instead of dry-running. Kept flipable via
  # smtp_host — set to "" to fall back to dry-run.
  smtp_host                   = module.ses.smtp_host
  smtp_port                   = module.ses.smtp_port
  smtp_from                   = module.ses.smtp_from
  smtp_credentials_secret_arn = module.ses.smtp_credentials_secret_arn
}

# A alias record so browsers + bridge collectors reach the API at
# api.<zone> instead of the raw ALB DNS. Alias is faster + free vs CNAME.
resource "aws_route53_record" "api" {
  zone_id = module.dns.zone_id
  name    = "api.${var.dns_zone_name}"
  type    = "A"

  alias {
    name                   = module.alb.dns_name
    zone_id                = module.alb.zone_id
    evaluate_target_health = true
  }
}

locals {
  # api.<zone> URL, used both in portal env vars and as the SSR upstream.
  api_url = "https://api.${var.dns_zone_name}"
  ws_url  = "wss://api.${var.dns_zone_name}"
}

module "portal" {
  source = "../../modules/portal-amplify"

  name_prefix         = var.name_prefix
  repository_url      = var.portal_repository_url
  github_access_token = var.github_access_token
  branch_name         = var.portal_branch
  stage               = "DEVELOPMENT"

  # Custom domain: portal at app.<zone>. Amplify manages its own ACM cert
  # for CloudFront; we just add the CNAMEs Amplify hands back.
  custom_domain        = var.dns_zone_name
  custom_domain_prefix = "app"

  # Per-customer branded URLs — <slug>.<zone> routes to the same portal,
  # sign-in server component picks the branding via the Host header. The
  # wildcard ACM cert issued by module.cert covers *.<zone>, so Amplify
  # can validate the wildcard association without extra DNS work.
  enable_wildcard_subdomain = true

  environment_variables = {
    AMPLIFY_MONOREPO_APP_ROOT = "av-bridge-portal"

    # Server-side: Next.js rewrites forward /api/v1/* to the cloud API
    # via the SSR runtime. All browser HTTP calls stay SAME-ORIGIN so
    # there's no CORS to configure on the API side.
    AV_BRIDGE_UPSTREAM = local.api_url

    # Same URL, NEXT_PUBLIC_-prefixed so Next.js inlines it into the
    # server bundle at build time. Needed because Amplify's WEB_COMPUTE
    # runtime doesn't forward plain environment_variables to the SSR
    # Lambda's process.env — only build-time visibility is guaranteed.
    # Server components read the inlined constant, so pre-login branding
    # fetch works from acme.<zone>/sign-in.
    NEXT_PUBLIC_AV_BRIDGE_UPSTREAM = local.api_url

    # NEXT_PUBLIC_AV_BRIDGE_HTTP deliberately UNSET: when unset, api.ts
    # falls back to "" (empty base URL) and every fetch goes same-origin
    # via the Next.js rewrite proxy. Setting it makes the client call
    # api.uat cross-origin, which hits CORS on the Go cloud API.

    # WebSocket has to go direct — Next.js rewrites don't tunnel WS in the
    # Amplify SSR runtime. WSS from HTTPS origin is allowed by the browser.
    NEXT_PUBLIC_AV_BRIDGE_WS = local.ws_url

    NEXT_TELEMETRY_DISABLED = "1"
  }
}

# Amplify auto-creates its own Route 53 records (cert-ownership verification
# + traffic CNAME) when the target hosted zone lives in the same AWS account.
# We don't declare them here — they're owned by the domain association
# itself and terraform would just fight Amplify over them.
#
# Amplify's outputs (custom_domain_certificate_verification_dns_record,
# custom_domain_sub_domain_dns_records) are still there for informational
# use / when the DNS zone lives in a different account (not our case yet).
