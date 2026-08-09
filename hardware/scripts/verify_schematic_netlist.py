#!/usr/bin/env python3
"""Compare the KiCad schematic XML netlist with the routed PCB endpoints."""

from __future__ import annotations

import pathlib
import sys
import xml.etree.ElementTree as ET

import pcbnew


def fail(message: str) -> None:
    raise SystemExit(f"schematic/PCB netlist mismatch: {message}")


def main() -> None:
    if len(sys.argv) != 3:
        raise SystemExit("usage: verify_schematic_netlist.py BOARD.kicad_pcb NETLIST.xml")
    board_path = pathlib.Path(sys.argv[1]).resolve()
    xml_path = pathlib.Path(sys.argv[2]).resolve()

    board = pcbnew.LoadBoard(str(board_path))
    board_nets: dict[str, set[tuple[str, str]]] = {}
    board_components: set[str] = set()
    for footprint in board.GetFootprints():
        ref = footprint.GetReference()
        if ref.startswith("H"):
            continue
        board_components.add(ref)
        for pad in footprint.Pads():
            number = pad.GetNumber()
            net = pad.GetNetname()
            if number and net:
                board_nets.setdefault(net, set()).add((ref, number))

    root = ET.parse(xml_path).getroot()
    component_elements = {
        element.attrib["ref"]: element
        for element in root.findall("./components/comp")
        if not element.attrib["ref"].startswith("#")
    }
    schematic_components = set(component_elements)
    if schematic_components != board_components:
        fail(f"component sets differ: missing={sorted(board_components-schematic_components)}, "
             f"extra={sorted(schematic_components-board_components)}")
    board_footprints = {
        footprint.GetReference(): footprint for footprint in board.GetFootprints()
        if not footprint.GetReference().startswith("H")
    }
    for ref, element in component_elements.items():
        board_footprint = board_footprints[ref]
        schematic_value = element.findtext("value", default="")
        schematic_fpid = element.findtext("footprint", default="")
        expected_fpid = "QuotaMonitor:" + board_footprint.GetFPID().GetUniStringLibItemName()
        if schematic_value != board_footprint.GetValue():
            fail(f"{ref} value differs: schematic={schematic_value!r}, "
                 f"PCB={board_footprint.GetValue()!r}")
        if schematic_fpid != expected_fpid:
            fail(f"{ref} footprint differs: schematic={schematic_fpid!r}, "
                 f"PCB={expected_fpid!r}")

    schematic_nets: dict[str, set[tuple[str, str]]] = {}
    for net in root.findall("./nets/net"):
        name = net.attrib["name"].lstrip("/")
        if name.startswith("unconnected-"):
            continue
        schematic_nets[name] = {
            (node.attrib["ref"], node.attrib["pin"])
            for node in net.findall("node")
            if not node.attrib["ref"].startswith("#")
        }

    if set(schematic_nets) != set(board_nets):
        fail(f"net names differ: missing={sorted(set(board_nets)-set(schematic_nets))}, "
             f"extra={sorted(set(schematic_nets)-set(board_nets))}")
    for name in sorted(board_nets):
        if schematic_nets[name] != board_nets[name]:
            fail(f"{name}: PCB-only={sorted(board_nets[name]-schematic_nets[name])}, "
                 f"schematic-only={sorted(schematic_nets[name]-board_nets[name])}")

    endpoint_count = sum(len(endpoints) for endpoints in board_nets.values())
    print(f"schematic/PCB netlist OK: {len(board_components)} components, "
          f"{len(board_nets)} nets, {endpoint_count} numbered endpoints")


if __name__ == "__main__":
    main()
