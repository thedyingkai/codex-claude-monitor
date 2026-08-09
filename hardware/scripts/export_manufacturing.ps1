$ErrorActionPreference = "Stop"

$root = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
$board = "/work/hardware/pcb/carrier.kicad_pcb"
$schematic = "/work/hardware/pcb/carrier.kicad_sch"
$out = Join-Path $root "hardware\rendered\pcb"
$gerber = Join-Path $out "gerber"

python (Join-Path $root "hardware\scripts\generate_kicad.py")
if ($LASTEXITCODE -ne 0) { throw "Deterministic board generation failed" }

New-Item -ItemType Directory -Force -Path $out | Out-Null
if (Test-Path -LiteralPath $gerber) {
    $resolvedGerber = (Resolve-Path -LiteralPath $gerber).Path
    $expectedGerber = [IO.Path]::GetFullPath((Join-Path $root "hardware\rendered\pcb\gerber"))
    if ($resolvedGerber -ne $expectedGerber) { throw "Refusing to clean unexpected path: $resolvedGerber" }
    Remove-Item -LiteralPath $resolvedGerber -Recurse -Force
}
New-Item -ItemType Directory -Force -Path $gerber | Out-Null

function Invoke-KiCad([string[]]$Arguments) {
    & docker run --rm -v "${root}:/work" -w /work kicad/kicad:9.0 kicad-cli @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "KiCad command failed: kicad-cli $($Arguments -join ' ')"
    }
}

& docker run --rm -v "${root}:/work" -w /work kicad/kicad:9.0 `
    python3 /work/hardware/scripts/verify_pcb.py $board
if ($LASTEXITCODE -ne 0) { throw "PCB electrical/mechanical invariant check failed" }

Invoke-KiCad @(
    "sch", "erc", "--exit-code-violations", "--severity-all",
    "--output", "/work/hardware/rendered/pcb/erc.rpt", $schematic
)
Invoke-KiCad @(
    "sch", "export", "netlist", "--format", "kicadxml",
    "--output", "/work/hardware/rendered/pcb/carrier-netlist.xml", $schematic
)
& docker run --rm -v "${root}:/work" -w /work kicad/kicad:9.0 `
    python3 /work/hardware/scripts/verify_schematic_netlist.py `
    $board /work/hardware/rendered/pcb/carrier-netlist.xml
if ($LASTEXITCODE -ne 0) { throw "Schematic/PCB netlist comparison failed" }

Invoke-KiCad @(
    "pcb", "drc", "--exit-code-violations", "--severity-all",
    "--output", "/work/hardware/rendered/pcb/drc.rpt", $board
)
Invoke-KiCad @(
    "pcb", "export", "gerbers", "--precision", "6",
    "--layers", "F.Cu,B.Cu,F.Paste,B.Paste,F.SilkS,B.SilkS,F.Mask,B.Mask,Edge.Cuts",
    "--output", "/work/hardware/rendered/pcb/gerber/", $board
)
Invoke-KiCad @(
    "pcb", "export", "drill", "--format", "excellon", "--excellon-units", "mm",
    "--excellon-separate-th", "--generate-map", "--map-format", "pdf",
    "--generate-report", "--report-path", "/work/hardware/rendered/pcb/drill-report.txt",
    "--output", "/work/hardware/rendered/pcb/gerber/", $board
)
Invoke-KiCad @(
    "pcb", "export", "pos", "--format", "csv", "--units", "mm",
    "--side", "both", "--smd-only",
    "--output", "/work/hardware/rendered/pcb/carrier-pos.csv", $board
)
& docker run --rm -v "${root}:/work" -w /work kicad/kicad:9.0 `
    python3 /work/hardware/scripts/verify_pnp.py `
    /work/hardware/pcb/positions.csv /work/hardware/rendered/pcb/carrier-pos.csv
if ($LASTEXITCODE -ne 0) { throw "PnP comparison failed" }
Invoke-KiCad @(
    "pcb", "export", "pdf", "--mode-multipage", "--black-and-white",
    "--sketch-pads-on-fab-layers",
    "--layers", "F.Cu,B.Cu,F.Mask,B.Mask,F.Fab,Edge.Cuts",
    "--output", "/work/hardware/rendered/pcb/layers/", $board
)
$layerPdf = Join-Path $out "carrier-layers.pdf"
if (Test-Path -LiteralPath $layerPdf) { Remove-Item -LiteralPath $layerPdf -Recurse -Force }
Move-Item -LiteralPath (Join-Path $out "layers\carrier.pdf") -Destination $layerPdf
Remove-Item -LiteralPath (Join-Path $out "layers") -Recurse -Force
Invoke-KiCad @(
    "pcb", "render", "--side", "top", "--quality", "high",
    "--width", "1800", "--height", "1200", "--background", "opaque",
    "--output", "/work/hardware/rendered/pcb/carrier-top.png", $board
)
Invoke-KiCad @(
    "pcb", "render", "--side", "bottom", "--quality", "high",
    "--width", "1800", "--height", "1200", "--background", "opaque",
    "--output", "/work/hardware/rendered/pcb/carrier-bottom.png", $board
)
Invoke-KiCad @(
    "sch", "export", "pdf",
    "--output", "/work/hardware/rendered/pcb/carrier-connections.pdf", $schematic
)

Copy-Item -LiteralPath (Join-Path $root "hardware\pcb\bom.csv") -Destination (Join-Path $out "bom.csv") -Force
Copy-Item -LiteralPath (Join-Path $root "hardware\pcb\system_bom.csv") -Destination (Join-Path $out "system_bom.csv") -Force
Copy-Item -LiteralPath (Join-Path $root "hardware\pcb\electrical_netlist.csv") -Destination (Join-Path $out "electrical_netlist.csv") -Force
Copy-Item -LiteralPath (Join-Path $root "hardware\pcb\positions.csv") -Destination (Join-Path $out "design-positions.csv") -Force
Copy-Item -LiteralPath (Join-Path $root "hardware\pcb\PRODUCTION_STATUS.md") -Destination (Join-Path $out "PRODUCTION_STATUS.md") -Force
Copy-Item -LiteralPath (Join-Path $root "hardware\pcb\CONNECTIONS.md") -Destination (Join-Path $out "CONNECTIONS.md") -Force
Copy-Item -LiteralPath (Join-Path $root "hardware\wiring\HARNESS_SPEC.md") -Destination (Join-Path $out "HARNESS_SPEC.md") -Force

$zip = Join-Path $out "carrier-gerber.zip"
if (Test-Path -LiteralPath $zip) { Remove-Item -LiteralPath $zip -Force }
Compress-Archive -LiteralPath (Get-ChildItem -LiteralPath $gerber | ForEach-Object FullName) -DestinationPath $zip

$checksumPath = Join-Path $out "SHA256SUMS.txt"
$files = Get-ChildItem -LiteralPath $out -Recurse -File |
    Where-Object { $_.FullName -ne $checksumPath } |
    Sort-Object FullName
$checksums = foreach ($file in $files) {
    $relative = $file.FullName.Substring($out.Length).TrimStart([char[]]@('\', '/')).Replace('\', '/')
    $hash = (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
    "$hash  $relative"
}
[IO.File]::WriteAllLines($checksumPath, $checksums, [Text.UTF8Encoding]::new($false))

Write-Host "Verified prototype package written to hardware/rendered/pcb; production remains blocked by PRODUCTION_STATUS.md"
