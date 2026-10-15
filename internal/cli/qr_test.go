package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The QR tests read back what was DRAWN -- the half-block text, not the
// encoder's bitmap -- and decode it with a small decoder written here,
// independently of rsc.io/qr: format information checked against its BCH
// code, the mask undone, the codewords de-interleaved and every block's
// Reed-Solomon syndromes required to be zero, then the byte-mode payload
// read out. A scanner does the same, plus error correction this perfect
// image never needs. Versions 1-6 at level M are enough for any sign-in
// URL a person would scan (up to 106 bytes); the test says so if a URL
// outgrows them.

// parseHalfBlocks turns renderQR's output back into a module grid (true is
// dark), including the quiet zone.
func parseHalfBlocks(t *testing.T, out string, color bool) [][]bool {
	t.Helper()
	out = strings.NewReplacer("\x1b[30;107m", "", "\x1b[0m", "").Replace(out)
	var grid [][]bool
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		var top, bottom []bool
		for _, r := range line {
			var tp, bt bool
			switch r {
			case '█':
				tp, bt = true, true
			case '▀':
				tp = true
			case '▄':
				bt = true
			case ' ':
			default:
				t.Fatalf("unexpected rune %q in the QR output", r)
			}
			if !color {
				// Light modules are the inked ones without colour.
				tp, bt = !tp, !bt
			}
			top, bottom = append(top, tp), append(bottom, bt)
		}
		grid = append(grid, top, bottom)
	}
	// The symbol plus quiet zone has an odd side, so the last text row's
	// lower half is not part of it.
	return grid[:len(grid[0])]
}

// stripQuietZone checks the border is blank and returns the symbol.
func stripQuietZone(t *testing.T, g [][]bool) [][]bool {
	t.Helper()
	n := len(g)
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			inside := x >= qrQuietZone && y >= qrQuietZone && x < n-qrQuietZone && y < n-qrQuietZone
			if !inside && g[y][x] {
				t.Fatalf("quiet zone module (%d,%d) is dark", x, y)
			}
		}
	}
	out := make([][]bool, n-2*qrQuietZone)
	for y := range out {
		out[y] = g[y+qrQuietZone][qrQuietZone : n-qrQuietZone]
	}
	return out
}

// Level M block structure for versions 1-6: blocks, data codewords per block,
// EC codewords per block. Every block in these versions is the same size.
var qrBlocksM = map[int][3]int{1: {1, 16, 10}, 2: {1, 28, 16}, 3: {1, 44, 26}, 4: {2, 32, 18}, 5: {2, 43, 24}, 6: {4, 27, 16}}

// GF(256) with the QR polynomial x^8+x^4+x^3+x^2+1.
var gfExp, gfLog = func() ([512]byte, [256]int) {
	var e [512]byte
	var l [256]int
	x := 1
	for i := 0; i < 255; i++ {
		e[i] = byte(x)
		l[x] = i
		x <<= 1
		if x&0x100 != 0 {
			x ^= 0x11d
		}
	}
	for i := 255; i < 512; i++ {
		e[i] = e[i-255]
	}
	return e, l
}()

func gfMul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return gfExp[gfLog[a]+gfLog[b]]
}

