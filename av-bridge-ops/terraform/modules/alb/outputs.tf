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
  value = aws_lb_listener.http.arn
}
