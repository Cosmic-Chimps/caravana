package ui

import (
	"fmt"
	"html/template"
	"strconv"
	"strings"
)

// The logo mark: four travellers on one path (a disc, a quarter-disc, a diamond
// and a square). It is drawn inline, never as <img>, because an SVG in an <img>
// doesn't inherit the page's `color`, and the mark has no color of its own: it
// is currentColor, which the console sets to fg-1 (.cv-logo-mark). The shapes
// are the brand files' (static/brand/mark.svg and favicon.svg).
const (
	// markShapes is mark.svg's geometry on its 32-unit grid; markBox is the
	// pieces' bounding box, so a height is the height of the pieces.
	markShapes = `<circle cx="9.875" cy="7.5" r="5.25"/><path d="M17.125 12.75V2.25A10.5 10.5 0 0 1 27.625 12.75Z"/><path d="M9.875 14.25L15.375 19.75L9.875 25.25L4.375 19.75Z"/><rect x="17.125" y="19.25" width="10.5" height="10.5"/>`
	markBox    = "4.375 2.25 23.25 27.5"
	markBoxW   = 23.25
	markBoxH   = 27.5

	// smallShapes is the same mark hinted to whole pixels on a 16-unit grid:
	// the favicon's geometry, which the brand README says to use below 24 px.
	// The favicon draws it on a dark tile; the tile is left out here.
	smallShapes = `<circle cx="5" cy="4" r="2.5"/><path d="M8 6.5V1.5A5 5 0 0 1 13 6.5Z"/><path d="M4.75 7.5L7.5 10.25L4.75 13L2 10.25Z"/><rect x="8" y="9.5" width="5" height="5"/>`
	smallBox    = "2 1.5 11 13"
	smallBoxW   = 11.0
	smallBoxH   = 13.0

	markMinHeight  = 16 // the brand's minimum: smaller than this the mark isn't used
	markSmallBelow = 24 // under this height the hinted geometry is used
)

// brandMark returns the logo mark as an inline <svg>, height px tall (the
// height of the pieces; the width follows). The minimum is 16 px: a smaller
// height is raised to 16 rather than refused, since a template has no way to
// handle an error here. The SVG is decorative (aria-hidden): the wordmark
// beside it is the accessible name. It has no style attribute and no color.
func brandMark(height int) template.HTML {
	height = max(height, markMinHeight)
	shapes, box, boxW, boxH := markShapes, markBox, markBoxW, markBoxH
	if height < markSmallBelow {
		shapes, box, boxW, boxH = smallShapes, smallBox, smallBoxW, smallBoxH
	}
	width := trimNumber(float64(height) * boxW / boxH)
	return template.HTML(fmt.Sprintf(
		`<svg class="cv-logo-mark" xmlns="http://www.w3.org/2000/svg" viewBox="%s" width="%s" height="%d" fill="currentColor" aria-hidden="true" focusable="false">%s</svg>`,
		box, width, height, shapes))
}

// trimNumber writes f with at most two decimals and no trailing zeros.
func trimNumber(f float64) string {
	s := strconv.FormatFloat(f, 'f', 2, 64)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}
