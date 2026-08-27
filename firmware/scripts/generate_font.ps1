param(
    [Parameter(Mandatory = $true)]
    [string]$FontPath,
    [string]$LvFontConv = "lv_font_conv"
)

$ErrorActionPreference = "Stop"
# Keep the script ASCII-only so Windows PowerShell 5.1 can parse it without a
# UTF-8 BOM. These code points spell the exact Simplified Chinese UI subset.
$Symbols = -join (@(
    0x4E0D, 0x5E38, 0x7535, 0x5EA6, 0x8FC7, 0x5373, 0x95F4, 0x636E,
    0x53EF, 0x79BB, 0x7ACB, 0x8FDE, 0x4EAE, 0x91CF, 0x7EDC, 0x79D2,
    0x671F, 0x5269, 0x65F6, 0x6570, 0x5237, 0x5929, 0x7F51, 0x65E0,
    0x7EBF, 0x5C0F, 0x6821, 0x65B0, 0x5DF2, 0x7528, 0x5728, 0x6B63,
    0x51C6, 0x91CD, 0x7F6E, 0x672A, 0x767B, 0x5F55
) | ForEach-Object { [char]$_ })
$FirmwareRoot = Split-Path -Parent $PSScriptRoot
$OutputPath = Join-Path $FirmwareRoot "src\lv_font_qmon_16.c"

if (-not (Test-Path -LiteralPath $FontPath -PathType Leaf)) {
    throw "Font file not found: $FontPath"
}

& $LvFontConv `
    --font $FontPath `
    --size 16 `
    --bpp 4 `
    --format lvgl `
    --range 0x20-0x7E `
    --symbols $Symbols `
    --lv-include lvgl.h `
    --lv-font-name lv_font_qmon_16 `
    --output $OutputPath

if ($LASTEXITCODE -ne 0) {
    throw "lv_font_conv failed with exit code $LASTEXITCODE"
}

# lv_font_conv records its full command line in the generated header. Replace
# that line so generated source never exposes a workstation path.
$Generated = [IO.File]::ReadAllText($OutputPath)
$Generated = [Text.RegularExpressions.Regex]::Replace(
    $Generated,
    '(?m)^ \* Opts:.*$',
    ' * Generated with lv_font_conv from Noto Sans CJK SC Medium at 16 px / 4 bpp.'
)
$Generated = $Generated.TrimEnd("`r", "`n") + "`n"
[IO.File]::WriteAllText($OutputPath, $Generated, [Text.UTF8Encoding]::new($false))

Write-Host "Generated $OutputPath"
