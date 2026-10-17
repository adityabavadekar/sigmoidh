---
name: documents
aliases: pdf, docx, doc, word, report, latex
description: Produce or edit PDF and Word documents, including conversion and extraction.
when: The user wants a PDF, Word document, report, letter, resume, or edits a .docx/.pdf.
requires: pandoc, LibreOffice, or Python (python-docx, reportlab)
---

# Documents (PDF / DOCX)

Keep one canonical source (Markdown or HTML) and export from it.

## Where from

| Task | Tool |
|------|------|
| Markdown → PDF/DOCX | `pandoc doc.md -o doc.docx`; PDF needs a LaTeX engine or `--pdf-engine=weasyprint` |
| Word, scratch or edit | `python-docx` (real styles, tables, images) |
| PDF from code | `reportlab` (precise layout) or `fpdf2`; HTML→PDF via weasyprint/chromium |
| Merge, split, rotate, watermark, forms | `pikepdf`/`pypdf`, `qpdf`, or `pdftk` |
| Extract text/tables | `pdftotext -layout`; `pdfplumber` for tables |
| Convert between any office format | `soffice --headless --convert-to pdf\|docx` |

## Workflow

- Prefer Markdown/HTML → pandoc for text-first documents.
- For DOCX, use real Word styles (Heading 1/2, Quote) so navigation and TOC work —
  never fake headings with manual bold.
- Editing a .docx: modify in place, preserve existing styles; do not rebuild the file.

## Gotchas

- PDF text extraction ignores columns/reading order — use `pdfplumber` for tables.
- Scanned PDFs have no text layer: OCR with `tesseract` first.
- Embed fonts when generating PDFs or the recipient sees substitutes.

## Verify

Re-extract the text and read it back; render pages to images to check layout.
