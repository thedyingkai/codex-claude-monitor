#!/usr/bin/env python3
"""Generate the deterministic KiCad 9 carrier board and connection drawing.

The PCB is deliberately self-contained.  Footprints, pads, nets, tracks and
the outline are embedded in the board file, so it does not rely on a KiCad
library table.  A KiCad 9 zone fill is performed after generation because the
official CLI DRC checks the saved fill, rather than refilling zones itself.
"""

from __future__ import annotations

import argparse
import csv
import pathlib
import shutil
import subprocess
import sys
import uuid


ROOT = pathlib.Path(__file__).resolve().parents[1]
REPO = ROOT.parent
PCB_DIR = ROOT / "pcb"
PCB_DIR.mkdir(parents=True, exist_ok=True)
BOARD_PATH = PCB_DIR / "carrier.kicad_pcb"
NS = uuid.UUID("888a10db-38d6-5a7c-a31c-5962b9f2ab65")


def uid(name: str) -> str:
    return str(uuid.uuid5(NS, name))


NETS = [
    "", "GND", "BAT_RAW", "BAT_FUSED", "VBAT_SAFE", "3V3", "DISP_3V3",
    "LCD_BL", "SCLK", "MOSI", "MISO", "LCD_DC", "LCD_RST", "LCD_CS",
    "SD_CS", "LCD_POWER_EN", "TOUCH_CS", "SCL", "SDA", "INT", "BUSY",
    "X1", "X2", "BTN_A", "BTN_B", "POWER_SW", "USB_5V", "USB_SENSE",
    "ALERT", "FET_COMMON", "PROT_GATE", "PROT_SHDN", "PROT_UV",
]
NET_ID = {name: index for index, name in enumerate(NETS)}


def effects(size: float = 1.0) -> str:
    return f'(effects (font (size {size} {size}) (thickness 0.15)))'


def board_text(text: str, x: float, y: float, size: float = 0.8,
               layer: str = "F.SilkS", justify: str = "") -> str:
    justify_text = f" (justify {justify})" if justify else ""
    return (
        f'  (gr_text "{text}" (at {x:.3f} {y:.3f}) (layer "{layer}") '
        f'(uuid "{uid(f"board-text-{layer}-{text}-{x}-{y}")}") '
        f'(effects (font (size {size:.3f} {size:.3f}) (thickness 0.120))'
        f'{justify_text}))'
    )


def pad_expr(ref: str, num: str, pad_type: str, shape: str, x: float, y: float,
             sx: float, sy: float, net: str, function: str = "",
             drill: float | None = None, layers: str | None = None,
             angle: float = 0.0) -> str:
    if layers is None:
        layers = ('"*.Cu" "*.Mask"' if pad_type == "thru_hole"
                  else '"F.Cu" "F.Paste" "F.Mask"')
    drill_text = f" (drill {drill})" if drill is not None else ""
    net_text = f' (net {NET_ID[net]} "{net}")' if net else ""
    pin_text = (f' (pinfunction "{function}") (pintype "passive")'
                if function else "")
    angle_text = f" {angle:.3f}" if angle else ""
    return (f'    (pad "{num}" {pad_type} {shape} '
            f'(at {x:.3f} {y:.3f}{angle_text}) '
            f'(size {sx:.3f} {sy:.3f}){drill_text} (layers {layers})'
            f'{net_text}{pin_text} '
            f'(uuid "{uid(f"{ref}-pad-{num}-{x}-{y}")}"))')


def footprint(ref: str, value: str, x: float, y: float, pads: list[dict],
              width: float, height: float, package: str,
              fab: bool = True, fab_offset_y: float = 0.0,
              rotation: float = 0.0,
              extra_graphics: list[str] | None = None) -> str:
    # A name without a library prefix is intentional.  These are embedded
    # project footprints; using "QuotaMonitor:..." causes a library warning on
    # clean machines and makes --severity-all DRC non-zero.
    lines = [
        f'  (footprint "{package}" (layer "F.Cu")',
        f'    (uuid "{uid(ref)}")',
        f'    (at {x:.3f} {y:.3f}'
        f'{f" {rotation:.3f}" if rotation else ""})',
        f'    (property "Reference" "{ref}" (at 0 {-height/2-1.0:.3f} 0) '
        f'(layer "F.Fab") (uuid "{uid(ref+"-ref")}") {effects(0.7)})',
        f'    (property "Value" "{value}" (at 0 {height/2+1.0:.3f} 0) '
        f'(layer "F.Fab") hide (uuid "{uid(ref+"-value")}") {effects(0.7)})',
        f'    (property "Datasheet" "" (at 0 0 0) (layer "F.Fab") hide '
        f'(uuid "{uid(ref+"-datasheet")}") {effects(1.27)})',
        f'    (property "Description" "" (at 0 0 0) (layer "F.Fab") hide '
        f'(uuid "{uid(ref+"-description")}") {effects(1.27)})',
    ]
    if fab:
        lines += [
            f'    (fp_rect (start {-width/2:.3f} {fab_offset_y-height/2:.3f}) '
            f'(end {width/2:.3f} {fab_offset_y+height/2:.3f}) '
            f'(stroke (width 0.10) (type default)) (fill none) '
            f'(layer "F.Fab") (uuid "{uid(ref+"-outline")}"))',
            f'    (fp_circle (center {-width/2+0.35:.3f} {fab_offset_y-height/2+0.35:.3f}) '
            f'(end {-width/2+0.55:.3f} {fab_offset_y-height/2+0.35:.3f}) '
            f'(stroke (width 0.10) (type default)) (fill solid) '
            f'(layer "F.Fab") (uuid "{uid(ref+"-pin1")}"))',
        ]
    if extra_graphics:
        lines.extend(extra_graphics)
    copper_pad_types = {pad["pad_type"] for pad in pads
                        if pad["pad_type"] != "np_thru_hole"}
    if copper_pad_types and copper_pad_types == {"smd"}:
        lines.append("    (attr smd)")
    elif "thru_hole" in copper_pad_types:
        lines.append("    (attr through_hole)")
    lines.extend(pad_expr(ref=ref, **pad) for pad in pads)
    lines.append("  )")
    return "\n".join(lines)


def th(num: int | str, x: float, y: float, net: str, function: str,
       size: float = 1.8, drill: float = 1.0) -> dict:
    return dict(num=str(num), pad_type="thru_hole",
                shape="rect" if str(num) == "1" else "circle",
                x=x, y=y, sx=size, sy=size, drill=drill,
                net=net, function=function)


def smd(num: int | str, x: float, y: float, sx: float, sy: float,
        net: str, function: str, angle: float = 0.0) -> dict:
    return dict(num=str(num), pad_type="smd", shape="rect", x=x, y=y,
                sx=sx, sy=sy, drill=None, net=net, function=function,
                angle=angle)


def npth(x: float, y: float, diameter: float) -> dict:
    return dict(num="", pad_type="np_thru_hole", shape="circle", x=x, y=y,
                sx=diameter, sy=diameter, drill=diameter, net="", function="",
                layers='"*.Cu" "*.Mask"')


def two_pad(ref: str, value: str, x: float, y: float, net1: str, net2: str,
            package: str = "R_0603") -> str:
    return footprint(
        ref, value, x, y,
        [smd(1, -0.9, 0, 0.8, 0.8, net1, "1"),
         smd(2, 0.9, 0, 0.8, 0.8, net2, "2")],
        2.8, 1.4, package,
    )


def two_pad_vertical(ref: str, value: str, x: float, y: float,
                     net1: str, net2: str,
                     package: str = "C_0603") -> str:
    return footprint(
        ref, value, x, y,
        [smd(1, 0, -0.9, 0.8, 0.8, net1, "1"),
         smd(2, 0, 0.9, 0.8, 0.8, net2, "2")],
        1.4, 2.8, package,
    )


def capacitor_0805_vertical(ref: str, value: str, x: float, y: float,
                            net1: str, net2: str) -> str:
    """Murata GRM21 reflow land at the centre of the published a/b/c ranges.

    Rotated 90 degrees: a=1.10 mm pad length, b=0.95 mm gap and c=0.95 mm
    pad width. Murata specifies a=1.0..1.2, b=0.9..1.0 and c=0.8..1.1 mm.
    """
    return footprint(
        ref, value, x, y,
        [smd(1, 0, -1.025, 0.95, 1.10, net1, "1"),
         smd(2, 0, 1.025, 0.95, 1.10, net2, "2")],
        1.25, 2.0, "Murata_GRM21_Reflow_a1.10_b0.95_c0.95",
    )


