variable "name" {
  description = "Repo name, e.g. 'avrmm-cloud'. ECR names are account-wide, not per-env — see caller."
  type        = string
}

variable "keep_last_n" {
  description = "Lifecycle policy: keep this many untagged + tagged images, expire the rest. 0 = no policy."
  type        = number
  default     = 10
}

variable "scan_on_push" {
  type    = bool
  default = true
}
