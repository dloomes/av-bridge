variable "name_prefix" {
  description = "Prefix for secret names, e.g. 'avrmm-uat'. Full path becomes '<prefix>/cloud/<key>'."
  type        = string
}

variable "recovery_window_days" {
  description = "Deletion recovery window. 0 = immediate delete (dev/UAT). 7-30 = safer for prod."
  type        = number
  default     = 0
}
