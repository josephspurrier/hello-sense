#!/usr/bin/env python3
"""Draw the insight card banners that Hello's S3 bucket used to serve.

The original artwork is gone. It lived in `hello-data/insights_images/` and that
bucket is private now; the search for a surviving copy is written up in
knowledgebase/CONSOLIDATION-PLAN.md, "The insight card art is gone". So these
are replacements, not restorations, and they are deliberately abstract: a wrong
photograph would look like a bug, whereas a coloured field reads as a design.

Output goes to orb/internal/api/insightart/ and is embedded in the binary, so a
deploy stays one file and the app never depends on anyone else's host again.

Run:  python3 generate.py            (needs Pillow)

Naming is the reference's convention and is load-bearing: the app asks for
phone_1x/phone_2x/phone_3x and orb derives all three from the lowercased
category, so `wake_variance.png`, `wake_variance@2x.png`, `wake_variance@3x.png`.
"""

import math
import os

from PIL import Image, ImageDraw, ImageFilter

# The cell crops to 398x132 and the image view inside it is 162 tall, anchored
# 15 above, so the art is seen through a window shorter than itself and scaled
# with aspectFill. Only the ratio really matters; the extra width covers the
# widest iPhone without upscaling.
BASE_W, BASE_H = 450, 176
SCALES = (1, 2, 3)

OUT = os.path.join(
    os.path.dirname(os.path.abspath(__file__)),
    "..", "..", "internal", "api", "insightart",
)

# Two stops per category, dark to saturated, chosen so the banner sits happily
# under both themes: it is its own block with the text below it, not a
# background, so it does not have to match either one.
PALETTES = {
    "air_quality":    ("#0B3B2E", "#2FA36B"),
    "generic":        ("#1B2437", "#46608C"),
    "humidity":       ("#06303F", "#2E93B8"),
    "light":          ("#3A2A06", "#D9A23B"),
    "partner_motion": ("#3A1030", "#C2557F"),
    "sleep_duration": ("#0E1A3C", "#3E5FC1"),
    "sleep_hygiene":  ("#062F31", "#2FA0A0"),
    "sleep_quality":  ("#1B1040", "#6A4FD0"),
    "sound":          ("#2B0A38", "#9B4FD0"),
    "temperature":    ("#3B1206", "#D9603B"),
    "wake_variance":  ("#3B1A06", "#E8913B"),
}

# Per-category wave shaping, so the eleven are recognisably a set without being
# the same picture in eleven colours. (amplitude, cycles, phase)
WAVES = {
    "air_quality":    [(0.10, 1.4, 0.0), (0.07, 2.2, 1.1), (0.05, 3.1, 2.4)],
    "generic":        [(0.09, 1.0, 0.3), (0.06, 1.8, 1.9)],
    "humidity":       [(0.12, 1.2, 0.0), (0.08, 2.0, 0.9), (0.05, 3.4, 2.0)],
    "light":          [(0.07, 0.8, 0.4), (0.05, 1.6, 1.6)],
    "partner_motion": [(0.11, 1.6, 0.0), (0.11, 1.6, math.pi)],
    "sleep_duration": [(0.08, 0.6, 0.2), (0.06, 1.2, 1.4), (0.04, 2.0, 2.7)],
    "sleep_hygiene":  [(0.09, 1.3, 0.5), (0.06, 2.6, 1.7)],
    "sleep_quality":  [(0.10, 0.9, 0.1), (0.07, 1.7, 1.2), (0.05, 2.6, 2.3)],
    "sound":          [(0.14, 2.4, 0.0), (0.10, 3.6, 1.0), (0.06, 5.0, 2.1)],
    "temperature":    [(0.08, 1.1, 0.6), (0.06, 2.1, 1.8)],
    "wake_variance":  [(0.13, 0.7, 0.0), (0.06, 2.9, 1.5), (0.04, 4.2, 2.8)],
}


def rgb(h):
    h = h.lstrip("#")
    return tuple(int(h[i:i + 2], 16) for i in (0, 2, 4))


