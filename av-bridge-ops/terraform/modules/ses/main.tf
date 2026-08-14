# SES sending setup for one domain. Creates the SES identity + DKIM
# signing keys, adds the DNS records that make SES trust us + receivers
# trust the mail (DKIM + SPF + DMARC), mints an IAM user whose access
# key doubles as SMTP credentials, and stores the credentials in Secrets
# Manager so the ECS task can pull them at boot.
#
# Sandbox note: fresh SES accounts sit in the sandbox — max 200 sends/day
# and the destination address must be a verified identity. Optional
# `verified_recipient_addresses` list adds those identities so dev + QA
# addresses receive alerts before production access is granted. Request
# production access via the SES console once you're ready to send to
# arbitrary customer addresses.

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.70"
    }
  }
}

data "aws_region" "current" {}

locals {
  # Resolve region: caller override → current provider region. The SMTP
  # endpoint depends on this, and IAM/SES resources have to live here.
  region = var.region != "" ? var.region : data.aws_region.current.name

  # SES SMTP endpoint hostname is region-specific.
  smtp_host = "email-smtp.${local.region}.amazonaws.com"

  from_address    = "${var.from_local_part}@${var.sending_domain}"
  from_with_name  = var.from_display_name == "" ? local.from_address : "${var.from_display_name} <${local.from_address}>"

  # DMARC record body — string concatenation with a conditional rua= tag
  # so we don't emit "rua=" with nothing on the right when caller opts
  # out of aggregate reports.
  dmarc_record = trimspace(join(" ",
    compact([
      "v=DMARC1;",
      "p=${var.dmarc_policy};",
      "adkim=r;",
      "aspf=r;",
      var.dmarc_rua == "" ? "" : "rua=mailto:${var.dmarc_rua};",
    ])
  ))
}

# -----------------------------------------------------------------------------
# SES domain identity + DKIM
# -----------------------------------------------------------------------------
#
# `aws_ses_domain_identity` registers the domain and hands back a
# verification token; the accompanying TXT record proves ownership.
# `aws_ses_domain_dkim` returns 3 DKIM tokens; the 3 CNAME records let
# SES sign outbound mail and let receivers cryptographically verify us.

resource "aws_ses_domain_identity" "this" {
  domain = var.sending_domain
}

resource "aws_route53_record" "domain_verification" {
  zone_id = var.hosted_zone_id
  name    = "_amazonses.${var.sending_domain}"
  type    = "TXT"
  ttl     = 300
  records = [aws_ses_domain_identity.this.verification_token]
}

# SES's "identity verified" flip is driven by finding the TXT record; the
# resource below blocks terraform until that flip lands so downstream
# resources (the identity policy, verified recipients) don't race a
# still-pending domain.
resource "aws_ses_domain_identity_verification" "this" {
  domain     = aws_ses_domain_identity.this.domain
  depends_on = [aws_route53_record.domain_verification]
}

resource "aws_ses_domain_dkim" "this" {
  domain = aws_ses_domain_identity.this.domain
}

resource "aws_route53_record" "dkim" {
  count   = 3
  zone_id = var.hosted_zone_id
  name    = "${aws_ses_domain_dkim.this.dkim_tokens[count.index]}._domainkey.${var.sending_domain}"
  type    = "CNAME"
  ttl     = 300
  records = ["${aws_ses_domain_dkim.this.dkim_tokens[count.index]}.dkim.amazonses.com"]
}

# -----------------------------------------------------------------------------
# SPF + DMARC
# -----------------------------------------------------------------------------
#
# SPF says "amazonses.com is allowed to send as this domain" — many
# gateways drop mail without it. DMARC starts at p=none (monitoring)
# so misconfiguration doesn't silently swallow real mail; tighten via
# the dmarc_policy variable once alignment is verified.

resource "aws_route53_record" "spf" {
  zone_id = var.hosted_zone_id
  name    = var.sending_domain
  type    = "TXT"
  ttl     = 300
  records = ["v=spf1 include:amazonses.com -all"]
}

resource "aws_route53_record" "dmarc" {
  zone_id = var.hosted_zone_id
  name    = "_dmarc.${var.sending_domain}"
  type    = "TXT"
  ttl     = 300
  records = [local.dmarc_record]
}

# -----------------------------------------------------------------------------
# Sandbox: verify additional recipient addresses
# -----------------------------------------------------------------------------
#
# In SES sandbox mode, sends to unverified recipients are rejected. Each
# entry here registers an email identity SES will send to during test.
# The verification email lands in the address's inbox on apply — the
# recipient clicks a link and the identity flips to "verified".

resource "aws_ses_email_identity" "recipients" {
  for_each = toset(var.verified_recipient_addresses)
  email    = each.value
}

# -----------------------------------------------------------------------------
# IAM user for SMTP + SendRawEmail policy
# -----------------------------------------------------------------------------
#
# SES SMTP auth uses a per-user access key whose secret is transformed
# into an SMTP password via SigV4 (aws_iam_access_key exposes it as
# `ses_smtp_password_v4`). The IAM user itself has no console login and
# only one policy: SendRawEmail scoped to our own domain identity.

resource "aws_iam_user" "smtp" {
  name = "${var.name_prefix}-ses-smtp"
  path = "/service/"
}

data "aws_iam_policy_document" "smtp_send" {
  statement {
    sid       = "AllowSendRawEmail"
    actions   = ["ses:SendRawEmail", "ses:SendEmail"]
    resources = [aws_ses_domain_identity.this.arn]

    # Belt-and-braces: only allow sending as the identity we've verified
    # (matches the resource ARN above, but this covers the ses:FromAddress
    # condition receivers can key on).
    condition {
      test     = "StringLike"
      variable = "ses:FromAddress"
      values   = ["*@${var.sending_domain}"]
    }
  }
}

resource "aws_iam_user_policy" "smtp" {
  name   = "${var.name_prefix}-ses-smtp-send"
  user   = aws_iam_user.smtp.name
  policy = data.aws_iam_policy_document.smtp_send.json
}

resource "aws_iam_access_key" "smtp" {
  user = aws_iam_user.smtp.name
}

# -----------------------------------------------------------------------------
# Secrets Manager entry for the SMTP credentials
# -----------------------------------------------------------------------------
#
# The username is the IAM access key ID; the password is the SigV4-derived
# SMTP password. Stored together as JSON so the ECS task def can reference
# both via `:username::` and `:password::` in its `secrets` block.

resource "aws_secretsmanager_secret" "smtp" {
  name                    = "${var.name_prefix}/smtp/credentials"
  description             = "SES SMTP username + password for ${var.sending_domain}"
  recovery_window_in_days = var.secret_recovery_window_days
}

resource "aws_secretsmanager_secret_version" "smtp" {
  secret_id = aws_secretsmanager_secret.smtp.id
  secret_string = jsonencode({
    username = aws_iam_access_key.smtp.id
    password = aws_iam_access_key.smtp.ses_smtp_password_v4
  })
}
