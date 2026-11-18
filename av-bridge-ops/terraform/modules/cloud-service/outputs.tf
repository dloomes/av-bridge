output "cluster_name" {
  value = element(split("/", local.cluster_arn), length(split("/", local.cluster_arn)) - 1)
}

output "cluster_arn" {
  value = local.cluster_arn
}

output "service_name" {
  value = aws_ecs_service.this.name
}

output "target_group_arn" {
  value = aws_lb_target_group.this.arn
}

output "log_group_name" {
  value = aws_cloudwatch_log_group.this.name
}

output "task_role_arn" {
  value = aws_iam_role.task.arn
}

output "target_group_arn_suffix" {
  value = aws_lb_target_group.this.arn_suffix
}