// decodeQR decodes a symbol (no quiet zone) to its byte-mode text.
func decodeQR(g [][]bool) (string, error) {
	size := len(g)
	version := (size - 17) / 4
	if size != 17+4*version || version < 1 {
		return "", fmt.Errorf("side %d is not a QR size", size)
	}
	blocks, ok := qrBlocksM[version]
	if !ok {
		return "", fmt.Errorf("version %d: this test decoder knows versions 1-6", version)
	}
	at := func(x, y int) uint { // column x, row y
		if g[y][x] {
			return 1
		}
		return 0
	}
	// Format information, first copy, bit 0 first.
	var format uint
	pos := [][2]int{{8, 0}, {8, 1}, {8, 2}, {8, 3}, {8, 4}, {8, 5}, {8, 7}, {8, 8}, {7, 8}, {5, 8}, {4, 8}, {3, 8}, {2, 8}, {1, 8}, {0, 8}}
	for i, p := range pos {
		format |= at(p[0], p[1]) << i
	}
	format ^= 0x5412
	data := format >> 10
	rem := data << 10
	for i := 14; i >= 10; i-- { // BCH(15,5), generator 0x537
		if rem&(1<<i) != 0 {
			rem ^= 0x537 << (i - 10)
		}
	}
	if format != data<<10|rem {
		return "", errors.New("format information fails its BCH check")
	}
	if level := data >> 3; level != 0 { // 0 is M
		return "", fmt.Errorf("error correction level bits %02b, want M (00)", level)
	}
	mask := data & 7

	function := func(x, y int) bool {
		switch {
		case x < 9 && y < 9, x >= size-8 && y < 9, x < 9 && y >= size-8, x == 6, y == 6:
			return true
		}
		last := size - 7
		return version >= 2 && x >= last-2 && x <= last+2 && y >= last-2 && y <= last+2
	}
	masked := func(x, y int) bool {
		switch mask {
		case 0:
			return (x+y)%2 == 0
		case 1:
			return y%2 == 0
		case 2:
			return x%3 == 0
		case 3:
			return (x+y)%3 == 0
		case 4:
			return (x/3+y/2)%2 == 0
		case 5:
			return x*y%2+x*y%3 == 0
		case 6:
			return (x*y%2+x*y%3)%2 == 0
		}
		return ((x+y)%2+x*y%3)%2 == 0
	}

	total := blocks[0] * (blocks[1] + blocks[2])
	raw := make([]byte, total)
	i := 0
	for right := size - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		for vert := 0; vert < size; vert++ {
			for j := 0; j < 2; j++ {
				x := right - j
				y := vert
				if (right+1)&2 == 0 {
					y = size - 1 - vert
				}
				if function(x, y) || i >= total*8 {
					continue
				}
				bit := at(x, y)
				if masked(x, y) {
					bit ^= 1
				}
				raw[i>>3] |= byte(bit) << (7 - i&7)
				i++
			}
		}
	}
	if i != total*8 {
		return "", fmt.Errorf("read %d data bits, want %d", i, total*8)
	}

	// De-interleave and check every block's syndromes.
	nb, nd, ne := blocks[0], blocks[1], blocks[2]
	var payload []byte
	for b := 0; b < nb; b++ {
		cw := make([]byte, 0, nd+ne)
		for k := 0; k < nd; k++ {
			cw = append(cw, raw[k*nb+b])
		}
		for k := 0; k < ne; k++ {
			cw = append(cw, raw[nb*nd+k*nb+b])
		}
		for r := 0; r < ne; r++ {
			var s byte
			for _, c := range cw {
				s = gfMul(s, gfExp[r]) ^ c
			}
			if s != 0 {
				return "", fmt.Errorf("block %d: Reed-Solomon syndrome %d is %#x, want 0", b, r, s)
			}
		}
		payload = append(payload, cw[:nd]...)
	}

	bitAt := 0
	read := func(n int) int {
		v := 0
		for k := 0; k < n; k++ {
			v = v<<1 | int(payload[bitAt>>3]>>(7-bitAt&7)&1)
			bitAt++
		}
		return v
	}
	if m := read(4); m != 0b0100 {
		return "", fmt.Errorf("mode %04b, want byte mode", m)
	}
	n := read(8)
	out := make([]byte, n)
	for k := range out {
		out[k] = byte(read(8))
	}
	return string(out), nil
}

func TestRenderQRRoundTrips(t *testing.T) {
	for _, url := range []string{
		"https://laptop.tail1234.ts.net",
		"https://my-workstation.tailnet-abcdef.ts.net",
		"http://100.101.102.103:8787",
		"https://a-rather-long-hostname.with-several-labels.example.com:8443",
	} {
		for _, color := range []bool{true, false} {
			var buf bytes.Buffer
			if err := renderQR(&buf, url, color); err != nil {
				t.Fatal(err)
			}
			got, err := decodeQR(stripQuietZone(t, parseHalfBlocks(t, buf.String(), color)))
			if err != nil {
				t.Fatalf("%s (color=%v): %v\n%s", url, color, err, buf.String())
			}
			if got != url {
				t.Fatalf("decoded %q, want %q", got, url)
			}
			if color && !strings.HasPrefix(buf.String(), "\x1b[30;107m") {
				t.Fatal("the coloured QR must set its own black-on-white, whatever the terminal theme")
			}
		}
	}
}

