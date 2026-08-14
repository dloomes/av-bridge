variable "name_prefix" {
  type = string
}

variable "repository_url" {
  description = "Full GitHub repo URL, e.g. https://github.com/dloomes/av-bridge"
  type        = string
}

variable "github_access_token" {
  description = "GitHub Personal Access Token with repo + admin:repo_hook scopes. Amplify uses it to register a webhook + read the repo on build. Store in envs/<env>/terraform.tfvars (git-ignored)."
  type        = string
  sensitive   = true
}

variable "branch_name" {
  description = "Branch the UAT env deploys from. Pushes trigger a rebuild."
  type        = string
  default     = "main"
}

variable "stage" {
  description = "Amplify branch stage. PRODUCTION for prod, DEVELOPMENT for UAT — affects only the label in the console."
  type        = string
  default     = "DEVELOPMENT"

  validation {
    condition     = contains(["PRODUCTION", "BETA", "DEVELOPMENT", "EXPERIMENTAL", "PULL_REQUEST"], var.stage)
    error_message = "stage must be one of PRODUCTION, BETA, DEVELOPMENT, EXPERIMENTAL, PULL_REQUEST."
  }
}

variable "environment_variables" {
  description = "Env vars available to Next.js at build + runtime. Set AV_BRIDGE_UPSTREAM here to point rewrites at the cloud API."
  type        = map(string)
  default     = {}
}

variable "custom_rules" {
  description = "Amplify custom rewrite/redirect rules. Leave empty unless you need SPA fallback etc. — Next.js SSR (WEB_COMPUTE) handles its own routing."
  type = list(object({
    source = string
    target = string
    status = optional(string, "200")
  }))
  default = []
}

variable "custom_domain" {
  description = "Custom apex to serve the app at, e.g. 'uat.involvecloud.com'. Amplify prepends 'app' via sub_domain_settings to become app.<custom_domain>. Leave empty to serve on the Amplify default domain only."
  type        = string
  default     = ""
}

variable "custom_domain_prefix" {
  description = "Subdomain prefix under custom_domain. Default 'app' means the portal lives at app.<custom_domain>."
  type        = string
  default     = "app"
}
