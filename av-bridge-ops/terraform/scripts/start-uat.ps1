<#
.SYNOPSIS
    Bring the UAT compute + database back up after a stop-uat.ps1.

.DESCRIPTION
    Starts the RDS instance, waits until it's available, then scales ECS
    back to 1 task. RDS start takes 3-5 min; the wait avoids ECS tasks
    crash-looping on "migration DB not reachable".

.EXAMPLE
    ./scripts/start-uat.ps1 -Profile avrmm-uat
#>
[CmdletBinding()]
param(
    [string]$Profile = "avrmm-uat",
    [string]$Region = "eu-west-2",
    [string]$Cluster = "avrmm-uat-cluster",
    [string]$Service = "avrmm-uat-cloud",
    [string]$DbInstance = "avrmm-uat-postgres",
    [int]$DesiredCount = 1
)
$ErrorActionPreference = 'Stop'

Write-Host "Starting RDS instance $DbInstance..." -ForegroundColor Cyan
aws rds start-db-instance --db-instance-identifier $DbInstance --profile $Profile --region $Region --no-cli-pager --query "DBInstance.{id:DBInstanceIdentifier,status:DBInstanceStatus}" | Out-Host
if ($LASTEXITCODE -ne 0) {
    Write-Warning "rds start-db-instance failed (already running?) — checking status"
}

Write-Host "`nWaiting for RDS to become available (3-5 min)..." -ForegroundColor Cyan
aws rds wait db-instance-available --db-instance-identifier $DbInstance --profile $Profile --region $Region
if ($LASTEXITCODE -ne 0) { throw "RDS never became available" }
Write-Host "RDS available." -ForegroundColor Green

Write-Host "`nScaling ECS service $Service to $DesiredCount..." -ForegroundColor Cyan
aws ecs update-service --cluster $Cluster --service $Service --desired-count $DesiredCount --profile $Profile --region $Region --no-cli-pager --query "service.{name:serviceName,desired:desiredCount}" | Out-Host
if ($LASTEXITCODE -ne 0) { throw "ecs update-service failed" }

Write-Host "`nDone. Give it ~60s more for a task to pass health check, then curl /healthz." -ForegroundColor Green