def route(name: str, layer: str, points: list[tuple[float, float]],
          width: float = 0.20, label: str = "") -> list[str]:
    result: list[str] = []
    for index, (start, end) in enumerate(zip(points, points[1:])):
        if start == end:
            continue
        route_id = f"route-{label}-{name}-{layer}-{points}-{index}"
        result.append(
            f'  (segment (start {start[0]:.3f} {start[1]:.3f}) '
            f'(end {end[0]:.3f} {end[1]:.3f}) (width {width:.3f}) '
            f'(layer "{layer}") (net {NET_ID[name]}) '
            f'(uuid "{uid(route_id)}"))'
        )
    return result


def via(name: str, x: float, y: float, label: str = "",
        size: float = 0.700, drill: float = 0.350) -> str:
    return (f'  (via (at {x:.3f} {y:.3f}) (size {size:.3f}) (drill {drill:.3f}) '
            f'(layers "F.Cu" "B.Cu") (net {NET_ID[name]}) '
            f'(uuid "{uid(f"via-{label}-{name}-{x}-{y}")}"))')


def fpc(ref: str, y: float) -> str:
    gdi = ["3V3", "LCD_BL", "GND", "SCLK", "MOSI", "MISO", "LCD_DC",
           "LCD_RST", "LCD_CS", "SD_CS", "LCD_POWER_EN", "TOUCH_CS",
           "SCL", "SDA", "INT", "BUSY", "X1", "X2"]
    if ref == "J4":
        gdi[0] = "DISP_3V3"
    # Hirose's FH12 land pattern: 0.30 x 1.30 mm contacts on 0.50 mm
    # pitch, with 1.80 x 2.20 mm hold-down lands.  The footprint origin is
    # deliberately the contact row so routed GDI coordinates stay obvious.
    pads = [smd(index + 1, -4.25 + index * 0.5, 0, 0.30, 1.30,
                net, net) for index, net in enumerate(gdi)]
    pads += [smd("MP", -6.15, 3.25, 1.80, 2.20, "", "MOUNT"),
             smd("MP", 6.15, 3.25, 1.80, 2.20, "", "MOUNT")]
    return footprint(ref, "FH12-18S-0.5SH(55)", 34.25, y, pads,
                     12.1, 5.6,
                     "Hirose_FH12-18S-0.5SH_1x18_P0.50mm", True, 3.45)


def jst_ph(ref: str, value: str, x: float, y: float,
           positive_net: str) -> str:
    """Official KiCad/JST S2B-PH-SM4-TB horizontal land pattern."""
    pads = [
        smd(1, -1.00, 0.00, 1.00, 3.50, positive_net, "+"),
        smd(2, 1.00, 0.00, 1.00, 3.50, "GND", "-"),
        smd("MP", -3.35, 5.75, 1.50, 3.40, "", "MOUNT"),
        smd("MP", 3.35, 5.75, 1.50, 3.40, "", "MOUNT"),
    ]
    return footprint(ref, value, x, y, pads, 8.1, 7.7,
                     "JST_PH_S2B-PH-SM4-TB_1x02_P2.00mm_Horizontal",
                     True, 3.45)


def fuse_nsmf150(ref: str, x: float, y: float) -> str:
    # Bourns MF-NSMF150 recommended 1206 land: 1.0 x 1.6 mm lands with
    # 2.0 mm inner gap (3.0 mm centre spacing).
    return footprint(
        ref, "MF-NSMF150-2", x, y,
        [smd(1, -1.50, 0, 1.00, 1.60, "BAT_RAW", "1"),
         smd(2, 1.50, 0, 1.00, 1.60, "BAT_FUSED", "2")],
        3.4, 1.8, "Bourns_MF-NSMF_1206",
    )


def dual_nfet(ref: str, x: float, y: float) -> str:
    """Si5908BDC 1206-8 ChipFET, wired source-to-source/gate-to-gate."""
    # Vishay AN826: 0.559 x 0.406 mm pads, 0.650 mm pitch, 0.914 mm
    # inner row gap.  Official device pins are 1=S1, 2=G1, 3=S2, 4=G2,
    # 5/6=D2 and 7/8=D1.
    xl, xr = -0.7365, 0.7365
    ys = [-0.975, -0.325, 0.325, 0.975]
    return footprint(
        ref, "Si5908BDC-T1-GE3", x, y,
        [
            smd(1, xl, ys[0], 0.559, 0.406, "FET_COMMON", "S1"),
            smd(2, xl, ys[1], 0.559, 0.406, "PROT_GATE", "G1"),
            smd(3, xl, ys[2], 0.559, 0.406, "FET_COMMON", "S2"),
            smd(4, xl, ys[3], 0.559, 0.406, "PROT_GATE", "G2"),
            smd(5, xr, ys[3], 0.559, 0.406, "VBAT_SAFE", "D2"),
            smd(6, xr, ys[2], 0.559, 0.406, "VBAT_SAFE", "D2"),
            smd(7, xr, ys[1], 0.559, 0.406, "BAT_FUSED", "D1"),
            smd(8, xr, ys[0], 0.559, 0.406, "BAT_FUSED", "D1"),
        ],
        3.0, 1.9, "Vishay_1206-8_ChipFET",
    )


def ltc4365(ref: str, x: float, y: float) -> str:
    # TS8 / TSOT-23-8 land pattern from ADI 05-08-1637 and KiCad 9.
    pads = []
    left_y = [-0.975, -0.325, 0.325, 0.975]
    right_y = [0.975, 0.325, -0.325, -0.975]
    nets = {
        1: ("PROT_GATE", "GATE"), 2: ("VBAT_SAFE", "VOUT"),
        3: ("", "FAULT"), 4: ("PROT_SHDN", "SHDN"),
        5: ("BAT_FUSED", "VIN"), 6: ("PROT_UV", "UV"),
        7: ("GND", "OV"), 8: ("GND", "GND"),
    }
    for number, yy in enumerate(left_y, start=1):
        net, name = nets[number]
        pads.append(smd(number, -1.1375, yy, 1.325, 0.50, net, name))
    for number, yy in zip(range(5, 9), right_y):
        net, name = nets[number]
        pads.append(smd(number, 1.1375, yy, 1.325, 0.50, net, name))
    return footprint(ref, "LTC4365ITS8#TRMPBF", x, y, pads,
                     2.9, 2.8, "TSOT-23-8")


def ap22804(ref: str, x: float, y: float) -> str:
    # Diodes SOT-25 recommended land: 0.55 x 0.80 mm, 0.95 mm pitch,
    # 2.40 mm row-centre separation.
    return footprint(
        ref, "AP22804AW5-7", x, y,
        [smd(1, -1.20, -0.95, 0.55, 0.80, "DISP_3V3", "OUT"),
         smd(2, -1.20, 0.00, 0.55, 0.80, "GND", "GND"),
         smd(3, -1.20, 0.95, 0.55, 0.80, "", "FLG"),
         smd(4, 1.20, 0.95, 0.55, 0.80, "LCD_POWER_EN", "EN"),
         smd(5, 1.20, -0.95, 0.55, 0.80, "3V3", "IN")],
        2.9, 2.8, "Diodes_SOT-25_AP22804",
    )


def max17048(ref: str, x: float, y: float) -> str:
    # ADI package T822+3, outline 21-0168, land pattern 90-0065.
    # This matches KiCad's TDFN-8-1EP_2x2mm_P0.5mm_EP0.8x1.2mm.
    pads = [
        smd(1, -0.9875, -0.75, 0.775, 0.25, "GND", "CTG"),
        smd(2, -0.9875, -0.25, 0.775, 0.25, "VBAT_SAFE", "CELL"),
        smd(3, -0.9875, 0.25, 0.775, 0.25, "VBAT_SAFE", "VDD"),
        smd(4, -0.9875, 0.75, 0.775, 0.25, "GND", "GND"),
        smd(5, 0.9875, 0.75, 0.775, 0.25, "ALERT", "ALRT"),
        smd(6, 0.9875, 0.25, 0.775, 0.25, "GND", "QSTRT"),
        smd(7, 0.9875, -0.25, 0.775, 0.25, "SCL", "SCL"),
        smd(8, 0.9875, -0.75, 0.775, 0.25, "SDA", "SDA"),
        smd(9, 0, 0, 0.80, 1.20, "GND", "EP"),
    ]
    return footprint(ref, "MAX17048G+T10", x, y, pads,
                     2.0, 2.0, "ADI_TDFN-8-1EP_2x2_P0.5_EP0.8x1.2")


