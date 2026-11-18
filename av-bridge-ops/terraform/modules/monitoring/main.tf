# Production alarms -> one SNS topic -> email. Every alarm also notifies on OK.
#
# treat_missing_data:
#   notBreaching  5XX count (no traffic or no errors publishes nothing)
#   breaching     HealthyHostCount / RunningTaskCount (no data = nothing running)
#   missing       RDS metrics (keep the last state; the RDS event
#                 subscription covers outright failure)

data "aws_caller_identity" "current" {}

resource "aws_sns_topic" "alerts" {
  name = "${var.name_prefix}-alerts"
}

resource "aws_sns_topic_policy" "alerts" {
  arn = aws_sns_topic.alerts.arn
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid       = "AccountOwner"
        Effect    = "Allow"
        Principal = { AWS = "*" }
        Action = [
          "SNS:GetTopicAttributes", "SNS:SetTopicAttributes", "SNS:AddPermission",
          "SNS:RemovePermission", "SNS:DeleteTopic", "SNS:Subscribe",
          "SNS:ListSubscriptionsByTopic", "SNS:Publish",
        ]
        Resource  = aws_sns_topic.alerts.arn
        Condition = { StringEquals = { "AWS:SourceOwner" = data.aws_caller_identity.current.account_id } }
      },
      {
        Sid       = "CloudWatchAlarms"
        Effect    = "Allow"
        Principal = { Service = "cloudwatch.amazonaws.com" }
        Action    = "SNS:Publish"
        Resource  = aws_sns_topic.alerts.arn
        Condition = { StringEquals = { "aws:SourceAccount" = data.aws_caller_identity.current.account_id } }
      },
      {
        Sid       = "RdsEvents"
        Effect    = "Allow"
        Principal = { Service = "events.rds.amazonaws.com" }
        Action    = "SNS:Publish"
        Resource  = aws_sns_topic.alerts.arn
      },
    ]
  })
}

resource "aws_sns_topic_subscription" "email" {
  topic_arn = aws_sns_topic.alerts.arn
  protocol  = "email"
  endpoint  = var.alert_email
}

locals {
  alb_dims = { LoadBalancer = var.alb_arn_suffix }
  db_dims  = { DBInstanceIdentifier = var.db_instance_identifier }

  alarms = {
    alb-5xx = {
      description = "ALB targets returned 20 or more 5XX responses in 5 minutes."
      namespace   = "AWS/ApplicationELB"
      metric      = "HTTPCode_Target_5XX_Count"
      dimensions  = local.alb_dims
      statistic   = "Sum"
      period      = 300
      evaluations = 1
      comparison  = "GreaterThanOrEqualToThreshold"
      threshold   = 20
      missing     = "notBreaching"
    }
    web-healthy-hosts = {
      description = "No healthy web targets behind the ALB for 2 minutes."
      namespace   = "AWS/ApplicationELB"
      metric      = "HealthyHostCount"
      dimensions  = merge(local.alb_dims, { TargetGroup = var.web_target_group_arn_suffix })
      statistic   = "Minimum"
      period      = 60
      evaluations = 2
      comparison  = "LessThanThreshold"
      threshold   = 1
      missing     = "breaching"
    }
    worker-healthy-hosts = {
      description = "No healthy worker targets behind the ALB for 5 minutes."
      namespace   = "AWS/ApplicationELB"
      metric      = "HealthyHostCount"
      dimensions  = merge(local.alb_dims, { TargetGroup = var.worker_target_group_arn_suffix })
      statistic   = "Minimum"
      period      = 60
      evaluations = 5
      comparison  = "LessThanThreshold"
      threshold   = 1
      missing     = "breaching"
    }
    web-running-tasks = {
      description = "Fewer than 2 web tasks running for 5 minutes."
      namespace   = "ECS/ContainerInsights"
      metric      = "RunningTaskCount"
      dimensions  = { ClusterName = var.cluster_name, ServiceName = var.web_service_name }
      statistic   = "Minimum"
      period      = 60
      evaluations = 5
      comparison  = "LessThanThreshold"
      threshold   = 2
      missing     = "breaching"
    }
    worker-running-tasks = {
      description = "No worker task running for 5 minutes."
      namespace   = "ECS/ContainerInsights"
      metric      = "RunningTaskCount"
      dimensions  = { ClusterName = var.cluster_name, ServiceName = var.worker_service_name }
      statistic   = "Minimum"
      period      = 60
      evaluations = 5
      comparison  = "LessThanThreshold"
      threshold   = 1
      missing     = "breaching"
    }
    rds-cpu = {
      description = "RDS CPU above 80% for 15 minutes."
      namespace   = "AWS/RDS"
      metric      = "CPUUtilization"
      dimensions  = local.db_dims
      statistic   = "Average"
      period      = 60
      evaluations = 15
      comparison  = "GreaterThanThreshold"
      threshold   = 80
      missing     = "missing"
    }
    rds-free-storage = {
      description = "RDS free storage below 5 GiB."
      namespace   = "AWS/RDS"
      metric      = "FreeStorageSpace"
      dimensions  = local.db_dims
      statistic   = "Minimum"
      period      = 300
      evaluations = 1
      comparison  = "LessThanThreshold"
      threshold   = 5 * 1024 * 1024 * 1024
      missing     = "missing"
    }
    rds-freeable-memory = {
      description = "RDS freeable memory below 256 MiB for 10 minutes."
      namespace   = "AWS/RDS"
      metric      = "FreeableMemory"
      dimensions  = local.db_dims
      statistic   = "Average"
      period      = 60
      evaluations = 10
      comparison  = "LessThanThreshold"
      threshold   = 256 * 1024 * 1024
      missing     = "missing"
    }
    rds-connections = {
      description = "More than 80 RDS connections for 10 minutes."
      namespace   = "AWS/RDS"
      metric      = "DatabaseConnections"
      dimensions  = local.db_dims
      statistic   = "Average"
      period      = 60
      evaluations = 10
      comparison  = "GreaterThanThreshold"
      threshold   = 80
      missing     = "missing"
    }
  }
}

resource "aws_cloudwatch_metric_alarm" "this" {
  for_each = local.alarms

  alarm_name          = "${var.name_prefix}-${each.key}"
  alarm_description   = each.value.description
  namespace           = each.value.namespace
  metric_name         = each.value.metric
  dimensions          = each.value.dimensions
  statistic           = each.value.statistic
  period              = each.value.period
  evaluation_periods  = each.value.evaluations
  datapoints_to_alarm = each.value.evaluations
  comparison_operator = each.value.comparison
  threshold           = each.value.threshold
  treat_missing_data  = each.value.missing

  alarm_actions = [aws_sns_topic.alerts.arn]
  ok_actions    = [aws_sns_topic.alerts.arn]
}

resource "aws_db_event_subscription" "rds" {
  name             = "${var.name_prefix}-rds-events"
  sns_topic        = aws_sns_topic.alerts.arn
  source_type      = "db-instance"
  source_ids       = [var.db_instance_identifier]
  event_categories = ["failover", "failure", "low storage"]

  depends_on = [aws_sns_topic_policy.alerts]
}
