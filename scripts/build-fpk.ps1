param(
    [string]$FnpackPath = "",
    [string]$FnpackVersion = "1.2.1"
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$packageDir = Join-Path $repoRoot "packaging\fpk"
$distDir = Join-Path $repoRoot "dist\fpk"

if (-not $FnpackPath) {
    $existing = Get-Command fnpack -ErrorAction SilentlyContinue
    if ($existing) {
        $FnpackPath = $existing.Source
    } else {
        $toolsDir = Join-Path $repoRoot ".tools"
        New-Item -ItemType Directory -Force -Path $toolsDir | Out-Null
        $FnpackPath = Join-Path $toolsDir "fnpack.exe"

        if (-not (Test-Path $FnpackPath)) {
            $url = "https://static2.fnnas.com/fnpack/fnpack-$FnpackVersion-windows-amd64"
            Invoke-WebRequest -UseBasicParsing -Uri $url -OutFile $FnpackPath
        }
    }
}

Push-Location $repoRoot
try {
    & $FnpackPath build --directory $packageDir

    New-Item -ItemType Directory -Force -Path $distDir | Out-Null
    $fpkName = "jimuqu-avd.fpk"
    $source = Join-Path $repoRoot $fpkName
    if (Test-Path $source) {
        Move-Item -LiteralPath $source -Destination (Join-Path $distDir $fpkName) -Force
        Write-Host "FPK output: $(Join-Path $distDir $fpkName)"
    }
} finally {
    Pop-Location
}
