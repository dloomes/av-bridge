variable "name_prefix" {
  description = "Prefix for the topic and alarm names."
  type        = string
}

variable "alert_email" {
  description = "Address subscribed to the alerts topic. Must confirm the subscription email."
  type        = string
}

variable "alb_arn_suffix" {
  description = "ALB arn_suffix (CloudWatch LoadBalancer dimension)."
  type        = string
}

variable "web_target_group_arn_suffix" {
  type = string
}

variable "worker_target_group_arn_suffix" {
  type = string
}

variable "cluster_name" {
  description = "ECS cluster name (Container Insights ClusterName dimension)."
  type        = string
}

variable "web_service_name" {
  type = string
}

variable "worker_service_name" {
  type = string
}

variable "db_instance_identifier" {
  type = string
}
