output "enabled" {
  description = "True when client_secret was provided and the ARN is populated."
  value       = local.enabled
}

output "tenant_id" {
  description = "Passthrough — the Entra tenant GUID."
  value       = var.tenant_id
}

output "client_id" {
  description = "Passthrough — the Entra app registration Application ID."
  value       = var.client_id
}

output "redirect_uri" {
  description = "Passthrough — the callback URL that must exactly match the Entra app registration."
  value       = var.redirect_uri
}

output "client_secret_arn" {
  description = "Secrets Manager ARN of the client secret entry. Empty string when SSO is disabled."
  value       = local.enabled ? aws_secretsmanager_secret.client_secret[0].arn : ""
}
