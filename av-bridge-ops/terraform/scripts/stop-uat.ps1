<#
.SYNOPSIS
    Stop the UAT compute + database to save cost while idle.

.DESCRIPTION
    Sets the ECS service desiredCount to 0 (stops Fargate tasks) and stops
    the RDS instance. NAT gateway + ALB stay running — those cost
    ~£40/mo and can't be stopped without destroying them.

    Overnight savings vs full-stack: ~£0.30/night. Not huge but free money.

    RDS "stop" is time-limited: AWS auto-restarts after 7 days. That's a
    feature, not a bug — it forces you to remember you left something off.

.EXAMPLE
    ./scripts/stop-uat.ps1 -Profile avrmm-uat
#>
[CmdletBinding()]
param(
    [string]$Profile = "avrmm-uat",
    [string]$Region = "eu-west-2",
    [string]$Cluster = "avrmm-uat-cluster",
    [string]$Service = "avrmm-uat-cloud",
    [string]$DbInstance = "avrmm-uat-postgres"
)
$ErrorActionPreference = 'Stop'

Write-Host "Scaling ECS service $Service to 0..." -ForegroundColor Cyan
aws ecs update-service --cluster $Cluster --service $Service --desired-count 0 --profile $Profile --region $Region --no-cli-pager --query "service.{name:serviceName,desired:desiredCount}" | Out-Host
if ($LASTEXITCODE -ne 0) { throw "ecs update-service failed" }

Write-Host "`nStopping RDS instance $DbInstance..." -ForegroundColor Cyan
aws rds stop-db-instance --db-instance-identifier $DbInstance --profile $Profile --region $Region --no-cli-pager --query "DBInstance.{id:DBInstanceIdentifier,status:DBInstanceStatus}" | Out-Host
if ($LASTEXITCODE -ne 0) {
    Write-Warning "rds stop-db-instance failed (already stopped? in an ineligible state?) — continuing"
}

Write-Host "`nDone. NAT + ALB stay up (~£1.40/day between them). Call start-uat.ps1 to bring compute back." -ForegroundColor Green