def pcm12(ref: str, x: float, y: float) -> str:
    # Exact KiCad/C&K PCM12 land pattern, including four shell anchors and
    # two 0.9 mm locating holes.  The electrical pad origin is asymmetric in
    # the manufacturer's drawing and is intentionally preserved.
    pads = [
        smd("", -3.65, -0.78, 1.00, 0.80, "", "MOUNT", 90),
        smd("", -3.65, 1.43, 1.00, 0.80, "", "MOUNT", 90),
        npth(-1.50, 0.33, 0.90), npth(1.50, 0.33, 0.90),
        smd("", 3.65, -0.78, 1.00, 0.80, "", "MOUNT", 90),
        smd("", 3.65, 1.43, 1.00, 0.80, "", "MOUNT", 90),
        smd(1, -2.25, -1.43, 0.70, 1.50, "GND", "OFF", 90),
        smd(2, 0.75, -1.43, 0.70, 1.50, "POWER_SW", "COMMON", 90),
        smd(3, 2.25, -1.43, 0.70, 1.50, "", "ON", 90),
    ]
    # The unrotated datasheet land has the actuator on local -Y and travel
    # along local X. KiCad's 270-degree placement points the actuator toward
    # the carrier's +X edge and makes user motion vertical along Y.
    return footprint(ref, "PCM12SMTR", x, y, pads, 6.7, 2.6,
                     "C&K_PCM12SMTR", rotation=270)


def pts850(ref: str, value: str, x: float, y: float, signal: str) -> str:
    # C&K WITH BOSS piercing plan, canonical actuator +X orientation.
    # The 4.3 mm dimension is the inner-edge gap; with 1.5 mm-high lands the
    # nominal row centres are Y=+/-2.90 mm. Pins 1/3 are the left contact and
    # pins 2/4 the right contact. The free actuator face is X=+4.35 mm.
    pads = [
        smd(1, -1.70, -2.90, 1.40, 1.50, signal, "CONTACT_A"),
        smd(2, 1.70, -2.90, 1.40, 1.50, "GND", "CONTACT_B"),
        smd(3, -1.70, 2.90, 1.40, 1.50, signal, "CONTACT_A"),
        smd(4, 1.70, 2.90, 1.40, 1.50, "GND", "CONTACT_B"),
        npth(-1.50, 0.00, 0.90), npth(1.50, 0.00, 0.90),
    ]
    actuator = [
        f'    (fp_rect (start 2.700 -0.700) (end 4.350 0.700) '
        f'(stroke (width 0.10) (type default)) (fill none) '
        f'(layer "F.Fab") (uuid "{uid(ref+"-actuator")}"))'
    ]
    return footprint(ref, value, x, y, pads, 5.4, 5.0,
                     "C&K_PTS850_with_boss_actuator_plusX",
                     extra_graphics=actuator)


def testpoint_5015(ref: str, x: float, y: float, net: str) -> str:
    """Keystone 5015 micro-miniature SMT loop test point.

    Keystone's catalog drawing calls out a 1.0 x 1.5 mm land.  The formed
    loop body is 3.4 x 1.8 mm; the body dimensions are used for F.Fab only.
    """
    return footprint(
        ref, "Keystone 5015", x, y,
        [smd(1, 0, 0, 1.00, 1.50, net, net)],
        3.4, 1.8, "Keystone_5015_SMT",
    )


def mounting_hole(ref: str, x: float, y: float) -> str:
    pad = dict(num="", pad_type="np_thru_hole", shape="circle", x=0, y=0,
               sx=2.6, sy=2.6, drill=2.6, net="", function="",
               layers='"*.Cu" "*.Mask"')
    return footprint(ref, "M2.5_NPTH", x, y, [pad], 3.0, 3.0,
                     "MountingHole_2.6mm", fab=False)


