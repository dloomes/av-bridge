# Security groups live in the env stack (not a module) so all cross-SG
# references are visible in one file.
#
# Layering: alb_sg (public 80/443) → app_sg (ECS tasks 8090) → db_sg (RDS 5432)

# ALB — internet-facing
resource "aws_security_group" "alb" {
  name        = "${var.name_prefix}-alb"
  description = "ALB inbound from internet"
  vpc_id      = module.network.vpc_id

  tags = { Name = "${var.name_prefix}-alb" }
}

resource "aws_security_group_rule" "alb_http_in" {
  security_group_id = aws_security_group.alb.id
  type              = "ingress"
  from_port         = 80
  to_port           = 80
  protocol          = "tcp"
  cidr_blocks       = ["0.0.0.0/0"]
  description       = "HTTP from internet"
}

resource "aws_security_group_rule" "alb_https_in" {
  security_group_id = aws_security_group.alb.id
  type              = "ingress"
  from_port         = 443
  to_port           = 443
  protocol          = "tcp"
  cidr_blocks       = ["0.0.0.0/0"]
  description       = "HTTPS from internet (once ACM cert is attached)"
}

resource "aws_security_group_rule" "alb_egress_all" {
  security_group_id = aws_security_group.alb.id
  type              = "egress"
  from_port         = 0
  to_port           = 0
  protocol          = "-1"
  cidr_blocks       = ["0.0.0.0/0"]
  description       = "All egress (needed to reach targets)"
}

# App — ECS Fargate tasks in private subnets
resource "aws_security_group" "app" {
  name        = "${var.name_prefix}-app"
  description = "ECS tasks running the cloud binary"
  vpc_id      = module.network.vpc_id

  tags = { Name = "${var.name_prefix}-app" }
}

resource "aws_security_group_rule" "app_from_alb" {
  security_group_id        = aws_security_group.app.id
  type                     = "ingress"
  from_port                = 8090
  to_port                  = 8090
  protocol                 = "tcp"
  source_security_group_id = aws_security_group.alb.id
  description              = "Cloud binary port from ALB"
}

resource "aws_security_group_rule" "app_egress_all" {
  security_group_id = aws_security_group.app.id
  type              = "egress"
  from_port         = 0
  to_port           = 0
  protocol          = "-1"
  cidr_blocks       = ["0.0.0.0/0"]
  description       = "All egress (ECR pulls, Secrets Manager, RDS via SG, outbound HTTPS)"
}

# DB — RDS Postgres in private subnets
resource "aws_security_group" "db" {
  name        = "${var.name_prefix}-db"
  description = "RDS Postgres"
  vpc_id      = module.network.vpc_id

  tags = { Name = "${var.name_prefix}-db" }
}

resource "aws_security_group_rule" "db_from_app" {
  security_group_id        = aws_security_group.db.id
  type                     = "ingress"
  from_port                = 5432
  to_port                  = 5432
  protocol                 = "tcp"
  source_security_group_id = aws_security_group.app.id
  description              = "Postgres from ECS tasks"
}
