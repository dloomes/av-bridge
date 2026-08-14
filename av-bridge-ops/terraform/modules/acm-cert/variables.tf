variable "primary_domain" {
  description = "Primary FQDN on the cert, e.g. 'uat.involvecloud.com'."
  type        = string
}

variable "subject_alternative_names" {
  description = "Extra SANs. Wildcard '*.uat.involvecloud.com' recommended so app + api + any future service resolve without re-issuing."
  type        = list(string)
  default     = []
}

variable "hosted_zone_id" {
  description = "Route 53 zone that terraform can write validation records into. Must own the primary_domain."
  type        = string
}
