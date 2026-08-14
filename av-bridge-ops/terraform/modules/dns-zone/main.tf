# Route 53 public hosted zone.
#
# We create a zone for a SUBDOMAIN of an externally-registered domain (e.g.
# uat.involvecloud.com) so this account can own DNS under just that subtree
# without moving the whole domain. The registrar keeps involvecloud.com; you
# add NS records at the registrar pointing 'uat' at these nameservers.
#
# The module doesn't manage the delegation itself — that step is at your
# registrar and lives outside terraform.

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.70"
    }
  }
}

resource "aws_route53_zone" "this" {
  name    = var.zone_name
  comment = "Managed by terraform. Delegate at parent registrar via the NS records in this zone."
}
