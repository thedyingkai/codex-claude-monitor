#!/usr/bin/env python3
"""Compare the generated KiCad SMD PnP export with design positions.csv."""

from __future__ import annotations

import csv
import pathlib
import sys


def fail(message: str) -> None:
    raise SystemExit(f"PnP mismatch: {message}")


def main() -> None:
    if len(sys.argv) != 3:
        raise SystemExit("usage: verify_pnp.py positions.csv carrier-pos.csv")
    design_path = pathlib.Path(sys.argv[1]).resolve()
    export_path = pathlib.Path(sys.argv[2]).resolve()
    with design_path.open(encoding="utf-8-sig", newline="") as stream:
        design = {row["Ref"]: row for row in csv.DictReader(stream)}
    with export_path.open(encoding="utf-8-sig", newline="") as stream:
        exported = {row["Ref"]: row for row in csv.DictReader(stream)}
    if set(design) != set(exported):
        fail(f"reference sets differ: design-only={sorted(set(design)-set(exported))}, "
             f"export-only={sorted(set(exported)-set(design))}")
    for ref in sorted(design):
        expected, actual = design[ref], exported[ref]
        if expected["Value"] != actual["Val"]:
            fail(f"{ref} value {actual['Val']!r} != {expected['Value']!r}")
        if expected["Package"] != actual["Package"]:
            fail(f"{ref} package {actual['Package']!r} != {expected['Package']!r}")
        numeric = [
            ("PosX", float(expected["PosX_mm"]), float(actual["PosX"])),
            # KiCad's CSV PnP uses an upward-positive Cartesian Y axis.
            ("PosY", -float(expected["PosY_mm"]), float(actual["PosY"])),
        ]
        for field, wanted, got in numeric:
            if abs(wanted - got) > 0.005:
                fail(f"{ref} {field} {got:.3f} != {wanted:.3f}")
        # KiCad may serialize the same placement as either -90 or 270 degrees.
        wanted_rotation = float(expected["Rotation_deg"]) % 360.0
        got_rotation = float(actual["Rot"]) % 360.0
        delta = abs((got_rotation - wanted_rotation + 180.0) % 360.0 - 180.0)
        if delta > 0.005:
            fail(f"{ref} Rot {float(actual['Rot']):.3f} != "
                 f"{float(expected['Rotation_deg']):.3f} modulo 360")
        if actual["Side"].lower() != expected["Side"].lower():
            fail(f"{ref} side {actual['Side']!r} != {expected['Side']!r}")
    print(f"PnP OK: {len(design)} SMD placements match board-generated CSV")


if __name__ == "__main__":
    main()
