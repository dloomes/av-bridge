variable "name_prefix" {
  description = "Prefix for all resource names in this module."
  type        = string
}

variable "vpc_cidr" {
  type = string
}

variable "azs" {
  description = "Availability zones. Must have at least 2 for RDS + ALB to work."
  type        = list(string)

  validation {
    condition     = length(var.azs) >= 2
    error_message = "azs must contain at least 2 availability zones."
  }
}

variable "single_nat_gateway" {
  description = "true = one NAT total (UAT-cheap). false = one NAT per AZ (prod-HA)."
  type        = bool
  default     = true
}
