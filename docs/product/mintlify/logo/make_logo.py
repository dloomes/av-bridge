#!/usr/bin/env python3
"""Generate the M.A.R.C.U.S. logo files for the Mintlify site.

Outputs (next to this script):
  marcus-light.svg   ring + indigo wordmark, for light mode
  marcus-dark.svg    ring + white wordmark, for dark mode
  favicon.svg        ring only
  favicon.png        ring only, 256x256

The ring is the official artwork (docs/product/involve-icon.png) embedded as
PNG so it matches exactly. The wordmark is converted to vector outlines from
Segoe UI Bold, so it renders identically everywhere — an SVG loaded through
<img>, as Mintlify does, can't use web fonts, and live <text> would fall back
to whatever font the visitor happens to have.

Requires Pillow and fontTools. Re-run after changing the colours or text:
  python docs/product/mintlify/logo/make_logo.py
"""
from __future__ import annotations

import base64
import io
from pathlib import Path

from fontTools.pens.svgPathPen import SVGPathPen
from fontTools.ttLib import TTFont
from PIL import Image

HERE = Path(__file__).resolve().parent
ICON_SRC = HERE.parent.parent / "involve-icon.png"
FONT = Path("C:/Windows/Fonts/segoeuib.ttf")

TEXT = "M.A.R.C.U.S."
INDIGO = "#2A1B5E"  # brand indigo, light mode
WHITE = "#FFFFFF"   # dark mode

# Layout, in SVG user units. The navbar scales the whole logo to its height,
# so only the proportions matter.
ICON = 64          # ring height/width
GAP = 14           # ring to wordmark
CAP = 29           # wordmark cap height
TRACKING = 0.02    # extra letter spacing, as a fraction of the em
PAD = 2            # breathing room so anti-aliasing isn't clipped


def ring_png(size: int) -> bytes:
    """Official ring, cropped to its artwork and resized to size x size."""
    im = Image.open(ICON_SRC).convert("RGBA")
    im = im.crop(im.getbbox())
    side = max(im.size)
    square = Image.new("RGBA", (side, side), (0, 0, 0, 0))
    square.paste(im, ((side - im.width) // 2, (side - im.height) // 2))
    square = square.resize((size, size), Image.LANCZOS)
    buf = io.BytesIO()
    square.save(buf, "PNG", optimize=True)
    return buf.getvalue()


def ring_image(x: float, y: float, size: float, png: bytes) -> str:
    data = base64.b64encode(png).decode("ascii")
    return (f'<image x="{x:g}" y="{y:g}" width="{size:g}" height="{size:g}" '
            f'href="data:image/png;base64,{data}"/>')


def wordmark_paths(font: TTFont, text: str, x0: float, baseline: float, fill: str) -> tuple[str, float]:
    """Outlined text as one <g>; returns (svg, advance width in user units)."""
    upm = font["head"].unitsPerEm
    cap = getattr(font["OS/2"], "sCapHeight", 0) or int(upm * 0.7)
    scale = CAP / cap
    glyphs = font.getGlyphSet()
    cmap = font.getBestCmap()
    hmtx = font["hmtx"]
    pen_x = 0.0
    parts = []
    for ch in text:
        name = cmap[ord(ch)]
        pen = SVGPathPen(glyphs)
        glyphs[name].draw(pen)
        d = pen.getCommands()
        if d:
            parts.append(f'<path transform="translate({pen_x:g} 0)" d="{d}"/>')
        pen_x += hmtx[name][0] + TRACKING * upm
    pen_x -= TRACKING * upm  # no tracking after the last glyph
    width = pen_x * scale
    g = (f'<g fill="{fill}" transform="translate({x0:g} {baseline:g}) scale({scale:.6f} {-scale:.6f})">'
         + "".join(parts) + "</g>")
    return g, width


def logo_svg(fill: str, font: TTFont, png: bytes) -> str:
    height = ICON + 2 * PAD
    text_x = PAD + ICON + GAP
    baseline = PAD + ICON / 2 + CAP / 2  # centre the caps on the ring
    words, text_w = wordmark_paths(font, TEXT, text_x, baseline, fill)
    width = text_x + text_w + PAD
    return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {width:.2f} {height:g}" '
            f'width="{width:.2f}" height="{height:g}" role="img" aria-labelledby="t">'
            f'<title id="t">M.A.R.C.U.S.</title>'
            + ring_image(PAD, PAD, ICON, png) + words + "</svg>\n")


def main() -> None:
    font = TTFont(FONT)
    png = ring_png(192)  # 3x the navbar's typical 64px-equivalent: crisp on retina
    (HERE / "marcus-light.svg").write_text(logo_svg(INDIGO, font, png), encoding="utf-8")
    (HERE / "marcus-dark.svg").write_text(logo_svg(WHITE, font, png), encoding="utf-8")

    fav = ring_png(256)
    (HERE / "favicon.png").write_bytes(fav)
    (HERE / "favicon.svg").write_text(
        '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 256 256" width="256" height="256">'
        + ring_image(0, 0, 256, fav) + "</svg>\n", encoding="utf-8")
    for f in ("marcus-light.svg", "marcus-dark.svg", "favicon.svg", "favicon.png"):
        print(f"wrote {HERE / f}  ({(HERE / f).stat().st_size // 1024} KB)")


if __name__ == "__main__":
    main()
