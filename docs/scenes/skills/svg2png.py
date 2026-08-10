#!/usr/bin/env python3
"""Degrade-aware SVG -> PNG converter for the fireworks diagram scene.

Contract (niuniu fireworks 集成 步骤2, issue #472)
-------------------------------------------------
PNG export depends on the OPTIONAL `cairosvg` pip package. SVG is always the
source of truth and a valid deliverable on its own; PNG is a convenience.

This helper is the canonical conversion entry point the fireworks-tech-graph
skill (and any other scene skill that wants a raster copy) should call instead
of importing cairosvg directly, so the degrade behaviour stays in one place:

  * cairosvg present  -> render SVG -> PNG headless (works on Win + Linux, no
    separate librsvg/GTK; verified on Windows 11 with a plain
    `python -m pip install --user cairosvg`).
  * cairosvg absent, or its native cairo backend fails to load, or rendering
    raises -> DEGRADE: keep the SVG, print a one-line hint to stderr, and exit
    0. Never crash the agent and never fail silently.

Install when missing (same command the Settings -> System Dependencies page and
internal/service/system_deps.go run):

    python -m pip install --user cairosvg

Usage:
    python svg2png.py INPUT.svg [-o OUTPUT.png]   # convert, degrade if needed
    python svg2png.py --selftest                   # exercise both code paths

Exit codes:
    0  PNG written, OR cairosvg unavailable and we degraded to SVG-only (the
       caller already has the SVG).
    2  Bad invocation / input SVG missing (a real error, distinct from degrade).
"""
from __future__ import annotations

import argparse
import os
import sys

# Test seam: setting NIUNIU_FORCE_NO_CAIROSVG=1 forces the "cairosvg missing"
# branch even on a host where it is installed, so the degrade path is verifiable
# without uninstalling anything. Honoured only here, never in production logic.
_FORCE_NO_CAIROSVG = "NIUNIU_FORCE_NO_CAIROSVG"


def _import_cairosvg():
    """Return the cairosvg module, or raise to trigger the degrade path."""
    if os.environ.get(_FORCE_NO_CAIROSVG):
        raise ImportError(f"forced via {_FORCE_NO_CAIROSVG}")
    import cairosvg  # noqa: PLC0415 — lazy import is the whole point
    return cairosvg


def convert(svg_path: str, png_path: str) -> str | None:
    """Render svg_path -> png_path. Returns the PNG path on success, or None
    when degrading to SVG-only. Degrading is NOT an error: the SVG at svg_path
    remains the deliverable.
    """
    try:
        cairosvg = _import_cairosvg()
    except Exception as exc:  # ImportError, or cairocffi OSError loading cairo
        print(
            f"[niuniu] cairosvg 不可用，已降级为仅 SVG（跳过 PNG）：{exc}",
            file=sys.stderr,
        )
        print(
            "[niuniu] 需要 PNG 时安装：python -m pip install --user cairosvg",
            file=sys.stderr,
        )
        return None
    try:
        cairosvg.svg2png(url=svg_path, write_to=png_path)
        return png_path
    except Exception as exc:  # malformed SVG, backend failure, etc.
        print(
            f"[niuniu] cairosvg 渲染失败，已降级为仅 SVG（保留 {svg_path}）：{exc}",
            file=sys.stderr,
        )
        return None


def _default_png_for(svg_path: str) -> str:
    base, _ = os.path.splitext(svg_path)
    return base + ".png"


def _selftest() -> int:
    """Exercise both the present and missing cairosvg paths. Returns 0 on pass."""
    import tempfile

    svg = (
        '<svg xmlns="http://www.w3.org/2000/svg" width="80" height="40">'
        '<rect width="80" height="40" fill="#1E5FCC"/></svg>'
    )
    with tempfile.TemporaryDirectory() as d:
        svg_path = os.path.join(d, "t.svg")
        png_path = os.path.join(d, "t.png")
        with open(svg_path, "w", encoding="utf-8") as fh:
            fh.write(svg)

        # 1) Degrade path: force-missing cairosvg must return None and NOT raise.
        os.environ[_FORCE_NO_CAIROSVG] = "1"
        try:
            if convert(svg_path, png_path) is not None:
                print("selftest FAIL: forced-missing path should degrade to None")
                return 1
            if os.path.exists(png_path):
                print("selftest FAIL: degrade path must not write a PNG")
                return 1
        finally:
            del os.environ[_FORCE_NO_CAIROSVG]

        # 2) Present path: only assertable when cairosvg is actually installed.
        try:
            _import_cairosvg()
            have = True
        except Exception:
            have = False
        if have:
            out = convert(svg_path, png_path)
            if out is None or not os.path.exists(png_path) or os.path.getsize(png_path) == 0:
                print("selftest FAIL: cairosvg present but PNG not produced")
                return 1
            with open(png_path, "rb") as fh:
                if fh.read(8) != b"\x89PNG\r\n\x1a\n":
                    print("selftest FAIL: output is not a valid PNG")
                    return 1
            print("selftest OK: degrade path + PNG render both verified")
        else:
            print("selftest OK: degrade path verified (cairosvg not installed; "
                  "install it to also verify the render path)")
    return 0


def main(argv: list[str]) -> int:
    ap = argparse.ArgumentParser(description="Degrade-aware SVG -> PNG converter.")
    ap.add_argument("svg", nargs="?", help="input .svg path")
    ap.add_argument("-o", "--out", help="output .png path (default: alongside the SVG)")
    ap.add_argument("--selftest", action="store_true", help="run the built-in self test")
    args = ap.parse_args(argv)

    if args.selftest:
        return _selftest()
    if not args.svg:
        ap.error("svg input required (or pass --selftest)")
    if not os.path.isfile(args.svg):
        print(f"[niuniu] 输入 SVG 不存在：{args.svg}", file=sys.stderr)
        return 2

    png_path = args.out or _default_png_for(args.svg)
    result = convert(args.svg, png_path)
    if result:
        print(result)
    return 0  # degrade is success-from-the-caller's-view: the SVG is still there


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
