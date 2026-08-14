variable "name_prefix" {
  description = "Env prefix used for IAM + Secrets Manager resource names, e.g. \"avrmm-uat\"."
  type        = string
}

variable "sending_domain" {
  description = "Domain SES will verify + sign for, e.g. \"uat.involvecloud.com\". Should match the Route 53 zone the caller passes in."
  type        = string
}

variable "hosted_zone_id" {
  description = "Route 53 hosted zone ID for sending_domain. SES ownership + DKIM + SPF + DMARC records are added here."
  type        = string
}

variable "from_local_part" {
  description = "Mailbox portion of the outbound From address, e.g. \"noreply\" gives noreply@<sending_domain>."
  type        = string
  default     = "noreply"
}

variable "from_display_name" {
  description = "Human-friendly name in the From header. Passed through to the app as part of POC_SMTP_FROM as \"Name <addr>\"."
  type        = string
  default     = "AV Bridge"
}

variable "dmarc_policy" {
  description = "DMARC p= value. Start at \"none\" for monitoring only; tighten to \"quarantine\" or \"reject\" once you're confident sends align."
  type        = string
  default     = "none"

  validation {
    condition     = contains(["none", "quarantine", "reject"], var.dmarc_policy)
    error_message = "dmarc_policy must be one of none, quarantine, reject."
  }
}

variable "dmarc_rua" {
  description = "DMARC aggregate report address (rua=mailto:...). Leave empty to omit the tag."
  type        = string
  default     = ""
}

variable "verified_recipient_addresses" {
  description = "Additional email addresses to verify as SES identities. SES accounts stay in sandbox by default (200 sends/day + only-to-verified). Any dev/test addresses that need to receive alerts must be listed here until you request production access."
  type        = list(string)
  default     = []
}

variable "region" {
  description = "AWS region SES runs in. Determines the SMTP endpoint hostname. Defaults to the caller's provider region."
  type        = string
  default     = ""
}

variable "secret_recovery_window_days" {
  description = "Days AWS keeps the SMTP-credentials secret recoverable after a destroy. 0 = immediate delete (UAT convenience); prod should use 7-30."
  type        = number
  default     = 0
}
