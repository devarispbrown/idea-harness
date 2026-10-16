package cli

import (
	"fmt"
	"io"
	"strings"

	"rsc.io/qr"
)

// `idea ui pair --qr`: the sign-in address as a terminal QR code, so a phone
// can open it with its camera instead of someone typing a tailnet hostname.
//
// What goes in the code is the URL and nothing else. The pairing code never
// does: codes never travel in URLs (no history, log or Referer holds one --
// docs/security-model.md "Remote access"), and a QR is a URL a camera app
// opens, previews and may sync to the cloud. The code is printed beside it,
// to be read and typed.
//
// The encoder is rsc.io/qr (Russ Cox; BSD-3-Clause, the Go authors' licence;
// pure Go, no cgo -- its libqrencode subpackage is not imported -- and about
// a thousand lines across qr, qr/coding and qr/gf256). Writing one here would
// mean Reed-Solomon over GF(256), the version/capacity tables and the
// placement rules: a few hundred lines of exactly the kind of code that is
// easy to get subtly wrong and hard to review, for a feature whose only job
// is to be scannable. Its one shortcut -- it always applies mask pattern 0
// instead of scoring all eight -- is allowed by the standard (the mask choice
// only optimises module balance) and costs nothing at these sizes.

// qrQuietZone is the blank border, in modules, the QR standard requires
// around the symbol; scanners find the finder patterns by it.
const qrQuietZone = 4

// renderQR writes text as a QR code made of UTF-8 half blocks: each character
// cell is two modules stacked, so the symbol is roughly square in a terminal
// whose cells are about twice as tall as they are wide.
//
// With colour, every line is drawn black-on-white explicitly, so the code
// scans on light and dark terminal themes alike. Without colour (not a
// terminal) there is no way to choose the background, so the
// blocks draw the LIGHT modules: correct on the dark-background terminals
// most people use, and inverted (which many, not all, scanners accept) on
// a light one. A scanner that refuses it still has the URL printed below.
func renderQR(w io.Writer, text string, color bool) error {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return err
	}
	n := code.Size + 2*qrQuietZone
	dark := func(x, y int) bool {
		x, y = x-qrQuietZone, y-qrQuietZone
		return x >= 0 && y >= 0 && x < code.Size && y < code.Size && code.Black(x, y)
	}
	for y := 0; y < n; y += 2 {
		var b strings.Builder
		if color {
			b.WriteString("\x1b[30;107m") // black on bright white
		}
		for x := 0; x < n; x++ {
			top, bottom := dark(x, y), y+1 < n && dark(x, y+1)
			if !color {
				// Draw the light modules instead (see above).
				top, bottom = !top, !(y+1 < n && dark(x, y+1)) && y+1 < n
			}
			b.WriteString(halfBlock(top, bottom))
		}
		if color {
			b.WriteString("\x1b[0m")
		}
		if _, err := fmt.Fprintln(w, b.String()); err != nil {
			return err
		}
	}
	return nil
}

// halfBlock is the cell whose upper half is inked when top and whose lower
// half is inked when bottom.
func halfBlock(top, bottom bool) string {
	switch {
	case top && bottom:
		return "█"
	case top:
		return "▀"
	case bottom:
		return "▄"
	}
	return " "
}
