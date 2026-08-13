output "vpc_id" {
  value = module.network.vpc_id
}

output "public_subnet_ids" {
  value = module.network.public_subnet_ids
}

output "private_subnet_ids" {
  value = module.network.private_subnet_ids
}

output "db_endpoint" {
  value = module.db.endpoint
}

output "db_master_secret_arn" {
  value = module.db.master_secret_arn
}

output "app_secret_arns" {
  value = module.secrets.all_arns
}

output "ecr_cloud_repository_url" {
  value = module.ecr_cloud.repository_url
}

output "alb_dns_name" {
  description = "Public DNS for the ALB — smoke-test the API via http://<this>/healthz once the service is healthy."
  value       = module.alb.dns_name
}

output "cloud_log_group" {
  value = module.cloud_service.log_group_name
}

output "cloud_cluster" {
  value = module.cloud_service.cluster_name
}

output "cloud_service" {
  value = module.cloud_service.service_name
}

output "portal_app_id" {
  value = module.portal.app_id
}

output "portal_default_domain" {
  description = "Amplify-issued portal URL. Custom domain lands later via Route 53 + ACM."
  value       = module.portal.default_domain
}
