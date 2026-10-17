---
name: images
aliases: figure, chart, matplotlib, svg, png, plot
description: Generate charts, figures, icons, and processed images.
when: The user wants a plot, chart, figure, icon, or image manipulation.
requires: Python matplotlib or Pillow; optional cairosvg, imagemagick
---

# Images & plots

## Where from

| Task | Tool |
|------|------|
| Charts / any numeric plot | `matplotlib` (seaborn for styling) |
| Compose, annotate, resize, crop | Pillow (PIL) |
| Vector art / logos | hand-written SVG; convert with `cairosvg` or `rsvg-convert` |
| Format conversion, montage | ImageMagick (`convert`, `montage`) |

## Workflow

- Pick the format to the target: **SVG** for logos/diagrams (scales), **PNG** 150-300 dpi
  for documents, **JPEG** for photos.
- Size the canvas to the destination; use a colourblind-safe palette; label axes with
  units; add a legend when there is more than one series.

## Gotchas

- matplotlib's default DPI is low — always pass `dpi=150`+ and
  `bbox_inches="tight"` so labels are not clipped.
- Special glyphs/emoji may be missing from the environment's fonts.
- JPEG has no alpha — flatten RGBA first.

## Verify

Open the produced image and check for clipped labels, unreadable small text, and low contrast.
