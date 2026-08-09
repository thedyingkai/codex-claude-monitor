param(
    [string]$Version = "dev",
    [string]$Output = "dist"
)

$ErrorActionPreference = "Stop"
$projectRoot = Split-Path -Parent $PSScriptRoot
$outputPath = Join-Path $projectRoot $Output
New-Item -ItemType Directory -Force -Path $outputPath | Out-Null

$targets = @(
    @{ GOOS = "windows"; GOARCH = "amd64"; Name = "quota-monitor-windows-amd64.exe" },
    @{ GOOS = "linux"; GOARCH = "amd64"; Name = "quota-monitor-linux-amd64" },
    @{ GOOS = "linux"; GOARCH = "arm64"; Name = "quota-monitor-linux-arm64" }
)

Push-Location $projectRoot
try {
    foreach ($target in $targets) {
        $env:GOOS = $target.GOOS
        $env:GOARCH = $target.GOARCH
        $env:CGO_ENABLED = "0"
        go build -trimpath -ldflags "-s -w -X main.version=$Version" `
            -o (Join-Path $outputPath $target.Name) ./cmd/quota-monitor
        if ($LASTEXITCODE -ne 0) {
            throw "go build failed for $($target.GOOS)/$($target.GOARCH) (exit $LASTEXITCODE)"
        }
    }
    $checksumLines = foreach ($target in $targets) {
        $artifact = Join-Path $outputPath $target.Name
        $digest = (Get-FileHash -Algorithm SHA256 -LiteralPath $artifact).Hash.ToLowerInvariant()
        "$digest  $($target.Name)"
    }
    [System.IO.File]::WriteAllLines(
        (Join-Path $outputPath "SHA256SUMS"),
        $checksumLines,
        [System.Text.UTF8Encoding]::new($false)
    )
} finally {
    Remove-Item Env:GOOS -ErrorAction SilentlyContinue
    Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
    Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue
    Pop-Location
}
