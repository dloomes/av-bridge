# av-bridge collector enrolment bootstrap — Windows edition.
#
# Invoked as (elevated PowerShell):
#   $env:AV_ENROLL_TOKEN='<token>'; iwr <portal>/public/collectors/install.ps1 -UseBasicParsing | iex
#
# The portal generates a per-collector one-liner in the /collectors
# page. This script:
#   1. Sanity-checks environment (elevation, TLS, target OS).
#   2. Redeems $env:AV_ENROLL_TOKEN against the cloud → HMAC secret + IDs.
#   3. Downloads the av-bridge Windows binary if it isn't already present.
#   4. Writes C:\ProgramData\av-bridge\{env,config.yaml} with the
#      minimum config the bridge needs to boot; devices come from the
#      cloud config-pull endpoint on first heartbeat.
#   5. Installs the Windows Service via `av-bridge.exe -service install`
#      (kardianos/service), starts it, and waits for /healthz.
#
# Single-use: re-running with the same token fails at step 2. Restarting
# an already-enrolled collector is `Restart-Service av-bridge`.

$ErrorActionPreference = 'Stop'
$ProgressPreference    = 'SilentlyContinue'  # keep Invoke-WebRequest quiet

# ── Substituted at request time by ServeInstallScript. Never edit here. ─────
$CloudBaseUrl = '@@CLOUD_BASE_URL@@'

# ── Constants ────────────────────────────────────────────────────────────────
$InstallDir   = 'C:\Program Files\av-bridge'
$ConfigDir    = 'C:\ProgramData\av-bridge'
$BinaryPath   = Join-Path $InstallDir 'av-bridge.exe'
$ConfigPath   = Join-Path $ConfigDir  'config.yaml'
$EnvPath      = Join-Path $ConfigDir  '.env'
$ServiceName  = 'av-bridge'

function Die($msg) {
    Write-Host "ERROR: $msg" -ForegroundColor Red
    exit 1
}

# ── Preflight ────────────────────────────────────────────────────────────────

# Elevation: the service register + Program Files writes both need Admin.
$isAdmin = ([Security.Principal.WindowsPrincipal] `
    [Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole(
    [Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $isAdmin) {
    Die 'Run this in an elevated PowerShell (Right-click → Run as Administrator).'
}

if (-not $env:AV_ENROLL_TOKEN) {
    Die 'AV_ENROLL_TOKEN environment variable is required. Set it before running:  $env:AV_ENROLL_TOKEN=''<token>'''
}

# TLS 1.2 default — Windows Server 2016 defaults to TLS 1.0 which no
# modern cloud endpoint accepts. Opt into 1.2 unconditionally.
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

$HostnameTag = try { [System.Net.Dns]::GetHostName() } catch { 'unknown' }

# ── Redeem the token ─────────────────────────────────────────────────────────

$EnrollEndpoint = "$CloudBaseUrl/public/collectors/enroll"
Write-Host "==> Redeeming enrollment token against $EnrollEndpoint"

$redeemBody = @{
    token    = $env:AV_ENROLL_TOKEN
    hostname = $HostnameTag
} | ConvertTo-Json -Compress

try {
    $redeemed = Invoke-RestMethod -Uri $EnrollEndpoint -Method Post `
        -ContentType 'application/json' -Body $redeemBody -UseBasicParsing
} catch {
    # Try to surface the cloud's JSON error body if we got one.
    $errText = $_.Exception.Message
    if ($_.Exception.Response) {
        try {
            $stream = $_.Exception.Response.GetResponseStream()
            $reader = New-Object System.IO.StreamReader($stream)
            $bodyText = $reader.ReadToEnd()
            $errText = "$errText`n$bodyText"
        } catch {}
    }
    Die "enrollment failed: $errText"
}

$CollectorId = $redeemed.collector_id
$BridgeId    = $redeemed.bridge_collector_id
$HmacSecret  = $redeemed.hmac_secret
$CloudUrl    = if ($redeemed.cloud_base_url) { $redeemed.cloud_base_url } else { $CloudBaseUrl }

Write-Host "    Enrolled as: $BridgeId (id=$CollectorId)"

# ── Directories ──────────────────────────────────────────────────────────────

New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
New-Item -ItemType Directory -Force -Path $ConfigDir  | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $ConfigDir 'state') | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $ConfigDir 'logs')  | Out-Null

# ── Binary ───────────────────────────────────────────────────────────────────
#
# For collector-enroll v1 we expect the binary to have been pre-placed
# in Program Files (via a downloaded ZIP or an MSI). If it isn't there
# and AV_BRIDGE_BINARY_URL is set, fetch it. A follow-up will teach this
# script to resolve the current GitHub release.