// TestQRDecoderIsNotVacuous: one flipped data module must fail the
// Reed-Solomon check, and a flipped format module the BCH check -- otherwise
// the round trip above proves nothing.
func TestQRDecoderIsNotVacuous(t *testing.T) {
	var buf bytes.Buffer
	if err := renderQR(&buf, "https://laptop.tail1234.ts.net", true); err != nil {
		t.Fatal(err)
	}
	fresh := func() [][]bool { return stripQuietZone(t, parseHalfBlocks(t, buf.String(), true)) }
	g := fresh()
	n := len(g)
	g[n-1][n-1] = !g[n-1][n-1] // the first data bit placed
	if _, err := decodeQR(g); err == nil || !strings.Contains(err.Error(), "syndrome") {
		t.Fatalf("a flipped data module: %v, want a Reed-Solomon failure", err)
	}
	g = fresh()
	g[0][8] = !g[0][8] // format bit 0
	if _, err := decodeQR(g); err == nil || !strings.Contains(err.Error(), "BCH") {
		t.Fatalf("a flipped format module: %v, want a BCH failure", err)
	}
}

// TestPairingQRNeverCarriesTheCode: the QR is the URL alone; the code is
// printed beside it, and is in no line that is part of the QR.
func TestPairingQRNeverCarriesTheCode(t *testing.T) {
	const code = "AB1CD-EF2GH"
	var buf bytes.Buffer
	exp := time.Now().Add(10 * time.Minute)
	if err := renderPairingQR(&buf, code, exp, true, []string{"https://laptop.tail1234.ts.net", "http://100.101.1.2:8787"}, false); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	var qrLines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.ContainsAny(l, "█▀▄") {
			qrLines = append(qrLines, l)
		}
	}
	got, err := decodeQR(stripQuietZone(t, parseHalfBlocks(t, strings.Join(qrLines, "\n")+"\n", false)))
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://laptop.tail1234.ts.net" {
		t.Fatalf("the QR holds %q, want the first sign-in URL and nothing else", got)
	}
	if strings.Contains(got, code) || strings.Contains(got, strings.ReplaceAll(code, "-", "")) {
		t.Fatal("the pairing code is in the QR")
	}
	for _, want := range []string{code, "not in the QR", "https://laptop.tail1234.ts.net", "also: http://100.101.1.2:8787"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// TestPairingQRWithoutRemote: no remote URL, no QR -- a loopback address on a
// phone opens the phone.
func TestPairingQRWithoutRemote(t *testing.T) {
	var buf bytes.Buffer
	if err := renderPairingQR(&buf, "AB1CD-EF2GH", time.Now().Add(time.Minute), false, nil, false); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(buf.String(), "█▀▄") || !strings.Contains(buf.String(), "reachable from this machine only") {
		t.Fatalf("output:\n%s", buf.String())
	}
}

// TestRenderQRGolden pins the exact drawing, so a change to the encoder (a
// dependency bump) or the renderer shows up as a diff a person can scan.
// `go test ./internal/cli -update-golden` rewrites it -- after scanning the
// new one with a phone.
func TestRenderQRGolden(t *testing.T) {
	var buf bytes.Buffer
	if err := renderQR(&buf, "https://laptop.tail1234.ts.net", false); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "pair-qr.golden")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run: go test ./internal/cli -update-golden)", err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("the QR drawing changed:\n%s\nwant:\n%s", buf.String(), want)
	}
}

func TestHalfBlock(t *testing.T) {
	for _, c := range []struct {
		top, bottom bool
		want        string
	}{{true, true, "█"}, {true, false, "▀"}, {false, true, "▄"}, {false, false, " "}} {
		if got := halfBlock(c.top, c.bottom); got != c.want {
			t.Errorf("halfBlock(%v,%v) = %q, want %q", c.top, c.bottom, got, c.want)
		}
	}
}
