# Internet-facing ALB. Default HTTP listener responds 404 for unmatched
# paths — services attach their own target groups + listener rules to route
# their traffic. HTTPS listener + ACM cert added later in modules/dns.

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.70"
    }
  }
}

resource "aws_lb" "this" {
  name               = "${var.name_prefix}-alb"
  internal           = var.internal
  load_balancer_type = "application"
  subnets            = var.subnet_ids
  security_groups    = [var.security_group_id]

  enable_deletion_protection = var.enable_deletion_protection
  idle_timeout               = var.idle_timeout_seconds
  drop_invalid_header_fields = true

  tags = { Name = "${var.name_prefix}-alb" }
}

locals {
  # Static bool — safe to gate resource counts on.
  tls_enabled = var.enable_tls
}

# :80 listener - split into two resources so terraform doesn't try to
# combine both default_action shapes into one listener (ALB only accepts
# one default_action).
#
# TLS off: :80 serves traffic directly (default 404, services attach rules).
# TLS on:  :80 permanently redirects to :443 (no rules on :80).

resource "aws_lb_listener" "http" {
  count = local.tls_enabled ? 0 : 1

  load_balancer_arn = aws_lb.this.arn
  port              = 80
  protocol          = "HTTP"

  default_action {
    type = "fixed-response"
    fixed_response {
      content_type = "text/plain"
      message_body = "not found"
      status_code  = "404"
    }
  }
}

resource "aws_lb_listener" "http_redirect" {
  count = local.tls_enabled ? 1 : 0

  load_balancer_arn = aws_lb.this.arn
  port              = 80
  protocol          = "HTTP"

  default_action {
    type = "redirect"
    redirect {
      port        = "443"
      protocol    = "HTTPS"
      status_code = "HTTP_301"
    }
  }
}

# :443 listener only when a cert is attached. Default action is 404 so
# services must attach explicit rules — same pattern as the :80 listener
# before TLS.
resource "aws_lb_listener" "https" {
  count = local.tls_enabled ? 1 : 0

  load_balancer_arn = aws_lb.this.arn
  port              = 443
  protocol          = "HTTPS"
  ssl_policy        = var.tls_policy
  certificate_arn   = var.certificate_arn

  default_action {
    type = "fixed-response"
    fixed_response {
      content_type = "text/plain"
      message_body = "not found"
      status_code  = "404"
    }
  }
}
