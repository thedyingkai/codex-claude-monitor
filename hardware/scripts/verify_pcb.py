#!/usr/bin/env python3
"""Fail-fast electrical and mechanical invariants for the carrier PCB.

Run inside the pinned KiCad 9 container so the check uses the same parser as
DRC and manufacturing export.
"""

from __future__ import annotations

import csv
import pathlib
import sys

import pcbnew


EXPECTED = {
    ("J1", "1"): "BAT_RAW",
    ("J1", "2"): "GND",
    ("J2", "1"): "VBAT_SAFE",
    ("J2", "2"): "GND",
    ("F1", "1"): "BAT_RAW",
    ("F1", "2"): "BAT_FUSED",
    ("Q1", "1"): "FET_COMMON",
    ("Q1", "2"): "PROT_GATE",
    ("Q1", "3"): "FET_COMMON",
    ("Q1", "4"): "PROT_GATE",
    ("Q1", "5"): "VBAT_SAFE",
    ("Q1", "6"): "VBAT_SAFE",
    ("Q1", "7"): "BAT_FUSED",
    ("Q1", "8"): "BAT_FUSED",
    ("U3", "1"): "PROT_GATE",
    ("U3", "2"): "VBAT_SAFE",
    ("U3", "3"): "",
    ("U3", "4"): "PROT_SHDN",
    ("U3", "5"): "BAT_FUSED",
    ("U3", "6"): "PROT_UV",
    ("U3", "7"): "GND",
    ("U3", "8"): "GND",
    ("U2", "1"): "DISP_3V3",
    ("U2", "2"): "GND",
    ("U2", "3"): "",
    ("U2", "4"): "LCD_POWER_EN",
    ("U2", "5"): "3V3",
    ("U1", "1"): "GND",
    ("U1", "2"): "VBAT_SAFE",  # CELL: safety-critical
    ("U1", "3"): "VBAT_SAFE",  # VDD: safety-critical
    ("U1", "4"): "GND",
    ("U1", "5"): "ALERT",
    ("U1", "6"): "GND",
    ("U1", "7"): "SCL",
    ("U1", "8"): "SDA",
    ("U1", "9"): "GND",
    ("SW1", "1"): "GND",
    ("SW1", "2"): "POWER_SW",
    ("SW1", "3"): "",
    ("SW2", "1"): "BTN_A",
    ("SW2", "2"): "GND",
    ("SW2", "3"): "BTN_A",
    ("SW2", "4"): "GND",
    ("SW3", "1"): "BTN_B",
    ("SW3", "2"): "GND",
    ("SW3", "3"): "BTN_B",
    ("SW3", "4"): "GND",
}

EXPECTED_POSITIONS = {
    "J1": (5.0, 18.0), "J2": (12.0, 41.0), "Q1": (18.0, 8.0),
    "U3": (26.0, 11.0), "U2": (22.0, 29.0), "U1": (58.0, 28.0),
    "SW1": (77.0, 34.0), "SW2": (79.0, 42.0),
    "SW3": (79.0, 51.0), "J5": (74.0, 7.0), "H4": (70.0, 55.0),
}

EXPECTED_PTS850_PAD_CENTERS = {
    "SW2": {
        "1": (77.3, 39.1), "2": (80.7, 39.1),
        "3": (77.3, 44.9), "4": (80.7, 44.9),
    },
    "SW3": {
        "1": (77.3, 48.1), "2": (80.7, 48.1),
        "3": (77.3, 53.9), "4": (80.7, 53.9),
    },
}

EXPECTED_PTS850_BOSS_CENTERS = {
    "SW2": {(77.5, 42.0), (80.5, 42.0)},
    "SW3": {(77.5, 51.0), (80.5, 51.0)},
}

GDI = [
    "3V3", "LCD_BL", "GND", "SCLK", "MOSI", "MISO", "LCD_DC",
    "LCD_RST", "LCD_CS", "SD_CS", "LCD_POWER_EN", "TOUCH_CS", "SCL",
    "SDA", "INT", "BUSY", "X1", "X2",
]


def fail(message: str) -> None:
    raise SystemExit(f"PCB invariant failed: {message}")


