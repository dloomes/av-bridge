output "zone_id" {
  value = aws_route53_zone.this.zone_id
}

output "zone_name" {
  value = aws_route53_zone.this.name
}

output "name_servers" {
  description = "Add these as NS records for '<zone_name>' at your parent domain's registrar to complete delegation."
  value       = aws_route53_zone.this.name_servers
}
