package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// TUI palette — mission-control neon ink on deep space (docs/TUI_DESIGN.md).
type tuiPalette struct {
	void   tcell.Color // app background
	panel  tcell.Color // widget backgrounds
	chrome tcell.Color // borders, rules, graphics
	focus  tcell.Color // focused border, titles, cyan accents
	brand  tcell.Color // ✻ logo, thinking markers
	mint   tcell.Color // success, results, live dot
	amber  tcell.Color // warnings, retries, read-only badge
	alert  tcell.Color // errors, destructive
	violet tcell.Color // streamed reasoning body
	text   tcell.Color // primary foreground
	dim    tcell.Color // secondary / meta
}

var pal = tuiPalette{
	void:   hexColor("#080B14"),
	panel:  hexColor("#0D121E"),
	chrome: hexColor("#3E5480"),
	focus:  hexColor("#5FD7FF"),
	brand:  hexColor("#FF5FD7"),
	mint:   hexColor("#5FFF87"),
	amber:  hexColor("#FFD75F"),
	alert:  hexColor("#FF6B6B"),
	violet: hexColor("#9D8CFF"),
	text:   hexColor("#C7D0DC"),
	dim:    hexColor("#5C6A7D"),
}

func hexColor(hex string) tcell.Color {
	v, err := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	if err != nil {
		return tcell.ColorDefault
	}
	return tcell.NewHexColor(int32(v))
}

// applyTheme installs the palette and heavy box-drawing chrome globally.
// Call once before any widget is created; every pane then inherits it.
func applyTheme() {
	tview.Styles.PrimitiveBackgroundColor = pal.void
	tview.Styles.ContrastBackgroundColor = pal.panel
	tview.Styles.MoreContrastBackgroundColor = pal.chrome
	tview.Styles.BorderColor = pal.chrome
	tview.Styles.TitleColor = pal.focus
	tview.Styles.GraphicsColor = pal.chrome
	tview.Styles.PrimaryTextColor = pal.text
	tview.Styles.SecondaryTextColor = pal.dim
	tview.Styles.TertiaryTextColor = pal.focus
	tview.Styles.InverseTextColor = pal.void
	tview.Styles.ContrastSecondaryTextColor = pal.dim

	// Thin rounded chrome everywhere — one border language, modern and calm.
	// Focus keeps the same geometry; emphasis comes from color, not weight.
	b := &tview.Borders
	b.Horizontal, b.Vertical = '─', '│'
	b.TopLeft, b.TopRight, b.BottomLeft, b.BottomRight = '╭', '╮', '╰', '╯'
	b.LeftT, b.RightT, b.TopT, b.BottomT, b.Cross = '├', '┤', '┬', '┴', '┼'
	focus := &tview.Borders
	focus.HorizontalFocus, focus.VerticalFocus = '─', '│'
	focus.TopLeftFocus, focus.TopRightFocus = '╭', '╮'
	focus.BottomLeftFocus, focus.BottomRightFocus = '╰', '╯'
}

// dynamic-color tag helpers (tview "[fg:bg:attrs]" grammar)
const tagReset = "[-:-:-]"

// tg returns a foreground tag for the palette color, e.g. "[#5FD7FF]".
func tg(c tcell.Color) string { return fmt.Sprintf("[#%06x]", c.Hex()) }

// tgOn returns a foreground+background tag for full-width bands.
func tgOn(fgC, bg tcell.Color) string {
	return fmt.Sprintf("[#%06x:#%06x]", fgC.Hex(), bg.Hex())
}

// tgBold returns a bold attribute tag preserving current colors.
const tgBold = "[::b]"
