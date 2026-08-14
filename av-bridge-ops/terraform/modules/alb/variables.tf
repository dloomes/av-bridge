variable "name_prefix" {
  type = string
}

variable "vpc_id" {
  type = string
}

variable "subnet_ids" {
  description = "Public subnet IDs — ALB must be internet-facing for portal + collector reach."
  type        = list(string)
}

variable "security_group_id" {
  description = "SG allowing inbound 80/443 from internet."
  type        = string
}

variable "internal" {
  description = "false = internet-facing (default). true = internal-only."
  type        = bool
  default     = false
}

variable "enable_deletion_protection" {
  type    = bool
  default = false
}

variable "idle_timeout_seconds" {
  description = "Keep-alive idle timeout. Default 60s is fine for REST; bump for long-lived SSE/WS."
  type        = number
  default     = 60
}

variable "enable_tls" {
  description = "true = create :443 listener with the ACM cert and redirect :80 -> :443. Set as a plain boolean at the call-site so terraform can plan the count without waiting for the cert ARN to be known."
  type        = bool
  default     = false
}

variable "certificate_arn" {
  description = "ACM cert ARN. Ignored when enable_tls = false. May be an unknown value at plan time (e.g. module.cert.certificate_arn) — the listener resource itself uses this at apply."
  type        = string
  default     = ""
}

variable "tls_policy" {
  description = "ALB SSL policy. ELBSecurityPolicy-TLS13-1-2-2021-06 forbids TLS < 1.2 and enables TLS 1.3 where the client supports it."
  type        = string
  default     = "ELBSecurityPolicy-TLS13-1-2-2021-06"
}
