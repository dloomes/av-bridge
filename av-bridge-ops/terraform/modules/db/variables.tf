variable "name_prefix" {
  type = string
}

variable "vpc_id" {
  type = string
}

variable "subnet_ids" {
  description = "Private subnet IDs across at least 2 AZs (RDS requires >= 2 even for single-AZ)."
  type        = list(string)
}

variable "security_group_id" {
  description = "SG that will be attached to the RDS instance. Inbound rules for 5432 live in the caller."
  type        = string
}

variable "db_name" {
  type    = string
  default = "avrmm"
}

variable "master_username" {
  type    = string
  default = "avrmm_master"
}

variable "instance_class" {
  description = "db.t4g.micro is ~£10/mo. Bump for prod."
  type        = string
  default     = "db.t4g.micro"
}

variable "allocated_storage_gb" {
  type    = number
  default = 20
}

variable "max_allocated_storage_gb" {
  description = "Storage autoscaling ceiling. 0 disables autoscaling."
  type        = number
  default     = 100
}

variable "multi_az" {
  description = "true = HA (prod). false = single-AZ (UAT-cheap)."
  type        = bool
  default     = false
}

variable "backup_retention_days" {
  type    = number
  default = 7
}

variable "deletion_protection" {
  description = "Belt-and-braces guard against `terraform destroy` in UAT. Turn on for prod."
  type        = bool
  default     = false
}

variable "engine_version" {
  description = "Postgres engine version. Pin to a specific minor (e.g. \"16.6\") for reproducibility. Query available versions with: aws rds describe-db-engine-versions --engine postgres --query \"DBEngineVersions[?starts_with(EngineVersion, '16.')].EngineVersion\""
  type        = string
  default     = "16.14"
}

variable "secret_recovery_window_days" {
  type    = number
  default = 0
}
