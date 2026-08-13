output "state_bucket" {
  description = "S3 bucket for terraform state. Reference from envs/*/backend.tf."
  value       = aws_s3_bucket.state.id
}

output "lock_table" {
  description = "DynamoDB table for state locking."
  value       = aws_dynamodb_table.lock.name
}

output "backend_config_hcl" {
  description = "Paste this into envs/<env>/backend.tf, replacing the placeholder key with a per-stack path."
  value       = <<-EOT
    terraform {
      backend "s3" {
        bucket         = "${aws_s3_bucket.state.id}"
        key            = "envs/${var.environment}/<stack>.tfstate"
        region         = "${var.region}"
        dynamodb_table = "${aws_dynamodb_table.lock.name}"
        encrypt        = true
        profile        = "${var.aws_profile}"
      }
    }
  EOT
}
