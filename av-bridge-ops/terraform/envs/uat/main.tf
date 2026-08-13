# UAT env stack. Composes modules with UAT-sized inputs.
# Prod uses the same modules with prod-sized inputs — the ONLY differences
# between envs should live in variables.tf / terraform.tfvars.

module "network" {
  source = "../../modules/network"

  name_prefix        = var.name_prefix
  vpc_cidr           = var.vpc_cidr
  azs                = var.azs
  single_nat_gateway = var.single_nat_gateway
}

module "secrets" {
  source = "../../modules/secrets"

  name_prefix = var.name_prefix
  # UAT: immediate secret deletion so `terraform destroy && apply` doesn't
  # trip on "secret pending deletion". Prod should use 7-30.
  recovery_window_days = 0
}

module "db" {
  source = "../../modules/db"

  name_prefix       = var.name_prefix
  vpc_id            = module.network.vpc_id
  subnet_ids        = module.network.private_subnet_ids
  security_group_id = aws_security_group.db.id

  instance_class      = "db.t4g.micro"
  multi_az            = false
  deletion_protection = false
  # UAT: 1 day of backups is plenty. Prod: 7-14.
  backup_retention_days = 1
}

module "ecr_cloud" {
  source = "../../modules/ecr"

  # ECR repo names are per-account, so include the env suffix.
  name        = "${var.name_prefix}-cloud"
  keep_last_n = 10
}

module "alb" {
  source = "../../modules/alb"

  name_prefix       = var.name_prefix
  vpc_id            = module.network.vpc_id
  subnet_ids        = module.network.public_subnet_ids
  security_group_id = aws_security_group.alb.id
}

module "cloud_service" {
  source = "../../modules/cloud-service"

  name_prefix       = var.name_prefix
  vpc_id            = module.network.vpc_id
  subnet_ids        = module.network.private_subnet_ids
  security_group_id = aws_security_group.app.id

  alb_listener_arn    = module.alb.http_listener_arn
  listener_host_names = [] # add once we attach a real domain

  image_url = module.ecr_cloud.repository_url
  image_tag = "latest"

  cpu           = 512
  memory        = 1024
  desired_count = 1

  db_master_secret_arn = module.db.master_secret_arn
  db_host              = module.db.address
  db_port              = module.db.port
  db_name              = module.db.db_name

  cloud_secret_key_arn = module.secrets.cloud_secret_key_arn
  admin_api_token_arn  = module.secrets.admin_api_token_arn
  portal_token_arn     = module.secrets.portal_token_arn
  hmac_secret_arn      = module.secrets.hmac_secret_arn

  # UAT: seed the PoC tenant so we can test end-to-end immediately.
  bootstrap_poc      = true
  vendor_admin_email = "dloomes@involve.vc"
  vendor_admin_name  = "Daniel Loomes"
}
