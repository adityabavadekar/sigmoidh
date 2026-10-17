---
name: slides
aliases: pptx, powerpoint, deck, presentation
description: Build or edit a presentation end to end, from content to a polished .pptx.
when: The user asks for a deck, slides, a pitch/presentation, or a .pptx/.potx file.
requires: python-pptx or Node pptxgenjs or Marp; LibreOffice to preview
---

# Slides

Pick the approach by task, then follow the design rules — most bad decks fail
on design, not on content.

## Where from

| Task | Tool |
|------|------|
| Create a .pptx in code | `python-pptx` (Python) or `pptxgenjs` (Node) — use whichever is installed |
| Markdown-first, fastest clean result | `marp deck.md -o deck.pptx` (or reveal.js for HTML) |
| Edit an existing deck or a template | unzip → edit `ppt/slides/slideN.xml` → rezip; or `python-pptx` for text swaps |
| Read/extract | `markitdown deck.pptx` (one block per slide) |

Check availability first: `python3 -c "import pptx"` or `node -e "require('pptxgenjs')"`.

## Design rules

- Choose a **topic-specific palette**: one dominant colour (60-70%), 1-2 supports,
  one sharp accent. Do not default to blue. Dark title/closing, light content.
- **Every slide needs a visual** — image, icon, chart, or shape. No text-only slides.
- Sizes: title 36-44pt bold, body 14-16pt, captions 10-12pt. Left-align body; centre only titles.
- 0.5" margins, 0.3-0.5" gaps. Prefer safe fonts (Arial, Calibri, Cambria, Times New Roman).
- Avoid: accent lines under titles, colour bars/stripes, cream/beige backgrounds,
  repeating one layout on every slide. Use native charts, not chart images.

## Gotchas

- `python-pptx`: cannot duplicate a slide (use `add_slide(layout)` + copy shapes);
  assigning `text_frame.text` collapses formatting (set `run.text`); `add_picture`
  cannot read SVG/EMF.
- `pptxgenjs`: set `pres.layout` before adding slides; hex colours without `#`;
  never reuse an options object across calls; use `bullet: true`, never a literal `•`.
- Rezip from **inside** the unpacked dir and `rm -f out.pptx` first, or stale parts survive.

## Verify

Convert to images and actually look at them:

```bash
soffice --headless --convert-to pdf deck.pptx
pdftoppm -jpeg -r 150 deck.pdf slide
```

Check for text overflow, overlaps, gaps under 0.3", and leftover placeholder text.
