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
