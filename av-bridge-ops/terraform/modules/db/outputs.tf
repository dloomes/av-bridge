output "endpoint" {
  description = "host:port for the DB"
  value       = aws_db_instance.this.endpoint
}

output "address" {
  value = aws_db_instance.this.address
}

output "port" {
  value = aws_db_instance.this.port
}

output "db_name" {
  value = var.db_name
}

output "master_secret_arn" {
  description = "Secrets Manager ARN for master credentials + connection JSON"
  value       = aws_secretsmanager_secret.master.arn
}