def build_pcb() -> str:
    parts: list[str] = [
        fpc("J3", 4.5),
        fpc("J4", 50.5),
        jst_ph("J1", "S2B-PH-SM4-TB(LF)(SN)", 5, 18, "BAT_RAW"),
        jst_ph("J2", "S2B-PH-SM4-TB(LF)(SN)", 12, 41, "VBAT_SAFE"),
        fuse_nsmf150("F1", 10, 8),
        dual_nfet("Q1", 18, 8),
        ltc4365("U3", 26, 11),
        two_pad("R5", "100k", 22, 16, "BAT_FUSED", "PROT_SHDN"),
        two_pad("R8", "100k", 28.5, 16, "BAT_FUSED", "PROT_UV"),
        two_pad_vertical("C6", "0.1uF", 13, 13, "BAT_FUSED", "GND"),
        capacitor_0805_vertical("C7", "10uF", 18, 16,
                                "VBAT_SAFE", "GND"),
        ap22804("U2", 22, 29),
        two_pad_vertical("C3", "1uF", 27, 25, "3V3", "GND"),
        two_pad_vertical("C4", "1uF", 18, 33, "DISP_3V3", "GND"),
        two_pad("R6", "100k", 26, 32, "LCD_POWER_EN", "GND"),
        max17048("U1", 58, 28),
        two_pad("C1", "0.1uF", 52, 24, "VBAT_SAFE", "GND", "C_0603"),
        two_pad_vertical("C2", "0.1uF", 54, 31, "VBAT_SAFE", "GND"),
        two_pad("R1", "4.7k", 50, 34, "3V3", "SCL"),
        two_pad("R2", "4.7k", 56, 22, "3V3", "SDA"),
        two_pad("R7", "10k", 68, 34, "3V3", "ALERT"),
        footprint("J5", "TSW-107-07-G-S", 74, 7,
                  [th(1, 0, 0, "BTN_A", "GPIO47", drill=1.02),
                   th(2, 0, 2.54, "BTN_B", "GPIO8", drill=1.02),
                   th(3, 0, 5.08, "POWER_SW", "GPIO5", drill=1.02),
                   th(4, 0, 7.62, "USB_5V", "VCC", drill=1.02),
                   th(5, 0, 10.16, "GND", "GND", drill=1.02),
                   th(6, 0, 12.70, "USB_SENSE", "GPIO6", drill=1.02),
                   th(7, 0, 15.24, "ALERT", "ALERT", drill=1.02)],
                  4, 18.8, "PinHeader_1x07_P2.54mm"),
        pts850("SW2", "PTS850VR18PSMTR LFS", 79, 42, "BTN_A"),
        pts850("SW3", "PTS850VR18PSMTR LFS", 79, 51, "BTN_B"),
        pcm12("SW1", 77, 34),
        two_pad("R3", "100k", 69, 15, "USB_5V", "USB_SENSE"),
        two_pad("R4", "100k", 70, 18, "USB_SENSE", "GND"),
        two_pad("C5", "0.1uF", 70, 21, "USB_SENSE", "GND", "C_0603"),
    ]

    test_points = [
        ("TP1", "VBAT_SAFE", 8, 40), ("TP2", "3V3", 42, 12),
        ("TP3", "GND", 8, 52), ("TP4", "USB_5V", 65, 12),
        ("TP5", "SDA", 47, 25), ("TP6", "SCL", 48, 31),
    ]
    for ref, name, x, y in test_points:
        parts.append(testpoint_5015(ref, x, y, name))
    parts += [mounting_hole("H1", 3, 3), mounting_hole("H2", 81, 3),
              mounting_hole("H3", 3, 55), mounting_hole("H4", 70, 55)]

    tracks: list[str] = []

    # GDI pass-through: pins 2..18 are direct; pin 1 is intentionally gated.
    gdi = ["3V3", "LCD_BL", "GND", "SCLK", "MOSI", "MISO", "LCD_DC",
           "LCD_RST", "LCD_CS", "SD_CS", "LCD_POWER_EN", "TOUCH_CS",
           "SCL", "SDA", "INT", "BUSY", "X1", "X2"]
    for pin, net in enumerate(gdi[1:], start=2):
        x = 30.0 + (pin - 1) * 0.5
        tracks += route(net, "F.Cu", [(x, 4.5), (x, 50.5)], 0.15,
                        f"gdi-{pin}")

    # Bottom fan-out gives branched 0.5-mm-pitch signals room for standard
    # 0.70/0.35-mm vias without violating adjacent GDI clearances.
    tracks += route("GND", "F.Cu", [(31, 50.5), (31, 53.5), (28.5, 56.2)],
                    0.15, "gdi-ground-fanout")
    tracks.append(via("GND", 28.5, 56.2, "gdi-ground"))
    tracks += route("LCD_POWER_EN", "F.Cu",
                    [(35, 50.5), (35, 54.5), (32.5, 56)],
                    0.15, "enable-fanout")
    tracks.append(via("LCD_POWER_EN", 32.5, 56, "enable-bottom"))
    tracks += route("LCD_POWER_EN", "B.Cu",
                    [(32.5, 56), (32.5, 51), (24, 51), (24, 32)],
                    0.20, "enable-back")
    tracks.append(via("LCD_POWER_EN", 24, 32, "enable-u2"))
    tracks += route("LCD_POWER_EN", "F.Cu",
                    [(24, 32), (24, 30), (23.2, 29.95)],
                    0.20, "enable-u2")
    tracks += route("LCD_POWER_EN", "F.Cu",
                    [(24, 32), (25.1, 32)], 0.20, "enable-pulldown")

    tracks += route("SCL", "F.Cu", [(36, 50.5), (36, 54)],
                    0.15, "scl-fanout")
    tracks.append(via("SCL", 36, 54, "scl-bottom"))
    tracks += route("SCL", "B.Cu",
                    [(36, 54), (44, 54), (44, 31), (48, 31),
                     (50.9, 31), (62, 31)],
                    0.20, "scl-tree")
    tracks += route("SCL", "B.Cu", [(48, 31), (48, 32)],
                    0.20, "scl-test-back")
    tracks.append(via("SCL", 48, 32, "scl-test"))
    tracks += route("SCL", "F.Cu", [(48, 32), (48, 31)],
                    0.20, "scl-test")
    tracks.append(via("SCL", 50.9, 31, "scl-r1"))
    tracks += route("SCL", "F.Cu", [(50.9, 31), (50.9, 34)],
                    0.20, "scl-r1")
    tracks.append(via("SCL", 62, 31, "scl-u1"))
    tracks += route("SCL", "F.Cu",
                    [(62, 31), (62, 27.75), (58.9875, 27.75)],
                    0.20, "scl-u1")

    tracks += route("SDA", "F.Cu",
                    [(36.5, 50.5), (36.5, 53.5), (39.5, 56.5)],
                    0.15, "sda-fanout")
    tracks.append(via("SDA", 39.5, 56.5, "sda-bottom"))
    tracks += route("SDA", "B.Cu",
                    [(39.5, 56.5), (42, 56.5)],
                    0.20, "sda-bottom-back")
    tracks.append(via("SDA", 42, 56.5, "sda-jumper-bottom"))
    tracks += route("SDA", "F.Cu", [(42, 56.5), (42, 52)],
                    0.20, "sda-layer-jumper")
    tracks.append(via("SDA", 42, 52, "sda-jumper-top"))
    tracks += route("SDA", "B.Cu",
                    [(42, 52), (42, 25), (47, 25),
                     (56.9, 25), (62, 25)],
                    0.20, "sda-tree")
    tracks += route("SDA", "B.Cu", [(47, 25), (47, 26)],
                    0.20, "sda-test-back")
    tracks.append(via("SDA", 47, 26, "sda-test"))
    tracks += route("SDA", "F.Cu", [(47, 26), (47, 25)],
                    0.20, "sda-test")
    tracks.append(via("SDA", 56.9, 25, "sda-r2"))
    tracks += route("SDA", "F.Cu", [(56.9, 25), (56.9, 22)],
                    0.20, "sda-r2")
    tracks.append(via("SDA", 62, 25, "sda-u1"))
    tracks += route("SDA", "F.Cu",
                    [(62, 25), (62, 27.25), (58.9875, 27.25)],
                    0.20, "sda-u1")

    # Gated display rail and its input supply.
    tracks += route("3V3", "F.Cu", [(30, 4.5), (28.5, 3)],
                    0.20, "supply-fpc-fanout")
    tracks.append(via("3V3", 28.5, 3, "supply-fpc"))
    tracks += route("3V3", "B.Cu",
                    [(28.5, 3), (40, 3), (40, 16.5), (25, 16.5)],
                    0.30, "supply-fpc-back")
    tracks.append(via("3V3", 25, 16.5, "supply-layer-jump"))
    tracks += route("3V3", "F.Cu", [(25, 16.5), (25, 20)],
                    0.30, "supply-layer-jump")
    tracks.append(via("3V3", 25, 20, "supply-trunk"))
    tracks += route("3V3", "F.Cu",
                    [(25, 20), (25, 28.05), (23.2, 28.05)],
                    0.30, "u2-input")
    tracks += route("3V3", "F.Cu", [(27, 24.1), (25, 24.1)],
                    0.25, "c3")
    tracks += route("3V3", "B.Cu", [(40, 16.5), (42, 16.5), (42, 13)],
                    0.20, "tp2-back")
    tracks.append(via("3V3", 42, 13, "tp2"))
    tracks += route("3V3", "F.Cu", [(42, 13), (42, 12)],
                    0.20, "tp2")
    tracks += route("3V3", "B.Cu", [(25, 20), (63, 20)],
                    0.30, "supply-trunk")
    tracks.append(via("3V3", 45, 20, "r1-supply"))
    tracks += route("3V3", "F.Cu", [(45, 20), (45, 36), (49.1, 34)],
                    0.20, "r1-supply")
    tracks.append(via("3V3", 54, 20, "r2-supply"))
    tracks += route("3V3", "F.Cu", [(54, 20), (55.1, 22)],
                    0.20, "r2-supply")
    tracks += route("3V3", "B.Cu", [(63, 20), (63, 34)],
                    0.25, "r7-supply")
    tracks.append(via("3V3", 63, 34, "r7-supply"))
    tracks += route("3V3", "F.Cu", [(63, 34), (67.1, 34)],
                    0.20, "r7-supply")

    tracks += route("DISP_3V3", "F.Cu",
                    [(20.8, 28.05), (18, 28.05), (18, 32.1)],
                    0.35, "display-output-local")
    tracks += route("DISP_3V3", "F.Cu",
                    [(18, 28.05), (16, 30), (16, 38), (25, 45),
                     (25, 48), (30, 48)],
                    0.35, "display-output-fpc")
    tracks += route("DISP_3V3", "F.Cu", [(30, 48), (30, 50.5)],
                    0.15, "display-output-fpc-pad")

    # Battery input, reverse-polarity protection and fuel-gauge supply.
    tracks += route("BAT_RAW", "F.Cu",
                    [(4, 18), (2, 18), (2, 5.5), (8.5, 5.5), (8.5, 8)],
                    0.60, "battery-input")
    tracks += route("BAT_FUSED", "F.Cu",
                    [(11.5, 8), (11.5, 4), (20, 4), (20, 7.025),
                     (18.7365, 7.025), (18.7365, 7.675)],
                    0.25, "battery-fused-fet")
    tracks += route("BAT_FUSED", "F.Cu",
                    [(18.7365, 7.675), (21, 6)],
                    0.20, "u3-vin-trunk")
    tracks += route("BAT_FUSED", "F.Cu",
                    [(21, 6), (29.5, 6), (29.5, 11.975),
                     (27.1375, 11.975)], 0.20, "u3-vin")
    tracks += route("BAT_FUSED", "F.Cu",
                    [(11.5, 8), (10, 10), (10, 12.1), (13, 12.1)],
                    0.25, "input-decoupling")
    tracks.append(via("BAT_FUSED", 29.5, 14, "protection-pullups"))
    tracks += route("BAT_FUSED", "F.Cu", [(29, 11.975), (29.5, 14)],
                    0.25, "protection-pullups")
    tracks += route("BAT_FUSED", "B.Cu",
                    [(29.5, 14), (27.6, 14), (21.1, 14)],
                    0.30, "protection-pullups-back")
    tracks.append(via("BAT_FUSED", 27.6, 14, "uv-pullup"))
    tracks.append(via("BAT_FUSED", 21.1, 14, "shdn-pullup"))
    tracks += route("BAT_FUSED", "F.Cu",
                    [(27.6, 14), (27.6, 16)], 0.25, "uv-pullup")
    tracks += route("BAT_FUSED", "F.Cu",
                    [(21.1, 14), (21.1, 16)], 0.25, "shdn-pullup")

    # The two MOSFET sources and gates must each be joined around intervening
    # opposite-net package pins; short B.Cu links avoid a same-layer crossing.
    common_vias = [(15.8, 7.025), (15.8, 8.325)]
    tracks += route("FET_COMMON", "F.Cu",
                    [(17.2635, 7.025), common_vias[0]],
                    0.18, "q1-common-1")
    tracks += route("FET_COMMON", "F.Cu",
                    [(17.2635, 8.325), common_vias[1]],
                    0.18, "q1-common-2")
    for index, point in enumerate(common_vias):
        tracks.append(via("FET_COMMON", *point, f"q1-common-{index}"))
    tracks += route("FET_COMMON", "B.Cu", common_vias,
                    0.18, "q1-common-join")

    gate_vias = [(14.5, 7.675), (14.5, 8.975)]
    tracks += route("PROT_GATE", "F.Cu",
                    [(17.2635, 7.675), gate_vias[0]],
                    0.18, "q1-gate-1")
    tracks += route("PROT_GATE", "F.Cu",
                    [(17.2635, 8.975), gate_vias[1]],
                    0.18, "q1-gate-2")
    for index, point in enumerate(gate_vias):
        tracks.append(via("PROT_GATE", *point, f"q1-gate-{index}"))
    tracks += route("PROT_GATE", "B.Cu", gate_vias,
                    0.18, "q1-gate-join")
    tracks += route("PROT_GATE", "B.Cu",
                    [(14.5, 8.975), (14.5, 12), (21.5, 13),
                     (22, 13), (22, 8)],
                    0.20, "u3-gate-back")
    tracks.append(via("PROT_GATE", 22, 8, "u3-gate"))
    tracks += route("PROT_GATE", "F.Cu",
                    [(22, 8), (24, 8), (24, 10.025), (24.8625, 10.025)],
                    0.18, "u3-gate")

    tracks += route("VBAT_SAFE", "F.Cu",
                    [(18.7365, 8.325), (18.7365, 8.975),
                     (21.5, 9.2), (23, 10.675), (24.8625, 10.675)],
                    0.22, "u3-vout")
    tracks += route("VBAT_SAFE", "F.Cu",
                    [(18.7365, 8.975), (20, 11), (20, 13), (18, 14.975)],
                    0.40, "battery-decoupling")
    tracks += route("VBAT_SAFE", "F.Cu",
                    [(20, 13), (16.5, 14), (16.5, 21), (14, 21),
                     (14, 36), (8, 36), (8, 40)],
                    0.60, "battery-safe-upper")
    tracks += route("VBAT_SAFE", "F.Cu",
                    [(8, 40), (10, 40), (11, 41)],
                    0.60, "battery-output")
    tracks += route("VBAT_SAFE", "F.Cu", [(16.5, 18), (20, 18)],
                    0.40, "fuel-gauge-via-feed")
    tracks.append(via("VBAT_SAFE", 20, 18, "fuel-gauge-feed"))
    tracks += route("VBAT_SAFE", "B.Cu",
                    [(20, 18), (23, 19), (52, 19)],
                    0.40, "fuel-gauge-feed")
    tracks.append(via("VBAT_SAFE", 52, 19, "fuel-gauge-feed"))
    tracks += route("VBAT_SAFE", "F.Cu",
                    [(52, 19), (50, 21), (50, 22), (51.1, 24),
                     (54, 26), (55.3, 26), (55.3, 27.75),
                     (57.0125, 27.75), (57.0125, 28.25)],
                    0.25, "fuel-local")
    tracks += route("VBAT_SAFE", "F.Cu",
                    [(54, 26), (54, 30.1)],
                    0.25, "fuel-vdd")

    tracks += route("PROT_SHDN", "F.Cu",
                    [(24.8625, 11.975), (23.5, 13), (22.9, 16)],
                    0.20, "protection-shdn")
    tracks += route("PROT_UV", "F.Cu",
                    [(27.1375, 11.325), (28.2, 11.325)],
                    0.15, "protection-uv-local")
    tracks.append(via("PROT_UV", 28.2, 11.325, "protection-uv-u3",
                      0.50, 0.30))
    tracks += route("PROT_UV", "B.Cu",
                    [(28.2, 11.325), (30.5, 13), (30.5, 15), (29.4, 15)],
                    0.18, "protection-uv-back")
    tracks.append(via("PROT_UV", 29.4, 15, "protection-uv-r8",
                      0.50, 0.30))
    tracks += route("PROT_UV", "F.Cu", [(29.4, 15), (29.4, 16)],
                    0.15, "protection-uv-r8")

    # MAX17048 ground fan-out.  Every front pad reaches a standard via and the
    # saved B.Cu ground plane; the exposed-pad via must be epoxy-filled/capped.
    ground_links = [
        ([(6, 18), (7.5, 18), (7.5, 20)], (7.5, 20), "j1"),
        ([(13, 41), (15, 42.5)], (15, 42.5), "j2"),
        ([(13, 13.9), (12, 15)], (12, 15), "c6"),
        ([(18, 17.025), (19, 17), (22, 17)], (22, 17), "c7"),
        ([(27.1375, 10.025), (28.5, 9.5)], (28.5, 9.5), "u3-gnd"),
        ([(27.1375, 10.675), (27.1375, 10.025)], None, "u3-ov"),
        ([(20.8, 29), (19.5, 29), (19.5, 31)], (19.5, 31), "u2"),
        ([(27, 25.9), (27, 27)], (27, 27), "c3"),
        ([(18, 33.9), (19.5, 34.5)], (19.5, 34.5), "c4"),
        ([(26.9, 32), (28, 33)], (28, 33), "r6"),
        ([(52.9, 24), (55.5, 24)], (55.5, 24), "c1"),
        ([(54, 31.9), (53, 33.5)], (53, 33.5), "c2"),
        ([(57.0125, 27.25), (56, 27.25), (56, 26.4)], (56, 26.4), "u1-ctg"),
        ([(57.0125, 28.75), (55.5, 29.0)], (55.5, 29.0), "u1-gnd"),
        ([(70.9, 18), (70.9, 16.5)], (70.9, 16.5), "usb-divider"),
        ([(70.9, 21), (70.9, 22.5)], (70.9, 22.5), "usb-filter"),
        ([(78.43, 31.75), (80.2, 31.75), (80.2, 29.5)],
         (80.2, 29.5), "power-switch"),
        ([(80.7, 39.1), (82.0, 39.1), (82.0, 44.9), (80.7, 44.9), (81.5, 45.5)],
         (81.5, 45.5), "button-a"),
        ([(80.7, 48.1), (82.0, 48.1), (82.0, 53.9), (80.7, 53.9), (81.5, 54.5)],
         (81.5, 54.5), "button-b"),
        ([(8, 52), (9.5, 53)], (9.5, 53), "tp3"),
    ]
    for points, via_point, label in ground_links:
        tracks += route("GND", "F.Cu", points, 0.25, f"ground-{label}")
        if via_point is not None:
            tracks.append(via("GND", via_point[0], via_point[1], label))
    tracks += route("GND", "B.Cu", [(70.9, 16.5), (70.9, 22.5)],
                    0.25, "usb-ground-link")
    tracks += route("GND", "F.Cu",
                    [(58, 28.4), (58.5, 28.4), (58.9875, 28.25)],
                    0.10, "ground-u1-ep")
    tracks += route("GND", "F.Cu",
                    [(58.9875, 28.25), (60.2, 28.35)],
                    0.10, "ground-u1-qstrt")
    tracks.append(via("GND", 60.2, 28.35, "u1-qstrt", 0.55, 0.30))

    # Fuel-gauge alert and right-side controls.
    tracks += route("ALERT", "F.Cu",
                    [(58.9875, 28.75), (60, 30), (62, 32),
                     (67, 32), (68, 24.5), (72, 24.5),
                     (74, 22.24)], 0.20, "alert-u1")
    tracks += route("ALERT", "F.Cu", [(67, 32), (68.9, 34)],
                    0.20, "alert-pullup")

    tracks += route("BTN_A", "F.Cu", [(74, 7), (68, 7)],
                    0.25, "button-a-header")
    tracks.append(via("BTN_A", 68, 7, "button-a-header"))
    tracks += route("BTN_A", "B.Cu", [(68, 7), (66, 9), (66, 45)],
                    0.25, "button-a")
    tracks.append(via("BTN_A", 66, 45, "button-a"))
    tracks += route("BTN_A", "F.Cu",
                    [(66, 45), (74.5, 45), (75.5, 44.9), (77.3, 44.9)],
                    0.25, "button-a")
    tracks += route("BTN_A", "F.Cu",
                    [(75.5, 44.9), (75.5, 39.1), (77.3, 39.1)],
                    0.25, "button-a-pair")
    tracks += route("BTN_B", "F.Cu", [(74, 9.54), (72, 9.54)],
                    0.25, "button-b-fanout")
    tracks.append(via("BTN_B", 72, 9.54, "button-b-header"))
    tracks += route("BTN_B", "B.Cu",
                    [(72, 9.54), (72, 54), (75, 54)],
                    0.25, "button-b")
    tracks.append(via("BTN_B", 75, 54, "button-b"))
    tracks += route("BTN_B", "F.Cu",
                    [(75, 54), (75.5, 53.9), (77.3, 53.9)],
                    0.25, "button-b")
    tracks += route("BTN_B", "F.Cu",
                    [(75.5, 53.9), (75.5, 48.1), (77.3, 48.1)],
                    0.25, "button-b-pair")
    tracks += route("POWER_SW", "B.Cu",
                    [(74, 12.08), (81, 12.08), (81, 34.75),
                     (80.5, 34.75)],
                    0.25, "power-switch")
    tracks.append(via("POWER_SW", 80.5, 34.75, "power-switch"))
    tracks += route("POWER_SW", "F.Cu", [(80.5, 34.75), (78.43, 34.75)],
                    0.25, "power-switch")

    tracks += route("USB_5V", "F.Cu",
                    [(74, 14.62), (72, 14.62), (70, 13),
                     (68.1, 15)],
                    0.30, "usb-input")
    tracks += route("USB_5V", "F.Cu", [(70, 13), (65, 12)],
                    0.25, "usb-test")
    tracks += route("USB_SENSE", "F.Cu",
                    [(69.9, 15), (68.5, 16.5), (69.1, 18)],
                    0.20, "usb-divider")
    tracks += route("USB_SENSE", "F.Cu",
                    [(69.1, 18), (69.1, 21)],
                    0.20, "usb-filter")
    tracks += route("USB_SENSE", "F.Cu",
                    [(69.1, 19.7), (74, 19.7)],
                    0.20, "usb-header")

    board = [
        "(kicad_pcb",
        "  (version 20241229)",
        '  (generator "pcbnew")',
        '  (generator_version "9.0")',
        "  (general (thickness 1.0) (legacy_teardrops no))",
        '  (paper "A4")',
        "  (layers",
        '    (0 "F.Cu" signal)', '    (2 "B.Cu" signal)',
        '    (9 "F.Adhes" user "F.Adhesive")',
        '    (11 "B.Adhes" user "B.Adhesive")',
        '    (13 "F.Paste" user)', '    (15 "B.Paste" user)',
        '    (5 "F.SilkS" user "F.Silkscreen")',
        '    (7 "B.SilkS" user "B.Silkscreen")',
        '    (1 "F.Mask" user)', '    (3 "B.Mask" user)',
        '    (17 "Dwgs.User" user "User.Drawings")',
        '    (19 "Cmts.User" user "User.Comments")',
        '    (21 "Eco1.User" user "User.Eco1")',
        '    (23 "Eco2.User" user "User.Eco2")',
        '    (25 "Edge.Cuts" user)', '    (27 "Margin" user)',
        '    (31 "F.CrtYd" user "F.Courtyard")',
        '    (29 "B.CrtYd" user "B.Courtyard")',
        '    (35 "F.Fab" user)', '    (33 "B.Fab" user)',
        "  )",
        "  (setup (pad_to_mask_clearance 0))",
    ]
    board += [f'  (net {index} "{name}")'
              for index, name in enumerate(NETS)]
    board += parts
    board.append(
        f'  (gr_rect (start 0 0) (end 84 58) '
        f'(stroke (width 0.15) (type solid)) (fill none) '
        f'(layer "Edge.Cuts") (uuid "{uid("edge-rectangle")}"))'
    )
    board += [
        board_text("QM R0.1 PROTOTYPE", 52, 56.5, 0.80),
        board_text("J1 BAT  +  -", 5, 14.0, 0.80),
        board_text("J2 SYS  +  -", 11, 37.5, 0.80),
        board_text("GDI IN  PIN1<", 34.25, 1.5, 0.80),
        board_text("GDI OUT PIN1<", 42.0, 50.5, 0.80, justify="left"),
        board_text("J5 AUX DNP", 76.5, 25.5, 0.80),
        board_text("PWR", 77.0, 28.0, 0.80),
        board_text("A", 74.5, 42.0, 0.80),
        board_text("B", 74.5, 51.0, 0.80),
        board_text("QM R0.1", 42, 56.5, 0.80, layer="B.SilkS",
                   justify="mirror"),
    ]
    board += tracks
    board += [
        f'  (zone (net 1) (net_name "GND") (layer "B.Cu") '
        f'(uuid "{uid("ground-zone")}") (hatch edge 0.5)',
        "    (connect_pads (clearance 0.25)) (min_thickness 0.20)",
        "    (fill yes (thermal_gap 0.30) (thermal_bridge_width 0.35))",
        "    (polygon (pts (xy 0.6 0.6) (xy 83.4 0.6) "
        "(xy 83.4 57.4) (xy 0.6 57.4)))",
        "  )",
        ")",
    ]
    return "\n".join(board) + "\n"