def main() -> None:
    if len(sys.argv) != 2:
        raise SystemExit("usage: verify_pcb.py BOARD.kicad_pcb")
    path = pathlib.Path(sys.argv[1]).resolve()
    board = pcbnew.LoadBoard(str(path))
    pads: dict[tuple[str, str], str] = {}
    pad_objects: dict[tuple[str, str], pcbnew.PAD] = {}
    footprints = {fp.GetReference(): fp for fp in board.GetFootprints()}
    for footprint in board.GetFootprints():
        ref = footprint.GetReference()
        for pad in footprint.Pads():
            number = pad.GetNumber()
            if number:
                pads[(ref, number)] = pad.GetNetname()
                pad_objects[(ref, number)] = pad

    for endpoint, expected in EXPECTED.items():
        actual = pads.get(endpoint)
        if actual != expected:
            fail(f"{endpoint[0]}.{endpoint[1]} expected {expected!r}, got {actual!r}")

    for index, input_net in enumerate(GDI, start=1):
        output_net = "DISP_3V3" if index == 1 else input_net
        for ref, expected in (("J3", input_net), ("J4", output_net)):
            actual = pads.get((ref, str(index)))
            if actual != expected:
                fail(f"{ref}.{index} expected {expected!r}, got {actual!r}")

    def mm(value: int) -> float:
        return pcbnew.ToMM(value)

    def check_pad(ref: str, number: str, sx: float, sy: float,
                  drill: float | None = None) -> None:
        pad = pad_objects.get((ref, number))
        if pad is None:
            fail(f"missing pad {ref}.{number}")
        actual = (mm(pad.GetSizeX()), mm(pad.GetSizeY()))
        if abs(actual[0] - sx) > 0.005 or abs(actual[1] - sy) > 0.005:
            fail(f"{ref}.{number} pad is {actual[0]:.3f} x {actual[1]:.3f} mm, "
                 f"expected {sx:.3f} x {sy:.3f} mm")
        if drill is not None:
            actual_drill = mm(pad.GetDrillSizeX())
            if abs(actual_drill - drill) > 0.005:
                fail(f"{ref}.{number} drill is {actual_drill:.3f} mm, "
                     f"expected {drill:.3f} mm")

    check_pad("J1", "1", 1.00, 3.50)
    check_pad("J2", "1", 1.00, 3.50)
    check_pad("F1", "1", 1.00, 1.60)
    check_pad("C7", "1", 0.950, 1.100)
    check_pad("Q1", "1", 0.559, 0.406)
    check_pad("U3", "1", 1.325, 0.500)
    check_pad("U2", "1", 0.550, 0.800)
    check_pad("U1", "1", 0.775, 0.250)
    check_pad("U1", "9", 0.800, 1.200)
    check_pad("SW1", "1", 0.700, 1.500)
    for ref in ("SW2", "SW3"):
        for number in ("1", "2", "3", "4"):
            check_pad(ref, number, 1.400, 1.500)
    check_pad("TP1", "1", 1.000, 1.500)
    for number in range(1, 8):
        check_pad("J5", str(number), 1.800, 1.800, 1.020)

    for ref, (expected_x, expected_y) in EXPECTED_POSITIONS.items():
        fp = footprints.get(ref)
        if fp is None:
            fail(f"missing footprint {ref}")
        pos = fp.GetPosition()
        actual_x, actual_y = mm(pos.x), mm(pos.y)
        if abs(actual_x - expected_x) > 0.005 or abs(actual_y - expected_y) > 0.005:
            fail(f"{ref} at {actual_x:.3f},{actual_y:.3f}; expected "
                 f"{expected_x:.3f},{expected_y:.3f}")

    sw1 = footprints["SW1"]
    sw1_rotation = sw1.GetOrientation().AsDegrees() % 360.0
    if abs(sw1_rotation - 270.0) > 0.005:
        fail(f"SW1 orientation is {sw1_rotation:.3f} deg; expected 270 deg "
             "so its actuator faces the +X enclosure wall")
    expected_sw1_centers = {
        "1": (78.43, 31.75),
        "2": (78.43, 34.75),
        "3": (78.43, 36.25),
    }
    for number, expected in expected_sw1_centers.items():
        pos = pad_objects[("SW1", number)].GetPosition()
        actual = (mm(pos.x), mm(pos.y))
        if any(abs(a - e) > 0.005 for a, e in zip(actual, expected)):
            fail(f"SW1.{number} center is {actual[0]:.3f},{actual[1]:.3f}; "
                 f"expected {expected[0]:.3f},{expected[1]:.3f}")

    for ref, centers in EXPECTED_PTS850_PAD_CENTERS.items():
        fp = footprints[ref]
        package = fp.GetFPID().GetUniStringLibItemName()
        if package != "C&K_PTS850_with_boss_actuator_plusX":
            fail(f"{ref} package is {package!r}; expected the audited PTS850 "
                 "with-boss +X-actuator footprint")
        if abs(fp.GetOrientation().AsDegrees() % 360.0) > 0.005:
            fail(f"{ref} rotation must remain 0 deg so its actuator faces +X")
        for number, expected in centers.items():
            pos = pad_objects[(ref, number)].GetPosition()
            actual = (mm(pos.x), mm(pos.y))
            if any(abs(a - e) > 0.005 for a, e in zip(actual, expected)):
                fail(f"{ref}.{number} center is {actual[0]:.3f},{actual[1]:.3f}; "
                     f"expected {expected[0]:.3f},{expected[1]:.3f}")
        bosses = {
            (round(mm(pad.GetPosition().x), 3),
             round(mm(pad.GetPosition().y), 3))
            for pad in fp.Pads()
            if not pad.GetNumber() and abs(mm(pad.GetDrillSizeX()) - 0.9) <= 0.005
        }
        if bosses != EXPECTED_PTS850_BOSS_CENTERS[ref]:
            fail(f"{ref} boss centers are {sorted(bosses)}; expected "
                 f"{sorted(EXPECTED_PTS850_BOSS_CENTERS[ref])}")

    bom_path = path.parent / "bom.csv"
    positions_path = path.parent / "positions.csv"
    with bom_path.open(encoding="utf-8-sig", newline="") as stream:
        bom_rows = list(csv.DictReader(stream))
    bom_refs: list[str] = []
    for row in bom_rows:
        bom_refs.extend(row["References"].split())
    if len(bom_refs) != len(set(bom_refs)):
        fail("bom.csv contains duplicate references")
    board_refs = set(footprints) - {"H1", "H2", "H3", "H4"}
    if set(bom_refs) != board_refs:
        fail(f"bom.csv refs differ from board: missing={sorted(board_refs-set(bom_refs))}, "
             f"extra={sorted(set(bom_refs)-board_refs)}")

    with positions_path.open(encoding="utf-8-sig", newline="") as stream:
        position_rows = list(csv.DictReader(stream))
    position_refs = [row["Ref"] for row in position_rows]
    if len(position_refs) != len(set(position_refs)):
        fail("positions.csv contains duplicate references")
    smd_refs = set()
    for ref, fp in footprints.items():
        if any(mm(pad.GetDrillSizeX()) == 0 and mm(pad.GetSizeX()) > 0
               for pad in fp.Pads()):
            smd_refs.add(ref)
    if set(position_refs) != smd_refs:
        fail(f"positions.csv refs differ from SMD board refs: "
             f"missing={sorted(smd_refs-set(position_refs))}, "
             f"extra={sorted(set(position_refs)-smd_refs)}")
    for row in position_rows:
        ref = row["Ref"]
        fp = footprints[ref]
        pos = fp.GetPosition()
        expected_package = fp.GetFPID().GetUniStringLibItemName()
        actual = (float(row["PosX_mm"]), float(row["PosY_mm"]))
        if abs(actual[0] - mm(pos.x)) > 0.005 or abs(actual[1] - mm(pos.y)) > 0.005:
            fail(f"positions.csv coordinate mismatch for {ref}")
        if row["Value"] != fp.GetValue():
            fail(f"positions.csv value mismatch for {ref}: {row['Value']!r} "
                 f"!= {fp.GetValue()!r}")
        if row["Package"] != expected_package:
            fail(f"positions.csv package mismatch for {ref}: {row['Package']!r} "
                 f"!= {expected_package!r}")

    box = board.GetBoardEdgesBoundingBox()
    width = pcbnew.ToMM(box.GetWidth())
    height = pcbnew.ToMM(box.GetHeight())
    thickness = pcbnew.ToMM(board.GetDesignSettings().GetBoardThickness())
    # KiCad includes half of the 0.15 mm Edge.Cuts stroke at each side.
    if abs(width - 84.15) > 0.01 or abs(height - 58.15) > 0.01:
        fail(f"edge bounding box is {width:.3f} x {height:.3f} mm, "
             "expected 84.15 x 58.15 mm for an 84 x 58 mm nominal outline")
    if abs(thickness - 1.0) > 0.01:
        fail(f"thickness is {thickness:.3f} mm, expected 1.0 mm")
    if board.GetCopperLayerCount() != 2:
        fail(f"copper layer count is {board.GetCopperLayerCount()}, expected 2")

    front_silk = {
        item.GetText() for item in board.GetDrawings()
        if hasattr(item, "GetText") and board.GetLayerName(item.GetLayer()) == "F.Silkscreen"
    }
    required_silk = {
        "QM R0.1 PROTOTYPE", "J1 BAT  +  -", "J2 SYS  +  -",
        "GDI IN  PIN1<", "GDI OUT PIN1<", "J5 AUX DNP", "PWR", "A", "B",
    }
    if not required_silk.issubset(front_silk):
        fail(f"missing required safety/assembly silkscreen: "
             f"{sorted(required_silk-front_silk)}")

    print("PCB invariants OK")
    print("  outline: 84.00 x 58.00 mm")
    print("  stack: 2 copper layers, 1.00 mm")
    print("  protection: LTC4365 + Si5908 dual NFET endpoints verified")
    print("  MAX17048: CELL=VBAT_SAFE, VDD=VBAT_SAFE, CTG/QSTRT/GND/EP=GND")
    print("  exact pads: JST, PTC, Si5908, LTC4365, AP22804, MAX17048, Murata GRM21, switches, TP, J5")
    print("  PCM12: rotation=270 deg; pad 1 GND and pad 2 POWER_SW centers verified")
    print("  PTS850: +X actuator; pads 1/3 signal, 2/4 GND; 1.4 x 1.5 mm pads and 0.9 mm bosses verified")
    print("  silkscreen: battery polarity, GDI pin 1, J5 DNP and controls verified")
    print("  BOM/positions: board reference, value, package and coordinate sets match")
    print("  GDI: 18 input/output pins verified; pin 1 gated, pins 2..18 matched")


if __name__ == "__main__":
    main()