if (-not (Test-Path $BinaryPath)) {
    # Default to the cloud's own downloads endpoint — same origin the
    # script was fetched from, so no cross-domain worry. AV_BRIDGE_BINARY_URL
    # still overrides for air-gapped / mirrored setups.
    $binaryUrl = if ($env:AV_BRIDGE_BINARY_URL) {
        $env:AV_BRIDGE_BINARY_URL
    } else {
        "$CloudBaseUrl/public/downloads/av-bridge-windows-amd64.exe"
    }
    Write-Host "==> Downloading av-bridge from $binaryUrl"
    try {
        Invoke-WebRequest -Uri $binaryUrl -OutFile $BinaryPath -UseBasicParsing
    } catch {
        Die "binary download failed: $($_.Exception.Message)"
    }
}

# ── Config + env ─────────────────────────────────────────────────────────────

Write-Host "==> Writing $EnvPath"
$envContent = @"
# av-bridge — generated by install.ps1 at enrollment time. Do not commit.
CLOUD_WEBHOOK_URL=$CloudUrl/ingest
CLOUD_HMAC_SECRET=$HmacSecret
"@
# UTF-8 without BOM — the bridge's env-file loader is byte-for-byte
# KEY=VALUE parsing, no BOM tolerance.
[System.IO.File]::WriteAllText($EnvPath, $envContent, (New-Object System.Text.UTF8Encoding($false)))

if (-not (Test-Path $ConfigPath)) {
    Write-Host "==> Writing $ConfigPath"
    $statePath = ((Join-Path $ConfigDir 'state\bridge.db') -replace '\\','\\')
    $configContent = @"
# av-bridge — generated by install.ps1. Device inventory pulls from the
# cloud on first heartbeat via /bridge/config. Edit here to override
# local listen address, poll intervals, etc.
hub:
  listen_addr: "0.0.0.0:8080"
  heartbeat_period: 30s
  log_level: info
  collector_id: "$BridgeId"
  store_path: "$statePath"

cloud:
  webhook_url: "`${CLOUD_WEBHOOK_URL}"
  collector_id: "$BridgeId"
  hmac_secret: "`${CLOUD_HMAC_SECRET}"
  portal_api: "$CloudUrl"
  push_interval: 30s
  retry_attempts: 3
  retry_delay: 5s

devices: []
"@
    [System.IO.File]::WriteAllText($ConfigPath, $configContent, (New-Object System.Text.UTF8Encoding($false)))
}

# ── Install the Windows Service ──────────────────────────────────────────────

# If a previous install is present, remove it first so the service
# registration picks up any binary / arg changes.
$existing = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if ($existing) {
    Write-Host "==> Stopping existing $ServiceName service"
    if ($existing.Status -eq 'Running') {
        Stop-Service -Name $ServiceName -Force -ErrorAction SilentlyContinue
    }
    Write-Host "==> Uninstalling existing $ServiceName service"
    & $BinaryPath -service uninstall | Out-Null
    Start-Sleep -Seconds 1
}

Write-Host "==> Installing $ServiceName Windows Service"
& $BinaryPath -config $ConfigPath -env $EnvPath -service install
if ($LASTEXITCODE -ne 0) {
    Die 'service install failed (see output above)'
}

Write-Host "==> Starting $ServiceName Windows Service"
Start-Service -Name $ServiceName

# ── Health check ─────────────────────────────────────────────────────────────

Write-Host '==> Waiting for av-bridge to be healthy...'
$healthy = $false
for ($i = 0; $i -lt 30; $i++) {
    try {
        $resp = Invoke-WebRequest -Uri 'http://127.0.0.1:8080/healthz' `
            -UseBasicParsing -TimeoutSec 2 -ErrorAction Stop
        if ($resp.StatusCode -eq 200) { $healthy = $true; break }
    } catch {}
    Start-Sleep -Seconds 2
}

if (-not $healthy) {
    Die 'av-bridge failed to become healthy — check the Windows Event Viewer (Application log) or the console output of the service.'
}

Write-Host ''
Write-Host '===================================================='
Write-Host " av-bridge enrolled + running as $BridgeId"
Write-Host '===================================================='
Write-Host ''
Write-Host '  Next steps:'
Write-Host '  * The portal /collectors page will light up green'
Write-Host '    once the first heartbeat lands (~30 seconds).'
Write-Host '  * Add devices from the portal — the bridge pulls'
Write-Host '    config on each poll and reconciles automatically.'
Write-Host '  * Logs: Get-EventLog -LogName Application -Source av-bridge -Newest 50'
Write-Host "  * Restart: Restart-Service $ServiceName"
Write-Host ''
