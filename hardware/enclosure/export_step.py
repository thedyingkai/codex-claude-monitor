"""Convert the verified OpenSCAD STL exports to genuine STEP solids.

Preferred standalone runtime:

    PYTHONPATH=.tools/ocp python hardware/enclosure/export_step.py

Install ``cadquery-ocp==7.8.1.1.post1`` into that private tool directory first.
The script also retains a FreeCADCmd fallback.  Both paths create an actual
boundary-representation solid and ask OpenCascade to write STEP; neither path
renames an STL or writes a placeholder.
"""

from __future__ import annotations

import os
import pathlib
import sys


ROOT = pathlib.Path(__file__).resolve().parent
GENERATED = ROOT.parent / "rendered" / "enclosure"
GENERATED.mkdir(parents=True, exist_ok=True)
PARTS = ("base", "lid", "button", "switch")

# Wheels for cadquery-ocp keep dependent OpenCascade DLLs in a sibling
# ``cadquery_ocp.libs`` directory.  CPython 3.8+ no longer searches arbitrary
# PATH entries for extension dependencies on Windows, so register a matching
# private-tool directory before importing OCP.  Keep the handles alive for the
# duration of the process.
_dll_handles = []
if os.name == "nt" and hasattr(os, "add_dll_directory"):
    for entry in sys.path:
        dll_dir = pathlib.Path(entry) / "cadquery_ocp.libs"
        if dll_dir.is_dir():
            _dll_handles.append(os.add_dll_directory(str(dll_dir)))


def convert_with_ocp(name: str) -> None:
    from OCP.BRepBuilderAPI import BRepBuilderAPI_MakeSolid, BRepBuilderAPI_Sewing
    from OCP.BRepCheck import BRepCheck_Analyzer
    from OCP.IFSelect import IFSelect_RetDone
    from OCP.STEPControl import STEPControl_AsIs, STEPControl_Writer
    from OCP.StlAPI import StlAPI_Reader
    from OCP.TopoDS import TopoDS, TopoDS_Shape

    source = GENERATED / f"quota_display_{name}.stl"
    destination = GENERATED / f"quota_display_{name}.step"
    mesh_faces = TopoDS_Shape()
    if not StlAPI_Reader().Read(mesh_faces, str(source)):
        raise RuntimeError(f"cannot read {source}")

    sewing = BRepBuilderAPI_Sewing(0.01)
    sewing.Add(mesh_faces)
    sewing.Perform()
    if sewing.NbFreeEdges() or sewing.NbMultipleEdges():
        raise RuntimeError(
            f"{source.name} is not a closed 2-manifold "
            f"(free={sewing.NbFreeEdges()}, multiple={sewing.NbMultipleEdges()})"
        )

    shell = TopoDS.Shell_s(sewing.SewedShape())
    solid = BRepBuilderAPI_MakeSolid(shell).Solid()
    if not BRepCheck_Analyzer(solid).IsValid():
        raise RuntimeError(f"OpenCascade rejected {source.name} as an invalid solid")

    writer = STEPControl_Writer()
    if writer.Transfer(solid, STEPControl_AsIs) != IFSelect_RetDone:
        raise RuntimeError(f"OpenCascade could not transfer {name} to STEP")
    if writer.Write(str(destination)) != IFSelect_RetDone:
        raise RuntimeError(f"OpenCascade could not write {destination}")


def convert_with_freecad(name: str) -> None:
    import Mesh  # type: ignore
    import Part  # type: ignore

    mesh = Mesh.Mesh(str(GENERATED / f"quota_display_{name}.stl"))
    shape = Part.Shape()
    shape.makeShapeFromMesh(mesh.Topology, 0.05)
    solid = Part.makeSolid(shape.removeSplitter())
    if solid.isNull() or not solid.isValid():
        raise RuntimeError(f"FreeCAD rejected {name} as an invalid solid")
    Part.export([solid], str(GENERATED / f"quota_display_{name}.step"))


try:
    import OCP  # noqa: F401
except ImportError:
    converter = convert_with_freecad
    backend = "FreeCAD/OpenCascade"
else:
    converter = convert_with_ocp
    backend = "cadquery-ocp/OpenCascade"

for item in PARTS:
    converter(item)
print(f"STEP solids written to {GENERATED} with {backend}")
