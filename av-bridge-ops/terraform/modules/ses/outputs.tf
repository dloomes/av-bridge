output "smtp_host" {
  description = "SES SMTP endpoint hostname, region-specific. Feed into POC_SMTP_HOST on the cloud task."
  value       = local.smtp_host
}

output "smtp_port" {
  description = "SES SMTP submission port. 587 uses STARTTLS which every mail client supports; use 465 for implicit TLS if needed."
  value       = "587"
}

output "smtp_from" {
  description = "Full From header value \"Display Name <local@domain>\". Feed into POC_SMTP_FROM."
  value       = local.from_with_name
}

output "smtp_from_address" {
  description = "Bare From address without display name — useful for the SES sandbox check (ses:FromAddress condition matches this)."
  value       = local.from_address
}

output "smtp_credentials_secret_arn" {
  description = "Secrets Manager ARN holding {username, password}. Grant the ECS execution role secretsmanager:GetSecretValue on this ARN and reference the keys via :username:: / :password:: in the container secrets block."
  value       = aws_secretsmanager_secret.smtp.arn
}

output "domain_identity_arn" {
  description = "ARN of the SES domain identity. Kept so downstream modules can attach identity policies without re-resolving the ARN."
  value       = aws_ses_domain_identity.this.arn
}

output "sandbox_reminder" {
  description = "Human-readable reminder about the SES sandbox. Print this after apply so operators remember to request production access before real traffic goes live."
  value = join("\n", [
    "SES sandbox is enabled by default:",
    "  * max 200 emails / 24h rolling window (all destinations, across the whole SES account)",
    "  * recipients must be verified email identities",
    "To send to arbitrary customer addresses, request production access:",
    "  AWS Console -> SES -> Account dashboard -> Request production access",
    "Approval typically lands within 24h."
  ])
}

output "dns_records" {
  description = "DNS records the domain needs. Create them at the DNS provider when manage_dns is false; informational otherwise."
  value = concat(
    [{
      name  = "_amazonses.${var.sending_domain}"
      type  = "TXT"
      value = aws_ses_domain_identity.this.verification_token
    }],
    [for t in aws_ses_domain_dkim.this.dkim_tokens : {
      name  = "${t}._domainkey.${var.sending_domain}"
      type  = "CNAME"
      value = "${t}.dkim.amazonses.com"
    }],
    [
      { name = var.sending_domain, type = "TXT", value = "v=spf1 include:amazonses.com -all" },
      { name = "_dmarc.${var.sending_domain}", type = "TXT", value = local.dmarc_record },
    ],
    var.mail_from_subdomain == "" ? [] : [
      { name = local.mail_from_domain, type = "MX", value = "10 feedback-smtp.${local.region}.amazonses.com" },
      { name = local.mail_from_domain, type = "TXT", value = "v=spf1 include:amazonses.com -all" },
    ],
  )
}
