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

resource "aws_lb_listener" "http" {
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