def gradient(w, h, top, bottom):
    """Diagonal two-stop gradient.

    Drawn per row on a tall thin strip and then resized, which is both faster
    than per-pixel and gives free smoothing. The shear comes from mixing the
    x position into the interpolation afterwards.
    """
    base = Image.new("RGB", (1, h))
    px = base.load()
    for y in range(h):
        t = y / max(h - 1, 1)
        px[0, y] = tuple(round(top[i] + (bottom[i] - top[i]) * t) for i in range(3))
    base = base.resize((w, h))

    # Shear: blend with a horizontal version of the same ramp so the light
    # corner ends up bottom-right rather than straight down.
    side = Image.new("RGB", (w, 1))
    spx = side.load()
    for x in range(w):
        t = x / max(w - 1, 1)
        spx[x, 0] = tuple(round(top[i] + (bottom[i] - top[i]) * t) for i in range(3))
    side = side.resize((w, h))
    return Image.blend(base, side, 0.35)


def glow(w, h):
    """Soft off-centre highlight, as a white alpha mask."""
    m = Image.new("L", (w, h), 0)
    d = ImageDraw.Draw(m)
    cx, cy = int(w * 0.72), int(h * 0.30)
    r = int(h * 0.95)
    # Concentric discs of increasing brightness approximate a radial falloff
    # closely enough once blurred, and avoid a per-pixel loop.
    steps = 24
    for i in range(steps):
        t = i / (steps - 1)
        rr = int(r * (1 - t))
        v = int(90 * (t ** 2))
        d.ellipse((cx - rr, cy - rr, cx + rr, cy + rr), fill=v)
    return m.filter(ImageFilter.GaussianBlur(radius=h * 0.18))


def waves(w, h, spec):
    """Stacked sine bands, filled downward, very low alpha."""
    layer = Image.new("L", (w, h), 0)
    d = ImageDraw.Draw(layer)
    for idx, (amp, cycles, phase) in enumerate(spec):
        mid = h * (0.52 + 0.10 * idx)
        pts = []
        for x in range(w + 1):
            t = x / w
            y = mid + math.sin(t * cycles * 2 * math.pi + phase) * (h * amp)
            pts.append((x, y))
        pts += [(w, h), (0, h)]
        d.polygon(pts, fill=22 + 8 * idx)
    return layer.filter(ImageFilter.GaussianBlur(radius=max(1, h * 0.012)))


def vignette(w, h):
    m = Image.new("L", (w, h), 0)
    d = ImageDraw.Draw(m)
    inset = int(min(w, h) * 0.02)
    d.rectangle((inset, inset, w - inset, h - inset), fill=255)
    m = m.filter(ImageFilter.GaussianBlur(radius=min(w, h) * 0.16))
    return Image.eval(m, lambda v: 255 - v)


def render(name, w, h):
    top, bottom = (rgb(c) for c in PALETTES[name])
    img = gradient(w, h, top, bottom).convert("RGB")

    white = Image.new("RGB", (w, h), (255, 255, 255))
    img = Image.composite(white, img, glow(w, h).point(lambda v: v))
    img = Image.blend(img, Image.new("RGB", (w, h), bottom), 0.0)

    # Waves lighten rather than tint, so each category keeps its own hue.
    img = Image.composite(white, img, waves(w, h, WAVES[name]))

    black = Image.new("RGB", (w, h), (0, 0, 0))
    img = Image.composite(black, img, vignette(w, h).point(lambda v: int(v * 0.45)))
    return img


def main():
    os.makedirs(OUT, exist_ok=True)
    written = 0
    for name in sorted(PALETTES):
        for s in SCALES:
            suffix = "" if s == 1 else "@%dx" % s
            path = os.path.join(OUT, "%s%s.png" % (name, suffix))
            render(name, BASE_W * s, BASE_H * s).save(path, "PNG", optimize=True)
            written += 1
            print("%-28s %5dx%-5d %6.1f KB" % (
                os.path.basename(path), BASE_W * s, BASE_H * s,
                os.path.getsize(path) / 1024.0))
    print("\n%d files -> %s" % (written, os.path.normpath(OUT)))


if __name__ == "__main__":
    main()
