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
