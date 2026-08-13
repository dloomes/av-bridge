variable "region" {
  description = "AWS region for the state backend."
  type        = string
  default     = "eu-west-2"
}

variable "aws_profile" {
  description = "AWS CLI profile with access to the target account (see `aws configure sso`)."
  type        = string
}

variable "environment" {
  description = "Environment name (uat, prod). Ties bucket + table names to the account."
  type        = string

  validation {
    condition     = contains(["uat", "prod"], var.environment)
    error_message = "environment must be one of: uat, prod."
  }
}

variable "name_prefix" {
  description = "Prefix for the state bucket and lock table names. Should be short and stable — used across all env stacks in the same account."
  type        = string
  default     = "avrmm"
}
