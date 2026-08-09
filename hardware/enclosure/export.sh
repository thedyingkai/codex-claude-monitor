#!/usr/bin/env sh
set -eu
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
out=$(CDPATH= cd -- "$here/.." && pwd)/rendered/enclosure
openscad_image='openscad/openscad@sha256:147e48525bec392bcf628d7a6d5ea4ccac71b16251952328f86e1061cbf47c37'
mkdir -p "$out"
for item in base:1 lid:2 button:3 switch:4; do
  part=${item%:*}
  part_id=${item#*:}
  if command -v openscad >/dev/null 2>&1; then
    openscad -D "part_id=$part_id" -o "$out/quota_display_${part}.stl" "$here/quota_display.scad"
  else
    repo=$(CDPATH= cd -- "$here/../.." && pwd)
    docker run --rm -v "$repo:/work" -w /work/hardware/enclosure \
      "$openscad_image" openscad -D "part_id=$part_id" \
      -o "/work/hardware/rendered/enclosure/quota_display_${part}.stl" quota_display.scad
  fi
done
printf '%s\n' "STL files written to $out"
