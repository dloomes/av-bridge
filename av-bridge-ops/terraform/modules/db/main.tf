# RDS Postgres 16 for the cloud API.
#
# Ownership model (see av-bridge-cloud/internal/db/migrations/0002_rls.sql):
#   - Master user (this module's random password) owns the schema and runs
#     migrations. Passed to the cloud binary as DATABASE_MIGRATION_URL.
#   - The migration creates app_admin (BYPASSRLS) and app_tenant (RLS-forced)
#     with well-known passwords. Those are the runtime roles for
#     DATABASE_ADMIN_URL / DATABASE_TENANT_URL.
#
# The app-role passwords are baked into the migration and are only acceptable
# because RDS is in private subnets and only the app SG can reach 5432 —
# they never traverse the internet. Rotate via a follow-up migration when we
# harden for prod.

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.70"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
  }
}

# -----------------------------------------------------------------------------
# Master credentials
# -----------------------------------------------------------------------------

resource "random_password" "master" {
  length = 32
  # RDS forbids @ / " and space; on top of that the password lands inside a
  # postgres:// userinfo string, so we also need to avoid every URL-reserved
  # character (: / ? # [ ] @ ! $ & ' ( ) * + , ; = %) — otherwise the URL
  # parser trips before the app can even reach the DB. The URL-unreserved
  # set below is safe without percent-encoding anywhere.
  override_special = "-._~"
}

resource "aws_secretsmanager_secret" "master" {
  name                    = "${var.name_prefix}/db/master"
  description             = "RDS Postgres master credentials + connection info"
  recovery_window_in_days = var.secret_recovery_window_days
}

resource "aws_secretsmanager_secret_version" "master" {
  secret_id = aws_secretsmanager_secret.master.id
  secret_string = jsonencode({
    username = var.master_username
    password = random_password.master.result
    host     = aws_db_instance.this.address
    port     = aws_db_instance.this.port
    dbname   = var.db_name
    # Full URL for convenience — the app can parse either shape.
    url = "postgres://${var.master_username}:${random_password.master.result}@${aws_db_instance.this.address}:${aws_db_instance.this.port}/${var.db_name}?sslmode=require"
  })
}

# -----------------------------------------------------------------------------
# Subnet group + parameter group
# -----------------------------------------------------------------------------

resource "aws_db_subnet_group" "this" {
  name       = "${var.name_prefix}-db"
  subnet_ids = var.subnet_ids

  tags = { Name = "${var.name_prefix}-db-subnets" }
}

resource "aws_db_parameter_group" "this" {
  name   = "${var.name_prefix}-db-pg16"
  family = "postgres16"

  # Force SSL/TLS on all connections. Baseline hardening — the ECS task
  # already sets sslmode=require but this rejects any client that forgets.
  parameter {
    name  = "rds.force_ssl"
    value = "1"
  }
}

# -----------------------------------------------------------------------------
# Instance
# -----------------------------------------------------------------------------

resource "aws_db_instance" "this" {
  identifier = "${var.name_prefix}-postgres"

  engine         = "postgres"
  engine_version = var.engine_version
  instance_class = var.instance_class

  allocated_storage     = var.allocated_storage_gb
  max_allocated_storage = var.max_allocated_storage_gb == 0 ? null : var.max_allocated_storage_gb
  storage_type          = "gp3"
  storage_encrypted     = true

  db_name  = var.db_name
  username = var.master_username
  password = random_password.master.result

  db_subnet_group_name   = aws_db_subnet_group.this.name
  vpc_security_group_ids = [var.security_group_id]
  parameter_group_name   = aws_db_parameter_group.this.name

  publicly_accessible = false
  multi_az            = var.multi_az

  backup_retention_period = var.backup_retention_days
  backup_window           = "02:00-03:00"
  maintenance_window      = "sun:03:30-sun:04:30"

  auto_minor_version_upgrade = true
  deletion_protection        = var.deletion_protection
  skip_final_snapshot        = !var.deletion_protection
  final_snapshot_identifier  = var.deletion_protection ? "${var.name_prefix}-postgres-final-${formatdate("YYYYMMDDhhmmss", timestamp())}" : null

  # We don't want terraform to churn every plan just because the auto-generated
  # final snapshot identifier changes.
  lifecycle {
    ignore_changes = [final_snapshot_identifier]
  }

  tags = { Name = "${var.name_prefix}-postgres" }
}
