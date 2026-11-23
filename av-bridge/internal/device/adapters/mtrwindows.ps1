# mtr_windows poll script — run by the Collector over WinRM on a Microsoft
# Teams Rooms on Windows PC, once per poll. Embedded in the Collector
# (mtrwindows.go); never edited on the PC.
#
# READ-ONLY. It reads the event log, the registry, the UDP endpoint table and
# WMI. It must never write a file, change a setting, start or stop anything.
#
# Output: exactly one JSON object on stdout (schema 1). Every section runs in
# its own try/catch: a section that fails leaves its value null and records
# why under section_errors, so one unreadable source never fails the poll.
#
# Everything Windows-side here is unverified until tried on a real MTR —
# see the "unverified" notes; the Go side (mtrwindows_parse.go) carries the
# matching notes.

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
try { [Console]::OutputEncoding = [Text.Encoding]::UTF8 } catch { }

$out = [ordered]@{
    schema         = 1
    event_log      = $null  # true when the Skype Room System log exists
    events         = $null  # newest info events (ID 2000): heartbeat, hardware checks
    errors         = $null  # error events (ID 2001, 3001) from the last 15 minutes
    mic            = $null  # microphone in use now
    camera         = $null  # camera in use now
    udp            = $null  # Teams UDP media sockets
    pc             = $null  # OS, uptime, make/model/serial
    section_errors = [ordered]@{}
}

# ── 1. Skype Room System event log ─────────────────────────────────────────
# unverified: confirm on a Windows MTR — log name, and event IDs 2000 (info:
# heartbeat / hardware check), 2001 and 3001 (errors), per Microsoft's
# "Teams Rooms event log" documentation.
$logName = 'Skype Room System'

# Event rows, written to the pipeline one by one; callers collect with @().
function ConvertTo-EventRows($events) {
    foreach ($e in @($events)) {
        if ($null -eq $e) { continue }
        $msg = [string]$e.Message
        if ($msg.Length -gt 3000) { $msg = $msg.Substring(0, 3000) }
        [ordered]@{
            id      = [int]$e.Id
            time    = $e.TimeCreated.ToUniversalTime().ToString('o')
            message = $msg
        }
    }
}

function Read-Events([string]$xpath, [int]$max) {
    try {
        ConvertTo-EventRows (Get-WinEvent -LogName $logName -FilterXPath $xpath -MaxEvents $max -ErrorAction Stop)
    } catch {
        # No matching events is not a failure: no rows.
        if ($_.FullyQualifiedErrorId -notlike 'NoMatchingEventsFound*') { throw }
    }
}

try {
    $null = Get-WinEvent -ListLog $logName -ErrorAction Stop
    $out.event_log = $true
} catch {
    $out.event_log = $false
    $out.section_errors['event_log'] = $_.Exception.Message
}
if ($out.event_log) {
    try {
        $out.events = @(Read-Events '*[System[(EventID=2000)]]' 20)
    } catch { $out.section_errors['events'] = $_.Exception.Message }
    try {
        $out.errors = @(Read-Events '*[System[(EventID=2001 or EventID=3001) and TimeCreated[timediff(@SystemTime) <= 900000]]]' 20)
    } catch { $out.section_errors['errors'] = $_.Exception.Message }
}

# ── 2. Microphone / camera in use (CapabilityAccessManager consent store) ──
# An app using the device now has LastUsedTimeStop = 0 (and a non-zero
# LastUsedTimeStart) under ...\ConsentStore\<capability>\<package> or
# ...\NonPackaged\<exe path>.
# unverified: confirm on a Windows MTR — the registry path, the
# LastUsedTimeStop = 0 rule, and that the Teams Rooms app runs as the local
# account "Skype" whose hive is loaded under HKU\<SID>.
$mtrUser = 'Skype'
$consentRel = 'SOFTWARE\Microsoft\Windows\CurrentVersion\CapabilityAccessManager\ConsentStore\'

