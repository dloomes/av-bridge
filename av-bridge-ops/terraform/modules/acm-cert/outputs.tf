output "certificate_arn" {
  description = "ARN of the issued cert. Wait on aws_acm_certificate_validation so downstream (ALB listener) doesn't attach an unvalidated cert."
  value       = aws_acm_certificate_validation.this.certificate_arn
}
