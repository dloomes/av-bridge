variable "name_prefix" {
  description = "Prefix for the Secrets Manager entry name."
  type        = string
}

variable "tenant_id" {
  description = "Entra (Azure AD) tenant GUID for the vendor sign-in app registration. Empty = SSO disabled downstream."
  type        = string
  default     = ""
}

variable "client_id" {
  description = "Entra app registration (Application ID). Empty = SSO disabled downstream."
  type        = string
  default     = ""
}

variable "client_secret" {
  description = "Entra app-registration client secret VALUE. Provide via terraform.tfvars (git-ignored). Empty = no Secrets Manager entry is created and downstream stays disabled."
  type        = string
  default     = ""
  sensitive   = true
}

variable "redirect_uri" {
  description = "Fully-qualified callback URL registered against the Entra app, e.g. https://api.uat.involvecloud.com/api/v1/auth/entra/vendor/callback."
  type        = string
  default     = ""
}

variable "recovery_window_days" {
  description = "Secrets Manager recovery window on the client secret. 0 for UAT so terraform destroy && apply doesn't trip on 'pending deletion'; 7-30 for prod."
  type        = number
  default     = 0
}
