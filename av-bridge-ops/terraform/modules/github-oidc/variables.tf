variable "role_name" {
  type    = string
  default = "github-deploy"
}

variable "github_subjects" {
  description = "OIDC sub claim patterns allowed to assume the role (StringLike)."
  type        = list(string)
}

variable "create_oidc_provider" {
  description = "Create the GitHub OIDC provider. Set false (and pass existing_oidc_provider_arn) when the account already has one."
  type        = bool
  default     = true
}

variable "existing_oidc_provider_arn" {
  type    = string
  default = ""
}

variable "ecr_push_repository_arns" {
  description = "ECR repositories the role may push to (and pull from)."
  type        = list(string)
  default     = []
}

variable "ecr_pull_repository_arns" {
  description = "ECR repositories the role may only pull from."
  type        = list(string)
  default     = []
}

variable "ecs_cluster_name" {
  type = string
}

variable "ecs_service_names" {
  description = "ECS services the role may update and describe."
  type        = list(string)
}

variable "rds_instance_identifier" {
  description = "When set, grants manual snapshot create/describe/delete for this instance."
  type        = string
  default     = ""
}

variable "rds_snapshot_prefix" {
  description = "Only snapshots whose identifier starts with this are touched."
  type        = string
  default     = ""
}

variable "s3_put_object_arns" {
  description = "Object ARN patterns (bucket/*) the role may s3:PutObject to."
  type        = list(string)
  default     = []
}
