output "app_id" {
  value = aws_amplify_app.this.id
}

output "app_arn" {
  value = aws_amplify_app.this.arn
}

output "default_domain" {
  description = "Amplify-issued domain. Format: <branch>.<app_id>.amplifyapp.com"
  value       = "${var.branch_name}.${aws_amplify_app.this.default_domain}"
}

output "branch_name" {
  value = aws_amplify_branch.this.branch_name
}

# When a custom domain is set, Amplify's domain association resolves the
# actual FQDN via <prefix>.<domain>. Empty otherwise.
output "custom_domain_fqdn" {
  value = var.custom_domain == "" ? "" : "${var.custom_domain_prefix}.${var.custom_domain}"
}

output "custom_domain_certificate_verification_dns_record" {
  description = "DNS record Amplify wants added to Route 53 to verify overall domain ownership. Format: 'name TYPE value'."
  value       = var.custom_domain == "" ? "" : aws_amplify_domain_association.this[0].certificate_verification_dns_record
}

output "custom_domain_sub_domain_dns_records" {
  description = "Traffic-serving CNAME(s) — one per sub_domain block. Format: 'name TYPE value' each. May be empty on first apply while Amplify is still provisioning CloudFront; re-apply once populated to create the Route 53 records."
  value       = var.custom_domain == "" ? [] : [for sd in aws_amplify_domain_association.this[0].sub_domain : sd.dns_record]
}

