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
$proc = Start-Process -FilePath dist/netguard-demo.exe -ArgumentList '-config demo/config.yaml' `
    -RedirectStandardError demo/netguard.log -WindowStyle Hidden -PassThru
try {
    # Wait for the dashboard and for the blocklists to load (first run downloads them).
    $deadline = (Get-Date).AddSeconds(120)
    $ready = $false
    while (-not $ready -and (Get-Date) -lt $deadline) {
        if ($proc.HasExited) { throw "demo NetGuard exited; see demo/netguard.log" }
        try { $ready = (Invoke-RestMethod http://127.0.0.1:18080/api/summary).list_domains -gt 0 } catch {}
        if (-not $ready) { Start-Sleep -Milliseconds 500 }
    }
    if (-not $ready) { throw 'demo NetGuard did not become ready; see demo/netguard.log' }

    New-Item -ItemType Directory -Force docs/screenshots | Out-Null
    $profileDir = Join-Path $env:TEMP 'netguard-screenshots'
    $shots = @(
        @{ name = 'overview';    page = 'overview'; height = 1350; dark = $false },
        @{ name = 'ads';         page = 'ads';      height = 1500; dark = $false },
        @{ name = 'devices';     page = 'devices';  height = 1000; dark = $false },
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