def schematic_components() -> list[dict]:
    """Authoritative schematic pin/net list used by native KiCad ERC."""
    gdi = ["3V3", "LCD_BL", "GND", "SCLK", "MOSI", "MISO", "LCD_DC",
           "LCD_RST", "LCD_CS", "SD_CS", "LCD_POWER_EN", "TOUCH_CS",
           "SCL", "SDA", "INT", "BUSY", "X1", "X2"]

    footprint_names = {row[0]: row[2] for row in POSITIONS}
    footprint_names["J5"] = "PinHeader_1x07_P2.54mm"

    def component(ref: str, value: str, pin_names: list[str],
                  nets: list[str], virtual: bool = False) -> dict:
        if len(pin_names) != len(nets):
            raise ValueError(f"schematic pin list mismatch for {ref}")
        footprint = "" if virtual else f'QuotaMonitor:{footprint_names[ref]}'
        return {"ref": ref, "value": value, "pin_names": pin_names,
                "pin_types": ["passive"] * len(pin_names), "nets": nets,
                "footprint": footprint, "virtual": virtual}

    items = [
        component("J1", "S2B-PH-SM4-TB(LF)(SN)", ["BAT+", "BAT-"],
                  ["BAT_RAW", "GND"]),
        component("J2", "S2B-PH-SM4-TB(LF)(SN)", ["SYS+", "SYS-"],
                  ["VBAT_SAFE", "GND"]),
        component("F1", "MF-NSMF150-2", ["1", "2"],
                  ["BAT_RAW", "BAT_FUSED"]),
        component("Q1", "Si5908BDC-T1-GE3",
                  ["S1", "G1", "S2", "G2", "D2", "D2", "D1", "D1"],
                  ["FET_COMMON", "PROT_GATE", "FET_COMMON", "PROT_GATE",
                   "VBAT_SAFE", "VBAT_SAFE", "BAT_FUSED", "BAT_FUSED"]),
        component("U3", "LTC4365ITS8#TRMPBF",
                  ["GATE", "VOUT", "FAULT", "SHDN", "VIN", "UV", "OV", "GND"],
                  ["PROT_GATE", "VBAT_SAFE", "", "PROT_SHDN", "BAT_FUSED",
                   "PROT_UV", "GND", "GND"]),
        component("R5", "100k", ["1", "2"], ["BAT_FUSED", "PROT_SHDN"]),
        component("R8", "100k", ["1", "2"], ["BAT_FUSED", "PROT_UV"]),
        component("C6", "0.1uF", ["1", "2"], ["BAT_FUSED", "GND"]),
        component("C7", "10uF", ["1", "2"], ["VBAT_SAFE", "GND"]),
        component("U2", "AP22804AW5-7", ["OUT", "GND", "FLG", "EN", "IN"],
                  ["DISP_3V3", "GND", "", "LCD_POWER_EN", "3V3"]),
        component("C3", "1uF", ["1", "2"], ["3V3", "GND"]),
        component("C4", "1uF", ["1", "2"], ["DISP_3V3", "GND"]),
        component("R6", "100k", ["1", "2"], ["LCD_POWER_EN", "GND"]),
        component("U1", "MAX17048G+T10",
                  ["CTG", "CELL", "VDD", "GND", "ALRT", "QSTRT", "SCL", "SDA", "EP"],
                  ["GND", "VBAT_SAFE", "VBAT_SAFE", "GND", "ALERT", "GND",
                   "SCL", "SDA", "GND"]),
        component("C1", "0.1uF", ["1", "2"], ["VBAT_SAFE", "GND"]),
        component("C2", "0.1uF", ["1", "2"], ["VBAT_SAFE", "GND"]),
        component("R1", "4.7k", ["1", "2"], ["3V3", "SCL"]),
        component("R2", "4.7k", ["1", "2"], ["3V3", "SDA"]),
        component("R7", "10k", ["1", "2"], ["3V3", "ALERT"]),
        component("J3", "FH12-18S-0.5SH(55)", gdi, gdi),
        component("J4", "FH12-18S-0.5SH(55)",
                  ["DISP_3V3"] + gdi[1:], ["DISP_3V3"] + gdi[1:]),
        component("J5", "TSW-107-07-G-S",
                  ["GPIO47", "GPIO8", "GPIO5", "VCC", "GND", "GPIO6", "ALERT"],
                  ["BTN_A", "BTN_B", "POWER_SW", "USB_5V", "GND",
                   "USB_SENSE", "ALERT"]),
        component("SW1", "PCM12SMTR", ["OFF", "COMMON", "ON"],
                  ["GND", "POWER_SW", ""]),
        component("SW2", "PTS850VR18PSMTR LFS",
                  ["A1", "B1", "A2", "B2"],
                  ["BTN_A", "GND", "BTN_A", "GND"]),
        component("SW3", "PTS850VR18PSMTR LFS",
                  ["A1", "B1", "A2", "B2"],
                  ["BTN_B", "GND", "BTN_B", "GND"]),
        component("R3", "100k", ["1", "2"], ["USB_5V", "USB_SENSE"]),
        component("R4", "100k", ["1", "2"], ["USB_SENSE", "GND"]),
        component("C5", "0.1uF", ["1", "2"], ["USB_SENSE", "GND"]),
    ]
    for index, net in enumerate(["VBAT_SAFE", "3V3", "GND", "USB_5V", "SDA", "SCL"], 1):
        items.append(component(f"TP{index}", "Keystone 5015", [net], [net]))
    items += [
        component("#FLG01", "PWR_FLAG BAT_FUSED", ["PWR"], ["BAT_FUSED"], True),
        component("#FLG02", "PWR_FLAG VBAT_SAFE", ["PWR"], ["VBAT_SAFE"], True),
    ]

    pin_type_overrides = {
        "J1": ["power_out", "power_out"],
        "Q1": ["passive", "input", "passive", "input",
               "passive", "passive", "passive", "passive"],
        "U3": ["output", "power_in", "open_collector", "input",
               "power_in", "input", "input", "power_in"],
        "U2": ["power_out", "power_in", "open_collector", "input", "power_in"],
        "U1": ["input", "power_in", "power_in", "power_in", "open_collector",
               "input", "input", "bidirectional", "power_in"],
        "J3": ["power_out", "output", "passive", "output", "output", "input",
               "output", "output", "output", "output", "output", "output",
               "output", "bidirectional", "input", "input", "passive", "passive"],
        "J4": ["power_in", "input", "passive", "input", "input", "output",
               "input", "input", "input", "input", "input", "input", "input",
               "bidirectional", "output", "output", "passive", "passive"],
        "J5": ["bidirectional", "bidirectional", "bidirectional", "power_out",
               "passive", "bidirectional", "bidirectional"],
        "#FLG01": ["power_out"],
        "#FLG02": ["power_out"],
    }
    for item in items:
        if item["ref"] in pin_type_overrides:
            item["pin_types"] = pin_type_overrides[item["ref"]]
    return items


