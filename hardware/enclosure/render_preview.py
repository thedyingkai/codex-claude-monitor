"""Render the superseded DFR0975/DFR0665 mechanical preview.

Do not use this geometry for the current Keyes E32R28T build. This is a
communication aid, not a collision checker. STL/STEP validity and real
component interference are verified separately.
"""

from __future__ import annotations

import argparse
import re
import struct
from pathlib import Path

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt
import numpy as np
from mpl_toolkits.mplot3d.art3d import Poly3DCollection


ROOT = Path(__file__).resolve().parents[1]
GENERATED = ROOT / "rendered" / "enclosure"


def read_stl(path: Path) -> np.ndarray:
    payload = path.read_bytes()
    if payload[:5].lower() == b"solid" and b"facet normal" in payload[:4096]:
        matches = re.findall(
            rb"\bvertex\s+([-+0-9.eE]+)\s+([-+0-9.eE]+)\s+([-+0-9.eE]+)",
            payload,
        )
        vertices = np.array(
            [[float(x), float(y), float(z)] for x, y, z in matches],
            dtype=np.float32,
        )
        return vertices.reshape((-1, 3, 3))
    count = struct.unpack_from("<I", payload, 80)[0]
    record = np.dtype([("normal", "<f4", (3,)), ("vertices", "<f4", (3, 3)), ("attr", "<u2")])
    return np.frombuffer(payload, dtype=record, offset=84, count=count)["vertices"].copy()


def add_mesh(ax, name: str, offset, color: str, alpha: float) -> None:
    triangles = read_stl(GENERATED / f"quota_display_{name}.stl")
    triangles += np.asarray(offset, dtype=np.float32)
    mesh = Poly3DCollection(triangles, linewidths=0.03, edgecolors=(0, 0, 0, 0.08))
    mesh.set_facecolor(matplotlib.colors.to_rgba(color, alpha))
    ax.add_collection3d(mesh)


def add_box(ax, origin, size, color: str, alpha: float, label: str) -> None:
    x, y, z = origin
    dx, dy, dz = size
    vertices = np.array(
        [
            [x, y, z], [x + dx, y, z], [x + dx, y + dy, z], [x, y + dy, z],
            [x, y, z + dz], [x + dx, y, z + dz],
            [x + dx, y + dy, z + dz], [x, y + dy, z + dz],
        ]
    )
    faces = [[0, 1, 2, 3], [4, 5, 6, 7], [0, 1, 5, 4], [1, 2, 6, 5], [2, 3, 7, 6], [3, 0, 4, 7]]
    collection = Poly3DCollection(vertices[faces], linewidths=0.5, edgecolors=color)
    collection.set_facecolor(matplotlib.colors.to_rgba(color, alpha))
    ax.add_collection3d(collection)
    ax.text(x + dx / 2, y + dy / 2, z + dz, label, fontsize=8, ha="center", va="bottom")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--output",
        type=Path,
        default=GENERATED / "quota_display_assembly.png",
    )
    args = parser.parse_args()

    figure = plt.figure(figsize=(14, 9), dpi=150)
    ax = figure.add_subplot(111, projection="3d")
    add_mesh(ax, "base", (0, 0, 0), "#202838", 0.58)
    add_mesh(ax, "lid", (0, 0, 16.3), "#5b83c4", 0.22)
    for y in (45.5, 54.5):
        add_mesh(ax, "button", (93, y - 3.8, 12.3), "#94a3b8", 0.95)
    # The PCM12 slider is shown at mid travel. Its two stable positions are
    # +/-0.75 mm on Y and the printed face is 4.6 mm wide.
    add_mesh(ax, "switch", (93, 37.5 - 4.6 / 2, 12.4), "#64748b", 0.95)

    add_box(ax, (3.0, 7.5, 1.9), (60, 50, 7.5), "#d97706", 0.28, "755060 battery envelope")
    add_box(ax, (65.0, 2.5, 1.9), (25.4, 60, 9.0), "#2563eb", 0.25, "DFR0975 PCB envelope")
    add_box(ax, (4.5, 3.5, 12.2), (84, 58, 1.0), "#15803d", 0.40, "carrier PCB")
    add_box(ax, (6.5, 7.5, 16.3), (80, 50, 7.2), "#0ea5e9", 0.28, "DFR0665 7.2 mm envelope")

    ax.set_xlim(0, 108)
    ax.set_ylim(0, 65)
    ax.set_zlim(0, 32)
    ax.set_box_aspect((108, 65, 46))
    ax.view_init(elev=27, azim=-56)
    ax.set_proj_type("ortho")
    ax.set_xlabel("X / mm")
    ax.set_ylabel("Y / mm")
    ax.set_zlabel("Z / mm")
    ax.set_title("Quota display mechanical envelope preview - 93 x 65 x 25.7 mm")
    figure.text(
        0.5,
        0.025,
        "Preview only: connector/display interference and battery clearances still require exact-part CAD and physical dry-fit.",
        ha="center",
        fontsize=9,
        color="#9a3412",
    )
    figure.tight_layout(rect=(0, 0.05, 1, 1))
    args.output.parent.mkdir(parents=True, exist_ok=True)
    figure.savefig(args.output, bbox_inches="tight")
    print(args.output)


if __name__ == "__main__":
    main()
