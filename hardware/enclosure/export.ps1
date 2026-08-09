$ErrorActionPreference = "Stop"
$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$outDir = Join-Path (Split-Path -Parent $scriptDir) "rendered\enclosure"
$openScadImage = "openscad/openscad@sha256:147e48525bec392bcf628d7a6d5ea4ccac71b16251952328f86e1061cbf47c37"
New-Item -ItemType Directory -Force -Path $outDir | Out-Null

$openScadCommand = Get-Command openscad -ErrorAction SilentlyContinue
$openScadPath = if ($openScadCommand) { $openScadCommand.Source } else { $null }
if (-not $openScadPath) {
    $candidate = "C:\Program Files\OpenSCAD\openscad.exe"
    if (Test-Path $candidate) { $openScadPath = $candidate }
}
foreach ($item in @(@("base", 1), @("lid", 2), @("button", 3), @("switch", 4))) {
    $name = $item[0]
    $partId = $item[1]
    if ($openScadPath) {
        & $openScadPath -D "part_id=$partId" -o (Join-Path $outDir "quota_display_$name.stl") (Join-Path $scriptDir "quota_display.scad")
    } else {
        $repoRoot = (Resolve-Path (Join-Path $scriptDir "..\..")).Path
        docker run --rm -v "${repoRoot}:/work" -w /work/hardware/enclosure `
          $openScadImage openscad -D "part_id=$partId" `
          -o "/work/hardware/rendered/enclosure/quota_display_$name.stl" quota_display.scad
    }
    if ($LASTEXITCODE -ne 0) { throw "OpenSCAD failed while exporting $name" }
}

Write-Host "STL files written to $outDir"
Write-Host "Run FreeCADCmd export_step.py to create genuine STEP solids."