def _sch_escape(value: str) -> str:
    return value.replace("\\", "\\\\").replace('"', '\\"')


def _symbol_name(component: dict) -> str:
    return "QM_" + component["ref"].replace("#", "PWR_")


def _symbol_definition(component: dict, qualified: bool) -> str:
    name = _symbol_name(component)
    outer = f"QuotaMonitor:{name}" if qualified else name
    pin_names = component["pin_names"]
    bottom = -(len(pin_names) - 1) * 2.54 - 1.27
    in_bom = "no" if component["virtual"] else "yes"
    on_board = "no" if component["virtual"] else "yes"
    lines = [
        f'    (symbol "{outer}"',
        "      (pin_names (offset 1.016))",
        f"      (exclude_from_sim no) (in_bom {in_bom}) (on_board {on_board})",
        f'      (property "Reference" "{component["ref"][0]}" (at 0 2.54 0) '
        '(effects (font (size 1.27 1.27))))',
        f'      (property "Value" "{_sch_escape(component["value"])}" '
        f'(at 0 {bottom - 2.54:.2f} 0) (effects (font (size 1.27 1.27))))',
        f'      (property "Footprint" "{_sch_escape(component["footprint"])}" (at 0 0 0) '
        '(effects (font (size 1.27 1.27)) (hide yes)))',
        '      (property "Datasheet" "~" (at 0 0 0) '
        '(effects (font (size 1.27 1.27)) (hide yes)))',
        '      (property "Description" "Quota monitor ERC symbol with declared electrical pin types" '
        '(at 0 0 0) (effects (font (size 1.27 1.27)) (hide yes)))',
        f'      (symbol "{name}_1_1"',
        f'        (rectangle (start -5.08 1.27) (end 5.08 {bottom:.2f}) '
        '(stroke (width 0.254) (type default)) (fill (type background)))',
    ]
    for index, pin_name in enumerate(pin_names):
        yy = -index * 2.54
        pin_type = component["pin_types"][index]
        lines += [
            f'        (pin {pin_type} line (at -7.62 {yy:.2f} 0) (length 2.54)',
            f'          (name "{_sch_escape(pin_name)}" '
            '(effects (font (size 1.27 1.27))))',
            f'          (number "{index + 1}" '
            '(effects (font (size 1.27 1.27)))))',
        ]
    lines += ["      )", "      (embedded_fonts no)", "    )"]
    return "\n".join(lines)


