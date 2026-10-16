<#
.SYNOPSIS
  Regenerate the README screenshots from fictional demo data.

  Creates demo/data with tools/demodata, runs NetGuard on local-only ports
  with demo/config.yaml, captures each dashboard page with headless Edge into
  docs/screenshots/, then stops NetGuard. Run from anywhere; needs Go and Edge.
#>
$ErrorActionPreference = 'Continue'  # native tools log to stderr; exit codes are checked instead
$root = Split-Path $PSScriptRoot -Parent
Set-Location $root

$edge = @(
    "${env:ProgramFiles(x86)}\Microsoft\Edge\Application\msedge.exe",
    "$env:ProgramFiles\Microsoft\Edge\Application\msedge.exe",
    "$env:ProgramFiles\Google\Chrome\Application\chrome.exe"
) | Where-Object { Test-Path $_ } | Select-Object -First 1
if (-not $edge) { throw 'Microsoft Edge or Google Chrome is required.' }

Write-Host '==> Generating demo data'
if (Test-Path demo/data) { Remove-Item -Recurse -Force demo/data }
go run ./tools/demodata -out demo/data/netguard.db
if ($LASTEXITCODE) { throw 'demodata failed' }
go build -o dist/netguard-demo.exe ./cmd/netguard
if ($LASTEXITCODE) { throw 'build failed' }

Write-Host '==> Starting demo NetGuard on http://127.0.0.1:18080'
# Antivirus (e.g. Avast) may first run a freshly built program in its own
# sandbox, where it grabs the ports and makes our real start fail. So retry,
# waiting for the scan to finish.
$proc = $null
for ($attempt = 1; $attempt -le 4 -and -not $proc; $attempt++) {
    $p = Start-Process -FilePath dist/netguard-demo.exe -ArgumentList '-config demo/config.yaml' `
        -RedirectStandardError demo/netguard.log -WindowStyle Hidden -PassThru
    # Wait for the dashboard and for the blocklists to load (first run downloads them).
    $deadline = (Get-Date).AddSeconds(120)
    while ((Get-Date) -lt $deadline -and -not $p.HasExited) {
        try { if ((Invoke-RestMethod http://127.0.0.1:18080/api/summary).list_domains -gt 0) { $proc = $p; break } } catch {}
        Start-Sleep -Milliseconds 500
    }
    if (-not $proc) {
        Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue
        Write-Host "    not ready (attempt $attempt, likely an antivirus scan); retrying in 15 s"
        Start-Sleep -Seconds 15
    }
}
if (-not $proc) { throw 'demo NetGuard did not start; see demo/netguard.log' }
try {
    New-Item -ItemType Directory -Force docs/screenshots | Out-Null
    $profileDir = Join-Path $env:TEMP 'netguard-screenshots'
    if (Test-Path $profileDir) { Remove-Item -Recurse -Force $profileDir }  # no remembered view settings
    $kidsIpad = [uri]::EscapeDataString('02:9e:41:d0:3a:27')
    $shots = @(
        @{ name = 'overview';    page = 'overview'; height = 1350; dark = $false },
        @{ name = 'ads';         page = 'ads';      height = 1500; dark = $false },
        @{ name = 'devices';     page = 'devices';  height = 1000; dark = $false },
        @{ name = 'summary';     page = "devices/$kidsIpad"; height = 900; dark = $false },
        @{ name = 'groups';      page = 'groups';   height = 1250; dark = $false },
        @{ name = 'querylog';    page = 'log';      height = 1000; dark = $false },
        @{ name = 'ads-dark';    page = 'ads';      height = 1000; dark = $true }
    )
    foreach ($s in $shots) {
        $out = Join-Path $root "docs/screenshots/$($s.name).png"
        $edgeArgs = @('--headless=new', '--disable-gpu', '--hide-scrollbars', "--user-data-dir=$profileDir",
            "--window-size=1280,$($s.height)", '--virtual-time-budget=6000', "--screenshot=$out")
        if ($s.dark) { $edgeArgs += '--force-dark-mode', '--blink-settings=preferredColorScheme=0' }
        $edgeArgs += "http://127.0.0.1:18080/#$($s.page)"
        & $edge @edgeArgs 2>$null | Out-Null
        Write-Host "    $($s.name).png"
    }
} finally {
    Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
}
Write-Host '==> Done: docs/screenshots/'
