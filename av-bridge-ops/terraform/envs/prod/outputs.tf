# --- DNS delegation (stage 1) ------------------------------------------------
# Add each set as NS records for its name at DNS Made Easy.

output "dns_app_name_servers" {
  description = "NS records for app.involvecloud.com - add at DNS Made Easy."
  value       = module.dns_app.name_servers
}

output "dns_api_name_servers" {
  description = "NS records for api.involvecloud.com - add at DNS Made Easy."
  value       = module.dns_api.name_servers
}

output "dns_access_name_servers" {
  description = "NS records for access.involvecloud.com - add at DNS Made Easy."
  value       = module.dns_access.name_servers
}

# --- Email ---------------------------------------------------------------------

output "ses_dns_records" {
  description = "Records notify.involvecloud.com needs at DNS Made Easy (identity, DKIM, SPF, DMARC, MAIL FROM)."
  value       = module.ses.dns_records
}

output "ses_sandbox_reminder" {
  value = module.ses.sandbox_reminder
}

# --- Network + data ------------------------------------------------------------

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

# --- Load balancer + services --------------------------------------------------

output "alb_dns_name" {
  value = module.alb.dns_name
}

output "cloud_cluster" {
  value = module.cloud_web.cluster_name
}

output "cloud_web_service" {
  value = module.cloud_web.service_name
}

output "cloud_worker_service" {
  value = module.cloud_worker.service_name
}

output "cloud_web_log_group" {
  value = module.cloud_web.log_group_name
}

output "cloud_worker_log_group" {
  value = module.cloud_worker.log_group_name
}

# --- Portal --------------------------------------------------------------------

output "portal_app_id" {
  value = module.portal.app_id
}

output "portal_default_domain" {
  value = module.portal.default_domain
}

output "portal_custom_domain" {
  value = module.portal.custom_domain_fqdn
}

output "api_url" {
  value = local.api_url
}

output "app_url" {
  value = local.app_url
}

# --- Secrets (ARNs only, never values) ---------------------------------------

output "vendor_admin_password_secret_arn" {
  description = "Secrets Manager ARN of the first-boot vendor admin password. Read the value in the console (or aws secretsmanager get-secret-value) for the first sign-in, then change it in the portal."
  value       = aws_secretsmanager_secret.vendor_admin_password.arn
}

output "entra_vendor_client_secret_arn" {
  description = "Hand-made Entra vendor client secret the services read."
  value       = data.aws_secretsmanager_secret.entra_vendor_client_secret.arn
}

output "db_app_credentials_secret_arn" {
  description = "Secrets Manager ARN of the app database role URLs and passwords (ARN only)."
  value       = aws_secretsmanager_secret.db_app_credentials.arn
}
