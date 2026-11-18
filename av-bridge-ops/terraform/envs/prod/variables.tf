variable "region" {
  type    = string
  default = "eu-west-2"
}

variable "aws_profile" {
  type    = string
  default = "avrmm-prod"
}

variable "name_prefix" {
  description = "Prefix baked into every resource name."
  type        = string
  default     = "avrmm-prod"
}

variable "vpc_cidr" {
  description = "Prod VPC range. Distinct from UAT's 10.10.0.0/16 so the two could ever be peered."
  type        = string
  default     = "10.20.0.0/16"
}

variable "azs" {
  description = "Three AZs: web copies, NAT gateways and the RDS standby spread across them."
  type        = list(string)
  default     = ["eu-west-2a", "eu-west-2b", "eu-west-2c"]
}

variable "single_nat_gateway" {
  description = "false = one NAT per AZ, so losing an AZ doesn't cut the others' egress."
  type        = bool
  default     = false
}

variable "portal_repository_url" {
  type    = string
  default = "https://github.com/dloomes/av-bridge"
}

variable "portal_branch" {
  description = "Branch the prod portal builds from. Releases are promoted to it; never main."
  type        = string
  default     = "release"
}

variable "vendor_admin_email" {
  description = "First-boot vendor (helpdesk) admin email. Provide via terraform.tfvars (git-ignored)."
  type        = string
  default     = ""
}

variable "vendor_admin_name" {
  description = "First-boot vendor admin display name."
  type        = string
  default     = ""
}

# Entra ID vendor SSO. The client secret is the hand-made Secrets Manager
# secret avrmm-prod/entra-vendor-client-secret (see main.tf). Empty ids =
# the app logs a warning and leaves vendor SSO off.
variable "entra_vendor_tenant_id" {
  description = "Entra tenant GUID for the vendor SSO app registration."
  type        = string
  default     = ""
}

variable "entra_vendor_client_id" {
  description = "Entra app registration Application ID (client_id)."
  type        = string
  default     = ""
}

variable "dmarc_rua" {
  description = "DMARC aggregate report address. Only shapes the DMARC value in the ses_dns_records output - the record itself lives at DNS Made Easy."
  type        = string
  default     = ""
}

variable "mapbox_public_token" {
  description = "Mapbox public access token for the portal's map view (URL-restricted in the Mapbox dashboard). Empty = map view shows an 'add a token' hint."
  type        = string
  default     = ""
}

variable "alert_email" {
  description = "Address that receives production alarm emails (SNS subscription; must be confirmed)."
  type        = string
}
