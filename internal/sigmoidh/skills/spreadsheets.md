---
name: spreadsheets
aliases: xlsx, excel, csv, sheet, workbook
description: Create, edit, format, or analyze spreadsheets and tabular data.
when: A spreadsheet is the deliverable or the input (.xlsx/.xlsm/.csv/.tsv).
requires: Python openpyxl or pandas; LibreOffice to recalculate
---

# Spreadsheets

## Where from

| Task | Tool |
|------|------|
| Create/edit with formulas and formatting | `openpyxl` |
| Bulk data in or out | `pandas.read_excel` / `to_excel` |
| Quick look | `markitdown file.xlsx` (no cell coordinates — not for planning edits) |
| Recalculate | LibreOffice (see below) |

## Rules for every output

- Professional font throughout; **use formulas, never hardcoded results** (`=SUM(B2:B9)`).
- Follow the user's spec literally: exact sheet names, headers, and formulas.
- Document every assumption/hardcoded number next to it.

## Recalculate (mandatory when the file has formulas)

openpyxl writes formula strings with **no cached values**, so readers see `None`.
Recalculate before anything reads the values:

```bash
soffice --headless --convert-to xlsx --outdir /tmp out.xlsx
```

## Gotchas

- `load_workbook(data_only=True)` **destroys formulas if you save** — read and write in separate passes.
- Merged cells: write the top-left anchor only.
- `.xlsm` needs `keep_vba=True`; a sheet name with a space needs quotes (`='My Sheet'!$B$5`).
- Avoid `XLOOKUP`/`FILTER`/`SORT`/`UNIQUE`; prefer `SUMIFS`/`INDEX`/`MATCH`/`IFERROR`.

## Financial models

Blue text for inputs, black for formulas, green for cross-sheet links, red for cross-file
links, yellow fill for cells the user fills in. Currency `$#,##0`, percents `0.0%`
stored as fractions. Every assumption in its own labelled cell, referenced by formulas.

## Verify

Recalculate, confirm zero formula errors, then re-read the computed values.
