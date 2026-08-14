# ACM cert with DNS validation.
#
# ACM issues the cert only after seeing a specific CNAME in the domain's DNS.
# We add that CNAME to the caller's Route 53 zone and wait for validation.
# All within one apply — no manual click-through in the console.
#
# Cert lives in the same region as the callers (ALB in eu-west-2). Amplify
# custom domains want a cert in us-east-1 (CloudFront region), but Amplify
# handles that automatically inside its own domain association — we don't
# need to duplicate.

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.70"
    }
  }
}

resource "aws_acm_certificate" "this" {
  domain_name               = var.primary_domain
  subject_alternative_names = var.subject_alternative_names
  validation_method         = "DNS"

  lifecycle {
    create_before_destroy = true
  }
}

# One validation record per unique domain in domain_validation_options.
# for_each over the set so ACM issues + re-issues cleanly if SANs change.
resource "aws_route53_record" "validation" {
  for_each = {
    for dvo in aws_acm_certificate.this.domain_validation_options :
    dvo.domain_name => {
      name   = dvo.resource_record_name
      record = dvo.resource_record_value
      type   = dvo.resource_record_type
    }
  }

  allow_overwrite = true
  name            = each.value.name
  records         = [each.value.record]
  ttl             = 60
  type            = each.value.type
  zone_id         = var.hosted_zone_id
}

resource "aws_acm_certificate_validation" "this" {
  certificate_arn         = aws_acm_certificate.this.arn
  validation_record_fqdns = [for r in aws_route53_record.validation : r.fqdn]
}
