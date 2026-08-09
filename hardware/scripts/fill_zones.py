#!/usr/bin/env python3
"""Fill and save all PCB copper zones using KiCad's pcbnew engine."""

from __future__ import annotations

import pathlib
import sys

import pcbnew


def main() -> None:
    if len(sys.argv) != 2:
        raise SystemExit("usage: fill_zones.py BOARD.kicad_pcb")
    path = pathlib.Path(sys.argv[1]).resolve()
    board = pcbnew.LoadBoard(str(path))
    pcbnew.ZONE_FILLER(board).Fill(board.Zones())
    if not pcbnew.SaveBoard(str(path), board):
        raise SystemExit(f"failed to save {path}")
    library = path.parent / "QuotaMonitor.pretty"
    plugin = pcbnew.PCB_IO_KICAD_SEXPR()
    if not library.exists():
        plugin.CreateLibrary(str(library))
    for stale in library.glob("*.kicad_mod"):
        stale.unlink()
    for footprint in board.GetFootprints():
        name = footprint.GetFPID().GetUniStringLibItemName()
        module = pcbnew.FOOTPRINT(footprint)
        module.SetPosition(pcbnew.VECTOR2I(0, 0))
        module.SetOrientationDegrees(0)
        module.ClearAllNets()
        module.SetReference("REF**")
        module.SetValue(name)
        module.SetFPIDAsString(f"QuotaMonitor:{name}")
        plugin.FootprintSave(str(library), module)
    print(f"filled zones in {path}")
    print(f"exported {len(list(library.glob('*.kicad_mod')))} project footprints to {library}")


if __name__ == "__main__":
    main()
