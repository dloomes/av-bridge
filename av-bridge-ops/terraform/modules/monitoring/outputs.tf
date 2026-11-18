output "topic_arn" {
  value = aws_sns_topic.alerts.arn
}

output "alarm_names" {
  value = [for a in aws_cloudwatch_metric_alarm.this : a.alarm_name]
}
