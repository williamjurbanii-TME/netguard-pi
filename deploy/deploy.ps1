<#
.SYNOPSIS
  Build NetGuard for the Raspberry Pi and install or update it over SSH.

.EXAMPLE
  .\deploy\deploy.ps1 -Target pi@192.168.1.2 -Install      # first time (also copies config.yaml)
  .\deploy\deploy.ps1 -Target pi@192.168.1.2               # update to the current code
  .\deploy\deploy.ps1 -Target pi@192.168.1.2 -PushConfig   # update and replace the Pi's config.yaml
  .\deploy\deploy.ps1 -Target pi@192.168.1.2 -Rollback     # undo the last update
#>
param(
    [Parameter(Mandatory)] [string]$Target,
    [switch]$Install,
    [switch]$PushConfig,
    [switch]$Rollback,
    [switch]$SkipTests
)
$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent

function Invoke-Native([string]$What, [scriptblock]$Cmd) {
    # Judge native tools by exit code only: ssh/go write normal progress to
    # stderr, which PowerShell 5.1 would otherwise turn into a terminating error.
    $ErrorActionPreference = 'Continue'
    & $Cmd
    if ($LASTEXITCODE -ne 0) { throw "$What failed (exit $LASTEXITCODE)" }
}

if ($Rollback) {
    Invoke-Native 'rollback' { ssh -t $Target 'sudo bash /opt/netguard/install.sh rollback' }
    return
}

Push-Location $root
try {
    if (($Install -or $PushConfig) -and -not (Test-Path config.yaml)) {
        throw 'config.yaml not found. Copy config.example.yaml to config.yaml and edit it first.'
    }
    if (-not $SkipTests) {
        Write-Host '==> Running tests'
        Invoke-Native 'tests' { go test ./... }
    }

    $version = Get-Date -Format 'yyyy.MM.dd-HHmm'
    Write-Host "==> Building NetGuard $version for Raspberry Pi (linux/arm64)"
    $env:GOOS = 'linux'; $env:GOARCH = 'arm64'; $env:CGO_ENABLED = '0'
    try {
        Invoke-Native 'build' { go build -trimpath -ldflags "-s -w -X main.version=$version" -o dist/netguard ./cmd/netguard }
    } finally {
        Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue
    }

    $files = @('dist/netguard', 'deploy/install.sh', 'deploy/netguard.service')
    if ($Install -or $PushConfig) { $files += 'config.yaml' }

    Write-Host "==> Uploading to $Target"
    Invoke-Native 'prepare upload folder' { ssh $Target 'rm -rf /tmp/netguard-upload && mkdir -p /tmp/netguard-upload' }
    Invoke-Native 'upload' { scp -q @files "${Target}:/tmp/netguard-upload/" }

    $mode = if ($Install) { 'install' } else { 'update' }
    $flag = if ($PushConfig) { '--config' } else { '' }
    Write-Host "==> Running $mode on the Pi"
    Invoke-Native $mode { ssh -t $Target "sudo bash /tmp/netguard-upload/install.sh $mode $flag" }
} finally {
    Pop-Location
}
