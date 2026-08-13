<#
.SYNOPSIS
    Build the av-bridge-cloud Docker image and push it to ECR.

.DESCRIPTION
    Reads the ECR repo URL from terraform output, logs docker into ECR,
    builds the image from ../../../av-bridge-cloud/Dockerfile, tags it as
    both :<git-sha> and :latest, and pushes both.

    Requires docker desktop to be running, an active SSO session
    (aws sso login --profile <profile>), and git for the SHA.

.PARAMETER Env
    Env name — matches the envs/<env> directory.

.PARAMETER Profile
    AWS CLI profile with access to the env's account.

.EXAMPLE
    ./scripts/push-cloud-image.ps1 -Env uat -Profile avrmm-uat
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Env,
    [Parameter(Mandatory = $true)][string]$Profile
)

$ErrorActionPreference = 'Stop'

$RepoRoot = Resolve-Path "$PSScriptRoot/../../.."
$EnvDir = "$PSScriptRoot/../envs/$Env"
$CloudDir = Join-Path $RepoRoot 'av-bridge-cloud'

if (-not (Test-Path $EnvDir)) {
    throw "Env dir not found: $EnvDir"
}
if (-not (Test-Path (Join-Path $CloudDir 'Dockerfile'))) {
    throw "Dockerfile not found: $CloudDir/Dockerfile"
}

Push-Location $EnvDir
try {
    Write-Host "Reading ECR URL from terraform output..." -ForegroundColor Cyan
    $repoUrl = (terraform output -raw ecr_cloud_repository_url).Trim()
    if (-not $repoUrl) { throw "terraform output ecr_cloud_repository_url is empty. Apply first." }
}
finally {
    Pop-Location
}

$region = ($repoUrl -split '\.')[3]
$registry = ($repoUrl -split '/')[0]

$sha = (git -C $RepoRoot rev-parse --short HEAD).Trim()
if (-not $sha) { throw "git rev-parse failed" }

$tagSha = "${repoUrl}:$sha"
$tagLatest = "${repoUrl}:latest"

Write-Host "Repo:     $repoUrl" -ForegroundColor Cyan
Write-Host "Region:   $region" -ForegroundColor Cyan
Write-Host "SHA:      $sha" -ForegroundColor Cyan

Write-Host "`nLogging docker into ECR..." -ForegroundColor Cyan
# PowerShell 5.1's pipe adds a UTF-16 BOM which docker login rejects with a
# 400 Bad Request. Shell out via cmd's pipe to sidestep the encoding.
cmd /c "aws ecr get-login-password --region $region --profile $Profile | docker login --username AWS --password-stdin $registry"
if ($LASTEXITCODE -ne 0) { throw "docker login failed" }

Write-Host "`nBuilding image..." -ForegroundColor Cyan
docker build -t $tagSha -t $tagLatest $CloudDir
if ($LASTEXITCODE -ne 0) { throw "docker build failed" }

Write-Host "`nPushing $tagSha ..." -ForegroundColor Cyan
docker push $tagSha
if ($LASTEXITCODE -ne 0) { throw "docker push (sha) failed" }

Write-Host "`nPushing $tagLatest ..." -ForegroundColor Cyan
docker push $tagLatest
if ($LASTEXITCODE -ne 0) { throw "docker push (latest) failed" }

Write-Host "`nPushed $tagSha and $tagLatest" -ForegroundColor Green
