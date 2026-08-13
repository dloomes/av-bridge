output "app_id" {
  value = aws_amplify_app.this.id
}

output "app_arn" {
  value = aws_amplify_app.this.arn
}

output "default_domain" {
  description = "Amplify-issued domain. Format: <branch>.<app_id>.amplifyapp.com"
  value       = "${var.branch_name}.${aws_amplify_app.this.default_domain}"
}

output "branch_name" {
  value = aws_amplify_branch.this.branch_name
}
