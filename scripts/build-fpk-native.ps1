param(
    [string]$FnpackPath = "",
    [string]$FnpackVersion = "1.2.1",
    [string]$Version = "1.0.0",
    [string]$Commit = "",
    [string]$BuildTime = ""
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$packageDir = Join-Path $repoRoot "packaging\fpk-native"
$distDir = Join-Path $repoRoot "dist\fpk"
$binaryPath = Join-Path $packageDir "app\bin\avd"

if (-not $Commit) {
    try {
        $Commit = (& git -C $repoRoot rev-parse HEAD).Trim()
    } catch {
        $Commit = "unknown"
    }
}

if (-not $BuildTime) {
    $BuildTime = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
}

New-Item -ItemType Directory -Force -Path (Split-Path -Parent $binaryPath) | Out-Null

$env:CGO_ENABLED = "0"
$env:GOOS = "linux"
$env:GOARCH = "amd64"

Push-Location $repoRoot
try {
    go build -trimpath "-ldflags=-s -w -X main.version=v$Version -X main.commit=$Commit -X main.buildTime=$BuildTime" -o $binaryPath .\cmd\avd
} finally {
    Pop-Location
}

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
    $fpkName = "jimuqu-avd-native.fpk"
    $source = Join-Path $repoRoot $fpkName
    if (Test-Path $source) {
        Move-Item -LiteralPath $source -Destination (Join-Path $distDir $fpkName) -Force
        Write-Host "FPK output: $(Join-Path $distDir $fpkName)"
    }
} finally {
    Pop-Location
}
