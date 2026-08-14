output "arn" {
  value = aws_lb.this.arn
}

output "dns_name" {
  description = "Public DNS name of the ALB — use it (or a Route 53 alias to it) to reach services."
  value       = aws_lb.this.dns_name
}

output "zone_id" {
  description = "Hosted zone ID of the ALB — pair with dns_name for Route 53 alias records."
  value       = aws_lb.this.zone_id
}

output "http_listener_arn" {
  description = "ARN of the :80 listener. When TLS is on, this is the redirect-to-443 listener; when off, the fixed-response listener."
  value       = local.tls_enabled ? aws_lb_listener.http_redirect[0].arn : aws_lb_listener.http[0].arn
}

output "https_listener_arn" {
  description = "ARN of the :443 listener when a cert is attached, else empty. Services attach their rules here once TLS is on."
  value       = local.tls_enabled ? aws_lb_listener.https[0].arn : ""
}

output "primary_listener_arn" {
  description = "The listener that services should attach rules to — :443 when TLS is on, :80 otherwise."
  value       = local.tls_enabled ? aws_lb_listener.https[0].arn : aws_lb_listener.http[0].arn
}
