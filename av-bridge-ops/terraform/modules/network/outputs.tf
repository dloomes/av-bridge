output "vpc_id" {
  value = aws_vpc.this.id
}

output "vpc_cidr" {
  value = aws_vpc.this.cidr_block
}

output "public_subnet_ids" {
  value = aws_subnet.public[*].id
}

output "private_subnet_ids" {
  value = aws_subnet.private[*].id
}

output "nat_gateway_public_ips" {
  description = "Egress IP(s) from private subnets. Add to bridge collector allowlists if you IP-restrict inbound to the collector."
  value       = aws_eip.nat[*].public_ip
}