# The user hives to read: the Skype account's (strict: unreadable fails the
# section, so it reports null rather than a false "not in use"), or, with no
# Skype account, every loaded user hive (lenient: unreadable ones are
# skipped, but at least one must be read).
function Get-UserHives {
    $acct = Get-CimInstance Win32_UserAccount -Filter "LocalAccount=True AND Name='$mtrUser'" -ErrorAction Stop
    if ($acct) {
        return [ordered]@{ strict = $true; hives = @('Registry::HKEY_USERS\' + $acct.SID) }
    }
    # Local / domain (S-1-5-21) and Entra ID (S-1-12-1) users; GetSubKeyNames
    # lists names without opening the service accounts' hives.
    $hives = @([Microsoft.Win32.Registry]::Users.GetSubKeyNames() |
        Where-Object { $_ -match '^S-1-(5-21|12-1)-[\d-]+$' } |
        ForEach-Object { 'Registry::HKEY_USERS\' + $_ })
    [ordered]@{ strict = $false; hives = $hives }
}

# Apps using the capability now under one registry root, written to the
# pipeline (none when the root has no ConsentStore key for it). Throws when
# a key can't be read.
function Get-InUseUnder([string]$root, [string]$capability) {
    $base = $root + '\' + $consentRel + $capability
    if (-not (Test-Path -LiteralPath $base)) { return }
    $keys = @(Get-ChildItem -LiteralPath $base -ErrorAction Stop)
    $np = $base + '\NonPackaged'
    if (Test-Path -LiteralPath $np) { $keys += @(Get-ChildItem -LiteralPath $np -ErrorAction Stop) }
    foreach ($k in $keys) {
        $p = Get-ItemProperty -LiteralPath $k.PSPath -ErrorAction Stop
        if ($null -eq $p.LastUsedTimeStop) { continue }
        if ([int64]$p.LastUsedTimeStop -eq 0 -and [int64]$p.LastUsedTimeStart -gt 0) {
            [string]$k.PSChildName
        }
    }
}

function Get-InUse($users, [string]$capability) {
    $apps = @(Get-InUseUnder 'Registry::HKEY_LOCAL_MACHINE' $capability)
    $read = 0
    foreach ($hive in $users.hives) {
        try {
            $null = Get-Item -LiteralPath $hive -ErrorAction Stop
            $apps += @(Get-InUseUnder $hive $capability)
            $read++
        } catch {
            if ($users.strict) { throw }
        }
    }
    if ($read -eq 0) { throw "no user registry hive readable for $capability" }
    [ordered]@{ in_use = ($apps.Count -gt 0); apps = $apps }
}

try {
    $users = Get-UserHives
    try { $out.mic = Get-InUse $users 'microphone' } catch { $out.section_errors['mic'] = $_.Exception.Message }
    try { $out.camera = Get-InUse $users 'webcam' } catch { $out.section_errors['camera'] = $_.Exception.Message }
} catch {
    $out.section_errors['mic'] = $_.Exception.Message
    $out.section_errors['camera'] = $_.Exception.Message
}

# ── 3. Teams UDP media sockets ─────────────────────────────────────────────
# Local UDP endpoints owned by a Teams process on ports >= 50000 (Teams
# media). Remote ports (3478-3481) are not visible in this table.
# unverified: confirm on a Windows MTR — the Teams process names, and that
# idle Teams holds no media ports >= 50000.
$teamsNames = @('ms-teams', 'Teams', 'MSTeams', 'ms-teams_modulehost')
try {
    $procs = @(Get-Process -Name $teamsNames -ErrorAction SilentlyContinue)
    $ids = @($procs | ForEach-Object { $_.Id })
    $n = 0
    if ($ids.Count -gt 0) {
        $n = @(Get-NetUDPEndpoint -ErrorAction Stop |
            Where-Object { ($ids -contains $_.OwningProcess) -and ($_.LocalPort -ge 50000) }).Count
    }
    $out.udp = [ordered]@{ teams_processes = $procs.Count; media_sockets = $n }
} catch { $out.section_errors['udp'] = $_.Exception.Message }

# ── 4. PC identity and uptime (WMI) ────────────────────────────────────────
try {
    $os = Get-CimInstance Win32_OperatingSystem -ErrorAction Stop
    $cs = Get-CimInstance Win32_ComputerSystem -ErrorAction Stop
    $bios = Get-CimInstance Win32_BIOS -ErrorAction Stop
    $out.pc = [ordered]@{
        os_caption   = [string]$os.Caption
        os_version   = [string]$os.Version
        uptime_s     = [int64]((Get-Date) - $os.LastBootUpTime).TotalSeconds
        manufacturer = [string]$cs.Manufacturer
        model        = [string]$cs.Model
        serial       = [string]$bios.SerialNumber
    }
} catch { $out.section_errors['pc'] = $_.Exception.Message }

$out | ConvertTo-Json -Depth 6 -Compress
