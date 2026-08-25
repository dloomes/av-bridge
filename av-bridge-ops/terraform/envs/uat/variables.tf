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

# Entra ID vendor SSO — set all three to enable. tenant + client id come
# from the Azure Portal app registration; secret is a client-secret value
# generated against that app. All three empty = SSO disabled (default).
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

variable "entra_vendor_client_secret" {
  description = "Entra app-registration client secret VALUE. Provide via terraform.tfvars (git-ignored)."
  type        = string
  sensitive   = true
  default     = ""
}

variable "dns_zone_name" {
  description = "Subdomain to delegate to this account for env-scoped DNS, e.g. 'uat.involvecloud.com'. The apex domain stays at whatever registrar owns it."
  type        = string
  default     = "uat.involvecloud.com"
}

variable "mapbox_public_token" {
  description = "Mapbox public access token for the portal's map view. URL-restrict it in the Mapbox dashboard so a leaked bundle can't be reused. Empty = map view renders a hint asking ops to configure a token."
  type        = string
  sensitive   = false
  default     = ""
}
