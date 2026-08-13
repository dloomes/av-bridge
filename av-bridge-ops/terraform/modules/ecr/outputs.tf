output "repository_url" {
  description = "e.g. 123456789012.dkr.ecr.eu-west-2.amazonaws.com/avrmm-cloud"
  value       = aws_ecr_repository.this.repository_url
}

output "repository_arn" {
  value = aws_ecr_repository.this.arn
}

output "repository_name" {
  value = aws_ecr_repository.this.name
}
