param(
    [string]$PortName = 'COM3',
    [string]$BaseUrl = 'https://monitor.example.com',
    [string]$TokenPath = (Join-Path $env:LOCALAPPDATA 'QuotaMonitor\display.token')
)

$ErrorActionPreference = 'Stop'

if (-not (Test-Path -LiteralPath $TokenPath -PathType Leaf)) {
    throw "Provisioning token file is missing: $TokenPath"
}
$token = [IO.File]::ReadAllText($TokenPath, [Text.Encoding]::ASCII).Trim()
if ($token -notmatch '^qmon_[A-Za-z0-9_-]{43}$') {
    throw 'Provisioning token failed strict validation.'
}
if ($BaseUrl -notmatch '^https://[A-Za-z0-9.:-]+$') {
    throw 'BaseUrl must be a simple HTTPS origin without a path.'
}
if ($PortName -notmatch '^COM[0-9]+$') {
    throw 'PortName must look like COM3.'
}

$serial = [IO.Ports.SerialPort]::new($PortName, 115200, [IO.Ports.Parity]::None, 8, [IO.Ports.StopBits]::One)
$serial.NewLine = "`n"
$serial.ReadTimeout = 250
$serial.WriteTimeout = 2000
$serial.DtrEnable = $false
$serial.RtsEnable = $false
$configured = $false

function Read-SerialOutput([int]$Milliseconds) {
    $deadline = [DateTime]::UtcNow.AddMilliseconds($Milliseconds)
    $builder = [Text.StringBuilder]::new()
    while ([DateTime]::UtcNow -lt $deadline) {
        try {
            $chunk = $serial.ReadExisting()
            if ($chunk.Length -gt 0) {
                [void]$builder.Append($chunk)
            }
        } catch [TimeoutException] {
        }
        Start-Sleep -Milliseconds 75
    }
    return $builder.ToString()
}

function Send-BoardCommand([string]$Command, [string]$Expected, [int]$WaitMilliseconds = 1200) {
    $serial.DiscardInBuffer()
    $serial.WriteLine($Command)
    $output = Read-SerialOutput $WaitMilliseconds
    if ($output -notmatch [regex]::Escape($Expected)) {
        throw "Board did not acknowledge '$($Command.Split(' ')[0])': $($output.Trim())"
    }
    return $output
}

try {
    $serial.Open()
    Start-Sleep -Milliseconds 1800
    [void](Read-SerialOutput 400)

    [void](Send-BoardCommand "set base_url $BaseUrl" 'OK staged; run save to persist')
    [void](Send-BoardCommand "set token $token" 'OK staged; run save to persist')
    [void](Send-BoardCommand 'set refresh_seconds 15' 'OK staged; run save to persist')
    [void](Send-BoardCommand 'save' 'OK saved' 2000)

    Start-Sleep -Seconds 8
    $testOutput = Send-BoardCommand 'test' 'OK snapshot received' 15000
    $showOutput = Send-BoardCommand 'show' 'base_url=' 1500
    if ($showOutput -notmatch ('base_url=' + [regex]::Escape($BaseUrl))) {
        throw 'Board saved an unexpected base URL.'
    }
    if ($showOutput -notmatch 'token=qmo\.\.\.[A-Za-z0-9_-]{3}') {
        throw 'Board did not report a masked display token.'
    }

    $configured = $true
    Write-Output "Board configured on $PortName; HTTPS snapshot received successfully."
    Write-Output ($showOutput -split "`r?`n" | Where-Object { $_ -match '^(base_url|token|refresh_seconds|dirty)=' })
} finally {
    if ($serial.IsOpen) {
        $serial.Close()
    }
    $serial.Dispose()
    $token = $null
    if ($configured -and (Test-Path -LiteralPath $TokenPath -PathType Leaf)) {
        Remove-Item -LiteralPath $TokenPath -Force
    }
}
