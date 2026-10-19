#!/usr/bin/env python3
"""Derive the static OG font from the existing OFL Golos Text subsets.

One-time tooling: fonttools==4.60.1 and brotli==1.2.0. The committed TTF is used
by Next.js; Python and FontTools are not build or runtime dependencies.
"""
from pathlib import Path
import tempfile

from fontTools.merge import Merger
from fontTools.ttLib import TTFont
from fontTools.varLib.instancer import instantiateVariableFont

fonts = Path(__file__).resolve().parents[1] / "public" / "fonts"
with tempfile.TemporaryDirectory(prefix="heartbeat-og-font-") as temporary:
    sources = []
    timestamps = []
    for language in ("latin", "cyrillic"):
        font = TTFont(fonts / f"golos-text-{language}.woff2")
        timestamps.append((font["head"].created, font["head"].modified))
        instantiateVariableFont(font, {"wght": 400}, inplace=True)
        font.flavor = None
        font.recalcTimestamp = False
        path = Path(temporary) / f"{language}.ttf"
        font.save(path)
        sources.append(str(path))
    merged = Merger().merge(sources)
    merged["head"].created = min(created for created, _ in timestamps)
    merged["head"].modified = max(modified for _, modified in timestamps)
    merged.recalcTimestamp = False
    merged.save(fonts / "golos-text-og.ttf")
