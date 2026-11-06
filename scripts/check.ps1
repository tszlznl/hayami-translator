# Local checks for Hayami Translator. Run with pwsh 7:
#   pwsh -File scripts/check.ps1
# Add -Race to run the test suite with the race detector.
param(
    [switch]$Race
)

$ErrorActionPreference = "Stop"
Set-Location (Split-Path -Parent $PSScriptRoot)

function Invoke-Step {
    param([string]$Name, [scriptblock]$Command)
    Write-Host "==> $Name" -ForegroundColor Cyan
    & $Command
    if ($LASTEXITCODE -ne 0) {
        Write-Host "FAILED: $Name" -ForegroundColor Red
        exit $LASTEXITCODE
    }
}

$unformatted = & gofmt -l .
if ($unformatted) {
    Write-Host "FAILED: these files need gofmt:" -ForegroundColor Red
    $unformatted | ForEach-Object { Write-Host "  $_" }
    exit 1
}

Invoke-Step "go vet" { go vet ./... }
Invoke-Step "mygo vet" { go tool mygo vet . }

if ($Race) {
    Invoke-Step "go test -race" { go test -race ./... }
} else {
    Invoke-Step "go test" { go test ./... }
}

Write-Host "All checks passed." -ForegroundColor Green
