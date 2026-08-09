#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUT="$ROOT/hardware/rendered/pcb"
GERBER="$OUT/gerber"
BOARD=/work/hardware/pcb/carrier.kicad_pcb
SCHEMATIC=/work/hardware/pcb/carrier.kicad_sch

python3 "$ROOT/hardware/scripts/generate_kicad.py"
mkdir -p "$OUT"
if [[ -d "$GERBER" ]]; then
  expected="$ROOT/hardware/rendered/pcb/gerber"
  [[ "$GERBER" == "$expected" ]] || { echo "refusing to clean unexpected path" >&2; exit 1; }
  rm -rf -- "$GERBER"
fi
mkdir -p "$GERBER"

kicad() {
  docker run --rm -v "$ROOT:/work" -w /work kicad/kicad:9.0 kicad-cli "$@"
}

docker run --rm -v "$ROOT:/work" -w /work kicad/kicad:9.0 \
  python3 /work/hardware/scripts/verify_pcb.py "$BOARD"

kicad sch erc --exit-code-violations --severity-all \
  --output /work/hardware/rendered/pcb/erc.rpt "$SCHEMATIC"
kicad sch export netlist --format kicadxml \
  --output /work/hardware/rendered/pcb/carrier-netlist.xml "$SCHEMATIC"
docker run --rm -v "$ROOT:/work" -w /work kicad/kicad:9.0 \
  python3 /work/hardware/scripts/verify_schematic_netlist.py \
  "$BOARD" /work/hardware/rendered/pcb/carrier-netlist.xml

kicad pcb drc --exit-code-violations --severity-all \
  --output /work/hardware/rendered/pcb/drc.rpt "$BOARD"
kicad pcb export gerbers --precision 6 \
  --layers F.Cu,B.Cu,F.Paste,B.Paste,F.SilkS,B.SilkS,F.Mask,B.Mask,Edge.Cuts \
  --output /work/hardware/rendered/pcb/gerber/ "$BOARD"
kicad pcb export drill --format excellon --excellon-units mm \
  --excellon-separate-th --generate-map --map-format pdf --generate-report \
  --report-path /work/hardware/rendered/pcb/drill-report.txt \
  --output /work/hardware/rendered/pcb/gerber/ "$BOARD"
kicad pcb export pos --format csv --units mm --side both --smd-only \
  --output /work/hardware/rendered/pcb/carrier-pos.csv "$BOARD"
docker run --rm -v "$ROOT:/work" -w /work kicad/kicad:9.0 \
  python3 /work/hardware/scripts/verify_pnp.py \
  /work/hardware/pcb/positions.csv /work/hardware/rendered/pcb/carrier-pos.csv
kicad pcb export pdf --mode-multipage --black-and-white \
  --sketch-pads-on-fab-layers --layers F.Cu,B.Cu,F.Mask,B.Mask,F.Fab,Edge.Cuts \
  --output /work/hardware/rendered/pcb/layers/ "$BOARD"
rm -rf -- "$OUT/carrier-layers.pdf"
mv "$OUT/layers/carrier.pdf" "$OUT/carrier-layers.pdf"
rmdir "$OUT/layers"
kicad pcb render --side top --quality high --width 1800 --height 1200 \
  --background opaque --output /work/hardware/rendered/pcb/carrier-top.png "$BOARD"
kicad pcb render --side bottom --quality high --width 1800 --height 1200 \
  --background opaque --output /work/hardware/rendered/pcb/carrier-bottom.png "$BOARD"
kicad sch export pdf --output /work/hardware/rendered/pcb/carrier-connections.pdf "$SCHEMATIC"

cp "$ROOT/hardware/pcb/bom.csv" "$OUT/bom.csv"
cp "$ROOT/hardware/pcb/system_bom.csv" "$OUT/system_bom.csv"
cp "$ROOT/hardware/pcb/electrical_netlist.csv" "$OUT/electrical_netlist.csv"
cp "$ROOT/hardware/pcb/positions.csv" "$OUT/design-positions.csv"
cp "$ROOT/hardware/pcb/PRODUCTION_STATUS.md" "$OUT/PRODUCTION_STATUS.md"
cp "$ROOT/hardware/pcb/CONNECTIONS.md" "$OUT/CONNECTIONS.md"
cp "$ROOT/hardware/wiring/HARNESS_SPEC.md" "$OUT/HARNESS_SPEC.md"

rm -f -- "$OUT/carrier-gerber.zip" "$OUT/SHA256SUMS.txt"
(cd "$GERBER" && zip -q "$OUT/carrier-gerber.zip" ./*)
(cd "$OUT" && find . -type f ! -name SHA256SUMS.txt -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS.txt)

echo "Verified prototype package written to hardware/rendered/pcb; production remains blocked by PRODUCTION_STATUS.md"
