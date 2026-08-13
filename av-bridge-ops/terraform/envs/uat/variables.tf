variable "region" {
  type    = string
  default = "eu-west-2"
}

variable "aws_profile" {
  type    = string
  default = "avrmm-uat"
}

variable "name_prefix" {
  description = "Prefix baked into every resource name so a second env in the same account (or shared VPCs) wouldn't collide."
  type        = string
  default     = "avrmm-uat"
}

variable "vpc_cidr" {
  type    = string
  default = "10.10.0.0/16"
}

variable "azs" {
  description = "Availability zones to use. Two is enough for UAT; prod may want three."
  type        = list(string)
  default     = ["eu-west-2a", "eu-west-2b"]
}

variable "single_nat_gateway" {
  description = "true = one NAT for the whole VPC (cheap, UAT). false = one NAT per AZ (HA, prod)."
  type        = bool
  default     = true
}

variable "portal_repository_url" {
  type    = string
  default = "https://github.com/dloomes/av-bridge"
}

variable "portal_branch" {
  description = "Branch that this env auto-builds from. UAT tracks main by default."
  type        = string
  default     = "main"
}

variable "github_access_token" {
  description = "GitHub PAT for Amplify. Provide via terraform.tfvars (git-ignored) — never commit."
  type        = string
  sensitive   = true
}

variable "vendor_admin_password" {
  description = "First-boot seed password for the vendor (helpdesk) admin. Only used when no vendor user exists yet — the app skips otherwise. Rotate immediately after first login. Provide via terraform.tfvars (git-ignored)."
  type        = string
  sensitive   = true
  default     = ""
}