def build_symbol_library() -> str:
    lines = ["(kicad_symbol_lib", "  (version 20231120)",
             '  (generator "kicad_symbol_editor")',
             '  (generator_version "9.0")']
    for component in schematic_components():
        lines.append(_symbol_definition(component, False).replace("    ", "  ", 1))
    lines.append(")")
    return "\n".join(lines) + "\n"


def build_schematic() -> str:
    """Build a native KiCad 9 symbol schematic suitable for official ERC."""
    components = schematic_components()
    root_id = uid("schematic-root")
    columns = [22.86, 91.44, 160.02, 228.60]
    cursors = [17.78] * len(columns)
    placements: list[tuple[dict, float, float]] = []
    for component in components:
        pin_count = len(component["pin_names"])
        cell_height = max(12.7, (pin_count - 1) * 2.54 + 10.16)
        column = min(range(len(columns)), key=lambda index: cursors[index])
        top = cursors[column]
        origin_y = top + (pin_count - 1) * 2.54
        placements.append((component, columns[column], origin_y))
        cursors[column] += cell_height

    lines = [
        "(kicad_sch", "  (version 20250114)",
        '  (generator "eeschema")', '  (generator_version "9.0")',
        f'  (uuid "{root_id}")', '  (paper "A4")',
        '  (title_block (title "Quota Monitor Carrier - ERC Schematic") '
        '(date "2026-08-03") (rev "0.3-prototype") '
        '(company "Open hardware reference design") '
        '(comment 1 "Prototype only: deep-discharge recharge recovery and enclosure clearance are unverified"))',
        "  (lib_symbols)",
    ]
    # Insert definitions inside lib_symbols rather than leaving the compact
    # placeholder above.
    lines.pop()
    lines.append("  (lib_symbols")
    lines.extend(_symbol_definition(component, True) for component in components)
    lines.append("  )")

    for component, x, origin_y in placements:
        ref = component["ref"]
        for index, net in enumerate(component["nets"]):
            pin_x = x + 7.62
            pin_y = origin_y - index * 2.54
            if not net:
                lines += [
                    "  (no_connect",
                    f"    (at {pin_x:.2f} {pin_y:.2f})",
                    f'    (uuid "{uid(f"sch-nc-{ref}-{index+1}")}")',
                    "  )",
                ]
                continue
            label_x = x + 12.70
            lines += [
                "  (wire",
                f"    (pts (xy {pin_x:.2f} {pin_y:.2f}) "
                f"(xy {label_x:.2f} {pin_y:.2f}))",
                "    (stroke (width 0) (type default))",
                f'    (uuid "{uid(f"sch-wire-{ref}-{index+1}")}")',
                "  )",
                f'  (label "{_sch_escape(net)}"',
                f"    (at {label_x:.2f} {pin_y:.2f} 0)",
                "    (effects (font (size 1.27 1.27)) (justify left bottom))",
                f'    (uuid "{uid(f"sch-label-{ref}-{index+1}")}")',
                "  )",
            ]

        top = origin_y - (len(component["pin_names"]) - 1) * 2.54
        name = _symbol_name(component)
        symbol_id = uid(f"sch-symbol-{ref}")
        in_bom = "no" if component["virtual"] else "yes"
        on_board = "no" if component["virtual"] else "yes"
        lines += [
            "  (symbol",
            f'    (lib_id "QuotaMonitor:{name}")',
            f"    (at {x:.2f} {origin_y:.2f} 180)",
            "    (unit 1)",
            f"    (exclude_from_sim no) (in_bom {in_bom}) "
            f"(on_board {on_board}) (dnp no)",
            f'    (uuid "{symbol_id}")',
            f'    (property "Reference" "{ref}" (at {x:.2f} {top-3.81:.2f} 0) '
            '(effects (font (size 1.27 1.27))))',
            f'    (property "Value" "{_sch_escape(component["value"])}" '
            f'(at {x:.2f} {top-1.27:.2f} 0) '
            '(effects (font (size 1.27 1.27))))',
            f'    (property "Footprint" "{_sch_escape(component["footprint"])}" '
            f'(at {x:.2f} {origin_y:.2f} 0) '
            '(effects (font (size 1.27 1.27)) (hide yes)))',
            f'    (property "Datasheet" "~" (at {x:.2f} {origin_y:.2f} 0) '
            '(effects (font (size 1.27 1.27)) (hide yes)))',
            f'    (property "Description" "Quota monitor ERC symbol" '
            f'(at {x:.2f} {origin_y:.2f} 0) '
            '(effects (font (size 1.27 1.27)) (hide yes)))',
        ]
        for index in range(len(component["pin_names"])):
            lines += [
                f'    (pin "{index+1}"',
                f'      (uuid "{uid(f"sch-pin-{ref}-{index+1}")}")',
                "    )",
            ]
        lines += [
            "    (instances",
            '      (project ""',
            f'        (path "/{root_id}"',
            f'          (reference "{ref}") (unit 1)',
            "        )",
            "      )",
            "    )",
            "  )",
        ]
    lines += ['  (sheet_instances (path "/" (page "1")))',
              "  (embedded_fonts no)", ")"]
    return "\n".join(lines) + "\n"


