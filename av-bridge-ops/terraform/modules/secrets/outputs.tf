output "cloud_secret_key_arn" {
  value = aws_secretsmanager_secret.cloud_secret_key.arn
}

output "admin_api_token_arn" {
  value = aws_secretsmanager_secret.admin_api_token.arn
}

output "portal_token_arn" {
  value = aws_secretsmanager_secret.portal_token.arn
}

output "hmac_secret_arn" {
  value = aws_secretsmanager_secret.hmac_secret.arn
}

output "all_arns" {
  description = "All app-layer secret ARNs. Grant task role secretsmanager:GetSecretValue for these."
  value = [
    aws_secretsmanager_secret.cloud_secret_key.arn,
    aws_secretsmanager_secret.admin_api_token.arn,
    aws_secretsmanager_secret.portal_token.arn,
    aws_secretsmanager_secret.hmac_secret.arn,
  ]
}
