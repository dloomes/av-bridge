variable "name_prefix" {
  type = string
}

variable "vpc_id" {
  type = string
}

variable "subnet_ids" {
  description = "Private subnet IDs — Fargate tasks must NOT be in public subnets."
  type        = list(string)
}

variable "security_group_id" {
  description = "SG attached to the tasks. Must allow inbound 8090 from ALB SG."
  type        = string
}

variable "alb_listener_arn" {
  description = "HTTP listener ARN. The module attaches a listener_rule forwarding to its target group."
  type        = string
}

variable "listener_rule_priority" {
  description = "Priority for the ALB listener rule. Must be unique per-listener. Lower = matched first."
  type        = number
  default     = 100
}

variable "listener_host_names" {
  description = "Host header(s) to route to this service. Leave empty to match any host by path only — fine while there's only one service on the listener. Once a real domain is attached, set to [\"api.example.com\"] to scope."
  type        = list(string)
  default     = []
}

# -----------------------------------------------------------------------------
# Container image
# -----------------------------------------------------------------------------

variable "image_url" {
  description = "Full ECR image URL, e.g. 123.dkr.ecr.eu-west-2.amazonaws.com/avrmm-uat-cloud"
  type        = string
}

variable "image_tag" {
  description = "Image tag to run. Use a specific SHA in prod for reproducibility; 'latest' fine for UAT auto-deploys."
  type        = string
  default     = "latest"
}

# -----------------------------------------------------------------------------
# Sizing
# -----------------------------------------------------------------------------

variable "cpu" {
  description = "Fargate CPU units. 256 = 0.25 vCPU (min). 512, 1024, 2048, 4096."
  type        = number
  default     = 512
}

variable "memory" {
  description = "Fargate memory MB. Must be valid for the chosen CPU — 1024/2048 pairs with 512 CPU."
  type        = number
  default     = 1024
}

variable "desired_count" {
  description = "Task count. 1 for UAT, 2+ for prod HA."
  type        = number
  default     = 1
}

# -----------------------------------------------------------------------------
# Secrets + DB
# -----------------------------------------------------------------------------

variable "db_master_secret_arn" {
  description = "Secrets Manager ARN holding the RDS master JSON (with a 'url' key)."
  type        = string
}

variable "db_host" {
  type = string
}

variable "db_port" {
  type    = number
  default = 5432
}

variable "db_name" {
  type    = string
  default = "avrmm"
}

variable "cloud_secret_key_arn" {
  type = string
}

variable "admin_api_token_arn" {
  type = string
}

variable "portal_token_arn" {
  type = string
}

variable "hmac_secret_arn" {
  type = string
}

# -----------------------------------------------------------------------------
# App bootstrap
# -----------------------------------------------------------------------------

variable "bootstrap_poc" {
  description = "true = seed one PoC tenant + collector on first boot. Turn off once you have real tenants."
  type        = bool
  default     = true
}

variable "poc_collector_id" {
  type    = string
  default = "poc-collector-01"
}

variable "poc_portal_role" {
  type    = string
  default = "admin"
}

variable "vendor_admin_email" {
  description = "Seed vendor (helpdesk) admin email. Only used on first boot when no vendor user exists."
  type        = string
  default     = ""
}

variable "vendor_admin_password" {
  description = "Seed vendor admin password. First-boot only; rotate via API after first login."
  type        = string
  default     = ""
  sensitive   = true
}

variable "vendor_admin_name" {
  type    = string
  default = ""
}

variable "nightly_exec_enabled" {
  type    = bool
  default = true
}

variable "log_retention_days" {
  type    = number
  default = 30
}

# -----------------------------------------------------------------------------
# SMTP (outbound email via SES)
# -----------------------------------------------------------------------------
#
# Leave smtp_host empty to keep the app in dry-run mode — email channels log
# instead of sending. Set all four to route real mail. Credentials come from
# Secrets Manager JSON with `username` + `password` keys.

variable "smtp_host" {
  description = "SMTP relay hostname. Empty = the app stays in dry-run (email logs, doesn't send). Set to the SES SMTP endpoint e.g. email-smtp.eu-west-2.amazonaws.com."
  type        = string
  default     = ""
}

variable "smtp_port" {
  description = "SMTP submission port. 587 = STARTTLS (default), 465 = implicit TLS."
  type        = string
  default     = "587"
}

variable "smtp_from" {
  description = "Full From header, e.g. \"AV Bridge <noreply@uat.involvecloud.com>\". Ignored when smtp_host is empty."
  type        = string
  default     = ""
}

variable "smtp_credentials_secret_arn" {
  description = "Secrets Manager ARN of a JSON secret with `username` + `password` keys (SES SMTP creds derived from an IAM access key). Ignored when smtp_host is empty. Grant the execution role secretsmanager:GetSecretValue on this ARN."
  type        = string
  default     = ""
}

# -----------------------------------------------------------------------------
# Entra ID vendor SSO
# -----------------------------------------------------------------------------
#
# Leave entra_vendor_client_secret_arn empty to keep the vendor SSO path
# disabled — the sign-in tile stays inert and the callback routes return
# 404. Setting all four (tenant_id, client_id, client_secret_arn, redirect_uri)
# turns it on: the cloud process reads the secret at startup and registers
# the /api/v1/auth/entra/vendor/{authorize,callback} routes.
#
# The redirect_uri must exactly match the redirect registered against the
# Entra app registration — Microsoft rejects a mismatch with AADSTS500113.

variable "entra_vendor_tenant_id" {
  description = "Entra tenant GUID for the vendor SSO app registration. Ignored when entra_vendor_client_secret_arn is empty."
  type        = string
  default     = ""
}

variable "entra_vendor_client_id" {
  description = "Entra app registration Application ID. Ignored when entra_vendor_client_secret_arn is empty."
  type        = string
  default     = ""
}

variable "entra_vendor_redirect_uri" {
  description = "Absolute URL of the callback endpoint as registered in the Entra app. e.g. https://api.uat.involvecloud.com/api/v1/auth/entra/vendor/callback"
  type        = string
  default     = ""
}

variable "entra_vendor_client_secret_arn" {
  description = "Secrets Manager ARN of the Entra client secret (plain string value). Empty = vendor SSO disabled. Grants the execution role secretsmanager:GetSecretValue on this ARN when set."
  type        = string
  default     = ""
}

variable "entra_portal_base_url" {
  description = "Origin the callback redirects the browser back to after minting the session, e.g. https://app.uat.involvecloud.com. Empty falls back to the callback request's own scheme+host (fine when portal and API share an origin)."
  type        = string
  default     = ""
}