POSITIONS = [
    ("J1", "S2B-PH-SM4-TB(LF)(SN)", "JST_PH_S2B-PH-SM4-TB_1x02_P2.00mm_Horizontal", 5, 18),
    ("J2", "S2B-PH-SM4-TB(LF)(SN)", "JST_PH_S2B-PH-SM4-TB_1x02_P2.00mm_Horizontal", 12, 41),
    ("F1", "MF-NSMF150-2", "Bourns_MF-NSMF_1206", 10, 8),
    ("Q1", "Si5908BDC-T1-GE3", "Vishay_1206-8_ChipFET", 18, 8),
    ("U3", "LTC4365ITS8#TRMPBF", "TSOT-23-8", 26, 11),
    ("R5", "100k", "R_0603", 22, 16),
    ("R8", "100k", "R_0603", 28.5, 16),
    ("C6", "0.1uF", "C_0603", 13, 13),
    ("C7", "10uF", "Murata_GRM21_Reflow_a1.10_b0.95_c0.95", 18, 16),
    ("U2", "AP22804AW5-7", "Diodes_SOT-25_AP22804", 22, 29),
    ("C3", "1uF", "C_0603", 27, 25),
    ("C4", "1uF", "C_0603", 18, 33),
    ("R6", "100k", "R_0603", 26, 32),
    ("U1", "MAX17048G+T10", "ADI_TDFN-8-1EP_2x2_P0.5_EP0.8x1.2", 58, 28),
    ("C1", "0.1uF", "C_0603", 52, 24),
    ("C2", "0.1uF", "C_0603", 54, 31),
    ("R1", "4.7k", "R_0603", 50, 34),
    ("R2", "4.7k", "R_0603", 56, 22),
    ("R7", "10k", "R_0603", 68, 34),
    ("J3", "FH12-18S-0.5SH(55)", "Hirose_FH12-18S-0.5SH_1x18_P0.50mm", 34.25, 4.5),
    ("J4", "FH12-18S-0.5SH(55)", "Hirose_FH12-18S-0.5SH_1x18_P0.50mm", 34.25, 50.5),
    ("SW1", "PCM12SMTR", "C&K_PCM12SMTR", 77, 34),
    ("SW2", "PTS850VR18PSMTR LFS", "C&K_PTS850_with_boss_actuator_plusX", 79, 42),
    ("SW3", "PTS850VR18PSMTR LFS", "C&K_PTS850_with_boss_actuator_plusX", 79, 51),
    ("R3", "100k", "R_0603", 69, 15),
    ("R4", "100k", "R_0603", 70, 18),
    ("C5", "0.1uF", "C_0603", 70, 21),
    ("TP1", "Keystone 5015", "Keystone_5015_SMT", 8, 40),
    ("TP2", "Keystone 5015", "Keystone_5015_SMT", 42, 12),
    ("TP3", "Keystone 5015", "Keystone_5015_SMT", 8, 52),
    ("TP4", "Keystone 5015", "Keystone_5015_SMT", 65, 12),
    ("TP5", "Keystone 5015", "Keystone_5015_SMT", 47, 25),
    ("TP6", "Keystone 5015", "Keystone_5015_SMT", 48, 31),
]


def write_positions() -> None:
    with (PCB_DIR / "positions.csv").open("w", encoding="utf-8", newline="") as stream:
        writer = csv.writer(stream, lineterminator="\n")
        writer.writerow(["Ref", "Value", "Package", "PosX_mm", "PosY_mm",
                         "Rotation_deg", "Side"])
        rotations = {"SW1": 270}
        for ref, value, package, x, y in POSITIONS:
            writer.writerow([ref, value, package, x, y,
                             rotations.get(ref, 0), "Top"])


def fill_zones() -> None:
    helper = ROOT / "scripts" / "fill_zones.py"
    try:
        import pcbnew  # type: ignore
    except ImportError:
        pcbnew = None
    if pcbnew is not None:
        board = pcbnew.LoadBoard(str(BOARD_PATH))
        pcbnew.ZONE_FILLER(board).Fill(board.Zones())
        pcbnew.SaveBoard(str(BOARD_PATH), board)
        return
    if shutil.which("docker") is None:
        raise RuntimeError(
            "KiCad pcbnew bindings or Docker are required to save the B.Cu zone fill. "
            "Install KiCad 9 or run with --no-fill only for source inspection."
        )
    mount = f"{REPO}:/work"
    subprocess.run(
        ["docker", "run", "--rm", "-v", mount, "-w", "/work",
         "kicad/kicad:9.0", "python3", "/work/hardware/scripts/fill_zones.py",
         "/work/hardware/pcb/carrier.kicad_pcb"],
        check=True,
    )


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--no-fill", action="store_true",
        help="write the source zone without invoking KiCad (DRC will report unrouted ground)",
    )
    args = parser.parse_args()
    BOARD_PATH.write_text(build_pcb(), encoding="utf-8", newline="\n")
    (PCB_DIR / "carrier.kicad_sch").write_text(
        build_schematic(), encoding="utf-8", newline="\n"
    )
    (PCB_DIR / "QuotaMonitor.kicad_sym").write_text(
        build_symbol_library(), encoding="utf-8", newline="\n"
    )
    (PCB_DIR / "sym-lib-table").write_text(
        '(sym_lib_table\n'
        '  (version 7)\n'
        '  (lib (name "QuotaMonitor")(type "KiCad")'
        '(uri "${KIPRJMOD}/QuotaMonitor.kicad_sym")'
        '(options "")(descr "Quota monitor generated ERC symbols"))\n'
        ')\n',
        encoding="utf-8", newline="\n",
    )
    (PCB_DIR / "fp-lib-table").write_text(
        '(fp_lib_table\n'
        '  (version 7)\n'
        '  (lib (name "QuotaMonitor")(type "KiCad")'
        '(uri "${KIPRJMOD}/QuotaMonitor.pretty")'
        '(options "")(descr "Quota monitor generated exact footprints"))\n'
        ')\n',
        encoding="utf-8", newline="\n",
    )
    write_positions()
    if not args.no_fill:
        fill_zones()
    print(f"wrote {BOARD_PATH}")
    print(f"wrote {PCB_DIR / 'carrier.kicad_sch'}")
    print(f"wrote {PCB_DIR / 'QuotaMonitor.kicad_sym'}")
    print(f"wrote {PCB_DIR / 'positions.csv'}")


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, subprocess.CalledProcessError) as exc:
        print(f"generate_kicad: {exc}", file=sys.stderr)
        raise SystemExit(1) from exc
