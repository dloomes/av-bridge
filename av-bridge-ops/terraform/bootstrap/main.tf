# Bootstrap: creates the S3 bucket + DynamoDB lock table that hold remote
# state for all env stacks in this AWS account. Run once per account
# (avrmm-uat, avrmm-prod).
#
# State for THIS module stays local — it only holds the two resources it
# creates, and can be reconstructed with `terraform import` if lost.

terraform {
  required_version = ">= 1.9"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.70"
    }
  }
}

provider "aws" {
  region  = var.region
  profile = var.aws_profile

  default_tags {
    tags = {
      Project     = "avrmm"
      ManagedBy   = "terraform"
      Component   = "tf-state-backend"
      Environment = var.environment
    }
  }
}

# -----------------------------------------------------------------------------
# State bucket
# -----------------------------------------------------------------------------

resource "aws_s3_bucket" "state" {
  bucket        = "${var.name_prefix}-tf-state-${var.environment}"
  force_destroy = false
}

resource "aws_s3_bucket_versioning" "state" {
  bucket = aws_s3_bucket.state.id

  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "state" {
  bucket = aws_s3_bucket.state.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_public_access_block" "state" {
  bucket = aws_s3_bucket.state.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_lifecycle_configuration" "state" {
  bucket = aws_s3_bucket.state.id

  rule {
    id     = "expire-noncurrent-versions"
    status = "Enabled"

    filter {}

    noncurrent_version_expiration {
      noncurrent_days = 90
    }
  }
}

# -----------------------------------------------------------------------------
# Lock table
# -----------------------------------------------------------------------------

resource "aws_dynamodb_table" "lock" {
  name         = "${var.name_prefix}-tf-lock-${var.environment}"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "LockID"

  attribute {
    name = "LockID"
    type = "S"
  }

  point_in_time_recovery {
    enabled = true
  }
}
